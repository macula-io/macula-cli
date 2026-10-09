package main

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/pool"
	"github.com/macula-io/macula-go/profile"
	"github.com/macula-io/macula-go/stationlink"
	"github.com/macula-io/macula-go/ucan"
)

const memberCan = "member/email-verified"

// aPerson is a person key and its membership in tm's realm, minted by the
// realm key as the realm mints one at a join, kept where person join keeps it.

// servingGated serves org/<name> on station 0 under realm_member_required for
// tm's realm key, counting the calls that reach its handler.
func servingGated(t *testing.T, tm *testMesh, name string, entered *atomic.Int64) string {
	t.Helper()
	p, _ := tm.node(t, 0, true, true)
	procedure := tm.realm.Org + "/" + name
	served, err := p.Serve(context.Background(), pool.Offer{Realm: tm.realm.ID, Procedure: procedure,
		// The key id of the realm key as carried, as a fleet gate names it
		// (mcl-search: macula_node_keys:key_id of the pinned realm key).
		Policy: ucan.RealmMemberRequired{KeyID: identity.KeyIDOf(tm.realm.RealmKey(), profile.PQPure), Can: memberCan},
		Handler: func(_ context.Context, r stationlink.Request) (cbor.Value, error) {
			entered.Add(1)
			return cbor.Text("served"), nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = served.Stop() })
	deadline := time.Now().Add(10 * time.Second)
	for !tm.stations[0].Advertised(tm.realm.ID, procedure) {
		if time.Now().After(deadline) {
			t.Fatalf("%s never advertised", procedure)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return procedure
}

// aClientMembership is a membership for node, minted by tm's realm key as the
// realm mints one for a client a person bound (macula-realm#46 step 2).
func aClientMembership(t *testing.T, tm *testMesh, node [32]byte, exp time.Time) presented {
	t.Helper()
	token, err := ucan.Create(tm.realm.Key, node, []ucan.Capability{{With: "mri:realm:" + tm.realm.Name, Can: memberCan}},
		ucan.Options{Exp: exp.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	return presented{token: token}
}

func TestEachBoundClientCallsAMemberGatedProcedureWithItsOwnMembership(t *testing.T) {
	tm := newTestMesh(t)
	var entered atomic.Int64
	procedure := servingGated(t, tm, "members_only", &entered)
	for i := range 2 {
		client, m := tm.node(t, 1, true, false)
		got, err := call(context.Background(), client, tm.realm.ID, procedure, cbor.Null(), [32]byte{}, m,
			aClientMembership(t, tm, client.NodeID(), time.Now().Add(time.Hour)))
		if err != nil {
			t.Fatalf("client %d: %v", i, err)
		}
		if string(got.Result) != `"served"` {
			t.Fatalf("client %d: result %s", i, got.Result)
		}
	}
	if n := entered.Load(); n != 2 {
		t.Fatalf("the handler ran %d times, want 2", n)
	}
}

func TestNoMembershipAndAnotherNodesAreRefusedBeforeTheHandler(t *testing.T) {
	tm := newTestMesh(t)
	var entered atomic.Int64
	procedure := servingGated(t, tm, "members_only", &entered)
	client, m := tm.node(t, 1, true, false)

	_, err := call(context.Background(), client, tm.realm.ID, procedure, cbor.Null(), [32]byte{}, m, presented{})
	if code := providerCode(err); code != "unauthorized" {
		t.Fatalf("no membership: %v", err)
	}
	// Another node's membership, presented by this one.
	elsewhere, _ := freshKey(t).NodeID()
	_, err = call(context.Background(), client, tm.realm.ID, procedure, cbor.Null(), [32]byte{}, m,
		aClientMembership(t, tm, elsewhere, time.Now().Add(time.Hour)))
	if code := providerCode(err); code != "unauthorized" {
		t.Fatalf("another node's membership: %v", err)
	}
	if n := entered.Load(); n != 0 {
		t.Fatalf("the handler ran %d times for refused calls", n)
	}
}

func providerCode(err error) string {
	var pe *stationlink.ProviderError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func TestAMembershipIsKeptPerPersonAndWrittenOwnerOnly(t *testing.T) {
	dir := t.TempDir()
	a := membershipPath(filepath.Join(dir, "person.key"), [32]byte{1}, "io.macula")
	b := membershipPath(filepath.Join(dir, "person.key"), [32]byte{2}, "io.macula")
	if a == b {
		t.Fatal("two persons share one membership file")
	}
	if err := os.MkdirAll(filepath.Dir(a), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writePrivate(a, []byte("token\n")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(a)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v (%v)", info.Mode().Perm(), err)
	}
	if got, _ := os.ReadFile(a); string(got) != "token\n" {
		t.Fatalf("content %q", got)
	}
}

func TestAChainFileIsTheTokenThenItsProofs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "chain")
	if err := os.WriteFile(file, []byte("\n note \n\nparent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, proofs, err := readChain(file)
	if err != nil || string(token) != "note" || len(proofs) != 1 || string(proofs[0]) != "parent" {
		t.Fatalf("token %q proofs %q err %v", token, proofs, err)
	}
	empty := filepath.Join(t.TempDir(), "empty")
	_ = os.WriteFile(empty, []byte("\n\n"), 0o600)
	if _, _, err := readChain(empty); err == nil {
		t.Fatal("an empty chain file was read")
	}
}

func TestServeRequireMemberServesAMemberAndRefusesNone(t *testing.T) {
	tm := newTestMesh(t)
	policy, err := memberPolicy(memberCan, hex.EncodeToString(tm.realm.RealmKey()), "pq_pure")
	if err != nil {
		t.Fatal(err)
	}
	var entered atomic.Int64
	procedure := serving(t, tm, 0, tm.realm.Org+"/members_only", serveOptions{policy: policy, reply: ptr(cbor.Text("served"))},
		func(servedCall) { entered.Add(1) })
	client, m := tm.node(t, 1, true, false)
	if _, err := call(context.Background(), client, tm.realm.ID, procedure, cbor.Null(), [32]byte{}, m, presented{}); providerCode(err) != "unauthorized" {
		t.Fatalf("no membership: %v", err)
	}
	got, err := call(context.Background(), client, tm.realm.ID, procedure, cbor.Null(), [32]byte{}, m,
		aClientMembership(t, tm, client.NodeID(), time.Now().Add(time.Hour)))
	if err != nil || string(got.Result) != `"served"` {
		t.Fatalf("with a membership: %v, %v", got.Result, err)
	}
	if n := entered.Load(); n != 1 {
		t.Fatalf("the handler ran %d times, want 1", n)
	}
}

func TestRequireMemberNeedsTheRealmKeyAndACan(t *testing.T) {
	if _, err := memberPolicy(memberCan, "", "pq_hybrid"); err == nil {
		t.Fatal("a member gate with no -realm-key was accepted")
	}
	if _, err := memberPolicy(" ", "00", "pq_hybrid"); err == nil {
		t.Fatal("a member gate with a blank can was accepted")
	}
}

func ptr[T any](v T) *T { return &v }

func TestAnEmptyRequireMemberIsRefusedNotServedOpen(t *testing.T) {
	code, _, errs := runCaptured(t, "serve", "-require-member", "", "-realm", "io.macula", "-realm-key", "00",
		"-seed", "127.0.0.1:1@"+strings.Repeat("0", 64), "acme/members_only")
	if code != 2 || !strings.Contains(errs, "-require-member") {
		t.Fatalf("exit %d, %q: want the empty gate refused by name", code, errs)
	}
}
