package postgres

import (
	"context"
	"time"

	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

// transactionHandle keeps the legacy fixture bridge from exposing GORM in
// its exported signatures. The handle is intentionally constructible only by
// the adapter boundary below and is not part of the capability contract.
type transactionHandle struct{ db *gorm.DB }

func LegacyTransaction(tx any) transactionHandle {
	db, _ := tx.(*gorm.DB)
	return transactionHandle{db: db}
}

var ErrRelationshipVersionMismatch = errRelationshipVersionMismatch

// ErrRelationshipCorrectionSelectionUnavailable preserves the error identity
// used by the legacy correction integration fixture until that fixture moves
// to the lifecycle owner package.
var ErrRelationshipCorrectionSelectionUnavailable = errRelationshipCorrectionSelectionUnavailable

// EvidenceLifecycleCompleted preserves the terminal state constant used by
// legacy lifecycle integration fixtures during the owner migration.
const EvidenceLifecycleCompleted = evidenceLifecycleCompleted

// NormalizeConflictRuntimeConfig preserves the owner-owned defaults for
// compatibility constructors without duplicating conflict policy.
func NormalizeConflictRuntimeConfig(input knowledgecontract.ConflictRuntimeConfig) knowledgecontract.ConflictRuntimeConfig {
	return normalizeConflictRuntimeConfig(input)
}

func ValidateRememberDuplicateCandidateInput(input RememberDuplicateCandidateInput) error {
	return validateRememberDuplicateCandidateInput(input)
}

func RememberDuplicateBatchCanonicalKey(item EvidenceInput) string {
	return rememberDuplicateBatchCanonicalKey(item)
}

func ValidateRetractEvidenceInput(input RetractEvidenceInput) error {
	return validateRetractEvidenceInput(input)
}

// ApplyEvidenceSupersessionsTx is retained for the legacy CreateIngest test
// fixture until that fixture migrates to the lifecycle owner port.
func ApplyEvidenceSupersessionsTx(
	ctx context.Context,
	tx transactionHandle,
	input CreateIngestInput,
	ingestID string,
	evidence []EvidenceFragment,
) error {
	return applyEvidenceSupersessions(ctx, tx.db, input, ingestID, evidence)
}

func ValidateRememberSubmissionSupersessionTargetsTx(
	ctx context.Context,
	tx transactionHandle,
	input CreateIngestInput,
	ingestID string,
) error {
	return validateRememberSubmissionSupersessionTargets(ctx, tx.db, input, ingestID)
}

func InsertEvidenceQuarantineTx(ctx context.Context, tx transactionHandle, input CreateIngestInput, ingestID, fragmentID, reason string) error {
	return insertEvidenceQuarantine(ctx, tx.db, input, ingestID, fragmentID, reason)
}

func InsertSecurityEventTx(ctx context.Context, tx transactionHandle, input SecurityEventInput) (string, error) {
	return insertSecurityEvent(ctx, tx.db, input)
}

func ValidateSecurityEventDraft(input SecurityEventDraft) error {
	return validateSecurityEventDraft(input)
}

func NormalizeAdvanceSourceRevisionInput(input AdvanceSourceRevisionInput) AdvanceSourceRevisionInput {
	return normalizeAdvanceSourceRevisionInput(input)
}

func ValidateAdvanceSourceRevisionInput(input AdvanceSourceRevisionInput) error {
	return validateAdvanceSourceRevisionInput(input)
}

func AdvanceSourceRevisionTx(
	ctx context.Context,
	tx transactionHandle,
	input AdvanceSourceRevisionInput,
	cache map[string]SourceRevisionResult,
) (*SourceRevisionResult, error) {
	return advanceSourceRevisionInTx(ctx, tx.db, input, cache)
}

func InsertRelationshipTransitionTx(
	ctx context.Context,
	tx transactionHandle,
	teamID, ownerProfileID, relationshipID, spaceID, fromStatus, toStatus, reason, verificationEventID, supportDecisionID, idempotencyKey string,
) (string, error) {
	return insertRelationshipTransition(ctx, tx.db, transitionInput{
		TeamID: teamID, OwnerProfileID: ownerProfileID, RelationshipID: relationshipID,
		SpaceID: spaceID, FromStatus: fromStatus, ToStatus: toStatus, Reason: reason,
		VerificationEventID: verificationEventID, SupportDecisionID: supportDecisionID,
		IdempotencyKey: idempotencyKey,
	})
}

var ErrRelationshipDecisionNonPromotable = errRelationshipDecisionNonPromotable

func SeedTeamPredicateDefinitions(ctx context.Context, tx transactionHandle, teamID string) error {
	return seedTeamPredicateDefinitions(ctx, tx.db, teamID)
}

func EnsureSemanticPredicateCandidateTx(
	ctx context.Context,
	tx transactionHandle,
	input EnsureSemanticPredicateCandidateInput,
) (*SemanticReviewPredicateCandidate, error) {
	return ensureSemanticPredicateCandidateTx(ctx, tx.db, input)
}

func CanonicalGeneratedPredicateKey(value string) string {
	return canonicalGeneratedPredicateKey(value)
}

func RelationshipConflictScopeKey(record *RelationshipRecord, spaceID, spaceKind string) string {
	return relationshipConflictScopeKey(record, spaceID, spaceKind)
}

func ConflictNextReviewAt(now, due time.Time) time.Time {
	return conflictNextReviewAt(now, due)
}

func NormalizeConflictReviewRunInput(input ConflictReviewRunInput) (ConflictReviewRunInput, error) {
	return normalizeConflictReviewRunInput(input)
}

func ValidateClaimConflictDerivedEvidenceTasksInput(input ClaimConflictDerivedEvidenceTasksInput) error {
	return validateClaimConflictDerivedEvidenceTasksInput(input)
}

func NormalizeCommitSubmissionAssessmentInput(input CommitSubmissionAssessmentInput) CommitSubmissionAssessmentInput {
	return normalizeCommitSubmissionAssessmentInput(input)
}

func NormalizeRememberCommitScope(scope RememberCommitScope) RememberCommitScope {
	return normalizeRememberCommitScope(scope)
}

func ValidateCommitSubmissionAssessmentInput(input CommitSubmissionAssessmentInput) error {
	return validateCommitSubmissionAssessmentInput(input)
}

func NormalizeCorrectRelationshipInput(input CorrectRelationshipInput) CorrectRelationshipInput {
	return normalizeCorrectRelationshipInput(input)
}

func ValidateCorrectRelationshipInput(input CorrectRelationshipInput) error {
	return validateCorrectRelationshipInput(input)
}

func NormalizeRememberAttemptRecord(input RememberAttemptRecordInput) RememberAttemptRecordInput {
	return normalizeRememberAttemptRecord(input)
}

func ValidateRememberAttemptRecord(input RememberAttemptRecordInput) error {
	return validateRememberAttemptRecord(input)
}

func RememberAttemptPhase(input RememberAttemptRecordInput) string {
	return rememberAttemptPhase(input)
}

func RememberAttemptEventKind(input RememberAttemptRecordInput) string {
	return rememberAttemptEventKind(input)
}

func ReauthorizeSubmissionKnownEvidence(ctx context.Context, tx transactionHandle, input CommitSubmissionAssessmentInput) error {
	return reauthorizeSubmissionKnownEvidence(ctx, tx.db, input)
}

func UpsertSearchDocumentInTx(ctx context.Context, tx transactionHandle, input UpsertSearchDocumentInput, contract *ActiveSearchContract) (*SearchDocumentResult, error) {
	return upsertSearchDocumentInTx(ctx, tx.db, input, contract)
}

func SemanticRelationshipSearchText(ctx context.Context, tx transactionHandle, relationship *RelationshipRecord) (string, error) {
	return semanticRelationshipSearchText(ctx, tx.db, relationship)
}

func RelationshipSearchEligible(relationship *RelationshipRecord) bool {
	return relationshipSearchEligible(relationship)
}

func RelationshipForegroundRecallGenerationID(ctx context.Context, tx transactionHandle, teamID string) (string, error) {
	return relationshipForegroundRecallGenerationID(ctx, tx.db, teamID)
}

func EnsureConflictSystemProfile(ctx context.Context, tx transactionHandle, teamID string) (string, error) {
	return ensureConflictSystemProfile(ctx, tx.db, teamID)
}

func ValidateRelationshipVersion(ctx context.Context, tx transactionHandle, teamID, relationshipID, ownerProfileID string, version int) error {
	return requireRelationshipVersion(ctx, tx.db, teamID, relationshipID, ownerProfileID, version)
}

// ConflictPlacement and ConflictPlacementRow expose the owner-owned
// transaction helpers to legacy test/setup callers without copying SQL.
type ConflictPlacement = conflictPlacement
type ConflictPlacementRow = conflictPlacementRow

// LoadRelationshipConflictPlacement and UpsertRelationshipConflictCase expose
// the owner-owned transaction helpers to legacy conflict fixtures without
// copying their SQL or changing transaction scope.
func LoadRelationshipConflictPlacement(
	ctx context.Context,
	tx transactionHandle,
	teamID string,
	source *RelationshipRecord,
) (*ConflictPlacement, error) {
	return loadRelationshipConflictPlacement(ctx, tx.db, teamID, source)
}

func UpsertRelationshipConflictCase(
	ctx context.Context,
	tx transactionHandle,
	teamID string,
	placement *ConflictPlacement,
	config ConflictRuntimeConfig,
) error {
	return upsertRelationshipConflictCase(ctx, tx.db, teamID, placement, config)
}

func EnqueueConflictDerivedEvidenceTasks(
	ctx context.Context,
	tx transactionHandle,
	resolutionPlanID string,
	targets []ConflictDerivedEvidenceTarget,
) ([]ConflictDerivedEvidenceTarget, error) {
	return enqueueConflictDerivedEvidenceTasks(ctx, tx.db, resolutionPlanID, targets)
}

func ResolveRememberExactEvidenceInTx(
	ctx context.Context,
	tx transactionHandle,
	input RememberDuplicateCandidateInput,
	evidence EvidenceInput,
) (string, bool, error) {
	return resolveRememberExactEvidenceInTx(ctx, tx.db, input, evidence)
}

func LockEvidenceLifecycleTarget(ctx context.Context, tx transactionHandle, teamID, fragmentID string) error {
	return lockEvidenceLifecycleTarget(ctx, tx.db, teamID, fragmentID)
}

func ValidateSelectedCorrectionEntities(
	ctx context.Context,
	tx transactionHandle,
	teamID string,
	candidates []RelationshipCorrectionCandidate,
	selection RelationshipCorrectionSelection,
) error {
	return validateSelectedCorrectionEntities(ctx, tx.db, teamID, candidates, selection)
}

func LockRelationshipConflictSnapshotScopeTx(ctx context.Context, tx transactionHandle, teamID, scopeKey string) error {
	return lockRelationshipConflictSnapshotScope(ctx, tx.db, teamID, scopeKey)
}
