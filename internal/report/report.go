// Package report gives every macula-cli subcommand one consistent way
// to emit a result: a JSON envelope for scripts/agents, or a plain
// human-readable summary, from the same data. Failures are reported
// through Macula's own BOLT#4 vocabulary (see macula-go's bolt4
// package) rather than ad hoc text, so a caller parsing --json output
// gets the same failure taxonomy the wire protocol itself uses.
package report

import (
	"encoding/json"
	"fmt"
	"os"
)

// Envelope is the top-level --json shape every subcommand emits.
type Envelope struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error *Error `json:"error,omitempty"`
}

// Error is the failure shape. Bolt4Code/Bolt4Name are populated only
// when the failure is a wire-level BOLT#4 error (a CALL ERROR frame or
// a STREAM_ERROR carrying one) — a local failure (DNS, timeout,
// connection refused) leaves them empty rather than guessing a code.
type Error struct {
	Message   string `json:"message"`
	Bolt4Code *uint8 `json:"bolt4_code,omitempty"`
	Bolt4Name string `json:"bolt4_name,omitempty"`
	Retryable *bool  `json:"retryable,omitempty"`
}

// Ok emits a success envelope. humanLines, if non-nil, is called
// instead of JSON when jsonOut is false — its own output should go to
// stdout via fmt.Println etc.
func Ok(jsonOut bool, data any, human func()) {
	if jsonOut {
		emit(Envelope{OK: true, Data: data})
		return
	}
	if human != nil {
		human()
	}
}

// Fail emits a failure envelope and returns the process exit code (1)
// the caller should return from main. wireErr carries BOLT#4 detail
// when the failure came from a parsed CALL/STREAM error rather than a
// local Go error — pass nil for a purely local failure.
func Fail(jsonOut bool, err error, wireErr *Error) int {
	e := &Error{Message: err.Error()}
	if wireErr != nil {
		e.Bolt4Code = wireErr.Bolt4Code
		e.Bolt4Name = wireErr.Bolt4Name
		e.Retryable = wireErr.Retryable
	}
	if jsonOut {
		emit(Envelope{OK: false, Error: e})
	} else {
		fmt.Fprintf(os.Stderr, "error: %s", e.Message)
		if e.Bolt4Name != "" {
			fmt.Fprintf(os.Stderr, " (bolt4=%s", e.Bolt4Name)
			if e.Retryable != nil {
				fmt.Fprintf(os.Stderr, ", retryable=%v", *e.Retryable)
			}
			fmt.Fprint(os.Stderr, ")")
		}
		fmt.Fprintln(os.Stderr)
	}
	return 1
}

func emit(env Envelope) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(env)
}
