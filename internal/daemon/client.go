package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// dialTimeout bounds connecting to the control socket -- generous for
// a local Unix socket, just long enough to give a genuinely wedged
// daemon a chance to be diagnosed as "not responding" rather than
// hanging the caller forever.
const dialTimeout = 3 * time.Second

// DaemonError is what Do and Watch return when the daemon answers with
// an error Response, preserving RPCError's BOLT#4 fields (rather than
// flattening straight to a plain error string) so a caller that cares
// -- call.invoke's own error path is the only one today -- can recover
// them via errors.As and reconstruct the exact same internal/report.Error
// shape the non-daemon command would have produced.
type DaemonError struct {
	Method    string
	Message   string
	Bolt4Code *uint8
	Bolt4Name string
	Retryable *bool
}

func (e *DaemonError) Error() string { return fmt.Sprintf("daemon: %s: %s", e.Method, e.Message) }

func errorFromResponse(method string, rpcErr *RPCError) error {
	return &DaemonError{
		Method:    method,
		Message:   rpcErr.Message,
		Bolt4Code: rpcErr.Bolt4Code,
		Bolt4Name: rpcErr.Bolt4Name,
		Retryable: rpcErr.Retryable,
	}
}

// Do sends one request to the daemon listening at socketPath and
// decodes its response, matching the CLI's own one-shot-per-invocation
// shape: dial, ask, get an answer, disconnect -- no persistent client
// state to manage. params may be nil; result may be nil when the
// caller doesn't need the payload back.
func Do(socketPath, method string, params any, result any) error {
	conn, err := net.DialTimeout("unix", socketPath, dialTimeout)
	if err != nil {
		return fmt.Errorf("daemon: connect to %s: %w (is \"macula-cli daemon start\" running?)", socketPath, err)
	}
	defer conn.Close()

	var paramsRaw json.RawMessage
	if params != nil {
		paramsRaw, err = json.Marshal(params)
		if err != nil {
			return fmt.Errorf("daemon: encode %s params: %w", method, err)
		}
	}
	if err := json.NewEncoder(conn).Encode(Request{ID: 1, Method: method, Params: paramsRaw}); err != nil {
		return fmt.Errorf("daemon: send %s request: %w", method, err)
	}

	var resp Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return fmt.Errorf("daemon: read %s response: %w", method, err)
	}
	if resp.Error != nil {
		return errorFromResponse(method, resp.Error)
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("daemon: decode %s result: %w", method, err)
		}
	}
	return nil
}

// WatchConn is an open control-socket connection past its initial
// pubsub.watch ack, now receiving a Notification per delivered event.
// Unlike Do, this connection's whole purpose is staying open -- Close
// it (or let the daemon end the subscription) to stop.
type WatchConn struct {
	conn net.Conn
	dec  *json.Decoder
}

// Watch sends a pubsub.watch request, waits for its ack, and returns a
// WatchConn ready for repeated Next calls. method is always
// MethodPubsubWatch today; taking it as a parameter (rather than
// hardcoding it) keeps this symmetric with Do and leaves room for a
// second streaming method later without changing this signature.
func Watch(socketPath, method string, params any) (*WatchConn, error) {
	conn, err := net.DialTimeout("unix", socketPath, dialTimeout)
	if err != nil {
		return nil, fmt.Errorf("daemon: connect to %s: %w (is \"macula-cli daemon start\" running?)", socketPath, err)
	}
	paramsRaw, err := json.Marshal(params)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("daemon: encode %s params: %w", method, err)
	}
	if err := json.NewEncoder(conn).Encode(Request{ID: 1, Method: method, Params: paramsRaw}); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("daemon: send %s request: %w", method, err)
	}

	dec := json.NewDecoder(conn)
	var resp Response
	if err := dec.Decode(&resp); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("daemon: read %s ack: %w", method, err)
	}
	if resp.Error != nil {
		_ = conn.Close()
		return nil, errorFromResponse(method, resp.Error)
	}
	return &WatchConn{conn: conn, dec: dec}, nil
}

// Next blocks for the next pushed Notification. Returns an error (most
// often io.EOF) when the daemon ends the subscription or the
// connection otherwise breaks.
func (w *WatchConn) Next() (Notification, error) {
	var n Notification
	err := w.dec.Decode(&n)
	return n, err
}

func (w *WatchConn) Close() error { return w.conn.Close() }
