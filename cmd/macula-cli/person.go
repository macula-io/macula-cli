package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/profile"
	"github.com/macula-io/macula-go/ucan"

	"github.com/macula-io/macula-cli/internal/identitystore"
	"github.com/macula-io/macula-cli/internal/report"
)

// A person joins a realm once, as themselves, and lets each of their clients
// act for them with a short-lived note (macula-architecture#15):
//
//   - person init makes the person's key, kept apart from any node's key: it
//     never connects, it only signs.
//   - person join asks the realm to admit the PERSON (a human confirms at the
//     join URL), and keeps the membership UCAN the realm issues to that key.
//   - person delegate signs a note from the person to one client's node_id: a
//     UCAN whose parent is the membership, granting what it grants, expiring
//     no later than it. The note and the membership travel together as a
//     chain file, the note first; call -ucan-file and macula-mcp's
//     MACULA_MCP_UCAN present it to a realm_member_required procedure.
//
// A note is revoked by letting it expire: there is no revocation fact in this
// slice, so -ttl is kept short.

// maxNoteTTL bounds a note's life: the only way to end one is to let it run
// out.
const maxNoteTTL = 7 * 24 * time.Hour

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

// claims are the parts of a token person delegate reads: who it is for, what
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
		return unknownSubcommand("person", args, "init, join, delegate")
	}
	switch args[0] {
	case "init":
		return runPersonInit(args[1:])
	case "join":
		return runPersonJoin(args[1:])
	case "delegate":
		return runPersonDelegate(args[1:])
	}
	return unknownSubcommand("person", args, "init, join, delegate")
}

func runPersonInit(args []string) int {
	fs := flag.NewFlagSet("person init", flag.ContinueOnError)
	var f personFlags
	f.register(fs)
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli person init [flags]")
		fmt.Fprintln(fs.Output(), "       makes (or shows) your person key: it signs your membership's notes and never connects")
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
	session, err := requestJoin(ctx, client, *realmURL, name, key, info)
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

// noteResult is a note signed for a client: where its chain went, for whom,
// and until when.
type noteResult struct {
	Client  string `json:"client"`
	Person  string `json:"person"`
	Expires string `json:"expires"`
	Chain   string `json:"chain,omitempty"`
}

func runPersonDelegate(args []string) int {
	fs := flag.NewFlagSet("person delegate", flag.ContinueOnError)
	var f personFlags
	f.register(fs)
	realmText := fs.String("realm", "", "the realm's name (io.macula)")
	realmKeyText := fs.String("realm-key", "", "the realm key as carried, in hex, or @file: the membership must be its")
	to := fs.String("to", "", "the client's node_id (64 hex): macula-cli identity, or macula-mcp's mesh://identity")
	ttl := fs.Duration("ttl", 24*time.Hour, "how long the note lasts, at most 168h and never past the membership")
	out := fs.String("out", "", "write the chain (note, then membership) to this file; stdout when absent")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli person delegate -realm <realm name> -realm-key <hex|@file> -to <client node_id> [flags]")
		fmt.Fprintln(fs.Output(), "       signs a note letting one of your clients act as your membership; revoked only by expiry")
		fs.PrintDefaults()
	}
	if code, ok := parse(fs, args, &f.jsonOut, exactly(0)); !ok {
		return code
	}
	name, err := realmName(*realmText)
	if err != nil {
		return report.Usage(f.jsonOut, err)
	}
	if *realmKeyText == "" {
		return report.Usage(f.jsonOut, errors.New("-realm-key is required: the membership is checked against it before a note is signed"))
	}
	carriedRealmKey, err := realmKey(*realmKeyText)
	if err != nil {
		return report.Usage(f.jsonOut, err)
	}
	client, err := hex32(*to)
	if err != nil {
		return report.Usage(f.jsonOut, fmt.Errorf("-to: %w", err))
	}
	if *ttl <= 0 || *ttl > maxNoteTTL {
		return report.Usage(f.jsonOut, fmt.Errorf("-ttl %s: want more than 0 and at most %s, the note's only revocation is its expiry", *ttl, maxNoteTTL))
	}
	key, path, err := f.load(false)
	if err != nil {
		return report.Fail(f.jsonOut, err)
	}
	personID, err := key.NodeID()
	if err != nil {
		return report.Fail(f.jsonOut, err)
	}
	chain, r, err := delegate(key, membershipPath(path, personID, name), carriedRealmKey, client, *ttl, time.Now())
	if err != nil {
		return report.Fail(f.jsonOut, err)
	}
	if *out == "" {
		report.Ok(f.jsonOut, map[string]any{"note": r, "chain_text": string(chain)}, func(w io.Writer) {
			_, _ = w.Write(chain)
			fmt.Fprintf(os.Stderr, "note for %s until %s\n", r.Client, r.Expires)
		})
		return 0
	}
	if err := writePrivate(*out, chain); err != nil {
		return report.Fail(f.jsonOut, err)
	}
	r.Chain = *out
	report.Ok(f.jsonOut, r, func(w io.Writer) { fmt.Fprintf(w, "note for %s until %s\nchain %s\n", r.Client, r.Expires, r.Chain) })
	return 0
}

// delegate signs person's note for client and returns the chain (the note,
// then the membership, a line each). It refuses before signing unless the
// membership is the person's, signed by the realm key, and still valid, and it
// checks the chain as a gate would before handing it out.
func delegate(person *identity.NodeKey, membershipFile string, carriedRealmKey []byte, client [32]byte, ttl time.Duration,
	now time.Time) ([]byte, noteResult, error) {
	raw, err := os.ReadFile(membershipFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, noteResult{}, fmt.Errorf("no membership at %s: run macula-cli person join first", membershipFile)
	}
	if err != nil {
		return nil, noteResult{}, err
	}
	membership := bytes.TrimSpace(raw)
	c, err := readClaims(membership)
	if err != nil {
		return nil, noteResult{}, fmt.Errorf("%s: %w", membershipFile, err)
	}
	if len(c.Cap) == 0 {
		return nil, noteResult{}, fmt.Errorf("%s grants nothing", membershipFile)
	}
	personID, err := person.NodeID()
	if err != nil {
		return nil, noteResult{}, err
	}
	keyID := identity.KeyIDOf(carriedRealmKey, person.Profile())
	policy := ucan.RealmMemberRequired{KeyID: keyID, Can: c.Cap[0].Can}
	if _, err := ucan.Authorize(membership, policy, ucan.Context{Caller: personID, Profile: person.Profile(), Now: now.Unix()}); err != nil {
		return nil, noteResult{}, fmt.Errorf("the membership in %s does not hold for this person under that realm key: %w", membershipFile, err)
	}
	exp := min(now.Add(ttl).Unix(), c.Exp)
	note, err := ucan.Create(person, client, c.Cap, ucan.Options{Exp: exp, Prf: []string{ucan.ProofID(membership)}})
	if err != nil {
		return nil, noteResult{}, err
	}
	proofs := map[string][]byte{ucan.ProofID(membership): membership}
	if _, err := ucan.Authorize(note, policy, ucan.Context{Caller: client, Profile: person.Profile(), Now: now.Unix(),
		Proofs: proofs}); err != nil {
		return nil, noteResult{}, fmt.Errorf("the signed note does not hold as a chain: %w", err)
	}
	chain := []byte(strings.Join([]string{string(note), string(membership)}, "\n") + "\n")
	return chain, noteResult{Client: fmt.Sprintf("%x", client), Person: fmt.Sprintf("%x", personID),
		Expires: time.Unix(exp, 0).UTC().Format(time.RFC3339)}, nil
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
