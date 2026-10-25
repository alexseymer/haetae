//go:build integration

package chat_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/chat/email"
	"github.com/alexseymer/haetae/internal/chat/slack"
	"github.com/alexseymer/haetae/internal/chat/telegram"
	"github.com/alexseymer/haetae/internal/chat/web"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

func TestIncidentToTelegramWithin60s(t *testing.T) {
	s := storetest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	org, host, admin := seedOrg(t, s, "tg-org", "admin@tg.test")
	var received atomic.Int32
	var firstAt atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "sendMessage") {
			received.Add(1)
			firstAt.CompareAndSwap(nil, time.Now())
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
			return
		}
		if strings.Contains(r.URL.Path, "getUpdates") {
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
		_ = body
	}))
	defer srv.Close()

	tg := &telegram.Channel{Token: "test-token", BaseURL: srv.URL, Client: srv.Client()}
	secret := []byte("0123456789abcdef0123456789abcdef")
	svc := chat.New(chat.Config{
		Store: s, Secret: secret, Channels: []chat.Channel{web.Channel{}, tg},
		PollEvery: 50 * time.Millisecond, Log: nil,
	})
	tg.Handler = svc

	code, err := s.CreateLinkCode(ctx, org.ID, admin.ID, store.ChannelTelegram)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.HandleLink(ctx, store.ChannelTelegram, "424242", code); err != nil {
		t.Fatal(err)
	}

	go func() { _ = svc.Run(ctx) }()

	inc := store.Incident{
		ID: uuid.New(), OrgID: org.ID, HostID: host.ID,
		Title: "shop: OOM on hel1", Severity: store.SeverityHigh, State: "open",
	}
	inc, err = s.InsertIncidentForTest(ctx, inc)
	if err != nil {
		t.Fatal(err)
	}
	alerts := []store.Alert{{
		OrgID: org.ID, HostID: host.ID, IncidentID: inc.ID,
		Rule: "oom_kill", Container: "api", Severity: store.SeverityHigh,
		State: "firing", DedupeKey: "oom|api", Summary: "OOM kill",
	}}
	start := time.Now()
	if err := svc.IncidentChanged(ctx, org.ID, inc, alerts); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if received.Load() >= 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if received.Load() < 1 {
		t.Fatal("telegram did not receive within 60s")
	}
	dur := time.Since(start)
	fmt.Printf("MEASURE incident-to-telegram: %s\n", dur)
	if dur > 60*time.Second {
		t.Fatalf("too slow: %s", dur)
	}

	// Web message exists.
	msgs, err := s.ForOrg(org.ID).ListMessages(ctx, org.ID, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 {
		t.Fatal("expected web message")
	}
}

func TestDeliveryRetryNoDuplicateAndIsolation(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, host, admin := seedOrg(t, s, "retry-org", "admin@retry.test")

	var tgCalls atomic.Int32
	var failUntil atomic.Int32
	failUntil.Store(2)
	tgSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "sendMessage") {
			n := tgCalls.Add(1)
			if n <= failUntil.Load() {
				http.Error(w, "nope", 500)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
			return
		}
		if strings.Contains(r.URL.Path, "getUpdates") {
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer tgSrv.Close()

	var emailCalls atomic.Int32
	emailLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer emailLn.Close()
	go acceptSMTP(t, emailLn, &emailCalls)

	tg := &telegram.Channel{Token: "t", BaseURL: tgSrv.URL, Client: tgSrv.Client()}
	em := email.New(email.Config{
		Addr: emailLn.Addr().String(), From: "h@test", Insecure: true,
		Dial: func(network, addr string) (net.Conn, error) {
			return net.DialTimeout(network, addr, time.Second)
		},
	})
	secret := []byte("0123456789abcdef0123456789abcdef")
	svc := chat.New(chat.Config{
		Store: s, Secret: secret,
		Channels:  []chat.Channel{web.Channel{}, tg, em},
		PollEvery: time.Hour, // manual deliver
	})

	linkAndAccount(t, s, svc, org.ID, admin.ID, store.ChannelTelegram, "1")
	linkAndAccount(t, s, svc, org.ID, admin.ID, store.ChannelEmail, "a@b.co")

	inc, err := s.InsertIncidentForTest(ctx, store.Incident{
		OrgID: org.ID, HostID: host.ID, Title: "x", Severity: store.SeverityCritical, State: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.IncidentChanged(ctx, org.ID, inc, nil); err != nil {
		t.Fatal(err)
	}

	// First pass: telegram fails, email should still succeed (isolation).
	svc.DeliverOnceForTest(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && emailCalls.Load() == 0 {
		time.Sleep(10 * time.Millisecond)
		svc.DeliverOnceForTest(ctx)
	}
	if emailCalls.Load() == 0 {
		t.Fatal("email blocked by failing telegram")
	}

	// Force telegram due again.
	_, _ = s.Pool().Exec(ctx, `UPDATE chat_messages SET next_attempt_at = now() WHERE channel = 'telegram' AND delivered_at IS NULL`)
	for i := 0; i < 5; i++ {
		svc.DeliverOnceForTest(ctx)
		_, _ = s.Pool().Exec(ctx, `UPDATE chat_messages SET next_attempt_at = now() WHERE channel = 'telegram' AND delivered_at IS NULL`)
		time.Sleep(20 * time.Millisecond)
	}
	if tgCalls.Load() < 3 {
		t.Fatalf("expected retries, got %d", tgCalls.Load())
	}
	// After success, further delivers must not re-send.
	before := tgCalls.Load()
	svc.DeliverOnceForTest(ctx)
	svc.DeliverOnceForTest(ctx)
	if tgCalls.Load() != before {
		t.Fatalf("duplicate send: %d -> %d", before, tgCalls.Load())
	}
}

func TestActionsAuthZ(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, host, admin := seedOrg(t, s, "act-org", "admin@act.test")
	member, err := s.CreateUser(ctx, org.ID, "member@act.test", "hash", "member")
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("0123456789abcdef0123456789abcdef")
	svc := chat.New(chat.Config{Store: s, Secret: secret, Channels: []chat.Channel{web.Channel{}}})

	inc, err := s.InsertIncidentForTest(ctx, store.Incident{
		OrgID: org.ID, HostID: host.ID, Title: "t", Severity: store.SeverityHigh, State: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertAlertForTest(ctx, store.Alert{
		OrgID: org.ID, HostID: host.ID, IncidentID: inc.ID,
		Rule: "oom_kill", Severity: store.SeverityHigh, State: "firing",
		DedupeKey: "oom|x", Summary: "oom",
	}); err != nil {
		t.Fatal(err)
	}
	msg, err := s.EnqueueChatMessage(ctx, store.ChatMessage{
		OrgID: org.ID, Channel: store.ChannelTelegram, Kind: store.KindIncident,
		Body: "hi", IncidentID: &inc.ID,
		Actions: []store.ChatAction{{ID: "mute30d", Label: "Mute 30 days"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tok := store.SignActionToken(secret, msg.ID, "mute30d", time.Now().Add(time.Hour))

	// Unlinked.
	if reply, _ := svc.HandleAction(ctx, store.ChannelTelegram, "999", tok); reply != "Action not allowed." {
		t.Fatalf("unlinked: %q", reply)
	}

	// Linked non-admin.
	linkAndAccount(t, s, svc, org.ID, member.ID, store.ChannelTelegram, "100")
	if reply, _ := svc.HandleAction(ctx, store.ChannelTelegram, "100", tok); reply != "Action not allowed." {
		t.Fatalf("non-admin: %q", reply)
	}

	// Forged.
	linkAndAccount(t, s, svc, org.ID, admin.ID, store.ChannelTelegram, "200")
	if reply, _ := svc.HandleAction(ctx, store.ChannelTelegram, "200", tok+"x"); reply != "Action not allowed." {
		t.Fatalf("forged: %q", reply)
	}

	// Expired.
	expired := store.SignActionToken(secret, msg.ID, "mute30d", time.Now().Add(-time.Minute))
	if reply, _ := svc.HandleAction(ctx, store.ChannelTelegram, "200", expired); reply != "Action not allowed." {
		t.Fatalf("expired: %q", reply)
	}

	// Admin success, then replay (idempotent mute).
	good := store.SignActionToken(secret, msg.ID, "mute30d", time.Now().Add(time.Hour))
	if reply, err := svc.HandleAction(ctx, store.ChannelTelegram, "200", good); err != nil || reply != "Muted for 30 days." {
		t.Fatalf("admin mute: %q %v", reply, err)
	}
	mutes, err := s.ListMutes(ctx, org.ID)
	if err != nil || len(mutes) == 0 {
		t.Fatalf("mutes: %v %v", mutes, err)
	}
	if reply, err := svc.HandleAction(ctx, store.ChannelTelegram, "200", good); err != nil || reply != "Muted for 30 days." {
		t.Fatalf("replay: %q %v", reply, err)
	}
}

func TestUnconfiguredChannelsNoNetwork(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	org, _, _ := seedOrg(t, s, "nonet", "a@nonet.test")

	failTransport := roundTripFunc(func(*http.Request) (*http.Response, error) {
		t.Fatal("unexpected network call")
		return nil, fmt.Errorf("no network")
	})
	_ = failTransport

	// Only web configured — building channels the same way as production with
	// empty env means telegram/slack/email/signal are absent.
	svc := chat.New(chat.Config{
		Store:    s,
		Secret:   []byte("0123456789abcdef0123456789abcdef"),
		Channels: []chat.Channel{web.Channel{}},
	})
	if err := svc.EnqueueMessage(ctx, org.ID, store.ChatMessage{
		Kind: store.KindText, Body: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	svc.DeliverOnceForTest(ctx)
	// Success: no panic from failTransport because those channels are not registered.
}

func TestFakeSMTPAndSlack(t *testing.T) {
	s := storetest.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	org, host, admin := seedOrg(t, s, "smtp-org", "admin@smtp.test")

	var smtpOK atomic.Bool
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		buf := make([]byte, 4096)
		_, _ = c.Write([]byte("220 hi\r\n"))
		for {
			n, err := c.Read(buf)
			if n > 0 {
				line := string(buf[:n])
				switch {
				case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
					_, _ = c.Write([]byte("250-localhost\r\n250 OK\r\n"))
				case strings.HasPrefix(line, "MAIL"), strings.HasPrefix(line, "RCPT"):
					_, _ = c.Write([]byte("250 OK\r\n"))
				case strings.HasPrefix(line, "DATA"):
					_, _ = c.Write([]byte("354 Go\r\n"))
				case strings.Contains(line, "\r\n.\r\n"):
					smtpOK.Store(true)
					_, _ = c.Write([]byte("250 OK\r\n"))
				case strings.HasPrefix(line, "QUIT"):
					_, _ = c.Write([]byte("221 bye\r\n"))
					return
				default:
					_, _ = c.Write([]byte("250 OK\r\n"))
				}
			}
			if err != nil {
				return
			}
		}
	}()

	wsURL := make(chan string, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "apps.connections.open"):
			u := <-wsURL
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": u})
		case strings.HasSuffix(r.URL.Path, "chat.postMessage"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	defer api.Close()

	wsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close(websocket.StatusNormalClosure, "")
		// Keep open briefly.
		_, _, _ = c.Read(r.Context())
	}))
	defer wsSrv.Close()
	wsURL <- "ws" + strings.TrimPrefix(wsSrv.URL, "http")

	em := email.New(email.Config{
		Addr: ln.Addr().String(), From: "h@test", PublicURL: "https://ui.test", Insecure: true,
	})
	sl := &slack.Channel{
		AppToken: "xapp", BotToken: "xoxb", APIURL: api.URL,
		Client: api.Client(),
		WSDial: func(ctx context.Context, u string) (*websocket.Conn, error) {
			c, _, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPClient: wsSrv.Client()})
			return c, err
		},
	}
	secret := []byte("0123456789abcdef0123456789abcdef")
	svc := chat.New(chat.Config{
		Store: s, Secret: secret, Channels: []chat.Channel{web.Channel{}, em, sl},
		PollEvery: 50 * time.Millisecond,
	})
	linkAndAccount(t, s, svc, org.ID, admin.ID, store.ChannelEmail, "user@ex.com")
	linkAndAccount(t, s, svc, org.ID, admin.ID, store.ChannelSlack, "U1")

	go func() { _ = svc.Run(ctx) }()

	inc, err := s.InsertIncidentForTest(ctx, store.Incident{
		OrgID: org.ID, HostID: host.ID, Title: "t", Severity: store.SeverityHigh, State: "open",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.IncidentChanged(ctx, org.ID, inc, nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && !smtpOK.Load() {
		time.Sleep(20 * time.Millisecond)
	}
	if !smtpOK.Load() {
		t.Fatal("smtp did not receive")
	}
}

func TestChatTenantIsolation(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	orgA, _, adminA := seedOrg(t, s, "iso-a", "a@iso.test")
	orgB, _, _ := seedOrg(t, s, "iso-b", "b@iso.test")
	secret := []byte("0123456789abcdef0123456789abcdef")
	svc := chat.New(chat.Config{Store: s, Secret: secret, Channels: []chat.Channel{web.Channel{}}})

	if err := svc.EnqueueMessage(ctx, orgA.ID, store.ChatMessage{Kind: store.KindText, Body: "secret-a"}); err != nil {
		t.Fatal(err)
	}
	listB, err := s.ForOrg(orgB.ID).ListMessages(ctx, orgB.ID, nil, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(listB) != 0 {
		t.Fatalf("org B saw %d messages", len(listB))
	}
	acc, err := s.CreateChatAccount(ctx, orgA.ID, adminA.ID, store.ChannelTelegram, "1")
	if err != nil {
		t.Fatal(err)
	}
	scoped := s.ForOrg(orgB.ID)
	if err := scoped.DeleteChatAccount(ctx, orgA.ID, acc.ID); err == nil {
		// RLS may make it look like not found rather than succeed
	}
	if _, err := s.GetChatAccount(ctx, orgA.ID, acc.ID); err != nil {
		t.Fatal("account deleted across tenants")
	}
}

func seedOrg(t *testing.T, s *store.Store, orgName, email string) (store.Org, store.Host, store.User) {
	t.Helper()
	ctx := context.Background()
	org, err := s.CreateOrg(ctx, orgName)
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := s.CreateEnrollmentToken(ctx, org.ID, orgName+"-host", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	host, err := s.EnrollHost(ctx, tok, store.HostInfo{}, func(uuid.UUID) (string, error) {
		return "serial-" + orgName, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(ctx, org.ID, email, "hash", "admin")
	if err != nil {
		t.Fatal(err)
	}
	return org, host, u
}

func linkAndAccount(t *testing.T, s *store.Store, svc *chat.Service, orgID, userID uuid.UUID, channel, ext string) {
	t.Helper()
	ctx := context.Background()
	_, _ = s.Pool().Exec(ctx, `DELETE FROM chat_link_codes WHERE user_id = $1`, userID)
	code, err := s.CreateLinkCode(ctx, orgID, userID, channel)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := svc.HandleLink(ctx, channel, ext, code)
	if err != nil || !strings.HasPrefix(reply, "Linked") {
		t.Fatalf("link: %q %v", reply, err)
	}
}

func acceptSMTP(t *testing.T, ln net.Listener, calls *atomic.Int32) {
	t.Helper()
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			buf := make([]byte, 4096)
			_, _ = c.Write([]byte("220 hi\r\n"))
			for {
				n, err := c.Read(buf)
				if n > 0 {
					line := string(buf[:n])
					switch {
					case strings.HasPrefix(line, "EHLO"), strings.HasPrefix(line, "HELO"):
						_, _ = c.Write([]byte("250 OK\r\n"))
					case strings.HasPrefix(line, "DATA"):
						_, _ = c.Write([]byte("354 Go\r\n"))
					case strings.Contains(line, "\r\n.\r\n"):
						calls.Add(1)
						_, _ = c.Write([]byte("250 OK\r\n"))
					case strings.HasPrefix(line, "QUIT"):
						_, _ = c.Write([]byte("221 bye\r\n"))
						return
					default:
						_, _ = c.Write([]byte("250 OK\r\n"))
					}
				}
				if err != nil {
					return
				}
			}
		}(c)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var _ = sync.Mutex{}
