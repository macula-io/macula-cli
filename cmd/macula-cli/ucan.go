package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/macula-io/macula-go-sdk/ucan"

	"github.com/macula-io/macula-cli/internal/report"
)

// runUcan is purely local -- no station, no network, same shape as
// runIdentity. It exists so a caller can mint or inspect a UCAN token
// without that logic being buried inside call/serve.
func runUcan(args []string) int {
	if len(args) == 0 {
		fmt.Println("Usage: macula-cli ucan mint|inspect ...")
		return 2
	}
	switch args[0] {
	case "mint":
		return runUcanMint(args[1:])
	case "inspect":
		return runUcanInspect(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "macula-cli ucan: unknown action %q (want mint or inspect)\n", args[0])
		return 2
	}
}

// capabilityFlag accumulates repeated -capability with:can pairs into a
// []ucan.Capability -- flag's stdlib has no repeatable-flag type, so this
// is the standard flag.Value pattern for one.
type capabilityFlag []ucan.Capability

func (c *capabilityFlag) String() string {
	return fmt.Sprintf("%v", []ucan.Capability(*c))
}

func (c *capabilityFlag) Set(s string) error {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			*c = append(*c, ucan.Capability{With: s[:i], Can: s[i+1:]})
			return nil
		}
	}
	return fmt.Errorf("-capability must be \"with:can\", got %q", s)
}

type ucanMintResult struct {
	Token   string `json:"token"`
	Issuer  string `json:"issuer"`
	Written string `json:"written,omitempty"`
}

func runUcanMint(args []string) int {
	fs := flag.NewFlagSet("ucan mint", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	identityPath := fs.String("identity", "", "path to a persisted identity seed (default: config dir) -- signs the token")
	expiresIn := fs.Duration("expires-in", 0, "token expires this long from now (0 = no expiration)")
	out := fs.String("out", "", "write the token to this file (default: print to stdout)")
	var caps capabilityFlag
	fs.Var(&caps, "capability", "a \"with:can\" capability entry; repeat for more than one")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli ucan mint [flags] <issuer> <audience>\n\n"+
			"Mints a UCAN token self-issued and signed by the local identity, matching\n"+
			"macula-go-sdk's ucan.Create exactly -- the same token verifies against\n"+
			"macula-rust-sdk, macula-dotnet-sdk, macula-php-sdk, or the Erlang reference.\n"+
			"<issuer>/<audience> are opaque DID strings, not validated here.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 2 {
		fs.Usage()
		return 2
	}
	issuer, audience := fs.Arg(0), fs.Arg(1)

	id, generated, err := loadIdentity(*identityPath)
	if err != nil {
		return report.Fail(*jsonOut, err, nil)
	}
	if generated && !*jsonOut {
		fmt.Println("(generated a new identity — puzzle grinding took a moment)")
	}

	opts := ucan.CreateOpts{}
	if *expiresIn > 0 {
		exp := time.Now().Add(*expiresIn).Unix()
		opts.ExpiresAt = &exp
	}
	token, err := ucan.Create(issuer, audience, []ucan.Capability(caps), id, opts)
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("mint: %w", err), nil)
	}

	result := ucanMintResult{Token: string(token), Issuer: issuer}
	if *out != "" {
		if err := os.WriteFile(*out, token, 0o600); err != nil {
			return report.Fail(*jsonOut, fmt.Errorf("write %s: %w", *out, err), nil)
		}
		result.Written = *out
	}
	report.Ok(*jsonOut, result, func() {
		if *out != "" {
			fmt.Printf("wrote token to %s\n", *out)
		} else {
			fmt.Println(string(token))
		}
	})
	return 0
}

type ucanInspectResult struct {
	Issuer       string            `json:"issuer"`
	Audience     string            `json:"audience"`
	Capabilities []ucan.Capability `json:"capabilities"`
	ExpiresAt    *int64            `json:"expires_at,omitempty"`
	NotBefore    *int64            `json:"not_before,omitempty"`
	Expired      bool              `json:"expired"`
	Proofs       []string          `json:"proofs,omitempty"`
	Facts        map[string]any    `json:"facts,omitempty"`
}

func runUcanInspect(args []string) int {
	fs := flag.NewFlagSet("ucan inspect", flag.ContinueOnError)
	jsonOut := fs.Bool("json", false, "emit a JSON result envelope instead of human-readable text")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "Usage: macula-cli ucan inspect [flags] <token-file>\n\n"+
			"Decodes a UCAN token's claims WITHOUT verifying its signature (ucan.Decode) --\n"+
			"for inspecting what a token claims, never for an authorization decision.\n"+
			"Pass - to read the token from stdin instead of a file.\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}

	var token []byte
	var err error
	if fs.Arg(0) == "-" {
		token, err = io.ReadAll(os.Stdin)
	} else {
		token, err = os.ReadFile(fs.Arg(0))
	}
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("read token: %w", err), nil)
	}

	payload, err := ucan.Decode(token)
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("decode: %w", err), nil)
	}
	expired, err := ucan.IsExpired(token)
	if err != nil {
		return report.Fail(*jsonOut, fmt.Errorf("check expiry: %w", err), nil)
	}

	result := ucanInspectResult{
		Issuer: payload.Issuer, Audience: payload.Audience,
		Capabilities: payload.Capabilities, ExpiresAt: payload.ExpiresAt,
		NotBefore: payload.NotBefore, Expired: expired,
		Proofs: payload.Proofs, Facts: payload.Facts,
	}
	report.Ok(*jsonOut, result, func() {
		fmt.Printf("issuer:      %s\n", result.Issuer)
		fmt.Printf("audience:    %s\n", result.Audience)
		fmt.Printf("expired:     %v\n", result.Expired)
		for _, c := range result.Capabilities {
			fmt.Printf("capability:  %s:%s\n", c.With, c.Can)
		}
	})
	return 0
}
