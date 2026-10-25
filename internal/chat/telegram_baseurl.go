package chat

import (
	"log/slog"
)

// ResolveTelegramBaseURL returns HAETAE_TELEGRAM_BASE_URL only when
// HAETAE_TEST_HOOKS=1. Otherwise the override is ignored (with a warning) and
// the Telegram client uses the production Bot API host.
func ResolveTelegramBaseURL(getenv func(string) string, log *slog.Logger) string {
	v := getenv("HAETAE_TELEGRAM_BASE_URL")
	if v == "" {
		return ""
	}
	if getenv("HAETAE_TEST_HOOKS") == "1" {
		return v
	}
	if log != nil {
		log.Warn("HAETAE_TELEGRAM_BASE_URL ignored without HAETAE_TEST_HOOKS=1")
	}
	return ""
}
