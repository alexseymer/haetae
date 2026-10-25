package chat_test

import (
	"testing"

	"github.com/alexseymer/haetae/internal/chat"
)

func TestValidatePublicURL(t *testing.T) {
	if err := chat.ValidatePublicURL(""); err != nil {
		t.Fatal(err)
	}
	if err := chat.ValidatePublicURL("https://app.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := chat.ValidatePublicURL("http://localhost:8443"); err != nil {
		t.Fatal(err)
	}
	if err := chat.ValidatePublicURL("http://evil.example"); err == nil {
		t.Fatal("expected reject for http non-localhost")
	}
	if err := chat.ValidatePublicURL("ftp://x"); err == nil {
		t.Fatal("expected reject for ftp")
	}
}
