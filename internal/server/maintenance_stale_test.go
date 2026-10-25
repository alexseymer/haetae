package server

import (
	"testing"

	"github.com/alexseymer/haetae/internal/agent/deploy"
)

func TestStaleDeployAgeExceedsAgentDeployTimeouts(t *testing.T) {
	need := deploy.DefaultBuildTimeout + deploy.DefaultStartTimeout
	if StaleDeployAge <= need {
		t.Fatalf("StaleDeployAge = %v, want > build+start (%v + %v = %v)",
			StaleDeployAge, deploy.DefaultBuildTimeout, deploy.DefaultStartTimeout, need)
	}
}
