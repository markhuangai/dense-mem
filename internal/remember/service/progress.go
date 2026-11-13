package service

import (
	"context"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type ProgressStage string

const (
	ProgressPreparingIntake        ProgressStage = "preparing_intake"
	ProgressCheckingDuplicates     ProgressStage = "checking_duplicates"
	ProgressAssessingEvidence      ProgressStage = "assessing_evidence"
	ProgressPreparingSearch        ProgressStage = "preparing_search"
	ProgressCommitting             ProgressStage = "committing"
	ProgressValidatingInput        ProgressStage = "validating_input"
	ProgressCheckingReplay         ProgressStage = "checking_replay"
	ProgressReturningReplay        ProgressStage = "returning_replay"
	ProgressPreparingBoundaries    ProgressStage = "preparing_boundaries"
	ProgressPartitioning           ProgressStage = "partitioning"
	ProgressValidatingPartitions   ProgressStage = "validating_partitions"
	ProgressRegeneratingPartitions ProgressStage = "regenerating_partitions"
	ProgressDuplicateEmbeddings    ProgressStage = "duplicate_embeddings"
	ProgressValidatingAssessment   ProgressStage = "validating_assessment"
	ProgressRegeneratingAssessment ProgressStage = "regenerating_assessment"
	ProgressPreparingResult        ProgressStage = "preparing_result"
	ProgressRecordingFailure       ProgressStage = "recording_failure"
)

type ProgressObserver func(context.Context, ProgressStage)

func WithProgressObserver(ctx context.Context, observer ProgressObserver) context.Context {
	return requestctx.WithProgressObserver(ctx, func(ctx context.Context, update requestctx.ProgressUpdate) {
		observer(ctx, ProgressStage(update.Stage))
	})
}

func ReportProgress(ctx context.Context, stage ProgressStage) {
	requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: string(stage)})
}
