package dream

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
)

func TestClaimedEvidenceCycleRecordsDependencyFailurePhases(t *testing.T) {
	teamID := uuid.NewString()
	claimed := &dreamcontract.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}
	diagnostics, store := &diagnosticRepositoryStub{}, &dreamRepositoryStub{}
	svc := &service{deps: Dependencies{ScheduledStore: store, Diagnostics: diagnostics}, now: time.Now}
	result, err := svc.runClaimedEvidenceCycle(context.Background(), teamID, EffectiveConfig{}, &RunCycleResult{TeamID: teamID, RunID: claimed.RunID}, claimed)
	require.Error(t, err)
	require.Equal(t, "error", result.Status)
	var phases []string
	for _, item := range diagnostics.recorded {
		if item.Phase != "run" {
			phases = append(phases, item.Phase)
		}
	}
	assert.Contains(t, phases, "target")
	assert.Contains(t, phases, "provider")
}
