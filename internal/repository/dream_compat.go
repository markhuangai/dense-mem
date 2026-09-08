package repository

import (
	"context"
	"database/sql"

	dreampostgres "github.com/markhuangai/dense-mem/internal/dream/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func discardDreamConfirmationLockConnection(lockConn *sql.Conn) error {
	return storagepostgres.DiscardAdvisoryLockConnection(lockConn)
}

// SemanticRepositoryImpl keeps the historical repository surface as a
// single-hop compatibility facade. Dream SQL and transaction policy belong to
// dreampostgres.Store.
func (r *SemanticRepositoryImpl) ClaimDreamCycle(ctx context.Context, input DreamCycleClaimInput) (*DreamCycleRun, error) {
	return r.dreamOwner().ClaimDreamCycle(ctx, input)
}

func (r *SemanticRepositoryImpl) CompleteDreamCycle(ctx context.Context, input DreamCycleCompleteInput) error {
	return r.dreamOwner().CompleteDreamCycle(ctx, input)
}

func (r *SemanticRepositoryImpl) ListDreamInputs(ctx context.Context, input DreamInputListInput) ([]DreamInput, error) {
	return r.dreamOwner().ListDreamInputs(ctx, input)
}

func (r *SemanticRepositoryImpl) ListDreamTargetPredicates(ctx context.Context, teamID string) ([]DreamTargetPredicate, error) {
	return r.dreamOwner().ListDreamTargetPredicates(ctx, teamID)
}

func (r *SemanticRepositoryImpl) ListAvailableDreamTargets(ctx context.Context, teamID string, targets []DreamTargetCandidate) ([]DreamTargetCandidate, error) {
	return r.dreamOwner().ListAvailableDreamTargets(ctx, teamID, targets)
}

func (r *SemanticRepositoryImpl) ListUnassessedDreamPaths(ctx context.Context, teamID string, paths []DreamPathEvaluationInput) ([]DreamPathEvaluationInput, error) {
	return r.dreamOwner().ListUnassessedDreamPaths(ctx, teamID, paths)
}

func (r *SemanticRepositoryImpl) RecordDreamPathEvaluations(ctx context.Context, input DreamPathEvaluationRecordInput) error {
	return r.dreamOwner().RecordDreamPathEvaluations(ctx, input)
}

func (r *SemanticRepositoryImpl) PersistDreamGeneration(ctx context.Context, input DreamGenerationPersistInput) (DreamGenerationPersistResult, error) {
	return r.dreamOwner().PersistDreamGeneration(ctx, input)
}

func (r *SemanticRepositoryImpl) UpsertHypothesis(ctx context.Context, input UpsertHypothesisInput) (*HypothesisRecord, bool, error) {
	return r.dreamOwner().UpsertHypothesis(ctx, input)
}

func (r *SemanticRepositoryImpl) ListHypotheses(ctx context.Context, input ListHypothesesInput) ([]HypothesisRecord, string, error) {
	return r.dreamOwner().ListHypotheses(ctx, input)
}

func (r *SemanticRepositoryImpl) GetHypothesis(ctx context.Context, input GetHypothesisInput) (*HypothesisRecord, error) {
	return r.dreamOwner().GetHypothesis(ctx, input)
}

func (r *SemanticRepositoryImpl) WithHypothesisConfirmationLock(ctx context.Context, teamID, hypothesisID string, fn func(DreamRepository) error) error {
	return r.dreamOwner().WithHypothesisConfirmationLock(ctx, teamID, hypothesisID, func(store dreampostgres.DreamRepository) error {
		return fn(store)
	})
}

func (r *SemanticRepositoryImpl) RecallHypotheses(ctx context.Context, input RecallHypothesesInput) ([]HypothesisRecord, error) {
	return r.dreamOwner().RecallHypotheses(ctx, input)
}

func (r *SemanticRepositoryImpl) UpdateHypothesisStatus(ctx context.Context, input UpdateHypothesisStatusInput) (*HypothesisRecord, error) {
	return r.dreamOwner().UpdateHypothesisStatus(ctx, input)
}

func (r *SemanticRepositoryImpl) SubmitHypothesis(ctx context.Context, input SubmitHypothesisInput) (*HypothesisRecord, error) {
	return r.dreamOwner().SubmitHypothesis(ctx, input)
}

func (r *SemanticRepositoryImpl) CountHypotheses(ctx context.Context, teamID, status string) (int, error) {
	return r.dreamOwner().CountHypotheses(ctx, teamID, status)
}

func (r *SemanticRepositoryImpl) ListDreamCyclesForTeam(ctx context.Context, teamID string, limit int) ([]DreamCycleRun, error) {
	return r.dreamOwner().ListDreamCyclesForTeam(ctx, teamID, limit)
}

func (r *SemanticRepositoryImpl) ClaimScheduledDreamCycle(ctx context.Context, input DreamCycleClaimInput) (*DreamCycleRun, error) {
	return r.dreamOwner().ClaimScheduledDreamCycle(ctx, input)
}

func (r *SemanticRepositoryImpl) ClaimRecoverableScheduledDreamCycle(ctx context.Context, input DreamCycleRecoveryClaimInput) (*DreamCycleRun, error) {
	return r.dreamOwner().ClaimRecoverableScheduledDreamCycle(ctx, input)
}

func (r *SemanticRepositoryImpl) CompleteScheduledDreamCycle(ctx context.Context, input DreamCycleCompleteInput) error {
	return r.dreamOwner().CompleteScheduledDreamCycle(ctx, input)
}

func (r *SemanticRepositoryImpl) UpsertScheduledHypothesis(ctx context.Context, input UpsertHypothesisInput) (*HypothesisRecord, bool, error) {
	return r.dreamOwner().UpsertScheduledHypothesis(ctx, input)
}

func (r *SemanticRepositoryImpl) RecordScheduledDreamPathEvaluations(ctx context.Context, input DreamPathEvaluationRecordInput) error {
	return r.dreamOwner().RecordScheduledDreamPathEvaluations(ctx, input)
}

func (r *SemanticRepositoryImpl) PersistScheduledDreamGeneration(ctx context.Context, input DreamGenerationPersistInput) (DreamGenerationPersistResult, error) {
	return r.dreamOwner().PersistScheduledDreamGeneration(ctx, input)
}

func (r *SemanticRepositoryImpl) RecordMissedScheduledDreamCycle(ctx context.Context, input DreamCycleClaimInput) (*DreamCycleRun, error) {
	return r.dreamOwner().RecordMissedScheduledDreamCycle(ctx, input)
}

func (r *SemanticRepositoryImpl) ListEvidenceDiscoveryTargets(ctx context.Context, teamID string, limit, maxContexts int) ([]EvidenceDiscoveryTargetInput, error) {
	return r.dreamOwner().ListEvidenceDiscoveryTargets(ctx, teamID, limit, maxContexts)
}

func (r *SemanticRepositoryImpl) LoadEvidenceDiscoveryRunTotals(ctx context.Context, teamID, runID string) (EvidenceDiscoveryRunTotals, error) {
	return r.dreamOwner().LoadEvidenceDiscoveryRunTotals(ctx, teamID, runID)
}

func (r *SemanticRepositoryImpl) PersistEvidenceDiscoveryEvaluation(ctx context.Context, input EvidenceDiscoveryEvaluationInput) (DreamGenerationPersistResult, error) {
	return r.dreamOwner().PersistEvidenceDiscoveryEvaluation(ctx, input)
}

func (r *SemanticRepositoryImpl) WithEvidenceDiscoveryTargetLock(ctx context.Context, teamID, targetEvidenceID, contentHash string, fn func(EvidenceDiscoveryAttempt) error) error {
	return r.dreamOwner().WithEvidenceDiscoveryTargetLock(ctx, teamID, targetEvidenceID, contentHash, fn)
}

func (r *SemanticRepositoryImpl) MarkEvidenceDiscoveryAttemptDispatched(ctx context.Context, input EvidenceDiscoveryAttemptValidationInput) error {
	return r.dreamOwner().MarkEvidenceDiscoveryAttemptDispatched(ctx, input)
}

func (r *SemanticRepositoryImpl) MarkEvidenceDiscoveryAttemptValidated(ctx context.Context, input EvidenceDiscoveryAttemptValidationInput) error {
	return r.dreamOwner().MarkEvidenceDiscoveryAttemptValidated(ctx, input)
}

func (r *SemanticRepositoryImpl) AbandonEvidenceDiscoveryAttempt(ctx context.Context, teamID, attemptID, reservationToken string) error {
	return r.dreamOwner().AbandonEvidenceDiscoveryAttempt(ctx, teamID, attemptID, reservationToken)
}

func (r *SemanticRepositoryImpl) ValidateEvidenceDiscoveryInputs(ctx context.Context, teamID string, target EvidenceTarget, contexts []EvidenceContext) error {
	return r.dreamOwner().ValidateEvidenceDiscoveryInputs(ctx, teamID, target, contexts)
}

var _ DreamRepository = (*SemanticRepositoryImpl)(nil)
var _ ScheduledDreamRepository = (*SemanticRepositoryImpl)(nil)
var _ DreamControlRepository = (*SemanticRepositoryImpl)(nil)
var _ EvidenceDiscoveryRepository = (*SemanticRepositoryImpl)(nil)
var _ EvidenceDiscoveryInputValidator = (*SemanticRepositoryImpl)(nil)
