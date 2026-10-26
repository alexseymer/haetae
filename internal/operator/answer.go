package operator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/alexseymer/haetae/internal/operator/provider"
)

// ErrBadResponse is returned when the model's final answer cannot be parsed
// as the required JSON, even after one repair attempt. It is the provider's
// error value, so errors.Is matches either name.
var ErrBadResponse = provider.ErrBadResponse

const (
	maxAnswerBytes   = 32 << 10
	maxStatementLen  = 500 // runes
	maxReasonLen     = 500 // runes
	maxActionsPerAns = 3
)

type wireStatement struct {
	Text     string   `json:"text"`
	Evidence []string `json:"evidence"`
}

type wireAction struct {
	Kind        string   `json:"kind"`
	ContainerID string   `json:"container_id"`
	Reason      string   `json:"reason"`
	Evidence    []string `json:"evidence"`
}

type wireAnswer struct {
	Statements []wireStatement `json:"statements"`
	Actions    []wireAction    `json:"actions"`
}

// parseAnswer strictly parses the model's final text: at most 32 KB, the first
// JSON object in it (text outside is ignored), unknown fields and wrong types
// rejected.
func parseAnswer(text string) (wireAnswer, error) {
	if len(text) > maxAnswerBytes {
		return wireAnswer{}, fmt.Errorf("%w: answer too large", ErrBadResponse)
	}
	i := strings.IndexByte(text, '{')
	if i < 0 {
		return wireAnswer{}, fmt.Errorf("%w: no JSON object", ErrBadResponse)
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(text[i:])))
	dec.DisallowUnknownFields()
	var w wireAnswer
	if err := dec.Decode(&w); err != nil {
		return wireAnswer{}, fmt.Errorf("%w: %v", ErrBadResponse, err)
	}
	return w, nil
}

var (
	evMarkerRe = regexp.MustCompile(`\[\s*ev_[^\]]*\]?`)
	evIDRe     = regexp.MustCompile(`^ev_[0-9]{1,6}$`)
)

// cleanText strips control characters and invented "[ev_" markers, collapses
// whitespace and cuts to max runes.
func cleanText(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = evMarkerRe.ReplaceAllString(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == ' ' || r == ' ' {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > max {
		s = string([]rune(s)[:max])
		s = strings.TrimSpace(s)
	}
	return s
}

// validIDs returns the cited ids that exist in the conversation, deduplicated.
func validIDs(c *conv, ids []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !evIDRe.MatchString(id) || seen[id] || c.find(id) == nil {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// validate keeps the statements whose evidence is non-empty and every id of
// which belongs to this conversation. It returns the surviving statements,
// the number dropped, and the cited evidence in id order.
func validate(c *conv, in []wireStatement) (kept []Statement, dropped int, cited []Evidence) {
	used := map[string]bool{}
	for _, st := range in {
		text := cleanText(st.Text, maxStatementLen)
		if text == "" || len(st.Evidence) == 0 {
			dropped++
			continue
		}
		ids := validIDs(c, st.Evidence)
		if len(ids) != len(dedupe(st.Evidence)) {
			dropped++ // names an id outside this conversation
			continue
		}
		kept = append(kept, Statement{Text: text, Evidence: ids})
		for _, id := range ids {
			used[id] = true
		}
	}
	for _, e := range c.evs {
		if used[e.ID] {
			cited = append(cited, e.Evidence)
		}
	}
	sort.SliceStable(cited, func(i, j int) bool { return evSeq(cited[i].ID) < evSeq(cited[j].ID) })
	return kept, dropped, cited
}

func dedupe(ids []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func evSeq(id string) int {
	var n int
	_, _ = fmt.Sscanf(id, "ev_%d", &n)
	return n
}
