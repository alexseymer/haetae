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
