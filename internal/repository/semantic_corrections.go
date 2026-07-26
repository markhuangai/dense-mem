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

var (
	ErrSemanticCorrectionPlanStale   = errors.New("semantic correction plan stale")
	ErrSemanticCorrectionPlanExpired = errors.New("semantic correction plan expired")
)

const entityCorrectionPlanTTL = 2 * time.Hour

type entityCorrectionObservation struct {
	ObservationID       string
	RelationshipID      string
	RelationshipVersion int
	SubjectRef          string
	ObjectRef           string
	SubjectEntityID     string
	ObjectEntityID      string
	ObjectValueID       string
	PredicateKey        string
	PredicateVersion    int
	Polarity            string
	ScopeKey            string
	ValidFrom           sql.NullTime
	ValidTo             sql.NullTime
}

type entityCorrectionRelationshipImpact struct {
	RelationshipID string   `json:"relationship_id"`
	Version        int      `json:"version"`
	ObservationIDs []string `json:"observation_ids"`
}

type entityCorrectionPlanRow struct {
	TeamID                 string
	PlanToken              string
	OwnerProfileID         string
	Action                 string
	SourceEntityID         string
	TargetEntityID         string
	NewEntityID            string
	SelectedObservationIDs []string
	BlockedObservationIDs  []string
	AffectedRelationships  []entityCorrectionRelationshipImpact
	ImpactSummary          string
	IdempotencyKey         string
	Status                 string
	CorrectionEventID      string
	ExpiresAt              time.Time
}

func (r *SemanticRepositoryImpl) CorrectEntityResolution(
	ctx context.Context,
	input CorrectEntityResolutionInput,
) (*CorrectEntityResolutionResult, error) {
	input = normalizeCorrectEntityResolutionInput(input)
	if err := validateCorrectEntityResolutionInput(input); err != nil {
		return nil, err
	}
	var result *CorrectEntityResolutionResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureSemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		var err error
		if input.DryRun {
			result, err = r.planEntityCorrection(ctx, tx, input)
		} else {
			result, err = r.applyEntityCorrection(ctx, tx, input)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: correct entity resolution: %w", err)
	}
	return result, nil
}

func (r *SemanticRepositoryImpl) planEntityCorrection(
	ctx context.Context,
	tx *gorm.DB,
	input CorrectEntityResolutionInput,
) (*CorrectEntityResolutionResult, error) {
	if input.IdempotencyKey != "" {
		existing, err := loadEntityCorrectionPlanByIdempotency(ctx, tx, input)
		if errors.Is(err, gorm.ErrRecordNotFound) {
			existing = nil
		} else if err != nil {
			return nil, err
		}
		if existing != nil {
			if existing.Action != input.Action ||
				existing.SourceEntityID != input.SourceEntityID ||
				existing.TargetEntityID != input.TargetEntityID {
				return nil, ErrSemanticIdempotencyConflict
			}
			return existing.toResult(true), nil
		}
	}
	observations, blockedIDs, err := loadEntityCorrectionObservations(ctx, tx, input)
	if err != nil {
		return nil, err
	}
	impacts, selectedIDs, conflictBlockedIDs, err := buildEntityCorrectionImpacts(ctx, tx, input, observations)
	if err != nil {
		return nil, err
	}
	blockedIDs = uniqueStrings(append(blockedIDs, conflictBlockedIDs...))
	if len(selectedIDs) == 0 {
		return &CorrectEntityResolutionResult{
			DryRun:                 true,
			SelectedObservationIDs: []string{},
			BlockedObservationIDs:  blockedIDs,
			ImpactSummary:          entityCorrectionSummary(input.Action, 0, 0, len(blockedIDs), false),
		}, nil
	}
	planToken, err := insertEntityCorrectionPlan(ctx, tx, input, impacts, selectedIDs, blockedIDs)
	if err != nil {
		return nil, err
	}
	return &CorrectEntityResolutionResult{
		DryRun:                 true,
		PlanToken:              planToken,
		SelectedObservationIDs: selectedIDs,
		BlockedObservationIDs:  blockedIDs,
		ImpactSummary:          entityCorrectionSummary(input.Action, len(impacts), len(selectedIDs), len(blockedIDs), false),
	}, nil
}

func (r *SemanticRepositoryImpl) applyEntityCorrection(
	ctx context.Context,
	tx *gorm.DB,
	input CorrectEntityResolutionInput,
) (*CorrectEntityResolutionResult, error) {
	plan, err := loadEntityCorrectionPlanByToken(ctx, tx, input.TeamID, input.OwnerProfileID, input.PlanToken, true)
	if err != nil {
		return nil, err
	}
	if plan.Action != input.Action ||
		plan.SourceEntityID != input.SourceEntityID ||
		plan.TargetEntityID != input.TargetEntityID {
		return nil, ErrSemanticIdempotencyConflict
	}
	if plan.Status == "applied" {
		return plan.toResult(false), nil
	}
	if time.Now().UTC().After(plan.ExpiresAt) {
		return nil, ErrSemanticCorrectionPlanExpired
	}
	if len(plan.AffectedRelationships) == 0 || len(plan.SelectedObservationIDs) == 0 {
		return nil, errors.New("entity correction plan has no selected caller-owned observations")
	}
	currentVersions, err := lockEntityCorrectionRelationships(ctx, tx, plan)
	if err != nil {
		return nil, err
	}
	for _, impact := range plan.AffectedRelationships {
		if currentVersions[impact.RelationshipID] != impact.Version {
			return nil, ErrSemanticCorrectionPlanStale
		}
	}
	replacementEntityID := plan.TargetEntityID
	if plan.Action == string(domain.EntityCorrectionSplit) {
		replacementEntityID, err = createEntityCorrectionSplitEntity(ctx, tx, plan)
		if err != nil {
			return nil, err
		}
	}
	if plan.Action == string(domain.EntityCorrectionMerge) {
		if err := ensureEntityExists(ctx, tx, plan.TeamID, replacementEntityID); err != nil {
			return nil, err
		}
		if err := rejectMergeConflictsAtApply(ctx, tx, plan, replacementEntityID); err != nil {
			return nil, err
		}
	}
	if err := updateEntityCorrectionRelationships(ctx, tx, plan, replacementEntityID); err != nil {
		return nil, err
	}
	correctionEventID, err := insertEntityCorrectionEvent(ctx, tx, plan, replacementEntityID)
	if err != nil {
		return nil, err
	}
	if err := markEntityCorrectionPlanApplied(ctx, tx, plan, correctionEventID, replacementEntityID); err != nil {
		return nil, err
	}
	plan.Status = "applied"
	plan.CorrectionEventID = correctionEventID
	if plan.Action == string(domain.EntityCorrectionSplit) {
		plan.NewEntityID = replacementEntityID
	}
	plan.ImpactSummary = entityCorrectionSummary(plan.Action, len(plan.AffectedRelationships), len(plan.SelectedObservationIDs), len(plan.BlockedObservationIDs), true)
	return plan.toResult(false), nil
}

func normalizeCorrectEntityResolutionInput(input CorrectEntityResolutionInput) CorrectEntityResolutionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Action = strings.TrimSpace(input.Action)
	input.SourceEntityID = strings.TrimSpace(input.SourceEntityID)
	input.TargetEntityID = strings.TrimSpace(input.TargetEntityID)
	input.PlanToken = strings.TrimSpace(input.PlanToken)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	input.SelectedObservationIDs = uniqueStrings(input.SelectedObservationIDs)
	for i := range input.Evidence {
		input.Evidence[i].Content = strings.TrimSpace(input.Evidence[i].Content)
		input.Evidence[i].SourceType = strings.TrimSpace(input.Evidence[i].SourceType)
		input.Evidence[i].Authority = strings.TrimSpace(input.Evidence[i].Authority)
		input.Evidence[i].SourceGroup = strings.TrimSpace(input.Evidence[i].SourceGroup)
	}
	return input
}

func validateCorrectEntityResolutionInput(input CorrectEntityResolutionInput) error {
	for label, value := range map[string]string{
		"team_id":          input.TeamID,
		"owner_profile_id": input.OwnerProfileID,
		"source_entity_id": input.SourceEntityID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	switch domain.EntityCorrectionAction(input.Action) {
	case domain.EntityCorrectionMerge:
		if _, err := uuid.Parse(input.TargetEntityID); err != nil {
			return fmt.Errorf("target_entity_id is required: %w", err)
		}
		if input.TargetEntityID == input.SourceEntityID {
			return errors.New("target_entity_id must differ from source_entity_id")
		}
	case domain.EntityCorrectionSplit:
		if input.TargetEntityID != "" {
			return errors.New("target_entity_id is only accepted for merge")
		}
		if len(input.SelectedObservationIDs) == 0 {
			return errors.New("selected_observation_ids is required for split")
		}
	default:
		return fmt.Errorf("unsupported entity correction action %q", input.Action)
	}
	for _, observationID := range input.SelectedObservationIDs {
		if _, err := uuid.Parse(observationID); err != nil {
			return fmt.Errorf("selected_observation_ids contains invalid id %q: %w", observationID, err)
		}
	}
	if !input.DryRun {
		if _, err := uuid.Parse(input.PlanToken); err != nil {
			return fmt.Errorf("plan_token is required: %w", err)
		}
	}
	return nil
}

func loadEntityCorrectionObservations(
	ctx context.Context,
	tx *gorm.DB,
	input CorrectEntityResolutionInput,
) ([]entityCorrectionObservation, []string, error) {
	args := []any{input.TeamID, input.OwnerProfileID, input.OwnerProfileID, input.SourceEntityID, input.SourceEntityID, input.SourceEntityID, input.SourceEntityID}
	filter := ""
	if input.Action == string(domain.EntityCorrectionSplit) {
		filter = "AND o.observation_id = ANY(?::uuid[])"
		args = append(args, pq.Array(input.SelectedObservationIDs))
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT o.observation_id::text,
		       r.relationship_id::text,
		       r.version,
		       o.subject_ref,
		       o.object_ref,
		       r.subject_entity_id::text,
		       COALESCE(r.object_entity_id::text, ''),
		       COALESCE(r.object_value_id::text, ''),
		       r.predicate_key,
		       r.predicate_version,
		       r.polarity,
		       COALESCE(r.scope_key, ''),
		       r.valid_from,
		       r.valid_to
		FROM relationship_observations AS o
		JOIN relationship_records AS r
		  ON r.team_id = o.team_id
		 AND r.relationship_id = o.relationship_id
		WHERE o.team_id = ?::uuid
		  AND o.owner_profile_id = ?::uuid
		  AND r.owner_profile_id = ?::uuid
		  AND r.identity_alias_of_relationship_id IS NULL
		  AND o.relationship_id IS NOT NULL
		  AND (
		      o.subject_entity_id = ?::uuid
		      OR o.object_entity_id = ?::uuid
		      OR r.subject_entity_id = ?::uuid
		      OR r.object_entity_id = ?::uuid
		  )
		`+filter+`
		ORDER BY o.created_at, o.observation_id
	`, args...).Rows()
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var observations []entityCorrectionObservation
	found := map[string]struct{}{}
	for rows.Next() {
		observation := entityCorrectionObservation{}
		if err := rows.Scan(
			&observation.ObservationID,
			&observation.RelationshipID,
			&observation.RelationshipVersion,
			&observation.SubjectRef,
			&observation.ObjectRef,
			&observation.SubjectEntityID,
			&observation.ObjectEntityID,
			&observation.ObjectValueID,
			&observation.PredicateKey,
			&observation.PredicateVersion,
			&observation.Polarity,
			&observation.ScopeKey,
			&observation.ValidFrom,
			&observation.ValidTo,
		); err != nil {
			return nil, nil, err
		}
		found[observation.ObservationID] = struct{}{}
		observations = append(observations, observation)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if input.Action != string(domain.EntityCorrectionSplit) {
		return observations, nil, nil
	}
	var blocked []string
	for _, selectedID := range input.SelectedObservationIDs {
		if _, ok := found[selectedID]; !ok {
			blocked = append(blocked, selectedID)
		}
	}
	return observations, blocked, nil
}

func buildEntityCorrectionImpacts(
	ctx context.Context,
	tx *gorm.DB,
	input CorrectEntityResolutionInput,
	observations []entityCorrectionObservation,
) ([]entityCorrectionRelationshipImpact, []string, []string, error) {
	byRelationship := map[string][]entityCorrectionObservation{}
	for _, observation := range observations {
		byRelationship[observation.RelationshipID] = append(byRelationship[observation.RelationshipID], observation)
	}
	relationshipIDs := make([]string, 0, len(byRelationship))
	for relationshipID := range byRelationship {
		relationshipIDs = append(relationshipIDs, relationshipID)
	}
	sort.Strings(relationshipIDs)
	var impacts []entityCorrectionRelationshipImpact
	var selectedIDs []string
	var blockedIDs []string
	seenMergeKeys := map[string]struct{}{}
	for _, relationshipID := range relationshipIDs {
		group := byRelationship[relationshipID]
		base := group[0]
		if base.SubjectEntityID != input.SourceEntityID && base.ObjectEntityID != input.SourceEntityID {
			blockedIDs = append(blockedIDs, observationIDsFromCorrectionGroup(group)...)
			continue
		}
		if input.Action == string(domain.EntityCorrectionMerge) {
			key, conflict, err := mergeConflictKey(ctx, tx, input, base)
			if err != nil {
				return nil, nil, nil, err
			}
			if conflict {
				blockedIDs = append(blockedIDs, observationIDsFromCorrectionGroup(group)...)
				continue
			}
			if _, duplicate := seenMergeKeys[key]; duplicate {
				blockedIDs = append(blockedIDs, observationIDsFromCorrectionGroup(group)...)
				continue
			}
			seenMergeKeys[key] = struct{}{}
		}
		ids := observationIDsFromCorrectionGroup(group)
		selectedIDs = append(selectedIDs, ids...)
		impacts = append(impacts, entityCorrectionRelationshipImpact{
			RelationshipID: relationshipID,
			Version:        base.RelationshipVersion,
			ObservationIDs: ids,
		})
	}
	return impacts, uniqueStrings(selectedIDs), uniqueStrings(blockedIDs), nil
}

func observationIDsFromCorrectionGroup(group []entityCorrectionObservation) []string {
	ids := make([]string, 0, len(group))
	for _, observation := range group {
		ids = append(ids, observation.ObservationID)
	}
	return uniqueStrings(ids)
}

func mergeConflictKey(
	ctx context.Context,
	tx *gorm.DB,
	input CorrectEntityResolutionInput,
	observation entityCorrectionObservation,
) (string, bool, error) {
	subjectEntityID := observation.SubjectEntityID
	objectEntityID := observation.ObjectEntityID
	if subjectEntityID == input.SourceEntityID {
		subjectEntityID = input.TargetEntityID
	}
	if objectEntityID == input.SourceEntityID {
		objectEntityID = input.TargetEntityID
	}
	key := strings.Join([]string{
		subjectEntityID,
		observation.PredicateKey,
		objectEntityID,
		observation.ObjectValueID,
		observation.Polarity,
		observation.ScopeKey,
		nullableTimeKey(observation.ValidFrom),
	}, "\x00")
	conflict, err := hasRelationshipIdentityConflict(ctx, tx, input.TeamID, input.OwnerProfileID, observation.RelationshipID, subjectEntityID, objectEntityID, observation)
	return key, conflict, err
}

func hasRelationshipIdentityConflict(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	ownerProfileID string,
	relationshipID string,
	subjectEntityID string,
	objectEntityID string,
	observation entityCorrectionObservation,
) (bool, error) {
	var existing string
	err := tx.WithContext(ctx).Raw(`
		SELECT relationship_id::text
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND relationship_id <> ?::uuid
		  AND subject_entity_id = ?::uuid
		  AND predicate_key = ?
		  AND object_entity_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND object_value_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND polarity = ?
		  AND valid_from IS NOT DISTINCT FROM ?
		  AND scope_key IS NOT DISTINCT FROM NULLIF(?, '')
		  AND identity_alias_of_relationship_id IS NULL
		LIMIT 1
	`, teamID, ownerProfileID, relationshipID, subjectEntityID, observation.PredicateKey,
		objectEntityID, observation.ObjectValueID,
		observation.Polarity, nullableTimeArg(observation.ValidFrom),
		observation.ScopeKey).Row().Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return existing != "", err
}

func insertEntityCorrectionPlan(
	ctx context.Context,
	tx *gorm.DB,
	input CorrectEntityResolutionInput,
	impacts []entityCorrectionRelationshipImpact,
	selectedIDs []string,
	blockedIDs []string,
) (string, error) {
	affected, err := json.Marshal(impacts)
	if err != nil {
		return "", fmt.Errorf("marshal affected relationships: %w", err)
	}
	evidenceInput := input.Evidence
	if evidenceInput == nil {
		evidenceInput = []CorrectionEvidenceInput{}
	}
	evidence, err := json.Marshal(evidenceInput)
	if err != nil {
		return "", fmt.Errorf("marshal correction evidence: %w", err)
	}
	metadata, err := marshalJSON(map[string]any{
		"contract": "correct_entity_resolution",
	})
	if err != nil {
		return "", err
	}
	summary := entityCorrectionSummary(input.Action, len(impacts), len(selectedIDs), len(blockedIDs), false)
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO entity_correction_plans (
		    team_id, owner_profile_id, action, source_entity_id, target_entity_id,
		    selected_observation_ids, blocked_observation_ids, affected_relationships,
		    evidence, impact_summary, idempotency_key, expires_at, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?, ?::uuid, NULLIF(?, '')::uuid,
		    ?::uuid[], ?::uuid[], ?::jsonb, ?::jsonb, ?, ?, ?, ?::jsonb
		)
		RETURNING plan_token::text
	`, input.TeamID, input.OwnerProfileID, input.Action, input.SourceEntityID,
		input.TargetEntityID, pq.Array(selectedIDs), pq.Array(blockedIDs),
		string(affected), string(evidence), summary, input.IdempotencyKey,
		time.Now().UTC().Add(entityCorrectionPlanTTL), string(metadata)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var planToken string
	if err := rows.Scan(&planToken); err != nil {
		return "", err
	}
	return planToken, rows.Err()
}

func loadEntityCorrectionPlanByIdempotency(
	ctx context.Context,
	tx *gorm.DB,
	input CorrectEntityResolutionInput,
) (*entityCorrectionPlanRow, error) {
	if input.IdempotencyKey == "" {
		return nil, nil
	}
	return scanEntityCorrectionPlan(ctx, tx, `
		SELECT team_id::text, plan_token::text, owner_profile_id::text, action,
		       source_entity_id::text, COALESCE(target_entity_id::text, ''),
		       COALESCE(new_entity_id::text, ''),
		       ARRAY(SELECT unnest(selected_observation_ids)::text),
		       ARRAY(SELECT unnest(blocked_observation_ids)::text),
		       affected_relationships, impact_summary, idempotency_key, status,
		       COALESCE(correction_event_id::text, ''), expires_at
		FROM entity_correction_plans
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND idempotency_key = ?
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey)
}

func loadEntityCorrectionPlanByToken(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	ownerProfileID string,
	planToken string,
	forUpdate bool,
) (*entityCorrectionPlanRow, error) {
	lockClause := ""
	if forUpdate {
		lockClause = "FOR UPDATE"
	}
	return scanEntityCorrectionPlan(ctx, tx, `
		SELECT team_id::text, plan_token::text, owner_profile_id::text, action,
		       source_entity_id::text, COALESCE(target_entity_id::text, ''),
		       COALESCE(new_entity_id::text, ''),
		       ARRAY(SELECT unnest(selected_observation_ids)::text),
		       ARRAY(SELECT unnest(blocked_observation_ids)::text),
		       affected_relationships, impact_summary, idempotency_key, status,
		       COALESCE(correction_event_id::text, ''), expires_at
		FROM entity_correction_plans
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND plan_token = ?::uuid
		`+lockClause+`
	`, teamID, ownerProfileID, planToken)
}

func scanEntityCorrectionPlan(ctx context.Context, tx *gorm.DB, query string, args ...any) (*entityCorrectionPlanRow, error) {
	rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, gorm.ErrRecordNotFound
	}
	var selected pq.StringArray
	var blocked pq.StringArray
	var affectedJSON []byte
	plan := &entityCorrectionPlanRow{}
	if err := rows.Scan(
		&plan.TeamID,
		&plan.PlanToken,
		&plan.OwnerProfileID,
		&plan.Action,
		&plan.SourceEntityID,
		&plan.TargetEntityID,
		&plan.NewEntityID,
		&selected,
		&blocked,
		&affectedJSON,
		&plan.ImpactSummary,
		&plan.IdempotencyKey,
		&plan.Status,
		&plan.CorrectionEventID,
		&plan.ExpiresAt,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(affectedJSON, &plan.AffectedRelationships); err != nil {
		return nil, fmt.Errorf("decode affected relationships: %w", err)
	}
	plan.SelectedObservationIDs = []string(selected)
	plan.BlockedObservationIDs = []string(blocked)
	return plan, rows.Err()
}

func (p *entityCorrectionPlanRow) toResult(dryRun bool) *CorrectEntityResolutionResult {
	return &CorrectEntityResolutionResult{
		DryRun:                 dryRun,
		PlanToken:              p.PlanToken,
		SelectedObservationIDs: p.SelectedObservationIDs,
		BlockedObservationIDs:  p.BlockedObservationIDs,
		ImpactSummary:          p.ImpactSummary,
		CorrectionEventID:      p.CorrectionEventID,
		NewEntityID:            p.NewEntityID,
	}
}

func entityCorrectionSummary(action string, relationships int, observations int, blocked int, applied bool) string {
	state := "will update"
	if applied {
		state = "updated"
	}
	return fmt.Sprintf("%s %s %d caller-owned relationship(s) across %d observation(s); %d observation(s) blocked", action, state, relationships, observations, blocked)
}

func uniqueStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func nullableTimeArg(value sql.NullTime) any {
	if !value.Valid {
		return nil
	}
	return value.Time.UTC()
}

func nullableTimeKey(value sql.NullTime) string {
	if !value.Valid {
		return ""
	}
	return value.Time.UTC().Format(time.RFC3339Nano)
}
