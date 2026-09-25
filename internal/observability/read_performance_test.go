package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestReadPerformanceRecordsNestedStageDeltas(t *testing.T) {
	metrics := NewInMemoryDiscoverabilityMetrics()
	ctx := WithReadPerformance(context.Background(), metrics)
	totalCtx, total := StartReadStage(ctx, ReadOperationEvidenceRecall, ReadStageTotal)
	RecordReadSQLStatement(totalCtx)
	fullTextCtx, fullText := StartReadStage(totalCtx, ReadOperationEvidenceRecall, ReadStageFullText)
	RecordReadSQLStatement(fullTextCtx)
	RecordReadSQLStatement(fullTextCtx)
	fullText.Finish(nil, 0)
	total.Finish(nil, 3)

	samples := metrics.ReadStageSamples()
	if len(samples) != 2 {
		t.Fatalf("read stage sample count = %d, want 2", len(samples))
	}
	if got, want := samples[0], (ReadStageSample{
		Operation: ReadOperationEvidenceRecall,
		Stage:     ReadStageFullText,
		Outcome:   ReadOutcomeSuccess,
		Items:     0,
	}); got.Operation != want.Operation || got.Stage != want.Stage || got.Outcome != want.Outcome || got.Items != want.Items || got.Duration <= 0 {
		t.Fatalf("full-text read sample = %#v, want %#v with positive duration", got, want)
	}
	if got, want := samples[1], (ReadStageSample{
		Operation: ReadOperationEvidenceRecall,
		Stage:     ReadStageTotal,
		Outcome:   ReadOutcomeSuccess,
		Items:     3,
	}); got.Operation != want.Operation || got.Stage != want.Stage || got.Outcome != want.Outcome || got.Items != want.Items || got.Duration <= 0 {
		t.Fatalf("total read sample = %#v, want %#v with positive duration", got, want)
	}
	if got := metrics.ReadSQLStatementCount(ReadOperationEvidenceRecall, ReadStageTotal); got != 1 {
		t.Fatalf("total-stage statement count = %d, want 1", got)
	}
	if got := metrics.ReadSQLStatementCount(ReadOperationEvidenceRecall, ReadStageFullText); got != 2 {
		t.Fatalf("full-text statement count = %d, want 2", got)
	}
}

func TestReadPerformanceClassifiesFailuresAndRequiresBoundedLabels(t *testing.T) {
	metrics := NewInMemoryDiscoverabilityMetrics()
	ctx := WithReadPerformance(context.Background(), metrics)
	for _, tc := range []struct {
		cause error
		want  ReadOutcome
	}{
		{cause: context.Canceled, want: ReadOutcomeCancellation},
		{cause: context.DeadlineExceeded, want: ReadOutcomeDeadlineExceeded},
		{cause: errReadPerformanceTest, want: ReadOutcomeError},
	} {
		_, stage := StartReadStage(ctx, ReadOperationRelationshipRecall, ReadStageHydration)
		stage.Finish(tc.cause, 0)
	}
	metrics.ObserveReadStage(ReadOperation("sentinel team id"), ReadStageSelection, ReadOutcomeSuccess, time.Millisecond, 1)
	metrics.ObserveReadStage(ReadOperationEvidenceRecall, ReadStage("sentinel query text"), ReadOutcomeSuccess, time.Millisecond, 1)
	metrics.IncReadSQLStatement(ReadOperation("sentinel profile id"), ReadStageSelection)

	samples := metrics.ReadStageSamples()
	if len(samples) != 3 {
		t.Fatalf("read stage sample count = %d, want 3", len(samples))
	}
	for index, want := range []ReadOutcome{ReadOutcomeCancellation, ReadOutcomeDeadlineExceeded, ReadOutcomeError} {
		if got := samples[index].Outcome; got != want {
			t.Errorf("read stage outcome %d = %q, want %q", index, got, want)
		}
	}
	if got := metrics.ReadSQLStatementCount(ReadOperation("sentinel profile id"), ReadStageSelection); got != 0 {
		t.Fatalf("unbounded SQL-statement labels recorded %d statements, want 0", got)
	}
}

func TestReadPerformancePrometheusOnlyExposesExecutedBoundedStages(t *testing.T) {
	metrics := NewPrometheusMetrics()
	if body := scrapePrometheusMetrics(t, metrics); strings.Contains(body, "densemem_read_stage_") {
		t.Fatalf("unexecuted read stages were exposed\n%s", body)
	}

	ctx := WithReadPerformance(context.Background(), metrics)
	stageCtx, stage := StartReadStage(ctx, ReadOperationEvidenceRecall, ReadStageFullText)
	RecordReadSQLStatement(stageCtx)
	stage.Finish(nil, 0)
	metrics.ObserveReadStage(ReadOperation("sentinel query text"), ReadStageFullText, ReadOutcomeSuccess, time.Millisecond, 1)

	body := scrapePrometheusMetrics(t, metrics)
	for _, want := range []string{
		`densemem_read_stage_duration_seconds_count{operation="evidence_recall",outcome="success",stage="full_text"} 1`,
		`densemem_read_sql_statements_total{operation="evidence_recall",stage="full_text"} 1`,
		`densemem_read_stage_items_count{operation="evidence_recall",stage="full_text"} 1`,
		`densemem_read_stage_items_sum{operation="evidence_recall",stage="full_text"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("read performance metrics missing %q", want)
		}
	}
	if strings.Contains(body, "sentinel query text") {
		t.Fatalf("read metrics exposed an unbounded label\n%s", body)
	}
}

var errReadPerformanceTest = errors.New("injected stage failure")
