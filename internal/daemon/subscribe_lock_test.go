package daemon

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/frame"
	"github.com/macula-io/macula-go/identity"
)

// subscribeFunc is what Server.subscribeOn holds.
type subscribeFunc = func(*connection.Session, frame.SubscribeSpec, identity.KeyPair) (liveSubscription, error)

// stallingSubscribe stands in for a SUBSCRIBE whose write stalls: each call
// counts itself, says so on stalled, waits for release, then fails.
func stallingSubscribe(calls *atomic.Int32, stalled chan<- struct{}, release <-chan struct{}) subscribeFunc {
	return func(*connection.Session, frame.SubscribeSpec, identity.KeyPair) (liveSubscription, error) {
		calls.Add(1)
		stalled <- struct{}{}
		<-release
		return nil, errors.New("the station went away")
	}
}

// releasedSubscribe stands in for a SUBSCRIBE that succeeds once released: each
// call counts itself, says so on started, waits for release, then returns live.
func releasedSubscribe(calls *atomic.Int32, started chan<- struct{}, release <-chan struct{}, live liveSubscription) subscribeFunc {
	return func(*connection.Session, frame.SubscribeSpec, identity.KeyPair) (liveSubscription, error) {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return live, nil
	}
}

// fakeLive stands in for a live subscription: it receives nothing, and Close
// ends it the way an UNSUBSCRIBE does.
type fakeLive struct {
	closed    chan struct{}
	closeOnce sync.Once
}

func newFakeLive() *fakeLive { return &fakeLive{closed: make(chan struct{})} }

// Recv waits for timeout, or until the subscription is closed.
func (f *fakeLive) Recv(timeout time.Duration) (frame.EventInfo, error) {
	select {
	case <-f.closed:
		return frame.EventInfo{}, errors.New("fakeLive: closed")
	case <-time.After(timeout):
		return frame.EventInfo{}, connection.ErrRecvTimeout
	}
}

func (f *fakeLive) Close() error {
	f.closeOnce.Do(func() { close(f.closed) })
	return nil
}

func (f *fakeLive) isClosed() bool {
	select {
	case <-f.closed:
		return true
	default:
		return false
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
	shared := make(chan struct{}, 2)
	srv.sharedSubscription = func(string) { shared <- struct{}{} }
	errs := make(chan error, 2)
	request := func() {
		_, err := srv.ensureSubscription(make([]byte, 32), "shared.topic")
		errs <- err
	}
	go request()
	<-stalled
	go request()
	select {
	case <-shared:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("the second request never found the first request's subscription while its SUBSCRIBE was in flight")
	}
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

// An unsubscribe while a topic's first SUBSCRIBE is in flight ends the
// subscription: when that SUBSCRIBE then succeeds, the topic stays untracked
// and the live subscription it returned is closed, so the station gets its
// UNSUBSCRIBE.
func TestAnUnsubscribeWhileTheSubscribeIsInFlightClosesTheLateSubscription(t *testing.T) {
	srv := newTestServer(t)
	var calls atomic.Int32
	started, release := make(chan struct{}, 1), make(chan struct{})
	late := newFakeLive()
	t.Cleanup(func() { _ = late.Close() })
	srv.subscribeOn = releasedSubscribe(&calls, started, release, late)
	subscribed := make(chan struct{})
	go func() {
		_, _ = srv.ensureSubscription(make([]byte, 32), "unsubscribed.topic")
		close(subscribed)
	}()
	<-started
	res, err := srv.Unsubscribe(PubsubUnsubscribeParams{Topic: "unsubscribed.topic"})
	close(release)
	<-subscribed

	if err != nil || !res.Unsubscribed {
		t.Errorf("Unsubscribe while the SUBSCRIBE was in flight = %+v, %v; want the topic unsubscribed", res, err)
	}
	if !late.isClosed() {
		t.Error("the SUBSCRIBE that succeeded after the unsubscribe left its live subscription open")
	}
	if topics := srv.subscriptionTopics(); len(topics) != 0 {
		t.Errorf("topics tracked after the unsubscribe = %q, want none", topics)
	}
}

// A failed SUBSCRIBE drops its claim, so the next request for the topic writes
// a SUBSCRIBE of its own and gets that one's outcome.
func TestTheRequestAfterAFailedSubscribeTriesAgain(t *testing.T) {
	srv := newTestServer(t)
	var failed, succeeded atomic.Int32
	started, release := make(chan struct{}, 2), make(chan struct{})
	close(release)
	srv.subscribeOn = stallingSubscribe(&failed, started, release)
	if _, err := srv.ensureSubscription(make([]byte, 32), "retried.topic"); err == nil {
		t.Fatal("the failed SUBSCRIBE's failure was not reported")
	}
	live := newFakeLive()
	t.Cleanup(func() { _ = live.Close() })
	srv.subscribeOn = releasedSubscribe(&succeeded, started, release, live)
	if _, err := srv.ensureSubscription(make([]byte, 32), "retried.topic"); err != nil {
		t.Fatalf("the request after a failed SUBSCRIBE = %v, want its own SUBSCRIBE's success", err)
	}
	if failed.Load() != 1 || succeeded.Load() != 1 {
		t.Errorf("SUBSCRIBEs written: %d failed and %d succeeded, want one of each", failed.Load(), succeeded.Load())
	}
}
