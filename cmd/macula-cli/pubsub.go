package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/pool"
	"github.com/macula-io/macula-go/stationlink"

	"github.com/macula-io/macula-cli/internal/report"
	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// heardEvent is one publication a watch heard, verified.
type heardEvent struct {
	Publisher    string  `json:"publisher"`
	Topic        string  `json:"topic"`
	Seq          uint64  `json:"seq"`
	PublishedAt  uint64  `json:"published_at"`
	DeliveredVia string  `json:"delivered_via"`
	Payload      rawJSON `json:"payload"`
}

// publish publishes payload on topic in realm, living ttl (macula's 10
// minutes when zero).
func publish(p *pool.Pool, realm [32]byte, topic string, payload cbor.Value, ttl time.Duration) error {
	pub := stationlink.Publication{Realm: realm, Topic: topic, Payload: payload}
	if ttl > 0 {
		ms := uint64(ttl.Milliseconds())
		pub.TTLMs = &ms
	}
	return p.Publish(pub)
}

// watch subscribes to topic in realm and hands each verified event to heard
// until ctx ends or count events arrived (count 0: no limit). ready is called
// once the subscription stands.
func watch(ctx context.Context, p *pool.Pool, realm [32]byte, topic string, count int, ready func(),
	heard func(heardEvent)) (int, error) {
	sub, err := p.Subscribe(realm, topic)
	if err != nil {
		return 0, err
	}
	defer sub.Unsubscribe()
	ready()
	n := 0
	for count == 0 || n < count {
		select {
		case <-ctx.Done():
			return n, nil
		case e, ok := <-sub.Events():
			if !ok {
				return n, nil
			}
			n++
			heard(heardEvent{Publisher: fmt.Sprintf("%x", e.Publisher), Topic: e.Topic, Seq: e.Seq,
				PublishedAt: e.PublishedAt, DeliveredVia: e.DeliveredVia, Payload: rawJSON(wirevalue.ToJSON(e.Payload))})
		}
	}
	return n, nil
}

func runPubsub(args []string) int {
	if len(args) == 0 {
		return unknownSubcommand("pubsub", args, "publish, watch")
	}
	switch args[0] {
	case "publish":
		return runPublish(args[1:])
	case "watch":
		return runWatch(args[1:])
	}
	return unknownSubcommand("pubsub", args, "publish, watch")
}

func runPublish(args []string) int {
	fs := flag.NewFlagSet("pubsub publish", flag.ContinueOnError)
	var m meshFlags
	m.register(fs, true)
	payloadText := fs.String("payload", "null", "the payload as JSON; ids go in the payload, never in the topic")
	payloadFile := fs.String("payload-file", "", "read the payload JSON from this file instead")
	ttl := fs.Duration("ttl", 0, "how long the publication lives (default: macula's 10 minutes)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli pubsub publish -seed host:port@<node_id> -realm <realm> [flags] <topic>")
		fs.PrintDefaults()
	}
	if code, ok := parse(fs, args, &m.jsonOut, exactly(1)); !ok {
		return code
	}
	payload, err := payloadFlag(*payloadText, *payloadFile)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	realm, err := realmID(m.realm)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	p, err := m.join(context.Background())
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	defer p.Close()
	if err := publish(p, realm, fs.Arg(0), payload, *ttl); err != nil {
		return report.Fail(m.jsonOut, err)
	}
	report.Ok(m.jsonOut, map[string]string{"topic": fs.Arg(0)}, func(w io.Writer) { fmt.Fprintf(w, "published on %s\n", fs.Arg(0)) })
	return 0
}

func runWatch(args []string) int {
	fs := flag.NewFlagSet("pubsub watch", flag.ContinueOnError)
	var m meshFlags
	m.register(fs, true)
	count := fs.Int("count", 0, "stop after this many events (default: no limit)")
	forTime := fs.Duration("for", 0, "stop after this long (default: until interrupted)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli pubsub watch -seed host:port@<node_id> -realm <realm> [flags] <topic>")
		fmt.Fprintln(fs.Output(), "       prints each verified event as it arrives; with -json, one envelope per event")
		fs.PrintDefaults()
	}
	if code, ok := parse(fs, args, &m.jsonOut, exactly(1)); !ok {
		return code
	}
	realm, err := realmID(m.realm)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *forTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *forTime)
		defer cancel()
	}
	p, err := m.join(ctx)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	defer p.Close()
	_, err = watch(ctx, p, realm, fs.Arg(0), *count, func() {
		if !m.jsonOut {
			fmt.Fprintf(os.Stderr, "watching %s as node %x\n", fs.Arg(0), p.NodeID())
		}
	}, func(e heardEvent) {
		report.Ok(m.jsonOut, e, func(w io.Writer) {
			fmt.Fprintf(w, "%s seq %d from %s: %s\n", e.Topic, e.Seq, e.Publisher, e.Payload)
		})
	})
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	return 0
}
