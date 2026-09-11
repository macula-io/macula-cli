//go:build live

package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/frame"
	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/transport"
)

// TestLiveDaemonStartFallsThroughDeadSeed proves NewServer's seed list
// is real end to end through this package, not just in
// connection.ConnectSeeds itself: seed 1 is unreachable, seed 2 is the
// real live station named by MACULA_LIVE_STATION (liveStation's own
// default). `go test -tags live -run FallsThroughDeadSeed ./internal/daemon`.
func TestLiveDaemonStartFallsThroughDeadSeed(t *testing.T) {
	host, port := liveStation(t)
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	seeds := []connection.Seed{
		{Host: "127.0.0.1", Port: 1}, // nothing listens here
		{Host: host, Port: port},
	}
	srv, err := NewServer(ctx, seeds, id)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	if got := srv.Status().ConnectedTo; got == "" {
		t.Fatalf("Status().ConnectedTo is empty after a successful NewServer")
	}
}

// TestLiveDaemonReconnectsAndReplaysAdvertisement pins the actual
// point of this whole feature: a daemon whose session dies keeps
// serving the same procedure afterward, without anything re-running
// "serve" by hand. Runs against a single real seed (MACULA_LIVE_STATION,
// default station-de-frankfurt) -- redialing the SAME station after a
// forced close is still a real test of the reconnect+replay mechanism
// itself; it doesn't need a second live seed to prove that part works.
// `go test -tags live -run ReconnectsAndReplays ./internal/daemon`.
func TestLiveDaemonReconnectsAndReplaysAdvertisement(t *testing.T) {
	host, port := liveStation(t)
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	srv, err := NewServer(ctx, []connection.Seed{{Host: host, Port: port}}, id)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	serveCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()
	go func() { _ = srv.runSession(serveCtx) }()
	time.Sleep(500 * time.Millisecond)

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	procedure := "daemon.reconnect.live." + hex.EncodeToString(suffix[:]) + ".probe"
	res, err := srv.Register(ServeRegisterParams{Procedure: procedure, Reply: []byte(`{"ok":1}`)})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !res.Registered {
		t.Fatalf("Register: not registered")
	}

	other, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate (caller): %v", err)
	}
	caller, err := connection.Connect(ctx, host, port, transport.WebPKI{}, other)
	if err != nil {
		t.Fatalf("Connect (caller): %v", err)
	}
	defer func() { _ = caller.Close("normal", nil, other) }()
	realm := make([]byte, 32)
	if _, err := caller.Call(procedure, realm, cbor.Null(), time.Now().Add(15*time.Second).UnixMilli(), other, 15*time.Second); err != nil {
		t.Fatalf("procedure not callable before the forced reconnect: %v", err)
	}

	// Force the exact failure mode reconnect exists for: the connection
	// is gone, out from under runSession, with no warning -- Close
	// sends GOODBYE and tears down the underlying QUIC connection,
	// which is exactly what Session.Done() fires on (see macula-go's
	// own doc), indistinguishable from a real station-side drop from
	// this daemon's point of view.
	dead := srv.session.Load()
	if err := dead.Close("normal", nil, srv.id); err != nil {
		t.Logf("forced Close returned an error (expected, connection is being torn down): %v", err)
	}

	// Give runSession room to notice, redial (respawnDelay + a real
	// handshake), and replay the advertisement.
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if srv.session.Load() == dead {
			time.Sleep(500 * time.Millisecond)
			continue // hasn't swapped in a fresh session yet
		}
		_, lastErr = caller.Call(procedure, realm, cbor.Null(), time.Now().Add(10*time.Second).UnixMilli(), other, 10*time.Second)
		if lastErr == nil {
			return // reconnected AND replayed -- the procedure works again
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("procedure never became callable again after the forced reconnect: %v", lastErr)
}

// TestLiveDaemonStatusReflectsConnectionLoss is the peer-review ask this
// whole feature exists to satisfy: not just "does the underlying
// reconnect logic run" (the test above already proved that), but "does
// Status() -- what an operator or a supervisor actually SEES -- reflect
// reality during the outage, not just after it's over." Kills the
// session the exact same way TestLiveDaemonReconnectsAndReplaysAdvertisement
// does (Close(), indistinguishable from a real station-side drop), then
// asserts Connected/LastError go bad WHILE the daemon is mid-reconnect
// (not just eventually recovering), a real log line was produced for
// the loss, and both clear again once reconnected. `go test -tags live
// -run StatusReflectsConnectionLoss ./internal/daemon`.
func TestLiveDaemonStatusReflectsConnectionLoss(t *testing.T) {
	host, port := liveStation(t)
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	srv, err := NewServer(ctx, []connection.Seed{{Host: host, Port: port}}, id)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	var logBuf bytes.Buffer
	srv.SetLogger(log.New(&logBuf, "", 0))

	serveCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()
	go func() { _ = srv.runSession(serveCtx) }()
	time.Sleep(500 * time.Millisecond)

	if got := srv.Status(); !got.Connected || got.LastError != "" {
		t.Fatalf("expected a healthy baseline status before forcing anything, got Connected=%v LastError=%q", got.Connected, got.LastError)
	}

	dead := srv.session.Load()
	if err := dead.Close("normal", nil, srv.id); err != nil {
		t.Logf("forced Close returned an error (expected, connection is being torn down): %v", err)
	}

	// The actual point: catch Status() DURING the outage, before
	// respawnDelay + redial has had a chance to complete -- proving this
	// is observable in real time, not just true in hindsight after
	// reconnection already happened.
	sawDisconnected := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got := srv.Status()
		if !got.Connected {
			sawDisconnected = true
			if got.LastError == "" {
				t.Fatalf("expected LastError populated while Connected=false, got empty")
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawDisconnected {
		t.Fatalf("never observed Status().Connected go false during the forced outage -- either the kill didn't take, or Status isn't reflecting it in real time")
	}
	if !strings.Contains(logBuf.String(), "session lost") {
		t.Fatalf("expected a logged connection-lost line, got:\n%s", logBuf.String())
	}

	// Now confirm it clears again once reconnected -- same reconnect
	// wait shape as TestLiveDaemonReconnectsAndReplaysAdvertisement.
	recoverDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(recoverDeadline) {
		got := srv.Status()
		if got.Connected && got.LastError == "" {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("Status() never returned to Connected=true/LastError=\"\" after the forced reconnect")
}

// TestLiveDaemonAfterAForcedReconnectItsAdvertisementsAndSubscriptionsWorkAgain
// proves the daemon's one reconnect path restores everything its session
// carries: once the session is gone, a registered procedure answers again
// and a watched subscription delivers events again, with nothing re-run by
// hand. `go test -tags live -run AfterAForcedReconnect ./internal/daemon`.
func TestLiveDaemonAfterAForcedReconnectItsAdvertisementsAndSubscriptionsWorkAgain(t *testing.T) {
	host, port := liveStation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	id := mustGenerate(t, "daemon")
	srv, err := NewServer(ctx, []connection.Seed{{Host: host, Port: port}}, id)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go func() { _ = srv.runSession(runCtx) }()
	time.Sleep(500 * time.Millisecond)

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	name := "daemon.reconnect.live." + hex.EncodeToString(suffix[:])
	procedure, topic := name+".probe", name+".events"
	realm := make([]byte, 32)
	if _, err := srv.Register(ServeRegisterParams{Procedure: procedure, Reply: []byte(`{"ok":1}`)}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	events := make(chan PubsubEventNotification, 64)
	unwatch, err := srv.watch(realm, topic, events)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer unwatch()

	callerID := mustGenerate(t, "caller")
	caller, err := connection.Connect(ctx, host, port, transport.WebPKI{}, callerID)
	if err != nil {
		t.Fatalf("Connect (caller): %v", err)
	}
	defer func() { _ = caller.Close("normal", nil, callerID) }()

	seq := uint64(time.Now().UnixMicro())
	works := func(stage string) {
		t.Helper()
		deadline := time.Now().Add(40 * time.Second)
		for {
			_, callErr := caller.Call(procedure, realm, cbor.Null(), time.Now().Add(10*time.Second).UnixMilli(), callerID, 10*time.Second)
			if callErr == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the registered procedure never answered: %v", stage, callErr)
			}
			time.Sleep(500 * time.Millisecond)
		}
		for {
			seq++
			spec := frame.NewPublishSpec(topic, realm, callerID.NodeID(), seq, cbor.Text(stage), time.Now().UnixMilli())
			if err := caller.Publish(spec, callerID); err != nil {
				t.Fatalf("%s: Publish: %v", stage, err)
			}
			select {
			case evt, ok := <-events:
				if !ok {
					t.Fatalf("%s: the daemon ended the subscription", stage)
				}
				if got, _ := evt.Payload.(string); got == stage {
					return
				}
			case <-time.After(time.Second):
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: the watched subscription never delivered an event", stage)
			}
		}
	}

	works("before the reconnect")
	dead := srv.session.Load()
	if err := dead.Close("normal", nil, srv.id); err != nil {
		t.Logf("forced Close returned an error (expected, connection is being torn down): %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for srv.session.Load() == dead {
		if time.Now().After(deadline) {
			t.Fatal("the daemon never replaced its closed session")
		}
		time.Sleep(200 * time.Millisecond)
	}
	works("after the reconnect")
}
