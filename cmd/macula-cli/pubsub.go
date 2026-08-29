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

	"github.com/macula-io/macula-go-sdk/connection"
	"github.com/macula-io/macula-go-sdk/frame"
	"github.com/macula-io/macula-go-sdk/transport"

	"github.com/macula-io/macula-cli/internal/report"
	"github.com/macula-io/macula-cli/internal/wirevalue"
)

func runPubsub(args []string) int {
	if len(args) == 0 || args[0] != "watch" {
		fmt.Fprintln(os.Stderr, "Usage: macula-cli pubsub watch [flags] <host[:port]> <topic>")
		return 2
	}
	return runPubsubWatch(args[1:])
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
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli pubsub watch [flags] <host[:port]> <topic>\n\n"+
			"Subscribes and prints events as they arrive. Stops on --count, --duration,\n"+
			"or Ctrl-C, whichever comes first.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return 2
	}

	host, port, err := parseHostPort(fs.Arg(0))
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
	session, err := connection.Connect(connectCtx, host, port, transport.WebPKI{}, id)
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
		fmt.Fprintf(os.Stderr, "watching %q on %s:%d (Ctrl-C to stop)\n", topic, host, port)
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
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			_ = enc.Encode(out)
		} else {
			fmt.Printf("[%s] seq=%d via=%s from=%s %v\n",
				out.ReceivedAt, out.Seq, out.DeliveredVia, out.Publisher, out.Payload)
		}
		received++
		if *count > 0 && received >= *count {
			return finishWatch(*jsonOut, received)
		}
	}
}

func finishWatch(jsonOut bool, received int) int {
	if !jsonOut {
		fmt.Fprintf(os.Stderr, "(stopped, %d event(s) received)\n", received)
	}
	return 0
}
