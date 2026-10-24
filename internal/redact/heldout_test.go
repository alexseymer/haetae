package redact

import (
	"strings"
	"testing"
)

// Held-out cases written separately from the rules and the labelled corpus,
// to check the rules generalise. Each secret substring must disappear; each
// clean line must stay unchanged.
func TestHeldOut(t *testing.T) {
	r := New()
	secrets := []struct{ line, secret string }{
		{`2026-10-24 INFO user login ok email=jane.doe+test@example.co.uk from 203.0.113.77`, "jane.doe+test@example.co.uk"},
		{`2026-10-24 INFO user login ok email=jane.doe+test@example.co.uk from 203.0.113.77`, "203.0.113.77"},
		{`connecting to postgres://app:S3cr3tPassw0rd!@db.internal:5432/app?sslmode=disable`, "S3cr3tPassw0rd!"},
		{`DEBUG request headers: Authorization: Bearer abcdEFGH1234ijklMNOP5678qrstUVWX`, "abcdEFGH1234ijklMNOP5678qrstUVWX"},
		{`{"level":"info","msg":"cfg","db_password":"hunter2hunter2","port":5432}`, "hunter2hunter2"},
		{`export AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY`, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		{`token ghp` + `_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789 used`, "ghp" + "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"},
		{`payment card 4111 1111 1111 1111 declined`, "4111 1111 1111 1111"},
		{`payment card 4111-1111-1111-1111 declined`, "4111-1111-1111-1111"},
		{`call me on +49 170 1234567 please`, "+49 170 1234567"},
		{`client=2001:db8:85a3::8a2e:370:7334 connected`, "2001:db8:85a3::8a2e:370:7334"},
		{`api_key=sk_` + `live_4eC39HqLyjWDarjtT1zdp7dc rotated`, "sk_" + "live_4eC39HqLyjWDarjtT1zdp7dc"},
		{`eyJ` + `hbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U`, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"},
		{`Cookie: session=9f8e7d6c5b4a39281706f5e4d3c2b1a0; theme=dark`, "9f8e7d6c5b4a39281706f5e4d3c2b1a0"},
		{`password: "p@ss w0rd with spaces"`, "p@ss w0rd with spaces"},
	}
	for _, tc := range secrets {
		got, err := r.Redact(tc.line)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, tc.secret) {
			t.Errorf("not redacted: %q\n  in:  %s\n  out: %s", tc.secret, tc.line, got)
		}
	}

	clean := []string{
		`2026-10-24T20:00:01.123Z INFO request id=7f3a9c12-1b2c-4d5e-8f90-a1b2c3d4e5f6 took 12ms status=200`,
		`GET /api/v1/items/12345 HTTP/1.1 200 5120 "-" "curl/8.5.0"`,
		`version 1.2.3.4 started, build 20261024, pid 4242`,
		`worker 3 processed 1500000 rows in 12.5s (120000 rows/s)`,
		`java.lang.NullPointerException at com.example.Service.handle(Service.java:123)`,
		`cache miss key=user:42:profile ttl=300`,
		`mounted /var/lib/docker/overlay2/abc123/merged on /app`,
		`sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08 pulled`,
		`rate limit: 1000 requests/minute for tenant 7`,
		`epoch 1761336001 delta 86400 offset -3600`,
	}
	for _, line := range clean {
		got, err := r.Redact(line)
		if err != nil {
			t.Fatal(err)
		}
		if got != line {
			t.Errorf("clean line changed:\n  in:  %s\n  out: %s", line, got)
		}
	}
}
