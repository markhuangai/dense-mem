package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func normalizeSynchronousRememberCommitInput(input SynchronousRememberCommitInput) SynchronousRememberCommitInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.RequestHash = strings.TrimSpace(input.RequestHash)
	input.SourceSummary = strings.TrimSpace(input.SourceSummary)
	input.AssessmentID = strings.TrimSpace(input.AssessmentID)
	input.Commit.RememberCommitScope = normalizeRememberCommitScope(input.Commit.RememberCommitScope)
	input.Commit.RememberCommitScope.TeamID = input.TeamID
	input.Commit.RememberCommitScope.OwnerProfileID = input.OwnerProfileID
	input.Commit.RememberCommitScope.IngestID = input.IngestID
	for index := range input.Evidence {
		input.Evidence[index].FragmentID = strings.TrimSpace(input.Evidence[index].FragmentID)
	}
	return input
}

func rememberAttemptDuration(input SynchronousRememberCommitInput) time.Duration {
	if !input.StartedAt.IsZero() {
		if elapsed := time.Since(input.StartedAt); elapsed >= 0 {
			return elapsed
		}
	}
	return input.Duration
}

func validateSynchronousRememberCommitInput(input SynchronousRememberCommitInput) error {
	for label, value := range map[string]string{"team_id": input.TeamID, "owner_profile_id": input.OwnerProfileID, "ingest_id": input.IngestID} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if input.AssessmentID != "" {
		if _, err := uuid.Parse(input.AssessmentID); err != nil {
			return fmt.Errorf("assessment_id is invalid: %w", err)
		}
	}
	if input.IdempotencyKey == "" || input.RequestHash == "" {
		return errors.New("remember idempotency fields are required")
	}
	if len(input.Evidence) == 0 {
		return errors.New("remember evidence is required")
	}
	seen := make(map[string]struct{}, len(input.Evidence))
	for index, evidence := range input.Evidence {
		if _, err := uuid.Parse(evidence.FragmentID); err != nil {
			return fmt.Errorf("evidence[%d].fragment_id is required: %w", index, err)
		}
		if _, exists := seen[evidence.FragmentID]; exists {
			return fmt.Errorf("evidence[%d].fragment_id is duplicated", index)
		}
		seen[evidence.FragmentID] = struct{}{}
	}
	commit := normalizeCommitSubmissionAssessmentInput(input.Commit)
	return validateCommitSubmissionAssessmentInput(commit)
}

func rememberCreateIngestInput(input SynchronousRememberCommitInput) CreateIngestInput {
	return normalizeCreateIngestInput(CreateIngestInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
		RequestHash: input.RequestHash, SourceSummary: input.SourceSummary, Status: "completed",
		Proposal: input.Proposal, Metadata: input.Metadata, Evidence: append([]EvidenceInput(nil), input.Evidence...),
	})
}

func rememberIngestStatus(outcome string) string {
	switch outcome {
	case "rejected", "quarantined":
		return outcome
	default:
		return "completed"
	}
}

func insertRememberKnowledgeIngest(ctx context.Context, tx *gorm.DB, input CreateIngestInput) (bool, error) {
	proposal, err := marshalJSON(input.Proposal)
	if err != nil {
		return false, err
	}
	metadataValues := make(map[string]any, len(input.Metadata)+1)
	for key, value := range input.Metadata {
		if key == ingestMetadataTelemetryOriginKey {
			continue
		}
		metadataValues[key] = value
	}
	metadataValues[ingestMetadataTelemetryOriginKey] = ingestMetadataTelemetryOriginRemember
	metadata, err := marshalJSON(metadataValues)
	if err != nil {
		return false, err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO knowledge_ingests (
		    ingest_id, team_id, owner_profile_id, space_id, space_generation,
		    idempotency_key, request_hash, source_summary, status, proposal, metadata, completed_at
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid, NULLIF(?::bigint, 0),
		    ?, ?, ?, ?, ?::jsonb, ?::jsonb, now()
		)
		ON CONFLICT (team_id, owner_profile_id, idempotency_key)
		WHERE idempotency_key <> '' AND status <> 'failed'
		DO NOTHING
		RETURNING ingest_id::text
	`, input.IngestID, input.TeamID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration,
		input.IdempotencyKey, input.RequestHash, input.SourceSummary, input.Status, string(proposal), string(metadata)).Rows()
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if rows.Next() {
		var ingestID string
		if err := rows.Scan(&ingestID); err != nil {
			return false, err
		}
		return true, rows.Err()
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	var existingHash string
	err = tx.WithContext(ctx).Raw(`
		SELECT request_hash FROM knowledge_ingests
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?
		  AND status <> 'failed'
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Row().Scan(&existingHash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrRememberReplay
	}
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(existingHash) != strings.TrimSpace(input.RequestHash) {
		return false, fmt.Errorf("%w: idempotency key reused with a different request hash", ErrIdempotencyConflict)
	}
	return false, nil
}

func loadRememberAttemptInTx(ctx context.Context, tx *gorm.DB, input SynchronousRememberCommitInput) (*RememberAttempt, error) {
	var attempt RememberAttempt
	var publicJSON []byte
	err := tx.WithContext(ctx).Raw(`
		SELECT attempt_id::text, request_hash, outcome, public_result
		FROM remember_attempts
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?
		  AND outcome IN ('completed', 'rejected', 'quarantined', 'replayed')
		ORDER BY created_at DESC, attempt_id DESC
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Row().Scan(
		&attempt.AttemptID, &attempt.RequestHash, &attempt.Outcome, &publicJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(attempt.RequestHash) != strings.TrimSpace(input.RequestHash) {
		return nil, fmt.Errorf("%w: idempotency key reused with a different request hash", ErrIdempotencyConflict)
	}
	if len(publicJSON) == 0 {
		attempt.PublicResult = map[string]any{}
	} else if err := json.Unmarshal(publicJSON, &attempt.PublicResult); err != nil {
		return nil, err
	}
	return &attempt, nil
}

func insertRememberSemanticAssessment(ctx context.Context, tx *gorm.DB, input SynchronousRememberCommitInput) error {
	if len(input.AssessmentJSON) == 0 {
		return errors.New("remember semantic assessment response is required")
	}
	history, err := json.Marshal([]map[string]any{{
		"assessment_id": input.AssessmentID, "normalized_response": json.RawMessage(input.AssessmentJSON),
		"response_hash": input.Commit.Payload["response_hash"], "provider_turns": input.ProviderTurns,
		"validated_at": time.Now().UTC(),
	}})
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO semantic_assessments (
		    team_id, semantic_assessment_id, attempt_id, owner_profile_id,
		    response_history, accepted_revision, provider_turns, model, tokenizer,
		    input_tokens, output_tokens, candidate_context_tokens, candidate_context_truncated,
		    response_hash, validated_at
		) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::jsonb, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, input.TeamID, input.AssessmentID, input.IngestID, input.OwnerProfileID,
		string(history), input.ProviderTurns, strings.TrimSpace(fmt.Sprint(input.Commit.Payload["model"])),
		strings.TrimSpace(fmt.Sprint(input.Commit.Payload["tokenizer"])), input.InputTokens, input.OutputTokens,
		input.Commit.Payload["candidate_context_tokens"], input.Commit.Payload["candidate_context_truncated"],
		strings.TrimSpace(fmt.Sprint(input.Commit.Payload["response_hash"])), time.Now().UTC()).Error
}

func validateRememberTerminalSourceRevisions(ctx context.Context, tx *gorm.DB, input SynchronousRememberCommitInput, createInput CreateIngestInput) error {
	for _, item := range createInput.Evidence {
		if item.SourceKey == "" {
			continue
		}
		if err := validateTerminalSourceRevisionInTx(ctx, tx, AdvanceSourceRevisionInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID,
			SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, SourceKey: item.SourceKey,
			SourceKind: sourceKindForEvidence(item.SourceType), Authority: item.Authority,
			RevisionToken: item.SourceRevisionToken, ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken,
			ContentHash: item.SourceRevisionContentHash, Envelope: item.SourceRevisionEnvelope,
		}); err != nil {
			return err
		}
	}
	return nil
}

func insertRememberTerminalEvidence(ctx context.Context, tx *gorm.DB, input SynchronousRememberCommitInput, createInput CreateIngestInput) ([]EvidenceFragment, error) {
	evidence := make([]EvidenceFragment, 0, len(createInput.Evidence))
	for index, item := range createInput.Evidence {
		fragment, err := insertEvidenceFragment(ctx, tx, createInput, input.IngestID, index, item, nil)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, fragment)
		if item.InitialEvent != nil {
			eventInput := SecurityEventInput{
				TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID,
				FragmentID: fragment.FragmentID, SecurityEventDraft: *item.InitialEvent,
			}
			if _, err := insertSecurityEvent(ctx, tx, eventInput); err != nil {
				return nil, err
			}
			if item.InitialEvent.Decision == "quarantine" {
				if err := insertEvidenceQuarantine(ctx, tx, createInput, input.IngestID, fragment.FragmentID, item.InitialEvent.Reason); err != nil {
					return nil, err
				}
			}
		}
	}
	return evidence, nil
}

func applyRememberCommitSourceReferences(observations []SubmissionAssessmentRelationshipObservationInput, evidence []EvidenceFragment) {
	type sourceRef struct{ sourceID, revisionID string }
	byFragment := make(map[string]sourceRef, len(evidence))
	for _, item := range evidence {
		byFragment[item.FragmentID] = sourceRef{sourceID: item.SourceID, revisionID: item.SourceRevisionID}
	}
	apply := func(support *EvidenceSupportInput) {
		if support == nil {
			return
		}
		if ref, ok := byFragment[support.FragmentID]; ok {
			support.SourceID, support.SourceRevisionID = ref.sourceID, ref.revisionID
		}
	}
	for index := range observations {
		apply(observations[index].Observation.Support)
		for supportIndex := range observations[index].Observation.Supports {
			apply(&observations[index].Observation.Supports[supportIndex])
		}
	}
}

func rememberEvidenceExists(evidence []EvidenceFragment, fragmentID string) bool {
	for _, item := range evidence {
		if item.FragmentID == strings.TrimSpace(fragmentID) {
			return true
		}
	}
	return false
}

func rememberRelationshipFragmentID(entry SubmissionAssessmentRelationshipObservationInput) (string, error) {
	if entry.Observation.Support != nil && strings.TrimSpace(entry.Observation.Support.FragmentID) != "" {
		return strings.TrimSpace(entry.Observation.Support.FragmentID), nil
	}
	if len(entry.Observation.Supports) > 0 && strings.TrimSpace(entry.Observation.Supports[0].FragmentID) != "" {
		return strings.TrimSpace(entry.Observation.Supports[0].FragmentID), nil
	}
	return "", errors.New("remember relationship observation support is required")
}

func rememberCorrelationID(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	if value, ok := metadata["correlation_id"].(string); ok {
		return strings.TrimSpace(value)
	}
	if actor, ok := metadata["actor"].(map[string]any); ok {
		if value, ok := actor["correlation_id"].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
