package chat_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/alexseymer/haetae/internal/chat"
)

func TestResolveTelegramBaseURLGated(t *testing.T) {
	env := map[string]string{
		"HAETAE_TELEGRAM_BASE_URL": "http://fake.example",
	}
	getenv := func(k string) string { return env[k] }

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	if got := chat.ResolveTelegramBaseURL(getenv, log); got != "" {
		t.Fatalf("without TEST_HOOKS got %q", got)
	}
	if !strings.Contains(buf.String(), "ignored") {
		t.Fatalf("expected warning log, got %q", buf.String())
	}

	env["HAETAE_TEST_HOOKS"] = "1"
	if got := chat.ResolveTelegramBaseURL(getenv, log); got != "http://fake.example" {
		t.Fatalf("with TEST_HOOKS got %q", got)
	}
}
