package logpipe

import (
	"testing"
)

func TestLevelString(t *testing.T) {
	cases := []struct {
		lv   Level
		want string
	}{
		{LevelUnknown, "unknown"},
		{LevelTrace, "trace"},
		{LevelDebug, "debug"},
		{LevelInfo, "info"},
		{LevelWarn, "warn"},
		{LevelError, "error"},
		{LevelFatal, "fatal"},
		{Level(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.lv.String(); got != tc.want {
			t.Errorf("%v.String()=%q want %q", tc.lv, got, tc.want)
		}
	}
}

func TestAlwaysSend(t *testing.T) {
	if !LevelWarn.AlwaysSend() || !LevelError.AlwaysSend() || !LevelFatal.AlwaysSend() || !LevelUnknown.AlwaysSend() {
		t.Fatal("warn/error/fatal/unknown must AlwaysSend")
	}
	if LevelInfo.AlwaysSend() || LevelDebug.AlwaysSend() || LevelTrace.AlwaysSend() {
		t.Fatal("info/debug/trace must not AlwaysSend")
	}
}

func TestDetectLevel(t *testing.T) {
	cases := []struct {
		line string
		want Level
	}{
		// JSON string levels
		{`{"level":"info","msg":"hi"}`, LevelInfo},
		{`{"level":"INFO","msg":"hi"}`, LevelInfo},
		{`{"lvl":"debug"}`, LevelDebug},
		{`{"severity":"warning"}`, LevelWarn},
		{`{"severity":"WARN"}`, LevelWarn},
		{`{"loglevel":"error"}`, LevelError},
		{`{"log.level":"trace"}`, LevelTrace},
		{`{"@level":"fatal"}`, LevelFatal},
		{`{"level":"err"}`, LevelError},
		{`{"level":"critical"}`, LevelFatal},
		{`{"level":"panic"}`, LevelFatal},
		{`{"level":"emerg"}`, LevelFatal},
		{`{"level":"alert"}`, LevelFatal},
		// pino/bunyan numeric
		{`{"level":10,"msg":"t"}`, LevelTrace},
		{`{"level":20}`, LevelDebug},
		{`{"level":30}`, LevelInfo},
		{`{"level":40}`, LevelWarn},
		{`{"level":50}`, LevelError},
		{`{"level":60}`, LevelFatal},
		// bracketed
		{`[ERROR] something broke`, LevelError},
		{`[WARN] slow`, LevelWarn},
		{`2024-01-01 [INFO] ok`, LevelInfo},
		{`[DEBUG] x`, LevelDebug},
		{`[FATAL] die`, LevelFatal},
		// ERROR: / WARN
		{`ERROR: disk full`, LevelError},
		{`WARN something`, LevelWarn},
		{`INFO starting`, LevelInfo},
		// level= logfmt
		{`ts=1 level=warn msg=slow`, LevelWarn},
		{`level=error status=500`, LevelError},
		{`lvl=info hello`, LevelInfo},
		{`severity=debug x`, LevelDebug},
		// glog
		{`I0102 15:04:05.123456 1 main.go:10] hello`, LevelInfo},
		{`W0315 00:00:01.0 1 x.go:1] warn`, LevelWarn},
		{`E1225 12:00:00.0 1 x.go:1] err`, LevelError},
		{`F0101 00:00:00.0 1 x.go:1] fatal`, LevelFatal},
		// python
		{`ERROR:root:boom`, LevelError},
		{`WARNING:app.db:slow query`, LevelWarn},
		{`INFO:myapp:started`, LevelInfo},
		{`DEBUG:pkg:x`, LevelDebug},
		// java
		{`SEVERE: connection lost`, LevelError},
		{`WARNING: deprecated`, LevelWarn},
		{`INFO: ready`, LevelInfo},
		{`FINE: detail`, LevelDebug},
		// special error prefixes
		{`Traceback (most recent call last):`, LevelError},
		{`panic: runtime error`, LevelError},
		{`fatal error: out of memory`, LevelError},
		{`Exception in thread "main" java.lang.RuntimeException`, LevelError},
		{"\tat com.example.App.main(App.java:10)", LevelError},
		{`goroutine 1 [running]:`, LevelError},
		// unknown
		{`just some text without level`, LevelUnknown},
		{``, LevelUnknown},
		{`{"msg":"no level field"}`, LevelUnknown},
		// arbitrary / binary-ish should not panic
		{string([]byte{0xff, 0xfe, 0x00, 'x'}), LevelUnknown},
	}
	if len(cases) < 40 {
		t.Fatalf("need >= 40 cases, got %d", len(cases))
	}
	for _, tc := range cases {
		got := DetectLevel(tc.line)
		if got != tc.want {
			t.Errorf("DetectLevel(%q)=%s want %s", tc.line, got, tc.want)
		}
	}
}

func FuzzDetectLevel(f *testing.F) {
	seeds := []string{
		`{"level":"info"}`,
		`[ERROR] x`,
		`I0102 15:04:05.0 1 x.go:1] hi`,
		`Traceback (most recent call last):`,
		`level=warn msg=x`,
		`ERROR:root:boom`,
		``,
		string([]byte{0, 1, 2, 255}),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, line string) {
		_ = DetectLevel(line)
	})
}

// A real error must not be hidden by level words an attacker or payload puts
// into the same line: the most severe signal wins.
func TestDetectLevelCannotBeSpoofedDownwards(t *testing.T) {
	for _, tc := range []struct {
		name string
		line string
		want Level
	}{
		{"nested JSON level first", `{"details": {"level": "info"}, "error": "database crashed", "level": "fatal"}`, LevelFatal},
		{"nested JSON level last", `{"level":"error","payload":{"level":"debug"}}`, LevelError},
		{"logfmt level inside quoted msg", `msg="processing level=info request" level=error`, LevelError},
		{"logfmt level inside quoted msg 2", `level=error msg="user said level=debug"`, LevelError},
		{"bracket injected after real level", `2026-10-24 [ERROR] login failed for [DEBUG]`, LevelError},
		{"bracket injected before real level", `[DEBUG] x [FATAL] crash`, LevelFatal},
		{"info stays info", `level=info msg="all good"`, LevelInfo},
		{"plain nested info stays info", `{"level":"info","ctx":{"level":"debug"}}`, LevelInfo},
	} {
		if got := DetectLevel(tc.line); got != tc.want {
			t.Errorf("%s: DetectLevel(%q) = %v, want %v", tc.name, tc.line, got, tc.want)
		}
	}
}

// HTTP 500s share a template with 200s once numbers are masked, so they must
// not be treated as routine info.
func TestDetectLevelHTTPServerErrors(t *testing.T) {
	for line, want := range map[string]Level{
		`10.0.0.1 - - [24/Oct/2026:12:00:00 +0000] "GET /api HTTP/1.1" 500 123 level=info`: LevelError,
		`GET /api/items/7 HTTP/1.1" 502 12 level=info`:                                     LevelError,
		`level=info msg="request done" status=503 dur=12ms`:                                LevelError,
		`{"level":"info","status":500,"path":"/x"}`:                                        LevelError,
		`level=info msg="request done" status_code=504`:                                    LevelError,
		`GET /api/items/7 HTTP/1.1" 200 123 level=info`:                                    LevelInfo,
		`GET /api/items/7 HTTP/1.1" 404 123 level=info`:                                    LevelInfo,
		`level=info msg="processed 500 rows"`:                                              LevelInfo,
	} {
		if got := DetectLevel(line); got != want {
			t.Errorf("DetectLevel(%q) = %v, want %v", line, got, want)
		}
	}
}
