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
// Kinds: provider_error, relay_error, stream_error, timeout, no_provider,
// no_realm_key, not_found, invalid_argument, failed.
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

func classify(err error) Error {
	e := Error{Kind: "failed", Message: err.Error()}
	var provider *stationlink.ProviderError
	var relay *stationlink.RelayError
	var stream *stationlink.StreamError
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
	case errors.Is(err, stationlink.ErrCallTimeout), errors.Is(err, context.DeadlineExceeded):
		e.Kind = "timeout"
	case errors.Is(err, pool.ErrNoProvider):
		e.Kind = "no_provider"
	case errors.Is(err, pool.ErrNoRealmKey):
		e.Kind = "no_realm_key"
	case errors.Is(err, stationlink.ErrRecordNotFound):
		e.Kind = "not_found"
	}
	return e
}

func emit(env Envelope) {
	enc := json.NewEncoder(Out)
	enc.SetIndent("", "  ")
	_ = enc.Encode(env)
}
