package operator_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/operator/provider"
)

func perr(kind error, status int, detail string) error {
	return fmt.Errorf("operator: provider: %w", &provider.Error{Kind: kind, Status: status, Detail: detail})
}

func TestClassifyReasons(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		class  string
		reason string
	}{
		{"auth", perr(provider.ErrAuth, 401, "bad key"), operator.ClassAuth, "the provider rejected the API key (HTTP 401)"},
		{"forbidden is not a key problem", perr(provider.ErrBadResponse, 403, "nope"), operator.ClassBadRequest,
			"the provider rejected the request: nope. Check HAETAE_AI_MODEL and HAETAE_AI_BASE_URL"},
		{"pii block", perr(provider.ErrBlocked, 403, "Request blocked: PII detected (redaction_context_lost)"), operator.ClassBlocked,
			"the provider's safety filter blocked this question; try rephrasing or check the provider settings"},
		{"model not found", perr(provider.ErrBadResponse, 404, "No endpoints found for x/y"), operator.ClassBadRequest,
			"the provider rejected the request: No endpoints found for x/y. Check HAETAE_AI_MODEL and HAETAE_AI_BASE_URL"},
		{"bad request without detail", perr(provider.ErrBadResponse, 400, ""), operator.ClassBadRequest,
			"the provider rejected the request. Check HAETAE_AI_MODEL and HAETAE_AI_BASE_URL"},
		{"rate limit", perr(provider.ErrRateLimited, 429, "slow"), operator.ClassRateLimit, "the provider is rate limiting requests, try again in a minute"},
		{"server error", perr(provider.ErrUnavailable, 503, ""), operator.ClassUnavailable, "the provider had a server error (HTTP 503)"},
		{"network", perr(provider.ErrUnavailable, 0, "dial tcp: lookup x: no such host"), operator.ClassUnavailable,
			"provider unreachable: dial tcp: lookup x: no such host"},
		{"timeout", perr(provider.ErrUnavailable, 0, "timeout awaiting response"), operator.ClassUnavailable, ""}, // replaced below
		{"cap", operator.ErrCapped, operator.ClassCap, "daily usage cap reached"},
		{"disabled", operator.ErrDisabled, operator.ClassDisabled, "turned off"},
		{"unusable answer", fmt.Errorf("%w: no JSON object", provider.ErrBadResponse), operator.ClassBadRequest, "the model returned an unusable answer"},
		{"legacy unavailable", fmt.Errorf("%w: x", provider.ErrUnavailable), operator.ClassUnavailable, "provider unreachable"},
		{"redaction", fmt.Errorf("%w: boom", operator.ErrRedaction), operator.ClassRedaction, "redaction failed, nothing was sent to the provider"},
	}
	for _, c := range cases {
		if c.name == "timeout" {
			continue
		}
		f := operator.Classify(c.err)
		if f.Class != c.class || f.Reason != c.reason {
			t.Errorf("%s: got %q / %q, want %q / %q", c.name, f.Class, f.Reason, c.class, c.reason)
		}
	}
	for _, err := range []error{
		&provider.Error{Kind: provider.ErrUnavailable, Detail: "timeout awaiting response", Timeout: true},
		fmt.Errorf("x: %w", context.DeadlineExceeded),
	} {
		f := operator.Classify(err)
		if f.Class != operator.ClassTimeout || !strings.Contains(f.Reason, "provider unreachable") || !strings.Contains(f.Reason, "did not answer in time") {
			t.Errorf("timeout: %+v", f)
		}
	}
	// The wrapped sentinels keep working.
	if !errors.Is(perr(provider.ErrAuth, 401, ""), provider.ErrAuth) {
		t.Fatal("errors.Is lost")
	}
}

func TestReasonIsPlainAndBounded(t *testing.T) {
	r := operator.Reason(perr(provider.ErrBadResponse, 400, "a\x00b\x1b[0m\n"+strings.Repeat("z", 500)))
	if strings.ContainsAny(r, "\x00\x1b\n") || len([]rune(r)) > 240 {
		t.Fatalf("reason = %q", r)
	}
}
