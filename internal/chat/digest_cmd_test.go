package chat

import (
	"errors"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/operator"
)

func TestParseDigestCommand(t *testing.T) {
	type want struct {
		kind   string
		host   string
		window time.Duration
		err    error
	}
	ok := func(kind, host string, w time.Duration) want { return want{kind: kind, host: host, window: w} }
	cases := map[string]struct {
		text string
		ok   bool // a shortcut at all
		want want
	}{
		"logs bare":            {"/logs", true, ok(digestCmdLogs, "", 0)},
		"logs host and window": {"/logs hel1 6h", true, ok(digestCmdLogs, "hel1", 6*time.Hour)},
		"window first":         {"/logs 1h hel1", true, ok(digestCmdLogs, "hel1", time.Hour)},
		"case and bot suffix":  {"/LOGS@haetae_bot Hel One 24h", true, ok(digestCmdLogs, "Hel One", 24*time.Hour)},
		"logs window only":     {"/logs 1h", true, ok(digestCmdLogs, "", time.Hour)},
		"logs bad window":      {"/logs hel1 2d", true, want{err: operator.ErrBadWindow}},
		"logs two windows":     {"/logs 1h 6h", true, want{err: operator.ErrBadWindow}},
		"security bare":        {"/security", true, ok(digestCmdSecurity, "", 0)},
		"security host":        {"/security  hel1 ", true, ok(digestCmdSecurity, "hel1", 0)},
		"security keeps 6h":    {"/security 6h", true, ok(digestCmdSecurity, "6h", 0)},
		"not a shortcut":       {"/loggers hel1", false, want{}},
		"prefix only":          {"/logsx", false, want{}},
		"mid sentence":         {"show /logs", false, want{}},
		"link command":         {"/link ABC123", false, want{}},
		"plain question":       {"what is down?", false, want{}},
		"empty":                {"   ", false, want{}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cmd, isCmd, err := parseDigestCommand(c.text)
			if isCmd != c.ok {
				t.Fatalf("ok = %v, want %v", isCmd, c.ok)
			}
			if !c.ok {
				return
			}
			if c.want.err != nil {
				if !errors.Is(err, c.want.err) {
					t.Fatalf("err = %v, want %v", err, c.want.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if cmd.kind != c.want.kind || cmd.host != c.want.host || cmd.window != c.want.window {
				t.Fatalf("cmd = %+v, want %+v", cmd, c.want)
			}
		})
	}
}
