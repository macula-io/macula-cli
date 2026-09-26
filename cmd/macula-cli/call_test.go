package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/stationlink"
)

// serving runs serve in the background until the test ends, and waits for
// its advertisement to be findable.
func serving(t *testing.T, tm *testMesh, i int, procedure string, o serveOptions, seen func(servedCall)) string {
	t.Helper()
	p, _ := tm.node(t, i, true, strings.Contains(procedure, "/") && !strings.HasPrefix(procedure, "~"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	full := ownProcedure(procedure, p.NodeID())
	go func() {
		defer close(done)
		if seen == nil {
			seen = func(servedCall) {}
		}
		if _, err := serve(ctx, p, tm.realm.ID, procedure, o, seen); err != nil && ctx.Err() == nil {
			t.Errorf("serve: %v", err)
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for !tm.stations[i].Advertised(tm.realm.ID, full) {
		if time.Now().After(deadline) {
			t.Fatalf("%s never advertised", full)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return full
}

func TestACallReachesAnOrgProcedureOnAnotherStationAndEchoes(t *testing.T) {
	tm := newTestMesh(t)
	procedure := serving(t, tm, 0, tm.realm.Org+"/echo", serveOptions{}, nil)
	caller, m := tm.node(t, 1, true, false)
	payload := cbor.Map([]cbor.MapEntry{{Key: cbor.Text("n"), Val: cbor.Int(-3)}, {Key: cbor.Text("b"), Val: cbor.Bytes([]byte{1})}})
	got, err := call(context.Background(), caller, tm.realm.ID, procedure, payload, [32]byte{}, m)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Result) != `{"b":{"$bytes":"AQ=="},"n":-3}` {
		t.Fatalf("result %s", got.Result)
	}
}

func TestServeAnswersAFixedReplyAndReportsTheCaller(t *testing.T) {
	tm := newTestMesh(t)
	reply := cbor.Text("pong")
	calls := make(chan servedCall, 1)
	procedure := serving(t, tm, 0, "~/ping", serveOptions{reply: &reply}, func(c servedCall) { calls <- c })
	caller, m := tm.node(t, 1, false, false)
	got, err := call(context.Background(), caller, tm.realm.ID, procedure, cbor.Text("ping"), [32]byte{}, m)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Result) != `"pong"` {
		t.Fatalf("result %s", got.Result)
	}
	c := <-calls
	if want := caller.NodeID(); c.Caller != hexOf(want[:]) || string(c.Payload) != `"ping"` {
		t.Fatalf("seen %+v", c)
	}
}

func TestServeOnceStopsAfterOneCall(t *testing.T) {
	tm := newTestMesh(t)
	p, _ := tm.node(t, 0, false, false)
	done := make(chan error, 1)
	go func() {
		_, err := serve(context.Background(), p, tm.realm.ID, "~/one", serveOptions{once: true}, func(servedCall) {})
		done <- err
	}()
	full := ownProcedure("~/one", p.NodeID())
	for !tm.stations[0].Advertised(tm.realm.ID, full) {
		time.Sleep(20 * time.Millisecond)
	}
	caller, m := tm.node(t, 1, false, false)
	if _, err := call(context.Background(), caller, tm.realm.ID, full, cbor.Int(1), [32]byte{}, m); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve -once did not stop after a call")
	}
}

func TestAnUnpinnedRealmAndAnUnservedProcedureAreRefused(t *testing.T) {
	tm := newTestMesh(t)
	caller, m := tm.node(t, 0, true, false)
	if _, err := call(context.Background(), caller, [32]byte{1}, tm.realm.Org+"/echo", cbor.Null(), [32]byte{}, m); err == nil {
		t.Fatal("a call in a realm with no pinned key went through")
	}
	_, err := call(context.Background(), caller, tm.realm.ID, tm.realm.Org+"/nothing", cbor.Null(), [32]byte{}, m)
	var provider *stationlink.ProviderError
	if err == nil || errors.As(err, &provider) {
		t.Fatalf("%v, want no provider", err)
	}
}
