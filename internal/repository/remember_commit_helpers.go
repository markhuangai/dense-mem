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

	"github.com/markhuangai/dense-mem/internal/domain"
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
	input.Commit = normalizeCommitSubmissionAssessmentInput(input.Commit)
	for index := range input.Evidence {
		input.Evidence[index].FragmentID = strings.TrimSpace(input.Evidence[index].FragmentID)
	}
	for index := range input.DuplicateResolutions {
		resolution := &input.DuplicateResolutions[index]
		resolution.EvidenceID = strings.TrimSpace(resolution.EvidenceID)
		resolution.InputFragmentID = strings.TrimSpace(resolution.InputFragmentID)
		resolution.CandidateFragmentID = strings.TrimSpace(resolution.CandidateFragmentID)
		resolution.CandidateOwnerID = strings.TrimSpace(resolution.CandidateOwnerID)
		resolution.Disposition = strings.TrimSpace(resolution.Disposition)
	}
	for index := range input.EvidenceSecurityResults {
		result := &input.EvidenceSecurityResults[index]
		result.FragmentID = strings.TrimSpace(result.FragmentID)
		result.EvidenceID = strings.TrimSpace(result.EvidenceID)
		result.Decision = strings.ToLower(strings.TrimSpace(result.Decision))
		if result.Decision == "safe" || result.Decision == "allow" {
			result.Decision = "pass"
		}
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
	return validateSynchronousRememberCommitInputWithSecurity(input, false)
}

func validateSynchronousRememberCommitInputWithSecurity(input SynchronousRememberCommitInput, allowUnsafe bool) error {
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
	if len(input.DuplicateResolutions) > 0 {
		if len(input.DuplicateResolutions) != len(input.Evidence) {
			return errors.New("remember duplicate resolutions must contain exactly one result per evidence item")
		}
		seenResolutions := make(map[int]struct{}, len(input.DuplicateResolutions))
		for index, resolution := range input.DuplicateResolutions {
			if resolution.EvidenceIndex < 0 || resolution.EvidenceIndex >= len(input.Evidence) {
				return fmt.Errorf("duplicate resolution[%d] has an invalid evidence index", index)
			}
			if _, exists := seenResolutions[resolution.EvidenceIndex]; exists {
				return fmt.Errorf("duplicate resolution[%d] repeats an evidence index", index)
			}
			seenResolutions[resolution.EvidenceIndex] = struct{}{}
			if resolution.InputFragmentID != input.Evidence[resolution.EvidenceIndex].FragmentID {
				return fmt.Errorf("duplicate resolution[%d] does not identify its submitted evidence", index)
			}
			switch resolution.Disposition {
			case "new":
				if resolution.CandidateFragmentID != "" || resolution.CandidateOwnerID != "" {
					return fmt.Errorf("duplicate resolution[%d] new result cannot carry a candidate", index)
				}
			case "reuse":
				if _, err := uuid.Parse(resolution.CandidateFragmentID); err != nil {
					return fmt.Errorf("duplicate resolution[%d] reuse candidate is invalid: %w", index, err)
				}
				if _, err := uuid.Parse(resolution.CandidateOwnerID); err != nil {
					return fmt.Errorf("duplicate resolution[%d] reuse candidate owner is invalid: %w", index, err)
				}
			default:
				return fmt.Errorf("duplicate resolution[%d] has unsupported disposition", index)
			}
		}
	}
	if err := validateEvidenceSecurityResults(input.Evidence, input.EvidenceSecurityResults, allowUnsafe); err != nil {
		return err
	}
	commit := normalizeCommitSubmissionAssessmentInput(input.Commit)
	return validateCommitSubmissionAssessmentInput(commit)
}

func validateEvidenceSecurityResults(evidence []EvidenceInput, results []EvidenceSecurityResult, allowUnsafe bool) error {
	if len(results) != len(evidence) {
		return fmt.Errorf("remember evidence security results must contain exactly one result per evidence item")
	}
	byIndex := make(map[int]EvidenceInput, len(evidence))
	byFragment := make(map[string]struct{}, len(evidence))
	for index, item := range evidence {
		byIndex[index] = item
		byFragment[item.FragmentID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(results))
	for index, result := range results {
		fragmentID := strings.TrimSpace(result.FragmentID)
		if fragmentID == "" && result.EvidenceIndex >= 0 {
			fragmentID = byIndex[result.EvidenceIndex].FragmentID
		}
		if fragmentID == "" {
			return fmt.Errorf("remember evidence security result[%d] must identify evidence", index)
		}
		if _, ok := byFragment[fragmentID]; !ok {
			return fmt.Errorf("remember evidence security result[%d] is outside the Remember request", index)
		}
		if _, ok := seen[fragmentID]; ok {
			return fmt.Errorf("remember evidence security result for %s is duplicated", fragmentID)
		}
		seen[fragmentID] = struct{}{}
		decision := strings.ToLower(strings.TrimSpace(result.Decision))
		if decision == "safe" || decision == "allow" {
			decision = "pass"
		}
		switch decision {
		case "pass":
			if !result.Safe {
				return fmt.Errorf("remember evidence security result[%d] pass must be marked safe", index)
			}
			if len(result.Signals) != 0 {
				return fmt.Errorf("remember evidence security result[%d] contains unsafe signals", index)
			}
		case "reject":
			if !allowUnsafe || result.Safe {
				return fmt.Errorf("remember evidence security result[%d] is not safe", index)
			}
		default:
			return fmt.Errorf("remember evidence security result[%d] has unsupported decision", index)
		}
	}
	if len(seen) != len(evidence) {
		return errors.New("remember evidence security results omit an evidence item")
	}
	return nil
}

func rememberCreateIngestInput(input SynchronousRememberCommitInput) CreateIngestInput {
	return normalizeCreateIngestInput(CreateIngestInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
		RequestHash: input.RequestHash, SourceSummary: input.SourceSummary, Status: "completed",
		Proposal: input.Proposal, Metadata: input.Metadata, Evidence: append([]EvidenceInput(nil), input.Evidence...),
	})
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
		SELECT attempt_id::text, request_hash, contract_version, outcome,
		       COALESCE(retryable, outcome = 'failed'), public_result
		FROM remember_attempts
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?
		  AND outcome IN ('completed', 'rejected', 'quarantined', 'replayed')
		ORDER BY created_at DESC, attempt_id DESC
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Row().Scan(
		&attempt.AttemptID, &attempt.RequestHash, &attempt.ContractVersion, &attempt.Outcome, &attempt.Retryable, &publicJSON,
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
	if strings.TrimSpace(attempt.ContractVersion) != "" && strings.TrimSpace(attempt.ContractVersion) != domain.ContractVersion {
		return nil, fmt.Errorf("%w: historical Remember contract is not replayable", ErrIdempotencyConflict)
	}
	if attempt.Outcome == "rejected" || attempt.Outcome == "quarantined" {
		return nil, fmt.Errorf("%w: historical Remember outcome is not replayable", ErrIdempotencyConflict)
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
	type sourceRef struct{ sourceID, revisionID, occurrenceID, evidenceOwnerID, occurrenceOwnerID, canonicalID string }
	byFragment := make(map[string]sourceRef, len(evidence))
	byOccurrence := make(map[string]sourceRef, len(evidence))
	for _, item := range evidence {
		ref := sourceRef{sourceID: item.SourceID, revisionID: item.SourceRevisionID, occurrenceID: item.OccurrenceID, evidenceOwnerID: item.CanonicalOwnerID, occurrenceOwnerID: item.OccurrenceOwnerID, canonicalID: item.FragmentID}
		byFragment[item.FragmentID] = ref
		if item.OccurrenceID != "" {
			byOccurrence[item.OccurrenceID] = ref
		}
		if item.SubmittedFragmentID != "" {
			byFragment[item.SubmittedFragmentID] = ref
		}
	}
	apply := func(support *EvidenceSupportInput) {
		if support == nil {
			return
		}
		if ref, ok := byOccurrence[strings.TrimSpace(support.OccurrenceID)]; ok {
			support.FragmentID = ref.canonicalID
			support.EvidenceOwnerProfileID = ref.evidenceOwnerID
			support.OccurrenceOwnerProfileID = ref.occurrenceOwnerID
			support.SourceID, support.SourceRevisionID = ref.sourceID, ref.revisionID
			return
		}
		if ref, ok := byFragment[support.FragmentID]; ok {
			support.FragmentID = ref.canonicalID
			support.OccurrenceID = ref.occurrenceID
			support.EvidenceOwnerProfileID = ref.evidenceOwnerID
			support.OccurrenceOwnerProfileID = ref.occurrenceOwnerID
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

func remapRememberCommitEvidenceReferences(commit *CommitSubmissionAssessmentInput, evidence []EvidenceFragment) error {
	if commit == nil {
		return errors.New("remember semantic commit input is required")
	}
	byID := make(map[string]EvidenceFragment, len(evidence)*2)
	for _, item := range evidence {
		byID[item.FragmentID] = item
		if item.SubmittedFragmentID != "" {
			byID[item.SubmittedFragmentID] = item
		}
	}
	resolve := func(fragmentID string, support *EvidenceSupportInput) error {
		if support == nil {
			return nil
		}
		item, ok := byID[strings.TrimSpace(fragmentID)]
		if !ok {
			return nil
		}
		support.FragmentID = item.FragmentID
		support.OccurrenceID = item.OccurrenceID
		support.OccurrenceOwnerProfileID = item.OccurrenceOwnerID
		support.EvidenceOwnerProfileID = item.CanonicalOwnerID
		return nil
	}
	for index := range commit.EntityResolutions {
		resolution := &commit.EntityResolutions[index].Resolution
		item, ok := byID[strings.TrimSpace(resolution.FragmentID)]
		if !ok {
			return fmt.Errorf("remember entity resolution evidence %q is outside the Remember request", resolution.FragmentID)
		}
		resolution.FragmentID = item.FragmentID
		resolution.OccurrenceID = item.OccurrenceID
		resolution.EvidenceOwnerProfileID = item.CanonicalOwnerID
	}
	for index := range commit.RelationshipObservations {
		observation := &commit.RelationshipObservations[index].Observation
		if observation.Support != nil {
			if err := resolve(observation.Support.FragmentID, observation.Support); err != nil {
				return err
			}
		}
		for supportIndex := range observation.Supports {
			if err := resolve(observation.Supports[supportIndex].FragmentID, &observation.Supports[supportIndex]); err != nil {
				return err
			}
		}
	}
	return nil
}

func rememberEvidenceExists(evidence []EvidenceFragment, fragmentID string) bool {
	for _, item := range evidence {
		if item.FragmentID == strings.TrimSpace(fragmentID) {
			return true
		}
	}
	return false
}

func rememberSubmittedOccurrenceBelongsToOwner(evidence []EvidenceFragment, support EvidenceSupportInput, ownerID string) bool {
	occurrenceID := strings.TrimSpace(support.OccurrenceID)
	if occurrenceID == "" {
		return false
	}
	for _, item := range evidence {
		if item.FragmentID == strings.TrimSpace(support.FragmentID) &&
			item.OccurrenceID == occurrenceID &&
			item.OccurrenceOwnerID == strings.TrimSpace(ownerID) {
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
