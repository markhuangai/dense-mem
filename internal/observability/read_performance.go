package observability

import (
	"context"
	"errors"
	"time"
)

type ReadOperation string

const (
	ReadOperationEvidenceRecall     ReadOperation = "evidence_recall"
	ReadOperationRelationshipRecall ReadOperation = "relationship_recall"
	ReadOperationSearchContract     ReadOperation = "search_contract"
	ReadOperationSearchReadiness    ReadOperation = "search_readiness"
	ReadOperationFullTextSearch     ReadOperation = "full_text_search"
	ReadOperationVectorSearch       ReadOperation = "vector_search"
)

type ReadStage string

const (
	ReadStageTotal            ReadStage = "total"
	ReadStageContract         ReadStage = "contract"
	ReadStageReadiness        ReadStage = "readiness"
	ReadStageFullText         ReadStage = "full_text"
	ReadStageVector           ReadStage = "vector"
	ReadStageExpansion        ReadStage = "expansion"
	ReadStageFusion           ReadStage = "fusion"
	ReadStageHydration        ReadStage = "hydration"
	ReadStageSelection        ReadStage = "selection"
	ReadStageConflicts        ReadStage = "conflicts"
	ReadStageTransactionSetup ReadStage = "transaction_setup"
)

type ReadOutcome string

const (
	ReadOutcomeSuccess          ReadOutcome = "success"
	ReadOutcomeError            ReadOutcome = "error"
	ReadOutcomeCancellation     ReadOutcome = "cancellation"
	ReadOutcomeDeadlineExceeded ReadOutcome = "deadline_exceeded"
)

type readPerformanceContextKey struct{}

type readStatementKey struct {
	operation ReadOperation
	stage     ReadStage
}

// ReadPerformanceMetrics is an optional, bounded telemetry surface for
// complete Search and Recall reads.
type ReadPerformanceMetrics interface {
	ObserveReadStage(operation ReadOperation, stage ReadStage, outcome ReadOutcome, duration time.Duration, items int)
	IncReadSQLStatement(operation ReadOperation, stage ReadStage)
}

type readPerformanceContext struct {
	recorder  ReadPerformanceMetrics
	operation ReadOperation
	stage     ReadStage
}

type ReadStageObservation struct {
	recorder  ReadPerformanceMetrics
	operation ReadOperation
	stage     ReadStage
	started   time.Time
}

func (observation ReadStageObservation) Active() bool {
	return observation.recorder != nil && !observation.started.IsZero()
}

// WithReadPerformance attaches a read recorder without changing the required
// DiscoverabilityMetrics contract.
func WithReadPerformance(ctx context.Context, metrics DiscoverabilityMetrics) context.Context {
	if ctx == nil || metrics == nil {
		return ctx
	}
	recorder, ok := metrics.(ReadPerformanceMetrics)
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, readPerformanceContextKey{}, &readPerformanceContext{recorder: recorder})
}

// StartReadStage scopes statement attribution and returns a completion receipt
// for the full stage, including row decoding and final row-error checks.
func StartReadStage(ctx context.Context, operation ReadOperation, stage ReadStage) (context.Context, ReadStageObservation) {
	if ctx == nil {
		return ctx, ReadStageObservation{}
	}
	parent, ok := ctx.Value(readPerformanceContextKey{}).(*readPerformanceContext)
	if !ok || parent == nil || parent.recorder == nil {
		return ctx, ReadStageObservation{}
	}
	if operation == "" {
		operation = parent.operation
	}
	if !validReadOperation(operation) || !validReadStage(stage) {
		return ctx, ReadStageObservation{}
	}
	scoped := *parent
	scoped.operation = operation
	scoped.stage = stage
	stageCtx := context.WithValue(ctx, readPerformanceContextKey{}, &scoped)
	return stageCtx, ReadStageObservation{
		recorder:  parent.recorder,
		operation: operation,
		stage:     stage,
		started:   time.Now(),
	}
}

func (observation ReadStageObservation) Finish(err error, items int) {
	if observation.recorder == nil || observation.started.IsZero() {
		return
	}
	if items < 0 {
		items = 0
	}
	observation.recorder.ObserveReadStage(
		observation.operation,
		observation.stage,
		readOutcome(err),
		time.Since(observation.started),
		items,
	)
}

// RecordReadSQLStatement attributes one GORM statement to the innermost read
// stage. Transaction begin and commit calls do not pass through GORM Trace.
func RecordReadSQLStatement(ctx context.Context) {
	if ctx == nil {
		return
	}
	scope, ok := ctx.Value(readPerformanceContextKey{}).(*readPerformanceContext)
	if !ok || scope == nil || scope.recorder == nil || scope.operation == "" || scope.stage == "" {
		return
	}
	scope.recorder.IncReadSQLStatement(scope.operation, scope.stage)
}

func readOutcome(err error) ReadOutcome {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ReadOutcomeDeadlineExceeded
	case errors.Is(err, context.Canceled):
		return ReadOutcomeCancellation
	case err != nil:
		return ReadOutcomeError
	default:
		return ReadOutcomeSuccess
	}
}

func validReadOperation(operation ReadOperation) bool {
	switch operation {
	case ReadOperationEvidenceRecall, ReadOperationRelationshipRecall,
		ReadOperationSearchContract, ReadOperationSearchReadiness,
		ReadOperationFullTextSearch, ReadOperationVectorSearch:
		return true
	default:
		return false
	}
}

func validReadStage(stage ReadStage) bool {
	switch stage {
	case ReadStageTotal, ReadStageContract, ReadStageReadiness,
		ReadStageFullText, ReadStageVector, ReadStageExpansion,
		ReadStageFusion, ReadStageHydration, ReadStageSelection,
		ReadStageConflicts, ReadStageTransactionSetup:
		return true
	default:
		return false
	}
}

func validReadOutcome(outcome ReadOutcome) bool {
	switch outcome {
	case ReadOutcomeSuccess, ReadOutcomeError, ReadOutcomeCancellation, ReadOutcomeDeadlineExceeded:
		return true
	default:
		return false
	}
}
