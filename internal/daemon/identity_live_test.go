//go:build live

package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// TestLiveADaemonCallArrivesAsTheDaemonsOwnIdentity proves a call through
// the daemon arrives as the daemon's own persisted identity, two ways. A
// provider gated with ucan.Required accepts a token only from the identity
// the token was minted for (its audience), so a token minted for the
// daemon's identity must be accepted, and one minted for any other
// identity refused. And an open provider that echoes its payload shows the
// caller its session verified, which macula-go puts in a map payload under
// "caller" in place of any "caller" the sender wrote: that must be the
// daemon's identity, whatever caller the payload given to the daemon named.
// `go test -tags live -run ArrivesAsTheDaemonsOwnIdentity ./internal/daemon`.
func TestLiveADaemonCallArrivesAsTheDaemonsOwnIdentity(t *testing.T) {
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
	echoProcedure := "daemon.identity.live." + hex.EncodeToString(suffix[:]) + ".echo"
	realm := make([]byte, 32)
	mustAdvertise(t, provider, realm, procedure, providerID)
	mustAdvertise(t, provider, realm, echoProcedure, providerID)
	handlers := map[string]connection.CallHandler{procedure: answerServed, echoProcedure: echoPayload}
	policies := map[string]ucan.Policy{procedure: ucan.Required(issuerID.NodeID()), echoProcedure: ucan.Open}
	lookup := func(_ []byte, proc string) (connection.CallHandler, bool) {
		handler, ok := handlers[proc]
		return handler, ok
	}
	policy := func(_ []byte, proc string) ucan.Policy { return policies[proc] }
	serveCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()
	go func() {
		_ = provider.ServeForever(serveCtx, lookup, policy, providerID)
	}()
	time.Sleep(500 * time.Millisecond)

	srv, err := NewServer(ctx, []connection.Seed{{Host: host, Port: port}}, daemonID)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	_, err = srv.Invoke(CallInvokeParams{Procedure: procedure, UcanTokenHex: mintFor(t, issuerID, otherID), TimeoutMs: 15000})
	var refused *wireCallError
	if !errors.As(err, &refused) || refused.bolt4Name != "unauthorized" {
		t.Fatalf("a call carrying a token minted for another identity = %v, want refused as unauthorized", err)
	}

	res, err := srv.Invoke(CallInvokeParams{Procedure: procedure, UcanTokenHex: mintFor(t, issuerID, daemonID), TimeoutMs: 15000})
	if err != nil {
		t.Fatalf("a call carrying a token minted for the daemon's own identity was refused: %v", err)
	}
	t.Logf("served through the daemon as its own identity: %v", res.Payload)

	named := "0x" + hex.EncodeToString(otherID.NodeID())
	echoed, err := srv.Invoke(CallInvokeParams{Procedure: echoProcedure, Payload: json.RawMessage(`{"caller":"` + named + `"}`), TimeoutMs: 15000})
	if err != nil {
		t.Fatalf("an open call through the daemon: %v", err)
	}
	want := "0x" + hex.EncodeToString(daemonID.NodeID())
	if got := callerIn(echoed.Payload); got != want {
		t.Fatalf("the provider verified the caller as %q, want the daemon's identity %q (the payload named %q)", got, want, named)
	}
}

// answerServed is the gated procedure's handler.
func answerServed(cbor.Value) (cbor.Value, error) {
	return cbor.Map([]cbor.MapEntry{{Key: cbor.Text("served"), Val: cbor.Uint64(1)}}), nil
}

// echoPayload answers with the payload as the provider's handler received it.
func echoPayload(payload cbor.Value) (cbor.Value, error) {
	return payload, nil
}

// callerIn is the "caller" entry of a call result's payload as the daemon
// renders it, or "" when it has none.
func callerIn(payload any) string {
	fields, _ := payload.(map[string]any)
	caller, _ := fields["caller"].(string)
	return caller
}

func mustAdvertise(t *testing.T, provider *connection.Session, realm []byte, procedure string, providerID identity.KeyPair) {
	t.Helper()
	if err := provider.Advertise(frame.NewAdvertiseSpec(realm, procedure, providerID.NodeID()), providerID); err != nil {
		t.Fatalf("Advertise %s: %v", procedure, err)
	}
}

// mintFor is a hex token from issuer that lets audience call.
func mintFor(t *testing.T, issuer, audience identity.KeyPair) string {
	t.Helper()
	token, err := ucan.Create("did:macula:daemon-identity-live-test", hex.EncodeToString(audience.NodeID()),
		[]ucan.Capability{{With: "mri:test:daemon", Can: "call"}}, issuer, ucan.CreateOpts{})
	if err != nil {
		t.Fatalf("ucan.Create: %v", err)
	}
	return hex.EncodeToString(token)
}

func mustGenerate(t *testing.T, role string) identity.KeyPair {
	t.Helper()
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate (%s): %v", role, err)
	}
	return id
}
