package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/macula-io/macula-cli/internal/report"
)

// runCaptured runs the CLI with its stdout and stderr captured.
func runCaptured(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errs bytes.Buffer
	oldOut, oldErrs := report.Out, report.Errs
	report.Out, report.Errs = &out, &errs
	t.Cleanup(func() { report.Out, report.Errs = oldOut, oldErrs })
	code := run(args)
	return code, out.String(), errs.String()
}

func envelope(t *testing.T, stdout string) report.Envelope {
	t.Helper()
	var env report.Envelope
	dec := json.NewDecoder(strings.NewReader(stdout))
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("stdout is not one JSON envelope: %v\n%q", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one envelope: %q", stdout)
	}
	return env
}

const deadSeed = "127.0.0.1:1@abababababababababababababababababababababababababababababababab"

func TestAMalformedInvocationWithJSONIsAnEnvelopeAndExit2(t *testing.T) {
	for _, args := range [][]string{
		{"call", "-json", "-bogus", "x"},
		{"call", "-json"},
		{"realm", "status", "-json"},
		{"pubsub", "nonsense", "-json"},
		{"serve", "-json", "-reply", "true", "-seed", deadSeed, "-realm", "r", "p"},
		{"identity", "prove-ownership", "-json", "-realm", "r", "-procedure", "p", "-payload", `{"caller": "me"}`},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, stdout, _ := runCaptured(t, args...)
			if code != 2 {
				t.Fatalf("exit %d, want 2", code)
			}
			env := envelope(t, stdout)
			if env.OK || env.Error == nil || env.Error.Kind != "invalid_argument" {
				t.Fatalf("envelope %+v, want invalid_argument", env)
			}
		})
	}
}

func TestHelpIsNotAFailure(t *testing.T) {
	if code, _, _ := runCaptured(t, "call", "-h"); code != 0 {
		t.Fatalf("call -h exits %d, want 0", code)
	}
}

func TestChecksThatNeedNoNetworkRunBeforeTheDial(t *testing.T) {
	for _, args := range [][]string{
		{"content", "get", "-json", "-seed", deadSeed, "-realm", "io.macula", strings.Repeat("02", 50)},
		{"realm", "join", "-json", "-realm", strings.Repeat("ab", 32)},
		{"realm", "membership", "-json", "-seed", deadSeed, "-realm", strings.Repeat("ab", 32), "-realm-key", "00"},
	} {
		t.Run(args[0]+" "+args[1], func(t *testing.T) {
			start := time.Now()
			code, stdout, _ := runCaptured(t, args...)
			if code != 2 || time.Since(start) > 2*time.Second {
				t.Fatalf("exit %d after %v, want 2 at once", code, time.Since(start))
			}
			if env := envelope(t, stdout); env.Error == nil || env.Error.Kind != "invalid_argument" {
				t.Fatalf("envelope %+v", env)
			}
		})
	}
}

func TestARealmRefusalCarriesItsKindAndCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error": "session_not_found"}`))
	}))
	defer server.Close()
	code, stdout, _ := runCaptured(t, "realm", "status", "-json", "-realm-url", server.URL, "s-9")
	if code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	env := envelope(t, stdout)
	if env.Error == nil || env.Error.Kind != "realm_refusal" || env.Error.Code != "session_not_found" || env.Error.Detail != "HTTP 404" {
		t.Fatalf("envelope %+v", env.Error)
	}
}

func TestIdentityPrintsOneEnvelope(t *testing.T) {
	path := t.TempDir() + "/k.key"
	code, stdout, _ := runCaptured(t, "identity", "-json", "-profile", "pq_pure", "-identity", path)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if env := envelope(t, stdout); !env.OK {
		t.Fatalf("envelope %+v", env)
	}
}
