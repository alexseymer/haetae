//go:build integration

package chat_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/alexseymer/haetae/internal/chat"
	"github.com/alexseymer/haetae/internal/chat/web"
	"github.com/alexseymer/haetae/internal/operator"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/alexseymer/haetae/internal/store/storetest"
)

// Email is outbound only, so an approval proposal sent there must not carry
// Approve/Decline buttons, even for an administrator.
func TestEmailAccountsNeverGetApprovalButtons(t *testing.T) {
	s := storetest.New(t)
	org, host, admin := seedOrg(t, s, "em-org", "admin@em.test")
	svc := chat.New(chat.Config{
		Store: s, Secret: testSecret, PublicURL: "https://haetae.test",
		Channels: []chat.Channel{web.Channel{}, stubChannel{store.ChannelTelegram}, stubChannel{store.ChannelEmail}},
	})
	linkAndAccount(t, s, svc, org.ID, admin.ID, store.ChannelTelegram, "111")
	linkAndAccount(t, s, svc, org.ID, admin.ID, store.ChannelEmail, "admin@em.test")

	var approvalID uuid.UUID
	if err := s.Pool().QueryRow(context.Background(), `
		INSERT INTO ai_actions (org_id, host_id, container_id, kind, level, origin, outcome, reason)
		VALUES ($1,$2,'c1','restart_container','suggest','proposed','awaiting_approval','crash loop')
		RETURNING id`, org.ID, host.ID).Scan(&approvalID); err != nil {
		t.Fatal(err)
	}
	ans := sampleAnswer(host.ID)
	ans.Actions = []operator.ActionDecision{{
		Action:     operator.ProposedAction{Kind: "restart_container", ContainerID: "api", Reason: "it is crash-looping"},
		Outcome:    "awaiting_approval",
		ApprovalID: approvalID,
	}}
	svc.SetOperator(&fakeOp{avail: true, answer: ans})
	e := &opEnv{s: s, svc: svc, org: org, host: host, user: admin}
	if _, err := svc.HandleInboundText(context.Background(), store.ChannelTelegram, "111", "fix api"); err != nil {
		t.Fatal(err)
	}
	rows := e.waitFor(t, "proposals", func(r []outRow) bool {
		var tg, em bool
		for _, x := range r {
			if strings.Contains(x.Body, "Haetae proposes") {
				tg = tg || x.Channel == store.ChannelTelegram
				em = em || x.Channel == store.ChannelEmail
			}
		}
		return tg && em
	})
	for _, r := range rows {
		if !strings.Contains(r.Body, "Haetae proposes") {
			continue
		}
		hasButtons := strings.Contains(r.Actions, store.ActionApprove)
		switch r.Channel {
		case store.ChannelEmail:
			if hasButtons {
				t.Errorf("email proposal has approval buttons: %s", r.Actions)
			}
		case store.ChannelTelegram:
			if !hasButtons {
				t.Errorf("telegram proposal lost its buttons")
			}
		}
	}
}
