package daemon

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/macula-io/macula-go/bolt4"
	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/connection"
	"github.com/macula-io/macula-go/dht"
	"github.com/macula-io/macula-go/frame"
	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/transport"
	"github.com/macula-io/macula-go/ucan"

	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// procKey identifies one registered (realm, procedure) pair. Realm is
// hex-encoded to make it a comparable map key without a second index.
type procKey struct {
	realmHex  string
	procedure string
}

// Server holds one Session to a station, under the daemon's own persisted
// identity, and a dynamically-changing registry of procedures served on
// it. The session carries everything at once: ServeForever answers
// inbound CALLs, Invoke makes outbound calls, and every subscription
// receives its events, concurrently, since macula-go's session reader
// routes each frame to whoever is waiting for it. Every outbound call and
// subscription is made as the daemon's own identity, the one "daemon
// status" reports and a caller resolves, so a token minted for that
// identity is accepted and a provider that checks its caller sees it.
//
// Registration and unregistration are ordinary mutex-guarded map
// operations; connection.ServeForever's lookup/policy parameters are
// already plain functions, so mutating srv.handlers while
// ServeForever's goroutine runs is the entire mechanism -- no restart,
// no second registration API on the SDK side.
//
// session is an atomic.Pointer: it is read from many goroutines
// (Register, Unregister, Invoke, the subscriptions) and REPLACED in place
// when the connection ends and runSession redials, so every reader takes a
// fresh Load() rather than a value captured once.
type Server struct {
	session     atomic.Pointer[connection.Session]
	id          identity.KeyPair
	connectedTo string // guarded by mu -- see setConnectedTo/Status
	startedAt   time.Time

	// connected/lastError are Status's own connectivity signal (2026-09-08,
	// a real live gap: connectedTo above only ever reports the last
	// address this daemon WAS reachable through, updated only on a
	// SUCCESSFUL (re)connect -- during an ongoing outage it silently kept
	// reporting that stale address forever, indistinguishable from
	// healthy to a supervisor or an operator running "daemon status").
	// Guarded by mu, same as connectedTo.
	connected bool
	lastError string

	// logger is nil (silent) unless SetLogger was called -- same
	// optional-setter shape as lazymesh's ringwaiter/roomwaiter
	// SetLogger, guarded by its own mutex since it's read from several
	// goroutines. Destination is the caller's choice (daemon.go wires
	// os.Stderr, not a file -- see its own comment on why that fits this
	// codebase's foreground-plus-systemd convention better than inventing
	// a log file/rotation scheme).
	loggerMu sync.Mutex
	logger   *log.Logger

	// seeds is this daemon's current dial order, used by reconnect() and
	// mutated in place (a seed that just failed rotates toward the back)
	// -- see reconnect's own doc.
	seeds   []connection.Seed
	seedsMu sync.Mutex

	mu       sync.Mutex
	handlers map[procKey]connection.CallHandler
	policies map[procKey]ucan.Policy
	order    []procKey // insertion order, for stable "serving" output
	cancel   context.CancelFunc
	// degraded marks a procKey still in handlers/order whose most recent
	// replayAdvertisements attempt failed -- registered and intended,
	// but not actually advertised on the mesh right now. Cleared the
	// next time that procedure's replay succeeds. Guarded by mu, same
	// as handlers/order (Register/Unregister/replayAdvertisements all
	// already hold mu when touching those).
	degraded map[procKey]bool

	// subsMu/subs are separate from mu: subscriptions and served
	// procedures are independent concerns, and giving them their own
	// lock avoids any chance of the two interfering with each other's
	// hold time.
	subsMu sync.Mutex
	subs   map[topicKey]*subscription
	// subDegraded is degraded's counterpart for subscriptions -- see its
	// own doc above. Guarded by subsMu, same as subs.
	subDegraded map[topicKey]bool

	// subscribeOn issues a SUBSCRIBE in place of Session.Subscribe when set.
	// Only tests set it, to stand in for a station.
	subscribeOn func(*connection.Session, frame.SubscribeSpec, identity.KeyPair) (*connection.Subscription, error)
}

// NewServer connects the daemon's session, under id, to the first
// reachable of seeds.
func NewServer(ctx context.Context, seeds []connection.Seed, id identity.KeyPair) (*Server, error) {
	session, err := connection.ConnectSeeds(ctx, seeds, transport.WebPKI{}, id)
	if err != nil {
		return nil, fmt.Errorf("daemon: connect: %w", err)
	}
	srv := &Server{
		id:          id,
		connectedTo: session.RemoteAddr(),
		connected:   true,
		startedAt:   time.Now(),
		seeds:       append([]connection.Seed(nil), seeds...), // own copy -- reconnect mutates this, must not alias the caller's slice
		handlers:    map[procKey]connection.CallHandler{},
		policies:    map[procKey]ucan.Policy{},
		degraded:    map[procKey]bool{},
		subs:        map[topicKey]*subscription{},
		subDegraded: map[topicKey]bool{},
	}
	srv.session.Store(session)
	return srv, nil
}

// Close closes the daemon's session. Call after Run returns.
func (srv *Server) Close() {
	_ = srv.session.Load().Close("normal", nil, srv.id)
}

// respawnDelay mirrors the reference Erlang SDK's own
// ?LINK_RESPAWN_DELAY_MS (macula_client.erl) -- a short pause before
// redialing, so a station mid-restart isn't hammered immediately.
const respawnDelay = 1 * time.Second

// reconnect redials this daemon's seed pool, blocking until one
// candidate answers or ctx is done. Rotates the current front seed to
// the back first: the daemon just lost a connection dialed from
// wherever srv.seeds currently starts, so deprioritizing that once
// (rather than trying it first again immediately) is a simple,
// good-enough policy -- srv.seeds is kept across reconnects, so a seed
// that keeps failing sinks toward the back over time.
// Returns (nil, false) only when ctx is done before any seed answered
// -- i.e. the daemon is shutting down, not "give up after N tries".
func (srv *Server) reconnect(ctx context.Context, trust transport.Trust, id identity.KeyPair) (*connection.Session, bool) {
	for {
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(respawnDelay):
		}

		srv.seedsMu.Lock()
		if len(srv.seeds) > 1 {
			srv.seeds = append(srv.seeds[1:], srv.seeds[0])
		}
		seeds := append([]connection.Seed(nil), srv.seeds...)
		srv.seedsMu.Unlock()

		sess, err := connection.ConnectSeeds(ctx, seeds, trust, id)
		if err == nil {
			return sess, true
		}
		if ctx.Err() != nil {
			return nil, false
		}
		// All seeds failed this pass -- loop back for another, after
		// another respawnDelay. Each seed's own dial already carries up
		// to connection.HandshakeTimeout before failing, which already
		// spaces out repeated full passes against a totally unreachable
		// pool; no separate backoff on top of that is needed.
	}
}

// setConnectedTo records addr as the station this daemon is currently
// reachable through -- called once at startup and again after every
// successful reconnect, so Status().ConnectedTo always reflects
// reality instead of going stale after the first station bounce.
func (srv *Server) setConnectedTo(addr string) {
	srv.mu.Lock()
	srv.connectedTo = addr
	srv.mu.Unlock()
}

// SetLogger sets where this daemon logs connection loss/recovery and
// otherwise-silent replay failures -- nil (the default) means silence,
// same optional-setter shape as lazymesh's ringwaiter/roomwaiter
// SetLogger. Safe to call at any point after NewServer, before or after
// Run starts -- every log call below takes loggerMu fresh rather than
// caching a value from before SetLogger might be called.
func (srv *Server) SetLogger(l *log.Logger) {
	srv.loggerMu.Lock()
	srv.logger = l
	srv.loggerMu.Unlock()
}

func (srv *Server) log(format string, args ...any) {
	srv.loggerMu.Lock()
	l := srv.logger
	srv.loggerMu.Unlock()
	if l != nil {
		l.Printf(format, args...)
	}
}

// setSessionLost marks the daemon's session down (Status().Connected is
// false), records reason as Status().LastError and logs it.
func (srv *Server) setSessionLost(reason string) {
	srv.mu.Lock()
	srv.connected = false
	srv.lastError = reason
	srv.mu.Unlock()
	srv.log("daemon: session lost, reconnecting: %s", reason)
}

// setSessionRecovered clears what setSessionLost set, once the session is
// back through addr.
func (srv *Server) setSessionRecovered(addr string) {
	srv.mu.Lock()
	srv.connected = true
	srv.lastError = ""
	srv.mu.Unlock()
	srv.log("daemon: session reconnected via %s", addr)
}

// replayAdvertisements re-sends ADVERTISE for every procedure this
// daemon currently has registered onto a freshly (re)connected
// session -- the direct analogue of the reference SDK's
// macula_client_replay:advs_to/2. Best-effort: an error here means the
// procedure silently isn't reachable through the new session yet, but
// it stays in srv.handlers/srv.order regardless and is retried on the
// next reconnect, same as everything else this daemon tracks.
func (srv *Server) replayAdvertisements(sess *connection.Session) {
	srv.mu.Lock()
	keys := append([]procKey(nil), srv.order...)
	srv.mu.Unlock()
	for _, k := range keys {
		realm, err := hex.DecodeString(k.realmHex)
		if err != nil {
			continue
		}
		advErr := sess.Advertise(frame.NewAdvertiseSpec(realm, k.procedure, srv.id.NodeID()), srv.id)
		srv.mu.Lock()
		srv.degraded[k] = advErr != nil
		srv.mu.Unlock()
		if advErr != nil {
			srv.log("daemon: re-advertise %s (realm %s) after reconnect failed, still registered but not reachable until the next successful replay: %v", k.procedure, k.realmHex, advErr)
		}
	}
}

func parseRealmHex(s string) ([]byte, error) {
	if s == "" {
		return make([]byte, 32), nil
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("realm_hex: invalid hex: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("realm_hex: must be 32 bytes (64 hex chars), got %d", len(b))
	}
	return b, nil
}

func (srv *Server) lookup(realm []byte, procedure string) (connection.CallHandler, bool) {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	h, ok := srv.handlers[procKey{hex.EncodeToString(realm), procedure}]
	return h, ok
}

func (srv *Server) policy(realm []byte, procedure string) ucan.Policy {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if p, ok := srv.policies[procKey{hex.EncodeToString(realm), procedure}]; ok {
		return p
	}
	return ucan.Open
}

// Register advertises p.Procedure and installs a persistent handler
// for it, reachable on the very next CALL a concurrently running
// ServeForever answers.
func (srv *Server) Register(p ServeRegisterParams) (ServeRegisterResult, error) {
	realm, err := parseRealmHex(p.RealmHex)
	if err != nil {
		return ServeRegisterResult{}, err
	}
	ttl := time.Duration(p.TTLSeconds) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}

	handler, err := BuildReplyHandler(p.Reply, p.Echo, p.Exec, time.Duration(p.ExecTimeoutMs)*time.Millisecond)
	if err != nil {
		return ServeRegisterResult{}, err
	}

	policy := ucan.Open
	if p.RequireUcanIssuer != "" {
		issuer, decErr := hex.DecodeString(p.RequireUcanIssuer)
		if decErr != nil || len(issuer) != 32 {
			return ServeRegisterResult{}, fmt.Errorf("require_ucan_issuer_hex must be a 32-byte Ed25519 public key as 64 hex chars")
		}
		policy = ucan.Required(issuer)
	}

	if err := srv.session.Load().Advertise(frame.NewAdvertiseSpec(realm, p.Procedure, srv.id.NodeID()), srv.id); err != nil {
		return ServeRegisterResult{}, fmt.Errorf("advertise: %w", err)
	}
	if p.Direct {
		if err := srv.publishDirectAdvertisement(realm, p.Procedure, ttl, p.CertChainPEM); err != nil {
			return ServeRegisterResult{}, err
		}
	}

	key := procKey{hex.EncodeToString(realm), p.Procedure}
	srv.mu.Lock()
	if _, exists := srv.handlers[key]; !exists {
		srv.order = append(srv.order, key)
	}
	srv.handlers[key] = handler
	srv.policies[key] = policy
	delete(srv.degraded, key) // the Advertise just above already succeeded, or Register would have returned its error instead of reaching here
	srv.mu.Unlock()

	return ServeRegisterResult{Registered: true, Procedure: p.Procedure}, nil
}

// publishDirectAdvertisement is the daemon's own version of
// directdial.AdvertiseDirect / AdvertiseDirectWithCertChain, for a
// procedure the caller has already advertised on the daemon's session
// (see AdvertiseDirect's own doc on why both are required): the record
// names that session's station as the server, is signed by srv.id (the
// identity a caller resolves), and is stored through the same session.
func (srv *Server) publishDirectAdvertisement(realm []byte, procedure string, ttl time.Duration, certChainPEM string) error {
	session := srv.session.Load()
	uri := dht.DiscoveryURI(realm, procedure)
	var rec dht.Record
	var err error
	if certChainPEM != "" {
		rec, err = dht.NewProcedureAdvertisementWithCertChain(srv.id.NodeID(), uri, session.Station.NodeID, ttl, []byte(certChainPEM))
	} else {
		rec, err = dht.NewProcedureAdvertisement(srv.id.NodeID(), uri, session.Station.NodeID, ttl)
	}
	if err != nil {
		return fmt.Errorf("advertise (direct): %w", err)
	}
	if err := dht.PutRecord(session, srv.id, dht.Sign(rec, srv.id)); err != nil {
		return fmt.Errorf("advertise (direct): %w", err)
	}
	return nil
}

// Unregister unadvertises p.Procedure and removes its handler.
// Unregistering a procedure that was never registered is not an
// error -- Unregistered comes back false, matching Unadvertise's own
// idempotent, side-effect-free-on-repeat nature.
func (srv *Server) Unregister(p ServeUnregisterParams) (ServeUnregisterResult, error) {
	realm, err := parseRealmHex(p.RealmHex)
	if err != nil {
		return ServeUnregisterResult{}, err
	}
	key := procKey{hex.EncodeToString(realm), p.Procedure}

	srv.mu.Lock()
	_, existed := srv.handlers[key]
	delete(srv.handlers, key)
	delete(srv.policies, key)
	delete(srv.degraded, key)
	if existed {
		for i, k := range srv.order {
			if k == key {
				srv.order = append(srv.order[:i], srv.order[i+1:]...)
				break
			}
		}
	}
	srv.mu.Unlock()

	if existed {
		if err := srv.session.Load().Unadvertise(frame.NewUnadvertiseSpec(realm, p.Procedure, srv.id.NodeID()), srv.id); err != nil {
			// Not restored to handlers/order/degraded above -- the
			// operator asked to stop serving this and that local intent
			// already took effect regardless of whether the mesh-facing
			// Unadvertise itself landed. Logged so a leftover, still-
			// live advertisement isn't a silent surprise if it's ever
			// noticed from the outside (e.g. a stale DHT record).
			srv.log("daemon: unadvertise %s (realm %s) failed, procedure was still unregistered locally: %v", p.Procedure, hex.EncodeToString(realm), err)
		}
	}
	return ServeUnregisterResult{Unregistered: existed}, nil
}

func (srv *Server) Status() StatusResult {
	srv.mu.Lock()
	procs := make([]string, len(srv.order))
	degradedProcs := make([]string, 0)
	for i, k := range srv.order {
		procs[i] = k.procedure
		if srv.degraded[k] {
			degradedProcs = append(degradedProcs, k.procedure)
		}
	}
	connectedTo := srv.connectedTo
	connected := srv.connected
	lastError := srv.lastError
	srv.mu.Unlock()
	return StatusResult{
		Identity:           hex.EncodeToString(srv.id.NodeID()),
		ConnectedTo:        connectedTo,
		Connected:          connected,
		LastError:          lastError,
		UptimeSeconds:      int64(time.Since(srv.startedAt).Seconds()),
		Serving:            procs,
		ServingDegraded:    degradedProcs,
		Subscribed:         srv.subscriptionTopics(),
		SubscribedDegraded: srv.subscriptionDegradedTopics(),
	}
}

// wireCallError carries BOLT#4 detail through dispatch() into the
// control-socket Response's RPCError -- the same fields the one-shot
// "call" command's own error output already surfaces via
// internal/report.Error, recovered here via errors.As rather than
// flattened to a plain message the way every other daemon method's
// errors are.
type wireCallError struct {
	message   string
	bolt4Code *uint8
	bolt4Name string
	retryable *bool
}

func (e *wireCallError) Error() string { return e.message }

// Invoke makes one unary RPC call on the daemon's session, as the daemon's
// own identity, instead of a caller dialing the mesh itself -- the
// daemon-mode counterpart to the one-shot "call" subcommand's plain
// (non-direct) path. Calls run concurrently.
func (srv *Server) Invoke(p CallInvokeParams) (CallInvokeResult, error) {
	realm, err := parseRealmHex(p.RealmHex)
	if err != nil {
		return CallInvokeResult{}, err
	}
	payload := cbor.Null()
	if len(p.Payload) > 0 {
		payload, err = wirevalue.FromJSON(p.Payload)
		if err != nil {
			return CallInvokeResult{}, fmt.Errorf("payload: %w", err)
		}
	}
	timeout := time.Duration(p.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadlineMs := time.Now().Add(timeout).UnixMilli()

	session := srv.session.Load()
	start := time.Now()
	var resp frame.CallResponse
	if p.UcanTokenHex != "" {
		token, decErr := hex.DecodeString(p.UcanTokenHex)
		if decErr != nil {
			return CallInvokeResult{}, fmt.Errorf("ucan_token_hex: invalid hex: %w", decErr)
		}
		resp, err = session.CallWithUCAN(p.Procedure, realm, payload, deadlineMs, srv.id, timeout, token)
	} else {
		resp, err = session.Call(p.Procedure, realm, payload, deadlineMs, srv.id, timeout)
	}
	duration := time.Since(start).Milliseconds()
	if err != nil {
		return CallInvokeResult{}, err
	}
	if resp.IsError {
		code := resp.Code
		name := resp.Name
		if bc, ok := bolt4.FromU8(code); ok {
			name = bc.Name()
		}
		retryable := bolt4.Code(code).IsRetryable()
		msg := fmt.Sprintf("call failed: %s (code=%d)", name, code)
		if resp.Detail != nil {
			msg += ": " + *resp.Detail
		}
		return CallInvokeResult{}, &wireCallError{message: msg, bolt4Code: &code, bolt4Name: name, retryable: &retryable}
	}
	return CallInvokeResult{
		RespondedBy: hex.EncodeToString(resp.RespondedBy),
		Payload:     wirevalue.ToJSON(resp.Payload),
		DurationMs:  duration,
	}, nil
}

// Shutdown asks a running Run to stop -- callable both from the
// "shutdown" control-socket method and from the start command's own
// signal handler, converging on the same context cancellation either
// way.
func (srv *Server) Shutdown() {
	srv.mu.Lock()
	cancel := srv.cancel
	srv.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// runSession serves inbound CALLs on the daemon's session for as long as
// ctx is not done. When the session ends -- its Done closes, with the
// reason in its Err -- it redials the seed pool and replays every
// registered procedure and every subscription onto the fresh session
// before serving again, mirroring the reference SDK's respawn_link +
// advs_to + subs_to. ServeForever stopping while the session is still up
// (a reply it could not send) just serves again on the same session. Only
// returns once ctx itself is done, ending every subscription on its way
// out -- ordinary station churn never ends this loop on its own.
func (srv *Server) runSession(ctx context.Context) error {
	defer srv.closeAllSubscriptions()
	for {
		sess := srv.session.Load()
		serveErr := sess.ServeForever(ctx, srv.lookup, srv.policy, srv.id)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		select {
		case <-sess.Done():
		default:
			srv.log("daemon: serving stopped on a live session, serving again: %v", serveErr)
			continue
		}
		srv.setSessionLost(sessionEndReason(sess))
		fresh, ok := srv.reconnect(ctx, transport.WebPKI{}, srv.id)
		if !ok {
			return ctx.Err()
		}
		srv.session.Store(fresh)
		srv.setConnectedTo(fresh.RemoteAddr())
		srv.setSessionRecovered(fresh.RemoteAddr())
		srv.replayAdvertisements(fresh)
		srv.replaySubscriptions(fresh)
	}
}

// sessionEndReason is why sess ended, as its Err reports it.
func sessionEndReason(sess *connection.Session) string {
	if err := sess.Err(); err != nil {
		return err.Error()
	}
	return "connection closed"
}

// Run answers inbound mesh CALLs against the dynamic registry AND
// serves the control socket at socketPath, until parentCtx is done or
// Shutdown is called. The caller still owns session's lifecycle --
// connect it before calling Run, close it after Run returns.
func (srv *Server) Run(parentCtx context.Context, socketPath string) error {
	ctx, cancel := context.WithCancel(parentCtx)
	srv.mu.Lock()
	srv.cancel = cancel
	srv.mu.Unlock()
	defer cancel()

	ln, err := Listen(socketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = ln.Close()
		_ = os.Remove(socketPath)
	}()

	serveErrCh := make(chan error, 1)
	go func() { serveErrCh <- srv.runSession(ctx) }()

	acceptErrCh := make(chan error, 1)
	go func() { acceptErrCh <- srv.acceptLoop(ctx, ln) }()

	select {
	case <-ctx.Done():
		_ = ln.Close()
		<-acceptErrCh
		select {
		case <-serveErrCh:
		case <-time.After(5 * time.Second):
		}
		return ctx.Err()
	case <-serveErrCh:
		// runSession only ever returns once ctx itself is done --
		// transient link failures are handled internally via
		// reconnect+replay, so this is the same shutdown as the
		// ctx.Done() case above, just observed via the other channel
		// first.
		_ = ln.Close()
		return ctx.Err()
	case err := <-acceptErrCh:
		return fmt.Errorf("daemon: control socket: %w", err)
	}
}

func (srv *Server) acceptLoop(ctx context.Context, ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil // deliberate close on shutdown, not a real failure
			default:
				return err
			}
		}
		go srv.handleConn(conn)
	}
}

func (srv *Server) handleConn(conn net.Conn) {
	defer conn.Close()
	// json.Decoder buffers ahead from conn internally (encoding/json's
	// own scanner, not a bufio.Reader, but same effect: a Decode() call
	// can read more bytes off the wire than the one JSON value it
	// returns -- e.g. the trailing '\n' every json.Encoder.Encode call
	// on the CLIENT side appends after its request). handleWatch used
	// to read directly from conn to detect the client going away, racing
	// that raw read against whatever the decoder already buffered but
	// hadn't consumed: for a short request, one Read() syscall grabbed
	// the JSON body AND its trailing newline together, so nothing was
	// left for handleWatch's read to find (the intended behaviour --
	// see its own doc, "nothing more will ever be read from this
	// connection"). Cross whatever chunk-size boundary the JSON body
	// happens to land on and the trailing newline is genuinely still
	// unread on the wire when Decode() returns, and handleWatch's raw
	// conn.Read(buf) picks it up as if the client had just sent
	// something -- which, per its own logic, means "gone", so it closed
	// the watch's own disconnected channel within microseconds of
	// starting, before any real event could ever arrive. Reproduced
	// live: a 74-byte pubsub topic name (73 worked, 74 didn't) reliably
	// tripped this depending on exactly where handleConn's own Decode()
	// call happened to stop reading. Fix: wrap conn in ONE shared
	// bufio.Reader, used for BOTH the decoder and handleWatch's own
	// read, so a leftover buffered byte is drained from the SAME buffer
	// the decoder left it in -- correctly, not raced against a second,
	// independent read on the same underlying connection.
	br := bufio.NewReader(conn)
	dec := json.NewDecoder(br)
	enc := json.NewEncoder(conn)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			return // client disconnected, or sent garbage -- nothing more to answer
		}
		if req.Method == MethodPubsubWatch {
			// handleWatch owns the rest of this connection's life --
			// one ack, then a Notification per event. Nothing more
			// will ever be read from this connection.
			srv.handleWatch(br, enc, req)
			return
		}
		if err := enc.Encode(srv.dispatch(req)); err != nil {
			return
		}
	}
}

func (srv *Server) dispatch(req Request) Response {
	result, err := srv.handle(req)
	if err != nil {
		rpcErr := &RPCError{Message: err.Error()}
		var wireErr *wireCallError
		if errors.As(err, &wireErr) {
			rpcErr.Bolt4Code = wireErr.bolt4Code
			rpcErr.Bolt4Name = wireErr.bolt4Name
			rpcErr.Retryable = wireErr.retryable
		}
		return Response{ID: req.ID, Error: rpcErr}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return Response{ID: req.ID, Error: &RPCError{Message: fmt.Sprintf("encode result: %v", err)}}
	}
	return Response{ID: req.ID, Result: raw}
}

func (srv *Server) handle(req Request) (any, error) {
	switch req.Method {
	case MethodServeRegister:
		var p ServeRegisterParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("decode params: %w", err)
		}
		return srv.Register(p)
	case MethodServeUnregister:
		var p ServeUnregisterParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("decode params: %w", err)
		}
		return srv.Unregister(p)
	case MethodCallInvoke:
		var p CallInvokeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("decode params: %w", err)
		}
		return srv.Invoke(p)
	case MethodPubsubSubscribe:
		var p PubsubSubscribeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("decode params: %w", err)
		}
		return srv.Subscribe(p)
	case MethodPubsubUnsubscribe:
		var p PubsubUnsubscribeParams
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, fmt.Errorf("decode params: %w", err)
		}
		return srv.Unsubscribe(p)
	case MethodStatus:
		return srv.Status(), nil
	case MethodShutdown:
		go srv.Shutdown()
		return ShutdownResult{ShuttingDown: true}, nil
	default:
		return nil, fmt.Errorf("unknown method %q", req.Method)
	}
}
