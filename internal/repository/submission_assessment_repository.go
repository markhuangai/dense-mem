package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

var (
	ErrSubmissionAssessmentNotFound        = errors.New("submission assessment not found")
	ErrSubmissionAssessorAttemptConsumed   = errors.New("submission assessor attempt already consumed")
	ErrSubmissionAssessmentScopeMismatch   = errors.New("submission assessment does not match placement run")
	ErrSubmissionPredicateRegistrationHeld = errors.New("submission predicate registration requires review")
	ErrSubmissionAssessmentNonPromotable   = errors.New("submission assessment is not promotable")
)

var _ SubmissionAssessmentRepository = (*LedgerRepositoryImpl)(nil)

func (r *LedgerRepositoryImpl) LoadSubmissionAssessment(
	ctx context.Context,
	input LoadSubmissionAssessmentInput,
) (*SubmissionAssessment, error) {
	input = normalizeLoadSubmissionAssessmentInput(input)
	if err := validateLoadSubmissionAssessmentInput(input); err != nil {
		return nil, err
	}
	var assessment *SubmissionAssessment
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		loaded, err := loadSubmissionAssessment(ctx, tx, input.TeamID, input.OwnerProfileID, input.PlacementRunID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSubmissionAssessmentNotFound
		}
		if err != nil {
			return err
		}
		assessment = loaded
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("submission assessment load: %w", err)
	}
	return assessment, nil
}

func (r *LedgerRepositoryImpl) ReserveSubmissionAssessorAttempt(
	ctx context.Context,
	input ReserveSubmissionAssessorAttemptInput,
) (bool, error) {
	input.SubmissionAssessmentRunScope = normalizeSubmissionAssessmentRunScope(input.SubmissionAssessmentRunScope)
	if err := validateSubmissionAssessmentRunScope(input.SubmissionAssessmentRunScope); err != nil {
		return false, err
	}
	reserved := false
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		result := tx.WithContext(ctx).Exec(`
			UPDATE placement_runs
			SET assessor_attempt_id = gen_random_uuid(),
			    assessor_attempted_at = now(),
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND ingest_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND status = 'processing'
			  AND worker_id = ?
			  AND attempts = ?
			  AND lease_until > clock_timestamp()
			  AND assessor_attempt_id IS NULL
		`, input.TeamID, input.OwnerProfileID, input.IngestID, input.PlacementRunID,
			input.WorkerID, input.ExpectedAttempts)
		if result.Error != nil {
			return result.Error
		}
		reserved = result.RowsAffected == 1
		if reserved {
			return nil
		}
		var consumed bool
		if err := tx.WithContext(ctx).Raw(`
			SELECT assessor_attempt_id IS NOT NULL
			FROM placement_runs
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND ingest_id = ?::uuid
			  AND placement_run_id = ?::uuid
		`, input.TeamID, input.OwnerProfileID, input.IngestID, input.PlacementRunID).Row().Scan(&consumed); err != nil {
			return err
		}
		if consumed {
			return nil
		}
		return ErrPlacementLeaseLost
	})
	if err != nil {
		return false, fmt.Errorf("reserve submission assessor attempt: %w", err)
	}
	return reserved, nil
}

func (r *LedgerRepositoryImpl) PersistSubmissionAssessment(
	ctx context.Context,
	input PersistSubmissionAssessmentInput,
) (*SubmissionAssessment, bool, error) {
	input = normalizePersistSubmissionAssessmentInput(input)
	if err := validatePersistSubmissionAssessmentInput(input); err != nil {
		return nil, false, err
	}
	var assessment *SubmissionAssessment
	existing := false
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		inserted, err := insertSubmissionAssessment(ctx, tx, input)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if inserted != nil {
			assessment = inserted
			return nil
		}
		existing = true
		loaded, err := loadSubmissionAssessment(ctx, tx, input.TeamID, input.OwnerProfileID, input.PlacementRunID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSubmissionAssessmentNotFound
		}
		if err != nil {
			return err
		}
		assessment = loaded
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("submission assessment persist: %w", err)
	}
	return assessment, existing, nil
}

func (r *LedgerRepositoryImpl) AppendSubmissionAssessmentRevision(
	ctx context.Context,
	input AppendSubmissionAssessmentRevisionInput,
) (*SubmissionAssessment, bool, error) {
	input = normalizeAppendSubmissionAssessmentRevisionInput(input)
	if err := validateAppendSubmissionAssessmentRevisionInput(input); err != nil {
		return nil, false, err
	}
	var assessment *SubmissionAssessment
	existing := false
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockSubmissionAssessmentRun(ctx, tx, input.SubmissionAssessmentRunScope); err != nil {
			return err
		}
		current, err := loadSubmissionAssessment(ctx, tx, input.TeamID, input.OwnerProfileID, input.PlacementRunID)
		if err != nil {
			return err
		}
		if current.AssessmentID != input.AssessmentID {
			return ErrSubmissionAssessmentScopeMismatch
		}
		if current.RevisionNumber > 0 && current.ResponseHash == input.ResponseHash && current.ProviderTurns == input.ProviderTurns {
			assessment = current
			existing = true
			return nil
		}
		if err := insertSubmissionAssessmentRevision(ctx, tx, input, current.RevisionNumber+1); err != nil {
			return err
		}
		assessment, err = loadSubmissionAssessment(ctx, tx, input.TeamID, input.OwnerProfileID, input.PlacementRunID)
		return err
	})
	if err != nil {
		return nil, false, fmt.Errorf("submission assessment revision append: %w", err)
	}
	return assessment, existing, nil
}

func normalizeSubmissionAssessmentRunScope(scope SubmissionAssessmentRunScope) SubmissionAssessmentRunScope {
	scope.TeamID = strings.TrimSpace(scope.TeamID)
	scope.OwnerProfileID = strings.TrimSpace(scope.OwnerProfileID)
	scope.IngestID = strings.TrimSpace(scope.IngestID)
	scope.PlacementRunID = strings.TrimSpace(scope.PlacementRunID)
	scope.CorrelationID = strings.TrimSpace(scope.CorrelationID)
	scope.WorkerID = strings.TrimSpace(scope.WorkerID)
	return scope
}

func validateSubmissionAssessmentRunScope(scope SubmissionAssessmentRunScope) error {
	for label, value := range map[string]string{
		"team_id":          scope.TeamID,
		"owner_profile_id": scope.OwnerProfileID,
		"ingest_id":        scope.IngestID,
		"placement_run_id": scope.PlacementRunID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if scope.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if scope.ExpectedAttempts < 1 {
		return errors.New("expected_attempts must be greater than zero")
	}
	return nil
}

func normalizeLoadSubmissionAssessmentInput(input LoadSubmissionAssessmentInput) LoadSubmissionAssessmentInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.PlacementRunID = strings.TrimSpace(input.PlacementRunID)
	return input
}

func validateLoadSubmissionAssessmentInput(input LoadSubmissionAssessmentInput) error {
	for label, value := range map[string]string{
		"team_id":          input.TeamID,
		"owner_profile_id": input.OwnerProfileID,
		"placement_run_id": input.PlacementRunID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	return nil
}

func normalizePersistSubmissionAssessmentInput(input PersistSubmissionAssessmentInput) PersistSubmissionAssessmentInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.PlacementRunID = strings.TrimSpace(input.PlacementRunID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.AssessorContractVersion = strings.TrimSpace(input.AssessorContractVersion)
	input.Model = strings.TrimSpace(input.Model)
	input.Tokenizer = strings.TrimSpace(input.Tokenizer)
	input.ResponseHash = strings.TrimSpace(input.ResponseHash)
	if input.ProviderTurns == 0 {
		input.ProviderTurns = 1
	}
	if input.ValidatedAt.IsZero() {
		input.ValidatedAt = time.Now().UTC()
	} else {
		input.ValidatedAt = input.ValidatedAt.UTC()
	}
	return input
}

func validatePersistSubmissionAssessmentInput(input PersistSubmissionAssessmentInput) error {
	for label, value := range map[string]string{
		"team_id":          input.TeamID,
		"owner_profile_id": input.OwnerProfileID,
		"ingest_id":        input.IngestID,
		"placement_run_id": input.PlacementRunID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	for label, value := range map[string]string{
		"request_id":                input.RequestID,
		"assessor_contract_version": input.AssessorContractVersion,
		"model":                     input.Model,
		"tokenizer":                 input.Tokenizer,
		"response_hash":             input.ResponseHash,
	} {
		if value == "" {
			return fmt.Errorf("%s is required", label)
		}
	}
	if input.ProviderTurns < 1 || input.ProviderTurns > 5 {
		return errors.New("provider_turns must be between 1 and 5")
	}
	if input.InputTokens < 0 || input.OutputTokens < 0 || input.CandidateContextTokens < 0 {
		return errors.New("assessment token counts must be non-negative")
	}
	if !jsonObject(input.NormalizedResponse) {
		return errors.New("normalized_response must be a JSON object")
	}
	return nil
}

func normalizeAppendSubmissionAssessmentRevisionInput(input AppendSubmissionAssessmentRevisionInput) AppendSubmissionAssessmentRevisionInput {
	input.SubmissionAssessmentRunScope = normalizeSubmissionAssessmentRunScope(input.SubmissionAssessmentRunScope)
	input.AssessmentID = strings.TrimSpace(input.AssessmentID)
	input.ResponseHash = strings.TrimSpace(input.ResponseHash)
	if input.ValidatedAt.IsZero() {
		input.ValidatedAt = time.Now().UTC()
	} else {
		input.ValidatedAt = input.ValidatedAt.UTC()
	}
	return input
}

func validateAppendSubmissionAssessmentRevisionInput(input AppendSubmissionAssessmentRevisionInput) error {
	if err := validateSubmissionAssessmentRunScope(input.SubmissionAssessmentRunScope); err != nil {
		return err
	}
	if _, err := uuid.Parse(input.AssessmentID); err != nil {
		return fmt.Errorf("assessment_id is required: %w", err)
	}
	if input.ProviderTurns < 1 || input.ProviderTurns > 5 {
		return errors.New("provider_turns must be between 1 and 5")
	}
	if input.InputTokens < 0 || input.OutputTokens < 0 || input.CandidateContextTokens < 0 {
		return errors.New("assessment revision token counts must be non-negative")
	}
	if input.ResponseHash == "" {
		return errors.New("response_hash is required")
	}
	if !jsonObject(input.NormalizedResponse) {
		return errors.New("normalized_response must be a JSON object")
	}
	return nil
}

func insertSubmissionAssessment(
	ctx context.Context,
	tx *gorm.DB,
	input PersistSubmissionAssessmentInput,
) (*SubmissionAssessment, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO placement_assessments (
		    team_id, assessment_scope, placement_run_id, ingest_id, owner_profile_id,
			    request_id, assessor_contract_version, model, tokenizer,
			    provider_turns, input_tokens, output_tokens, candidate_context_tokens,
		    candidate_context_truncated, normalized_response, response_hash, validated_at,
		    space_id, space_generation
		) VALUES (
		    ?::uuid, 'submission', ?::uuid, ?::uuid, ?::uuid,
			    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?
		    ,(SELECT run.space_id FROM placement_runs AS run
		      WHERE run.team_id = ?::uuid AND run.placement_run_id = ?::uuid),
		     (SELECT run.space_generation FROM placement_runs AS run
		      WHERE run.team_id = ?::uuid AND run.placement_run_id = ?::uuid)
		)
		ON CONFLICT (team_id, placement_run_id) WHERE assessment_scope = 'submission' DO NOTHING
		RETURNING team_id::text, assessment_id::text, owner_profile_id::text,
		          ingest_id::text, placement_run_id::text, request_id,
			          assessor_contract_version, model, tokenizer, 0 AS revision_number, provider_turns,
			          input_tokens, output_tokens, candidate_context_tokens,
		          candidate_context_truncated, normalized_response, response_hash,
		          validated_at, created_at
	`, input.TeamID, input.PlacementRunID, input.IngestID, input.OwnerProfileID,
		input.RequestID, input.AssessorContractVersion, input.Model, input.Tokenizer, input.ProviderTurns,
		input.InputTokens, input.OutputTokens, input.CandidateContextTokens,
		input.CandidateContextTruncated, string(input.NormalizedResponse), input.ResponseHash,
		input.ValidatedAt, input.TeamID, input.PlacementRunID, input.TeamID, input.PlacementRunID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	assessment, err := scanSubmissionAssessment(rows)
	if err != nil {
		return nil, err
	}
	return assessment, rows.Err()
}

func insertSubmissionAssessmentRevision(
	ctx context.Context,
	tx *gorm.DB,
	input AppendSubmissionAssessmentRevisionInput,
	revisionNumber int,
) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO submission_assessment_response_revisions (
		    team_id, assessment_id, ingest_id, placement_run_id, owner_profile_id,
		    revision_number, provider_turns, input_tokens, output_tokens, candidate_context_tokens,
		    candidate_context_truncated, normalized_response, response_hash, validated_at,
		    space_id, space_generation
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid,
		    ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?,
		    (SELECT run.space_id FROM placement_runs AS run
		      WHERE run.team_id = ?::uuid AND run.placement_run_id = ?::uuid),
		    (SELECT run.space_generation FROM placement_runs AS run
		      WHERE run.team_id = ?::uuid AND run.placement_run_id = ?::uuid)
		)
	`, input.TeamID, input.AssessmentID, input.IngestID, input.PlacementRunID, input.OwnerProfileID,
		revisionNumber, input.ProviderTurns, input.InputTokens, input.OutputTokens, input.CandidateContextTokens,
		input.CandidateContextTruncated, string(input.NormalizedResponse), input.ResponseHash, input.ValidatedAt,
		input.TeamID, input.PlacementRunID, input.TeamID, input.PlacementRunID).Error
}

func loadSubmissionAssessment(
	ctx context.Context,
	tx *gorm.DB,
	teamID, ownerProfileID, placementRunID string,
) (*SubmissionAssessment, error) {
	rows, err := tx.WithContext(ctx).Raw(`
			SELECT assessment.team_id::text, assessment.assessment_id::text, assessment.owner_profile_id::text,
			       assessment.ingest_id::text, assessment.placement_run_id::text, assessment.request_id,
			       assessment.assessor_contract_version, assessment.model, assessment.tokenizer,
			       COALESCE(revision.revision_number, 0),
			       COALESCE(revision.provider_turns, assessment.provider_turns),
			       COALESCE(revision.input_tokens, assessment.input_tokens),
			       COALESCE(revision.output_tokens, assessment.output_tokens),
			       COALESCE(revision.candidate_context_tokens, assessment.candidate_context_tokens),
			       COALESCE(revision.candidate_context_truncated, assessment.candidate_context_truncated),
			       COALESCE(revision.normalized_response, assessment.normalized_response),
			       COALESCE(revision.response_hash, assessment.response_hash),
			       COALESCE(revision.validated_at, assessment.validated_at), assessment.created_at
			FROM placement_assessments AS assessment
			LEFT JOIN LATERAL (
			    SELECT revision_number, provider_turns, input_tokens, output_tokens, candidate_context_tokens,
			           candidate_context_truncated, normalized_response, response_hash, validated_at
			    FROM submission_assessment_response_revisions
			    WHERE team_id = assessment.team_id AND assessment_id = assessment.assessment_id
			    ORDER BY revision_number DESC
			    LIMIT 1
			) AS revision ON true
			WHERE assessment.team_id = ?::uuid
			  AND assessment.owner_profile_id = ?::uuid
			  AND assessment.placement_run_id = ?::uuid
			  AND assessment.assessment_scope = 'submission'
		LIMIT 1
	`, teamID, ownerProfileID, placementRunID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	assessment, err := scanSubmissionAssessment(rows)
	if err != nil {
		return nil, err
	}
	return assessment, rows.Err()
}

func scanSubmissionAssessment(rows *sql.Rows) (*SubmissionAssessment, error) {
	assessment := SubmissionAssessment{}
	var response []byte
	if err := rows.Scan(
		&assessment.TeamID,
		&assessment.AssessmentID,
		&assessment.OwnerProfileID,
		&assessment.IngestID,
		&assessment.PlacementRunID,
		&assessment.RequestID,
		&assessment.AssessorContractVersion,
		&assessment.Model,
		&assessment.Tokenizer,
		&assessment.RevisionNumber,
		&assessment.ProviderTurns,
		&assessment.InputTokens,
		&assessment.OutputTokens,
		&assessment.CandidateContextTokens,
		&assessment.CandidateContextTruncated,
		&response,
		&assessment.ResponseHash,
		&assessment.ValidatedAt,
		&assessment.CreatedAt,
	); err != nil {
		return nil, err
	}
	if !jsonObject(response) {
		return nil, errors.New("stored normalized_response is not a JSON object")
	}
	assessment.NormalizedResponse = append(json.RawMessage(nil), response...)
	return &assessment, nil
}

func (r *LedgerRepositoryImpl) CompleteSubmissionAssessment(
	ctx context.Context,
	input CompleteSubmissionAssessmentInput,
) (*CompleteSubmissionAssessmentResult, error) {
	input = normalizeCompleteSubmissionAssessmentInput(input)
	if err := validateCompleteSubmissionAssessmentInput(input); err != nil {
		return nil, err
	}
	result := &CompleteSubmissionAssessmentResult{Status: input.Status}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockSubmissionAssessmentRun(ctx, tx, input.SubmissionAssessmentRunScope); err != nil {
			return err
		}
		items, err := loadLockedSubmissionAssessmentItems(ctx, tx, input.SubmissionAssessmentRunScope)
		if err != nil {
			return err
		}
		itemByFragment := make(map[string]submissionLockedItem, len(items))
		for _, item := range items {
			itemByFragment[item.FragmentID] = item
		}
		for _, quarantine := range input.SecurityQuarantines {
			if _, ok := itemByFragment[quarantine.FragmentID]; !ok {
				return errors.New("submission security quarantine fragment is outside the placement run")
			}
			securityInput := SecurityEventInput{
				TeamID:             input.TeamID,
				OwnerProfileID:     input.OwnerProfileID,
				IngestID:           input.IngestID,
				FragmentID:         quarantine.FragmentID,
				SecurityEventDraft: quarantine.SecurityEventDraft,
			}
			if err := ensureEvidenceEventOwnership(ctx, tx, securityInput); err != nil {
				return err
			}
			if _, err := insertSecurityEvent(ctx, tx, securityInput); err != nil {
				return err
			}
			if err := insertEvidenceQuarantine(ctx, tx, CreateIngestInput{
				TeamID:         input.TeamID,
				OwnerProfileID: input.OwnerProfileID,
			}, input.IngestID, quarantine.FragmentID, quarantine.Reason); err != nil {
				return err
			}
		}
		if len(input.SecurityQuarantines) > 0 {
			if err := storeSubmissionQuarantinePayload(ctx, tx, input.SubmissionAssessmentRunScope); err != nil {
				return err
			}
		}
		itemStatus, itemCategory, runStatus := submissionTerminalStatuses(input.Status, input.Category)
		payload := terminalPlacementPayload(input.Payload, input.Status)
		payload["submission_atomic"] = true
		for _, item := range items {
			itemPayload := cloneSubmissionPayload(payload)
			itemPayload["evidence_index"] = item.EvidenceIndex
			outcomeID, err := insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
				TeamID:          input.TeamID,
				OwnerProfileID:  input.OwnerProfileID,
				PlacementRunID:  input.PlacementRunID,
				PlacementItemID: item.PlacementItemID,
				OutcomeKind:     input.OutcomeKind,
				Status:          input.Status,
				Payload:         itemPayload,
			})
			if err != nil {
				return err
			}
			if err := updatePlacementItemOutcome(ctx, tx, PlacementOutcomeInput{
				TeamID:             input.TeamID,
				OwnerProfileID:     input.OwnerProfileID,
				PlacementItemID:    item.PlacementItemID,
				UpdateItemStatus:   itemStatus,
				UpdateItemCategory: itemCategory,
				Payload:            itemPayload,
			}); err != nil {
				return err
			}
			result.OutcomeIDs = append(result.OutcomeIDs, outcomeID)
		}
		if err := insertSubmissionRelationshipResults(ctx, tx, input.SubmissionAssessmentRunScope, input.RelationshipResults, nil); err != nil {
			return err
		}
		firstDisposition, err := completeSubmissionPlacementRun(ctx, tx, input.SubmissionAssessmentRunScope, runStatus, "")
		if err != nil {
			return err
		}
		result.FirstDisposition = firstDisposition
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("submission assessment completion: %w", err)
	}
	return result, nil
}

// storeSubmissionQuarantinePayload moves the exact provider-facing material
// into the system-only raw-payload retention table before normal evidence is
// tombstoned. The append-only source rows remain restricted audit history;
// purge removes only this raw-payload copy while IDs and hashes preserve lineage.
func storeSubmissionQuarantinePayload(ctx context.Context, tx *gorm.DB, scope SubmissionAssessmentRunScope) error {
	var proposal, evidence, assessorResponse []byte
	row := tx.WithContext(ctx).Raw(`
		SELECT COALESCE(ingest.proposal, '{}'::jsonb),
			   COALESCE((
			       SELECT COALESCE(revision.normalized_response, assessment.normalized_response)
			       FROM placement_assessments AS assessment
			       LEFT JOIN LATERAL (
			           SELECT response_revision.normalized_response
			           FROM submission_assessment_response_revisions AS response_revision
			           WHERE response_revision.team_id = assessment.team_id
			             AND response_revision.assessment_id = assessment.assessment_id
			           ORDER BY response_revision.revision_number DESC
			           LIMIT 1
			       ) AS revision ON true
			       WHERE assessment.team_id = run.team_id
		             AND assessment.placement_run_id = run.placement_run_id
		             AND assessment.ingest_id = run.ingest_id
		             AND assessment.owner_profile_id = run.owner_profile_id
		             AND assessment.assessment_scope = 'submission'
		           ORDER BY assessment.created_at DESC, assessment.assessment_id DESC
		           LIMIT 1
		       ), '{}'::jsonb),
		       COALESCE((
		           SELECT jsonb_agg(jsonb_build_object(
		               'evidence_id', fragment.fragment_id::text,
		               'evidence_index', fragment.evidence_index,
		               'content', fragment.content,
		               'content_hash', fragment.content_hash,
		               'source_type', fragment.source_type,
		               'source_ref', fragment.source_ref,
		               'metadata', fragment.metadata
		           ) ORDER BY fragment.evidence_index)
		           FROM evidence_fragments AS fragment
		           WHERE fragment.team_id = run.team_id
		             AND fragment.ingest_id = run.ingest_id
		             AND fragment.owner_profile_id = run.owner_profile_id
		       ), '[]'::jsonb)
		FROM placement_runs AS run
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = run.team_id AND ingest.ingest_id = run.ingest_id
		WHERE run.team_id = ?::uuid
		  AND run.placement_run_id = ?::uuid
		  AND run.ingest_id = ?::uuid
		  AND run.owner_profile_id = ?::uuid
	`, scope.TeamID, scope.PlacementRunID, scope.IngestID, scope.OwnerProfileID).Row()
	if err := row.Scan(&proposal, &assessorResponse, &evidence); err != nil {
		return err
	}
	payload := map[string]json.RawMessage{
		"proposal":          append(json.RawMessage(nil), proposal...),
		"evidence":          append(json.RawMessage(nil), evidence...),
		"assessor_response": append(json.RawMessage(nil), assessorResponse...),
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(canonical)
	// The profile policy permits writes but not raw-payload reads, so ON CONFLICT
	// cannot be used here: PostgreSQL's arbiter would require a SELECT policy.
	// Completion holds the run lock and commits atomically, making this plain
	// insert idempotent at the terminal transition boundary.
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO submission_quarantine_payloads (
		    team_id, placement_run_id, ingest_id, owner_profile_id,
		    proposal, evidence, assessor_response, payload_sha256, space_id, space_generation,
		    quarantined_at, expires_at
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?::uuid,
		    ?::jsonb, ?::jsonb, ?::jsonb, ?,
		    (SELECT run.space_id FROM placement_runs AS run
		     WHERE run.team_id = ?::uuid AND run.placement_run_id = ?::uuid),
		    (SELECT run.space_generation FROM placement_runs AS run
		     WHERE run.team_id = ?::uuid AND run.placement_run_id = ?::uuid),
		    now(), now() + interval '24 hours'
		)
	`, scope.TeamID, scope.PlacementRunID, scope.IngestID, scope.OwnerProfileID,
		string(proposal), string(evidence), string(assessorResponse), hex.EncodeToString(sum[:]),
		scope.TeamID, scope.PlacementRunID, scope.TeamID, scope.PlacementRunID).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO submission_quarantine_tombstones (
		    team_id, fragment_id, ingest_id, owner_profile_id, content_hash, space_id, space_generation
		)
		SELECT fragment.team_id, fragment.fragment_id, fragment.ingest_id,
		       fragment.owner_profile_id, fragment.content_hash,
		       fragment.space_id, fragment.space_generation
		FROM evidence_fragments AS fragment
		WHERE fragment.team_id = ?::uuid
		  AND fragment.ingest_id = ?::uuid
		  AND fragment.owner_profile_id = ?::uuid
	`, scope.TeamID, scope.IngestID, scope.OwnerProfileID).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		UPDATE placement_runs
		SET quarantine_expires_at = COALESCE(quarantine_expires_at, now() + interval '24 hours'),
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND owner_profile_id = ?::uuid
	`, scope.TeamID, scope.PlacementRunID, scope.OwnerProfileID).Error
}

func (r *LedgerRepositoryImpl) RequeueSubmissionAssessment(
	ctx context.Context,
	input RequeueSubmissionAssessmentInput,
) (*RequeueSubmissionAssessmentResult, error) {
	input = normalizeRequeueSubmissionAssessmentInput(input)
	if err := validateRequeueSubmissionAssessmentInput(input); err != nil {
		return nil, err
	}
	result := &RequeueSubmissionAssessmentResult{Status: string(domain.SemanticReviewRetryable)}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockSubmissionAssessmentRun(ctx, tx, input.SubmissionAssessmentRunScope); err != nil {
			return err
		}
		items, err := loadLockedSubmissionAssessmentItems(ctx, tx, input.SubmissionAssessmentRunScope)
		if err != nil {
			return err
		}
		payload := retryPlacementPayload(input.Payload)
		payload["submission_atomic"] = true
		for _, item := range items {
			itemPayload := cloneSubmissionPayload(payload)
			itemPayload["evidence_index"] = item.EvidenceIndex
			outcomeID, err := insertPlacementOutcome(ctx, tx, PlacementOutcomeInput{
				TeamID:          input.TeamID,
				OwnerProfileID:  input.OwnerProfileID,
				PlacementRunID:  input.PlacementRunID,
				PlacementItemID: item.PlacementItemID,
				OutcomeKind:     input.OutcomeKind,
				Status:          string(domain.SemanticReviewRetryable),
				Payload:         itemPayload,
			})
			if err != nil {
				return err
			}
			if err := updatePlacementItemOutcome(ctx, tx, PlacementOutcomeInput{
				TeamID:           input.TeamID,
				OwnerProfileID:   input.OwnerProfileID,
				PlacementItemID:  item.PlacementItemID,
				UpdateItemStatus: string(domain.PlacementRunQueued),
				Payload:          itemPayload,
			}); err != nil {
				return err
			}
			result.OutcomeIDs = append(result.OutcomeIDs, outcomeID)
		}
		retryDelay := placementEffectiveRetryDelay(input.ExpectedAttempts, input.PlacementRunID, input.RetryAfter)
		var nextAttemptAt time.Time
		resultUpdate := tx.WithContext(ctx).Raw(`
			UPDATE placement_runs
			SET status = `+placementRunGuardedStatusCase+`,
			    worker_id = '',
			    lease_until = NULL,
			    assessor_attempt_id = CASE WHEN ? THEN NULL ELSE assessor_attempt_id END,
			    assessor_attempted_at = CASE WHEN ? THEN NULL ELSE assessor_attempted_at END,
			    available_at = now() + (? * interval '1 second'),
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND owner_profile_id = ?::uuid
			  AND ingest_id = ?::uuid
			  AND placement_run_id = ?::uuid
			  AND status = 'processing'
			  AND worker_id = ?
			  AND attempts = ?
			  AND lease_until > clock_timestamp()
			RETURNING available_at
		`, input.ReleaseAssessorAttempt, input.ReleaseAssessorAttempt, int(retryDelay/time.Second),
			input.TeamID, input.OwnerProfileID, input.IngestID, input.PlacementRunID,
			input.WorkerID, input.ExpectedAttempts).Row().Scan(&nextAttemptAt)
		if errors.Is(resultUpdate, sql.ErrNoRows) {
			return ErrPlacementLeaseLost
		}
		if resultUpdate != nil {
			return resultUpdate
		}
		nextAttemptAt = nextAttemptAt.UTC()
		result.NextAttemptAt = &nextAttemptAt
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("submission assessment retry: %w", err)
	}
	return result, nil
}

func retryPlacementPayload(base map[string]any) map[string]any {
	payload := map[string]any{
		"contract_version": domain.ContractVersion,
		"status":           string(domain.SemanticReviewRetryable),
	}
	for key, value := range base {
		payload[key] = value
	}
	return payload
}

func terminalPlacementPayload(base map[string]any, status string) map[string]any {
	payload := map[string]any{
		"contract_version": domain.ContractVersion,
		"status":           status,
	}
	for key, value := range base {
		payload[key] = value
	}
	return payload
}

func normalizeCompleteSubmissionAssessmentInput(input CompleteSubmissionAssessmentInput) CompleteSubmissionAssessmentInput {
	input.SubmissionAssessmentRunScope = normalizeSubmissionAssessmentRunScope(input.SubmissionAssessmentRunScope)
	input.OutcomeKind = strings.TrimSpace(input.OutcomeKind)
	input.Status = strings.TrimSpace(input.Status)
	input.Category = strings.TrimSpace(input.Category)
	if input.OutcomeKind == "" {
		input.OutcomeKind = "submission_assessment_terminal"
	}
	for i := range input.SecurityQuarantines {
		quarantine := &input.SecurityQuarantines[i]
		quarantine.FragmentID = strings.TrimSpace(quarantine.FragmentID)
		quarantine.SecurityEventDraft = normalizeSecurityEventDraft(quarantine.SecurityEventDraft)
	}
	return input
}

func validateCompleteSubmissionAssessmentInput(input CompleteSubmissionAssessmentInput) error {
	if err := validateSubmissionAssessmentRunScope(input.SubmissionAssessmentRunScope); err != nil {
		return err
	}
	switch input.Status {
	case string(domain.SemanticReviewTerminalFailure), string(domain.SemanticReviewQuarantined), string(domain.SemanticReviewRejected), string(domain.SemanticReviewSuperseded):
	default:
		return fmt.Errorf("unsupported submission terminal status %q", input.Status)
	}
	if input.Status == string(domain.SemanticReviewQuarantined) && len(input.SecurityQuarantines) == 0 {
		return errors.New("submission quarantine requires at least one security event")
	}
	if input.Status != string(domain.SemanticReviewQuarantined) && len(input.SecurityQuarantines) > 0 {
		return errors.New("submission security events require quarantined terminal status")
	}
	for _, quarantine := range input.SecurityQuarantines {
		securityInput := SecurityEventInput{
			TeamID:             input.TeamID,
			OwnerProfileID:     input.OwnerProfileID,
			IngestID:           input.IngestID,
			FragmentID:         quarantine.FragmentID,
			SecurityEventDraft: quarantine.SecurityEventDraft,
		}
		if err := validateSecurityEventInput(securityInput); err != nil {
			return fmt.Errorf("submission security quarantine: %w", err)
		}
		if securityInput.Decision != "quarantine" {
			return errors.New("submission security quarantine requires quarantine decision")
		}
	}
	return nil
}

func normalizeRequeueSubmissionAssessmentInput(input RequeueSubmissionAssessmentInput) RequeueSubmissionAssessmentInput {
	input.SubmissionAssessmentRunScope = normalizeSubmissionAssessmentRunScope(input.SubmissionAssessmentRunScope)
	input.OutcomeKind = strings.TrimSpace(input.OutcomeKind)
	if input.OutcomeKind == "" {
		input.OutcomeKind = "submission_assessment_retry"
	}
	if input.RetryAfter < 0 {
		input.RetryAfter = 0
	}
	if input.RetryAfter > placementRetryMaxDelay {
		input.RetryAfter = placementRetryMaxDelay
	}
	return input
}

func validateRequeueSubmissionAssessmentInput(input RequeueSubmissionAssessmentInput) error {
	return validateSubmissionAssessmentRunScope(input.SubmissionAssessmentRunScope)
}

func submissionTerminalStatuses(status, category string) (string, string, string) {
	switch status {
	case string(domain.SemanticReviewQuarantined):
		return "quarantined", "quarantined", string(domain.PlacementRunQuarantined)
	case string(domain.SemanticReviewTerminalFailure):
		return "failed", "failed", string(domain.PlacementRunFailed)
	case string(domain.SemanticReviewSuperseded):
		return "failed", "failed", string(domain.PlacementRunFailed)
	case string(domain.SemanticReviewRejected):
		return "rejected", "rejected", string(domain.PlacementRunRejected)
	default:
		if category == "" {
			category = "candidate"
		}
		return string(domain.PlacementRunCompleted), category, string(domain.PlacementRunCompleted)
	}
}

func completeSubmissionPlacementRun(
	ctx context.Context,
	tx *gorm.DB,
	scope SubmissionAssessmentRunScope,
	status, message string,
) (*PlacementFirstDisposition, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		UPDATE placement_runs
		SET status = ?,
		    error = ?,
		    lease_until = NULL,
		    completed_at = now(),
		    quarantine_expires_at = CASE
		        WHEN ? = 'quarantined' THEN now() + interval '24 hours'
		        ELSE quarantine_expires_at
		    END,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND ingest_id = ?::uuid
		  AND placement_run_id = ?::uuid
		  AND status = 'processing'
		  AND worker_id = ?
		  AND attempts = ?
		  AND lease_until > clock_timestamp()
		RETURNING created_at, completed_at
	`, status, strings.TrimSpace(message), status, scope.TeamID, scope.OwnerProfileID, scope.IngestID,
		scope.PlacementRunID, scope.WorkerID, scope.ExpectedAttempts).Rows()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		closeErr := rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return nil, ErrPlacementLeaseLost
	}
	var createdAt, completedAt time.Time
	if err := rows.Scan(&createdAt, &completedAt); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return appendPlacementFirstDisposition(ctx, tx, scope.TeamID, scope.OwnerProfileID, scope.PlacementRunID, status, createdAt, completedAt)
}

func cloneSubmissionPayload(input map[string]any) map[string]any {
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
