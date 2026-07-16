package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func (r *SemanticRepositoryImpl) StoreEvidenceSecurityEvent(ctx context.Context, input SemanticEvidenceSecurityEventInput) error {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.FragmentID = strings.TrimSpace(input.FragmentID)
	input.Content = strings.TrimSpace(input.Content)
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.FragmentID); err != nil {
		return fmt.Errorf("fragment_id is required: %w", err)
	}
	if input.Content == "" {
		return fmt.Errorf("content is required")
	}
	if err := validateEvidenceSecurityAssessment(0, input.Assessment, input.Content); err != nil {
		return err
	}
	return r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		rememberInput := SemanticRememberInput{
			TeamID:         input.TeamID,
			OwnerProfileID: input.OwnerProfileID,
		}
		if err := upsertSemanticRefs(ctx, tx, rememberInput); err != nil {
			return err
		}
		if err := insertSemanticEvidenceSecurityEvent(ctx, tx, rememberInput, input.FragmentID, input.Assessment); err != nil {
			return err
		}
		if input.DeactivateSearch {
			return markSemanticEvidenceSearchNotRequired(ctx, tx, input.TeamID, input.FragmentID)
		}
		return nil
	})
}

func validateEvidenceSecurityAssessment(index int, assessment domain.EvidenceSecurityAssessment, content string) error {
	if !assessment.Decision.IsValid() {
		return fmt.Errorf("evidence[%d].security_assessment.decision is invalid", index)
	}
	if !assessment.EventKind.IsValid() {
		return fmt.Errorf("evidence[%d].security_assessment.event_kind is invalid", index)
	}
	if len([]rune(strings.TrimSpace(assessment.Reason))) > 1000 {
		return fmt.Errorf("evidence[%d].security_assessment.reason is too long", index)
	}
	contentRunes := []rune(content)
	for signalIndex, signal := range assessment.Signals {
		if !signal.Kind.IsValid() {
			return fmt.Errorf("evidence[%d].security_assessment.signals[%d].kind is invalid", index, signalIndex)
		}
		if !signal.Severity.IsValid() {
			return fmt.Errorf("evidence[%d].security_assessment.signals[%d].severity is invalid", index, signalIndex)
		}
		if signal.SpanStart < 0 || signal.SpanEnd <= signal.SpanStart || signal.SpanEnd > len(contentRunes) {
			return fmt.Errorf("evidence[%d].security_assessment.signals[%d].span is invalid", index, signalIndex)
		}
	}
	return nil
}

func insertSemanticEvidenceSecurityEvent(ctx context.Context, tx *gorm.DB, input SemanticRememberInput, fragmentID string, assessment domain.EvidenceSecurityAssessment) error {
	metadata, err := marshalMap(nil)
	if err != nil {
		return err
	}
	reason := strings.TrimSpace(assessment.Reason)
	rows, err := tx.WithContext(ctx).Raw(`
			INSERT INTO semantic_evidence_security_events (
			    team_id, fragment_id, owner_profile_id, event_kind, decision,
			    scan_policy_hash, reason, metadata, created_at
			) VALUES (
			    ?, ?::uuid, ?, ?, ?, ?, ?, ?::jsonb, now()
			)
			RETURNING security_event_id::text
		`, input.TeamID, fragmentID, input.OwnerProfileID, string(assessment.EventKind),
		string(assessment.Decision), assessment.ScanPolicyHash, reason, string(metadata)).Rows()
	if err != nil {
		return err
	}
	if !rows.Next() {
		_ = rows.Close()
		return sql.ErrNoRows
	}
	var eventID string
	if err := rows.Scan(&eventID); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for i, signal := range assessment.Signals {
		if err := insertSemanticEvidenceSecuritySignal(ctx, tx, input.TeamID, eventID, i, signal); err != nil {
			return err
		}
	}
	return nil
}

func insertSemanticEvidenceSecuritySignal(ctx context.Context, tx *gorm.DB, teamID, eventID string, index int, signal domain.EvidenceSecuritySignal) error {
	metadata, err := marshalMap(nil)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO semantic_evidence_security_signals (
		    team_id, security_event_id, signal_index, kind, severity,
		    span_start, span_end, quote, metadata, created_at
		) VALUES (
		    ?, ?::uuid, ?, ?, ?, ?, ?, ?, ?::jsonb, now()
		)
	`, teamID, eventID, index, string(signal.Kind), string(signal.Severity),
		signal.SpanStart, signal.SpanEnd, signal.Quote, string(metadata)).Error
}

func markSemanticEvidenceSearchNotRequired(ctx context.Context, tx *gorm.DB, teamID, fragmentID string) error {
	if err := tx.WithContext(ctx).Exec(`
		UPDATE semantic_evidence_fragments
		SET search_state = 'not_required',
		    updated_at = now()
		WHERE team_id = ?
		  AND fragment_id = ?::uuid
	`, teamID, fragmentID).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec(`
		UPDATE semantic_search_documents
		SET search_state = 'not_required',
		    embedding = NULL,
		    embedding_model = '',
		    embedding_contract_id = '',
		    last_error = '',
		    updated_at = now()
		WHERE team_id = ?
		  AND source_type = 'evidence'
		  AND source_id = ?::uuid
	`, teamID, fragmentID).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		UPDATE semantic_embedding_jobs
		SET status = 'failed',
		    last_error = 'evidence quarantined',
		    lease_until = NULL,
		    completed_at = now(),
		    updated_at = now()
		WHERE team_id = ?
		  AND source_type = 'evidence'
		  AND source_id = ?::uuid
		  AND status IN ('queued', 'processing')
	`, teamID, fragmentID).Error
}
