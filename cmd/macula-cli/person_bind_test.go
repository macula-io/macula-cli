package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/devicerequest"
	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/pool"
	"github.com/macula-io/macula-go/profile"
)

// A person binds their clients once (macula-realm#46 step 2): the realm admits
// each client on its own membership, sponsored by the person, and ends it with
// the person. The person key signs the request; it never connects.

// bindingCaller answers a bind or unbind as macula-realm does, and keeps the
// call it was given.
func bindingCaller(sent *pool.Call, status string) caller {
	return func(_ context.Context, c pool.Call) (cbor.Value, error) {
		*sent = c
		client, _ := c.Payload.Get("client")
		return cbor.Map([]cbor.MapEntry{
			{Key: cbor.Text("client"), Val: client},
			{Key: cbor.Text("status"), Val: cbor.Text(status)},
		}), nil
	}
}

// verifiesForProcedure is whether the call's proof is the person's signature,
// for procedure in the call's realm, over the payload less its proof.
func verifiesForProcedure(t *testing.T, sent pool.Call, procedure string) bool {
	t.Helper()
	carried, _ := sent.Payload.Get("public_key")
	client, _ := sent.Payload.Get("client")
	proof, _ := sent.Payload.Get("proof")
	field := func(name string) cbor.Value { v, _ := proof.Get(name); return v }
	ts, _ := field("timestamp").AsInt64()
	nonceText, _ := field("nonce").AsText()
	sigText, _ := field("signature").AsText()
	nonce, _ := hex.DecodeString(nonceText)
	signature, _ := hex.DecodeString(sigText)
	carriedText, _ := carried.AsText()
	public, _ := base64Decode(carriedText)
	request := cbor.Map([]cbor.MapEntry{{Key: cbor.Text("public_key"), Val: carried}, {Key: cbor.Text("client"), Val: client}})
	message := devicerequest.Message(public, sent.Realm, procedure, uint64(ts), [16]byte(nonce), request)
	return identity.Verify(message, signature, public, profile.PQPure)
}

func TestABindIsSignedByThePersonOverThePayloadLessItsProof(t *testing.T) {
	person := freshKey(t)
	client, _ := freshKey(t).NodeID()
	var sent pool.Call

	r, err := requestBinding(context.Background(), bindingCaller(&sent, "bound"), person, "io.macula", client, bindClient, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Procedure != "io.macula/_realm/_realm/identity/bind_client_v1" || sent.Realm != sha256.Sum256([]byte("io.macula")) {
		t.Fatalf("called %s in %x", sent.Procedure, sent.Realm)
	}
	if got, _ := sent.Payload.Get("client"); !textIs(got, hexOf(client[:])) {
		t.Fatalf("client %v, want the client's node id in hex", got)
	}
	if !verifiesForProcedure(t, sent, "macula_realm.bind_client") {
		t.Fatal("the bind proof is not the person's signature over the payload less its proof")
	}
	if r.Client != hexOf(client[:]) || r.Status != "bound" {
		t.Fatalf("result %+v", r)
	}
}

func TestAnUnbindIsSignedForUnbinding(t *testing.T) {
	person := freshKey(t)
	client, _ := freshKey(t).NodeID()
	var sent pool.Call

	r, err := requestBinding(context.Background(), bindingCaller(&sent, "unbound"), person, "io.macula", client, unbindClient, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Procedure != "io.macula/_realm/_realm/identity/unbind_client_v1" {
		t.Fatalf("called %s", sent.Procedure)
	}
	// A bind proof must not unbind, nor the reverse: the procedure is signed.
	if !verifiesForProcedure(t, sent, "macula_realm.unbind_client") || verifiesForProcedure(t, sent, "macula_realm.bind_client") {
		t.Fatal("the unbind proof is not signed for unbinding alone")
	}
	if r.Status != "unbound" {
		t.Fatalf("result %+v", r)
	}
}

func TestTheRealmsRefusalIsReportedAsItSaysIt(t *testing.T) {
	refusal := "email_not_verified: verify your email on the realm's web join first, then bind your clients"
	refusing := func(context.Context, pool.Call) (cbor.Value, error) { return cbor.Value{}, errors.New(refusal) }
	client, _ := freshKey(t).NodeID()

	_, err := requestBinding(context.Background(), refusing, freshKey(t), "io.macula", client, bindClient, time.Second)
	if err == nil || !strings.Contains(err.Error(), refusal) {
		t.Fatalf("%v, want the realm's own words", err)
	}
}

func TestBindNamesItsClientAsANodeID(t *testing.T) {
	for _, to := range []string{"", "abc", strings.Repeat("zz", 32), strings.Repeat("ab", 31)} {
		code, out, _ := runCaptured(t, "person", "bind", "-json", "-realm", "io.macula", "-to", to)
		if code != 2 || !strings.Contains(out, "-to") {
			t.Fatalf("-to %q: exit %d %s, want a usage error naming -to", to, code, out)
		}
	}
}

// The short-lived note a person signed for a client is gone: a client now has
// its own membership, which the realm ends with the person.
func TestPersonDelegateIsGone(t *testing.T) {
	code, out, _ := runCaptured(t, "person", "delegate", "-json")
	if code != 2 || !strings.Contains(out, "bind") {
		t.Fatalf("exit %d %s, want an unknown subcommand naming bind", code, out)
	}
}

func textIs(v cbor.Value, want string) bool {
	s, ok := v.AsText()
	return ok && s == want
}
