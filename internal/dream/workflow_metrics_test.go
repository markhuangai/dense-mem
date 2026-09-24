package dream

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func TestRunCyclePersistsValidatedProviderHypothesis(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	runID := uuid.NewString()
	subjectID := uuid.NewString()
	middleID := uuid.NewString()
	objectID := uuid.NewString()
	activeSourceID := "relationship_a"
	candidateSourceID := "relationship_b"
	repo := &dreamRepositoryStub{
		run: dreamcontract.DreamCycleRun{
			TeamID:               teamID.String(),
			RunID:                runID,
			InitiatedByProfileID: ownerID.String(),
			RunDate:              "2026-07-17",
			Status:               "running",
			Claimed:              true,
		},
		inputs: []dreamcontract.DreamInput{
			{
				RelationshipID:   activeSourceID,
				OwnerProfileID:   ownerID.String(),
				Version:          2,
				Status:           "active",
				SubjectEntityID:  subjectID,
				SubjectName:      "Dense-Mem",
				PredicateKey:     "works_on",
				PredicateVersion: 1,
				ObjectEntityID:   middleID,
				ObjectName:       "PostgreSQL",
				SubjectKind:      "project",
				ObjectKind:       "product",
				Evidence:         []dreamcontract.DreamEvidence{{Content: "Dense-Mem works on PostgreSQL.", Authority: "primary"}},
			},
			{
				RelationshipID:   candidateSourceID,
				OwnerProfileID:   ownerID.String(),
				Version:          4,
				Status:           "pending_evidence",
				SubjectEntityID:  middleID,
				SubjectName:      "PostgreSQL",
				PredicateKey:     "informs",
				PredicateVersion: 1,
				ObjectEntityID:   objectID,
				ObjectName:       "Search freshness",
				SubjectKind:      "product",
				ObjectKind:       "concept",
				Evidence:         []dreamcontract.DreamEvidence{{Content: "PostgreSQL informs search freshness.", Authority: "primary"}},
			},
		},
		predicates: []dreamcontract.DreamTargetPredicate{{
			PredicateKey: "uses", Version: 1, AllowedSubjectKinds: []string{"project"}, AllowedObjectKinds: []string{"concept"}, RelationshipKind: "state", CurrentCardinality: "many",
		}},
	}
	generator := &dreamGeneratorStub{
		model: "provider-canonical",
		generated: []GeneratedDream{{
			PathRef:         "path_1",
			PredicateRef:    "predicate_1",
			EvidenceRefs:    []string{"evidence_1", "evidence_2"},
			Hypothesis:      "Dense-Mem may use search freshness.",
			Rationale:       "Active and candidate inputs point at a possible durable dependency.",
			WhatIf:          "What if the connection needs independent confirmation?",
			PossibleOutcome: "Collect independent evidence before accepting it.",
		}},
	}
	metrics := observability.NewPrometheusMetrics()
	svc := New(Dependencies{
		Store:     repo,
		Generator: generator,
		Metrics:   metrics,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
		Now:       func() time.Time { return time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC) },
	})

	result, err := svc.RunCycle(dreamTestContext(teamID, ownerID), "ignored-profile", RunCycleRequest{Manual: true})

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 1, generator.calls)
	require.Len(t, generator.lastReq.Paths, 1)
	require.Len(t, repo.upserts, 1)
	assert.Equal(t, "provider", repo.upserts[0].GeneratorKind)
	assert.Equal(t, "provider-canonical", repo.upserts[0].GeneratorVersion)
	assert.Equal(t, subjectID, repo.upserts[0].SubjectEntityID)
	assert.Equal(t, "uses", repo.upserts[0].PredicateKey)
	assert.Equal(t, objectID, repo.upserts[0].ObjectEntityID)
	assert.Len(t, repo.upserts[0].SourceRefs, 2)
	assert.Equal(t, 2, repo.upserts[0].SourceVersions[activeSourceID])
	assert.Equal(t, 4, repo.upserts[0].SourceVersions[candidateSourceID])
	assert.NotEmpty(t, repo.upserts[0].ContentHash)
	metricText := dreamMetricsText(t, metrics)
	require.Contains(t, metricText, `densemem_dream_cycle_attempts_total{lane="graph",status="completed"} 1`)
	require.Contains(t, metricText, `densemem_dream_provider_attempts_total{outcome="ok",stage="graph_generation"} 1`)
}

func TestDreamCycleMetricsClassifyContextCancellationAndPreserveProviderTimeouts(t *testing.T) {
	metrics := observability.NewPrometheusMetrics()
	svc := New(Dependencies{Metrics: metrics}).(*service)
	providerTimeout := &modelprovider.TimeoutError{Provider: "fixture", Message: "provider request timed out or was canceled"}
	failed := &RunCycleResult{Status: "error"}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		lane      string
		operation string
	}{
		{lane: string(domain.DreamLaneGraph), operation: "dream_graph"},
		{lane: string(domain.DreamLaneEvidenceDiscovery), operation: "dream_evidence"},
	} {
		svc.recordDreamCycleMetrics(cancelledCtx, test.lane, time.Now(), failed, providerTimeout)
		svc.recordDreamRecovery(test.operation, dreamRecoveryOutcome(cancelledCtx, failed, providerTimeout))
	}

	svc.recordDreamCycleMetrics(context.Background(), string(domain.DreamLaneGraph), time.Now(), failed, providerTimeout)
	svc.recordDreamRecovery("dream_graph", dreamRecoveryOutcome(context.Background(), failed, providerTimeout))

	completedCtx, cancelCompleted := context.WithCancel(context.Background())
	cancelCompleted()
	completed := &RunCycleResult{Status: "completed"}
	svc.recordDreamCycleMetrics(completedCtx, string(domain.DreamLaneGraph), time.Now(), completed, nil)
	svc.recordDreamRecovery("dream_graph", dreamRecoveryOutcome(completedCtx, completed, nil))

	metricText := dreamMetricsText(t, metrics)
	require.Contains(t, metricText, `densemem_dream_cycle_attempts_total{lane="graph",status="cancelled"} 1`)
	require.Contains(t, metricText, `densemem_dream_cycle_attempts_total{lane="evidence_discovery",status="cancelled"} 1`)
	require.Contains(t, metricText, `densemem_dream_cycle_attempts_total{lane="graph",status="failed"} 1`)
	require.Contains(t, metricText, `densemem_dream_cycle_attempts_total{lane="graph",status="completed"} 1`)
	require.Contains(t, metricText, `densemem_logical_operation_recoveries_total{operation="dream_evidence",outcome="cancelled"} 1`)
	require.Contains(t, metricText, `densemem_logical_operation_recoveries_total{operation="dream_graph",outcome="cancelled"} 1`)
	require.Contains(t, metricText, `densemem_logical_operation_recoveries_total{operation="dream_graph",outcome="failed"} 1`)
	require.Contains(t, metricText, `densemem_logical_operation_recoveries_total{operation="dream_graph",outcome="succeeded"} 1`)
}
