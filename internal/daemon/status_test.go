package daemon

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/identity"
)

// testID is a fast, deterministic identity for tests that need a real
// KeyPair (Status() calls srv.id.NodeID()) but never actually connect
// anywhere -- identity.Generate's own puzzle grinding would make every
// one of these tests unnecessarily slow for no benefit; FromSeed skips
// it entirely (see its own doc: "does not check the puzzle").
func testID(t *testing.T) identity.KeyPair {
	t.Helper()
	id, err := identity.FromSeed(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatalf("identity.FromSeed: %v", err)
	}
	return id
}

// newTestServer builds a Server with every map/field the methods under
// test touch initialized, but no real connection.Session anywhere --
// this whole file exercises only the pure connectivity/degraded-state
// bookkeeping (setSessionLost/setSessionRecovered/Status/replay
// degraded-tracking), never the mesh-facing calls that need a live
// station (those are reconnect_live_test.go's job, tag "live").
func newTestServer(t *testing.T) *Server {
	t.Helper()
	return &Server{
		id:          testID(t),
		connected:   true,
		degraded:    map[procKey]bool{},
		subs:        map[topicKey]*subscription{},
		subDegraded: map[topicKey]bool{},
	}
}

// Reproduces the live 2026-09-08 gap (Raf, via github-com-1f): before
// this, Status().ConnectedTo only ever reported the last address a
// session WAS reachable through, updated only on a successful (re)
// connect -- during an ongoing outage it silently kept reporting that
// stale address, indistinguishable from healthy. These pin the new
// Connected/LastError bookkeeping in isolation, no live mesh needed.

func TestSetSessionLost_MarksDisconnectedAndRecordsError(t *testing.T) {
	srv := newTestServer(t)
	srv.setSessionLost("connection reset")

	got := srv.Status()
	if got.Connected {
		t.Fatalf("expected Connected=false after the session was lost")
	}
	if got.LastError != "connection reset" {
		t.Fatalf("expected the loss reason as last_error, got %q", got.LastError)
	}
}

func TestSetSessionRecovered_RestoresConnectedAndClearsError(t *testing.T) {
	srv := newTestServer(t)
	srv.setSessionLost("connection reset")
	srv.setSessionRecovered("station-a:4433")

	got := srv.Status()
	if !got.Connected {
		t.Fatalf("expected Connected=true after the session recovered")
	}
	if got.LastError != "" {
		t.Fatalf("expected last_error cleared, got %q", got.LastError)
	}
}

func TestSetLogger_LogsSessionLostAndRecovered(t *testing.T) {
	srv := newTestServer(t)
	var buf bytes.Buffer
	srv.SetLogger(log.New(&buf, "", 0))

	srv.setSessionLost("connection reset")
	srv.setSessionRecovered("station-a:4433")

	out := buf.String()
	if !strings.Contains(out, "session lost") || !strings.Contains(out, "connection reset") {
		t.Fatalf("expected a logged connection-lost line, got:\n%s", out)
	}
	if !strings.Contains(out, "session reconnected") || !strings.Contains(out, "station-a:4433") {
		t.Fatalf("expected a logged reconnect line, got:\n%s", out)
	}
}

func TestSetLogger_NilLoggerIsSilentByDefault(t *testing.T) {
	srv := newTestServer(t)
	// No SetLogger call at all -- must not panic, must produce nothing
	// observable (there's nothing to assert against beyond "it doesn't
	// crash", which is the actual property this guards).
	srv.setSessionLost("connection reset")
	srv.setSessionRecovered("station-a:4433")
}

func TestStatus_ServingDegradedListsOnlyProceduresMarkedDegraded(t *testing.T) {
	srv := newTestServer(t)
	healthy := procKey{realmHex: "00", procedure: "healthy.proc"}
	broken := procKey{realmHex: "00", procedure: "broken.proc"}
	srv.order = []procKey{healthy, broken}
	srv.handlers = map[procKey]connection.CallHandler{healthy: nil, broken: nil}
	srv.degraded[broken] = true

	got := srv.Status()
	if len(got.Serving) != 2 {
		t.Fatalf("expected both procedures still listed in Serving, got %v", got.Serving)
	}
	if len(got.ServingDegraded) != 1 || got.ServingDegraded[0] != "broken.proc" {
		t.Fatalf("expected only broken.proc in ServingDegraded, got %v", got.ServingDegraded)
	}
}

func TestStatus_SubscribedDegradedListsOnlyTopicsMarkedDegraded(t *testing.T) {
	srv := newTestServer(t)
	healthy := topicKey{realmHex: "00", topic: "healthy.topic"}
	broken := topicKey{realmHex: "00", topic: "broken.topic"}
	srv.subs[healthy] = &subscription{watchers: map[chan PubsubEventNotification]struct{}{}}
	srv.subs[broken] = &subscription{watchers: map[chan PubsubEventNotification]struct{}{}}
	srv.subDegraded[broken] = true

	got := srv.Status()
	if len(got.Subscribed) != 2 {
		t.Fatalf("expected both topics still listed in Subscribed, got %v", got.Subscribed)
	}
	if len(got.SubscribedDegraded) != 1 || got.SubscribedDegraded[0] != "broken.topic" {
		t.Fatalf("expected only broken.topic in SubscribedDegraded, got %v", got.SubscribedDegraded)
	}
}

func TestStatus_ConnectedTrueAndNoDegradedWhenNothingHasFailed(t *testing.T) {
	srv := newTestServer(t)
	got := srv.Status()
	if !got.Connected {
		t.Fatalf("expected Connected=true for a freshly built, never-degraded server")
	}
	if got.LastError != "" {
		t.Fatalf("expected empty last_error, got %q", got.LastError)
	}
	if len(got.ServingDegraded) != 0 || len(got.SubscribedDegraded) != 0 {
		t.Fatalf("expected no degraded entries, got serving=%v subscribed=%v", got.ServingDegraded, got.SubscribedDegraded)
	}
}
