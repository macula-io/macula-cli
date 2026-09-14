package daemon

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/frame"

	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// topicKey identifies one daemon-owned subscription. Realm is
// hex-encoded for the same reason procKey does it: a comparable map
// key without a second index.
type topicKey struct {
	realmHex string
	topic    string
}

// liveSubscription is what the daemon uses of a connection.Subscription, as an
// interface so a test can stand in for one.
type liveSubscription interface {
	Recv(timeout time.Duration) (frame.EventInfo, error)
	Close() error
}

// subscription is one daemon-owned mesh subscription, independent of
// how many (if any) control-socket connections are currently watching
// it -- "subscribe" creates durable state, "watch" just taps into it,
// matching PubsubSubscribeParams's own doc on why these are separate
// verbs. live is its connection.Subscription on the daemon's current
// session, replaced after a reconnect; forwardEvents hands what live
// receives to every watcher. Once ended, a subscription takes no new
// watcher and no new live subscription.
type subscription struct {
	mu       sync.Mutex
	watchers map[chan PubsubEventNotification]struct{}
	live     liveSubscription
	ended    bool

	// ready is closed once the SUBSCRIBE that created this subscription has
	// been written, or has failed with err. A subscription made any other
	// way has no ready channel, and never waits.
	ready chan struct{}
	err   error
}

// subscriptionPollInterval bounds one Subscription.Recv wait. Not a wire
// timeout: a forwarder simply waits again.
const subscriptionPollInterval = 2 * time.Second

// subscribe issues spec's SUBSCRIBE on sess as the daemon's identity, through
// subscribeOn when a test has set it.
func (srv *Server) subscribe(sess *connection.Session, spec frame.SubscribeSpec) (liveSubscription, error) {
	if srv.subscribeOn != nil {
		return srv.subscribeOn(sess, spec, srv.id)
	}
	live, err := sess.Subscribe(spec, srv.id)
	if err != nil {
		return nil, err
	}
	return live, nil
}

// replaySubscriptions subscribes every topic this daemon tracks on a
// freshly (re)connected session -- the pub/sub analogue of
// replayAdvertisements (server.go), mirroring
// macula_client_replay:subs_to/2. Best-effort for the same reason: a
// topic whose re-subscribe fails stays in srv.subs, marked degraded, and
// is retried on the next reconnect rather than silently dropped.
func (srv *Server) replaySubscriptions(sess *connection.Session) {
	srv.subsMu.Lock()
	tracked := make(map[topicKey]*subscription, len(srv.subs))
	for k, sub := range srv.subs {
		tracked[k] = sub
	}
	srv.subsMu.Unlock()
	for k, sub := range tracked {
		srv.replaySubscription(sess, k, sub)
	}
}

// replaySubscription subscribes k again on sess for sub. A subscription whose
// first SUBSCRIBE is still in flight is waited for first, since that SUBSCRIBE
// may have gone out on the session this replay replaces; one that has ended
// meanwhile is left alone.
func (srv *Server) replaySubscription(sess *connection.Session, k topicKey, sub *subscription) {
	realm, err := hex.DecodeString(k.realmHex)
	if err != nil || sub.waitReady() != nil || sub.isEnded() {
		return
	}
	live, subErr := srv.subscribe(sess, frame.NewSubscribeSpec(k.topic, realm, srv.id.NodeID()))
	srv.setSubscriptionDegraded(k, sub, subErr != nil)
	if subErr != nil {
		srv.log("daemon: re-subscribe %s (realm %s) after reconnect failed, still tracked but not receiving events until the next successful replay: %v", k.topic, k.realmHex, subErr)
		return
	}
	if sub.follow(live) {
		go srv.forwardEvents(k, sub, live)
	}
}

// setSubscriptionDegraded records whether k's most recent (re)subscribe
// failed, as long as sub is still the subscription tracked for k.
func (srv *Server) setSubscriptionDegraded(k topicKey, sub *subscription, degraded bool) {
	srv.subsMu.Lock()
	defer srv.subsMu.Unlock()
	if srv.subs[k] == sub {
		srv.subDegraded[k] = degraded
	}
}

// forwardEvents hands every event live receives to sub's watchers, until
// live ends: replaced after a reconnect, closed by Unsubscribe, or ended
// with its session. A subscription that fell behind its queue is replaced
// on the current session first and closed after, so the station
// keeps the topic subscribed throughout.
func (srv *Server) forwardEvents(k topicKey, sub *subscription, live liveSubscription) {
	for {
		evt, err := live.Recv(subscriptionPollInterval)
		switch {
		case err == nil:
			sub.deliver(eventNotification(evt))
		case errors.Is(err, connection.ErrRecvTimeout):
		case errors.Is(err, connection.ErrConsumerOverflow):
			next, ok := srv.replaceOverflowed(k, sub, live)
			if !ok {
				return
			}
			live = next
		default:
			return
		}
	}
}

// replaceOverflowed subscribes k again on the daemon's current session,
// then closes live, the subscription that fell behind. It reports false
// when there is nothing left to forward: the replacement failed, or sub
// has ended or moved on to another live subscription meanwhile.
func (srv *Server) replaceOverflowed(k topicKey, sub *subscription, live liveSubscription) (liveSubscription, bool) {
	realm, err := hex.DecodeString(k.realmHex)
	if err != nil {
		_ = live.Close()
		return nil, false
	}
	next, subErr := srv.subscribe(srv.session.Load(), frame.NewSubscribeSpec(k.topic, realm, srv.id.NodeID()))
	if subErr != nil {
		_ = live.Close()
		srv.setSubscriptionDegraded(k, sub, true)
		srv.log("daemon: replacing the subscription to %s (realm %s) that fell behind failed, still tracked but not receiving events until the next successful replay: %v", k.topic, k.realmHex, subErr)
		return nil, false
	}
	if !sub.swap(live, next) {
		_ = next.Close()
		_ = live.Close()
		return nil, false
	}
	_ = live.Close()
	return next, true
}

// follow makes live this subscription's current one, closing the one it
// replaces. It reports false, closing live instead, once the subscription
// has ended.
func (sub *subscription) follow(live liveSubscription) bool {
	sub.mu.Lock()
	if sub.ended {
		sub.mu.Unlock()
		_ = live.Close()
		return false
	}
	prev := sub.live
	sub.live = live
	sub.mu.Unlock()
	if prev != nil {
		_ = prev.Close()
	}
	return true
}

// swap makes next this subscription's current one in place of prev. It
// reports false when the subscription has ended or prev is no longer its
// current one.
func (sub *subscription) swap(prev, next liveSubscription) bool {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.ended || sub.live != prev {
		return false
	}
	sub.live = next
	return true
}

// waitReady waits for the SUBSCRIBE that created sub, when it is still in
// flight, and returns that SUBSCRIBE's error.
func (sub *subscription) waitReady() error {
	if sub.ready != nil {
		<-sub.ready
	}
	return sub.err
}

// isEnded reports whether sub has ended.
func (sub *subscription) isEnded() bool {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	return sub.ended
}

// deliver hands out to every watcher without waiting on any of them.
func (sub *subscription) deliver(out PubsubEventNotification) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	for ch := range sub.watchers {
		select {
		case ch <- out:
		default:
			// A slow watcher drops an event rather than blocking every
			// other watcher, or this topic's forwarder, on one laggard.
		}
	}
}

// addWatcher attaches out, and reports false when the subscription has
// already ended.
func (sub *subscription) addWatcher(out chan PubsubEventNotification) bool {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if sub.ended {
		return false
	}
	sub.watchers[out] = struct{}{}
	return true
}

func (sub *subscription) removeWatcher(out chan PubsubEventNotification) {
	sub.mu.Lock()
	defer sub.mu.Unlock()
	delete(sub.watchers, out)
}

// end closes every watcher and the live subscription, and keeps a later
// replay from starting another. It returns the live subscription's Close
// error, which is how a failed UNSUBSCRIBE surfaces.
func (sub *subscription) end() error {
	sub.mu.Lock()
	sub.ended = true
	for ch := range sub.watchers {
		close(ch)
	}
	sub.watchers = nil
	live := sub.live
	sub.live = nil
	sub.mu.Unlock()
	if live == nil {
		return nil
	}
	return live.Close()
}

func eventNotification(evt frame.EventInfo) PubsubEventNotification {
	return PubsubEventNotification{
		Topic:        evt.Topic,
		Publisher:    hex.EncodeToString(evt.Publisher),
		Seq:          evt.Seq,
		Payload:      wirevalue.ToJSON(evt.Payload),
		DeliveredVia: evt.DeliveredVia,
		ReceivedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func (srv *Server) closeAllSubscriptions() {
	srv.subsMu.Lock()
	subs := srv.subs
	srv.subs = map[topicKey]*subscription{}
	srv.subDegraded = map[topicKey]bool{}
	srv.subsMu.Unlock()
	for _, sub := range subs {
		_ = sub.end()
	}
}

// ensureSubscription creates (realm, topic)'s subscription -- issuing
// the actual wire SUBSCRIBE on the daemon's session -- the first time it's
// asked for, and returns the existing one on every call after. The SUBSCRIBE
// is written outside subsMu, so a write that stalls holds up no unsubscribe,
// replay or status report; a request that arrives while it is in flight waits
// for its outcome instead of writing a second one.
func (srv *Server) ensureSubscription(realm []byte, topic string) (*subscription, error) {
	key := topicKey{hex.EncodeToString(realm), topic}
	sub, claimed := srv.claimSubscription(key)
	if claimed {
		srv.subscribeClaimed(key, sub, realm, topic)
	}
	if !claimed && srv.sharedSubscription != nil {
		srv.sharedSubscription(topic)
	}
	if err := sub.waitReady(); err != nil {
		return nil, err
	}
	return sub, nil
}

// claimSubscription returns key's subscription, first creating one that has
// yet to subscribe when there is none, and reports whether it did.
func (srv *Server) claimSubscription(key topicKey) (*subscription, bool) {
	srv.subsMu.Lock()
	defer srv.subsMu.Unlock()
	if sub, ok := srv.subs[key]; ok {
		return sub, false
	}
	sub := &subscription{watchers: map[chan PubsubEventNotification]struct{}{}, ready: make(chan struct{})}
	srv.subs[key] = sub
	return sub, true
}

// subscribeClaimed writes the SUBSCRIBE for sub, which claimSubscription just
// created for key, and starts forwarding its events. A failed SUBSCRIBE drops
// the claim, so the next request tries again. Either way, every request
// waiting on sub learns the outcome when this returns.
func (srv *Server) subscribeClaimed(key topicKey, sub *subscription, realm []byte, topic string) {
	defer close(sub.ready)
	live, err := srv.subscribe(srv.session.Load(), frame.NewSubscribeSpec(topic, realm, srv.id.NodeID()))
	if err != nil {
		sub.err = fmt.Errorf("subscribe: %w", err)
		srv.forgetSubscription(key, sub)
		return
	}
	if sub.follow(live) {
		srv.setSubscriptionDegraded(key, sub, false)
		go srv.forwardEvents(key, sub, live)
	}
}

// forgetSubscription stops tracking sub under key, as long as sub is still the
// subscription tracked there, and ends it.
func (srv *Server) forgetSubscription(key topicKey, sub *subscription) {
	srv.subsMu.Lock()
	if srv.subs[key] == sub {
		delete(srv.subs, key)
		delete(srv.subDegraded, key)
	}
	srv.subsMu.Unlock()
	_ = sub.end()
}

// Subscribe creates (or confirms) a durable subscription to
// (p.RealmHex, p.Topic). A Subscribe racing an Unsubscribe of the same topic
// can come back Subscribed for a subscription the Unsubscribe has already
// ended: whichever of the two changes the daemon's registry last wins.
func (srv *Server) Subscribe(p PubsubSubscribeParams) (PubsubSubscribeResult, error) {
	realm, err := parseRealmHex(p.RealmHex)
	if err != nil {
		return PubsubSubscribeResult{}, err
	}
	if _, err := srv.ensureSubscription(realm, p.Topic); err != nil {
		return PubsubSubscribeResult{}, err
	}
	return PubsubSubscribeResult{Subscribed: true, Topic: p.Topic}, nil
}

// Unsubscribe ends a subscription: unsubscribes on the wire and closes
// every attached watcher's channel so a blocked "pubsub watch -daemon"
// stops instead of hanging. Unsubscribing a topic that was never
// subscribed is not an error -- Unsubscribed comes back false.
func (srv *Server) Unsubscribe(p PubsubUnsubscribeParams) (PubsubUnsubscribeResult, error) {
	realm, err := parseRealmHex(p.RealmHex)
	if err != nil {
		return PubsubUnsubscribeResult{}, err
	}
	key := topicKey{hex.EncodeToString(realm), p.Topic}
	srv.subsMu.Lock()
	sub, existed := srv.subs[key]
	delete(srv.subs, key)
	delete(srv.subDegraded, key)
	srv.subsMu.Unlock()

	if existed {
		if err := sub.end(); err != nil {
			srv.log("daemon: unsubscribe %s (realm %s) failed, topic was still untracked locally: %v", p.Topic, hex.EncodeToString(realm), err)
		}
	}
	return PubsubUnsubscribeResult{Unsubscribed: existed}, nil
}

// watch attaches out to (realm, topic)'s subscription, creating it
// first if needed, and returns a function to detach it again.
func (srv *Server) watch(realm []byte, topic string, out chan PubsubEventNotification) (func(), error) {
	sub, err := srv.ensureSubscription(realm, topic)
	if err != nil {
		return nil, err
	}
	if !sub.addWatcher(out) {
		return nil, fmt.Errorf("subscription to %q ended", topic)
	}
	return func() { sub.removeWatcher(out) }, nil
}

func (srv *Server) subscriptionTopics() []string {
	srv.subsMu.Lock()
	defer srv.subsMu.Unlock()
	topics := make([]string, 0, len(srv.subs))
	for k := range srv.subs {
		topics = append(topics, k.topic)
	}
	return topics
}

// subscriptionDegradedTopics is ServingDegraded's counterpart for
// subscriptions -- see StatusResult.SubscribedDegraded's own doc.
func (srv *Server) subscriptionDegradedTopics() []string {
	srv.subsMu.Lock()
	defer srv.subsMu.Unlock()
	topics := make([]string, 0)
	for k := range srv.subs {
		if srv.subDegraded[k] {
			topics = append(topics, k.topic)
		}
	}
	return topics
}

// handleWatch answers one MethodPubsubWatch request and then owns the
// rest of the connection's life: one ack Response, then a
// MethodPubsubEvent Notification per delivered event until the client
// disconnects or the subscription ends. Unlike every other method,
// this does NOT return to handleConn's read-request loop -- there is
// nothing more this connection is expected to send.
//
// br is handleConn's own bufio.Reader (the SAME one its json.Decoder
// reads from), not the raw net.Conn -- see handleConn's own comment
// on why: any byte the decoder buffered internally but didn't consume
// (e.g. the client's trailing '\n' after its request) must be drained
// from that SAME buffer, not raced against via a second, independent
// read on the underlying connection.
func (srv *Server) handleWatch(br *bufio.Reader, enc *json.Encoder, req Request) {
	var p PubsubWatchParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		_ = enc.Encode(Response{ID: req.ID, Error: &RPCError{Message: fmt.Sprintf("decode params: %v", err)}})
		return
	}
	realm, err := parseRealmHex(p.RealmHex)
	if err != nil {
		_ = enc.Encode(Response{ID: req.ID, Error: &RPCError{Message: err.Error()}})
		return
	}

	events := make(chan PubsubEventNotification, 32)
	unwatch, err := srv.watch(realm, p.Topic, events)
	if err != nil {
		_ = enc.Encode(Response{ID: req.ID, Error: &RPCError{Message: err.Error()}})
		return
	}
	defer unwatch()

	ackResult, err := json.Marshal(PubsubWatchAck{Watching: true, Topic: p.Topic})
	if err != nil {
		_ = enc.Encode(Response{ID: req.ID, Error: &RPCError{Message: err.Error()}})
		return
	}
	if err := enc.Encode(Response{ID: req.ID, Result: ackResult}); err != nil {
		return
	}

	// Nothing more is expected FROM this connection -- watchForDisconnect's
	// only job is noticing the client went away so the pump loop below
	// doesn't write into a dead connection forever.
	disconnected := watchForDisconnect(br)

	for {
		select {
		case evt, ok := <-events:
			if !ok {
				return // the subscription itself ended
			}
			raw, err := json.Marshal(evt)
			if err != nil {
				return
			}
			if err := enc.Encode(Notification{Method: MethodPubsubEvent, Params: raw}); err != nil {
				return
			}
		case <-disconnected:
			return
		}
	}
}

// watchForDisconnect returns a channel that closes once br produces a
// byte that ISN'T the one specific benign leftover this protocol
// always risks: json.Encoder.Encode (the client's own request writer)
// appends exactly one trailing '\n' after every request body, and
// json.Decoder.Decode stops reading the instant it has a complete
// value -- it does not consume that newline itself, so it can still
// be sitting unread in br when this is called. Read it as EOF-of-
// nothing-else-expected, not as the client sending something new.
//
// Found live (2026-08-31): a straight `_, _ = br.Read(buf); close(ch)`
// treated that leftover newline as "client disconnected" -- correct
// in effect (it closed the channel) but for the wrong reason and with
// no way to tell it apart from a REAL early disconnect. Harmless for
// a short request, where one Read() syscall already swallowed the
// newline along with the JSON body before Decode() ever returned,
// leaving nothing left to find here; reliably wrong once the request
// crossed whatever chunk-size boundary left the newline genuinely
// still unread on the wire (reproduced with a 74-byte pubsub topic:
// 73 bytes worked, 74 consistently didn't) -- closing `disconnected`
// within microseconds of the watch starting, before any real event
// could ever be dispatched to it.
func watchForDisconnect(br *bufio.Reader) <-chan struct{} {
	disconnected := make(chan struct{})
	go func() {
		defer close(disconnected)
		buf := make([]byte, 1)
		for {
			n, err := br.Read(buf)
			if err != nil {
				return // genuine read error/EOF -- really gone
			}
			if n > 0 && buf[0] == '\n' {
				continue // the expected leftover -- keep waiting for something that actually is one
			}
			return // a real, unexpected byte -- treat as gone
		}
	}()
	return disconnected
}
