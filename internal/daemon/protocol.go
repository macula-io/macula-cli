// Package daemon implements macula-cli's optional long-lived mode: one
// process holds a single Session open against a station, and other
// macula-cli invocations control it over a local Unix domain socket
// instead of each dialing the mesh fresh. The control protocol is
// newline-delimited JSON, not the mesh's own CBOR — this socket only
// ever talks to another macula-cli process on the same machine, so
// there's no reason to make it speak the wire protocol's codec, and
// NDJSON stays inspectable with plain tools (socat, nc).
package daemon

import "encoding/json"

// Request is one control-socket request, correlated to its Response by
// ID. A Request never has ID 0 -- Client.nextID starts at 1 -- so ID 0
// on an incoming message unambiguously marks a Notification instead.
type Request struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Response answers exactly one Request, matched by ID.
type Response struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *RPCError       `json:"error,omitempty"`
}

// Notification is an unsolicited server-to-client push (e.g.
// pubsub.event) -- no ID, since it isn't answering anything.
type Notification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// RPCError carries a failure back to the client. Message is always
// populated; Bolt4Code/Bolt4Name/Retryable are set only when the
// failure is a parsed BOLT#4 error from the mesh itself (call.invoke's
// own call failing on the wire, or a future advertise-level one) --
// the exact same fields internal/report.Error carries for a direct
// (non-daemon) command's own error output, so a caller can reconstruct
// one from the other and get identical --json output either way.
type RPCError struct {
	Message   string `json:"message"`
	Bolt4Code *uint8 `json:"bolt4_code,omitempty"`
	Bolt4Name string `json:"bolt4_name,omitempty"`
	Retryable *bool  `json:"retryable,omitempty"`
}

const (
	MethodServeRegister     = "serve.register"
	MethodServeUnregister   = "serve.unregister"
	MethodStatus            = "status"
	MethodShutdown          = "shutdown"
	MethodCallInvoke        = "call.invoke"
	MethodPubsubSubscribe   = "pubsub.subscribe"
	MethodPubsubUnsubscribe = "pubsub.unsubscribe"
	MethodPubsubWatch       = "pubsub.watch"
	// MethodPubsubEvent never appears as a Request -- it's the Method
	// on the Notification pushed to a connection that sent
	// MethodPubsubWatch, one per delivered event.
	MethodPubsubEvent = "pubsub.event"
)

// ServeRegisterParams registers a persistent handler with a running
// daemon -- the daemon-mode counterpart to one invocation of the
// one-shot "serve" subcommand's own flags.
type ServeRegisterParams struct {
	Procedure         string          `json:"procedure"`
	RealmHex          string          `json:"realm_hex,omitempty"`
	Reply             json.RawMessage `json:"reply,omitempty"`
	Echo              bool            `json:"echo,omitempty"`
	Direct            bool            `json:"direct,omitempty"`
	TTLSeconds        int64           `json:"ttl_seconds,omitempty"`
	CertChainPEM      string          `json:"cert_chain_pem,omitempty"`
	RequireUcanIssuer string          `json:"require_ucan_issuer_hex,omitempty"`
}

type ServeRegisterResult struct {
	Registered bool   `json:"registered"`
	Procedure  string `json:"procedure"`
}

type ServeUnregisterParams struct {
	Procedure string `json:"procedure"`
	RealmHex  string `json:"realm_hex,omitempty"`
}

type ServeUnregisterResult struct {
	Unregistered bool `json:"unregistered"`
}

type StatusResult struct {
	Identity      string   `json:"identity"`
	ConnectedTo   string   `json:"connected_to"`
	UptimeSeconds int64    `json:"uptime_seconds"`
	Serving       []string `json:"serving"`
	Subscribed    []string `json:"subscribed"`
}

type ShutdownResult struct {
	ShuttingDown bool `json:"shutting_down"`
}

// CallInvokeParams routes one unary RPC call through the daemon's
// already-open Session instead of dialing the mesh fresh -- the
// daemon-mode counterpart to one invocation of the one-shot "call"
// subcommand's own plain (non-direct) flags. Direct-dial calls aren't
// supported this way: a direct-dial call resolves and dials a
// DIFFERENT station per call, unrelated to whatever station the
// daemon happens to already be connected to.
type CallInvokeParams struct {
	Procedure    string          `json:"procedure"`
	RealmHex     string          `json:"realm_hex,omitempty"`
	Payload      json.RawMessage `json:"payload,omitempty"`
	TimeoutMs    int64           `json:"timeout_ms,omitempty"`
	UcanTokenHex string          `json:"ucan_token_hex,omitempty"`
}

type CallInvokeResult struct {
	RespondedBy string `json:"responded_by"`
	Payload     any    `json:"payload"`
	DurationMs  int64  `json:"duration_ms"`
}

// PubsubSubscribeParams creates (or confirms) a daemon-owned
// subscription to (RealmHex, Topic) -- persists past the invocation
// that requested it, unlike the one-shot "pubsub watch" command's own
// subscribe-for-the-duration-of-one-process shape. Meaningless without
// a daemon, so unlike "serve", "pubsub subscribe"/"unsubscribe" have
// no non-daemon form at all.
type PubsubSubscribeParams struct {
	Topic    string `json:"topic"`
	RealmHex string `json:"realm_hex,omitempty"`
}

type PubsubSubscribeResult struct {
	Subscribed bool   `json:"subscribed"`
	Topic      string `json:"topic"`
}

type PubsubUnsubscribeParams struct {
	Topic    string `json:"topic"`
	RealmHex string `json:"realm_hex,omitempty"`
}

type PubsubUnsubscribeResult struct {
	Unsubscribed bool `json:"unsubscribed"`
}

// PubsubWatchParams attaches the connection that sends it to a live
// tap on (RealmHex, Topic)'s subscription, creating it first if it
// doesn't already exist. The server answers with one PubsubWatchAck
// Response, then pushes a MethodPubsubEvent Notification per event on
// the SAME connection until the client disconnects or the
// subscription ends -- this connection does not accept any further
// Request after this one.
type PubsubWatchParams struct {
	Topic    string `json:"topic"`
	RealmHex string `json:"realm_hex,omitempty"`
}

type PubsubWatchAck struct {
	Watching bool   `json:"watching"`
	Topic    string `json:"topic"`
}

// PubsubEventNotification is MethodPubsubEvent's Params shape -- field
// names and JSON tags deliberately match cmd/macula-cli/pubsub.go's
// own pubsubEvent struct exactly, so "pubsub watch" prints identical
// output whether or not -daemon was used.
type PubsubEventNotification struct {
	Topic        string `json:"topic"`
	Publisher    string `json:"publisher"`
	Seq          uint64 `json:"seq"`
	Payload      any    `json:"payload"`
	DeliveredVia string `json:"delivered_via"`
	ReceivedAt   string `json:"received_at"`
}
