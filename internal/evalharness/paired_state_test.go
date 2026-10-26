package evalharness

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPairImportResumeRequiresExactScopeAndTerminalEvidence(t *testing.T) {
	scope := PairScope{TeamID: "team-a", OwnerID: "owner-a", SpaceID: "shared-a", Generation: 2}
	corpus := []CorpusItem{{SourceDocID: "one", Content: "first"}, {SourceDocID: "two", Content: "second"}}
	hash, err := pairRequestHash(corpus[0])
	require.NoError(t, err)
	rows := []PairImportAttempt{{Scope: scope, SourceDocID: "one", AttemptID: "attempt-one", RequestHash: hash, Outcome: "completed", EvidenceIDs: []string{"evidence-one"}, ContentHashes: []string{pairBytesHash([]byte("first"))}, Live: true}}
	result, err := EvaluatePairImport(scope, corpus, rows, false)
	require.NoError(t, err)
	require.Equal(t, []string{"one"}, result.CompletedSourceIDs)
	require.Equal(t, []string{"two"}, result.PendingSourceIDs)
	require.False(t, result.Complete)

	for _, change := range []func(*PairImportAttempt){
		func(row *PairImportAttempt) { row.Scope.OwnerID = "owner-b" },
		func(row *PairImportAttempt) { row.Scope.Generation++ },
		func(row *PairImportAttempt) { row.Outcome = "failed" },
		func(row *PairImportAttempt) { row.Live = false },
		func(row *PairImportAttempt) { row.RequestHash = "" },
		func(row *PairImportAttempt) { row.RequestHash = "sha256:other-request" },
		func(row *PairImportAttempt) { row.SourceDocID = "unknown" },
	} {
		row := rows[0]
		change(&row)
		_, err := EvaluatePairImport(scope, corpus, []PairImportAttempt{row}, false)
		require.Error(t, err)
	}
	_, err = EvaluatePairImport(scope, corpus, rows, true)
	require.ErrorContains(t, err, "fresh")
	_, err = EvaluatePairImport(scope, corpus, append(rows, rows[0]), false)
	require.ErrorContains(t, err, "duplicate")
}

func drainedPairObservation() PairObservation {
	return PairObservation{
		ObservedAt: time.Now().UTC(), Scope: PairScope{TeamID: "a", OwnerID: "b", SpaceID: "c", Generation: 1},
		Enabled: true, DiscoveryComplete: true, CoverageComplete: true,
		SearchConverged: true, ProjectionCoverageComplete: true,
		CanonicalHash: "sha256:canonical", SourceHash: "sha256:source", WindowID: "window",
		Policy: PairCapacity{Global: 8, Team: 4, ContextWindowTokens: 300000},
	}
}

func TestPairReadinessSeparatesUnknownUsageFromActiveWork(t *testing.T) {
	observation := drainedPairObservation()
	observation.Usage.Unavailable = 1
	observation.ReservedInput = 9913
	observation.ReservedOutput = 65536
	result, err := EvaluatePairReadiness("adr0014", observation, false)
	require.NoError(t, err)
	require.True(t, result.Ready)
	require.True(t, result.Complete)
	legacy, err := EvaluatePairReadiness("legacy", observation, false)
	require.NoError(t, err)
	require.False(t, legacy.Ready)

	for _, change := range []func(*PairObservation){
		func(o *PairObservation) { o.ActiveWork = 1 },
		func(o *PairObservation) { o.ActiveBatches = 1 },
		func(o *PairObservation) { o.ActivePreparations = 1 },
		func(o *PairObservation) { o.ActiveProjections = 1 },
		func(o *PairObservation) { o.Usage.Unreconciled = 1 },
		func(o *PairObservation) { o.Pending = 1 },
		func(o *PairObservation) { o.SearchConverged = false },
		func(o *PairObservation) { o.PendingProjections = 1 },
		func(o *PairObservation) { o.DiscoveryComplete = false },
	} {
		o := observation
		change(&o)
		result, err := EvaluatePairReadiness("adr0014", o, false)
		require.NoError(t, err)
		require.False(t, result.Ready)
	}
}

func TestPairReadinessRetainsTerminalFailuresAndRequiresFreeze(t *testing.T) {
	o := drainedPairObservation()
	o.Failed, o.Ambiguous, o.FailedProjections = 1, 1, 1
	o.CoverageComplete, o.ProjectionCoverageComplete = false, false
	result, err := EvaluatePairReadiness("adr0014", o, false)
	require.NoError(t, err)
	require.True(t, result.Ready)
	require.False(t, result.Complete)
	require.NotEmpty(t, result.IncompleteReasons)
	result, err = EvaluatePairReadiness("adr0014", o, true)
	require.NoError(t, err)
	require.False(t, result.Ready)
	o.Paused = true
	result, err = EvaluatePairReadiness("adr0014", o, true)
	require.NoError(t, err)
	require.True(t, result.Ready)
	o.Enabled = false
	_, err = EvaluatePairReadiness("adr0014", o, true)
	require.ErrorContains(t, err, "enabled")
}

func TestPairAttemptAccountingUsesFullReceiptIdentity(t *testing.T) {
	start := time.Date(2026, 10, 26, 1, 0, 0, 0, time.UTC)
	first := PairProviderAttempt{TeamID: "a", SpaceID: "s", Generation: 1, OperationKey: "maintenance:batch-a", AssessmentID: "assessment", Number: 1, AdmittedAt: start, ReconciledAt: start.Add(4 * time.Second), Terminal: true}
	second := first
	second.OperationKey = "maintenance:batch-b"
	second.AdmittedAt, second.ReconciledAt = start.Add(2*time.Second), start.Add(6*time.Second)
	second.UsageAvailable, second.InputTokens, second.OutputTokens = true, 10, 20
	report, err := MeasurePairAttempts([]PairProviderAttempt{first, second})
	require.NoError(t, err)
	require.Equal(t, 2, report.Attempts)
	require.Equal(t, 1, report.UnknownUsage)
	require.Equal(t, 2, report.MaximumOverlap)
	require.Equal(t, 6.0, report.CoveredSeconds)
	require.Equal(t, 8.0, report.AttemptSeconds)
	_, err = MeasurePairAttempts([]PairProviderAttempt{first, first})
	require.ErrorContains(t, err, "duplicate")
	second.ReconciledAt = start.Add(-time.Second)
	report, err = MeasurePairAttempts([]PairProviderAttempt{first, second})
	require.NoError(t, err)
	require.Equal(t, "unavailable", report.IntervalStatus)
	require.Equal(t, 1, report.InvalidIntervals)
	require.Zero(t, report.MaximumOverlap)
}
