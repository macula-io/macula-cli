package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/devicerequest"
	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/pool"
	"github.com/macula-io/macula-go/profile"
	"github.com/macula-io/macula-go/ucan"

	"github.com/macula-io/macula-cli/internal/identitystore"
	"github.com/macula-io/macula-cli/internal/report"
)

// A person joins a realm once, as themselves, and binds each of their clients
// once (macula-realm#46):
//
//   - person init makes the person's key, kept apart from any node's key: it
//     never connects, it only signs.
//   - person join asks the realm to admit the PERSON (a human confirms at the
//     join URL), and keeps the membership UCAN the realm issues to that key.
//   - person bind asks the realm, over the mesh, to admit one client's node as
//     the person's: the request is signed by the person key, and the realm
//     admits the client on its OWN membership, sponsored by the person. The
//     client then renews its own token (realm membership, macula-mcp) at the
//     person's tier, for as long as the person is a member, and is ended with
//     the person. person unbind ends one client.

// personFlags are the flags every person command takes.
type personFlags struct {
	jsonOut bool
	person  string
	profile string
}

func (f *personFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&f.jsonOut, "json", false, "emit a JSON envelope")
	fs.StringVar(&f.person, "person", "", "the person key file (default: the user config dir's macula-cli/person.key)")
	fs.StringVar(&f.profile, "profile", "pq_hybrid", "crypto profile: pq_hybrid (the fleet's) or pq_pure")
}

// path is the person key file.
func (f *personFlags) path() (string, error) {
	if f.person != "" {
		return f.person, nil
	}
	return identitystore.PersonPath()
}

// load loads the person key, or creates it when create is set and none exists.
func (f *personFlags) load(create bool) (*identity.NodeKey, string, error) {
	p, err := profile.Parse(f.profile)
	if err != nil {
		return nil, "", err
	}
	path, err := f.path()
	if err != nil {
		return nil, "", err
	}
	if !create {
		key, err := identity.LoadKey(path, identity.PurposeIdentity, p)
		if errors.Is(err, os.ErrNotExist) {
			return nil, path, fmt.Errorf("no person key at %s: run macula-cli person init first", path)
		}
		return key, path, err
	}
	key, created, err := identitystore.LoadOrCreate(path, p)
	if created && !f.jsonOut {
		fmt.Fprintf(os.Stderr, "created the person key %s\n", path)
	}
	return key, path, err
}

// membershipPath is where person's membership UCAN in realmName is kept:
// beside the person key, one file per person and realm, so two person keys in
// one directory never share one.
func membershipPath(personPath string, person [32]byte, realmName string) string {
	return filepath.Join(filepath.Dir(personPath), "person", fmt.Sprintf("%x", person), realmName+".ucan")
}

// claims are the parts of a token person join reads: who it is for, what
// it grants and until when. The signature is checked by ucan.Authorize.
type claims struct {
	Aud string            `json:"aud"`
	Exp int64             `json:"exp"`
	Cap []ucan.Capability `json:"cap"`
}

func readClaims(token []byte) (claims, error) {
	parts := bytes.Split(token, []byte("."))
	if len(parts) != 3 {
		return claims{}, errors.New("not a UCAN: want three dot-separated parts")
	}
	raw, err := base64.RawURLEncoding.DecodeString(string(parts[1]))
	if err != nil {
		return claims{}, fmt.Errorf("not a UCAN: the claims: %w", err)
	}
	var c claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return claims{}, fmt.Errorf("not a UCAN: the claims: %w", err)
	}
	return c, nil
}

// personResult describes the person key and, per realm asked about, its
// membership.
type personResult struct {
	PersonID   string `json:"person_id"`
	KeyFile    string `json:"key_file"`
	Profile    string `json:"profile"`
	Membership string `json:"membership,omitempty"`
	Expires    string `json:"expires,omitempty"`
}

func runPerson(args []string) int {
	if len(args) == 0 {
		return unknownSubcommand("person", args, "init, join, bind, unbind")
	}
	switch args[0] {
	case "init":
		return runPersonInit(args[1:])
	case "join":
		return runPersonJoin(args[1:])
	case "bind":
		return runPersonBind(args[1:])
	case "unbind":
		return runPersonUnbind(args[1:])
	}
	return unknownSubcommand("person", args, "init, join, bind, unbind")
}

func runPersonInit(args []string) int {
	fs := flag.NewFlagSet("person init", flag.ContinueOnError)
	var f personFlags
	f.register(fs)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli person init [flags]")
		fmt.Fprintln(fs.Output(), "       makes (or shows) your person key: it signs your requests to bind your clients and never connects")
		fs.PrintDefaults()
	}
	if code, ok := parse(fs, args, &f.jsonOut, exactly(0)); !ok {
		return code
	}
	key, path, err := f.load(true)
	if err != nil {
		return report.Fail(f.jsonOut, err)
	}
	id, err := key.NodeID()
	if err != nil {
		return report.Fail(f.jsonOut, err)
	}
	r := personResult{PersonID: fmt.Sprintf("%x", id), KeyFile: path, Profile: string(key.Profile())}
	report.Ok(f.jsonOut, r, func(w io.Writer) { fmt.Fprintf(w, "person %s\nkey %s\n", r.PersonID, r.KeyFile) })
	return 0
}

func runPersonJoin(args []string) int {
	fs := flag.NewFlagSet("person join", flag.ContinueOnError)
	var f personFlags
	f.register(fs)
	realmText := fs.String("realm", "", "the realm's name (io.macula)")
	realmURL := fs.String("realm-url", "https://realm.macula.io", "the realm's HTTP base URL")
	wait := fs.Duration("wait", 10*time.Minute, "how long to wait for you to confirm at the join URL")
	timeout := fs.Duration("timeout", 30*time.Second, "how long to wait for each realm request")
	membershipTTL := fs.Duration("membership-ttl", 720*time.Hour,
		"how long to ask the membership to last; the realm clamps it to its cap (30 days) and the join page shows it")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli person join -realm <realm name> [flags]")
		fmt.Fprintln(fs.Output(), "       asks the realm to admit you, the person: confirm at the join URL, signed in as yourself;")
		fmt.Fprintln(fs.Output(), "       keeps the membership UCAN the realm issues to your person key")
		fs.PrintDefaults()
	}
	if code, ok := parse(fs, args, &f.jsonOut, exactly(0)); !ok {
		return code
	}
	name, err := realmName(*realmText)
	if err != nil {
		return report.Usage(f.jsonOut, err)
	}
	if *wait <= 0 {
		return report.Usage(f.jsonOut, errors.New("-wait must be positive: the membership is kept once you confirm"))
	}
	if *membershipTTL < time.Second {
		return report.Usage(f.jsonOut, errors.New("-membership-ttl must be at least 1s: how long your membership lasts"))
	}
	key, path, err := f.load(true)
	if err != nil {
		return report.Fail(f.jsonOut, err)
	}
	id, err := key.NodeID()
	if err != nil {
		return report.Fail(f.jsonOut, err)
	}
	host, _ := os.Hostname()
	info := map[string]any{"hostname": "person key on " + host, "os": runtime.GOOS + "/" + runtime.GOARCH, "version": "macula-cli " + version}
	ctx := context.Background()
	client := &http.Client{Timeout: *timeout}
	session, err := requestJoin(ctx, client, *realmURL, name, key, info, *membershipTTL)
	if err != nil {
		return report.Fail(f.jsonOut, err)
	}
	if !f.jsonOut {
		fmt.Fprintf(os.Stderr, "confirm at %s, signed in as yourself; waiting\n", session.JoinURL)
	}
	status, err := waitAdmitted(ctx, client, *realmURL, session.SessionID, *wait, 2*time.Second)
	if err != nil {
		return report.Fail(f.jsonOut, err)
	}
	if status.Status != "confirmed" {
		return report.Fail(f.jsonOut, fmt.Errorf("the join session is %s, not confirmed: no membership", status.Status))
	}
	if status.UCAN == nil || *status.UCAN == "" {
		return report.Fail(f.jsonOut, errors.New("the realm confirmed the join but issued no membership UCAN"))
	}
	token := []byte(*status.UCAN)
	c, err := readClaims(token)
	if err != nil {
		return report.Fail(f.jsonOut, fmt.Errorf("the realm's membership: %w", err))
	}
	if c.Aud != fmt.Sprintf("%x", id) {
		return report.Fail(f.jsonOut, fmt.Errorf("the realm's membership is for %s, not this person %x: not kept", c.Aud, id))
	}
	file := membershipPath(path, id, name)
	if err := writePrivate(file, append(token, '\n')); err != nil {
		return report.Fail(f.jsonOut, err)
	}
	r := personResult{PersonID: fmt.Sprintf("%x", id), KeyFile: path, Profile: string(key.Profile()), Membership: file,
		Expires: time.Unix(c.Exp, 0).UTC().Format(time.RFC3339)}
	report.Ok(f.jsonOut, r, func(w io.Writer) {
		fmt.Fprintf(w, "person %s is a member of %s until %s\nmembership %s\n", r.PersonID, name, r.Expires, r.Membership)
	})
	return 0
}

// bindClient and unbindClient are the two binding procedures: the name the
// person's proof signs, and the procedure under the realm.
var (
	bindClient   = binding{proof: "macula_realm.bind_client", procedure: "bind_client_v1"}
	unbindClient = binding{proof: "macula_realm.unbind_client", procedure: "unbind_client_v1"}
)

type binding struct {
	proof     string
	procedure string
}

// bindingResult is the realm's answer: which client, and what became of it
// (bound, already_bound, unbound).
type bindingResult struct {
	Client string `json:"client"`
	Status string `json:"status"`
}

func runPersonBind(args []string) int   { return runBinding("bind", bindClient, args) }
func runPersonUnbind(args []string) int { return runBinding("unbind", unbindClient, args) }

func runBinding(name string, b binding, args []string) int {
	fs := flag.NewFlagSet("person "+name, flag.ContinueOnError)
	var m meshFlags
	m.register(fs, true)
	personFile := fs.String("person", "", "the person key file (default: the user config dir's macula-cli/person.key)")
	to := fs.String("to", "", "the client's node_id (64 hex): macula-cli identity, or macula-mcp's mesh://identity")
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "usage: macula-cli person %s -seed host:port@<node_id> -realm <realm name> -realm-key <hex|@file> -to <client node_id> [flags]\n", name)
		fmt.Fprintln(fs.Output(), "       bind: the realm admits that node as your client; it renews its own membership, and ends with yours")
		fmt.Fprintln(fs.Output(), "       unbind: the realm ends that client's membership")
		fs.PrintDefaults()
	}
	if code, ok := parse(fs, args, &m.jsonOut, exactly(0)); !ok {
		return code
	}
	client, err := hex32(*to)
	if err != nil {
		return report.Usage(m.jsonOut, fmt.Errorf("-to: %w", err))
	}
	realm, err := realmName(m.realm)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	person, _, err := (&personFlags{jsonOut: m.jsonOut, person: *personFile, profile: m.profile}).load(false)
	if err != nil {
		return report.Fail(m.jsonOut, err)
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
	r, err := requestBinding(ctx, p.Call, person, realm, client, b, m.timeout)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	report.Ok(m.jsonOut, r, func(w io.Writer) { fmt.Fprintf(w, "%s %s\n", r.Client, r.Status) })
	return 0
}

// requestBinding asks the realm named realmName, over the mesh, to bind (or
// unbind) client to person. The person key signs the request; the connection
// is any node's, since the realm reads the person from the proof alone.
func requestBinding(ctx context.Context, call caller, person *identity.NodeKey, realmName string, client [32]byte,
	b binding, timeout time.Duration) (bindingResult, error) {
	realm := sha256.Sum256([]byte(realmName))
	carried := cbor.Text(base64.StdEncoding.EncodeToString(person.PublicKey()))
	clientHex := cbor.Text(hex.EncodeToString(client[:]))
	request := cbor.Map([]cbor.MapEntry{{Key: cbor.Text("public_key"), Val: carried}, {Key: cbor.Text("client"), Val: clientHex}})
	proof, err := devicerequest.Sign(person, realm, b.proof, request)
	if err != nil {
		return bindingResult{}, err
	}
	payload := cbor.Map([]cbor.MapEntry{
		{Key: cbor.Text("public_key"), Val: carried},
		{Key: cbor.Text("client"), Val: clientHex},
		{Key: cbor.Text("proof"), Val: proofValue(proof)},
	})
	result, err := call(ctx, pool.Call{Realm: realm, Procedure: realmName + "/_realm/_realm/identity/" + b.procedure,
		Payload: payload, Timeout: timeout})
	if err != nil {
		return bindingResult{}, err
	}
	return bindingResult{Client: textOrBytes(result, "client"), Status: textOrBytes(result, "status")}, nil
}

// textOrBytes reads a text field the realm may answer as text or as its bytes.
func textOrBytes(result cbor.Value, field string) string {
	v, ok := result.Get(field)
	if !ok {
		return ""
	}
	if t, ok := v.AsText(); ok {
		return t
	}
	b, _ := v.AsBytes()
	return string(b)
}

// readChain is a chain file: the token presented on its first non-empty line,
// its chain's parents on the lines after.
func readChain(file string) ([]byte, [][]byte, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, nil, fmt.Errorf("-ucan-file: %w", err)
	}
	var lines [][]byte
	for _, l := range bytes.Split(raw, []byte("\n")) {
		if l = bytes.TrimSpace(l); len(l) > 0 {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		return nil, nil, fmt.Errorf("-ucan-file: %s holds no token", file)
	}
	return lines[0], lines[1:], nil
}

// writePrivate writes data to file, readable by its owner only, creating its
// directory owner-only. The data goes to a fresh owner-only file beside it,
// renamed over file, so it is never readable by others and never half written.
func writePrivate(file string, data []byte) error {
	dir := filepath.Dir(file)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(file)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
