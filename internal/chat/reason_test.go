package chat

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/alexseymer/haetae/internal/operator/provider"
)

func TestReasonForExplainsProviderFailures(t *testing.T) {
	wrap := func(k error, status int, d string) error {
		return fmt.Errorf("operator: provider: %w", &provider.Error{Kind: k, Status: status, Detail: d})
	}
	cases := map[string]error{
		"the provider rejected the API key (HTTP 401)":                                                   wrap(provider.ErrAuth, 401, ""),
		"the provider rejected the request: unknown model. Check HAETAE_AI_MODEL and HAETAE_AI_BASE_URL": wrap(provider.ErrBadResponse, 404, "unknown model"),
		"the provider is rate limiting requests, try again in a minute":                                  wrap(provider.ErrRateLimited, 429, ""),
		"the provider had a server error (HTTP 503)":                                                     wrap(provider.ErrUnavailable, 503, ""),
		"provider unreachable: no such host":                                                             wrap(provider.ErrUnavailable, 0, "no such host"),
	}
	for want, err := range cases {
		if got := reasonFor(err); got != want {
			t.Errorf("reasonFor = %q, want %q", got, want)
		}
	}
	if got := reasonFor(context.DeadlineExceeded); !strings.Contains(got, "provider unreachable") {
		t.Errorf("timeout reason = %q", got)
	}
	if got := unavailableReply(reasonFor(wrap(provider.ErrAuth, 401, ""))); got !=
		"The operator is unavailable (the provider rejected the API key (HTTP 401)); rule alerts are active." {
		t.Errorf("reply = %q", got)
	}
}
