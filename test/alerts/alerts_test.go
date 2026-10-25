//go:build e2e_alerts

package alerts_test

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestAlertsE2E(t *testing.T) {
	h := startHarness(t)

	t.Run("no_ai_outbound", func(t *testing.T) { testNoAIOutbound(t, h) })
	t.Run("quiet_day_briefing", func(t *testing.T) { testQuietDayBriefing(t, h) })
	t.Run("crash_loop", func(t *testing.T) { testCrashLoop(t, h) })
	t.Run("host_offline", func(t *testing.T) { testHostOffline(t, h) })
	t.Run("buttons_mute", func(t *testing.T) { testButtonsMute(t, h) })
	t.Run("tenant_isolation", func(t *testing.T) { testTenantIsolation(t, h) })
}

func testNoAIOutbound(t *testing.T, h *harness) {
	h.assertNoAIEnv()
	h.assertLocalPeersOnly()
}

func testQuietDayBriefing(t *testing.T, h *harness) {
	// Force a briefing tick immediately (test-only env). Exactly-once via briefings PK.
	h.restartServer([]string{"HAETAE_BRIEFING_NOW=1"})
	since := time.Now().Add(-5 * time.Second)

	isBriefing := func(m map[string]any) bool {
		return m["kind"] == "briefing" && m["channel"] == "web" && m["direction"] == "out"
	}
	msg, _ := h.waitWebMessage(since, isBriefing, 45*time.Second)
	body, _ := msg["body"].(string)
	lines := nonEmptyLines(body)
	if len(lines) > 3 {
		t.Fatalf("briefing lines=%d want <=3: %q", len(lines), body)
	}
	if len(body) > 300 {
		t.Fatalf("briefing chars=%d want <=300", len(body))
	}
	t.Logf("MEASURE quiet-day-briefing lines=%d chars=%d body=%q", len(lines), len(body), body)

	tgPred := func(m tgSent) bool {
		return strings.Contains(m.Text, "briefing")
	}
	_, _ = h.waitTelegram(adminTgID, since, tgPred, 30*time.Second)

	webN := h.countWebMessages(isBriefing)
	tgN := h.tg.CountSince(adminTgID, time.Time{}, tgPred)
	if webN != 1 {
		t.Fatalf("web briefing count=%d want 1", webN)
	}
	if tgN != 1 {
		t.Fatalf("telegram briefing count=%d want 1", tgN)
	}

	// Trigger again (another forced Check via restart) — still exactly one.
	h.restartServer([]string{"HAETAE_BRIEFING_NOW=1"})
	time.Sleep(3 * time.Second)
	webN = h.countWebMessages(isBriefing)
	tgN = h.tg.CountSince(adminTgID, time.Time{}, tgPred)
	if webN != 1 || tgN != 1 {
		t.Fatalf("after second trigger+restart: web=%d tg=%d want 1/1", webN, tgN)
	}
}

func testCrashLoop(t *testing.T, h *harness) {
	// Ensure agent is up (quiet_day may have restarted the server).
	if h.agentCmd == nil || h.agentCmd.Process == nil {
		h.startAgent()
		waitFor(t, 45*time.Second, "host connected after agent start", func() bool {
			var hosts struct {
				Hosts []struct {
					Connected bool `json:"connected"`
				} `json:"hosts"`
			}
			status, _ := h.api(h.admin, "GET", "/api/hosts", nil, &hosts)
			return status == 200 && len(hosts.Hosts) == 1 && hosts.Hosts[0].Connected
		})
	}

	since := time.Now()
	name := h.runCrashLoopContainer()
	waitFor(t, 120*time.Second, "docker restart_count>=3", func() bool {
		out, err := exec.Command("docker", "inspect", "-f", "{{.RestartCount}}", name).Output()
		if err != nil {
			return false
		}
		var n int
		_, _ = fmt.Sscanf(strings.TrimSpace(string(out)), "%d", &n)
		return n >= 3
	})

	alert, _ := h.waitIncidentRule("crash_loop", "firing", 90*time.Second)
	if alert == nil {
		t.Fatal("no crash_loop alert")
	}
	t.Logf("MEASURE crash_loop alert fired")

	webPred := func(m map[string]any) bool {
		return m["kind"] == "incident" && m["channel"] == "web" &&
			strings.Contains(strings.ToLower(fmt.Sprint(m["body"])), "restart")
	}
	_, webElapsed := h.waitWebMessage(since, webPred, 60*time.Second)
	tgPred := func(m tgSent) bool {
		return len(m.Keyboard) > 0 && strings.Contains(strings.ToLower(m.Text), "restart")
	}
	tgMsg, tgElapsed := h.waitTelegram(adminTgID, since, tgPred, 60*time.Second)
	t.Logf("MEASURE crash_loop web=%s telegram=%s", webElapsed.Round(time.Millisecond), tgElapsed.Round(time.Millisecond))
	if webElapsed > 60*time.Second || tgElapsed > 60*time.Second {
		t.Fatalf("delivery too slow web=%s tg=%s", webElapsed, tgElapsed)
	}
	if tgMsg.Text == "" {
		t.Fatal("empty telegram crash_loop message")
	}

	// Recovery: stop/remove container → resolve after 2 evaluations.
	if out, err := exec.Command("docker", "rm", "-f", name).CombinedOutput(); err != nil {
		t.Fatalf("rm crash container: %v\n%s", err, out)
	}
	resolveSince := time.Now()
	waitFor(t, 90*time.Second, "crash_loop resolved", func() bool {
		var out struct {
			Alerts []map[string]any `json:"alerts"`
		}
		status, _ := h.api(h.admin, "GET", "/api/alerts?state=resolved", nil, &out)
		if status != 200 {
			return false
		}
		for _, a := range out.Alerts {
			if a["rule"] == "crash_loop" && a["hostId"] == h.hostID {
				return true
			}
		}
		var incs struct {
			Incidents []map[string]any `json:"incidents"`
		}
		status, _ = h.api(h.admin, "GET", "/api/incidents?state=resolved", nil, &incs)
		if status != 200 {
			return false
		}
		for _, inc := range incs.Incidents {
			if inc["hostId"] == h.hostID {
				return true
			}
		}
		return false
	})

	resolvePred := func(m map[string]any) bool {
		body := strings.ToLower(fmt.Sprint(m["body"]))
		return m["kind"] == "incident" && m["channel"] == "web" && strings.Contains(body, "resolved")
	}
	_, _ = h.waitWebMessage(resolveSince, resolvePred, 60*time.Second)
	tgResolve := func(m tgSent) bool {
		return strings.Contains(strings.ToLower(m.Text), "resolved")
	}
	_, _ = h.waitTelegram(adminTgID, resolveSince, tgResolve, 60*time.Second)

	// Resolve message sent once (web + one telegram).
	webResolve := h.countWebMessages(func(m map[string]any) bool {
		body := strings.ToLower(fmt.Sprint(m["body"]))
		return m["kind"] == "incident" && strings.Contains(body, "resolved")
	})
	if webResolve < 1 {
		t.Fatal("expected at least one resolve web message")
	}
	tgResolveN := h.tg.CountSince(adminTgID, resolveSince, tgResolve)
	if tgResolveN != 1 {
		t.Fatalf("telegram resolve messages=%d want 1", tgResolveN)
	}
	t.Logf("MEASURE crash_loop resolved web=%d tg=%d", webResolve, tgResolveN)
}

func testHostOffline(t *testing.T, h *harness) {
	// Ensure connected before stopping.
	waitFor(t, 30*time.Second, "host connected before offline", func() bool {
		var hosts struct {
			Hosts []struct {
				Connected bool `json:"connected"`
			} `json:"hosts"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts", nil, &hosts)
		return status == 200 && len(hosts.Hosts) == 1 && hosts.Hosts[0].Connected
	})

	lastSeen := h.hostLastSeen()
	h.stopAgent()

	alert, _ := h.waitIncidentRule("host_offline", "firing", 90*time.Second)
	if alert == nil {
		t.Fatal("no host_offline alert")
	}

	webPred := func(m map[string]any) bool {
		body := strings.ToLower(fmt.Sprint(m["body"]))
		return m["kind"] == "incident" && m["channel"] == "web" &&
			(strings.Contains(body, "offline") || strings.Contains(body, "critical"))
	}
	_, _ = h.waitWebMessage(lastSeen, webPred, 60*time.Second)
	webElapsed := time.Since(lastSeen)

	tgPred := func(m tgSent) bool {
		return len(m.Keyboard) > 0 && strings.Contains(strings.ToLower(m.Text), "offline")
	}
	tgMsg, _ := h.waitTelegram(adminTgID, lastSeen, tgPred, 60*time.Second)
	tgElapsed := time.Since(lastSeen)

	t.Logf("MEASURE host_offline web=%s telegram=%s (from last_seen, limit 60s)",
		webElapsed.Round(time.Millisecond), tgElapsed.Round(time.Millisecond))
	if webElapsed > 60*time.Second {
		t.Fatalf("web delivery %s > 60s from last_seen", webElapsed)
	}
	if tgElapsed > 60*time.Second {
		t.Fatalf("telegram delivery %s > 60s from last_seen", tgElapsed)
	}
	if len(tgMsg.Keyboard) == 0 {
		t.Fatal("telegram message missing inline keyboard")
	}

	var incs struct {
		Incidents []map[string]any `json:"incidents"`
	}
	status, _ := h.api(h.admin, "GET", "/api/incidents?state=open", nil, &incs)
	if status != 200 || len(incs.Incidents) == 0 {
		t.Fatalf("open incidents: %d len=%d", status, len(incs.Incidents))
	}
}

func testButtonsMute(t *testing.T, h *harness) {
	// Prefer the Mute token from the latest host_offline telegram (not an earlier crash_loop).
	token := ""
	for _, m := range h.tg.Messages() {
		if m.ChatID != adminTgID {
			continue
		}
		if !strings.Contains(strings.ToLower(m.Text), "offline") {
			continue
		}
		for _, b := range m.Keyboard {
			if strings.Contains(strings.ToLower(b.Text), "mute") {
				token = b.Data
			}
		}
	}
	if token == "" {
		token = h.tg.FindMuteToken(adminTgID, time.Time{})
	}
	if token == "" {
		t.Fatal("no Mute button token on telegram messages")
	}

	// Unlinked user → rejected, no mute change.
	mutesBefore := len(h.listMutes())
	h.tg.PushCallback(unlinkedTgID, token)
	time.Sleep(2 * time.Second)
	if len(h.listMutes()) != mutesBefore {
		t.Fatal("unlinked callback changed mutes")
	}

	// Tampered token → rejected.
	h.tg.PushCallback(adminTgID, token+"x")
	time.Sleep(2 * time.Second)
	if len(h.listMutes()) != mutesBefore {
		t.Fatal("tampered token changed mutes")
	}

	// Linked member (non-admin) → rejected.
	h.linkTelegram(h.member, memberTgID)
	h.tg.PushCallback(memberTgID, token)
	time.Sleep(2 * time.Second)
	if len(h.listMutes()) != mutesBefore {
		t.Fatal("member callback changed mutes")
	}

	// Admin Mute 30 days → mute exists.
	h.tg.PushCallback(adminTgID, token)
	waitFor(t, 20*time.Second, "mute created", func() bool {
		return len(h.listMutes()) > mutesBefore
	})
	t.Logf("MEASURE mute created count=%d", len(h.listMutes()))

	// Bring agent back, clear offline, then go offline again: alert stored, no new message.
	h.startAgent()
	waitFor(t, 45*time.Second, "host reconnected after mute", func() bool {
		var hosts struct {
			Hosts []struct {
				Connected bool `json:"connected"`
			} `json:"hosts"`
		}
		status, _ := h.api(h.admin, "GET", "/api/hosts", nil, &hosts)
		return status == 200 && len(hosts.Hosts) == 1 && hosts.Hosts[0].Connected
	})
	// Wait until host_offline resolves (2 evals).
	waitFor(t, 90*time.Second, "host_offline clear", func() bool {
		var out struct {
			Alerts []map[string]any `json:"alerts"`
		}
		status, _ := h.api(h.admin, "GET", "/api/alerts?state=firing", nil, &out)
		if status != 200 {
			return false
		}
		for _, a := range out.Alerts {
			if a["rule"] == "host_offline" {
				return false
			}
		}
		return true
	})

	msgCountBefore := len(h.tg.Messages())
	webBefore := h.countWebMessages(func(m map[string]any) bool {
		return m["kind"] == "incident" && m["channel"] == "web"
	})
	// Mark time so we only count NEW incident deliveries after re-offline.
	afterMute := time.Now()

	h.stopAgent()
	// Wait for host_offline to fire again (stored).
	_, _ = h.waitIncidentRule("host_offline", "firing", 90*time.Second)
	time.Sleep(8 * time.Second) // delivery window if any

	newTg := h.tg.CountSince(adminTgID, afterMute, func(m tgSent) bool {
		return strings.Contains(strings.ToLower(m.Text), "offline") ||
			strings.Contains(strings.ToLower(m.Text), "critical")
	})
	if newTg != 0 {
		t.Fatalf("muted host produced telegram offline messages: %d (total %d→%d)",
			newTg, msgCountBefore, len(h.tg.Messages()))
	}
	webAfter := h.countWebMessages(func(m map[string]any) bool {
		if m["kind"] != "incident" || m["channel"] != "web" {
			return false
		}
		created, _ := m["createdAt"].(string)
		ct, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			ct, _ = time.Parse(time.RFC3339, created)
		}
		return !ct.Before(afterMute)
	})
	if webAfter != 0 {
		t.Fatalf("muted host produced web incident messages after re-offline: %d (before total %d)", webAfter, webBefore)
	}
}

func testTenantIsolation(t *testing.T, h *harness) {
	var incs struct {
		Incidents []map[string]any `json:"incidents"`
	}
	status, _ := h.api(h.admin, "GET", "/api/incidents", nil, &incs)
	if status != 200 || len(incs.Incidents) == 0 {
		t.Fatalf("org1 incidents: %d len=%d", status, len(incs.Incidents))
	}
	org1IncIDs := map[string]bool{}
	for _, inc := range incs.Incidents {
		id, _ := inc["id"].(string)
		org1IncIDs[id] = true
	}
	incID, _ := incs.Incidents[0]["id"].(string)

	if s, body := h.api(h.org2, "GET", "/api/incidents/"+incID, nil); s != http.StatusNotFound {
		t.Fatalf("org2 get org1 incident: %d %s", s, body)
	}
	var otherIncs struct {
		Incidents []map[string]any `json:"incidents"`
	}
	if s, _ := h.api(h.org2, "GET", "/api/incidents", nil, &otherIncs); s != 200 {
		t.Fatalf("org2 list incidents: %d", s)
	}
	for _, inc := range otherIncs.Incidents {
		id, _ := inc["id"].(string)
		if org1IncIDs[id] {
			t.Fatalf("org2 list includes org1 incident %s", id)
		}
	}
	if len(otherIncs.Incidents) != 0 {
		t.Fatalf("org2 unexpected incidents: %d", len(otherIncs.Incidents))
	}

	var org1Msgs struct {
		Messages []map[string]any `json:"messages"`
	}
	if s, _ := h.api(h.admin, "GET", "/api/chat/messages?limit=200", nil, &org1Msgs); s != 200 {
		t.Fatalf("org1 messages: %d", s)
	}
	org1MsgIDs := map[string]bool{}
	for _, m := range org1Msgs.Messages {
		id, _ := m["id"].(string)
		org1MsgIDs[id] = true
	}
	if len(org1MsgIDs) == 0 {
		t.Fatal("org1 has no messages to isolate against")
	}

	var msgs struct {
		Messages []map[string]any `json:"messages"`
	}
	if s, _ := h.api(h.org2, "GET", "/api/chat/messages", nil, &msgs); s != 200 {
		t.Fatalf("org2 messages: %d", s)
	}
	for _, m := range msgs.Messages {
		id, _ := m["id"].(string)
		if org1MsgIDs[id] {
			t.Fatalf("org2 sees org1 message %s", id)
		}
	}
	// Org2 may have its own briefing from HAETAE_BRIEFING_NOW; that is fine.
	// Fetching a specific org1 message id must 404/empty via list already checked.
	if len(org1Msgs.Messages) > 0 {
		oid, _ := org1Msgs.Messages[0]["id"].(string)
		// No per-message GET; isolation is list emptiness of foreign ids above.
		_ = oid
	}

	var org1Accs struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if s, _ := h.api(h.admin, "GET", "/api/chat/accounts", nil, &org1Accs); s != 200 || len(org1Accs.Accounts) == 0 {
		t.Fatalf("org1 accounts: %d len=%d", s, len(org1Accs.Accounts))
	}
	org1AccIDs := map[string]bool{}
	for _, a := range org1Accs.Accounts {
		id, _ := a["id"].(string)
		org1AccIDs[id] = true
	}

	var accs struct {
		Accounts []map[string]any `json:"accounts"`
	}
	if s, _ := h.api(h.org2, "GET", "/api/chat/accounts", nil, &accs); s != 200 {
		t.Fatalf("org2 accounts: %d", s)
	}
	for _, a := range accs.Accounts {
		id, _ := a["id"].(string)
		if org1AccIDs[id] {
			t.Fatalf("org2 sees org1 account %s", id)
		}
	}
	if len(accs.Accounts) != 0 {
		t.Fatalf("org2 unexpected accounts: %d", len(accs.Accounts))
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}
