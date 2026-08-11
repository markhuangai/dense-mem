package observability

import (
	"context"
	"testing"
)

func TestNoopDiscoverabilityMetricsNeverPanics(t *testing.T) {
	m := NoopDiscoverabilityMetrics()
	m.ObserveEmbeddingLatency(10, "ok")
	m.IncEmbeddingError("timeout")
	m.ObserveRecallLatency(5)
	m.ObserveRecall(5, 2, "ok")
	m.ObserveRecallFeedback(RecallFeedback{Used: true, AnswerSupported: true, Quality: "high"})
	m.ObserveDreamFeedback(DreamFeedback{Decision: "reinforce", Outcome: "ok", FromStatus: "proposed"})
	m.ObserveConflictReviewDuration(1, "completed")
	m.IncVerifyVerdict("verified")
	metrics, ok := m.(SubmissionQuarantineMetrics)
	if !ok {
		t.Fatal("noop metrics does not implement SubmissionQuarantineMetrics")
	}
	metrics.IncSubmissionQuarantinePurgeFailure()
}

func TestInMemoryDiscoverabilityMetricsRecordsActiveSignals(t *testing.T) {
	m := NewInMemoryDiscoverabilityMetrics()
	m.ObserveEmbeddingLatency(123.4, "ok")
	m.IncEmbeddingError("timeout")
	m.ObserveRecallLatency(42)
	m.ObserveRecall(7.5, 3, "ok")
	m.ObserveRecallFeedback(RecallFeedback{
		Used:            true,
		AnswerSupported: false,
		Quality:         "medium",
		MissingContext:  true,
	})
	m.ObserveDreamFeedback(DreamFeedback{Decision: "confirm_true", Outcome: "ok", FromStatus: "proposed"})
	m.ObserveDreamFeedback(DreamFeedback{Decision: "invalid", Outcome: "invalid", FromStatus: "invalid"})
	m.ObserveConflictReviewDuration(2.5, "completed")
	m.IncVerifyVerdict("verified")
	m.IncSubmissionQuarantinePurgeFailure()
	m.ObserveEmbeddingReconciliationRun("completed")
	m.ObserveEmbeddingReconciliationCanary("succeeded")
	m.ObserveEmbeddingReconciliationJobs("requeued", "evidence", "transient", "provider_timeout", 2)
	m.ObserveEmbeddingReconciliationJobs("ignored", "evidence", "transient", "provider_timeout", 0)
	m.ObserveEmbeddingReconciliationDuration(1.25, "completed")

	if got := m.EmbeddingSamples(); len(got) != 1 || got[0].DurationMs != 123.4 || got[0].Outcome != "ok" {
		t.Fatalf("embedding samples = %+v", got)
	}
	if got := m.EmbeddingErrorCount("timeout"); got != 1 {
		t.Fatalf("embedding timeout count = %d; want 1", got)
	}
	if got := m.RecallLatencies(); len(got) != 2 || got[0] != 42 || got[1] != 7.5 {
		t.Fatalf("recall latencies = %+v", got)
	}
	if got := m.RecallSamples(); len(got) != 1 || got[0].ResultCount != 3 || got[0].Outcome != "ok" {
		t.Fatalf("recall samples = %+v", got)
	}
	if got := m.RecallFeedbackSamples(); len(got) != 1 || got[0].Quality != "medium" || got[0].QualityScore != 0.5 || !got[0].MissingContext {
		t.Fatalf("recall feedback = %+v", got)
	}
	if got := m.DreamFeedbackSamples(); len(got) != 2 || got[0].Decision != "confirm_true" || got[1].Decision != unknownMetricLabel {
		t.Fatalf("dream feedback = %+v", got)
	}
	if got := m.ConflictReviewSamples(); len(got) != 1 || got[0].Seconds != 2.5 || got[0].Outcome != "completed" {
		t.Fatalf("conflict review samples = %+v", got)
	}
	if got := m.VerifyVerdictCount("verified"); got != 1 {
		t.Fatalf("verified verdict count = %d; want 1", got)
	}
	if got := m.SubmissionQuarantinePurgeFailureCount(); got != 1 {
		t.Fatalf("quarantine purge failure count = %d; want 1", got)
	}
	if got := m.EmbeddingReconciliationRunCount("completed"); got != 1 {
		t.Fatalf("reconciliation run count = %d; want 1", got)
	}
	if got := m.EmbeddingReconciliationCanaryCount("succeeded"); got != 1 {
		t.Fatalf("reconciliation canary count = %d; want 1", got)
	}
	if got := m.reconciliationJobs["requeued:evidence:transient:provider_timeout"]; got != 2 {
		t.Fatalf("reconciliation jobs = %d; want 2", got)
	}
	if _, ok := m.reconciliationJobs["ignored:evidence:transient:provider_timeout"]; ok {
		t.Fatal("zero-count reconciliation job observation was recorded")
	}
	if len(m.reconciliationDurations) != 1 || m.reconciliationDurations[0] != 1.25 {
		t.Fatalf("reconciliation durations = %#v", m.reconciliationDurations)
	}
}

func TestRecordDiscoverabilityMetricsUsesUnscopedRecorder(t *testing.T) {
	metrics := NewInMemoryDiscoverabilityMetrics()
	ctx := context.Background()

	RecordEmbeddingLatency(ctx, metrics, "embed-model", 12, "ok")
	RecordEmbeddingError(ctx, metrics, "embed-model", "timeout")
	RecordEmbeddingTokens(ctx, metrics, "embed-model", 3, 5)
	RecordVerifierLatency(ctx, metrics, "verify-model", 9, "ok")
	RecordVerifierTokens(ctx, metrics, "verify-model", 2, 3, 5)
	RecordVerifyVerdict(ctx, metrics, "verify-model", "verified")
	RecordRecallLatency(ctx, metrics, 7)
	RecordRecall(ctx, metrics, 8, 2, "ok")
	RecordRecallFeedback(ctx, metrics, RecallFeedback{Used: true, Quality: "high"})
	RecordDreamFeedback(ctx, metrics, DreamFeedback{Decision: "confirm_true", Outcome: "ok", FromStatus: "proposed"})
	RecordConflictReviewDuration(ctx, metrics, 1.5, "completed")
	RecordConflictReviewDuration(ctx, metrics, -1, "ignored")

	if got := metrics.EmbeddingSamples(); len(got) != 1 || got[0].DurationMs != 12 {
		t.Fatalf("embedding samples = %+v", got)
	}
	if got := metrics.EmbeddingErrorCount("timeout"); got != 1 {
		t.Fatalf("embedding errors = %d, want 1", got)
	}
	if got := metrics.RecallSamples(); len(got) != 1 || got[0].ResultCount != 2 {
		t.Fatalf("recall samples = %+v", got)
	}
	if got := metrics.RecallFeedbackSamples(); len(got) != 1 || got[0].Quality != "high" {
		t.Fatalf("recall feedback = %+v", got)
	}
	if got := metrics.DreamFeedbackSamples(); len(got) != 1 || got[0].Decision != "confirm_true" {
		t.Fatalf("dream feedback = %+v", got)
	}
	if got := metrics.ConflictReviewSamples(); len(got) != 1 || got[0].Seconds != 1.5 {
		t.Fatalf("conflict review samples = %+v", got)
	}
	if got := metrics.VerifyVerdictCount("verified"); got != 1 {
		t.Fatalf("verify verdicts = %d, want 1", got)
	}
}

func TestInMemoryDiscoverabilityMetricsIsConcurrentSafe(t *testing.T) {
	m := NewInMemoryDiscoverabilityMetrics()
	done := make(chan struct{})
	for i := 0; i < 50; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			m.ObserveEmbeddingLatency(1, "ok")
			m.IncEmbeddingError("timeout")
			m.ObserveRecall(1, 1, "ok")
			m.ObserveRecallFeedback(RecallFeedback{Quality: "high"})
			m.ObserveDreamFeedback(DreamFeedback{Decision: "reinforce", Outcome: "ok", FromStatus: "proposed"})
			m.ObserveConflictReviewDuration(1, "completed")
			m.IncVerifyVerdict("verified")
		}()
	}
	for i := 0; i < 50; i++ {
		<-done
	}
	if got := len(m.EmbeddingSamples()); got != 50 {
		t.Fatalf("embedding samples = %d; want 50", got)
	}
	if got := len(m.ConflictReviewSamples()); got != 50 {
		t.Fatalf("conflict review samples = %d; want 50", got)
	}
	if got := m.VerifyVerdictCount("verified"); got != 50 {
		t.Fatalf("verified verdict count = %d; want 50", got)
	}
}
