package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/connection"

	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// DefaultExecTimeout bounds one exec-backed handler invocation. Every
// procedure registered on a daemon shares ONE serveSession (see
// Server's own doc on why) -- ServeForever answers inbound CALLs one at
// a time on that session's control stream, so a hung external command
// wedges every OTHER registered procedure too, not just its own. This
// exists to guarantee a runaway or misbehaving script can't do that
// silently; -exec-timeout overrides it per registration.
const DefaultExecTimeout = 10 * time.Second

// BuildReplyHandler returns the CallHandler "serve" and "serve -daemon"
// both build a registration from -- exec (if set) takes precedence over
// reply/echo; the CLI layer is responsible for treating them as
// mutually exclusive (flag validation), not this function. Shared here
// so both call sites (cmd/macula-cli/serve.go's direct path, and
// Server.Register below) construct a handler identically rather than
// two copies of the same reply/echo/exec branching drifting apart.
func BuildReplyHandler(reply json.RawMessage, echo bool, execCmd string, execTimeout time.Duration) (connection.CallHandler, error) {
	if execCmd != "" {
		if execTimeout <= 0 {
			execTimeout = DefaultExecTimeout
		}
		return buildExecHandler(execCmd, execTimeout), nil
	}

	replyValue := cbor.Null()
	if len(reply) > 0 {
		v, err := wirevalue.FromJSON(reply)
		if err != nil {
			return nil, fmt.Errorf("reply: %w", err)
		}
		replyValue = v
	}
	return func(payload cbor.Value) (cbor.Value, error) {
		if echo {
			return payload, nil
		}
		return replyValue, nil
	}, nil
}

// buildExecHandler runs execCmd through a shell (cmd /C on Windows, sh
// -c elsewhere) once per inbound CALL, writes the call's payload to its
// stdin as one JSON document, and parses its entire stdout as the
// reply -- the closest thing to a dynamic handler this package offers
// without embedding a script interpreter of its own. A non-zero exit,
// a timeout, or stdout that isn't valid JSON all become a CallHandler
// error, which connection.ServeForever's own contract turns into a
// normal BOLT#4 ERROR frame back to the caller (see its doc on
// CallHandler) -- never a crash of the shared serve loop, so one
// misbehaving exec-backed procedure can only ever fail its OWN calls
// with a clear error, not take down anything else registered.
func buildExecHandler(execCmd string, timeout time.Duration) connection.CallHandler {
	return func(payload cbor.Value) (cbor.Value, error) {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "cmd", "/C", execCmd)
		} else {
			cmd = exec.CommandContext(ctx, "sh", "-c", execCmd)
		}

		inputJSON, err := json.Marshal(wirevalue.ToJSON(payload))
		if err != nil {
			return cbor.Null(), fmt.Errorf("exec: encode payload: %w", err)
		}
		cmd.Stdin = bytes.NewReader(inputJSON)

		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		runErr := cmd.Run()
		if ctx.Err() == context.DeadlineExceeded {
			return cbor.Null(), fmt.Errorf("exec %q: timed out after %s", execCmd, timeout)
		}
		if runErr != nil {
			if detail := strings.TrimSpace(stderr.String()); detail != "" {
				return cbor.Null(), fmt.Errorf("exec %q: %w: %s", execCmd, runErr, detail)
			}
			return cbor.Null(), fmt.Errorf("exec %q: %w", execCmd, runErr)
		}

		// Empty stdout is treated as a deliberate null reply, not an
		// error -- the same "null is a legitimate empty reply"
		// convention -reply's own default already uses, useful for a
		// script that's purely side-effecting (e.g. it only logs to
		// stderr) and has nothing to say back.
		out := bytes.TrimSpace(stdout.Bytes())
		if len(out) == 0 {
			return cbor.Null(), nil
		}
		replyValue, err := wirevalue.FromJSON(out)
		if err != nil {
			return cbor.Null(), fmt.Errorf("exec %q: stdout is not valid JSON: %w", execCmd, err)
		}
		return replyValue, nil
	}
}
