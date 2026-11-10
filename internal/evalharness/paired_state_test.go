package evalharness

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func boolPointer(value bool) *bool           { return &value }
func timePointer(value time.Time) *time.Time { return &value }

func pairFixtureImportAttempt(t *testing.T, scope PairScope, item CorpusItem, id string, created time.Time) PairImportAttempt {
	t.Helper()
	hash, err := pairRequestHash(item)
	require.NoError(t, err)
	return PairImportAttempt{Scope: scope, SourceDocID: item.SourceDocID, AttemptID: id, RequestHash: hash, ContractVersion: "dense-mem.v3.0.0", Outcome: "completed", EvidenceIDs: []string{"evidence-" + id}, ContentHashes: []string{pairBytesHash([]byte(item.Content))}, Live: true, IdempotencyKey: "eval:" + item.SourceDocID, Retryable: boolPointer(false), CreatedAt: timePointer(created)}
}

func TestEvidenceOnlyUnitPairImportRequiresExactScopeAndTerminalEvidence(t *testing.T) {
	scope := PairScope{TeamID: "a", OwnerID: "b", SpaceID: "s", Generation: 2}
	corpus := []CorpusItem{{SourceDocID: "one", Content: "first"}, {SourceDocID: "two", Content: "second"}}
	row := pairFixtureImportAttempt(t, scope, corpus[0], "one", time.Now())
	result, err := EvaluatePairImport(scope, corpus, []PairImportAttempt{row}, false)
	require.NoError(t, err)
	require.Equal(t, []string{"one"}, result.CompletedSourceIDs)
	require.Equal(t, []string{"two"}, result.PendingSourceIDs)
	require.False(t, result.Complete)
	for _, change := range []func(*PairImportAttempt){
		func(r *PairImportAttempt) { r.Scope.OwnerID = "foreign" },
		func(r *PairImportAttempt) { r.Scope.Generation++ },
		func(r *PairImportAttempt) { r.Live = false },
		func(r *PairImportAttempt) { r.RequestHash = "changed" },
		func(r *PairImportAttempt) { r.ContractVersion = "dense-mem.v2.7.0" },
		func(r *PairImportAttempt) { r.SourceDocID = "unknown" },
		func(r *PairImportAttempt) { r.Retryable = nil },
		func(r *PairImportAttempt) { r.CreatedAt = nil },
		func(r *PairImportAttempt) { r.IdempotencyKey = "different" },
	} {
		changed := row
		change(&changed)
		_, err := EvaluatePairImport(scope, corpus, []PairImportAttempt{changed}, false)
		require.Error(t, err)
	}
	_, err = EvaluatePairImport(scope, corpus, []PairImportAttempt{row}, true)
	require.ErrorContains(t, err, "fresh")
	_, err = EvaluatePairImport(scope, corpus, []PairImportAttempt{row, row}, false)
	require.ErrorContains(t, err, "duplicate")
}

func TestEvidenceOnlyUnitPairImportSeparatesRetainedCRequestHash(t *testing.T) {
	scope := PairScope{TeamID: "a", OwnerID: "b", SpaceID: "s", Generation: 1}
	item := CorpusItem{SourceDocID: "document", Content: "Exact café Evidence"}
	row := pairFixtureImportAttempt(t, scope, item, "one", time.Now())
	current := row.RequestHash
	retained, err := retainedCEvidenceRequestHash(item)
	require.NoError(t, err)
	require.NotEqual(t, current, retained)
	release := PairRelease{Mode: PairModeRetainedBaseline}
	row.ContractVersion, row.RequestHash = "dense-mem.v2.7.0", retained
	row.CreatedAt, row.Retryable = nil, nil
	progress, err := EvaluatePairImport(scope, []CorpusItem{item}, []PairImportAttempt{row}, false, release)
	require.NoError(t, err)
	require.True(t, progress.Complete)
	row.RequestHash = current
	_, err = EvaluatePairImport(scope, []CorpusItem{item}, []PairImportAttempt{row}, false, release)
	require.ErrorContains(t, err, "exact request")
	row = pairFixtureImportAttempt(t, scope, item, "one", time.Now())
	row.RequestHash = retained
	_, err = EvaluatePairImport(scope, []CorpusItem{item}, []PairImportAttempt{row}, false)
	require.ErrorContains(t, err, "exact request")
}

func TestEvidenceOnlyUnitPairImportAllowsOneSupportedRecovery(t *testing.T) {
	scope := PairScope{TeamID: "a", OwnerID: "b", SpaceID: "s", Generation: 1}
	item := CorpusItem{SourceDocID: "one", Content: "Exact original Evidence"}
	for _, retryable := range []bool{true, false} {
		t.Run(fmt.Sprint(retryable), func(t *testing.T) {
			created := time.Now()
			failed := pairFixtureImportAttempt(t, scope, item, "failed", created)
			failed.Outcome, failed.Live = "failed", false
			failed.EvidenceIDs, failed.ContentHashes = nil, nil
			failed.Retryable = boolPointer(retryable)
			completed := pairFixtureImportAttempt(t, scope, item, "completed", created.Add(time.Second))
			if !retryable {
				completed.ResubmissionOf = failed.AttemptID
				completed.IdempotencyKey += ":resubmit1"
			}
			for _, rows := range [][]PairImportAttempt{{failed, completed}, {completed, failed}} {
				progress, err := EvaluatePairImport(scope, []CorpusItem{item}, rows, false)
				require.NoError(t, err)
				require.True(t, progress.Complete)
			}
			third := completed
			third.AttemptID = "third"
			third.CreatedAt = timePointer(created.Add(2 * time.Second))
			_, err := EvaluatePairImport(scope, []CorpusItem{item}, []PairImportAttempt{failed, completed, third}, false)
			require.Error(t, err)
			_, err = EvaluatePairImport(scope, []CorpusItem{item}, []PairImportAttempt{failed}, false)
			require.ErrorContains(t, err, "automatic retry is forbidden")
			for _, change := range []func(*PairImportAttempt){
				func(r *PairImportAttempt) { r.Scope.OwnerID = "foreign" },
				func(r *PairImportAttempt) { r.RequestHash = "changed" },
				func(r *PairImportAttempt) { r.ResubmissionOf = "unknown"; r.IdempotencyKey = "eval:one:resubmit1" },
				func(r *PairImportAttempt) { r.AttemptID = failed.AttemptID },
				func(r *PairImportAttempt) { r.IdempotencyKey = "unsupported-key" },
			} {
				changed := completed
				change(&changed)
				_, err := EvaluatePairImport(scope, []CorpusItem{item}, []PairImportAttempt{failed, changed}, false)
				require.Error(t, err)
			}
			if !retryable {
				changed := completed
				changed.ResubmissionOf = ""
				changed.IdempotencyKey = "eval:one"
				_, err := EvaluatePairImport(scope, []CorpusItem{item}, []PairImportAttempt{failed, changed}, false)
				require.ErrorContains(t, err, "linked fresh-key")
			}
		})
	}
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
