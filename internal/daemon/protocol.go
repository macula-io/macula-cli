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
// populated; Bolt4Name is set only when the failure is a parsed
// BOLT#4 error from the mesh itself (an advertise or call failing on
// the wire), matching internal/report's own Bolt4Name/omitempty
// convention for the same distinction.
type RPCError struct {
	Message   string `json:"message"`
	Bolt4Name string `json:"bolt4_name,omitempty"`
}

const (
	MethodServeRegister   = "serve.register"
	MethodServeUnregister = "serve.unregister"
	MethodStatus          = "status"
	MethodShutdown        = "shutdown"
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
}

type ShutdownResult struct {
	ShuttingDown bool `json:"shutting_down"`
}
