package service

import "context"

type ProgressStage string

const (
	ProgressPreparingIntake    ProgressStage = "preparing_intake"
	ProgressCheckingDuplicates ProgressStage = "checking_duplicates"
	ProgressAssessingEvidence  ProgressStage = "assessing_evidence"
	ProgressPreparingSearch    ProgressStage = "preparing_search"
	ProgressCommitting         ProgressStage = "committing"
)

type ProgressObserver func(context.Context, ProgressStage)

type progressObserverContextKey struct{}

func WithProgressObserver(ctx context.Context, observer ProgressObserver) context.Context {
	return context.WithValue(ctx, progressObserverContextKey{}, observer)
}

func ReportProgress(ctx context.Context, stage ProgressStage) {
	if observer, _ := ctx.Value(progressObserverContextKey{}).(ProgressObserver); observer != nil {
		observer(ctx, stage)
	}
}
