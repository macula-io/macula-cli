package report

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/macula-io/macula-go/handshake"
	"github.com/macula-io/macula-go/pool"
	"github.com/macula-io/macula-go/stationlink"
)

func TestFailuresAreClassifiedByTheirTypeNotTheirText(t *testing.T) {
	detail := "boom"
	cases := []struct {
		err  error
		want Error
	}{
		{&stationlink.ProviderError{Code: "handler_error", Detail: &detail},
			Error{Kind: "provider_error", Code: "handler_error", Detail: "boom"}},
		{fmt.Errorf("call: %w", &stationlink.RelayError{Code: "unknown_next_peer"}),
			Error{Kind: "relay_error", Code: "unknown_next_peer"}},
		{&stationlink.StreamError{Code: "error", Message: "x", Relay: true},
			Error{Kind: "stream_error", Code: "error", Detail: "x", Relay: true}},
		{stationlink.ErrCallTimeout, Error{Kind: "timeout"}},
		{context.DeadlineExceeded, Error{Kind: "timeout"}},
		{fmt.Errorf("x: %w", pool.ErrNoProvider), Error{Kind: "no_provider"}},
		{pool.ErrNoRealmKey, Error{Kind: "no_realm_key"}},
		{stationlink.ErrRecordNotFound, Error{Kind: "not_found"}},
		{fmt.Errorf("get: %w", pool.ErrNotShared), Error{Kind: "not_shared"}},
		{pool.ErrContentUnavailable, Error{Kind: "content_unavailable"}},
		{testRefusal{}, Error{Kind: "realm_refusal", Code: "bad_proof", Detail: "HTTP 401"}},
		{errors.Join(context.DeadlineExceeded, fmt.Errorf("dial: %w", handshake.ErrPeerIdentityMismatch)), Error{Kind: "identity_mismatch"}},
		{errors.New("anything else"), Error{Kind: "failed"}},
	}
	for _, c := range cases {
		got := classify(c.err)
		c.want.Message = c.err.Error()
		if got != c.want {
			t.Errorf("%v:\n got %+v\nwant %+v", c.err, got, c.want)
		}
	}
}

type testRefusal struct{}

func (testRefusal) Error() string          { return "refused" }
func (testRefusal) Refusal() (string, int) { return "bad_proof", 401 }
