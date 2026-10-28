package operator

import (
	"time"

	"github.com/alexseymer/haetae/internal/operator/provider"
)

// failLogEvery is the shortest gap between two failure log lines of one class.
const failLogEvery = 60 * time.Second

// logFailure logs a failed AI call at Warn, at most once per class per
// failLogEvery per process, so a down provider cannot flood the log. It logs
// the class, status and Detail only: never the question, the key or the body.
func (s *Service) logFailure(op string, err error) {
	if err == nil {
		return
	}
	f := Classify(err)
	switch f.Class {
	case ClassDisabled, ClassCanceled, ClassUnknown:
		return
	}
	now := s.cfg.Now()
	s.failMu.Lock()
	if s.failLast == nil {
		s.failLast = map[string]time.Time{}
	}
	if t, ok := s.failLast[f.Class]; ok && now.Sub(t) < failLogEvery {
		s.failMu.Unlock()
		return
	}
	s.failLast[f.Class] = now
	s.failMu.Unlock()
	name, model := "", ""
	if s.cfg.Provider != nil {
		name, model = s.cfg.Provider.Name(), provider.ModelOf(s.cfg.Provider)
	}
	s.cfg.Log.Warn("operator: AI call failed", "op", op, "class", f.Class, "status", f.Status,
		"detail", f.Detail, "provider", name, "model", model)
}
