package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/macula-io/macula-go/frame"

	"github.com/macula-io/macula-cli/internal/daemon"
	"github.com/macula-io/macula-cli/internal/report"
	"github.com/macula-io/macula-cli/internal/wirevalue"
)

func runPubsub(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: macula-cli pubsub watch|publish|subscribe|unsubscribe [flags] ...")
		return 2
	}
	switch args[0] {
	case "watch":
		return runPubsubWatch(args[1:])
	case "publish":
		return runPubsubPublish(args[1:])
	case "subscribe":
		return runPubsubSubscribe(args[1:])
	case "unsubscribe":
		return runPubsubUnsubscribe(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "macula-cli pubsub: unknown subcommand %q (want watch, publish, subscribe, or unsubscribe)\n", args[0])
		return 2
	}
}

type publishResult struct {
	Topic      string `json:"topic"`
	Seq        uint64 `json:"seq"`
	DurationMs int64  `json:"duration_ms"`
}

// runPubsubPublish is a one-shot PUBLISH: connect, publish, close. No
// standing session, no delivery confirmation beyond the wire send
// succeeding (PUBLISH has no ack in this protocol) -- same one-shot
// shape as every other command in this repo, deliberately not a
// long-lived publisher process.
func runPubsubPublish(args []string) int {
	fs := flag.NewFlagSet("pubsub publish", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	realmHex := fs.String("realm", "", "32-byte realm as hex (default: all-zero realm)")
	payloadJSON := fs.String("payload", "null", "event payload as a JSON document")
	connectTimeout := fs.Duration("connect-timeout", 15*time.Second, "connect timeout")
	var seeds seedFlag
	fs.Var(&seeds, "seed", "additional fallback station host[:port], tried in order after <host> if it doesn't answer; repeat for more than one")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli pubsub publish [flags] <host[:port]> <topic>\n\n"+
			"Publishes one event to a topic and exits -- no standing connection, no\n"+
			"subscriber confirmation (PUBLISH has no ack on this wire protocol).\n\n"+
			"With -seed, falls back to additional stations in order if <host> doesn't\n"+
			"answer.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return 2
	}

	resolvedSeeds, err := resolveSeeds(fs.Arg(0), seeds)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	topic := fs.Arg(1)

	realm, err := parseRealm(*realmHex)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	payload, err := wirevalue.FromJSON([]byte(*payloadJSON))
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	id, generated, err := loadIdentity(*identityPath)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	if generated && !*jsonOut {
		fmt.Println("(generated a new identity — puzzle grinding took a moment)")
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), *connectTimeout)
	defer cancel()
	session, err := dialSeeds(ctx, resolvedSeeds, id)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	defer session.Close("normal", nil, id)

	// No persisted sequence counter (this process doesn't live between
	// invocations) -- current-time-millis is monotonic enough across
	// separate one-shot runs without over-engineering real state.
	seq := uint64(time.Now().UnixMilli())
	spec := frame.NewPublishSpec(topic, realm, id.NodeID(), seq, payload, time.Now().UnixMilli())
	if err := session.Publish(spec, id); err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	result := publishResult{Topic: topic, Seq: seq, DurationMs: time.Since(start).Milliseconds()}
	report.Ok(*jsonOut, result, func() {
		fmt.Printf("published to %q (seq=%d, %d ms)\n", topic, seq, result.DurationMs)
	})
	return 0
}

type pubsubEvent struct {
	Topic        string `json:"topic"`
	Publisher    string `json:"publisher"`
	Seq          uint64 `json:"seq"`
	Payload      any    `json:"payload"`
	DeliveredVia string `json:"delivered_via"`
	ReceivedAt   string `json:"received_at"`
}

func runPubsubWatch(args []string) int {
	fs := flag.NewFlagSet("pubsub watch", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit one JSON object per line as events arrive")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir)")
	realmHex := fs.String("realm", "", "32-byte realm as hex (default: all-zero realm)")
	count := fs.Int("count", 0, "stop after this many events (0 = unbounded, until --duration or Ctrl-C)")
	duration := fs.Duration("duration", 0, "stop watching after this long (0 = unbounded)")
	pollTimeout := fs.Duration("poll-timeout", 30*time.Second, "how long to wait for each next event before re-polling")
	connectTimeout := fs.Duration("connect-timeout", 15*time.Second, "connect timeout")
	viaDaemon := fs.Bool("daemon", false, "tap into a running \"macula-cli daemon\"'s subscription instead of subscribing here -- persists past this command's own exit, takes no <host[:port]>")
	socketName := fs.String("socket-name", daemon.DefaultName, "with -daemon, the target daemon instance's -socket-name")
	socketPath := fs.String("socket", "", "with -daemon, control socket path (default: derived from -socket-name)")
	var seeds seedFlag
	fs.Var(&seeds, "seed", "additional fallback station host[:port], tried in order after <host> if it doesn't answer; repeat for more than one")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli pubsub watch [flags] <host[:port]> <topic>\n"+
			"       macula-cli pubsub watch -daemon [flags] <topic>\n\n"+
			"Subscribes and prints events as they arrive. Stops on --count, --duration,\n"+
			"or Ctrl-C, whichever comes first.\n\n"+
			"With -daemon, taps into a running daemon's subscription instead (creating it\n"+
			"first if it doesn't already exist) -- the subscription itself is daemon-owned\n"+
			"and keeps running after this command exits; use \"pubsub unsubscribe -daemon\"\n"+
			"to actually end it. Takes no <host[:port]> (the daemon already has one), and\n"+
			"-count/-duration/Ctrl-C stop only THIS command's own tap, not the\n"+
			"subscription.\n\n"+
			"With -seed (non-daemon mode only), falls back to additional stations in\n"+
			"order if <host> doesn't answer.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *viaDaemon {
		if fs.NArg() != 1 {
			fs.Usage()
			return 2
		}
		return runPubsubWatchDaemon(pubsubWatchDaemonArgs{
			jsonOut:    *jsonOut,
			topic:      fs.Arg(0),
			socketName: *socketName,
			socketPath: *socketPath,
			realmHex:   *realmHex,
			count:      *count,
			duration:   *duration,
		})
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return 2
	}

	resolvedSeeds, err := resolveSeeds(fs.Arg(0), seeds)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	topic := fs.Arg(1)

	realm, err := parseRealm(*realmHex)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}

	id, generated, err := loadIdentity(*identityPath)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	if generated && !*jsonOut {
		fmt.Println("(generated a new identity — puzzle grinding took a moment)")
	}

	connectCtx, connectCancel := context.WithTimeout(context.Background(), *connectTimeout)
	defer connectCancel()
	session, err := dialSeeds(connectCtx, resolvedSeeds, id)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	defer session.Close("normal", nil, id)

	subSpec := frame.NewSubscribeSpec(topic, realm, id.NodeID())
	if err := session.Subscribe(subSpec, id); err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	defer session.Unsubscribe(frame.NewUnsubscribeSpec(topic, realm, id.NodeID()), id)

	if !*jsonOut {
		fmt.Fprintf(os.Stderr, "watching %q on %s (Ctrl-C to stop)\n", topic, session.RemoteAddr())
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	var deadline <-chan time.Time
	if *duration > 0 {
		timer := time.NewTimer(*duration)
		defer timer.Stop()
		deadline = timer.C
	}

	received := 0
	for {
		select {
		case <-sigCh:
			return finishWatch(*jsonOut, received)
		case <-deadline:
			return finishWatch(*jsonOut, received)
		default:
		}

		evt, err := session.RecvEvent(*pollTimeout)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue // just a poll timeout, keep watching
			}
			if errors.Is(err, frame.ErrNotAnEventFrame) {
				// The control stream isn't event-exclusive: a session
				// can receive unsolicited non-EVENT frames on it (e.g.
				// built-in advertise gossip for _content.* procedures,
				// found live 2026-08-29). RecvFrame already consumed
				// it, so just keep waiting for the next one.
				continue
			}
			return report.Fail(*jsonOut, err, nil)
		}

		out := pubsubEvent{
			Topic:        evt.Topic,
			Publisher:    hex.EncodeToString(evt.Publisher),
			Seq:          evt.Seq,
			Payload:      wirevalue.ToJSON(evt.Payload),
			DeliveredVia: evt.DeliveredVia,
			ReceivedAt:   time.Now().UTC().Format(time.RFC3339Nano),
		}
		printPubsubEvent(*jsonOut, out)
		received++
		if *count > 0 && received >= *count {
			return finishWatch(*jsonOut, received)
		}
	}
}

// printPubsubEvent is the one place "pubsub watch" formats an event,
// shared by the direct-session loop above and the daemon-tap loop
// below so both modes print byte-for-byte the same shape.
func printPubsubEvent(jsonOut bool, out pubsubEvent) {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		_ = enc.Encode(out)
	} else {
		fmt.Printf("[%s] seq=%d via=%s from=%s %v\n",
			out.ReceivedAt, out.Seq, out.DeliveredVia, out.Publisher, out.Payload)
	}
}

func finishWatch(jsonOut bool, received int) int {
	if !jsonOut {
		fmt.Fprintf(os.Stderr, "(stopped, %d event(s) received)\n", received)
	}
	return 0
}

// pubsubWatchDaemonArgs carries "pubsub watch -daemon"'s already-parsed
// flags into runPubsubWatchDaemon, mirroring serveDaemonArgs's own
// reasoning for the same shape of split.
type pubsubWatchDaemonArgs struct {
	jsonOut    bool
	topic      string
	socketName string
	socketPath string
	realmHex   string
	count      int
	duration   time.Duration
}

// runPubsubWatchDaemon taps into a daemon-owned subscription instead
// of subscribing here -- the subscription itself outlives this
// command; -count/-duration/Ctrl-C stop only this tap.
func runPubsubWatchDaemon(a pubsubWatchDaemonArgs) int {
	sockPath, err := resolveSocketPath(a.socketPath, a.socketName)
	if err != nil {
		return report.Fail(a.jsonOut, err, nil)
	}

	params := daemon.PubsubWatchParams{Topic: a.topic, RealmHex: a.realmHex}
	w, err := daemon.Watch(sockPath, daemon.MethodPubsubWatch, params)
	if err != nil {
		return report.Fail(a.jsonOut, err, nil)
	}
	defer w.Close()

	if !a.jsonOut {
		fmt.Fprintf(os.Stderr, "watching %q via daemon at %s (Ctrl-C to stop; the daemon's own subscription keeps running)\n", a.topic, sockPath)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	var deadline <-chan time.Time
	if a.duration > 0 {
		timer := time.NewTimer(a.duration)
		defer timer.Stop()
		deadline = timer.C
	}

	// Next() blocks on the socket, so it runs on its own goroutine and
	// feeds a channel the select below can race against sigCh/deadline
	// -- the same shape the direct-session loop gets for free from
	// RecvEvent's own bounded poll-timeout, just adapted for a
	// connection read that has no equivalent timeout parameter.
	notifications := make(chan daemon.Notification)
	watchErrCh := make(chan error, 1)
	go func() {
		for {
			n, err := w.Next()
			if err != nil {
				watchErrCh <- err
				return
			}
			notifications <- n
		}
	}()

	received := 0
	for {
		select {
		case <-sigCh:
			return finishWatch(a.jsonOut, received)
		case <-deadline:
			return finishWatch(a.jsonOut, received)
		case err := <-watchErrCh:
			if !a.jsonOut {
				fmt.Fprintf(os.Stderr, "(subscription ended: %v)\n", err)
			}
			return finishWatch(a.jsonOut, received)
		case n := <-notifications:
			if n.Method != daemon.MethodPubsubEvent {
				continue // not expected on this connection, but skip rather than fail
			}
			var out pubsubEvent
			if err := json.Unmarshal(n.Params, &out); err != nil {
				continue
			}
			printPubsubEvent(a.jsonOut, out)
			received++
			if a.count > 0 && received >= a.count {
				return finishWatch(a.jsonOut, received)
			}
		}
	}
}

// pubsubDaemonArgs carries "pubsub subscribe/unsubscribe -daemon"'s
// already-parsed flags -- both are daemon-only (no non-daemon form
// makes sense for either), so unlike watch's own args struct this has
// no -daemon toggle to track.
type pubsubDaemonArgs struct {
	jsonOut    bool
	topic      string
	socketName string
	socketPath string
	realmHex   string
}

func parsePubsubDaemonArgs(cmdName string, args []string) (pubsubDaemonArgs, int, bool) {
	fs := flag.NewFlagSet(cmdName, flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	realmHex := fs.String("realm", "", "32-byte realm as hex (default: all-zero realm)")
	socketName := fs.String("socket-name", daemon.DefaultName, "the target daemon instance's -socket-name")
	socketPath := fs.String("socket", "", "control socket path (default: derived from -socket-name)")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: macula-cli %s [flags] <topic>\n\n"+
			"Always talks to a running \"macula-cli daemon\" -- there is no non-daemon\n"+
			"form of this command, unlike \"serve\" or \"pubsub watch\".\n\nFlags:\n", cmdName)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return pubsubDaemonArgs{}, 2, false
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return pubsubDaemonArgs{}, 2, false
	}
	return pubsubDaemonArgs{
		jsonOut:    *jsonOut,
		topic:      fs.Arg(0),
		socketName: *socketName,
		socketPath: *socketPath,
		realmHex:   *realmHex,
	}, 0, true
}

func runPubsubSubscribe(args []string) int {
	a, code, ok := parsePubsubDaemonArgs("pubsub subscribe", args)
	if !ok {
		return code
	}
	sockPath, err := resolveSocketPath(a.socketPath, a.socketName)
	if err != nil {
		return report.Fail(a.jsonOut, err, nil)
	}
	var result daemon.PubsubSubscribeResult
	params := daemon.PubsubSubscribeParams{Topic: a.topic, RealmHex: a.realmHex}
	if err := daemon.Do(sockPath, daemon.MethodPubsubSubscribe, params, &result); err != nil {
		return report.Fail(a.jsonOut, err, nil)
	}
	report.Ok(a.jsonOut, result, func() {
		fmt.Printf("subscribed to %q via the daemon at %s\n", a.topic, sockPath)
	})
	return 0
}

func runPubsubUnsubscribe(args []string) int {
	a, code, ok := parsePubsubDaemonArgs("pubsub unsubscribe", args)
	if !ok {
		return code
	}
	sockPath, err := resolveSocketPath(a.socketPath, a.socketName)
	if err != nil {
		return report.Fail(a.jsonOut, err, nil)
	}
	var result daemon.PubsubUnsubscribeResult
	params := daemon.PubsubUnsubscribeParams{Topic: a.topic, RealmHex: a.realmHex}
	if err := daemon.Do(sockPath, daemon.MethodPubsubUnsubscribe, params, &result); err != nil {
		return report.Fail(a.jsonOut, err, nil)
	}
	report.Ok(a.jsonOut, result, func() {
		if result.Unsubscribed {
			fmt.Printf("%s unsubscribed\n", a.topic)
		} else {
			fmt.Printf("%s was not subscribed\n", a.topic)
		}
	})
	return 0
}
