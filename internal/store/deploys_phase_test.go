package store

import "testing"

func TestPhaseAllowed(t *testing.T) {
	ok := [][2]string{
		{DeployQueued, DeployFetching},
		{DeployQueued, DeployBuilding}, // skip forward
		{DeployFetching, DeployBuilding},
		{DeployBuilding, DeployStarting},
		{DeployStarting, DeployChecking},
		{DeployChecking, DeployHealthy},
		{DeployChecking, DeployFailed},
		{DeployFailed, DeployRollingBack},
		{DeployRollingBack, DeployRolledBack},
		{DeployRollingBack, DeployRollbackFailed},
	}
	for _, p := range ok {
		if !phaseAllowed(p[0], p[1]) {
			t.Errorf("phaseAllowed(%s→%s) = false, want true", p[0], p[1])
		}
	}
	bad := [][2]string{
		{DeployQueued, DeployQueued},
		{DeployFetching, DeployQueued},
		{DeployHealthy, DeployFailed},
		{DeployFailed, DeployHealthy},
		{DeployChecking, DeployRollingBack},
		{DeployBuilding, DeployHealthy},
		{DeployRolledBack, DeployRollingBack},
		{DeployHealthy, DeployRollingBack},
	}
	for _, p := range bad {
		if phaseAllowed(p[0], p[1]) {
			t.Errorf("phaseAllowed(%s→%s) = true, want false", p[0], p[1])
		}
	}
}
