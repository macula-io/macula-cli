package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/macula-io/macula-go/cbor"
	"github.com/macula-io/macula-go/identity"
	"github.com/macula-io/macula-go/pool"
	"github.com/macula-io/macula-go/profile"
	"github.com/macula-io/macula-go/stationlink"
	"github.com/macula-io/macula-go/ucan"

	"github.com/macula-io/macula-cli/internal/report"
	"github.com/macula-io/macula-cli/internal/wirevalue"
)

// servedCall is one call a served procedure answered; Sealed is 1 when the
// request came sealed end to end.
type servedCall struct {
	Caller  string  `json:"caller"`
	Payload rawJSON `json:"payload"`
	Sealed  int     `json:"sealed"`
}

// serveOptions say what serve answers, how it takes requests and when it
// stops.
type serveOptions struct {
	reply        *cbor.Value // nil: echo the caller's payload
	once         bool        // stop after answering one call
	confidential stationlink.Confidentiality
	policy       ucan.Policy // nil: serve any caller
}

// memberPolicy is -require-member: serve only a caller whose UCAN chain is
// rooted at the realm key (-realm-key, as carried) and grants can, the gate a
// fleet service names (realm_member_required). The key id is taken under the
// node's profile, as the gate checks it.
func memberPolicy(can, realmKeyText, profileText string) (ucan.Policy, error) {
	if strings.TrimSpace(can) == "" {
		return nil, errors.New("-require-member: name the capability a member's chain must grant (member/email-verified)")
	}
	if realmKeyText == "" {
		return nil, errors.New("-require-member needs -realm-key: the gate is rooted at the realm's key")
	}
	key, err := realmKey(realmKeyText)
	if err != nil {
		return nil, err
	}
	p, err := profile.Parse(profileText)
	if err != nil {
		return nil, err
	}
	return ucan.RealmMemberRequired{KeyID: identity.KeyIDOf(key, p), Can: can}, nil
}

// confidentialFlag reads -confidential: preferred (seal when the caller can),
// required (refuse clear requests) or off (serve in the clear, name no key).
func confidentialFlag(text string) (stationlink.Confidentiality, error) {
	switch text {
	case "preferred":
		return stationlink.ConfidentialPreferred, nil
	case "required":
		return stationlink.ConfidentialRequired, nil
	case "off":
		return stationlink.ConfidentialOff, nil
	}
	return 0, fmt.Errorf("-confidential: %q is not preferred, required or off", text)
}

// serve serves procedure in realm until ctx ends, or after one call with
// once, reporting each call to seen. It answers reply, or echoes the payload.
// err is a failure to serve at all; withdrawErr a failure to withdraw the
// procedure at the end, which leaves the advertisement to lapse on its own.
func serve(ctx context.Context, p *pool.Pool, realm [32]byte, procedure string, o serveOptions,
	seen func(servedCall)) (full string, withdrawErr error, err error) {
	procedure = ownProcedure(procedure, p.NodeID())
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	handler := func(_ context.Context, r stationlink.Request) (cbor.Value, error) {
		sealed := 0
		if r.Sealed {
			sealed = 1
		}
		seen(servedCall{Caller: fmt.Sprintf("%x", r.Caller), Payload: rawJSON(wirevalue.ToJSON(r.Payload)),
			Sealed: sealed})
		if o.once {
			// Let the answer go out before the procedure is withdrawn.
			time.AfterFunc(500*time.Millisecond, stop)
		}
		if o.reply != nil {
			return *o.reply, nil
		}
		return r.Payload, nil
	}
	served, err := p.Serve(ctx, pool.Offer{Realm: realm, Procedure: procedure, Handler: handler,
		Confidential: o.confidential, Policy: o.policy})
	if err != nil {
		return procedure, nil, err
	}
	<-ctx.Done()
	return procedure, served.Stop(), nil
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var m meshFlags
	m.register(fs, true)
	replyText := fs.String("reply", "", "answer every call with this JSON; echo the caller's payload when absent")
	once := fs.Bool("once", false, "exit after answering one call")
	forTime := fs.Duration("for", 0, "stop serving after this long (default: until interrupted)")
	confidentialText := fs.String("confidential", "preferred",
		"preferred: advertise a KEM key and answer sealed calls sealed; required: also refuse clear calls; off: clear only")
	requireMember := fs.String("require-member", "",
		"serve only realm members: a caller whose UCAN chain, rooted at -realm-key, grants this can (member/email-verified)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "usage: macula-cli serve -seed host:port@<node_id> -realm <realm> [-realm-key <hex|@file>] [flags] <procedure>")
		fmt.Fprintln(fs.Output(), "       '~/<name>' (quoted) serves <name> in this node's own namespace, which needs no org and no realm key;")
		fmt.Fprintln(fs.Output(), "       <org>/<name> needs the org's delegation to this node in the DHT")
		fs.PrintDefaults()
	}
	if code, ok := parse(fs, args, &m.jsonOut, exactly(1)); !ok {
		return code
	}
	var o serveOptions
	o.once = *once
	confidential, err := confidentialFlag(*confidentialText)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	o.confidential = confidential
	if *requireMember != "" {
		if strings.HasPrefix(fs.Arg(0), "~") {
			return report.Usage(m.jsonOut, errors.New("-require-member gates an <org>/<name> procedure; a node's own namespace cannot be gated"))
		}
		if o.policy, err = memberPolicy(*requireMember, m.realmKey, m.profile); err != nil {
			return report.Usage(m.jsonOut, err)
		}
	}
	m.kemAdvertise = confidential != stationlink.ConfidentialOff
	if *replyText != "" {
		v, err := wirevalue.FromJSON([]byte(*replyText))
		if err != nil {
			return report.Usage(m.jsonOut, fmt.Errorf("-reply: %w", err))
		}
		o.reply = &v
	}
	realm, err := realmID(m.realm)
	if err != nil {
		return report.Usage(m.jsonOut, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *forTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *forTime)
		defer cancel()
	}
	p, err := m.join(ctx)
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	defer p.Close()
	var (
		mu    sync.Mutex
		calls = []servedCall{}
	)
	if !m.jsonOut {
		fmt.Fprintf(os.Stderr, "serving as node %x; interrupt to stop\n", p.NodeID())
	}
	procedure, withdrawErr, err := serve(ctx, p, realm, fs.Arg(0), o, func(c servedCall) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, c)
		if !m.jsonOut {
			fmt.Fprintf(report.Out, "call from %s: %s\n", c.Caller, c.Payload)
		}
	})
	if err != nil {
		return report.Fail(m.jsonOut, err)
	}
	// The calls answered are reported whether or not the withdrawal went
	// through; a procedure not withdrawn lapses when its advertisement does.
	result := map[string]any{"procedure": procedure, "calls": calls, "withdrawn": 1}
	if withdrawErr != nil {
		result["withdrawn"], result["withdraw_error"] = 0, withdrawErr.Error()
	}
	report.Ok(m.jsonOut, result, func(w io.Writer) {
		fmt.Fprintf(w, "stopped serving %s after %d calls\n", procedure, len(calls))
		if withdrawErr != nil {
			fmt.Fprintf(w, "not withdrawn (it lapses on its own): %v\n", withdrawErr)
		}
	})
	return 0
}
