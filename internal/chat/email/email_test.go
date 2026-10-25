package email_test

import (
	"context"
	"net"
	"strings"
	"testing"

	"github.com/alexseymer/haetae/internal/chat/email"
	"github.com/alexseymer/haetae/internal/store"
	"github.com/google/uuid"
)

func TestEmailRejectsHeaderInjection(t *testing.T) {
	ch := email.New(email.Config{
		Addr: "127.0.0.1:1", From: "ok@example.com", Insecure: true,
		Dial: func(network, addr string) (net.Conn, error) {
			t.Fatal("should not dial")
			return nil, nil
		},
	})
	acc := &store.ChatAccount{ExternalID: "evil@x.com\r\nBcc: attacker@evil"}
	err := ch.Send(context.Background(), store.ChatMessage{
		ID: uuid.New(), Body: "hi", Kind: store.KindIncident,
	}, acc)
	if err == nil || !strings.Contains(err.Error(), "control") && !strings.Contains(err.Error(), "to:") {
		t.Fatalf("want header rejection, got %v", err)
	}
}
