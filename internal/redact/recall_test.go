package redact

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type corpusSpan struct {
	Kind string `json:"kind"`
	Rule string `json:"rule"`
	Text string `json:"text"`
}

type corpusLine struct {
	Line  string       `json:"line"`
	Spans []corpusSpan `json:"spans"`
}

func TestCorpusRecall(t *testing.T) {
	path := corpusPath(t)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open corpus: %v", err)
	}
	defer f.Close()

	r := New()
	var (
		secretTotal, secretHit   int
		piiTotal, piiHit         int
		cleanTotal, cleanChanged int
	)

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), MaxLineBytes+1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		var row corpusLine
		// The corpus marks secret-shaped values with U+27E6 so the repository
		// never contains a contiguous secret pattern (GitHub push protection
		// would reject it). Remove the marker to get the real test value.
		if err := json.Unmarshal([]byte(strings.ReplaceAll(sc.Text(), "\u27e6", "")), &row); err != nil {
			t.Fatalf("line %d: json: %v", lineNo, err)
		}
		got, err := r.Redact(row.Line)
		if err != nil {
			t.Fatalf("line %d: redact: %v", lineNo, err)
		}
		if len(row.Spans) == 0 {
			cleanTotal++
			if got != row.Line {
				cleanChanged++
				t.Errorf("line %d: clean line changed:\n  in:  %q\n  out: %q", lineNo, row.Line, got)
			}
			continue
		}
		for _, sp := range row.Spans {
			if sp.Text == "" {
				t.Fatalf("line %d: empty span text for rule %s", lineNo, sp.Rule)
			}
			removed := !strings.Contains(got, sp.Text)
			switch sp.Kind {
			case "secret":
				secretTotal++
				if removed {
					secretHit++
				} else {
					t.Errorf("line %d: secret span still present rule=%s text=%q out=%q", lineNo, sp.Rule, sp.Text, got)
				}
			case "pii":
				piiTotal++
				if removed {
					piiHit++
				} else {
					t.Errorf("line %d: pii span still present rule=%s text=%q out=%q", lineNo, sp.Rule, sp.Text, got)
				}
			default:
				t.Fatalf("line %d: unknown kind %q", lineNo, sp.Kind)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if secretTotal < 60 {
		t.Fatalf("corpus secret spans = %d, want >= 60", secretTotal)
	}
	if piiTotal < 40 {
		t.Fatalf("corpus pii spans = %d, want >= 40", piiTotal)
	}
	if cleanTotal < 80 {
		t.Fatalf("corpus clean lines = %d, want >= 80", cleanTotal)
	}

	secretRecall := float64(secretHit) / float64(secretTotal)
	piiRecall := float64(piiHit) / float64(piiTotal)
	t.Logf("MEASURE redact recall secret=%.3f pii=%.3f clean-lines-changed=%d/%d",
		secretRecall, piiRecall, cleanChanged, cleanTotal)

	if secretRecall < 0.95 {
		t.Fatalf("secret recall = %.3f, want >= 0.95", secretRecall)
	}
	if cleanChanged != 0 {
		t.Fatalf("clean lines changed = %d/%d, want 0", cleanChanged, cleanTotal)
	}
}

func corpusPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	return filepath.Join(root, "test", "corpus", "redaction", "labelled.jsonl")
}
