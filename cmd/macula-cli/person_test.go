package main

import (
	"context"
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
func aPerson(t *testing.T, tm *testMesh, exp time.Time) (*identity.NodeKey, string) {
	t.Helper()
	person := freshKey(t)
	id, _ := person.NodeID()
	membership, err := ucan.Create(tm.realm.Key, id, []ucan.Capability{{With: "mri:realm:" + tm.realm.Name, Can: memberCan}},
		ucan.Options{Exp: exp.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "person", tm.realm.Name+".ucan")
	if err := writePrivate(file, append(membership, '\n')); err != nil {
		t.Fatal(err)
	}
	return person, file
}

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

func chainOf(t *testing.T, chain []byte) presented {
	t.Helper()
	file := filepath.Join(t.TempDir(), "chain")
	if err := writePrivate(file, chain); err != nil {
		t.Fatal(err)
	}
	token, proofs, err := readChain(file)
	if err != nil {
		t.Fatal(err)
	}
	return presented{token: token, proofs: proofs}
}

func TestAPersonsNoteLetsEachOfTheirClientsCallAMemberGatedProcedure(t *testing.T) {
	tm := newTestMesh(t)
	var entered atomic.Int64
	procedure := servingGated(t, tm, "members_only", &entered)
	person, membership := aPerson(t, tm, time.Now().Add(4*time.Hour))
	for i := range 2 {
		client, m := tm.node(t, 1, true, false)
		chain, note, err := delegate(person, membership, tm.realm.RealmKey(), client.NodeID(), time.Hour, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		clientID := client.NodeID()
		if note.Client != hexOf(clientID[:]) {
			t.Fatalf("client %d: the note is for %s", i, note.Client)
		}
		got, err := call(context.Background(), client, tm.realm.ID, procedure, cbor.Null(), [32]byte{}, m, chainOf(t, chain))
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

func TestNoNoteAndAnotherPersonsNoteAreRefusedBeforeTheHandler(t *testing.T) {
	tm := newTestMesh(t)
	var entered atomic.Int64
	procedure := servingGated(t, tm, "members_only", &entered)
	client, m := tm.node(t, 1, true, false)

	_, err := call(context.Background(), client, tm.realm.ID, procedure, cbor.Null(), [32]byte{}, m, presented{})
	if code := providerCode(err); code != "unauthorized" {
		t.Fatalf("no note: %v", err)
	}
	// Another person's note, for another client, presented by this one.
	other, otherMembership := aPerson(t, tm, time.Now().Add(4*time.Hour))
	elsewhere := freshKey(t)
	elsewhereID, _ := elsewhere.NodeID()
	chain, _, err := delegate(other, otherMembership, tm.realm.RealmKey(), elsewhereID, time.Hour, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	_, err = call(context.Background(), client, tm.realm.ID, procedure, cbor.Null(), [32]byte{}, m, chainOf(t, chain))
	if code := providerCode(err); code != "unauthorized" {
		t.Fatalf("another person's note: %v", err)
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

func TestANoteNeverOutlivesTheMembershipItComesFrom(t *testing.T) {
	tm := newTestMesh(t)
	now := time.Now()
	membershipExp := now.Add(30 * time.Minute)
	person, membership := aPerson(t, tm, membershipExp)
	client := freshKey(t)
	id, _ := client.NodeID()
	chain, note, err := delegate(person, membership, tm.realm.RealmKey(), id, 24*time.Hour, now)
	if err != nil {
		t.Fatal(err)
	}
	if note.Expires != time.Unix(membershipExp.Unix(), 0).UTC().Format(time.RFC3339) {
		t.Fatalf("the note expires %s, the membership %s", note.Expires, membershipExp.UTC())
	}
	c, err := readClaims([]byte(strings.SplitN(string(chain), "\n", 2)[0]))
	if err != nil || c.Exp != membershipExp.Unix() {
		t.Fatalf("the note's exp %d, want %d (%v)", c.Exp, membershipExp.Unix(), err)
	}
}

func TestADelegationIsRefusedUnlessTheMembershipHoldsForThisPerson(t *testing.T) {
	tm := newTestMesh(t)
	now := time.Now()
	client := freshKey(t)
	clientID, _ := client.NodeID()

	// Another person's membership in this person's file.
	person := freshKey(t)
	_, othersMembership := aPerson(t, tm, now.Add(time.Hour))
	if _, _, err := delegate(person, othersMembership, tm.realm.RealmKey(), clientID, time.Hour, now); ucan.RefusalName(err) != "not_the_audience" {
		t.Fatalf("someone else's membership: %v", err)
	}
	// A membership the realm key did not sign.
	member, membership := aPerson(t, tm, now.Add(time.Hour))
	if _, _, err := delegate(member, membership, freshKey(t).PublicKey(), clientID, time.Hour, now); ucan.RefusalName(err) != "not_the_issuer" {
		t.Fatalf("a membership another key issued: %v", err)
	}
	// An expired membership.
	if _, _, err := delegate(member, membership, tm.realm.RealmKey(), clientID, time.Hour, now.Add(2*time.Hour)); ucan.RefusalName(err) != "expired" {
		t.Fatalf("an expired membership: %v", err)
	}
	// No membership kept.
	if _, _, err := delegate(member, filepath.Join(t.TempDir(), "none.ucan"), tm.realm.RealmKey(), clientID, time.Hour, now); err == nil ||
		!strings.Contains(err.Error(), "person join") {
		t.Fatalf("no membership: %v", err)
	}
}

func TestPersonDelegateRefusesALongNoteBeforeTouchingAKey(t *testing.T) {
	code, out, _ := runCaptured(t, "person", "delegate", "-json", "-realm", "io.macula", "-realm-key", "ab",
		"-to", strings.Repeat("ab", 32), "-ttl", "200h")
	if code != 2 || !strings.Contains(out, "168h") {
		t.Fatalf("code %d: %s", code, out)
	}
	path, _ := filepath.Abs(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "macula-cli", "person.key"))
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a person key was created for a refused invocation")
	}
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
