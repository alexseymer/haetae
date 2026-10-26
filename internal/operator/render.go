package operator

import "strings"

func renderAnswer(a Answer) string {
	if a.CauseNotFound || len(a.Statements) == 0 {
		return CauseNotFound
	}
	var b strings.Builder
	for i, s := range a.Statements {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(strings.TrimSpace(s.Text))
		b.WriteString(" [")
		b.WriteString(strings.Join(s.Evidence, ", "))
		b.WriteString("]")
	}
	return b.String()
}
