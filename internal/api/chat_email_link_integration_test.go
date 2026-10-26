//go:build integration

package api

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/chat/web"
	"github.com/alexseymer/haetae/internal/store"
)

type sentCode struct{ to, code string }

type fakeEmailChannel struct {
	mu   sync.Mutex
	sent []sentCode
	fail bool
}

func (f *fakeEmailChannel) Name() string { return store.ChannelEmail }
func (f *fakeEmailChannel) Send(context.Context, store.ChatMessage, *store.ChatAccount) error {
	return nil
}
func (f *fakeEmailChannel) Run(context.Context) error { return nil }
func (f *fakeEmailChannel) SendLinkCode(_ context.Context, to, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return context.DeadlineExceeded
	}
	f.sent = append(f.sent, sentCode{to, code})
	return nil
}
func (f *fakeEmailChannel) last() sentCode {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sent) == 0 {
		return sentCode{}
	}
	return f.sent[len(f.sent)-1]
}
func (f *fakeEmailChannel) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func emailFixture(t *testing.T) (*chatFixture, *fakeEmailChannel) {
	t.Helper()
	f := newChatFixture(t)
	em := &fakeEmailChannel{}
	f.e.api.cfg.Chat = chat.New(chat.Config{
		Store: f.e.st, Secret: []byte(testChatSecret),
		Channels: []chat.Channel{web.Channel{}, em}, Log: f.e.api.log,
	})
	return f, em
}

func TestEmailLinkSendsCodeToOwnAddressOnly(t *testing.T) {
	f, em := emailFixture(t)
	// The request cannot carry a destination: unknown fields are refused and
	// nothing is sent.
	if s := f.member.post("/api/chat/link", map[string]any{"channel": "email", "to": "attacker@evil.test"}, nil); s == http.StatusOK || em.count() != 0 {
		t.Fatalf("request with a to address: %d, sent %d", s, em.count())
	}
	s, _, raw := f.member.do(http.MethodPost, "/api/chat/link", map[string]any{"channel": "email"})
	if s != http.StatusOK {
		t.Fatalf("link: %d %s", s, raw)
	}
	got := em.last()
	if got.to != "member@chat.test" || len(got.code) != store.LinkCodeLen {
		t.Fatalf("sent %+v", got)
	}
	body := string(raw)
	if strings.Contains(body, got.code) || strings.Contains(body, "/link") || strings.Contains(body, "member@chat.test") {
		t.Fatalf("response leaks the code, mentions /link or the full address: %s", body)
	}
	if !strings.Contains(body, "We sent a code to m***@chat.test; enter it here.") {
		t.Fatalf("instructions: %s", body)
	}
}

func TestEmailLinkConfirmLinksVerifiedAccount(t *testing.T) {
	f, em := emailFixture(t)
	if s := f.member.post("/api/chat/link", map[string]any{"channel": "email"}, nil); s != http.StatusOK {
		t.Fatal(s)
	}
	code := em.last().code
	if s, _, raw := f.member.do(http.MethodPost, "/api/chat/link/confirm", map[string]any{"channel": "email", "code": strings.ToLower(code)}); s != http.StatusOK {
		t.Fatalf("confirm: %d %s", s, raw)
	}
	var ext string
	var verified bool
	err := f.e.st.Pool().QueryRow(context.Background(),
		`SELECT external_id, verified_at IS NOT NULL FROM chat_accounts WHERE channel = 'email' AND org_id = $1`, f.org.ID).Scan(&ext, &verified)
	if err != nil || ext != "member@chat.test" || !verified {
		t.Fatalf("account: %q %v %v", ext, verified, err)
	}
	// Replay is rejected (single use).
	if s := f.member.post("/api/chat/link/confirm", map[string]any{"channel": "email", "code": code}, nil); s != http.StatusBadRequest {
		t.Fatalf("replay: %d", s)
	}
}

func TestEmailLinkConfirmRejections(t *testing.T) {
	f, em := emailFixture(t)
	if s := f.member.post("/api/chat/link", map[string]any{"channel": "email"}, nil); s != http.StatusOK {
		t.Fatal(s)
	}
	code := em.last().code

	// Another user in the same org cannot use it; neither can another org.
	for name, c := range map[string]*client{"same-org admin": f.admin, "other org": f.other} {
		if s := c.post("/api/chat/link/confirm", map[string]any{"channel": "email", "code": code}, nil); s != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, s)
		}
	}
	var n int
	_ = f.e.st.Pool().QueryRow(context.Background(), `SELECT count(*) FROM chat_accounts WHERE channel = 'email'`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d email accounts were created by foreign confirms", n)
	}
	// Wrong code, wrong channel, missing code.
	if s := f.member.post("/api/chat/link/confirm", map[string]any{"channel": "email", "code": "ZZZZZZZZ"}, nil); s != http.StatusBadRequest {
		t.Errorf("wrong code: %d", s)
	}
	if s := f.member.post("/api/chat/link/confirm", map[string]any{"channel": "telegram", "code": code}, nil); s != http.StatusBadRequest {
		t.Errorf("wrong channel: %d", s)
	}
	if s := f.member.post("/api/chat/link/confirm", map[string]any{"channel": "email"}, nil); s != http.StatusBadRequest {
		t.Errorf("empty code: %d", s)
	}
	// Expired.
	if _, err := f.e.st.Pool().Exec(context.Background(), `UPDATE chat_link_codes SET expires_at = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if s := f.member.post("/api/chat/link/confirm", map[string]any{"channel": "email", "code": code}, nil); s != http.StatusBadRequest {
		t.Errorf("expired: %d", s)
	}
}

func TestEmailLinkConfirmRateLimited(t *testing.T) {
	f, em := emailFixture(t)
	if s := f.member.post("/api/chat/link", map[string]any{"channel": "email"}, nil); s != http.StatusOK {
		t.Fatal(s)
	}
	good := em.last().code
	var last int
	for i := 0; i < 8; i++ {
		last = f.member.post("/api/chat/link/confirm", map[string]any{"channel": "email", "code": "AAAAAAA" + string(rune('A'+i))}, nil)
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("after many failures: %d, want 429", last)
	}
	// Even the right code is refused while limited.
	if s := f.member.post("/api/chat/link/confirm", map[string]any{"channel": "email", "code": good}, nil); s != http.StatusTooManyRequests {
		t.Fatalf("limited correct code: %d", s)
	}
}

func TestEmailLinkRequestRateLimitAndSendFailure(t *testing.T) {
	f, em := emailFixture(t)
	for i := 0; i < store.LinkCodeRateMax; i++ {
		if s := f.member.post("/api/chat/link", map[string]any{"channel": "email"}, nil); s != http.StatusOK {
			t.Fatalf("request %d: %d", i, s)
		}
	}
	before := em.count()
	if s := f.member.post("/api/chat/link", map[string]any{"channel": "email"}, nil); s != http.StatusTooManyRequests {
		t.Fatalf("over limit: %d", s)
	}
	if em.count() != before {
		t.Fatal("an email was sent although rate limited")
	}
	em.fail = true
	if s := f.admin.post("/api/chat/link", map[string]any{"channel": "email"}, nil); s != http.StatusBadGateway {
		t.Fatalf("send failure: %d", s)
	}
}
