//go:build live

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/frame"
	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/transport"
	"github.com/macula-io/macula-go/ucan"
)

// TestLiveDaemonCallWithATokenForItsOwnIdentityIsAccepted proves a call
// through the daemon arrives as the daemon's own persisted identity: a
// provider gated with ucan.Required accepts a token only from the identity
// the token was minted for (its audience), so a token minted for the
// daemon's identity must be accepted, and one minted for any other
// identity refused. `go test -tags live -run TokenForItsOwnIdentity
// ./internal/daemon`.
func TestLiveDaemonCallWithATokenForItsOwnIdentityIsAccepted(t *testing.T) {
	host, port := liveStation(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	daemonID := mustGenerate(t, "daemon")
	providerID := mustGenerate(t, "provider")
	issuerID := mustGenerate(t, "issuer")
	otherID := mustGenerate(t, "other")

	provider, err := connection.Connect(ctx, host, port, transport.WebPKI{}, providerID)
	if err != nil {
		t.Fatalf("Connect (provider): %v", err)
	}
	defer func() { _ = provider.Close("normal", nil, providerID) }()

	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("rand: %v", err)
	}
	procedure := "daemon.identity.live." + hex.EncodeToString(suffix[:]) + ".gated"
	realm := make([]byte, 32)
	if err := provider.Advertise(frame.NewAdvertiseSpec(realm, procedure, providerID.NodeID()), providerID); err != nil {
		t.Fatalf("Advertise: %v", err)
	}
	gated := ucan.Required(issuerID.NodeID())
	lookup := func(_ []byte, proc string) (connection.CallHandler, bool) {
		if proc != procedure {
			return nil, false
		}
		return func(cbor.Value) (cbor.Value, error) {
			return cbor.Map([]cbor.MapEntry{{Key: cbor.Text("served"), Val: cbor.Uint64(1)}}), nil
		}, true
	}
	serveCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()
	go func() {
		_ = provider.ServeForever(serveCtx, lookup, func([]byte, string) ucan.Policy { return gated }, providerID)
	}()
	time.Sleep(500 * time.Millisecond)

	srv, err := NewServer(ctx, []connection.Seed{{Host: host, Port: port}}, daemonID)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	mint := func(audience identity.KeyPair) string {
		token, err := ucan.Create("did:macula:daemon-identity-live-test", hex.EncodeToString(audience.NodeID()),
			[]ucan.Capability{{With: "mri:test:daemon", Can: "call"}}, issuerID, ucan.CreateOpts{})
		if err != nil {
			t.Fatalf("ucan.Create: %v", err)
		}
		return hex.EncodeToString(token)
	}

	_, err = srv.Invoke(CallInvokeParams{Procedure: procedure, UcanTokenHex: mint(otherID), TimeoutMs: 15000})
	var refused *wireCallError
	if !errors.As(err, &refused) || refused.bolt4Name != "unauthorized" {
		t.Fatalf("a call carrying a token minted for another identity = %v, want refused as unauthorized", err)
	}

	res, err := srv.Invoke(CallInvokeParams{Procedure: procedure, UcanTokenHex: mint(daemonID), TimeoutMs: 15000})
	if err != nil {
		t.Fatalf("a call carrying a token minted for the daemon's own identity was refused: %v", err)
	}
	t.Logf("served through the daemon as its own identity: %v", res.Payload)
}

func mustGenerate(t *testing.T, role string) identity.KeyPair {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate (%s): %v", role, err)
	}
	return id
}
