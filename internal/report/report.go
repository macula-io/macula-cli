// Package report gives every macula-cli command one way to emit a result: a
// JSON envelope for scripts and agents (--json), or plain text for people,
// from the same data. A failure carries a kind from a fixed set, taken from
// macula-go's typed errors, never from their text, and a provider's, relay's
// or stream's own code where the wire gave one.
package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/macula-io/macula-go/handshake"
	"github.com/macula-io/macula-go/pool"
	"github.com/macula-io/macula-go/stationlink"
)

// Envelope is the --json shape of every result.
type Envelope struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error *Error `json:"error,omitempty"`
}

// Error is a failure: its kind, the provider's, relay's or stream's code and
// detail when the wire carried one, and the message for people.
//
// Kinds: provider_error, relay_error, stream_error, identity_mismatch (a
// station that does not prove the pinned node_id), refused (a station refused
// the registration: code is its reason), unacknowledged (a station did not
// answer one), realm_refusal (code: the realm's error, detail: the HTTP
// status), timeout, no_link, no_provider, no_station_endpoint (code: why the
// station's endpoint record is unusable), no_realm_key, not_found, not_shared,
// content_unavailable, invalid_argument, failed.
type Error struct {
	Kind    string `json:"kind"`
	Code    string `json:"code,omitempty"`
	Detail  string `json:"detail,omitempty"`
	Relay   bool   `json:"relay,omitempty"`
	Message string `json:"message"`
}

// Out is where results go; tests replace it.
var Out io.Writer = os.Stdout

// Errs is where failures go in text mode; tests replace it.
var Errs io.Writer = os.Stderr

// Ok emits a success: data as JSON when jsonOut, else human().
func Ok(jsonOut bool, data any, human func(w io.Writer)) {
	if jsonOut {
		emit(Envelope{OK: true, Data: data})
		return
	}
	if human != nil {
		human(Out)
	}
}

// Fail emits err and returns the exit code, 1.
func Fail(jsonOut bool, err error) int {
	e := classify(err)
	if jsonOut {
		emit(Envelope{OK: false, Error: &e})
		return 1
	}
	fmt.Fprintf(Errs, "error: %s\n", e.Message)
	return 1
}

// Usage emits a malformed invocation (kind invalid_argument) and returns the
// exit code, 2.
func Usage(jsonOut bool, err error) int {
	e := Error{Kind: "invalid_argument", Message: err.Error()}
	if jsonOut {
		emit(Envelope{OK: false, Error: &e})
		return 2
	}
	fmt.Fprintf(Errs, "error: %s\n", e.Message)
	return 2
}

// refusal is an error that carries a service's own refusal code and status,
// such as a realm's HTTP answer.
type refusal interface {
	Refusal() (code string, status int)
}

func classify(err error) Error {
	e := Error{Kind: "failed", Message: err.Error()}
	switch {
	case classifyWire(err, &e):
	case classifyRealm(err, &e):
	case classifyLink(err, &e):
	}
	return e
}

// classifyWire fills e for the errors a mesh call returns about what was asked
// for: a provider, relay or stream complaint, an identity that did not match,
// or a station that would not register it.
func classifyWire(err error, e *Error) bool {
	var provider *stationlink.ProviderError
	var relay *stationlink.RelayError
	var stream *stationlink.StreamError
	var refusedRegistration *stationlink.RefusedError
	var unacknowledged *stationlink.UnacknowledgedError
	switch {
	case errors.As(err, &provider):
		e.Kind, e.Code = "provider_error", provider.Code
		if provider.Detail != nil {
			e.Detail = *provider.Detail
		}
	case errors.As(err, &relay):
		e.Kind, e.Code = "relay_error", relay.Code
	case errors.As(err, &stream):
		e.Kind, e.Code, e.Detail, e.Relay = "stream_error", stream.Code, stream.Message, stream.Relay
	case errors.Is(err, handshake.ErrPeerIdentityMismatch):
		e.Kind = "identity_mismatch"
	case errors.As(err, &refusedRegistration):
		e.Kind, e.Code = "refused", refusedRegistration.Reason
		e.Detail = fmt.Sprintf("%s of %q at station %x", refusedRegistration.Kind,
			refusedRegistration.Name, refusedRegistration.Station)
	case errors.As(err, &unacknowledged):
		e.Kind = "unacknowledged"
		e.Detail = fmt.Sprintf("%s of %q at station %x", unacknowledged.Kind, unacknowledged.Name, unacknowledged.Station)
	default:
		return false
	}
	return true
}

// classifyRealm fills e for a realm that refused the caller.
func classifyRealm(err error, e *Error) bool {
	var refused refusal
	switch {
	case errors.As(err, &refused):
		code, status := refused.Refusal()
		e.Kind, e.Code, e.Detail = "realm_refusal", code, fmt.Sprintf("HTTP %d", status)
	default:
		return false
	}
	return true
}

// classifyLink fills e for what the pool could not reach: no link, no provider,
// no realm key, nothing shared, content gone, or a call that ran out of time.
func classifyLink(err error, e *Error) bool {
	var endpoint *pool.StationEndpointError
	switch {
	case errors.Is(err, stationlink.ErrCallTimeout), errors.Is(err, context.DeadlineExceeded):
		e.Kind = "timeout"
	case errors.Is(err, pool.ErrNoLink):
		e.Kind = "no_link"
	case errors.Is(err, pool.ErrNoProvider):
		e.Kind = "no_provider"
	case errors.As(err, &endpoint):
		e.Kind, e.Code, e.Detail = "no_station_endpoint", endpoint.Reason, fmt.Sprintf("station %x", endpoint.Station)
	case errors.Is(err, pool.ErrNoRealmKey):
		e.Kind = "no_realm_key"
	case errors.Is(err, stationlink.ErrRecordNotFound):
		e.Kind = "not_found"
	case errors.Is(err, pool.ErrNotShared):
		e.Kind = "not_shared"
	case errors.Is(err, pool.ErrContentUnavailable):
		e.Kind = "content_unavailable"
	default:
		return false
	}
	return true
}

func emit(env Envelope) {
	enc := json.NewEncoder(Out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(env)
}
