package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func lockReplaceableQuarantinedSubmission(ctx context.Context, tx *gorm.DB, input CreateSubmissionInput) error {
	var status string
	err := tx.WithContext(ctx).Raw(`
		SELECT status
		FROM submission_runs
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
		FOR UPDATE
	`, input.TeamID, input.OwnerProfileID, input.ReplacesQuarantinedSubmissionID).Row().Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrSubmissionNotFound
	}
	if err != nil {
		return err
	}
	if status != string(domain.SubmissionQuarantined) {
		return ErrSubmissionConflict
	}
	return nil
}

func insertSubmissionRun(ctx context.Context, tx *gorm.DB, input CreateSubmissionInput) (string, bool, error) {
	if input.IdempotencyKey == "" {
		row := tx.WithContext(ctx).Raw(`
			INSERT INTO submission_runs (
				team_id, owner_profile_id, request_hash, source_summary, status, replaces_quarantined_submission_id
			) VALUES (?::uuid, ?::uuid, ?, ?, 'queued', NULLIF(?, '')::uuid)
			RETURNING submission_id::text
		`, input.TeamID, input.OwnerProfileID, input.RequestHash, input.SourceSummary, input.ReplacesQuarantinedSubmissionID).Row()
		var submissionID string
		if err := row.Scan(&submissionID); err != nil {
			return "", false, err
		}
		return submissionID, true, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH inserted AS (
			INSERT INTO submission_runs (
				team_id, owner_profile_id, idempotency_key, request_hash, source_summary, status,
				replaces_quarantined_submission_id
			) VALUES (?::uuid, ?::uuid, ?, ?, ?, 'queued', NULLIF(?, '')::uuid)
			ON CONFLICT (team_id, owner_profile_id, idempotency_key)
			WHERE idempotency_key <> ''
			DO NOTHING
			RETURNING submission_id::text, request_hash, true AS created
		)
		SELECT submission_id, request_hash, created FROM inserted
		UNION ALL
		SELECT submission_id::text, request_hash, false AS created
		FROM submission_runs
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey, input.RequestHash, input.SourceSummary,
		input.ReplacesQuarantinedSubmissionID, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Rows()
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", false, rows.Err()
	}
	var submissionID, requestHash string
	var created bool
	if err := rows.Scan(&submissionID, &requestHash, &created); err != nil {
		return "", false, err
	}
	if requestHash != input.RequestHash {
		return "", false, ErrSubmissionConflict
	}
	return submissionID, created, rows.Err()
}

func insertSubmissionProposal(ctx context.Context, tx *gorm.DB, teamID, ownerID, submissionID string, proposal map[string]any) error {
	payload, err := marshalJSON(proposal)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO submission_staged_proposals (team_id, submission_id, owner_profile_id, proposal)
		VALUES (?::uuid, ?::uuid, ?::uuid, ?::jsonb)
	`, teamID, submissionID, ownerID, string(payload)).Error
}

func insertStagedSubmissionEvidence(
	ctx context.Context,
	tx *gorm.DB,
	teamID, ownerID, submissionID string,
	index int,
	evidence SubmissionEvidenceInput,
) error {
	metadata, err := marshalJSON(evidence.Metadata)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO submission_staged_evidence (
			team_id, submission_id, owner_profile_id, evidence_index, content, content_hash,
			source_type, source_ref, source_group, authority, source_key, source_revision,
			previous_source_revision, supersedes_evidence_ids, idempotency_key, labels, metadata
		) VALUES (
			?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb
		)
	`, teamID, submissionID, ownerID, index, evidence.Content, sha256Hex(evidence.Content),
		evidence.SourceType, evidence.Source, evidence.SourceGroup, evidence.Authority, evidence.SourceKey,
		evidence.SourceRevision, evidence.PreviousSourceRevision, pqStringArray(evidence.SupersedesEvidenceIDs),
		evidence.IdempotencyKey, pqStringArray(evidence.Labels), string(metadata)).Error
}

func loadSubmission(
	ctx context.Context,
	tx *gorm.DB,
	teamID, ownerID, submissionID string,
	includeEvidence bool,
) (*Submission, error) {
	row := tx.WithContext(ctx).Raw(`
	SELECT submission_id::text, request_hash, source_summary, created_at, status, attempts, max_attempts, lease_until, worker_id,
		       error_code, COALESCE(canonical_ingest_id::text, ''),
		       COALESCE(replaces_quarantined_submission_id::text, ''), quarantine_expires_at
		FROM submission_runs
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
	`, teamID, ownerID, submissionID).Row()
	loaded := &Submission{TeamID: teamID, OwnerProfileID: ownerID, Proposal: map[string]any{}, Evidence: []SubmissionEvidence{}, Outcomes: []SubmissionOutcome{}}
	var leaseUntil, expiresAt sql.NullTime
	if err := row.Scan(&loaded.SubmissionID, &loaded.RequestHash, &loaded.SourceSummary, &loaded.CreatedAt, &loaded.Status, &loaded.Attempts, &loaded.MaxAttempts, &leaseUntil,
		&loaded.WorkerID, &loaded.ErrorCode, &loaded.CanonicalIngestID, &loaded.ReplacesQuarantinedSubmissionID, &expiresAt); err != nil {
		return nil, err
	}
	if leaseUntil.Valid {
		loaded.LeaseUntil = &leaseUntil.Time
	}
	if expiresAt.Valid {
		loaded.QuarantineExpiresAt = &expiresAt.Time
	}
	proposalRow := tx.WithContext(ctx).Raw(`
		SELECT proposal FROM submission_staged_proposals
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
	`, teamID, ownerID, submissionID).Row()
	var proposalRaw []byte
	if err := proposalRow.Scan(&proposalRaw); err == nil {
		if err := json.Unmarshal(proposalRaw, &loaded.Proposal); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if includeEvidence {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT evidence_index, content, content_hash, source_type, source_ref, source_group,
			       authority, source_key, source_revision, previous_source_revision,
			       supersedes_evidence_ids, idempotency_key, labels, metadata
			FROM submission_staged_evidence
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
			ORDER BY evidence_index
		`, teamID, ownerID, submissionID).Rows()
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var item SubmissionEvidence
			var supersedes, labels pq.StringArray
			var metadataRaw []byte
			if err := rows.Scan(&item.EvidenceIndex, &item.Content, &item.ContentHash, &item.SourceType,
				&item.Source, &item.SourceGroup, &item.Authority, &item.SourceKey, &item.SourceRevision,
				&item.PreviousSourceRevision, &supersedes, &item.IdempotencyKey, &labels, &metadataRaw); err != nil {
				return nil, err
			}
			item.SupersedesEvidenceIDs = append([]string(nil), []string(supersedes)...)
			item.Labels = append([]string(nil), []string(labels)...)
			item.Metadata = map[string]any{}
			if err := json.Unmarshal(metadataRaw, &item.Metadata); err != nil {
				return nil, err
			}
			loaded.Evidence = append(loaded.Evidence, item)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return loaded, nil
}

func loadSubmissionStatus(ctx context.Context, tx *gorm.DB, teamID, ownerID, submissionID string) (*SubmissionStatus, error) {
	row := tx.WithContext(ctx).Raw(`
		SELECT status, error_code, quarantine_expires_at, COALESCE(canonical_ingest_id::text, '')
		FROM submission_runs
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
	`, teamID, ownerID, submissionID).Row()
	result := &SubmissionStatus{SubmissionID: submissionID, Evidence: []SubmissionEvidenceStatus{}, RelationshipOutcomes: []SubmissionRelationshipOutcome{}, Errors: []SubmissionStatusError{}}
	var expiresAt sql.NullTime
	var errorCode string
	var canonicalIngestID string
	if err := row.Scan(&result.ProcessingState, &errorCode, &expiresAt, &canonicalIngestID); err != nil {
		return nil, err
	}
	if expiresAt.Valid {
		result.QuarantineExpiresAt = &expiresAt.Time
	}
	evidenceSearchStates := map[int]string{}
	if canonicalIngestID != "" {
		searchState, byEvidence, err := loadSubmissionSearchStates(ctx, tx, teamID, ownerID, canonicalIngestID)
		if err != nil {
			return nil, err
		}
		result.SearchState = searchState
		evidenceSearchStates = byEvidence
	} else {
		result.SearchState = string(domain.SearchProjectionNotRequired)
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT outcome_kind, evidence_index, proposal_id, status, reason_code, details, created_at
		FROM submission_outcomes
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
		ORDER BY created_at, outcome_id
	`, teamID, ownerID, submissionID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	evidenceByIndex := map[int]SubmissionEvidenceStatus{}
	for rows.Next() {
		var outcome SubmissionOutcome
		var index sql.NullInt64
		var detailsRaw []byte
		if err := rows.Scan(&outcome.OutcomeKind, &index, &outcome.ProposalID, &outcome.Status, &outcome.ReasonCode, &detailsRaw, &outcome.CreatedAt); err != nil {
			return nil, err
		}
		if len(detailsRaw) > 0 {
			if err := json.Unmarshal(detailsRaw, &outcome.Details); err != nil {
				return nil, err
			}
		}
		if index.Valid {
			item := SubmissionEvidenceStatus{EvidenceIndex: int(index.Int64), Status: outcome.Status, ReasonCode: outcome.ReasonCode, SearchState: statusDetailString(outcome.Details, "search_state")}
			if outcome.Status == "accepted" {
				if searchState, exists := evidenceSearchStates[item.EvidenceIndex]; exists {
					item.SearchState = searchState
				}
			}
			if item.SearchState == "" {
				item.SearchState = result.SearchState
			}
			evidenceByIndex[item.EvidenceIndex] = item
		}
		if outcome.ProposalID != "" {
			result.RelationshipOutcomes = append(result.RelationshipOutcomes, SubmissionRelationshipOutcome{
				ProposalID:     outcome.ProposalID,
				RelationshipID: statusDetailString(outcome.Details, "relationship_id"),
				Status:         outcome.Status,
				ReasonCode:     outcome.ReasonCode,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stageRows, err := tx.WithContext(ctx).Raw(`
		SELECT evidence_index FROM submission_staged_evidence
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
		ORDER BY evidence_index
	`, teamID, ownerID, submissionID).Rows()
	if err != nil {
		return nil, err
	}
	defer stageRows.Close()
	for stageRows.Next() {
		var index int
		if err := stageRows.Scan(&index); err != nil {
			return nil, err
		}
		if _, exists := evidenceByIndex[index]; !exists {
			evidenceByIndex[index] = SubmissionEvidenceStatus{EvidenceIndex: index, Status: result.ProcessingState, SearchState: result.SearchState}
		}
	}
	indices := make([]int, 0, len(evidenceByIndex))
	for index := range evidenceByIndex {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		result.Evidence = append(result.Evidence, evidenceByIndex[index])
	}
	if errorCode != "" {
		result.Errors = append(result.Errors, SubmissionStatusError{Code: errorCode})
	}
	return result, nil
}

func loadSubmissionSearchStates(
	ctx context.Context,
	tx *gorm.DB,
	teamID, ownerID, ingestID string,
) (string, map[int]string, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT item.evidence_index, COALESCE(document.search_state, 'failed')
		FROM placement_items AS item
		CROSS JOIN LATERAL jsonb_array_elements_text(
			CASE
				WHEN jsonb_typeof(item.result->'search_document_ids') = 'array'
					THEN item.result->'search_document_ids'
				ELSE '[]'::jsonb
			END
		) AS linked(search_document_id)
		LEFT JOIN search_documents AS document
		  ON document.team_id = item.team_id
		 AND document.owner_profile_id = item.owner_profile_id
		 AND document.search_document_id::text = linked.search_document_id
		WHERE item.team_id = ?::uuid
		  AND item.owner_profile_id = ?::uuid
		  AND item.ingest_id = ?::uuid
		ORDER BY item.evidence_index, linked.search_document_id
	`, teamID, ownerID, ingestID).Rows()
	if err != nil {
		return "", nil, err
	}
	defer rows.Close()
	statesByEvidence := map[int][]string{}
	allStates := []string{}
	for rows.Next() {
		var evidenceIndex int
		var state string
		if err := rows.Scan(&evidenceIndex, &state); err != nil {
			return "", nil, err
		}
		statesByEvidence[evidenceIndex] = append(statesByEvidence[evidenceIndex], state)
		allStates = append(allStates, state)
	}
	if err := rows.Err(); err != nil {
		return "", nil, err
	}
	result := make(map[int]string, len(statesByEvidence))
	for evidenceIndex, states := range statesByEvidence {
		result[evidenceIndex] = submissionSearchProjectionState(states)
	}
	return submissionSearchProjectionState(allStates), result, nil
}

func submissionSearchProjectionState(states []string) string {
	if len(states) == 0 {
		return string(domain.SearchProjectionNotRequired)
	}
	hasPending := false
	hasFailed := false
	hasCurrent := false
	for _, state := range states {
		switch strings.TrimSpace(state) {
		case string(domain.SearchProjectionPending):
			hasPending = true
		case string(domain.SearchProjectionFailed):
			hasFailed = true
		case string(domain.SearchProjectionCurrent):
			hasCurrent = true
		}
	}
	if hasPending {
		return string(domain.SearchProjectionPending)
	}
	if hasFailed {
		return string(domain.SearchProjectionFailed)
	}
	if hasCurrent {
		return string(domain.SearchProjectionCurrent)
	}
	return string(domain.SearchProjectionNotRequired)
}

func statusDetailString(details map[string]any, key string) string {
	if details == nil {
		return ""
	}
	value, ok := details[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func loadSubmissionAssessment(ctx context.Context, tx *gorm.DB, teamID, ownerID, submissionID string) (*SubmissionAssessment, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT assessment_id::text, submission_id::text, request_id, model, tokenizer,
		       input_tokens, output_tokens, candidate_context_tokens, candidate_context_truncated,
		       normalized_response, response_hash, validated_at
		FROM submission_assessments
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
	`, teamID, ownerID, submissionID).Rows()
	if err != nil {
		return nil, err
	}
	return scanSubmissionAssessment(rows)
}

func scanSubmissionAssessment(rows *sql.Rows) (*SubmissionAssessment, error) {
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	loaded := &SubmissionAssessment{}
	if err := rows.Scan(&loaded.AssessmentID, &loaded.SubmissionID, &loaded.RequestID, &loaded.Model, &loaded.Tokenizer,
		&loaded.InputTokens, &loaded.OutputTokens, &loaded.CandidateContextTokens, &loaded.CandidateContextTruncated,
		&loaded.NormalizedResponse, &loaded.ResponseHash, &loaded.ValidatedAt); err != nil {
		return nil, err
	}
	return loaded, rows.Err()
}

func requireSubmissionLease(ctx context.Context, tx *gorm.DB, teamID, ownerID, submissionID, workerID string, attempts int) error {
	var found int
	if err := tx.WithContext(ctx).Raw(`
		SELECT 1 FROM submission_runs
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
		  AND status = 'processing' AND worker_id = ? AND attempts = ? AND lease_until > clock_timestamp()
		FOR UPDATE
	`, teamID, ownerID, submissionID, workerID, attempts).Scan(&found).Error; err != nil {
		return err
	}
	if found != 1 {
		return ErrSubmissionLeaseConflict
	}
	return nil
}

func insertSubmissionOutcome(ctx context.Context, tx *gorm.DB, teamID, ownerID, submissionID string, evidenceIndex *int, proposalID, status, reasonCode string, details map[string]any) error {
	payload, err := marshalJSON(details)
	if err != nil {
		return err
	}
	var index any
	if evidenceIndex != nil {
		index = *evidenceIndex
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO submission_outcomes (
			team_id, submission_id, owner_profile_id, outcome_kind, evidence_index,
			proposal_id, status, reason_code, details
		) VALUES (?::uuid, ?::uuid, ?::uuid, 'submission', ?, ?, ?, ?, ?::jsonb)
	`, teamID, submissionID, ownerID, index, proposalID, status, reasonCode, string(payload)).Error
}

func insertSubmissionCompletionOutcomes(ctx context.Context, tx *gorm.DB, input CompleteSubmissionInput) error {
	for _, outcome := range input.EvidenceOutcomes {
		index := outcome.EvidenceIndex
		if err := insertSubmissionOutcome(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, &index, "", outcome.Status, outcome.ReasonCode, map[string]any{"search_state": outcome.SearchState}); err != nil {
			return err
		}
	}
	for _, outcome := range input.RelationshipOutcomes {
		if err := insertSubmissionOutcome(ctx, tx, input.TeamID, input.OwnerProfileID, input.SubmissionID, nil, outcome.ProposalID, outcome.Status, outcome.ReasonCode, map[string]any{"relationship_id": outcome.RelationshipID}); err != nil {
			return err
		}
	}
	return nil
}

func deleteSubmissionStage(ctx context.Context, tx *gorm.DB, teamID, submissionID string) error {
	if err := tx.WithContext(ctx).Exec(`DELETE FROM submission_staged_evidence WHERE team_id = ?::uuid AND submission_id = ?::uuid`, teamID, submissionID).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`DELETE FROM submission_staged_proposals WHERE team_id = ?::uuid AND submission_id = ?::uuid`, teamID, submissionID).Error
}

func normalizePersistSubmissionAssessmentInput(input PersistSubmissionAssessmentInput) PersistSubmissionAssessmentInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.Model = strings.TrimSpace(input.Model)
	input.Tokenizer = strings.TrimSpace(input.Tokenizer)
	input.ResponseHash = strings.TrimSpace(input.ResponseHash)
	if input.ValidatedAt.IsZero() {
		input.ValidatedAt = time.Now().UTC()
	}
	return input
}

func validatePersistSubmissionAssessmentInput(input PersistSubmissionAssessmentInput) error {
	if err := validateSubmissionIdentity(input.TeamID, input.OwnerProfileID, input.SubmissionID); err != nil {
		return err
	}
	if input.WorkerID == "" || input.ExpectedAttempts < 1 || input.RequestID == "" || input.Model == "" || input.Tokenizer == "" || input.ResponseHash == "" {
		return errors.New("complete submission assessment identity is required")
	}
	if !json.Valid(input.NormalizedResponse) || len(input.NormalizedResponse) == 0 {
		return errors.New("normalized submission assessment response is invalid")
	}
	if input.InputTokens < 0 || input.OutputTokens < 0 || input.CandidateContextTokens < 0 {
		return errors.New("submission assessment token counts are invalid")
	}
	return nil
}

func normalizeCompleteSubmissionInput(input CompleteSubmissionInput) CompleteSubmissionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.Status = strings.TrimSpace(input.Status)
	input.ReasonCode = strings.TrimSpace(input.ReasonCode)
	input.CanonicalIngestID = strings.TrimSpace(input.CanonicalIngestID)
	return input
}

func validateCompleteSubmissionInput(input CompleteSubmissionInput) error {
	if err := validateSubmissionIdentity(input.TeamID, input.OwnerProfileID, input.SubmissionID); err != nil {
		return err
	}
	if input.WorkerID == "" || input.ExpectedAttempts < 1 {
		return errors.New("submission lease is required")
	}
	if !contains([]string{string(domain.SubmissionCompleted), string(domain.SubmissionRejected), string(domain.SubmissionFailed)}, input.Status) {
		return fmt.Errorf("unsupported submission completion status %q", input.Status)
	}
	if input.CanonicalIngestID != "" {
		if _, err := uuid.Parse(input.CanonicalIngestID); err != nil {
			return fmt.Errorf("canonical_ingest_id is invalid: %w", err)
		}
	}
	return nil
}

func normalizeQuarantineSubmissionInput(input QuarantineSubmissionInput) QuarantineSubmissionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.SubmissionID = strings.TrimSpace(input.SubmissionID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.ReasonCode = strings.TrimSpace(input.ReasonCode)
	return input
}

func validateQuarantineSubmissionInput(input QuarantineSubmissionInput) error {
	if err := validateSubmissionIdentity(input.TeamID, input.OwnerProfileID, input.SubmissionID); err != nil {
		return err
	}
	if input.WorkerID == "" || input.ExpectedAttempts < 1 || input.ReasonCode == "" {
		return errors.New("submission quarantine lease and reason are required")
	}
	return nil
}
