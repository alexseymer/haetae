package redact

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRulesOrderSecretsBeforePII(t *testing.T) {
	r := New()
	infos := r.Rules()
	if len(infos) == 0 {
		t.Fatal("no rules")
	}
	seenPII := false
	for _, info := range infos {
		switch info.Kind {
		case KindSecret:
			if seenPII {
				t.Fatalf("secret rule %q after pii", info.Name)
			}
		case KindPII:
			seenPII = true
		default:
			t.Fatalf("unknown kind %q", info.Kind)
		}
	}
}

func TestRedactTable(t *testing.T) {
	r := New()
	cases := []struct {
		rule    string
		pos     []string
		neg     []string
		contain string // substring that must appear in redacted positive lines
	}{
		{
			rule: "aws-access-key",
			pos: []string{
				"key=AKI" + "AIOSFODNN7EXAMPLE used",
				"ASI" + "AIOSFODNN7EXAMPLEextra",
				"creds AKI" + "AJGHTUYRFMDISPLOK end",
			},
			neg: []string{
				"key=AKIASHORT",
				"akiaiosfodnn7example",
				"BKIAIOSFODNN7EXAMPLE",
			},
		},
		{
			rule: "aws-secret-key",
			pos: []string{
				"aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
				"AWS_SECRET_ACCESS_KEY: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
				"aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			},
			neg: []string{
				"aws_secret_access_key=short",
				"secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
				"aws_secret_access_key=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPL",
			},
		},
		{
			rule: "private-key",
			pos: []string{
				`pem -----BEGIN ` + `RSA PRIVATE KEY-----MIIEowIBAAKCAQEA0Z3VS5JJcds3xfn/-----END RSA PRIVATE KEY----- done`,
				`hdr -----BEGIN ` + `EC PRIVATE KEY-----`,
				`-----BEGIN ` + `PRIVATE KEY-----YGJiYmJiYmJiYmJiYmI=`,
			},
			neg: []string{
				"-----BEGIN CERTIFICATE-----MIIB",
				"BEGIN PUBLIC KEY-----",
				"private key without header",
			},
		},
		{
			rule: "github-token",
			pos: []string{
				"tok=ghp" + "_abcdefghijklmnopqrstuvwxyz0123456789",
				"gho" + "_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789ab",
				"ghs_" + strings.Repeat("a", 36),
			},
			neg: []string{
				"ghp_short",
				"ghx_" + strings.Repeat("a", 36),
				"github_pat_notthis",
			},
		},
		{
			rule: "slack-token",
			pos: []string{
				"xox" + "b-123456789012-abcdefghij",
				"xox" + "p-1-2-3-abcdefghijklmnop",
				"xox" + "a-slack-token-value-here",
			},
			neg: []string{
				"xoxb-short",
				"xoxz-123456789012-abcdefghij",
				"slack-token-plain",
			},
		},
		{
			rule: "stripe-key",
			pos: []string{
				"sk_live_" + strings.Repeat("a", 20),
				"rk_live_" + strings.Repeat("b", 24),
				"key sk_" + "live_abcdefghijklmnopqrst",
			},
			neg: []string{
				"sk_test_" + strings.Repeat("a", 20),
				"sk_live_short",
				"pk_live_" + strings.Repeat("a", 20),
			},
		},
		{
			rule: "google-api-key",
			pos: []string{
				"AIza" + strings.Repeat("A", 35),
				"key=AIzaSyA" + strings.Repeat("1", 32),
				"AIza-_A" + strings.Repeat("x", 32),
			},
			neg: []string{
				"AIzaShort",
				"AIza" + strings.Repeat("A", 34),
				"BIza" + strings.Repeat("A", 35),
			},
		},
		{
			rule: "jwt",
			pos: []string{
				"eyJ" + "hbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.signaturepart00",
				"header=eyJabc.eyJdef.ghi_jkl",
				"eyJ" + "0eXAiOiJKV1QifQ.eyJmb28iOiJiYXIifQ.dGVzdHNpZw",
			},
			neg: []string{
				"eyJonlyone",
				"eyJab.eyJcd",
				"notajwt.at.all",
			},
		},
		{
			rule: "bearer-token",
			pos: []string{
				"Authorization: Bearer " + strings.Repeat("a", 16),
				"bearer abcdefghijklmnop",
				"Bearer tok_" + strings.Repeat("x", 20),
			},
			neg: []string{
				"Bearer short",
				"Bear abcdefghijklmnop",
				"token Bearer",
			},
		},
		{
			rule: "basic-auth-header",
			pos: []string{
				"Authorization: Basic dXNlcjpwYXNz",
				"authorization: basic YWRtaW46c2VjcmV0",
				"Authorization:  Basic  abcdef==",
			},
			neg: []string{
				"Authorization: Bearer aaa",
				"Basic dXNlcjpwYXNz",
				"Authorization: Digest abc",
			},
		},
		{
			rule: "url-credentials",
			pos: []string{
				"https://user:secret@example.com/path",
				"http://alice:pw12345@db.internal:5432",
				"wss://u:p@host/ws",
			},
			neg: []string{
				"https://example.com/path",
				"https://user@example.com",
				"not a url user:pass@host",
			},
		},
		{
			rule: "generic-secret",
			pos: []string{
				"password=s3cret!",
				`config token: abcdefgh`,
				`{"api_key":"abcdef"}`,
			},
			neg: []string{
				"password=ab",
				"username=longenough",
				"password =",
			},
		},
		{
			rule: "email",
			pos: []string{
				"from alice@example.com",
				"contact=bob.smith+tag@mail.co.uk",
				"user@localhost.localdomain",
			},
			neg: []string{
				"not-an-email@",
				"@nodomain",
				"plain text",
			},
		},
		{
			rule: "ipv4",
			pos: []string{
				"client 203.0.113.10 connected",
				"peer=198.51.100.1",
				"DNS 8.8.8.8 ok",
			},
			neg: []string{
				"version 1.2.3.4 released",
				"app v1.2.3.4 starting",
				"not an ip 999.1.1.1",
			},
		},
		{
			rule: "ipv6",
			pos: []string{
				"addr 2001:db8:85a3::8a2e:370:7334",
				"fe80:0:0:0:0:0:0:1 local",
				"dst=2001:db8::1 path",
			},
			neg: []string{
				"::1",
				"ab:cd",
				"no colons here",
			},
		},
		{
			rule: "phone",
			pos: []string{
				"call +1-415-555-2671 now",
				"mobile +44 20 7946 0958",
				"office (415) 555-2671",
			},
			neg: []string{
				"ts=2024-01-15T12:34:56Z",
				"epoch=1704067200",
				"version 1.2.3 build",
			},
		},
		{
			rule: "credit-card",
			pos: []string{
				"card 4111111111111111",
				"pan=5500-0000-0000-0004",
				"cc 3400 0000 0000 009",
			},
			neg: []string{
				"card 4111111111111112",
				"digits 1234567890123",
				"order-id 4111",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.rule, func(t *testing.T) {
			tok := "[REDACTED:" + tc.rule + "]"
			for i, line := range tc.pos {
				got, err := r.Redact(line)
				if err != nil {
					t.Fatalf("pos[%d] err: %v", i, err)
				}
				if !strings.Contains(got, tok) {
					t.Fatalf("pos[%d]: want token %q in %q (from %q)", i, tok, got, line)
				}
			}
			for i, line := range tc.neg {
				got, err := r.Redact(line)
				if err != nil {
					t.Fatalf("neg[%d] err: %v", i, err)
				}
				if strings.Contains(got, tok) {
					t.Fatalf("neg[%d]: unexpected token in %q (from %q)", i, got, line)
				}
			}
		})
	}
}

func TestURLCredentialsKeepsSchemeAndHost(t *testing.T) {
	r := New()
	got, err := r.Redact("fetch https://user:s3cret@example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "https://") || !strings.Contains(got, "@example.com") {
		t.Fatalf("got %q", got)
	}
	if strings.Contains(got, "user:") || strings.Contains(got, "s3cret") {
		t.Fatalf("credentials remain: %q", got)
	}
}

func TestRedactLineTooLong(t *testing.T) {
	r := New()
	line := strings.Repeat("a", MaxLineBytes+1)
	if _, err := r.Redact(line); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("err = %v, want ErrLineTooLong", err)
	}
}

func TestRedactBatchFailClosed(t *testing.T) {
	t.Run("panic", func(t *testing.T) {
		r := newWithRules([]rule{{
			name: "boom",
			kind: KindSecret,
			apply: func(string) (string, int) {
				panic("injected")
			},
		}})
		res, err := r.RedactBatch(context.Background(), []string{"hello"}, time.Second)
		if err == nil {
			t.Fatal("want error")
		}
		if len(res.Lines) != 0 || len(res.Counts) != 0 {
			t.Fatalf("partial output: %+v", res)
		}
	})

	t.Run("budget", func(t *testing.T) {
		r := newWithRules([]rule{{
			name: "slow",
			kind: KindSecret,
			apply: func(s string) (string, int) {
				time.Sleep(5 * time.Millisecond)
				return s, 0
			},
		}})
		res, err := r.RedactBatch(context.Background(), []string{"a", "b"}, time.Nanosecond)
		if !errors.Is(err, ErrBudget) {
			t.Fatalf("err = %v, want ErrBudget", err)
		}
		if len(res.Lines) != 0 {
			t.Fatalf("partial output: %+v", res)
		}
	})

	t.Run("ctx", func(t *testing.T) {
		r := New()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		res, err := r.RedactBatch(ctx, []string{"hello"}, time.Second)
		if err == nil {
			t.Fatal("want error")
		}
		if len(res.Lines) != 0 {
			t.Fatalf("partial output: %+v", res)
		}
	})

	t.Run("too-long", func(t *testing.T) {
		r := New()
		long := strings.Repeat("b", MaxLineBytes+1)
		res, err := r.RedactBatch(context.Background(), []string{"ok", long}, time.Second)
		if !errors.Is(err, ErrLineTooLong) {
			t.Fatalf("err = %v, want ErrLineTooLong", err)
		}
		if len(res.Lines) != 0 {
			t.Fatalf("partial output: %+v", res)
		}
	})
}

func TestRedactBatchOK(t *testing.T) {
	r := New()
	res, err := r.RedactBatch(context.Background(), []string{
		"mail alice@example.com",
		"key=AKI" + "AIOSFODNN7EXAMPLE",
	}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 2 {
		t.Fatalf("lines = %d", len(res.Lines))
	}
	if res.Counts["email"] < 1 || res.Counts["aws-access-key"] < 1 {
		t.Fatalf("counts = %#v", res.Counts)
	}
}

func TestRedactAdversarialLinear(t *testing.T) {
	r := New()
	cases := []string{
		strings.Repeat("a", MaxLineBytes),
		strings.Repeat("1-", MaxLineBytes/2)[:MaxLineBytes],
		strings.Repeat("a@", MaxLineBytes/2)[:MaxLineBytes],
	}
	for i, line := range cases {
		start := time.Now()
		if _, err := r.Redact(line); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("case %d took %v, want <2s", i, d)
		}
	}
}

func BenchmarkRedact(b *testing.B) {
	r := New()
	typical := `2024-01-15T12:34:56Z level=info msg="handled request" req_id=abc-123 path=/api/v1/items duration_ms=42 status=200`
	adv := strings.Repeat("a", MaxLineBytes)
	b.Run("typical", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := r.Redact(typical); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("adversarial64KiB", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := r.Redact(adv); err != nil {
				b.Fatal(err)
			}
		}
	})
}
