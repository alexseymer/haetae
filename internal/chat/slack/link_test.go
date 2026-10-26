package slack

import (
	"context"
	"encoding/json"
	"testing"
)

type recHandler struct{ texts []string }

func (r *recHandler) HandleInboundText(_ context.Context, _, _, text string) (string, error) {
	r.texts = append(r.texts, text)
	return "", nil
}
func (r *recHandler) HandleAction(context.Context, string, string, string) (string, error) {
	return "", nil
}

func dm(text, chType string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{"event": map[string]any{
		"type": "message", "user": "U1", "text": text, "channel": "D1", "channel_type": chType,
	}})
	return b
}

func TestSlackDMLinkWithoutSlash(t *testing.T) {
	h := &recHandler{}
	c := &Channel{Handler: h}
	c.handleEvents(context.Background(), dm("link ABCD2345", "im"))
	c.handleEvents(context.Background(), dm("  Link   ABCD2345 ", "im"))
	c.handleEvents(context.Background(), dm("what is the status", "im"))
	c.handleEvents(context.Background(), dm("link ABCD2345 please", "im"))
	// Only a direct message counts: a channel message is passed unchanged.
	c.handleEvents(context.Background(), dm("link ABCD2345", "channel"))
	want := []string{"/link ABCD2345", "/link ABCD2345", "what is the status", "link ABCD2345 please", "link ABCD2345"}
	if len(h.texts) != len(want) {
		t.Fatalf("got %q", h.texts)
	}
	for i := range want {
		if h.texts[i] != want[i] {
			t.Errorf("text %d = %q, want %q", i, h.texts[i], want[i])
		}
	}
}
