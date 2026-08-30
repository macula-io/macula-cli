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

// Do sends one request to the daemon listening at socketPath and
// decodes its response, matching the CLI's own one-shot-per-invocation
// shape: dial, ask, get an answer, disconnect -- no persistent client
// state to manage, since nothing in this slice (serve register/
// unregister, status, shutdown) needs a standing connection or a
// stream of pushed notifications. params may be nil; result may be nil
// when the caller doesn't need the payload back.
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
		return fmt.Errorf("daemon: %s: %s", method, resp.Error.Message)
	}
	if result != nil && len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			return fmt.Errorf("daemon: decode %s result: %w", method, err)
		}
	}
	return nil
}
