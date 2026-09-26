package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/devicerequest"
	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/pool"
	"github.com/macula-io/macula-go/profile"
	"github.com/macula-io/macula-go/record"
)

func TestAWatchHearsAPublicationOnce(t *testing.T) {
	tm := newTestMesh(t)
	listener, _ := tm.node(t, 0, false, false)
	publisher, _ := tm.node(t, 0, false, false)
	ready := make(chan struct{})
	heard := make(chan heardEvent, 4)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan int, 1)
	go func() {
		n, _ := watch(ctx, listener, tm.realm.ID, "mcl-cli/tests/greeting_sent_v1", 1, func() { close(ready) },
			func(e heardEvent) { heard <- e })
		done <- n
	}()
	<-ready
	time.Sleep(200 * time.Millisecond)
	if err := publish(publisher, tm.realm.ID, "mcl-cli/tests/greeting_sent_v1", cbor.Text("hi"), 0); err != nil {
		t.Fatal(err)
	}
	if n := <-done; n != 1 {
		t.Fatalf("heard %d events, want 1", n)
	}
	e := <-heard
	if want := publisher.NodeID(); e.Publisher != hexOf(want[:]) || string(e.Payload) != `"hi"` {
		t.Fatalf("heard %+v", e)
	}
}

func TestTheStreamProbeRoundTripsThroughTwoStations(t *testing.T) {
	tm := newTestMesh(t)
	provider, _ := tm.node(t, 0, false, false)
	caller, _ := tm.node(t, 1, false, false)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	r, err := streamProbe(ctx, provider, caller, tm.realm.ID, 4, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if r.Chunks != 4 || r.Bytes != 12000 || !strings.HasPrefix(r.Procedure, "~") {
		t.Fatalf("probe %+v", r)
	}
}

func TestTheContentProbeSharesAndFetchesChunkedContent(t *testing.T) {
	tm := newTestMesh(t)
	sharer, _ := tm.node(t, 0, false, false)
	fetcher, _ := tm.node(t, 1, false, false)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r, err := contentProbe(ctx, sharer, fetcher, tm.realm.ID, 300_000)
	if err != nil {
		t.Fatal(err)
	}
	mcid, err := parseMcid(r.Mcid)
	if err != nil || mcid[1] != 0x56 {
		t.Fatalf("content id %s, want a manifest (codec 0x56) for chunked content", r.Mcid)
	}
}

func TestAContentIDMustBe50Bytes(t *testing.T) {
	if _, err := parseMcid(strings.Repeat("02", 49)); err == nil {
		t.Fatal("a 49-byte content id parsed")
	}
}

func TestTheDHTHoldsTheStationsOwnEndpointRecords(t *testing.T) {
	tm := newTestMesh(t)
	p, _ := tm.node(t, 0, false, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	found, err := findByType(ctx, p, recordTypes["station_endpoint"])
	if err != nil {
		t.Fatal(err)
	}
	var keyIDs []string
	for _, r := range found.Records {
		keyIDs = append(keyIDs, r.KeyID)
	}
	for _, s := range tm.stations {
		if !strings.Contains(strings.Join(keyIDs, ","), hexOf(s.NodeID[:])) {
			t.Fatalf("no endpoint record by station %x in %v", s.NodeID[:4], keyIDs)
		}
	}
}

func TestRecordTypesAreReadByNameOrNumber(t *testing.T) {
	for text, want := range map[string]record.Type{"node_record": 0x01, "0x12": 0x12, "21": 0x15} {
		if got, err := recordType(text); err != nil || got != want {
			t.Errorf("%s: %v %v, want %v", text, got, err, want)
		}
	}
	if _, err := recordType("nonsense"); err == nil {
		t.Error("an unknown type name parsed")
	}
}

func TestConnectReportsTheLinkUpToThePinnedStation(t *testing.T) {
	tm := newTestMesh(t)
	m := tm.flags(0, false)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := connectStages(ctx, m, freshKey(t))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Up || r.Station != hexOf(tm.stations[0].NodeID[:]) || len(r.Addresses) == 0 {
		t.Fatalf("connect %+v", r)
	}
}

func TestConnectRefusesAStationThatDoesNotProveThePinnedNodeID(t *testing.T) {
	tm := newTestMesh(t)
	m := tm.flags(0, false)
	m.seeds[0].NodeID = tm.stations[1].NodeID
	m.timeout = 3 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := connectStages(ctx, m, freshKey(t)); err == nil {
		t.Fatal("linked to a station pinned by another station's node_id")
	}
}

func TestSeedsArePinnedAndParsed(t *testing.T) {
	id := strings.Repeat("ab", 32)
	for text, want := range map[string]pool.Seed{
		"station.example:4433@" + id: {Host: "station.example", Port: 4433},
		"[2a01:4f8::1]:5000@" + id:   {Host: "2a01:4f8::1", Port: 5000},
		"station.example@" + id:      {Host: "station.example", Port: 4433},
	} {
		got, err := parseSeed(text)
		if err != nil || got.Host != want.Host || got.Port != want.Port || hexOf(got.NodeID[:]) != id {
			t.Errorf("%s: %+v %v", text, got, err)
		}
	}
	for _, bad := range []string{"station.example:4433", "h:0@" + id, "h@abcd"} {
		if _, err := parseSeed(bad); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

func TestARealmIsItsHexIDOrTheSHA256OfItsName(t *testing.T) {
	if got, _ := realmID("io.macula"); got != sha256.Sum256([]byte("io.macula")) {
		t.Fatal("a realm name is not its sha256")
	}
	id := strings.Repeat("0f", 32)
	if got, _ := realmID(id); hexOf(got[:]) != id {
		t.Fatal("a hex realm id was hashed")
	}
}

func TestOwnProcedureExpandsOnlyTheShorthand(t *testing.T) {
	node := [32]byte{1}
	if got := ownProcedure("~/echo", node); got != "~"+hexOf(node[:])+"/echo" {
		t.Fatal(got)
	}
	if got := ownProcedure("acme/echo", node); got != "acme/echo" {
		t.Fatal(got)
	}
}

func TestTheIdentityIsDescribed(t *testing.T) {
	key := freshKey(t)
	r, err := describeKey(key)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := key.NodeID()
	if r.NodeID != hexOf(id[:]) || r.Profile != "pq_pure" || r.PublicKeyBytes != len(key.PublicKey()) {
		t.Fatalf("%+v", r)
	}
}

// fakeRealm verifies each join request's v2 proof as macula-realm does, and
// answers pending until admitted is set.
type fakeRealm struct {
	name     string
	admitted atomic.Bool
	polls    atomic.Int32
	t        *testing.T
}

func (f *fakeRealm) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/api/v1/join/sessions":
		body, _ := io.ReadAll(r.Body)
		if reason := f.verify(body); reason != "" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": reason})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(joinSession{SessionID: "s-1", JoinURL: "https://realm.test/join/s-1", ExpiresAt: "later"})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v1/join/sessions/s-1":
		f.polls.Add(1)
		if f.admitted.Load() {
			_ = json.NewEncoder(w).Encode(sessionStatus{Status: "confirmed", OrgIdentity: "mri:org:" + f.name + "/x", CitizenDID: "did"})
			return
		}
		_ = json.NewEncoder(w).Encode(sessionStatus{Status: "pending", ExpiresAt: "later"})
	default:
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "session_not_found"})
	}
}

// verify rebuilds the signed request from the body, as the realm does, and
// checks the proof over it with the carried key.
func (f *fakeRealm) verify(body []byte) string {
	var envelope struct {
		PublicKey string              `json:"public_key"`
		Proof     devicerequest.Proof `json:"proof"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "not_a_json_object"
	}
	carried, err := base64Decode(envelope.PublicKey)
	if err != nil {
		return "invalid_base64_encoding"
	}
	request, err := devicerequest.JSONRequest(body)
	if err != nil {
		return err.Error()
	}
	nonce, _ := hex.DecodeString(envelope.Proof.Nonce)
	signature, _ := hex.DecodeString(envelope.Proof.Signature)
	if envelope.Proof.V != 2 || len(nonce) != 16 {
		return "unsupported_version"
	}
	message := devicerequest.Message(carried, sha256.Sum256([]byte(f.name)), devicerequest.ProcedureJoinSession,
		envelope.Proof.Timestamp, [16]byte(nonce), request)
	if !identity.Verify(message, signature, carried, profile.PQPure) {
		return "bad_proof"
	}
	return ""
}

func TestAJoinRequestCarriesAV2ProofTheRealmVerifies(t *testing.T) {
	realm := &fakeRealm{name: "io.macula", t: t}
	server := httptest.NewServer(realm)
	defer server.Close()
	session, err := requestJoin(context.Background(), server.Client(), server.URL, "io.macula", freshKey(t),
		map[string]any{"hostname": "laptop.local", "note": nil, "n": 3})
	if err != nil {
		t.Fatal(err)
	}
	if session.SessionID != "s-1" || session.JoinURL == "" {
		t.Fatalf("session %+v", session)
	}
}

func TestAJoinSignedForAnotherRealmIsRefused(t *testing.T) {
	server := httptest.NewServer(&fakeRealm{name: "io.macula", t: t})
	defer server.Close()
	_, err := requestJoin(context.Background(), server.Client(), server.URL, "elsewhere", freshKey(t), map[string]any{"hostname": "h"})
	var refusal *realmRefusal
	if !errors.As(err, &refusal) || refusal.Reason != "bad_proof" {
		t.Fatalf("%v, want the realm's bad_proof", err)
	}
}

func TestWaitingForAdmissionPollsUntilConfirmed(t *testing.T) {
	realm := &fakeRealm{name: "io.macula", t: t}
	server := httptest.NewServer(realm)
	defer server.Close()
	go func() {
		time.Sleep(150 * time.Millisecond)
		realm.admitted.Store(true)
	}()
	status, err := waitAdmitted(context.Background(), server.Client(), server.URL, "s-1", 5*time.Second, 20*time.Millisecond)
	if err != nil || status.Status != "confirmed" || realm.polls.Load() < 2 {
		t.Fatalf("status %+v after %d polls, %v", status, realm.polls.Load(), err)
	}
}

func TestWaitingStopsPendingWhenTheWaitRunsOut(t *testing.T) {
	server := httptest.NewServer(&fakeRealm{name: "io.macula", t: t})
	defer server.Close()
	status, err := waitAdmitted(context.Background(), server.Client(), server.URL, "s-1", 100*time.Millisecond, 20*time.Millisecond)
	if err != nil || status.Status != "pending" {
		t.Fatalf("status %+v, %v", status, err)
	}
}

func TestAMembershipRequestIsSignedOverThePayloadLessItsProof(t *testing.T) {
	key := freshKey(t)
	id, _ := key.NodeID()
	var sent pool.Call
	fake := func(_ context.Context, c pool.Call) (cbor.Value, error) {
		sent = c
		return cbor.Map([]cbor.MapEntry{{Key: cbor.Text("citizen_did"), Val: cbor.Bytes([]byte(hexOf(id[:])))}}), nil
	}
	r, err := requestMembership(context.Background(), fake, key, "io.macula", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if sent.Procedure != "io.macula/_realm/_realm/identity/issue_membership_ucan_v1" || sent.Realm != sha256.Sum256([]byte("io.macula")) {
		t.Fatalf("called %s in %x", sent.Procedure, sent.Realm)
	}
	if r.CitizenDID != hexOf(id[:]) {
		t.Fatalf("citizen_did %q", r.CitizenDID)
	}
	carried, _ := sent.Payload.Get("public_key")
	proof, _ := sent.Payload.Get("proof")
	field := func(name string) cbor.Value { v, _ := proof.Get(name); return v }
	ts, _ := field("timestamp").AsInt64()
	nonceText, _ := field("nonce").AsText()
	sigText, _ := field("signature").AsText()
	nonce, _ := hex.DecodeString(nonceText)
	signature, _ := hex.DecodeString(sigText)
	carriedText, _ := carried.AsText()
	public, _ := base64Decode(carriedText)
	request := cbor.Map([]cbor.MapEntry{{Key: cbor.Text("public_key"), Val: carried}})
	message := devicerequest.Message(public, sent.Realm, devicerequest.ProcedureMembershipUCAN, uint64(ts), [16]byte(nonce), request)
	if !identity.Verify(message, signature, public, profile.PQPure) {
		t.Fatal("the membership proof does not verify over the payload less its proof")
	}
}
