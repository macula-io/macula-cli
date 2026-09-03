package daemon

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
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

// Server holds THREE Sessions to the same station and a
// dynamically-changing registry of procedures served against one of
// them -- not one Session doing everything. macula-go's
// FrameStream.Call/RecvFrame explicitly documents that a shared
// control stream has "one thing at a time" semantics: any frame
// arriving while something else is waiting on that same stream gets
// discarded or misattributed, not queued. A single-Session daemon
// answering inbound CALLs (ServeForever) while ALSO making outbound
// calls and running subscriptions on that same stream hits this for
// real -- confirmed live: an outbound call.invoke intermittently timed
// out because ServeForever's own receive loop had already consumed
// and discarded the RESULT frame meant for it. Splitting by concern
// (matching the SDK's own "use a second Session" guidance, and its
// live tests' own convention of a fresh identity per Session) removes
// the race entirely instead of trying to get lucky with timing:
//
//   - serveSession/id: the daemon's real, persisted identity. Owns
//     ServeForever and every Register/Unregister Advertise/Unadvertise
//     -- this is the identity "daemon status" reports, and the one a
//     caller resolves to reach anything this daemon advertises.
//   - callSession/callID: a fresh ephemeral identity, minted once at
//     startup, used ONLY for call.invoke. callMu serializes access --
//     two concurrent Invoke calls sharing this session's control
//     stream would race each other exactly the same way, just between
//     themselves instead of against ServeForever.
//   - subSession/subID: a second fresh ephemeral identity, used ONLY
//     for subscriptions. Every topic this daemon subscribes to shares
//     this ONE session and ONE receive loop (runSubscriptionLoop),
//     dispatching by (realm, topic) -- not one Session/loop per topic,
//     which would just relocate the same race between subscriptions.
//
// Registration and unregistration are ordinary mutex-guarded map
// operations; connection.ServeForever's lookup/policy parameters are
// already plain functions, so mutating srv.handlers while
// ServeForever's goroutine runs is the entire mechanism -- no restart,
// no second registration API on the SDK side.
type Server struct {
	serveSession *connection.Session
	id           identity.KeyPair
	connectedTo  string
	startedAt    time.Time

	callSession *connection.Session
	callID      identity.KeyPair
	callMu      sync.Mutex

	subSession *connection.Session
	subID      identity.KeyPair

	mu       sync.Mutex
	handlers map[procKey]connection.CallHandler
	policies map[procKey]ucan.Policy
	order    []procKey // insertion order, for stable "serving" output
	cancel   context.CancelFunc

	// subsMu/subs are separate from mu: subscriptions and served
	// procedures are independent concerns, and giving them their own
	// lock avoids any chance of the two interfering with each other's
	// hold time.
	subsMu sync.Mutex
	subs   map[topicKey]*subscription
}

// NewServer connects all three of a daemon's Sessions (see Server's
// own doc) to host:port using id for serving/advertising, minting the
// two ephemeral calling/subscribing identities itself. On any failure
// partway through, everything already connected is closed before
// returning the error -- no leaked Sessions on a failed startup.
func NewServer(ctx context.Context, host string, port uint16, id identity.KeyPair) (*Server, error) {
	connectedTo := fmt.Sprintf("%s:%d", host, port)

	serveSession, err := connection.Connect(ctx, host, port, transport.WebPKI{}, id)
	if err != nil {
		return nil, fmt.Errorf("daemon: connect (serve session): %w", err)
	}

	callID, err := identity.Generate()
	if err != nil {
		_ = serveSession.Close("normal", nil, id)
		return nil, fmt.Errorf("daemon: generate calling identity: %w", err)
	}
	callSession, err := connection.Connect(ctx, host, port, transport.WebPKI{}, callID)
	if err != nil {
		_ = serveSession.Close("normal", nil, id)
		return nil, fmt.Errorf("daemon: connect (call session): %w", err)
	}

	subID, err := identity.Generate()
	if err != nil {
		_ = serveSession.Close("normal", nil, id)
		_ = callSession.Close("normal", nil, callID)
		return nil, fmt.Errorf("daemon: generate subscribing identity: %w", err)
	}
	subSession, err := connection.Connect(ctx, host, port, transport.WebPKI{}, subID)
	if err != nil {
		_ = serveSession.Close("normal", nil, id)
		_ = callSession.Close("normal", nil, callID)
		return nil, fmt.Errorf("daemon: connect (subscribe session): %w", err)
	}

	return &Server{
		serveSession: serveSession,
		id:           id,
		connectedTo:  connectedTo,
		startedAt:    time.Now(),
		callSession:  callSession,
		callID:       callID,
		subSession:   subSession,
		subID:        subID,
		handlers:     map[procKey]connection.CallHandler{},
		policies:     map[procKey]ucan.Policy{},
		subs:         map[topicKey]*subscription{},
	}, nil
}

// Close closes every Session this daemon holds. Call after Run
// returns.
func (srv *Server) Close() {
	_ = srv.serveSession.Close("normal", nil, srv.id)
	_ = srv.callSession.Close("normal", nil, srv.callID)
	_ = srv.subSession.Close("normal", nil, srv.subID)
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

	if err := srv.serveSession.Advertise(frame.NewAdvertiseSpec(realm, p.Procedure, srv.id.NodeID()), srv.id); err != nil {
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
	srv.mu.Unlock()

	return ServeRegisterResult{Registered: true, Procedure: p.Procedure}, nil
}

// publishDirectAdvertisement is the daemon's own version of
// directdial.AdvertiseDirect / AdvertiseDirectWithCertChain, split across
// two sessions on purpose. Those helpers do the plain Advertise AND the
// DHT put_record on the ONE session they are handed; on a daemon that
// session is serveSession, whose receive loop belongs to ServeForever,
// so the put_record's RESULT frame was consumed there and every
// `serve -daemon -direct` registration died with "dht: put_record:
// connection: read stream: deadline exceeded" (seen live 2026-09-03 on
// every attempt, while the one-shot `serve -direct`, with no
// ServeForever running, worked) -- exactly the shared-control-stream
// race the Server doc above explains callSession exists to avoid.
//
// So: the plain Advertise has already happened on serveSession (the
// caller does it first, direct or not -- see AdvertiseDirect's own doc
// on why both are required), the record names serveSession's station
// as the server and is signed by srv.id (the identity a caller
// resolves), and the put_record CALL rides callSession under callMu,
// signed by callID like every other outbound call this daemon makes.
// The station verifies the RECORD's signature against the advertiser
// it names, not against whoever carried it.
func (srv *Server) publishDirectAdvertisement(realm []byte, procedure string, ttl time.Duration, certChainPEM string) error {
	uri := dht.DiscoveryURI(realm, procedure)
	var rec dht.Record
	var err error
	if certChainPEM != "" {
		rec, err = dht.NewProcedureAdvertisementWithCertChain(srv.id.NodeID(), uri, srv.serveSession.Station.NodeID, ttl, []byte(certChainPEM))
	} else {
		rec, err = dht.NewProcedureAdvertisement(srv.id.NodeID(), uri, srv.serveSession.Station.NodeID, ttl)
	}
	if err != nil {
		return fmt.Errorf("advertise (direct): %w", err)
	}
	rec = dht.Sign(rec, srv.id)

	srv.callMu.Lock()
	defer srv.callMu.Unlock()
	if err := dht.PutRecord(srv.callSession, srv.callID, rec); err != nil {
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
		_ = srv.serveSession.Unadvertise(frame.NewUnadvertiseSpec(realm, p.Procedure, srv.id.NodeID()), srv.id)
	}
	return ServeUnregisterResult{Unregistered: existed}, nil
}

func (srv *Server) Status() StatusResult {
	srv.mu.Lock()
	procs := make([]string, len(srv.order))
	for i, k := range srv.order {
		procs[i] = k.procedure
	}
	srv.mu.Unlock()
	return StatusResult{
		Identity:      hex.EncodeToString(srv.id.NodeID()),
		ConnectedTo:   srv.connectedTo,
		UptimeSeconds: int64(time.Since(srv.startedAt).Seconds()),
		Serving:       procs,
		Subscribed:    srv.subscriptionTopics(),
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

// Invoke routes one unary RPC call through srv.callSession instead of
// a caller dialing the mesh itself -- the daemon-mode counterpart to
// the one-shot "call" subcommand's plain (non-direct) path. callMu
// serializes this against any other concurrent Invoke: callSession is
// dedicated to calling (see Server's own doc), but its control stream
// still only tolerates one waiter at a time.
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

	srv.callMu.Lock()
	defer srv.callMu.Unlock()

	start := time.Now()
	var resp frame.CallResponse
	if p.UcanTokenHex != "" {
		token, decErr := hex.DecodeString(p.UcanTokenHex)
		if decErr != nil {
			return CallInvokeResult{}, fmt.Errorf("ucan_token_hex: invalid hex: %w", decErr)
		}
		resp, err = srv.callSession.CallWithUCAN(p.Procedure, realm, payload, deadlineMs, srv.callID, timeout, token)
	} else {
		resp, err = srv.callSession.Call(p.Procedure, realm, payload, deadlineMs, srv.callID, timeout)
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
	go func() { serveErrCh <- srv.serveSession.ServeForever(ctx, srv.lookup, srv.policy, srv.id) }()

	go srv.runSubscriptionLoop(ctx)

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
	case err := <-serveErrCh:
		_ = ln.Close()
		return fmt.Errorf("daemon: mesh serve loop: %w", err)
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
