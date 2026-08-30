package daemon

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/macula-io/macula-go-sdk/cbor"
	"github.com/macula-io/macula-go-sdk/connection"
	"github.com/macula-io/macula-go-sdk/directdial"
	"github.com/macula-io/macula-go-sdk/frame"
	"github.com/macula-io/macula-go-sdk/identity"
	"github.com/macula-io/macula-go-sdk/ucan"

	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// procKey identifies one registered (realm, procedure) pair. Realm is
// hex-encoded to make it a comparable map key without a second index.
type procKey struct {
	realmHex  string
	procedure string
}

// Server holds one Session and a dynamically-changing registry of
// procedures served against it -- the daemon-mode counterpart to the
// one-shot "serve" subcommand's single advertise-then-answer-once
// shape. Registration and unregistration are ordinary mutex-guarded
// map operations; connection.ServeForever's lookup/policy parameters
// are already plain functions, so mutating srv.handlers while
// ServeForever's goroutine runs is the entire mechanism -- no restart,
// no second registration API on the SDK side.
type Server struct {
	session     *connection.Session
	id          identity.KeyPair
	connectedTo string
	startedAt   time.Time

	mu       sync.Mutex
	handlers map[procKey]connection.CallHandler
	policies map[procKey]ucan.Policy
	order    []procKey // insertion order, for stable "serving" output
	cancel   context.CancelFunc
}

// NewServer wraps an already-connected session. The caller retains
// ownership of session's lifecycle (connect before, close after Run
// returns) -- same ownership shape every one-shot subcommand already
// uses, just held for longer.
func NewServer(session *connection.Session, id identity.KeyPair, connectedTo string) *Server {
	return &Server{
		session:     session,
		id:          id,
		connectedTo: connectedTo,
		startedAt:   time.Now(),
		handlers:    map[procKey]connection.CallHandler{},
		policies:    map[procKey]ucan.Policy{},
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

	replyValue := cbor.Null()
	if len(p.Reply) > 0 {
		replyValue, err = wirevalue.FromJSON(p.Reply)
		if err != nil {
			return ServeRegisterResult{}, fmt.Errorf("reply: %w", err)
		}
	}
	echo := p.Echo
	handler := func(payload cbor.Value) (cbor.Value, error) {
		if echo {
			return payload, nil
		}
		return replyValue, nil
	}

	policy := ucan.Open
	if p.RequireUcanIssuer != "" {
		issuer, decErr := hex.DecodeString(p.RequireUcanIssuer)
		if decErr != nil || len(issuer) != 32 {
			return ServeRegisterResult{}, fmt.Errorf("require_ucan_issuer_hex must be a 32-byte Ed25519 public key as 64 hex chars")
		}
		policy = ucan.Required(issuer)
	}

	switch {
	case p.Direct && p.CertChainPEM != "":
		if err := directdial.AdvertiseDirectWithCertChain(srv.session, srv.id, realm, p.Procedure, ttl, []byte(p.CertChainPEM)); err != nil {
			return ServeRegisterResult{}, fmt.Errorf("advertise (direct, cert-chain): %w", err)
		}
	case p.Direct:
		if err := directdial.AdvertiseDirect(srv.session, srv.id, realm, p.Procedure, ttl); err != nil {
			return ServeRegisterResult{}, fmt.Errorf("advertise (direct): %w", err)
		}
	default:
		if err := srv.session.Advertise(frame.NewAdvertiseSpec(realm, p.Procedure, srv.id.NodeID()), srv.id); err != nil {
			return ServeRegisterResult{}, fmt.Errorf("advertise: %w", err)
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
		_ = srv.session.Unadvertise(frame.NewUnadvertiseSpec(realm, p.Procedure, srv.id.NodeID()), srv.id)
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
	}
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
	go func() { serveErrCh <- srv.session.ServeForever(ctx, srv.lookup, srv.policy, srv.id) }()

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
	dec := json.NewDecoder(conn)
	enc := json.NewEncoder(conn)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			return // client disconnected, or sent garbage -- nothing more to answer
		}
		if err := enc.Encode(srv.dispatch(req)); err != nil {
			return
		}
	}
}

func (srv *Server) dispatch(req Request) Response {
	result, err := srv.handle(req)
	if err != nil {
		return Response{ID: req.ID, Error: &RPCError{Message: err.Error()}}
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
	case MethodStatus:
		return srv.Status(), nil
	case MethodShutdown:
		go srv.Shutdown()
		return ShutdownResult{ShuttingDown: true}, nil
	default:
		return nil, fmt.Errorf("unknown method %q", req.Method)
	}
}
