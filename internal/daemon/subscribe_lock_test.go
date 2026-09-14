package daemon

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/frame"
	"github.com/macula-io/macula-go/identity"
)

// stallingSubscribe stands in for a SUBSCRIBE whose write stalls: each call
// counts itself, says so on stalled, waits for release, then fails.
func stallingSubscribe(calls *atomic.Int32, stalled chan<- struct{}, release <-chan struct{}) func(*connection.Session, frame.SubscribeSpec, identity.KeyPair) (*connection.Subscription, error) {
	return func(*connection.Session, frame.SubscribeSpec, identity.KeyPair) (*connection.Subscription, error) {
		calls.Add(1)
		stalled <- struct{}{}
		<-release
		return nil, errors.New("the station went away")
	}
}

// A SUBSCRIBE still being written doesn't hold the subscription registry:
// while it stalls, an unsubscribe and a status report go on.
func TestAStalledSubscribeDoesNotBlockUnsubscribeOrStatus(t *testing.T) {
	srv := newTestServer(t)
	var calls atomic.Int32
	stalled, release := make(chan struct{}, 2), make(chan struct{})
	srv.subscribeOn = stallingSubscribe(&calls, stalled, release)
	subscribed := make(chan error, 1)
	go func() {
		_, err := srv.ensureSubscription(make([]byte, 32), "stalled.topic")
		subscribed <- err
	}()
	<-stalled

	done := make(chan struct{})
	go func() {
		_, _ = srv.Unsubscribe(PubsubUnsubscribeParams{Topic: "another.topic"})
		_ = srv.Status()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("an unsubscribe and a status report waited for a stalled SUBSCRIBE")
	}
	close(release)
	if err := <-subscribed; err == nil {
		t.Error("the stalled SUBSCRIBE's failure was not reported")
	}
}

// Requests for one topic while its SUBSCRIBE is in flight share that
// SUBSCRIBE: the daemon writes it once, and every request gets its outcome.
func TestRequestsForATopicWhoseSubscribeIsInFlightShareIt(t *testing.T) {
	srv := newTestServer(t)
	var calls atomic.Int32
	stalled, release := make(chan struct{}, 2), make(chan struct{})
	srv.subscribeOn = stallingSubscribe(&calls, stalled, release)
	errs := make(chan error, 2)
	request := func() {
		_, err := srv.ensureSubscription(make([]byte, 32), "shared.topic")
		errs <- err
	}
	go request()
	<-stalled
	go request()
	// Time for the second request to reach the registry while the first
	// SUBSCRIBE is still stalled.
	time.Sleep(200 * time.Millisecond)
	close(release)

	for range 2 {
		if err := <-errs; err == nil {
			t.Error("a request did not get the SUBSCRIBE's failure")
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("SUBSCRIBE written %d times, want once", n)
	}
}
