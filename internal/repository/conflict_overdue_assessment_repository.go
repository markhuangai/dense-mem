package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	conflictAssessmentMaxFailedDays = 5
	conflictResolutionMaxFragments  = 200
)

var (
	ErrConflictAssessmentUnavailable = errors.New("conflict assessment is unavailable")
	ErrConflictAssessmentStale       = errors.New("conflict assessment is stale")
	ErrConflictAssessmentReserved    = errors.New("conflict assessment is not reserved")
)

type ReserveOverdueConflictAssessmentInput struct {
	TeamID              string
	ConflictID          string
	ReviewRunID         string
	WorkerID            string
	LocalAssessmentDate time.Time
	Model               string
	PolicyVersion       string
}

type OverdueConflictAssessmentReservation struct {
	AssessmentAttemptID string
	CaseVersion         int
	Model               string
	PolicyVersion       string
	LastWriteWins       bool
}

type OverdueConflictAssessmentDossier struct {
	TeamID          string
	ConflictID      string
	CaseVersion     int
	Question        string
	Positions       []OverdueConflictAssessmentPosition
	Evidence        []OverdueConflictAssessmentEvidence
	SystemProfileID string
}

type OverdueConflictAssessmentPosition struct {
	PositionID              string
	PositionKey             string
	SupportGroupCount       int
	AuthoritativeGroupCount int
	OwnerProfileCount       int
	Supports                []domain.ConflictResolutionSupport
}

type OverdueConflictAssessmentEvidence struct {
	FragmentID     string
	OwnerProfileID string
	PositionID     string
	SupportID      string
	SourceGroupKey string
	Authority      string
	AcceptedAt     time.Time
	EffectiveAt    *time.Time
	EvidenceIndex  int
	Content        string
}

type CompleteOverdueConflictAssessmentInput struct {
	TeamID              string
	ConflictID          string
	AssessmentAttemptID string
	CaseVersion         int
	ReviewRunID         string
	Decision            string
	SelectedPositionID  string
	Confidence          *float64
	ProviderTurns       int
	ResponseHash        string
	FailureClass        string
}

type CompleteOverdueConflictAssessmentResult struct {
	FailureCount int
}

type ApplyOverdueConflictResolutionInput struct {
	TeamID              string
	ConflictID          string
	ReviewRunID         string
	WorkerID            string
	ExpectedCaseVersion int
	PreferredPositionID string
	AssessmentAttemptID string
	Method              string
	Now                 time.Time
}

type ApplyOverdueConflictResolutionResult struct {
	ConflictID           string
	PreferredPositionID  string
	Method               string
	Resolved             bool
	Pending              bool
	Stale                bool
	UpdatedRelationships []string
	RetractedEvidenceIDs []string
	DerivedEvidence      []ConflictDerivedEvidenceTarget
}

type ResumePendingOverdueConflictResolutionInput struct {
	TeamID      string
	ConflictID  string
	ReviewRunID string
	WorkerID    string
	Now         time.Time
}

type ConflictDerivedEvidenceTarget struct {
	TeamID               string
	ConflictID           string
	SystemProfileID      string
	TargetFragmentID     string
	TargetOwnerProfileID string
	SelectedPositionID   string
	SourceGroupKey       string
	EvidenceIndex        int
}

type StageConflictDerivedEvidenceResult struct {
	IngestID            string
	ReplacementFragment string
	Existing            bool
}

func (r *LedgerRepositoryImpl) ReserveOverdueConflictAssessment(
	ctx context.Context,
	input ReserveOverdueConflictAssessmentInput,
) (*OverdueConflictAssessmentReservation, *OverdueConflictAssessmentDossier, bool, error) {
	input = normalizeReserveOverdueConflictAssessmentInput(input)
	if err := validateReserveOverdueConflictAssessmentInput(input); err != nil {
		return nil, nil, false, err
	}
	var reservation *OverdueConflictAssessmentReservation
	var dossier *OverdueConflictAssessmentDossier
	reserved := false
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if err := ensureActiveTeamForMutation(ctx, tx, input.TeamID); err != nil {
			return err
		}
		var status string
		var version int
		if err := tx.WithContext(ctx).Raw(`
			SELECT status, version
			FROM relationship_conflict_cases
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			FOR UPDATE
		`, input.TeamID, input.ConflictID).Row().Scan(&status, &version); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			return err
		}
		if status != string(domain.RelationshipConflictOverdue) {
			return nil
		}
		var pending int
		if err := tx.WithContext(ctx).Raw(`
			SELECT count(*)::int
			FROM relationship_conflict_resolution_plans
			WHERE team_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND expected_case_version = ?
			  AND status = 'resolution_pending'
		`, input.TeamID, input.ConflictID, version).Row().Scan(&pending); err != nil {
			return err
		}
		if pending > 0 {
			return nil
		}
		if err := expirePriorOverdueConflictAssessmentReservations(ctx, tx, input, version); err != nil {
			return err
		}
		failureCount, err := countFailedOverdueConflictAssessments(ctx, tx, input.TeamID, input.ConflictID, version, input.Model, input.PolicyVersion)
		if err != nil {
			return err
		}
		if failureCount >= conflictAssessmentMaxFailedDays {
			assessmentAttemptID, err := latestFailedOverdueConflictAssessmentID(ctx, tx, input.TeamID, input.ConflictID, version, input.Model, input.PolicyVersion)
			if err != nil {
				return err
			}
			loaded, err := loadOverdueConflictAssessmentDossier(ctx, tx, input.TeamID, input.ConflictID, version)
			if err != nil {
				return err
			}
			reservation = &OverdueConflictAssessmentReservation{
				AssessmentAttemptID: assessmentAttemptID,
				CaseVersion:         version,
				Model:               input.Model,
				PolicyVersion:       input.PolicyVersion,
				LastWriteWins:       true,
			}
			dossier = loaded
			reserved = true
			return nil
		}

		var assessmentAttemptID string
		err = tx.WithContext(ctx).Raw(`
			INSERT INTO relationship_conflict_ai_assessment_attempts (
			    team_id, conflict_id, case_version, local_assessment_date, model, policy_version
			) VALUES (
			    ?::uuid, ?::uuid, ?, ?, ?, ?
			)
			ON CONFLICT (team_id, conflict_id, case_version, local_assessment_date, model, policy_version)
			DO NOTHING
			RETURNING assessment_attempt_id::text
		`, input.TeamID, input.ConflictID, version, input.LocalAssessmentDate, input.Model, input.PolicyVersion).Row().Scan(&assessmentAttemptID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if assessmentAttemptID == "" {
			return nil
		}
		if err := appendConflictAssessmentEvent(ctx, tx, input.TeamID, assessmentAttemptID, "reserved", "", map[string]any{
			"case_version": version,
			"model":        input.Model,
		}); err != nil {
			return err
		}
		if err := appendRelationshipConflictEvent(ctx, tx, input.TeamID, input.ConflictID, "", "", "", string(domain.RelationshipConflictEventAIAssessmentReserved), "reserved", "case:"+input.ConflictID+":assessment:"+assessmentAttemptID+":reserved", map[string]any{
			"assessment_attempt_id": assessmentAttemptID,
			"case_version":          version,
			"model":                 input.Model,
		}); err != nil {
			return err
		}
		loaded, err := loadOverdueConflictAssessmentDossier(ctx, tx, input.TeamID, input.ConflictID, version)
		if err != nil {
			return err
		}
		reservation = &OverdueConflictAssessmentReservation{
			AssessmentAttemptID: assessmentAttemptID,
			CaseVersion:         version,
			Model:               input.Model,
			PolicyVersion:       input.PolicyVersion,
		}
		dossier = loaded
		reserved = true
		return nil
	})
	if err != nil {
		return nil, nil, false, fmt.Errorf("conflict review: reserve overdue assessment: %w", err)
	}
	return reservation, dossier, reserved, nil
}

func (r *LedgerRepositoryImpl) CompleteOverdueConflictAssessment(
	ctx context.Context,
	input CompleteOverdueConflictAssessmentInput,
) (*CompleteOverdueConflictAssessmentResult, error) {
	input = normalizeCompleteOverdueConflictAssessmentInput(input)
	if err := validateCompleteOverdueConflictAssessmentInput(input); err != nil {
		return nil, err
	}
	result := &CompleteOverdueConflictAssessmentResult{}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if err := setConflictSystemTeamContext(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if err := ensureActiveTeamForMutation(ctx, tx, input.TeamID); err != nil {
			return err
		}
		if input.Decision == "selected" {
			if err := validateConflictResolutionPosition(ctx, tx, input.TeamID, input.ConflictID, input.SelectedPositionID); err != nil {
				return err
			}
		}
		status, outcome, err := conflictAssessmentStoredStatus(input)
		if err != nil {
			return err
		}
		update := tx.WithContext(ctx).Exec(`
			UPDATE relationship_conflict_ai_assessment_attempts
			SET status = ?,
			    selected_position_id = NULLIF(?, '')::uuid,
			    confidence = ?,
			    provider_turns = ?,
			    response_hash = ?,
			    failure_class = ?,
			    completed_at = now()
			WHERE team_id = ?::uuid
			  AND assessment_attempt_id = ?::uuid
			  AND conflict_id = ?::uuid
			  AND case_version = ?
			  AND status = 'reserved'
		`, status, input.SelectedPositionID, input.Confidence, input.ProviderTurns, input.ResponseHash, input.FailureClass,
			input.TeamID, input.AssessmentAttemptID, input.ConflictID, input.CaseVersion)
		if update.Error != nil {
			return update.Error
		}
		if update.RowsAffected != 1 {
			return ErrConflictAssessmentReserved
		}
		metadata := map[string]any{
			"case_version":   input.CaseVersion,
			"provider_turns": input.ProviderTurns,
		}
		if input.SelectedPositionID != "" {
			metadata["selected_position_id"] = input.SelectedPositionID
		}
		if input.Confidence != nil {
			metadata["confidence"] = *input.Confidence
		}
		if input.FailureClass != "" {
			metadata["failure_class"] = input.FailureClass
		}
		if err := appendConflictAssessmentEvent(ctx, tx, input.TeamID, input.AssessmentAttemptID, status, outcome, metadata); err != nil {
			return err
		}
		if err := appendRelationshipConflictEvent(ctx, tx, input.TeamID, input.ConflictID, input.SelectedPositionID, "", "", string(domain.RelationshipConflictEventAIAssessed), outcome, "case:"+input.ConflictID+":assessment:"+input.AssessmentAttemptID+":"+status, metadata); err != nil {
			return err
		}
		if status == "failed" {
			var model string
			var policyVersion string
			if err := tx.WithContext(ctx).Raw(`
				SELECT model, policy_version
				FROM relationship_conflict_ai_assessment_attempts
				WHERE team_id = ?::uuid
				  AND assessment_attempt_id = ?::uuid
			`, input.TeamID, input.AssessmentAttemptID).Row().Scan(&model, &policyVersion); err != nil {
				return err
			}
			failureCount, err := countFailedOverdueConflictAssessments(ctx, tx, input.TeamID, input.ConflictID, input.CaseVersion, model, policyVersion)
			if err != nil {
				return err
			}
			result.FailureCount = failureCount
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("conflict review: complete overdue assessment: %w", err)
	}
	return result, nil
}

func normalizeReserveOverdueConflictAssessmentInput(input ReserveOverdueConflictAssessmentInput) ReserveOverdueConflictAssessmentInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ConflictID = strings.TrimSpace(input.ConflictID)
	input.ReviewRunID = strings.TrimSpace(input.ReviewRunID)
	input.WorkerID = strings.TrimSpace(input.WorkerID)
	input.Model = strings.TrimSpace(input.Model)
	input.PolicyVersion = strings.TrimSpace(input.PolicyVersion)
	if input.PolicyVersion == "" {
		input.PolicyVersion = domain.ConflictOverduePolicyVersion
	}
	if input.LocalAssessmentDate.IsZero() {
		input.LocalAssessmentDate = time.Now().UTC()
	}
	year, month, day := input.LocalAssessmentDate.Date()
	input.LocalAssessmentDate = time.Date(
		year, month, day,
		0, 0, 0, 0, time.UTC,
	)
	return input
}

func validateReserveOverdueConflictAssessmentInput(input ReserveOverdueConflictAssessmentInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.ConflictID); err != nil {
		return fmt.Errorf("conflict_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.ReviewRunID); err != nil {
		return fmt.Errorf("review_run_id is required: %w", err)
	}
	if input.WorkerID == "" {
		return errors.New("worker_id is required")
	}
	if input.Model == "" {
		return errors.New("model is required")
	}
	return nil
}

func normalizeCompleteOverdueConflictAssessmentInput(input CompleteOverdueConflictAssessmentInput) CompleteOverdueConflictAssessmentInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.ConflictID = strings.TrimSpace(input.ConflictID)
	input.AssessmentAttemptID = strings.TrimSpace(input.AssessmentAttemptID)
	input.ReviewRunID = strings.TrimSpace(input.ReviewRunID)
	input.Decision = strings.TrimSpace(input.Decision)
	input.SelectedPositionID = strings.TrimSpace(input.SelectedPositionID)
	input.ResponseHash = strings.TrimSpace(input.ResponseHash)
	input.FailureClass = strings.TrimSpace(input.FailureClass)
	if input.ProviderTurns < 0 {
		input.ProviderTurns = 0
	}
	return input
}

func validateCompleteOverdueConflictAssessmentInput(input CompleteOverdueConflictAssessmentInput) error {
	for _, value := range []struct {
		name string
		id   string
	}{
		{name: "team_id", id: input.TeamID},
		{name: "conflict_id", id: input.ConflictID},
		{name: "assessment_attempt_id", id: input.AssessmentAttemptID},
	} {
		if _, err := uuid.Parse(value.id); err != nil {
			return fmt.Errorf("%s is required: %w", value.name, err)
		}
	}
	if input.CaseVersion < 1 {
		return errors.New("case_version is required")
	}
	if len(input.ResponseHash) > 128 {
		return errors.New("response_hash exceeds maximum 128")
	}
	if len(input.FailureClass) > 128 {
		return errors.New("failure_class exceeds maximum 128")
	}
	switch input.Decision {
	case "selected":
		if _, err := uuid.Parse(input.SelectedPositionID); err != nil {
			return fmt.Errorf("selected_position_id is required: %w", err)
		}
		if input.Confidence == nil || *input.Confidence < 0 || *input.Confidence > 1 {
			return errors.New("selected assessment confidence must be between 0 and 1")
		}
	case "abstained":
		if input.SelectedPositionID != "" || input.Confidence == nil || *input.Confidence != 0 {
			return errors.New("abstained assessment must not select a position and must use zero confidence")
		}
	case "failed":
		if input.SelectedPositionID != "" || input.Confidence != nil || input.FailureClass == "" {
			return errors.New("failed assessment requires a failure class and no selected position")
		}
	default:
		return errors.New("assessment decision is unsupported")
	}
	return nil
}

func conflictAssessmentStoredStatus(input CompleteOverdueConflictAssessmentInput) (string, string, error) {
	switch input.Decision {
	case "selected":
		return "selected", "selected", nil
	case "abstained":
		return "abstained", "abstained", nil
	case "failed":
		return "failed", input.FailureClass, nil
	default:
		return "", "", errors.New("assessment decision is unsupported")
	}
}

func appendConflictAssessmentEvent(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	assessmentAttemptID string,
	action string,
	outcome string,
	metadata map[string]any,
) error {
	encoded, err := marshalJSON(metadata)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO relationship_conflict_ai_assessment_events (
		    team_id, assessment_attempt_id, action, outcome, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?, ?, ?::jsonb
		)
	`, teamID, assessmentAttemptID, action, outcome, string(encoded)).Error
}

func supersedeReservedOverdueConflictAssessments(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
) error {
	rows, err := tx.WithContext(ctx).Raw(`
		UPDATE relationship_conflict_ai_assessment_attempts
		SET status = 'superseded',
		    failure_class = 'case_version_changed',
		    completed_at = now()
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND status = 'reserved'
		RETURNING assessment_attempt_id::text, case_version
	`, teamID, conflictID).Rows()
	if err != nil {
		return err
	}
	type supersededAssessment struct {
		assessmentAttemptID string
		caseVersion         int
	}
	superseded := []supersededAssessment{}
	for rows.Next() {
		item := supersededAssessment{}
		if err := rows.Scan(&item.assessmentAttemptID, &item.caseVersion); err != nil {
			_ = rows.Close()
			return err
		}
		superseded = append(superseded, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range superseded {
		metadata := map[string]any{
			"case_version":  item.caseVersion,
			"failure_class": "case_version_changed",
		}
		if err := appendConflictAssessmentEvent(ctx, tx, teamID, item.assessmentAttemptID, "superseded", "case_version_changed", metadata); err != nil {
			return err
		}
		if err := appendRelationshipConflictEvent(ctx, tx, teamID, conflictID, "", "", "", string(domain.RelationshipConflictEventAIAssessed), "superseded", "case:"+conflictID+":assessment:"+item.assessmentAttemptID+":superseded", metadata); err != nil {
			return err
		}
	}
	return nil
}

func supersedePendingOverdueConflictResolutions(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
) error {
	return tx.WithContext(ctx).Exec(`
		UPDATE relationship_conflict_resolution_plans
		SET status = 'superseded',
		    failure_reason = 'case_version_changed'
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND status = 'resolution_pending'
	`, teamID, conflictID).Error
}

func loadOverdueConflictAssessmentDossier(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	conflictID string,
	caseVersion int,
) (*OverdueConflictAssessmentDossier, error) {
	dossier := &OverdueConflictAssessmentDossier{TeamID: teamID, ConflictID: conflictID, CaseVersion: caseVersion}
	if err := tx.WithContext(ctx).Raw(`
		SELECT question
		FROM relationship_conflict_cases
		WHERE team_id = ?::uuid
		  AND conflict_id = ?::uuid
		  AND version = ?
		  AND status = 'overdue'
	`, teamID, conflictID, caseVersion).Row().Scan(&dossier.Question); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConflictAssessmentStale
		}
		return nil, err
	}
	positionByID := make(map[string]*OverdueConflictAssessmentPosition)
	positionRows, err := tx.WithContext(ctx).Raw(`
		SELECT position.position_id::text,
		       position.position_key,
		       position.support_group_count,
		       position.authoritative_group_count,
		       (
		           SELECT count(DISTINCT member.owner_profile_id)::int
		           FROM relationship_conflict_position_members AS member
		           WHERE member.team_id = position.team_id
		             AND member.position_id = position.position_id
		             AND member.active
		       ) AS owner_profile_count
		FROM relationship_conflict_positions AS position
		WHERE position.team_id = ?::uuid
		  AND position.conflict_id = ?::uuid
		  AND position.active
		ORDER BY position.position_id
	`, teamID, conflictID).Rows()
	if err != nil {
		return nil, err
	}
	defer positionRows.Close()
	for positionRows.Next() {
		position := OverdueConflictAssessmentPosition{}
		if err := positionRows.Scan(&position.PositionID, &position.PositionKey, &position.SupportGroupCount, &position.AuthoritativeGroupCount, &position.OwnerProfileCount); err != nil {
			return nil, err
		}
		dossier.Positions = append(dossier.Positions, position)
		positionByID[position.PositionID] = &dossier.Positions[len(dossier.Positions)-1]
	}
	if err := positionRows.Err(); err != nil {
		return nil, err
	}
	if len(dossier.Positions) < 2 {
		return nil, ErrConflictAssessmentUnavailable
	}

	evidenceRows, err := tx.WithContext(ctx).Raw(`
		WITH member_relationships AS (
			SELECT DISTINCT member.position_id, member.relationship_id, member.owner_profile_id
			FROM relationship_conflict_position_members AS member
			JOIN relationship_conflict_positions AS position
			  ON position.team_id = member.team_id
			 AND position.position_id = member.position_id
			WHERE member.team_id = ?::uuid
			  AND member.conflict_id = ?::uuid
			  AND member.active
			  AND position.active
		),
		latest_support_decision AS (
			SELECT DISTINCT ON (support.support_id)
			       support.support_id,
			       decision.decision
			FROM relationship_evidence_supports AS support
			JOIN member_relationships AS member
			  ON member.relationship_id = support.relationship_id
			 AND member.owner_profile_id = support.owner_profile_id
			JOIN relationship_support_decision_events AS decision
			  ON decision.team_id = support.team_id
			 AND decision.support_id = support.support_id
			WHERE support.team_id = ?::uuid
			ORDER BY support.support_id, decision.created_at DESC, decision.support_decision_id DESC
		)
		SELECT member.position_id::text,
		       fragment.fragment_id::text,
		       fragment.owner_profile_id::text,
		       support.support_id::text,
		       COALESCE(
		           NULLIF(source.source_key, ''),
		           NULLIF(fragment.metadata->>'contract_source_group', ''),
		           NULLIF(fragment.metadata->>'v2_contract_source_group', ''),
		           NULLIF(support.source_group_key, ''),
		           support.support_id::text
		       ) AS source_group_key,
		       support.authority,
		       support.created_at,
		       relationship.valid_from,
		       fragment.evidence_index,
		       fragment.content
		FROM member_relationships AS member
		JOIN relationship_records AS relationship
		  ON relationship.team_id = ?::uuid
		 AND relationship.relationship_id = member.relationship_id
		 AND relationship.owner_profile_id = member.owner_profile_id
		JOIN relationship_evidence_supports AS support
		  ON support.team_id = relationship.team_id
		 AND support.relationship_id = relationship.relationship_id
		 AND support.owner_profile_id = relationship.owner_profile_id
		JOIN latest_support_decision AS latest
		  ON latest.support_id = support.support_id
		 AND latest.decision IN ('grant', 'reinstate')
		JOIN evidence_fragments AS fragment
		  ON fragment.team_id = support.team_id
		 AND fragment.fragment_id = support.fragment_id
		LEFT JOIN evidence_quarantines AS quarantine
		  ON quarantine.team_id = support.team_id
		 AND quarantine.fragment_id = support.fragment_id
		 AND quarantine.status = 'active'
		LEFT JOIN evidence_sources AS source
		  ON source.team_id = support.team_id
		 AND source.source_id = support.source_id
		LEFT JOIN evidence_lifecycle_events AS lifecycle
		  ON lifecycle.team_id = support.team_id
		 AND lifecycle.target_fragment_id = support.fragment_id
		WHERE quarantine.quarantine_id IS NULL
		  AND lifecycle.lifecycle_event_id IS NULL
		  AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		  AND (support.source_id IS NULL OR source.current_revision_id = support.source_revision_id)
		ORDER BY member.position_id, support.created_at, support.support_id
	`, teamID, conflictID, teamID, teamID).Rows()
	if err != nil {
		return nil, err
	}
	defer evidenceRows.Close()
	for evidenceRows.Next() {
		item := OverdueConflictAssessmentEvidence{}
		if err := evidenceRows.Scan(
			&item.PositionID,
			&item.FragmentID,
			&item.OwnerProfileID,
			&item.SupportID,
			&item.SourceGroupKey,
			&item.Authority,
			&item.AcceptedAt,
			&item.EffectiveAt,
			&item.EvidenceIndex,
			&item.Content,
		); err != nil {
			return nil, err
		}
		position, exists := positionByID[item.PositionID]
		if !exists {
			return nil, ErrConflictAssessmentStale
		}
		item.AcceptedAt = item.AcceptedAt.UTC()
		if item.EffectiveAt != nil {
			value := item.EffectiveAt.UTC()
			item.EffectiveAt = &value
		}
		position.Supports = append(position.Supports, domain.ConflictResolutionSupport{Authority: item.Authority, AcceptedAt: item.AcceptedAt})
		dossier.Evidence = append(dossier.Evidence, item)
	}
	if err := evidenceRows.Err(); err != nil {
		return nil, err
	}
	if len(dossier.Evidence) == 0 {
		return nil, ErrConflictAssessmentUnavailable
	}
	return dossier, nil
}
