package operator

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/alexseymer/haetae/internal/operator/provider"
)

// Failure classes, used in logs, the connection test and user-facing reasons.
const (
	ClassAuth        = "auth"
	ClassRateLimit   = "rate_limit"
	ClassBadRequest  = "bad_request"
	ClassUnavailable = "unavailable"
	ClassTimeout     = "timeout"
	ClassCap         = "cap"
	ClassRedaction   = "redaction"
	ClassDisabled    = "disabled"
	ClassCanceled    = "canceled"
	ClassUnknown     = "unknown"
)

const maxReasonRunes = 240

// Failure describes why an AI call failed, in terms a user can act on.
type Failure struct {
	Class  string
	Status int    // HTTP status, 0 when none
	Detail string // short scrubbed cause from the provider, may be empty
	Reason string // plain text for "The operator is unavailable (<Reason>)"
	Hint   string // what to change, may be empty
}

// Classify maps an error from an operator call to a Failure. It never
// includes the API key, the question or provider output beyond Detail.
func Classify(err error) Failure {
	f := Failure{Class: ClassUnknown}
	var pe *provider.Error
	hasPE := errors.As(err, &pe)
	if hasPE {
		f.Status, f.Detail = pe.Status, pe.Detail
	}
	switch {
	case err == nil:
		return Failure{}
	case errors.Is(err, ErrCapped):
		f.Class, f.Reason = ClassCap, "daily usage cap reached"
		f.Hint = "Raise the daily token cap in Settings, AI operator, or wait until tomorrow (UTC)."
	case errors.Is(err, ErrDisabled):
		f.Class, f.Reason = ClassDisabled, "turned off"
	case errors.Is(err, ErrRedaction):
		f.Class, f.Reason = ClassRedaction, "redaction failed, nothing was sent to the provider"
	case errors.Is(err, context.Canceled) && !hasPE:
		f.Class, f.Reason = ClassCanceled, "request cancelled"
	case (hasPE && pe.Timeout) || (!hasPE && errors.Is(err, context.DeadlineExceeded)):
		f.Class, f.Reason = ClassTimeout, "provider unreachable: the provider did not answer in time"
		f.Hint = "The model may be slow or overloaded. Try again, or choose a faster model."
	case errors.Is(err, provider.ErrAuth):
		f.Class = ClassAuth
		f.Reason = "the provider rejected the API key"
		if f.Status != 0 {
			f.Reason += fmt.Sprintf(" (HTTP %d)", f.Status)
		}
		f.Hint = "Check HAETAE_AI_API_KEY: it must be a valid, active key for this provider."
	case errors.Is(err, provider.ErrRateLimited):
		f.Class, f.Reason = ClassRateLimit, "the provider is rate limiting requests, try again in a minute"
		f.Hint = "Wait a minute, or use a model or plan with a higher rate limit."
	case errors.Is(err, provider.ErrBadResponse) && hasPE:
		f.Class = ClassBadRequest
		f.Reason = "the provider rejected the request"
		if f.Detail != "" {
			f.Reason += ": " + f.Detail
		}
		f.Reason += ". Check HAETAE_AI_MODEL and HAETAE_AI_BASE_URL"
		f.Hint = "Use the model id exactly as the provider lists it (OpenRouter: vendor/model) and the provider's own base URL."
	case errors.Is(err, provider.ErrBadResponse):
		f.Class, f.Reason = ClassBadRequest, "the model returned an unusable answer"
		f.Hint = "Try again, or choose a model that follows instructions better."
	case errors.Is(err, provider.ErrUnavailable) && hasPE && pe.Status >= http.StatusInternalServerError:
		f.Class = ClassUnavailable
		f.Reason = fmt.Sprintf("the provider had a server error (HTTP %d)", pe.Status)
		f.Hint = "This is on the provider's side. Try again later."
	case errors.Is(err, provider.ErrUnavailable):
		f.Class, f.Reason = ClassUnavailable, "provider unreachable"
		if f.Detail != "" {
			f.Reason += ": " + f.Detail
		}
		f.Hint = "Check HAETAE_AI_BASE_URL, DNS and the server's outbound network access."
	default:
		f.Reason = "provider unreachable"
	}
	f.Reason = cleanReason(f.Reason)
	return f
}

// Reason is Classify(err).Reason.
func Reason(err error) string { return Classify(err).Reason }

func cleanReason(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > maxReasonRunes {
		s = string([]rune(s)[:maxReasonRunes-3]) + "..."
	}
	return s
}
