package store

import "testing"

func TestPhaseAllowed(t *testing.T) {
	ok := [][2]string{
		{DeployQueued, DeployFetching},
		{DeployQueued, DeployFailed},
		{DeployFetching, DeployBuilding},
		{DeployFetching, DeployFailed},
		{DeployBuilding, DeployStarting},
		{DeployBuilding, DeployFailed},
		{DeployStarting, DeployChecking},
		{DeployStarting, DeployFailed},
		{DeployStarting, DeployRollingBack},
		{DeployChecking, DeployHealthy},
		{DeployChecking, DeployFailed},
		{DeployChecking, DeployRollingBack},
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
		{DeployQueued, DeployBuilding},
		{DeployFetching, DeployQueued},
		{DeployHealthy, DeployFailed},
		{DeployFailed, DeployHealthy},
		{DeployBuilding, DeployHealthy},
		{DeployRolledBack, DeployRollingBack},
		{DeployHealthy, DeployRollingBack},
		{DeployRollingBack, DeployFetching},
	}
	for _, p := range bad {
		if phaseAllowed(p[0], p[1]) {
			t.Errorf("phaseAllowed(%s→%s) = true, want false", p[0], p[1])
		}
	}
}
