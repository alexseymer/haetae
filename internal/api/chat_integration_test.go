//go:build integration

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/auth"
	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/chat/web"
	"github.com/alexseymer/haetae/internal/store"
)

const (
	testTelegramToken = "TEST-TELEGRAM-TOKEN-never-leak-xyz"
	testChatSecret    = "test-chat-hmac-secret-0123456789abcd"
)

type chatFixture struct {
	e         *env
	org       store.Org
	host      store.Host
	admin     *client
	member    *client
	other     *client
	otherOrg  store.Org
	svc       *chat.Service
	adminUser store.User
}

func newChatFixture(t *testing.T) *chatFixture {
	t.Helper()
	var svc *chat.Service
	e := newEnv(t, func(c *Config) {
		// Store filled after New; chat wired below once we have e.st.
	})
	svc = chat.New(chat.Config{
		Store:  e.st,
		Secret: []byte(testChatSecret),
		Channels: []chat.Channel{
			web.Channel{},
			fakeChatChannel{store.ChannelTelegram},
		},
		Log: e.api.log,
	})
	e.api.cfg.Chat = svc

	org, admin := e.addOrgWithUser("chat-org", "admin@chat.test", "admin")
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	memberUser, err := e.st.CreateUser(context.Background(), org.ID, "member@chat.test", hash, "member")
	if err != nil {
		t.Fatal(err)
	}
	_ = memberUser
	member := e.newClient()
	if s := member.post("/api/login", map[string]string{"email": "member@chat.test", "password": testPassword}, nil); s != http.StatusOK {
		t.Fatalf("member login %d", s)
	}
	otherOrg, other := e.addOrgWithUser("chat-other", "admin@chat-other.test", "admin")
	host := e.addHost(org.ID, "chat-host")

	adminUser, err := e.st.UserByEmail(context.Background(), "admin@chat.test")
	if err != nil {
		t.Fatal(err)
	}
	return &chatFixture{
		e: e, org: org, host: host, admin: admin, member: member,
		other: other, otherOrg: otherOrg, svc: svc, adminUser: adminUser,
	}
}

func TestChatChannelsNoSecrets(t *testing.T) {
	f := newChatFixture(t)
	// Put a canary token only in env-style naming; channel fake has no token field exposed.
	t.Setenv("HAETAE_TELEGRAM_TOKEN", testTelegramToken)

	var out struct {
		Channels []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"channels"`
	}
	status, _, raw := f.admin.do(http.MethodGet, "/api/chat/channels", nil)
	if status != http.StatusOK {
		t.Fatalf("%d %s", status, raw)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	enabled := map[string]bool{}
	for _, c := range out.Channels {
		enabled[c.Name] = c.Enabled
	}
	if !enabled[store.ChannelWeb] || !enabled[store.ChannelTelegram] {
		t.Fatalf("enabled=%v", enabled)
	}
	if enabled[store.ChannelSlack] || enabled[store.ChannelEmail] || enabled[store.ChannelSignal] {
		t.Fatalf("unexpected enabled: %v", enabled)
	}
	assertNoSecrets(t, raw)
}

func TestChatMessagesPaginationAndPost(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	var ids []uuid.UUID
	for i := 0; i < 3; i++ {
		m, err := f.e.st.EnqueueChatMessage(ctx, store.ChatMessage{
			OrgID: f.org.ID, Channel: store.ChannelWeb, Kind: store.KindText,
			Body: "msg-" + string(rune('a'+i)),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
		time.Sleep(2 * time.Millisecond) // distinct created_at ordering
	}

	var page1 struct {
		Messages []map[string]any `json:"messages"`
		Next     any              `json:"next"`
	}
	if s := f.member.get("/api/chat/messages?limit=2", &page1); s != http.StatusOK {
		t.Fatalf("page1: %d", s)
	}
	if len(page1.Messages) != 2 || page1.Next == nil {
		t.Fatalf("page1=%+v", page1)
	}
	nextID := page1.Next.(string)
	var page2 struct {
		Messages []map[string]any `json:"messages"`
		Next     any              `json:"next"`
	}
	if s := f.member.get("/api/chat/messages?after="+nextID+"&limit=2", &page2); s != http.StatusOK {
		t.Fatalf("page2: %d", s)
	}
	if len(page2.Messages) != 1 {
		t.Fatalf("page2 len=%d", len(page2.Messages))
	}
	if page2.Messages[0]["id"] != ids[2].String() {
		t.Fatalf("page2 id=%v want %s", page2.Messages[0]["id"], ids[2])
	}

	// RFC3339Nano cursor: after first message timestamp should skip it.
	first, err := f.e.st.GetChatMessage(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	cursor := first.CreatedAt.UTC().Format(time.RFC3339Nano)
	var afterTime struct {
		Messages []map[string]any `json:"messages"`
	}
	if s := f.admin.get("/api/chat/messages?after="+cursor, &afterTime); s != http.StatusOK {
		t.Fatalf("after time: %d", s)
	}
	if len(afterTime.Messages) < 2 {
		t.Fatalf("after time got %d", len(afterTime.Messages))
	}

	var reply struct {
		Reply string `json:"reply"`
	}
	if s := f.admin.post("/api/chat/messages", map[string]any{"body": "hello operator"}, &reply); s != http.StatusOK {
		t.Fatalf("post: %d", s)
	}
	if reply.Reply != chat.OperatorDisabledReply {
		t.Fatalf("reply=%q", reply.Reply)
	}
	if s, _, _ := f.admin.do(http.MethodPost, "/api/chat/messages", map[string]any{"body": strings.Repeat("x", 4001)}); s != http.StatusBadRequest {
		t.Fatalf("long body: %d", s)
	}
}

func TestChatActionsAdminMemberCrossOrg(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	inc, err := f.e.st.InsertIncidentForTest(ctx, store.Incident{
		OrgID: f.org.ID, HostID: f.host.ID, Title: "oom",
		Severity: store.SeverityHigh, State: store.IncidentOpen,
	})
	if err != nil {
		t.Fatal(err)
	}
	incID := inc.ID
	msg, err := f.e.st.EnqueueChatMessage(ctx, store.ChatMessage{
		OrgID: f.org.ID, Channel: store.ChannelWeb, Kind: store.KindIncident,
		Body: "[HIGH] oom", Actions: []store.ChatAction{{ID: "later", Label: "Later"}},
		IncidentID: &incID,
	})
	if err != nil {
		t.Fatal(err)
	}
	otherMsg, err := f.e.st.EnqueueChatMessage(ctx, store.ChatMessage{
		OrgID: f.otherOrg.ID, Channel: store.ChannelWeb, Kind: store.KindText, Body: "x",
	})
	if err != nil {
		t.Fatal(err)
	}

	if s, _, _ := f.member.do(http.MethodPost, "/api/chat/actions", map[string]any{
		"messageId": msg.ID.String(), "action": "later",
	}); s != http.StatusForbidden {
		t.Fatalf("member action: %d", s)
	}
	var out struct {
		Reply string `json:"reply"`
	}
	if s := f.admin.post("/api/chat/actions", map[string]any{
		"messageId": msg.ID.String(), "action": "later",
	}, &out); s != http.StatusOK {
		t.Fatalf("admin later: %d", s)
	}
	if out.Reply != "Snoozed." {
		t.Fatalf("reply=%q", out.Reply)
	}
	if s, _, _ := f.admin.do(http.MethodPost, "/api/chat/actions", map[string]any{
		"messageId": otherMsg.ID.String(), "action": "ack",
	}); s != http.StatusNotFound {
		t.Fatalf("cross-org action: %d", s)
	}
	if s, _, _ := f.admin.do(http.MethodPost, "/api/chat/actions", map[string]any{
		"messageId": msg.ID.String(), "action": "nope",
	}); s != http.StatusBadRequest {
		t.Fatalf("bad action: %d", s)
	}
}

func TestChatLinkCodeHashAndEnabled(t *testing.T) {
	f := newChatFixture(t)
	t.Setenv("HAETAE_TELEGRAM_TOKEN", testTelegramToken)

	if s, _, raw := f.admin.do(http.MethodPost, "/api/chat/link", map[string]any{"channel": "slack"}); s != http.StatusConflict {
		t.Fatalf("disabled channel: %d %s", s, raw)
	}
	var link struct {
		Code         string `json:"code"`
		ExpiresAt    string `json:"expiresAt"`
		Instructions string `json:"instructions"`
	}
	status, _, raw := f.admin.do(http.MethodPost, "/api/chat/link", map[string]any{"channel": "telegram"})
	if status != http.StatusOK {
		t.Fatalf("link: %d %s", status, raw)
	}
	assertNoSecrets(t, raw)
	if err := json.Unmarshal(raw, &link); err != nil {
		t.Fatal(err)
	}
	if len(link.Code) != store.LinkCodeLen || link.ExpiresAt == "" || !strings.Contains(link.Instructions, link.Code) {
		t.Fatalf("%+v", link)
	}
	// Only the hash is stored.
	sum := sha256.Sum256([]byte(strings.ToUpper(link.Code)))
	var n int
	err := f.e.st.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM chat_link_codes WHERE code_hash = $1`, sum[:]).Scan(&n)
	if err != nil || n != 1 {
		t.Fatalf("hash stored: n=%d err=%v", n, err)
	}
	var plain int
	_ = f.e.st.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM chat_link_codes WHERE convert_from(code_hash, 'UTF8') = $1`, link.Code).Scan(&plain)
	// Plain code must not appear as a UTF-8 row value we can find as the code itself in any text column.
	var bodyScan int
	_ = f.e.st.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM chat_link_codes WHERE code_hash::text LIKE $1`, "%"+link.Code+"%").Scan(&bodyScan)
	if bodyScan != 0 {
		t.Fatal("plaintext code appears in code_hash storage")
	}

	// Member can also request a link code.
	if s := f.member.post("/api/chat/link", map[string]any{"channel": "telegram"}, nil); s != http.StatusOK {
		t.Fatalf("member link: %d", s)
	}
}

func TestChatAccountsMaskAndDelete(t *testing.T) {
	f := newChatFixture(t)
	ctx := context.Background()
	acc, err := f.e.st.CreateChatAccount(ctx, f.org.ID, f.adminUser.ID, store.ChannelTelegram, "telegram-user-999")
	if err != nil {
		t.Fatal(err)
	}
	otherUser, err := f.e.st.UserByEmail(ctx, "admin@chat-other.test")
	if err != nil {
		t.Fatal(err)
	}
	otherAcc, err := f.e.st.CreateChatAccount(ctx, f.otherOrg.ID, otherUser.ID, store.ChannelTelegram, "other-ext-1")
	if err != nil {
		t.Fatal(err)
	}

	var list struct {
		Accounts []map[string]any `json:"accounts"`
	}
	status, _, raw := f.admin.do(http.MethodGet, "/api/chat/accounts", nil)
	if status != http.StatusOK {
		t.Fatalf("%d", status)
	}
	assertNoSecrets(t, raw)
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Accounts) != 1 {
		t.Fatalf("accounts=%d", len(list.Accounts))
	}
	ext := list.Accounts[0]["externalId"].(string)
	if ext == "telegram-user-999" || !strings.HasSuffix(ext, "999") || !strings.Contains(ext, "*") {
		t.Fatalf("mask failed: %q", ext)
	}

	if s, _, _ := f.admin.do(http.MethodDelete, "/api/chat/accounts/"+otherAcc.ID.String(), map[string]any{}); s != http.StatusNotFound {
		t.Fatalf("cross-org delete: %d", s)
	}
	if s, _, _ := f.member.do(http.MethodDelete, "/api/chat/accounts/"+acc.ID.String(), map[string]any{}); s != http.StatusForbidden {
		t.Fatalf("member delete other's: %d", s)
	}
	if s, _, _ := f.admin.do(http.MethodDelete, "/api/chat/accounts/"+acc.ID.String(), map[string]any{}); s != http.StatusNoContent {
		t.Fatalf("admin delete own: %d", s)
	}
}

func TestChatWritesRequireCSRF(t *testing.T) {
	f := newChatFixture(t)
	paths := []struct {
		method string
		path   string
		body   any
	}{
		{http.MethodPost, "/api/chat/messages", map[string]any{"body": "hi"}},
		{http.MethodPost, "/api/chat/actions", map[string]any{"messageId": uuid.New().String(), "action": "later"}},
		{http.MethodPost, "/api/chat/link", map[string]any{"channel": "telegram"}},
	}
	for _, tc := range paths {
		req, _ := http.NewRequest(tc.method, f.e.ts.URL+tc.path, bytes.NewReader(mustJSON(tc.body)))
		req.Header.Set("Content-Type", "application/json")
		for _, c := range f.admin.hc.Jar.Cookies(req.URL) {
			req.AddCookie(c)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s %s without CSRF: %d", tc.method, tc.path, resp.StatusCode)
		}
	}
}

func assertNoSecrets(t *testing.T, raw []byte) {
	t.Helper()
	for _, secret := range []string{testTelegramToken, testChatSecret} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("secret %q leaked in response: %s", secret, raw)
		}
	}
}
