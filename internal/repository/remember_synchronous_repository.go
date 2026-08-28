package repository

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const synchronousRememberWorkerID = "synchronous-remember"

type synchronousRememberCommitStageError struct {
	stage string
	err   error
}

func (e *synchronousRememberCommitStageError) Error() string {
	return "synchronous Remember commit " + e.stage + " failed"
}

func (e *synchronousRememberCommitStageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *synchronousRememberCommitStageError) SynchronousRememberCommitStage() string {
	if e == nil {
		return ""
	}
	return e.stage
}

func wrapSynchronousRememberCommitStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &synchronousRememberCommitStageError{stage: stage, err: err}
}

// SynchronousRememberCommitBuilder adapts already-validated provider output to
// the persisted IDs allocated inside the terminal transaction. It must perform
// no provider calls and no durable writes.
type SynchronousRememberCommitBuilder func(*CreateIngestResult, SubmissionAssessmentRunScope) (PersistSubmissionAssessmentInput, CommitSubmissionAssessmentInput, error)
type SynchronousRememberPublicResultBuilder func(*CreateIngestResult, *CommitSubmissionAssessmentResult) (map[string]any, error)

// SynchronousRememberCommitInput contains only request material whose provider
// work has already completed. CreateIngest is deliberately inside the final
// transaction: a timeout or malformed provider response leaves no canonical
// intake, placement, semantic, or search rows behind.
type SynchronousRememberCommitInput struct {
	CreateIngest      CreateIngestInput
	BuildCommit       SynchronousRememberCommitBuilder
	BuildPublicResult SynchronousRememberPublicResultBuilder
	Attempt           RememberAttemptRecordInput
	// InlineEmbeddings is populated only by the E2E synchronous Remember
	// processor after it has executed the request-owned embedding plan. A nil
	// value deliberately retains the existing background embedding path.
	InlineEmbeddings *SynchronousRememberEmbeddingResult
}

type SynchronousRememberCommitResult struct {
	Ingest     *CreateIngestResult
	Assessment *SubmissionAssessment
	Commit     *CommitSubmissionAssessmentResult
	Attempt    *RememberAttempt
}

type SynchronousRememberTerminalBuilder func(*CreateIngestResult, SubmissionAssessmentRunScope) (*PersistSubmissionAssessmentInput, CompleteSubmissionAssessmentInput, error)

type SynchronousRememberTerminalInput struct {
	CreateIngest      CreateIngestInput
	BuildTerminal     SynchronousRememberTerminalBuilder
	BuildPublicResult SynchronousRememberPublicResultBuilder
	Attempt           RememberAttemptRecordInput
}

// CommitSynchronousRemember writes compatibility placement lineage directly
// to its terminal state. The temporary processing state is transaction-local;
// other sessions cannot observe a queued or claimed synchronous submission.
func (r *LedgerRepositoryImpl) CommitSynchronousRemember(ctx context.Context, input SynchronousRememberCommitInput) (*SynchronousRememberCommitResult, error) {
	input.CreateIngest = normalizeCreateIngestInput(input.CreateIngest)
	input.Attempt = normalizeRememberAttemptRecord(input.Attempt)
	if err := validateSynchronousRememberCommitInput(input); err != nil {
		return nil, err
	}
	result := &SynchronousRememberCommitResult{}
	err := r.withAtomicRememberTx(ctx, input.CreateIngest.TeamID, input.CreateIngest.OwnerProfileID, func(txCtx context.Context) error {
		tx := transactionFromContext(txCtx)
		if err := lockRememberIdempotencyKeyInTx(txCtx, tx, input.Attempt.TeamID, input.Attempt.OwnerProfileID, input.Attempt.IdempotencyKey); err != nil {
			return wrapSynchronousRememberCommitStage("idempotency_lock", err)
		}
		if err := validateRememberFailureRetryInTx(txCtx, tx, input.Attempt.TeamID, input.Attempt.OwnerProfileID, input.Attempt.IdempotencyKey, input.Attempt.RequestHash, input.Attempt.MigratedRequestHash); err != nil {
			return wrapSynchronousRememberCommitStage("failed_attempt_fence", err)
		}
		if replay, err := loadTerminalRememberAttemptInTx(txCtx, tx, input.Attempt.TeamID, input.Attempt.OwnerProfileID, input.Attempt.IdempotencyKey, input.Attempt.RequestHash, input.Attempt.MigratedRequestHash); err != nil {
			return wrapSynchronousRememberCommitStage("replay_load", err)
		} else if replay != nil {
			result.Attempt = replay
			return ErrRememberReplay
		}

		created, err := r.CreateIngest(txCtx, input.CreateIngest)
		if err != nil {
			return wrapSynchronousRememberCommitStage("ingest", err)
		}
		if created.Existing {
			return ErrRememberReplay
		}
		result.Ingest = created
		scope, err := activateSynchronousRememberRun(txCtx, transactionFromContext(txCtx), created)
		if err != nil {
			return wrapSynchronousRememberCommitStage("activate", err)
		}
		assessmentInput, commitInput, err := input.BuildCommit(created, scope)
		if err != nil {
			return wrapSynchronousRememberCommitStage("assessment_build", err)
		}
		assessmentInput.TeamID, assessmentInput.OwnerProfileID = scope.TeamID, scope.OwnerProfileID
		assessmentInput.IngestID, assessmentInput.PlacementRunID = scope.IngestID, scope.PlacementRunID
		assessment, _, err := r.PersistSubmissionAssessment(txCtx, assessmentInput)
		if err != nil {
			return wrapSynchronousRememberCommitStage("assessment_persist", err)
		}
		result.Assessment = assessment
		commitInput.SubmissionAssessmentRunScope = scope
		bindSynchronousRememberAssessmentID(&commitInput, assessment.AssessmentID)
		if input.InlineEmbeddings != nil {
			txCtx = withSynchronousInlineEmbedding(txCtx, input.InlineEmbeddings)
		}
		committed, err := r.CommitSubmissionAssessment(txCtx, commitInput)
		if err != nil {
			return wrapSynchronousRememberCommitStage("semantic_commit", err)
		}
		if input.InlineEmbeddings != nil {
			if err := applySynchronousRememberEmbeddings(txCtx, transactionFromContext(txCtx), input.CreateIngest.TeamID, input.CreateIngest.OwnerProfileID, committed.SearchDocuments, *input.InlineEmbeddings); err != nil {
				return wrapSynchronousRememberCommitStage("inline_embeddings", err)
			}
			for index := range committed.SearchDocuments {
				if committed.SearchDocuments[index].SearchState == "pending" {
					committed.SearchDocuments[index].SearchState = "current"
				}
			}
		}
		result.Commit = committed
		if input.BuildPublicResult != nil {
			publicResult, err := input.BuildPublicResult(created, committed)
			if err != nil {
				return wrapSynchronousRememberCommitStage("public_result", err)
			}
			input.Attempt.PublicResult = publicResult
		}
		input.Attempt.Outcome = "completed"
		input.Attempt.EvidenceCount = len(created.Evidence)
		input.Attempt.RelationshipCount = len(commitInput.RelationshipResults)
		input.Attempt.DocumentCount = len(committed.SearchDocuments)
		if err := insertRememberAttemptInTx(txCtx, transactionFromContext(txCtx), input.Attempt); err != nil {
			return wrapSynchronousRememberCommitStage("attempt_record", err)
		}
		result.Attempt = &RememberAttempt{AttemptID: input.Attempt.AttemptID, RequestHash: input.Attempt.RequestHash, ContractVersion: input.Attempt.ContractVersion, Outcome: input.Attempt.Outcome, PublicResult: input.Attempt.PublicResult}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("repository: commit synchronous Remember: %w", err)
	}
	return result, nil
}

func bindSynchronousRememberAssessmentID(input *CommitSubmissionAssessmentInput, assessmentID string) {
	if input == nil {
		return
	}
	input.AssessmentID = assessmentID
	for index := range input.EntityResolutions {
		input.EntityResolutions[index].Resolution.AssessmentID = assessmentID
	}
	for index := range input.RelationshipObservations {
		input.RelationshipObservations[index].Observation.AssessmentID = assessmentID
	}
}

// CommitSynchronousRememberTerminal persists a post-provider rejected or
// quarantined request with compatibility placement rows that are terminal when
// they become visible. Admission quarantine must use
// RecordSynchronousRememberPreflightQuarantine instead.
func (r *LedgerRepositoryImpl) CommitSynchronousRememberTerminal(ctx context.Context, input SynchronousRememberTerminalInput) (*SynchronousRememberCommitResult, error) {
	input.CreateIngest = normalizeCreateIngestInput(input.CreateIngest)
	input.Attempt = normalizeRememberAttemptRecord(input.Attempt)
	if input.BuildTerminal == nil {
		return nil, errors.New("synchronous Remember terminal builder is required")
	}
	if err := validateCreateIngestInput(input.CreateIngest); err != nil {
		return nil, fmt.Errorf("synchronous Remember ingest: %w", err)
	}
	if input.CreateIngest.Status != string(domain.PlacementRunQueued) || !input.CreateIngest.TelemetryRemember {
		return nil, errors.New("synchronous Remember ingest must use queued remember compatibility lineage")
	}
	if input.Attempt.Outcome != "rejected" && input.Attempt.Outcome != "quarantined" {
		return nil, errors.New("synchronous Remember terminal outcome must be rejected or quarantined")
	}
	if input.Attempt.TeamID != input.CreateIngest.TeamID || input.Attempt.OwnerProfileID != input.CreateIngest.OwnerProfileID || input.Attempt.IdempotencyKey != input.CreateIngest.IdempotencyKey || input.Attempt.RequestHash != input.CreateIngest.RequestHash {
		return nil, errors.New("synchronous Remember attempt identity must match ingest")
	}
	if err := validateRememberAttemptRecord(input.Attempt); err != nil {
		return nil, err
	}
	result := &SynchronousRememberCommitResult{}
	err := r.withAtomicRememberTx(ctx, input.CreateIngest.TeamID, input.CreateIngest.OwnerProfileID, func(txCtx context.Context) error {
		tx := transactionFromContext(txCtx)
		if err := lockRememberIdempotencyKeyInTx(txCtx, tx, input.Attempt.TeamID, input.Attempt.OwnerProfileID, input.Attempt.IdempotencyKey); err != nil {
			return err
		}
		if err := validateRememberFailureRetryInTx(txCtx, tx, input.Attempt.TeamID, input.Attempt.OwnerProfileID, input.Attempt.IdempotencyKey, input.Attempt.RequestHash, input.Attempt.MigratedRequestHash); err != nil {
			return err
		}
		if replay, err := loadTerminalRememberAttemptInTx(txCtx, tx, input.Attempt.TeamID, input.Attempt.OwnerProfileID, input.Attempt.IdempotencyKey, input.Attempt.RequestHash, input.Attempt.MigratedRequestHash); err != nil {
			return err
		} else if replay != nil {
			result.Attempt = replay
			return ErrRememberReplay
		}
		created, err := r.CreateIngest(txCtx, input.CreateIngest)
		if err != nil {
			return err
		}
		if created.Existing {
			return ErrRememberReplay
		}
		result.Ingest = created
		scope, err := activateSynchronousRememberRun(txCtx, tx, created)
		if err != nil {
			return err
		}
		assessmentInput, completeInput, err := input.BuildTerminal(created, scope)
		if err != nil {
			return err
		}
		if assessmentInput != nil {
			assessmentInput.TeamID, assessmentInput.OwnerProfileID, assessmentInput.IngestID, assessmentInput.PlacementRunID = scope.TeamID, scope.OwnerProfileID, scope.IngestID, scope.PlacementRunID
			assessment, _, err := r.PersistSubmissionAssessment(txCtx, *assessmentInput)
			if err != nil {
				return err
			}
			result.Assessment = assessment
		}
		completeInput.SubmissionAssessmentRunScope = scope
		completed, err := r.CompleteSubmissionAssessment(txCtx, completeInput)
		if err != nil {
			return err
		}
		result.Commit = &CommitSubmissionAssessmentResult{Status: completed.Status, OutcomeIDs: completed.OutcomeIDs, FirstDisposition: completed.FirstDisposition}
		if input.BuildPublicResult != nil {
			publicResult, err := input.BuildPublicResult(created, result.Commit)
			if err != nil {
				return err
			}
			input.Attempt.PublicResult = publicResult
		}
		input.Attempt.EvidenceCount = len(created.Evidence)
		if err := insertRememberAttemptInTx(txCtx, tx, input.Attempt); err != nil {
			return err
		}
		result.Attempt = &RememberAttempt{AttemptID: input.Attempt.AttemptID, RequestHash: input.Attempt.RequestHash, ContractVersion: input.Attempt.ContractVersion, Outcome: input.Attempt.Outcome, PublicResult: input.Attempt.PublicResult}
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("repository: commit synchronous Remember terminal: %w", err)
	}
	return result, nil
}

// RecordSynchronousRememberPreflightQuarantine preserves a replayable,
// security-admission result while intentionally leaving all canonical intake
// and semantic tables untouched.
func (r *LedgerRepositoryImpl) RecordSynchronousRememberPreflightQuarantine(ctx context.Context, attempt RememberAttemptRecordInput) error {
	attempt = normalizeRememberAttemptRecord(attempt)
	attempt.Outcome = "quarantined"
	attempt.FailedPhase = "preflight"
	if attempt.ErrorCode == "" {
		attempt.ErrorCode = "submission_quarantined"
	}
	return r.RecordRememberAttempt(ctx, attempt)
}

func validateSynchronousRememberCommitInput(input SynchronousRememberCommitInput) error {
	if input.BuildCommit == nil {
		return errors.New("synchronous Remember commit builder is required")
	}
	if err := validateCreateIngestInput(input.CreateIngest); err != nil {
		return fmt.Errorf("synchronous Remember ingest: %w", err)
	}
	if input.CreateIngest.Status != string(domain.PlacementRunQueued) || !input.CreateIngest.TelemetryRemember {
		return errors.New("synchronous Remember ingest must use queued remember compatibility lineage")
	}
	if input.Attempt.TeamID != input.CreateIngest.TeamID || input.Attempt.OwnerProfileID != input.CreateIngest.OwnerProfileID ||
		input.Attempt.IdempotencyKey != input.CreateIngest.IdempotencyKey || input.Attempt.RequestHash != input.CreateIngest.RequestHash {
		return errors.New("synchronous Remember attempt identity must match ingest")
	}
	if err := validateRememberAttemptRecord(input.Attempt); err != nil {
		return err
	}
	if input.Attempt.Outcome != "completed" {
		return errors.New("synchronous Remember accepted attempt must declare completed outcome")
	}
	return nil
}

func transactionFromContext(ctx context.Context) *gorm.DB {
	scoped, _ := ctx.Value(teamProfileTransactionContextKey{}).(teamProfileTransaction)
	return scoped.tx
}

func activateSynchronousRememberRun(ctx context.Context, tx *gorm.DB, created *CreateIngestResult) (SubmissionAssessmentRunScope, error) {
	if created == nil || tx == nil {
		return SubmissionAssessmentRunScope{}, errors.New("synchronous Remember transaction state is required")
	}
	var attempts, maxAttempts int
	result := tx.WithContext(ctx).Raw(`
		UPDATE placement_runs
		SET status = 'processing', attempts = 1, worker_id = ?,
		    lease_until = now() + interval '10 minutes', started_at = now(), updated_at = now()
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid AND placement_run_id = ?::uuid
		  AND status = 'queued' AND attempts = 0
		RETURNING attempts, max_attempts
	`, synchronousRememberWorkerID, created.TeamID, created.OwnerProfileID, created.IngestID, created.PlacementRunID).Row()
	if err := result.Scan(&attempts, &maxAttempts); err != nil {
		return SubmissionAssessmentRunScope{}, err
	}
	if attempts != 1 || maxAttempts < 1 {
		return SubmissionAssessmentRunScope{}, errors.New("synchronous Remember placement run was not activated")
	}
	return SubmissionAssessmentRunScope{TeamID: created.TeamID, OwnerProfileID: created.OwnerProfileID, IngestID: created.IngestID,
		PlacementRunID: created.PlacementRunID, WorkerID: synchronousRememberWorkerID, ExpectedAttempts: attempts, MaxAttempts: maxAttempts}, nil
}
