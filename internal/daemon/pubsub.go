package daemon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/macula-io/macula-go-sdk/frame"

	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// topicKey identifies one daemon-owned subscription. Realm is
// hex-encoded for the same reason procKey does it: a comparable map
// key without a second index.
type topicKey struct {
	realmHex string
	topic    string
}

// subscription is one daemon-owned mesh subscription, independent of
// how many (if any) control-socket connections are currently watching
// it -- "subscribe" creates durable state, "watch" just taps into it,
// matching PubsubSubscribeParams's own doc on why these are separate
// verbs. Unlike an earlier draft of this file, a subscription owns no
// goroutine or Session of its own -- see runSubscriptionLoop's doc on
// why every topic shares Server.subSession and ONE receive loop.
type subscription struct {
	mu       sync.Mutex
	watchers map[chan PubsubEventNotification]struct{}
}

// subscriptionPollInterval bounds how long a single RecvEvent wait on
// srv.subSession blocks between checking ctx -- mirrors
// macula-go-sdk's own subscriberPollInterval reasoning exactly.
const subscriptionPollInterval = 2 * time.Second

// runSubscriptionLoop is the ONLY goroutine that ever reads
// srv.subSession's control stream, for as long as this daemon runs.
// Every topic this daemon subscribes to shares this one session and
// this one loop, dispatched to the right subscription by (realm,
// topic) -- running one reader per topic on a SHARED session would
// just relocate Server's own documented "one thing at a time" race
// between subscriptions instead of eliminating it, since RecvEvent
// doesn't filter by topic; whichever goroutine happens to call it
// first wins the next frame regardless of which topic it's actually
// for.
func (srv *Server) runSubscriptionLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			srv.closeAllSubscriptions()
			return
		default:
		}
		evt, err := srv.subSession.RecvEvent(subscriptionPollInterval)
		if err != nil {
			if isRecvTimeout(err) || errors.Is(err, frame.ErrNotAnEventFrame) {
				continue
			}
			// A real connection failure -- nothing more will ever
			// arrive on this session. Close every watcher's channel so
			// a "pubsub watch -daemon" tap stops instead of hanging.
			srv.closeAllSubscriptions()
			return
		}
		srv.dispatchEvent(evt)
	}
}

func isRecvTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (srv *Server) dispatchEvent(evt frame.EventInfo) {
	key := topicKey{hex.EncodeToString(evt.Realm), evt.Topic}
	srv.subsMu.Lock()
	sub, ok := srv.subs[key]
	srv.subsMu.Unlock()
	if !ok {
		return // nothing here is subscribed to this -- shouldn't happen, harmless if it does
	}
	out := PubsubEventNotification{
		Topic:        evt.Topic,
		Publisher:    hex.EncodeToString(evt.Publisher),
		Seq:          evt.Seq,
		Payload:      wirevalue.ToJSON(evt.Payload),
		DeliveredVia: evt.DeliveredVia,
		ReceivedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	}
	sub.mu.Lock()
	for ch := range sub.watchers {
		select {
		case ch <- out:
		default:
			// A slow watcher drops an event rather than blocking every
			// other watcher, or runSubscriptionLoop itself, on one
			// laggard.
		}
	}
	sub.mu.Unlock()
}

func (srv *Server) closeAllSubscriptions() {
	srv.subsMu.Lock()
	for _, sub := range srv.subs {
		sub.mu.Lock()
		for ch := range sub.watchers {
			close(ch)
		}
		sub.watchers = nil
		sub.mu.Unlock()
	}
	srv.subs = map[topicKey]*subscription{}
	srv.subsMu.Unlock()
}

// ensureSubscription creates (realm, topic)'s subscription -- issuing
// the actual wire SUBSCRIBE on srv.subSession -- the first time it's
// asked for, and just returns the existing one on every call after.
func (srv *Server) ensureSubscription(realm []byte, topic string) (*subscription, error) {
	key := topicKey{hex.EncodeToString(realm), topic}
	srv.subsMu.Lock()
	defer srv.subsMu.Unlock()
	if sub, ok := srv.subs[key]; ok {
		return sub, nil
	}
	spec := frame.NewSubscribeSpec(topic, realm, srv.subID.NodeID())
	if err := srv.subSession.Subscribe(spec, srv.subID); err != nil {
		return nil, fmt.Errorf("subscribe: %w", err)
	}
	sub := &subscription{watchers: map[chan PubsubEventNotification]struct{}{}}
	srv.subs[key] = sub
	return sub, nil
}

// Subscribe creates (or confirms) a durable subscription to
// (p.RealmHex, p.Topic).
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
	srv.subsMu.Unlock()

	if existed {
		_ = srv.subSession.Unsubscribe(frame.NewUnsubscribeSpec(p.Topic, realm, srv.subID.NodeID()), srv.subID)
		sub.mu.Lock()
		for ch := range sub.watchers {
			close(ch)
		}
		sub.watchers = nil
		sub.mu.Unlock()
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
	sub.mu.Lock()
	sub.watchers[out] = struct{}{}
	sub.mu.Unlock()
	return func() {
		sub.mu.Lock()
		delete(sub.watchers, out)
		sub.mu.Unlock()
	}, nil
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

// handleWatch answers one MethodPubsubWatch request and then owns the
// rest of conn's life: one ack Response, then a MethodPubsubEvent
// Notification per delivered event until conn disconnects or the
// subscription ends. Unlike every other method, this does NOT return
// to handleConn's read-request loop -- there is nothing more this
// connection is expected to send.
func (srv *Server) handleWatch(conn net.Conn, enc *json.Encoder, req Request) {
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

	// Nothing more is expected FROM this connection -- this goroutine's
	// only job is noticing the client went away (read returns
	// EOF/error) so the pump loop below doesn't write into a dead
	// connection forever.
	disconnected := make(chan struct{})
	go func() {
		defer close(disconnected)
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	}()

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
