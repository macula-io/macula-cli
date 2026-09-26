package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/devicerequest"
	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/pool"

	"github.com/macula-io/macula-cli/internal/report"
	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// A realm joins a device in two steps a human stands between: the device
// asks for a join session (realm join), someone admits it at the join URL,
// and the device reads the outcome (realm status). A member node asks for its
// membership UCAN over the mesh (realm membership). Every request carries a
// realm proof v2 (devicerequest): the device's key signs the whole request,
// this realm, the procedure, a timestamp and a nonce.

// joinSession is the realm's answer to a join request.
type joinSession struct {
	SessionID string `json:"session_id"`
	JoinURL   string `json:"join_url"`
	ExpiresAt string `json:"expires_at"`
}

// sessionStatus is a join session's state: pending, or confirmed with what
// the realm granted.
type sessionStatus struct {
	Status      string  `json:"status"`
	ExpiresAt   string  `json:"expires_at,omitempty"`
	OrgIdentity string  `json:"org_identity,omitempty"`
	CitizenDID  string  `json:"citizen_did,omitempty"`
	CertPEM     string  `json:"cert_pem,omitempty"`
	UCAN        *string `json:"ucan,omitempty"`
}

// realmRefusal is the realm's error body.
type realmRefusal struct {
	Status int
	Reason string
}

func (r *realmRefusal) Error() string {
	return fmt.Sprintf("the realm refused it: HTTP %d %s", r.Status, r.Reason)
}

// requestJoin asks the realm at baseURL for a join session for key's device,
// signed for the realm named realmName.
func requestJoin(ctx context.Context, client *http.Client, baseURL, realmName string, key *identity.NodeKey,
	deviceInfo map[string]any) (joinSession, error) {
	body := map[string]any{"public_key": base64.StdEncoding.EncodeToString(key.PublicKey()), "device_info": deviceInfo}
	unsigned, err := json.Marshal(body)
	if err != nil {
		return joinSession{}, err
	}
	request, err := devicerequest.JSONRequest(unsigned)
	if err != nil {
		return joinSession{}, err
	}
	proof, err := devicerequest.Sign(key, sha256.Sum256([]byte(realmName)), devicerequest.ProcedureJoinSession, request)
	if err != nil {
		return joinSession{}, err
	}
	body["proof"] = proof
	var session joinSession
	if err := realmHTTP(ctx, client, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/v1/join/sessions", body,
		http.StatusCreated, &session); err != nil {
		return joinSession{}, err
	}
	return session, nil
}

// joinStatus reads a join session's state.
func joinStatus(ctx context.Context, client *http.Client, baseURL, sessionID string) (sessionStatus, error) {
	var status sessionStatus
	err := realmHTTP(ctx, client, http.MethodGet, strings.TrimRight(baseURL, "/")+"/api/v1/join/sessions/"+sessionID, nil,
		http.StatusOK, &status)
	return status, err
}

func realmHTTP(ctx context.Context, client *http.Client, method, url string, body any, want int, out any) error {
	var reader io.Reader
	if body != nil {
		text, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(text)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != want {
		var refusal struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(answer, &refusal)
		if refusal.Error == "" {
			refusal.Error = strings.TrimSpace(string(answer))
		}
		return &realmRefusal{Status: res.StatusCode, Reason: refusal.Error}
	}
	return json.Unmarshal(answer, out)
}

// membershipResult is a membership UCAN the realm issued.
type membershipResult struct {
	CitizenDID string  `json:"citizen_did"`
	Result     rawJSON `json:"result"`
}

// caller makes one call; a pool's Call.
type caller func(ctx context.Context, c pool.Call) (cbor.Value, error)

// requestMembership asks the realm named realmName, over the mesh, for key's
// node's membership UCAN.
func requestMembership(ctx context.Context, call caller, key *identity.NodeKey, realmName string, timeout time.Duration) (membershipResult, error) {
	realm := sha256.Sum256([]byte(realmName))
	carried := cbor.Text(base64.StdEncoding.EncodeToString(key.PublicKey()))
	request := cbor.Map([]cbor.MapEntry{{Key: cbor.Text("public_key"), Val: carried}})
	proof, err := devicerequest.Sign(key, realm, devicerequest.ProcedureMembershipUCAN, request)
	if err != nil {
		return membershipResult{}, err
	}
	payload := cbor.Map([]cbor.MapEntry{
		{Key: cbor.Text("public_key"), Val: carried},
		{Key: cbor.Text("proof"), Val: cbor.Map([]cbor.MapEntry{
			{Key: cbor.Text("v"), Val: cbor.Uint64(uint64(proof.V))},
			{Key: cbor.Text("timestamp"), Val: cbor.Uint64(proof.Timestamp)},
			{Key: cbor.Text("nonce"), Val: cbor.Text(proof.Nonce)},
			{Key: cbor.Text("signature"), Val: cbor.Text(proof.Signature)},
		})},
	})
	result, err := call(ctx, pool.Call{Realm: realm, Procedure: realmName + "/_realm/_realm/identity/issue_membership_ucan_v1",
		Payload: payload, Timeout: timeout})
	if err != nil {
		return membershipResult{}, err
	}
	// The realm answers citizen_did as the bytes of its hex text.
	var did string
	if v, ok := result.Get("citizen_did"); ok {
		if t, ok := v.AsText(); ok {
			did = t
		} else if b, ok := v.AsBytes(); ok {
			did = string(b)
		}
	}
	return membershipResult{CitizenDID: did, Result: rawJSON(wirevalue.ToJSON(result))}, nil
}

func runRealm(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: macula-cli realm join|status|membership ...")
		return 2
	}
	switch args[0] {
	case "join":
		return runRealmJoin(args[1:])
	case "status":
		return runRealmStatus(args[1:])
	case "membership":
		return runRealmMembership(args[1:])
	}
	fmt.Fprintf(os.Stderr, "macula-cli realm: unknown subcommand %q (join, status, membership)\n", args[0])
	return 2
}

func runRealmJoin(args []string) int {
	fs := flag.NewFlagSet("realm join", flag.ContinueOnError)
	var m meshFlags
	m.register(fs, true)
	realmURL := fs.String("realm-url", "https://realm.macula.io", "the realm's HTTP base URL")
	hostname := fs.String("hostname", "", "the hostname the admitter reads (default: this machine's)")
	wait := fs.Duration("wait", 0, "wait this long for a human to admit the device, polling the session")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli realm join -realm <realm name> [flags]")
		fmt.Fprintln(fs.Output(), "       asks the realm for a join session; a human admits the device at the join URL")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	if m.realm == "" {
		return report.Usage(m.jsonOut, errors.New("-realm is the realm's name (io.macula): the proof signs sha256 of it"))
	}
	key, err := m.key()
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	host := *hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	info := map[string]any{"hostname": host, "os": runtime.GOOS + "/" + runtime.GOARCH, "version": "macula-cli " + version}
	ctx := context.Background()
	client := &http.Client{Timeout: m.timeout}
	session, err := requestJoin(ctx, client, *realmURL, m.realm, key, info)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	if *wait <= 0 {
		report.Ok(m.jsonOut, session, func(w io.Writer) {
			fmt.Fprintf(w, "join session %s, pending until %s\nadmit the device at %s\n", session.SessionID, session.ExpiresAt, session.JoinURL)
		})
		return 0
	}
	if !m.jsonOut {
		fmt.Fprintf(os.Stderr, "admit the device at %s; waiting\n", session.JoinURL)
	}
	status, err := waitAdmitted(ctx, client, *realmURL, session.SessionID, *wait, 2*time.Second)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	report.Ok(m.jsonOut, map[string]any{"session": session, "status": status}, func(w io.Writer) { printStatus(w, status) })
	return 0
}

// waitAdmitted polls the session every interval until it is no longer
// pending or wait ran out.
func waitAdmitted(ctx context.Context, client *http.Client, baseURL, sessionID string, wait, interval time.Duration) (sessionStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		status, err := joinStatus(ctx, client, baseURL, sessionID)
		if err != nil || status.Status != "pending" {
			return status, err
		}
		select {
		case <-ctx.Done():
			return status, nil
		case <-time.After(interval):
		}
	}
}

func printStatus(w io.Writer, s sessionStatus) {
	fmt.Fprintf(w, "status %s\n", s.Status)
	if s.Status == "confirmed" {
		fmt.Fprintf(w, "org %s\ncitizen_did %s\n", s.OrgIdentity, s.CitizenDID)
	}
}

func runRealmStatus(args []string) int {
	fs := flag.NewFlagSet("realm status", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON envelope")
	realmURL := fs.String("realm-url", "https://realm.macula.io", "the realm's HTTP base URL")
	timeout := fs.Duration("timeout", 30*time.Second, "how long to wait for the realm")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli realm status [flags] <session id>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	status, err := joinStatus(context.Background(), &http.Client{Timeout: *timeout}, *realmURL, fs.Arg(0))
	if err != nil {
		return report.Fail(*jsonOut, err)
	}
	report.Ok(*jsonOut, status, func(w io.Writer) { printStatus(w, status) })
	return 0
}

func runRealmMembership(args []string) int {
	fs := flag.NewFlagSet("realm membership", flag.ContinueOnError)
	var m meshFlags
	m.register(fs, true)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli realm membership -seed host:port@<node_id> -realm <realm name> -realm-key <hex|@file> [flags]")
		fmt.Fprintln(fs.Output(), "       asks the realm, over the mesh, for this node's membership UCAN; the node must be admitted")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	if _, err := hex32(m.realm); err == nil || m.realm == "" {
		return report.Usage(m.jsonOut, errors.New("-realm is the realm's name (io.macula): the procedure is named under it"))
	}
	key, err := m.key()
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	ctx := context.Background()
	p, err := m.connect(ctx, key, nil)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	defer p.Close()
	r, err := requestMembership(ctx, p.Call, key, m.realm, m.timeout)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	report.Ok(m.jsonOut, r, func(w io.Writer) { fmt.Fprintf(w, "citizen_did %s\n%s\n", r.CitizenDID, r.Result) })
	return 0
}
