package chat

import (
	"strings"
	"testing"

	"github.com/alexseymer/haetae/internal/store"
	"github.com/google/uuid"
)

func renderFixture() (store.Incident, []store.Alert) {
	inc := store.Incident{
		ID: uuid.New(), HostID: uuid.New(),
		Title: "Strato offline", Severity: store.SeverityCritical, State: "open",
	}
	alerts := []store.Alert{
		{State: "firing", Summary: "Strato offline"},
		{State: "firing", Summary: "disk almost full", Container: "db"},
	}
	return inc, alerts
}

func TestRenderIncidentBodyNoDuplicateTitle(t *testing.T) {
	inc, alerts := renderFixture()
	for _, ch := range []string{store.ChannelWeb, store.ChannelTelegram, store.ChannelSlack, store.ChannelEmail, store.ChannelSignal} {
		body := renderIncidentBody(ch, inc, alerts)
		if n := strings.Count(body, "Strato offline"); n != 1 {
			t.Errorf("%s: title appears %d times: %q", ch, n, body)
		}
		if !strings.Contains(body, "disk almost full") {
			t.Errorf("%s: distinct alert missing: %q", ch, body)
		}
		hasLinks := strings.Contains(body, "Host: /hosts/"+inc.HostID.String()) &&
			strings.Contains(body, "Incident: /incidents/"+inc.ID.String())
		if ch == store.ChannelWeb && hasLinks {
			t.Errorf("web body must not bake links into text: %q", body)
		}
		if ch != store.ChannelWeb && !hasLinks {
			t.Errorf("%s: links missing: %q", ch, body)
		}
	}
}

func TestRenderIncidentBodyResolvedNoRepeat(t *testing.T) {
	inc, alerts := renderFixture()
	inc.State = "resolved"
	alerts[0].State = "resolved"
	body := renderIncidentBody(store.ChannelTelegram, inc, alerts)
	if strings.Count(body, "Strato offline") != 1 || !strings.Contains(body, "Resolved.") {
		t.Fatalf("body: %q", body)
	}
}

func TestIncidentMeta(t *testing.T) {
	inc, alerts := renderFixture()
	m := incidentMeta(inc, alerts, "Strato")
	if m.Severity != "critical" || m.Title != "Strato offline" || m.State != "open" || m.Host != "Strato" {
		t.Fatalf("meta: %+v", m)
	}
	if len(m.Summary) != 1 || m.Summary[0] != "disk almost full (db)" {
		t.Fatalf("summary: %v", m.Summary)
	}
	inc.State = "resolved"
	if incidentMeta(inc, alerts, "").State != "resolved" {
		t.Fatal("resolved state")
	}
}
