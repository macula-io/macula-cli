//go:build live

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/directdial"
	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/transport"
)

// Pins the fix in Server.publishDirectAdvertisement: a daemon that is
// already running ServeForever must still be able to register a
// procedure with Direct, and the resulting DHT record must resolve to
// this daemon's own station from a fresh, unrelated session. Before the
// fix every such registration failed with a put_record read deadline,
// because the put rode the same session ServeForever was reading.
//
// Needs a real station: MACULA_LIVE_STATION (host:port), default the
// public Frankfurt station. `go test -tags live -run Direct ./internal/daemon`.
func TestLiveRegisterDirect_PublishesResolvableRecordWhileServing(t *testing.T) {
	host, port := liveStation(t)
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	srv, err := NewServer(ctx, host, port, id)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	// ServeForever must be RUNNING for this to mean anything -- that is
	// the loop that ate the put_record reply before the fix. Started the
	// way Run starts it, minus the control socket this test has no use for.
	serveCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()
	go func() { _ = srv.serveSession.ServeForever(serveCtx, srv.lookup, srv.policy, srv.id) }()
	time.Sleep(500 * time.Millisecond)

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	procedure := "daemon.direct.live." + hex.EncodeToString(suffix[:]) + ".probe"
	res, err := srv.Register(ServeRegisterParams{Procedure: procedure, Reply: []byte(`{"ok":1}`), Direct: true, TTLSeconds: 120})
	if err != nil {
		t.Fatalf("Register(Direct): %v", err)
	}
	if !res.Registered {
		t.Fatalf("Register(Direct): not registered")
	}

	// Resolve from a fresh session with a fresh identity: what any other
	// agent on the mesh would do.
	other, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate (resolver): %v", err)
	}
	resolver, err := connection.Connect(ctx, host, port, transport.WebPKI{}, other)
	if err != nil {
		t.Fatalf("Connect (resolver): %v", err)
	}
	defer func() { _ = resolver.Close("normal", nil, other) }()

	realm := make([]byte, 32)
	var lastErr error
	for attempt := 0; attempt < 10; attempt++ {
		station, _, _, rerr := directdial.Resolve(resolver, other, realm, procedure)
		if rerr == nil {
			if hex.EncodeToString(station) != hex.EncodeToString(srv.serveSession.Station.NodeID) {
				t.Fatalf("resolved station %x, daemon serves on %x", station, srv.serveSession.Station.NodeID)
			}
			return
		}
		lastErr = rerr
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("direct-dial record never became resolvable: %v", lastErr)
}

func liveStation(t *testing.T) (string, uint16) {
	t.Helper()
	raw := os.Getenv("MACULA_LIVE_STATION")
	if raw == "" {
		raw = "station-de-frankfurt.macula.io:4433"
	}
	host, portStr, ok := strings.Cut(raw, ":")
	if !ok {
		t.Fatalf("MACULA_LIVE_STATION must be host:port, got %q", raw)
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		t.Fatalf("MACULA_LIVE_STATION port: %v", err)
	}
	return host, uint16(port)
}
