package repository

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var (
	ErrRelationshipCorrectionNotFound             = errors.New("relationship correction not found")
	ErrRelationshipCorrectionConfirmation         = errors.New("relationship correction confirmation is invalid")
	ErrRelationshipCorrectionConfirmationExpired  = errors.New("relationship correction confirmation expired")
	ErrRelationshipCorrectionStateConflict        = errors.New("relationship correction state conflict")
	errRelationshipCorrectionSelectionUnavailable = errors.New("relationship correction selected entity is unavailable")
)

const (
	relationshipCorrectionConfirmationTTL = 2 * time.Hour
	relationshipCorrectionCandidateLimit  = 20
)

const relationshipCorrectionSubmissionSelectSQL = `
	SELECT submission.team_id::text, submission.submission_id::text,
	       submission.owner_profile_id::text, submission.space_id::text, submission.relationship_id::text,
	       submission.expected_version, submission.request_hash,
	       submission.patch, submission.supports, submission.reason,
	       submission.idempotency_key, submission.confirmation_idempotency_key,
	       submission.confirmation_request_hash, submission.processing_state,
	       submission.confirmation_round, submission.confirmation_token,
	       submission.confirmation_expires_at, submission.candidates,
	       submission.selection, COALESCE(submission.successor_relationship_id::text, ''),
	       submission.reused_successor, submission.error_code, submission.error_message,
	       COALESCE(successor.version, 0), COALESCE(successor_search.search_state, 'not_required')
	FROM relationship_correction_submissions AS submission
	JOIN memory_spaces AS submission_space
	  ON submission_space.team_id = submission.team_id
	 AND submission_space.id = submission.space_id
	 AND submission_space.generation = submission.space_generation
	 AND submission_space.lifecycle_state = 'active'
	LEFT JOIN relationship_records AS successor
	  ON successor.team_id = submission.team_id
	 AND successor.relationship_id = submission.successor_relationship_id
	 AND successor.space_id = submission.space_id
	 AND successor.space_generation = submission.space_generation
	LEFT JOIN LATERAL (
	    SELECT document.search_state
	    FROM search_documents AS document
	    JOIN search_index_generations AS generation
	      ON generation.embedding_contract_id = document.embedding_contract_id
	     AND generation.embedding_dimensions = document.embedding_dimensions
	     AND generation.activation_state = 'active'
	    JOIN embedding_contracts AS contract
	      ON contract.embedding_contract_id = document.embedding_contract_id
	     AND contract.lifecycle_state = 'active'
	     AND contract.distance_metric = 'cosine'
	    WHERE document.team_id = submission.team_id
	      AND document.source_kind = 'relationship'
	      AND document.source_id = submission.successor_relationship_id
	      AND document.space_id = submission.space_id
	      AND document.space_generation = submission.space_generation
	    ORDER BY contract.version DESC, generation.generation DESC, document.updated_at DESC
	    LIMIT 1
	) AS successor_search ON true
`

type relationshipCorrectionSubmissionRow struct {
	TeamID                  string
	SubmissionID            string
	OwnerProfileID          string
	SpaceID                 string
	RelationshipID          string
	ExpectedVersion         int
	RequestHash             string
	Patch                   RelationshipCorrectionPatch
	Supports                []RelationshipCorrectionSupport
	Reason                  string
	IdempotencyKey          string
	ConfirmationIdempotency string
	ConfirmationRequestHash string
	ProcessingState         string
	ConfirmationRound       int
	ConfirmationToken       string
	ConfirmationExpiresAt   *time.Time
	Candidates              []RelationshipCorrectionCandidate
	Selection               RelationshipCorrectionSelection
	SuccessorRelationshipID string
	ReusedSuccessor         bool
	ErrorCode               string
	ErrorMessage            string
	SuccessorVersion        int
	SearchState             string
}

func (r *SemanticRepositoryImpl) CorrectRelationship(
	ctx context.Context,
	input CorrectRelationshipInput,
) (*CorrectRelationshipResult, error) {
	// Corrections cannot fall back to the asynchronous embedding queue. This
	// compatibility entry point therefore fails a state-changing correction
	// unless its caller supplies the precomputed provider result.
	return r.correctRelationship(withRelationshipCorrectionEmbeddings(ctx, nil), input)
}

// CorrectRelationshipWithEmbeddings commits only provider results produced by
// PlanRelationshipCorrectionEmbeddings before the transaction began.
func (r *SemanticRepositoryImpl) CorrectRelationshipWithEmbeddings(
	ctx context.Context,
	input CorrectRelationshipInput,
	embeddings []RelationshipCorrectionEmbedding,
) (*CorrectRelationshipResult, error) {
	return r.correctRelationship(withRelationshipCorrectionEmbeddings(ctx, embeddings), input)
}

func (r *SemanticRepositoryImpl) correctRelationship(
	ctx context.Context,
	input CorrectRelationshipInput,
) (*CorrectRelationshipResult, error) {
	input = normalizeCorrectRelationshipInput(input)
	if err := validateCorrectRelationshipInput(input); err != nil {
		return nil, err
	}
	var result *CorrectRelationshipResult
	var committedErr error
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		var err error
		if input.Action == "confirm" {
			result, err = r.confirmRelationshipCorrection(ctx, tx, input)
			if errors.Is(err, ErrRelationshipCorrectionConfirmationExpired) {
				committedErr = err
				return nil
			}
			if errors.Is(err, ErrRelationshipCorrectionConfirmation) && result != nil && result.ProcessingState == "awaiting_confirmation" {
				return nil
			}
		} else {
			result, err = r.submitRelationshipCorrection(ctx, tx, input)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: correct relationship: %w", err)
	}
	if committedErr != nil {
		return nil, fmt.Errorf("semantic: correct relationship: %w", committedErr)
	}
	return result, nil
}

func (r *SemanticRepositoryImpl) GetRelationshipCorrection(
	ctx context.Context,
	input GetRelationshipCorrectionInput,
) (*RelationshipCorrectionStatus, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	for label, value := range map[string]string{
		"team_id":          input.TeamID,
		"owner_profile_id": input.OwnerProfileID,
		"submission_id":    input.SubmissionID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return nil, fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if r == nil || r.db == nil {
		return nil, errors.New("semantic: database is required")
	}
	if r.rls == nil {
		return nil, errors.New("semantic: rls helper is required")
	}
	var result *CorrectRelationshipResult
	err := r.rls.WithTeamProfileTx(ctx, r.db, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		row, err := loadRelationshipCorrectionSubmission(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, false)
		if err != nil {
			return err
		}
		result = relationshipCorrectionResultFromRow(row)
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRelationshipCorrectionNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("semantic: get relationship correction: %w", err)
	}
	return result, nil
}

func (r *SemanticRepositoryImpl) submitRelationshipCorrection(
	ctx context.Context,
	tx *gorm.DB,
	input CorrectRelationshipInput,
) (*CorrectRelationshipResult, error) {
	requestHash, err := relationshipCorrectionRequestHash(input)
	if err != nil {
		return nil, err
	}
	if existing, err := loadRelationshipCorrectionByIdempotency(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey); err == nil {
		if existing.RequestHash != requestHash {
			return nil, ErrSemanticIdempotencyConflict
		}
		return relationshipCorrectionResultFromRow(existing), nil
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	source, err := loadRelationshipRecordForUpdate(ctx, tx, input.TeamID, input.RelationshipID)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrSemanticOwnerMismatch
	}
	if err != nil {
		return nil, err
	}
	if source.OwnerProfileID != input.OwnerProfileID {
		return nil, ErrSemanticOwnerMismatch
	}
	if source.SpaceID == "" {
		return nil, errors.New("relationship correction requires a memory space")
	}

	rejection := func(code, message string) (*CorrectRelationshipResult, error) {
		row, _, err := insertRelationshipCorrectionSubmission(ctx, tx, input, requestHash, relationshipCorrectionInsert{
			State: "rejected", SpaceID: source.SpaceID, ErrorCode: code, ErrorMessage: message,
		})
		if err != nil {
			return nil, err
		}
		return relationshipCorrectionResultFromRow(row), nil
	}
	if source.Version != input.ExpectedVersion {
		return rejection("relationship_version_stale", "relationship version is stale")
	}
	if source.IdentityAliasOfID != "" || source.Status != string(domain.RelationshipStatusActive) || source.SupportCount == 0 {
		return rejection("relationship_not_active", "relationship must be active, supported, and canonical")
	}
	if input.Patch.ObjectEntity != nil && source.ObjectEntityID == "" {
		return rejection("object_kind_change_forbidden", "a Value object cannot be replaced with an Entity")
	}

	effectiveSupports, err := loadEffectiveRelationshipCorrectionSupports(ctx, tx, input.TeamID, source.RelationshipID)
	if err != nil {
		return nil, err
	}
	if !relationshipCorrectionSupportsEqual(input.Supports, effectiveSupports) {
		return rejection("support_set_mismatch", "supports must exactly match the relationship's effective evidence spans")
	}
	for _, support := range effectiveSupports {
		if err := requireSemanticSpaceMatch(source.SpaceID, support.SpaceID); err != nil {
			return rejection("support_space_mismatch", "relationship supports must remain in the relationship memory space")
		}
	}

	resolution, err := resolveRelationshipCorrectionPatch(ctx, tx, input, source)
	if err != nil {
		return nil, err
	}
	if resolution.RejectionCode != "" {
		return rejection(resolution.RejectionCode, resolution.RejectionMessage)
	}
	if len(resolution.Candidates) > 0 {
		token := uuid.NewString()
		expiresAt := time.Now().UTC().Add(relationshipCorrectionConfirmationTTL)
		row, _, err := insertRelationshipCorrectionSubmission(ctx, tx, input, requestHash, relationshipCorrectionInsert{
			State:      "awaiting_confirmation",
			SpaceID:    source.SpaceID,
			Token:      token,
			ExpiresAt:  &expiresAt,
			Candidates: resolution.Candidates,
			Selection:  resolution.Selection,
		})
		if err != nil {
			return nil, err
		}
		return relationshipCorrectionResultFromRow(row), nil
	}

	row, created, err := insertRelationshipCorrectionSubmission(ctx, tx, input, requestHash, relationshipCorrectionInsert{
		State:     "processing",
		SpaceID:   source.SpaceID,
		Selection: resolution.Selection,
	})
	if err != nil {
		return nil, err
	}
	if !created {
		return relationshipCorrectionResultFromRow(row), nil
	}
	return r.applyRelationshipCorrection(ctx, tx, row, source, resolution, effectiveSupports)
}

func (r *SemanticRepositoryImpl) confirmRelationshipCorrection(
	ctx context.Context,
	tx *gorm.DB,
	input CorrectRelationshipInput,
) (*CorrectRelationshipResult, error) {
	row, err := loadRelationshipCorrectionSubmission(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, true)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrRelationshipCorrectionNotFound
	}
	if err != nil {
		return nil, err
	}
	confirmationHash, err := relationshipCorrectionConfirmationHash(input)
	if err != nil {
		return nil, err
	}
	if row.ConfirmationIdempotency != "" {
		if row.ConfirmationIdempotency == input.IdempotencyKey && row.ConfirmationRequestHash == confirmationHash {
			if row.ErrorCode == "confirmation_expired" {
				return relationshipCorrectionResultFromRow(row), ErrRelationshipCorrectionConfirmationExpired
			}
			return relationshipCorrectionResultFromRow(row), nil
		}
		if row.ConfirmationIdempotency == input.IdempotencyKey {
			return nil, ErrSemanticIdempotencyConflict
		}
	}
	if row.ProcessingState != "awaiting_confirmation" || row.ConfirmationRound != 0 {
		return nil, ErrRelationshipCorrectionConfirmation
	}
	if row.ConfirmationExpiresAt == nil || !time.Now().UTC().Before(*row.ConfirmationExpiresAt) {
		if err := rejectRelationshipCorrectionSubmission(ctx, tx, row, "confirmation_expired", "relationship correction confirmation expired", input.IdempotencyKey, confirmationHash); err != nil {
			return nil, err
		}
		updated, err := loadRelationshipCorrectionSubmission(ctx, tx, row.TeamID, row.OwnerProfileID, row.SubmissionID, false)
		if err != nil {
			return nil, err
		}
		return relationshipCorrectionResultFromRow(updated), ErrRelationshipCorrectionConfirmationExpired
	}
	if subtle.ConstantTimeCompare([]byte(row.ConfirmationToken), []byte(input.ConfirmationToken)) != 1 {
		return relationshipCorrectionResultFromRow(row), ErrRelationshipCorrectionConfirmation
	}

	source, err := loadRelationshipRecordForUpdate(ctx, tx, row.TeamID, row.RelationshipID)
	if err != nil || source.OwnerProfileID != row.OwnerProfileID {
		if err == nil {
			err = ErrSemanticOwnerMismatch
		}
		return nil, err
	}
	if source.Version != row.ExpectedVersion || source.Status != string(domain.RelationshipStatusActive) || source.SupportCount == 0 || source.IdentityAliasOfID != "" {
		if err := rejectRelationshipCorrectionSubmission(ctx, tx, row, "relationship_changed", "relationship changed while confirmation was pending", input.IdempotencyKey, confirmationHash); err != nil {
			return nil, err
		}
		updated, err := loadRelationshipCorrectionSubmission(ctx, tx, row.TeamID, row.OwnerProfileID, row.SubmissionID, false)
		if err != nil {
			return nil, err
		}
		return relationshipCorrectionResultFromRow(updated), nil
	}
	effectiveSupports, err := loadEffectiveRelationshipCorrectionSupports(ctx, tx, row.TeamID, row.RelationshipID)
	if err != nil {
		return nil, err
	}
	if !relationshipCorrectionSupportsEqual(row.Supports, effectiveSupports) {
		if err := rejectRelationshipCorrectionSubmission(ctx, tx, row, "support_set_changed", "relationship supports changed while confirmation was pending", input.IdempotencyKey, confirmationHash); err != nil {
			return nil, err
		}
		updated, err := loadRelationshipCorrectionSubmission(ctx, tx, row.TeamID, row.OwnerProfileID, row.SubmissionID, false)
		if err != nil {
			return nil, err
		}
		return relationshipCorrectionResultFromRow(updated), nil
	}
	for _, support := range effectiveSupports {
		if err := requireSemanticSpaceMatch(source.SpaceID, support.SpaceID); err != nil {
			if rejectErr := rejectRelationshipCorrectionSubmission(ctx, tx, row, "support_space_mismatch", "relationship supports must remain in the relationship memory space", input.IdempotencyKey, confirmationHash); rejectErr != nil {
				return nil, rejectErr
			}
			updated, loadErr := loadRelationshipCorrectionSubmission(ctx, tx, row.TeamID, row.OwnerProfileID, row.SubmissionID, false)
			if loadErr != nil {
				return nil, loadErr
			}
			return relationshipCorrectionResultFromRow(updated), nil
		}
	}

	selection, err := validateRelationshipCorrectionSelection(row, input.Selection)
	if err != nil {
		return relationshipCorrectionResultFromRow(row), err
	}
	confirmInput := CorrectRelationshipInput{
		TeamID:          row.TeamID,
		OwnerProfileID:  row.OwnerProfileID,
		RelationshipID:  row.RelationshipID,
		ExpectedVersion: row.ExpectedVersion,
		Patch:           row.Patch,
		Supports:        row.Supports,
		Reason:          row.Reason,
		IdempotencyKey:  row.IdempotencyKey,
	}
	resolution, err := resolveRelationshipCorrectionPatch(ctx, tx, confirmInput, source)
	if err != nil {
		return nil, err
	}
	if resolution.RejectionCode != "" {
		if err := rejectRelationshipCorrectionSubmission(ctx, tx, row, resolution.RejectionCode, resolution.RejectionMessage, input.IdempotencyKey, confirmationHash); err != nil {
			return nil, err
		}
		updated, err := loadRelationshipCorrectionSubmission(ctx, tx, row.TeamID, row.OwnerProfileID, row.SubmissionID, false)
		if err != nil {
			return nil, err
		}
		return relationshipCorrectionResultFromRow(updated), nil
	}
	resolution.Selection = mergeRelationshipCorrectionSelection(resolution.Selection, selection)
	resolution.Candidates = nil
	if err := validateSelectedCorrectionEntities(ctx, tx, row.TeamID, row.Candidates, resolution.Selection); err != nil {
		if !errors.Is(err, errRelationshipCorrectionSelectionUnavailable) {
			return nil, err
		}
		if rejectErr := rejectRelationshipCorrectionSubmission(ctx, tx, row, "persistent_ambiguity", "selected Entity candidate is no longer available", input.IdempotencyKey, confirmationHash); rejectErr != nil {
			return nil, rejectErr
		}
		updated, loadErr := loadRelationshipCorrectionSubmission(ctx, tx, row.TeamID, row.OwnerProfileID, row.SubmissionID, false)
		if loadErr != nil {
			return nil, loadErr
		}
		return relationshipCorrectionResultFromRow(updated), nil
	}
	if err := markRelationshipCorrectionConfirmation(ctx, tx, row, input.IdempotencyKey, confirmationHash, resolution.Selection); err != nil {
		return nil, err
	}
	row.ConfirmationIdempotency = input.IdempotencyKey
	row.ConfirmationRequestHash = confirmationHash
	row.ConfirmationRound = 1
	row.Selection = resolution.Selection
	row.ProcessingState = "processing"
	return r.applyRelationshipCorrection(ctx, tx, row, source, resolution, effectiveSupports)
}

func normalizeCorrectRelationshipInput(input CorrectRelationshipInput) CorrectRelationshipInput {
	input.TeamID = canonicalCorrectionUUID(input.TeamID)
	input.OwnerProfileID = canonicalCorrectionUUID(input.OwnerProfileID)
	input.Action = strings.TrimSpace(input.Action)
	input.RelationshipID = canonicalCorrectionUUID(input.RelationshipID)
	input.Reason = strings.TrimSpace(input.Reason)
	input.SubmissionID = canonicalCorrectionUUID(input.SubmissionID)
	input.ConfirmationToken = canonicalCorrectionUUID(input.ConfirmationToken)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if input.Patch.SubjectEntity != nil {
		copy := *input.Patch.SubjectEntity
		input.Patch.SubjectEntity = &copy
	}
	if input.Patch.ObjectEntity != nil {
		copy := *input.Patch.ObjectEntity
		input.Patch.ObjectEntity = &copy
	}
	if input.Patch.Predicate != nil {
		copy := *input.Patch.Predicate
		input.Patch.Predicate = &copy
	}
	normalizeCorrectionEntityPatch(input.Patch.SubjectEntity)
	normalizeCorrectionEntityPatch(input.Patch.ObjectEntity)
	if input.Patch.Predicate != nil {
		input.Patch.Predicate.Key = strings.TrimSpace(input.Patch.Predicate.Key)
	}
	input.Selection.SubjectEntityID = canonicalCorrectionUUID(input.Selection.SubjectEntityID)
	input.Selection.ObjectEntityID = canonicalCorrectionUUID(input.Selection.ObjectEntityID)
	input.Supports = append([]RelationshipCorrectionSupport(nil), input.Supports...)
	for index := range input.Supports {
		input.Supports[index].EvidenceID = canonicalCorrectionUUID(input.Supports[index].EvidenceID)
	}
	sort.Slice(input.Supports, func(i, j int) bool {
		return relationshipCorrectionSupportKey(input.Supports[i]) < relationshipCorrectionSupportKey(input.Supports[j])
	})
	return input
}

func normalizeCorrectionEntityPatch(patch *RelationshipCorrectionEntityPatch) {
	if patch == nil {
		return
	}
	patch.EntityID = canonicalCorrectionUUID(patch.EntityID)
	patch.Name = strings.TrimSpace(patch.Name)
	patch.EntityKind = strings.TrimSpace(patch.EntityKind)
}

func canonicalCorrectionUUID(value string) string {
	trimmed := strings.TrimSpace(value)
	parsed, err := uuid.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	return parsed.String()
}

func validateCorrectRelationshipInput(input CorrectRelationshipInput) error {
	for label, value := range map[string]string{"team_id": input.TeamID, "owner_profile_id": input.OwnerProfileID} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if input.IdempotencyKey == "" {
		return errors.New("idempotency_key is required")
	}
	switch input.Action {
	case "submit":
		if _, err := uuid.Parse(input.RelationshipID); err != nil {
			return fmt.Errorf("relationship_id is required: %w", err)
		}
		if input.ExpectedVersion < 1 {
			return errors.New("expected_version must be greater than zero")
		}
		if input.Patch.SubjectEntity == nil && input.Patch.Predicate == nil && input.Patch.ObjectEntity == nil {
			return errors.New("patch must change at least one relationship field")
		}
		if input.Reason == "" || len([]rune(input.Reason)) > 1000 {
			return errors.New("reason is required and must be at most 1000 characters")
		}
		if len(input.Supports) == 0 || len(input.Supports) > 200 {
			return errors.New("supports must contain between 1 and 200 entries")
		}
		seen := make(map[string]struct{}, len(input.Supports))
		for _, support := range input.Supports {
			if _, err := uuid.Parse(support.EvidenceID); err != nil {
				return fmt.Errorf("support evidence_id is invalid: %w", err)
			}
			if support.Start < 0 || support.End <= support.Start {
				return errors.New("support span is invalid")
			}
			key := relationshipCorrectionSupportKey(support)
			if _, exists := seen[key]; exists {
				return errors.New("supports must not contain duplicate evidence spans")
			}
			seen[key] = struct{}{}
		}
		if err := validateCorrectionEntityPatch(input.Patch.SubjectEntity); err != nil {
			return fmt.Errorf("subject_entity: %w", err)
		}
		if err := validateCorrectionEntityPatch(input.Patch.ObjectEntity); err != nil {
			return fmt.Errorf("object_entity: %w", err)
		}
		if input.Patch.Predicate != nil && input.Patch.Predicate.Key == "" {
			return errors.New("predicate.key is required")
		}
	case "confirm":
		if _, err := uuid.Parse(input.SubmissionID); err != nil {
			return fmt.Errorf("submission_id is required: %w", err)
		}
		if _, err := uuid.Parse(input.ConfirmationToken); err != nil {
			return fmt.Errorf("confirmation_token is required: %w", err)
		}
		if input.Selection.SubjectEntityID == "" && input.Selection.ObjectEntityID == "" {
			return errors.New("selection must choose at least one Entity candidate")
		}
		for label, value := range map[string]string{
			"selection.subject_entity_id": input.Selection.SubjectEntityID,
			"selection.object_entity_id":  input.Selection.ObjectEntityID,
		} {
			if value != "" {
				if _, err := uuid.Parse(value); err != nil {
					return fmt.Errorf("%s is invalid: %w", label, err)
				}
			}
		}
	default:
		return errors.New("action must be submit or confirm")
	}
	return nil
}

func validateCorrectionEntityPatch(patch *RelationshipCorrectionEntityPatch) error {
	if patch == nil {
		return nil
	}
	if patch.EntityID != "" {
		if patch.Name != "" || patch.EntityKind != "" {
			return errors.New("entity_id cannot be combined with name or entity_kind")
		}
		if _, err := uuid.Parse(patch.EntityID); err != nil {
			return fmt.Errorf("entity_id is invalid: %w", err)
		}
		return nil
	}
	if patch.Name == "" || patch.EntityKind == "" {
		return errors.New("name and entity_kind are required when entity_id is omitted")
	}
	if !contains(domain.EntityKinds(), patch.EntityKind) {
		return fmt.Errorf("unsupported entity_kind %q", patch.EntityKind)
	}
	return nil
}

func relationshipCorrectionRequestHash(input CorrectRelationshipInput) (string, error) {
	payload := struct {
		RelationshipID  string                          `json:"relationship_id"`
		ExpectedVersion int                             `json:"expected_version"`
		Patch           RelationshipCorrectionPatch     `json:"patch"`
		Supports        []RelationshipCorrectionSupport `json:"supports"`
		Reason          string                          `json:"reason"`
	}{input.RelationshipID, input.ExpectedVersion, input.Patch, input.Supports, input.Reason}
	return relationshipCorrectionHash(payload)
}

func relationshipCorrectionConfirmationHash(input CorrectRelationshipInput) (string, error) {
	payload := struct {
		SubmissionID string                          `json:"submission_id"`
		Token        string                          `json:"confirmation_token"`
		Selection    RelationshipCorrectionSelection `json:"selection"`
	}{input.SubmissionID, input.ConfirmationToken, input.Selection}
	return relationshipCorrectionHash(payload)
}

func relationshipCorrectionHash(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func relationshipCorrectionSupportKey(support RelationshipCorrectionSupport) string {
	return fmt.Sprintf("%s\x00%010d\x00%010d", support.EvidenceID, support.Start, support.End)
}

func relationshipCorrectionSupportsEqual(requested []RelationshipCorrectionSupport, effective []effectiveRelationshipCorrectionSupport) bool {
	if len(requested) != len(effective) {
		return false
	}
	for index := range requested {
		if requested[index] != effective[index].RelationshipCorrectionSupport {
			return false
		}
	}
	return true
}

func mergeRelationshipCorrectionSelection(left, right RelationshipCorrectionSelection) RelationshipCorrectionSelection {
	if right.SubjectEntityID != "" {
		left.SubjectEntityID = right.SubjectEntityID
	}
	if right.ObjectEntityID != "" {
		left.ObjectEntityID = right.ObjectEntityID
	}
	return left
}

func relationshipCorrectionResultFromRow(row *relationshipCorrectionSubmissionRow) *CorrectRelationshipResult {
	if row == nil {
		return nil
	}
	result := &CorrectRelationshipResult{
		SubmissionID:    row.SubmissionID,
		ProcessingState: row.ProcessingState,
		ErrorCode:       row.ErrorCode,
		ErrorMessage:    row.ErrorMessage,
		SearchState:     row.SearchState,
	}
	if row.ProcessingState == "awaiting_confirmation" && row.ConfirmationExpiresAt != nil {
		result.Confirmation = &RelationshipCorrectionConfirmation{
			Token: row.ConfirmationToken, ExpiresAt: *row.ConfirmationExpiresAt,
			Candidates: append([]RelationshipCorrectionCandidate(nil), row.Candidates...),
		}
	}
	if row.SuccessorRelationshipID != "" {
		result.Correction = &RelationshipCorrectionResult{
			OriginalRelationshipID:  row.RelationshipID,
			OriginalVersion:         row.ExpectedVersion + 1,
			SuccessorRelationshipID: row.SuccessorRelationshipID,
			SuccessorVersion:        row.SuccessorVersion,
			ReusedSuccessor:         row.ReusedSuccessor,
		}
	}
	return result
}

func loadRelationshipCorrectionSubmission(
	ctx context.Context,
	tx *gorm.DB,
	teamID, ownerProfileID, submissionID string,
	lock bool,
) (*relationshipCorrectionSubmissionRow, error) {
	lockClause := ""
	if lock {
		lockClause = "FOR UPDATE OF submission"
	}
	return scanRelationshipCorrectionSubmission(tx.WithContext(ctx).Raw(relationshipCorrectionSubmissionSelectSQL+`
		WHERE submission.team_id = ?::uuid
		  AND submission.owner_profile_id = ?::uuid
		  AND submission.submission_id = ?::uuid
		`+lockClause+`
	`, teamID, ownerProfileID, submissionID).Row())
}

func loadRelationshipCorrectionByIdempotency(
	ctx context.Context,
	tx *gorm.DB,
	teamID, ownerProfileID, idempotencyKey string,
) (*relationshipCorrectionSubmissionRow, error) {
	return scanRelationshipCorrectionSubmission(tx.WithContext(ctx).Raw(relationshipCorrectionSubmissionSelectSQL+`
		WHERE submission.team_id = ?::uuid
		  AND submission.owner_profile_id = ?::uuid
		  AND submission.idempotency_key = ?
		LIMIT 1
	`, teamID, ownerProfileID, idempotencyKey).Row())
}

func scanRelationshipCorrectionSubmission(row *sql.Row) (*relationshipCorrectionSubmissionRow, error) {
	var loaded relationshipCorrectionSubmissionRow
	var patchJSON, supportsJSON, candidatesJSON, selectionJSON []byte
	var expiresAt sql.NullTime
	if err := row.Scan(
		&loaded.TeamID, &loaded.SubmissionID, &loaded.OwnerProfileID, &loaded.SpaceID, &loaded.RelationshipID,
		&loaded.ExpectedVersion, &loaded.RequestHash, &patchJSON, &supportsJSON, &loaded.Reason,
		&loaded.IdempotencyKey, &loaded.ConfirmationIdempotency, &loaded.ConfirmationRequestHash,
		&loaded.ProcessingState, &loaded.ConfirmationRound, &loaded.ConfirmationToken, &expiresAt,
		&candidatesJSON, &selectionJSON, &loaded.SuccessorRelationshipID, &loaded.ReusedSuccessor,
		&loaded.ErrorCode, &loaded.ErrorMessage, &loaded.SuccessorVersion, &loaded.SearchState,
	); errors.Is(err, sql.ErrNoRows) {
		return nil, gorm.ErrRecordNotFound
	} else if err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		value := expiresAt.Time.UTC()
		loaded.ConfirmationExpiresAt = &value
	}
	if err := json.Unmarshal(patchJSON, &loaded.Patch); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(supportsJSON, &loaded.Supports); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(candidatesJSON, &loaded.Candidates); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(selectionJSON, &loaded.Selection); err != nil {
		return nil, err
	}
	return &loaded, nil
}
