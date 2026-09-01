package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// CommitRememberTerminal records a deterministic rejected or quarantined
// Remember after assessment. It uses the same no-prewrite boundary as the
// accepted path and keeps the terminal attempt/event in the transaction.
func (r *LedgerRepositoryImpl) CommitRememberTerminal(
	ctx context.Context,
	input SynchronousRememberCommitInput,
	outcome string,
	terminalError RememberTerminalErrorInput,
	quarantines []SubmissionAssessmentSecurityQuarantineInput,
) (*SynchronousRememberCommitResult, error) {
	input = normalizeSynchronousRememberCommitInput(input)
	if outcome != "rejected" && outcome != "quarantined" {
		return nil, fmt.Errorf("unsupported Remember terminal outcome %q", outcome)
	}
	createInput := rememberCreateIngestInput(input)
	createInput.Status = rememberIngestStatus(outcome)
	if err := validateSynchronousRememberTerminalInput(input); err != nil {
		return nil, err
	}
	if err := validateCreateIngestInput(createInput); err != nil {
		return nil, err
	}
	result := &SynchronousRememberCommitResult{IngestID: input.IngestID, AssessmentID: input.AssessmentID, Outcome: outcome}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockRememberIdempotencyKeyInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey); err != nil {
			return err
		}
		if err := validateRememberFailureRetryInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey, input.RequestHash); err != nil && !errors.Is(err, ErrRememberReplay) {
			return err
		}
		if replay, err := loadRememberAttemptInTx(ctx, tx, input); err != nil {
			return err
		} else if replay != nil {
			result.PublicResult, result.Outcome = replay.PublicResult, replay.Outcome
			return ErrRememberReplay
		}
		created, err := insertRememberKnowledgeIngest(ctx, tx, createInput)
		if err != nil {
			return err
		}
		if !created {
			return ErrRememberReplay
		}
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if err := validateRememberTerminalSourceRevisions(ctx, tx, input, createInput); err != nil {
			return err
		}
		evidence, err := insertRememberTerminalEvidence(ctx, tx, input, createInput)
		if err != nil {
			return err
		}
		if input.AssessmentID != "" && len(input.AssessmentJSON) > 0 {
			if err := insertRememberSemanticAssessment(ctx, tx, input); err != nil {
				return err
			}
		}
		for _, quarantine := range quarantines {
			if !rememberEvidenceExists(evidence, quarantine.FragmentID) {
				return errors.New("remember security quarantine fragment is outside the Remember request")
			}
			securityInput := SecurityEventInput{
				TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID,
				FragmentID: quarantine.FragmentID, SecurityEventDraft: quarantine.SecurityEventDraft,
			}
			if err := ensureEvidenceEventOwnership(ctx, tx, securityInput); err != nil {
				return err
			}
			if _, err := insertSecurityEvent(ctx, tx, securityInput); err != nil {
				return err
			}
			if err := insertEvidenceQuarantine(ctx, tx, createInput, input.IngestID, quarantine.FragmentID, quarantine.Reason); err != nil {
				return err
			}
		}
		publicResult := rememberTerminalPublicResult(input, evidence, outcome, terminalError)
		if err := insertRememberAttemptInTx(ctx, tx, RememberAttemptRecordInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, AttemptID: input.IngestID,
			SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
			RequestHash: input.RequestHash, ContractVersion: domain.ContractVersion, SubmissionKind: "remember",
			Outcome: outcome, ErrorCode: terminalError.Code, CorrelationID: rememberCorrelationID(input.Metadata), PublicResult: publicResult,
			RetryabilitySet: true,
			EvidenceCount:   len(evidence), RelationshipCount: len(input.Commit.RelationshipResults), AssessorTurns: input.AssessorTurns, Duration: rememberAttemptDuration(input),
		}); err != nil {
			return err
		}
		result.PublicResult = publicResult
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("repository: commit Remember terminal: %w", err)
	}
	if strings.TrimSpace(input.SpaceID) != "" {
		if err := r.synchronizeRememberFailureArtifactHold(ctx, input.SpaceID); err != nil {
			return result, fmt.Errorf("repository: commit Remember terminal retention: %w", err)
		}
	}
	return result, nil
}

// CommitRememberPreflightQuarantine records a deterministic admission
// rejection without creating canonical ingest, evidence, source, semantic, or
// search rows. The admission audit is persisted separately from this terminal
// attempt, so the submitted content never becomes durable memory.
func (r *LedgerRepositoryImpl) CommitRememberPreflightQuarantine(
	ctx context.Context,
	input SynchronousRememberCommitInput,
	terminalError RememberTerminalErrorInput,
) (*SynchronousRememberCommitResult, error) {
	input = normalizeSynchronousRememberCommitInput(input)
	relationshipResults, err := rememberTerminalRelationshipResultsFromProposal(input.Proposal, "security_quarantine")
	if err != nil {
		return nil, err
	}
	input.Commit.RelationshipResults = relationshipResults
	input.EvidenceSecurityResults = make([]EvidenceSecurityResult, 0, len(input.Evidence))
	for index, evidence := range input.Evidence {
		input.EvidenceSecurityResults = append(input.EvidenceSecurityResults, EvidenceSecurityResult{
			FragmentID: evidence.FragmentID, EvidenceIndex: index, Decision: "quarantine", Safe: false,
		})
	}
	if err := validateSynchronousRememberTerminalInput(input); err != nil {
		return nil, err
	}
	result := &SynchronousRememberCommitResult{IngestID: input.IngestID, Outcome: "quarantined"}
	syntheticEvidence := make([]EvidenceFragment, 0, len(input.Evidence))
	for index, item := range input.Evidence {
		syntheticEvidence = append(syntheticEvidence, EvidenceFragment{
			FragmentID: item.FragmentID, EvidenceIndex: index,
			SupersededEvidenceIDs: append([]string(nil), item.SupersedesEvidenceIDs...),
		})
	}
	err = r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := lockRememberIdempotencyKeyInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey); err != nil {
			return err
		}
		if err := validateRememberFailureRetryInTx(ctx, tx, input.TeamID, input.OwnerProfileID, input.IdempotencyKey, input.RequestHash); err != nil && !errors.Is(err, ErrRememberReplay) {
			return err
		}
		if replay, err := loadRememberAttemptInTx(ctx, tx, input); err != nil {
			return err
		} else if replay != nil {
			result.PublicResult, result.Outcome = replay.PublicResult, replay.Outcome
			return ErrRememberReplay
		}
		publicResult := rememberTerminalPublicResult(input, syntheticEvidence, "quarantined", terminalError)
		if err := insertRememberAttemptInTx(ctx, tx, RememberAttemptRecordInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, AttemptID: input.IngestID,
			SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
			RequestHash: input.RequestHash, ContractVersion: domain.ContractVersion, SubmissionKind: "remember",
			Outcome: "quarantined", ErrorCode: terminalError.Code, CorrelationID: rememberCorrelationID(input.Metadata), PublicResult: publicResult,
			RetryabilitySet: true,
			EvidenceCount:   len(input.Evidence), RelationshipCount: len(input.Commit.RelationshipResults), AssessorTurns: input.AssessorTurns, Duration: rememberAttemptDuration(input),
		}); err != nil {
			return err
		}
		if err := insertPolicyRejectedRequestArtifact(ctx, tx, input); err != nil {
			return err
		}
		result.PublicResult = publicResult
		return nil
	})
	if err != nil {
		return result, fmt.Errorf("repository: commit Remember preflight quarantine: %w", err)
	}
	if strings.TrimSpace(input.SpaceID) != "" {
		if err := r.synchronizeRememberFailureArtifactHold(ctx, input.SpaceID); err != nil {
			return result, fmt.Errorf("repository: commit Remember preflight quarantine retention: %w", err)
		}
	}
	return result, nil
}

func insertPolicyRejectedRequestArtifact(ctx context.Context, tx *gorm.DB, input SynchronousRememberCommitInput) error {
	if len(input.Evidence) == 0 {
		return nil
	}
	evidence := make([]map[string]any, 0, len(input.Evidence))
	for index, item := range input.Evidence {
		contentHash := strings.TrimSpace(item.ContentHash)
		if contentHash == "" {
			contentHash = sha256Hex(item.Content)
		}
		evidence = append(evidence, map[string]any{
			"evidence_index": index,
			"content_hash":   contentHash,
		})
	}
	content, err := json.Marshal(map[string]any{
		"submission_id": input.IngestID,
		"evidence":      evidence,
	})
	if err != nil {
		return fmt.Errorf("policy rejection artifact: encode: %w", err)
	}
	if len(content) > maxRememberFailureArtifactBytes {
		return errors.New("policy rejection artifact exceeds retention size")
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO remember_failure_artifacts (
		    team_id, artifact_id, attempt_id, owner_profile_id, artifact_kind,
		    content_type, content_bytes, byte_count, content_sha256, captured_at, expires_at
		) VALUES (?::uuid, gen_random_uuid(), ?::uuid, ?::uuid, 'policy_rejected_request',
		          'application/json', ?, ?, ?, statement_timestamp(), statement_timestamp() + interval '7 days')
	`, input.TeamID, input.IngestID, input.OwnerProfileID, content, len(content), sha256Hex(string(content))).Error
}

func rememberTerminalRelationshipResultsFromProposal(proposal map[string]any, reason string) ([]SubmissionRelationshipResultInput, error) {
	reason = strings.TrimSpace(reason)
	if !submissionRelationshipNotStoredReasonAllowed(reason) {
		return nil, errors.New("terminal Remember relationship result reason is unsupported")
	}
	var raw any
	if proposal != nil {
		raw = proposal["relationship_hints"]
		if raw == nil {
			raw = proposal["relationships"]
		}
	}
	if raw == nil {
		return []SubmissionRelationshipResultInput{}, nil
	}
	var relationships []any
	switch typed := raw.(type) {
	case []any:
		relationships = typed
	case []map[string]any:
		relationships = make([]any, len(typed))
		for index := range typed {
			relationships[index] = typed[index]
		}
	default:
		return nil, errors.New("terminal Remember relationship hints must be an array")
	}
	results := make([]SubmissionRelationshipResultInput, 0, len(relationships))
	seen := make(map[string]struct{}, len(relationships))
	for index, rawRelationship := range relationships {
		fields, ok := rawRelationship.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("terminal Remember relationship hint %d must be an object", index)
		}
		ref, _ := fields["ref"].(string)
		ref = strings.TrimSpace(ref)
		if ref == "" || len([]rune(ref)) > 128 {
			return nil, fmt.Errorf("terminal Remember relationship hint %d has an invalid ref", index)
		}
		if _, exists := seen[ref]; exists {
			return nil, fmt.Errorf("terminal Remember relationship ref %q is duplicated", ref)
		}
		seen[ref] = struct{}{}
		results = append(results, SubmissionRelationshipResultInput{
			RelationshipRef: ref,
			Disposition:     "not_stored",
			Reason:          reason,
		})
	}
	return results, nil
}
