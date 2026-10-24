package logpipe

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestAggregatorMergeWindow(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	clock := now
	a := NewAggregator(Config{
		MergeWindow: 10 * time.Second,
		Now:         func() time.Time { return clock },
	})
	line := `{"level":"info","msg":"tick","n":1}`
	for i := 0; i < 5; i++ {
		a.Add(line, now.Add(time.Duration(i)*time.Second))
	}
	clock = now.Add(11 * time.Second)
	ev := a.Flush(false)
	if len(ev) != 1 {
		t.Fatalf("events=%d want 1: %+v", len(ev), ev)
	}
	if ev[0].Count != 5 {
		t.Fatalf("count=%d want 5", ev[0].Count)
	}
}

func TestAggregatorTwoWindows(t *testing.T) {
	now := time.Unix(2000, 0).UTC()
	clock := now
	a := NewAggregator(Config{
		MergeWindow: 5 * time.Second,
		Now:         func() time.Time { return clock },
	})
	line := "INFO tick"
	a.Add(line, now)
	a.Add(line, now.Add(1*time.Second))
	clock = now.Add(6 * time.Second)
	ev1 := a.Flush(false)
	if len(ev1) != 1 || ev1[0].Count != 2 {
		t.Fatalf("first flush: %+v", ev1)
	}
	a.Add(line, clock)
	a.Add(line, clock.Add(time.Second))
	clock = clock.Add(6 * time.Second)
	ev2 := a.Flush(false)
	if len(ev2) != 1 || ev2[0].Count != 2 {
		t.Fatalf("second flush: %+v", ev2)
	}
}

func TestAggregatorErrorNeverSkipped(t *testing.T) {
	a := NewAggregator(Config{})
	line := `[ERROR] boom id=1`
	id := TemplateID(DetectLevel(line), Template(line))
	a.SetGreen([]string{id})
	skipped := a.Add(line, time.Now())
	if skipped {
		t.Fatal("error line must never be skipped")
	}
	ev := a.Flush(true)
	if len(ev) != 1 || ev[0].Count != 1 {
		t.Fatalf("ev=%+v", ev)
	}
}

func TestAggregatorGreenSkip(t *testing.T) {
	a := NewAggregator(Config{})
	line := `{"level":"info","msg":"ok","n":1}`
	id := TemplateID(DetectLevel(line), Template(line))
	a.SetGreen([]string{id})
	if !a.Add(line, time.Now()) {
		t.Fatal("expected skipped")
	}
	st := a.Stats()
	if st.LinesSkipped != 1 || st.LinesIn != 1 {
		t.Fatalf("stats=%+v", st)
	}
	if len(a.Flush(true)) != 0 {
		t.Fatal("skipped lines must not emit")
	}
}

func TestAggregatorMaxTemplates(t *testing.T) {
	a := NewAggregator(Config{
		MergeWindow:  time.Hour,
		MaxTemplates: 50,
	})
	base := time.Now()
	for i := 0; i < 100000; i++ {
		// Use alphabetic unique tokens so Template does not collapse them to <NUM>.
		line := "INFO unique-shape-" + alphaID(i) + " done"
		a.Add(line, base)
		if a.openCount() > 50 {
			t.Fatalf("open=%d > 50 at i=%d", a.openCount(), i)
		}
	}
	if a.seenCount() > 4096 {
		t.Fatalf("seen=%d > 4096", a.seenCount())
	}
	st := a.Stats()
	if st.EarlyFlushes == 0 {
		t.Fatal("expected EarlyFlushes > 0")
	}
	_ = a.Flush(true)
}

func TestAggregatorSampleTruncate(t *testing.T) {
	a := NewAggregator(Config{MaxSampleLen: 8})
	line := "INFO " + strings.Repeat("あ", 20)
	a.Add(line, time.Now())
	ev := a.Flush(true)
	if len(ev) != 1 {
		t.Fatal(ev)
	}
	if len(ev[0].Sample) > 8 {
		t.Fatalf("sample len=%d", len(ev[0].Sample))
	}
	if !utf8.ValidString(ev[0].Sample) {
		t.Fatal("invalid utf8 sample")
	}
}

func TestNewTemplates(t *testing.T) {
	a := NewAggregator(Config{})
	a.Add(`{"level":"info","msg":"a"}`, time.Now())
	a.Add(`{"level":"info","msg":"a"}`, time.Now()) // same template
	a.Add(`{"level":"warn","msg":"b"}`, time.Now())
	infos := a.NewTemplates()
	if len(infos) != 2 {
		t.Fatalf("got %d want 2: %+v", len(infos), infos)
	}
	if len(a.NewTemplates()) != 0 {
		t.Fatal("second call should be empty")
	}
	// green excluded
	a2 := NewAggregator(Config{})
	line := `{"level":"info","msg":"c"}`
	id := TemplateID(DetectLevel(line), Template(line))
	a2.SetGreen([]string{id})
	a2.Add(line, time.Now()) // skipped — noteSeen not called for green skip path
	// Actually skipped lines return before noteSeen — so NewTemplates empty is correct.
	if n := a2.NewTemplates(); len(n) != 0 {
		t.Fatalf("green skipped should not appear: %+v", n)
	}
}

func TestCorpusAggregate(t *testing.T) {
	path := corpusPath(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(lines) < 10000 {
		t.Fatalf("corpus too small: %d lines", len(lines))
	}

	// Count AlwaysSend lines in full corpus.
	always := 0
	for _, line := range lines {
		if DetectLevel(line).AlwaysSend() {
			always++
		}
	}

	// (a)+(b): no green set
	now := time.Unix(0, 0).UTC()
	a := NewAggregator(Config{
		MergeWindow:  30 * time.Second,
		MaxTemplates: 2000,
		Now:          func() time.Time { return now.Add(time.Hour) },
	})
	t0 := now
	for i, line := range lines {
		a.Add(line, t0.Add(time.Duration(i)*time.Millisecond))
	}
	events := a.Flush(true)

	var alwaysCount int
	for _, ev := range events {
		if ev.Level.AlwaysSend() {
			alwaysCount += ev.Count
		}
	}
	if alwaysCount != always {
		t.Fatalf("(a) always-send accounted %d want %d", alwaysCount, always)
	}
	if float64(len(events)) >= 0.15*float64(len(lines)) {
		t.Fatalf("(b) events=%d >= 15%% of lines=%d", len(events), len(lines))
	}

	// (c) green warmup from first 10%
	warmupN := len(lines) / 10
	greenSet := map[string]struct{}{}
	for _, line := range lines[:warmupN] {
		lv := DetectLevel(line)
		if lv.AlwaysSend() {
			continue
		}
		id := TemplateID(lv, Template(line))
		greenSet[id] = struct{}{}
	}
	ids := make([]string, 0, len(greenSet))
	for id := range greenSet {
		ids = append(ids, id)
	}

	a2 := NewAggregator(Config{
		MergeWindow:  30 * time.Second,
		MaxTemplates: 2000,
		Now:          func() time.Time { return now.Add(time.Hour) },
	})
	a2.SetGreen(ids)
	rest := lines[warmupN:]
	var skipped int
	for i, line := range rest {
		if a2.Add(line, t0.Add(time.Duration(i)*time.Millisecond)) {
			skipped++
		}
	}
	frac := float64(skipped) / float64(len(rest))
	if frac < 0.90 {
		t.Fatalf("(c) skipped fraction %.3f < 0.90 (skipped=%d rest=%d green=%d)", frac, skipped, len(rest), len(ids))
	}
	t.Logf("MEASURE logpipe corpus lines=%d events=%d suppressed-after-warmup=%.3f", len(lines), len(events), frac)
}

func corpusPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	// internal/logpipe -> repo root
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(root, "test", "corpus", "logs", "app.log")
}

func alphaID(i int) string {
	// Base-20 using only letters outside a-f so Template won't collapse to <HEX>.
	const alphabet = "ghijklmnopqrstuvwx"
	if i < 0 {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for {
		pos--
		b[pos] = alphabet[i%len(alphabet)]
		i /= len(alphabet)
		if i == 0 {
			break
		}
	}
	return string(b[pos:])
}
