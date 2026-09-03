//go:build live

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/connection"
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
// point of this whole feature: a daemon whose serve session dies keeps
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
	go func() { _ = srv.runServeLoop(serveCtx) }()
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
	// is gone, out from under runServeLoop, with no warning -- Close
	// sends GOODBYE and tears down the underlying QUIC connection,
	// which is exactly what Session.Done() fires on (see macula-go's
	// own doc), indistinguishable from a real station-side drop from
	// this daemon's point of view.
	dead := srv.serveSession.Load()
	if err := dead.Close("normal", nil, srv.id); err != nil {
		t.Logf("forced Close returned an error (expected, connection is being torn down): %v", err)
	}

	// Give runServeLoop room to notice, redial (respawnDelay + a real
	// handshake), and replay the advertisement.
	deadline := time.Now().Add(30 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		if srv.serveSession.Load() == dead {
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
