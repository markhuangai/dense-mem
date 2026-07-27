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
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type predicateDefinition struct {
	Key                 string
	Version             int
	AllowedSubjectKinds []string
	AllowedObjectKinds  []string
	RelationshipKind    string
	CurrentCardinality  string
}

type relationshipRecordState struct {
	Record          *RelationshipRecord
	Created         bool
	Changed         bool
	ValidToConflict bool
	FromTier        string
	FromStatus      string
}

type transitionInput struct {
	TeamID              string
	OwnerProfileID      string
	RelationshipID      string
	IdempotencyKey      string
	FromTier            string
	FromStatus          string
	ToTier              string
	ToStatus            string
	Reason              string
	VerificationEventID string
	SupportDecisionID   string
}

func normalizeCreateEntityInput(input CreateEntityInput) CreateEntityInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.EntityKind = strings.TrimSpace(input.EntityKind)
	input.CanonicalName = strings.TrimSpace(input.CanonicalName)
	if input.EntityKind == "" {
		input.EntityKind = string(domain.EntityKindOther)
	}
	return input
}

func validateCreateEntityInput(input CreateEntityInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if !contains(domain.EntityKinds(), input.EntityKind) {
		return fmt.Errorf("unsupported entity_kind %q", input.EntityKind)
	}
	if input.CanonicalName == "" {
		return errors.New("canonical_name is required")
	}
	return nil
}

func normalizeAddEntityNameInput(input AddEntityNameInput) AddEntityNameInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.EntityID = strings.TrimSpace(input.EntityID)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	input.NameKind = strings.TrimSpace(input.NameKind)
	input.Locale = strings.TrimSpace(input.Locale)
	if input.NameKind == "" {
		input.NameKind = "alias"
	}
	return input
}

func validateAddEntityNameInput(input AddEntityNameInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.EntityID); err != nil {
		return fmt.Errorf("entity_id is required: %w", err)
	}
	if input.DisplayName == "" {
		return errors.New("display_name is required")
	}
	if input.NameKind != "canonical" && input.NameKind != "alias" && input.NameKind != "former" {
		return fmt.Errorf("unsupported name_kind %q", input.NameKind)
	}
	return nil
}

func normalizeUpsertValueInput(input UpsertValueInput) UpsertValueInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.ValueType = strings.TrimSpace(input.ValueType)
	input.CanonicalValue = strings.TrimSpace(input.CanonicalValue)
	input.Unit = strings.TrimSpace(input.Unit)
	input.Display = strings.TrimSpace(input.Display)
	if input.Display == "" {
		input.Display = input.CanonicalValue
	}
	if input.NormalizationVersion == 0 {
		input.NormalizationVersion = 1
	}
	return input
}

func validateUpsertValueInput(input UpsertValueInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if !contains(domain.ValueTypes(), input.ValueType) {
		return fmt.Errorf("unsupported value_type %q", input.ValueType)
	}
	if input.CanonicalValue == "" {
		return errors.New("canonical_value is required")
	}
	if input.NormalizationVersion < 1 {
		return errors.New("normalization_version must be greater than zero")
	}
	return nil
}

func normalizeApplyRelationshipDecisionInput(input ApplyRelationshipDecisionInput) ApplyRelationshipDecisionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.PlacementItemID = strings.TrimSpace(input.PlacementItemID)
	input.ProposalRef = strings.TrimSpace(input.ProposalRef)
	input.SubjectRef = strings.TrimSpace(input.SubjectRef)
	input.SubjectEntityID = strings.TrimSpace(input.SubjectEntityID)
	input.OriginalPredicate = strings.TrimSpace(input.OriginalPredicate)
	input.PredicateKey = strings.TrimSpace(input.PredicateKey)
	input.ObjectRef = strings.TrimSpace(input.ObjectRef)
	input.ObjectEntityID = strings.TrimSpace(input.ObjectEntityID)
	input.ObjectValueID = strings.TrimSpace(input.ObjectValueID)
	input.Polarity = strings.TrimSpace(input.Polarity)
	input.ScopeKey = strings.TrimSpace(input.ScopeKey)
	input.EvidenceVerdict = strings.TrimSpace(input.EvidenceVerdict)
	input.Rationale = strings.TrimSpace(input.Rationale)
	input.Model = strings.TrimSpace(input.Model)
	input.ResponseHash = strings.TrimSpace(input.ResponseHash)
	if input.PredicateVersion == 0 {
		input.PredicateVersion = 1
	}
	if input.OriginalPredicate == "" {
		input.OriginalPredicate = input.PredicateKey
	}
	if input.SubjectRef == "" {
		input.SubjectRef = input.SubjectEntityID
	}
	if input.ObjectRef == "" {
		input.ObjectRef = input.ObjectEntityID
		if input.ObjectRef == "" {
			input.ObjectRef = input.ObjectValueID
		}
	}
	if input.Polarity == "" {
		input.Polarity = "+"
	}
	if input.EvidenceVerdict == "" {
		input.EvidenceVerdict = string(domain.VerificationEntailed)
	}
	if input.Support != nil {
		input.Support.FragmentID = strings.TrimSpace(input.Support.FragmentID)
		input.Support.SourceGroupKey = strings.TrimSpace(input.Support.SourceGroupKey)
		input.Support.SourceID = strings.TrimSpace(input.Support.SourceID)
		input.Support.SourceRevisionID = strings.TrimSpace(input.Support.SourceRevisionID)
		input.Support.Authority = strings.TrimSpace(input.Support.Authority)
		if input.Support.Authority == "" {
			input.Support.Authority = string(domain.AuthorityPrimary)
		}
	}
	return input
}

func validateApplyRelationshipDecisionInput(input ApplyRelationshipDecisionInput) error {
	for label, value := range map[string]string{
		"team_id":           input.TeamID,
		"owner_profile_id":  input.OwnerProfileID,
		"ingest_id":         input.IngestID,
		"subject_entity_id": input.SubjectEntityID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if input.PlacementItemID != "" {
		if _, err := uuid.Parse(input.PlacementItemID); err != nil {
			return fmt.Errorf("placement_item_id is invalid: %w", err)
		}
	}
	if input.PredicateKey == "" {
		return errors.New("predicate_key is required")
	}
	if input.PredicateVersion < 1 {
		return errors.New("predicate_version must be greater than zero")
	}
	if (input.ObjectEntityID == "") == (input.ObjectValueID == "") {
		return errors.New("exactly one object endpoint is required")
	}
	if input.ObjectEntityID != "" {
		if _, err := uuid.Parse(input.ObjectEntityID); err != nil {
			return fmt.Errorf("object_entity_id is invalid: %w", err)
		}
	}
	if input.ObjectValueID != "" {
		if _, err := uuid.Parse(input.ObjectValueID); err != nil {
			return fmt.Errorf("object_value_id is invalid: %w", err)
		}
	}
	if input.Polarity != "+" && input.Polarity != "-" {
		return fmt.Errorf("unsupported polarity %q", input.Polarity)
	}
	if !contains(domain.VerificationVerdicts(), input.EvidenceVerdict) {
		return fmt.Errorf("unsupported evidence_verdict %q", input.EvidenceVerdict)
	}
	if input.Confidence != nil && (*input.Confidence < 0 || *input.Confidence > 1) {
		return errors.New("confidence must be between 0 and 1")
	}
	if input.ValidFrom != nil && input.ValidTo != nil && input.ValidTo.Before(*input.ValidFrom) {
		return errors.New("valid_to must be greater than or equal to valid_from")
	}
	if input.Support != nil {
		if err := validateEvidenceSupportInput(*input.Support); err != nil {
			return err
		}
	}
	if input.EvidenceVerdict == string(domain.VerificationEntailed) && input.Support == nil {
		return errors.New("entailed relationship decisions require support")
	}
	return nil
}

func validateEvidenceSupportInput(input EvidenceSupportInput) error {
	if _, err := uuid.Parse(input.FragmentID); err != nil {
		return fmt.Errorf("support.fragment_id is required: %w", err)
	}
	if input.SourceID != "" {
		if _, err := uuid.Parse(input.SourceID); err != nil {
			return fmt.Errorf("support.source_id is invalid: %w", err)
		}
	}
	if input.SourceRevisionID != "" {
		if _, err := uuid.Parse(input.SourceRevisionID); err != nil {
			return fmt.Errorf("support.source_revision_id is invalid: %w", err)
		}
	}
	if (input.SourceID == "") != (input.SourceRevisionID == "") {
		return errors.New("support.source_id and source_revision_id must be provided together")
	}
	if input.SourceGroupKey == "" {
		return errors.New("support.source_group_key is required")
	}
	if input.SpanStart < 0 || input.SpanEnd <= input.SpanStart {
		return errors.New("support span is invalid")
	}
	if !domain.Authority(input.Authority).IsValid() {
		return fmt.Errorf("unsupported support authority %q", input.Authority)
	}
	return nil
}

func normalizeRetractRelationshipInput(input RetractRelationshipInput) RetractRelationshipInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.RelationshipID = strings.TrimSpace(input.RelationshipID)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" {
		input.Reason = "forget"
	}
	return input
}

func validateRetractRelationshipInput(input RetractRelationshipInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.RelationshipID); err != nil {
		return fmt.Errorf("relationship_id is required: %w", err)
	}
	return nil
}

func normalizeAppendCrossReferenceInput(input AppendCrossReferenceInput) AppendCrossReferenceInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.AuthorProfileID = strings.TrimSpace(input.AuthorProfileID)
	input.SourceRelationshipID = strings.TrimSpace(input.SourceRelationshipID)
	input.TargetRelationshipID = strings.TrimSpace(input.TargetRelationshipID)
	input.Kind = strings.TrimSpace(input.Kind)
	input.VerificationEventID = strings.TrimSpace(input.VerificationEventID)
	return input
}

func validateAppendCrossReferenceInput(input AppendCrossReferenceInput) error {
	for label, value := range map[string]string{
		"team_id":                input.TeamID,
		"author_profile_id":      input.AuthorProfileID,
		"source_relationship_id": input.SourceRelationshipID,
		"target_relationship_id": input.TargetRelationshipID,
		"verification_event_id":  input.VerificationEventID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if input.SourceRelationshipVersion < 1 || input.TargetRelationshipVersion < 1 {
		return errors.New("relationship versions must be greater than zero")
	}
	if !contains(domain.CrossReferenceKinds(), input.Kind) {
		return fmt.Errorf("unsupported cross reference kind %q", input.Kind)
	}
	return nil
}

func normalizeCreateHypothesisInput(input CreateHypothesisInput) CreateHypothesisInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.Status = strings.TrimSpace(input.Status)
	if input.Status == "" {
		input.Status = string(domain.HypothesisProposed)
	}
	return input
}

func validateCreateHypothesisInput(input CreateHypothesisInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if !contains(domain.HypothesisStatuses(), input.Status) {
		return fmt.Errorf("unsupported hypothesis status %q", input.Status)
	}
	return nil
}

func insertEntityName(ctx context.Context, tx *gorm.DB, input AddEntityNameInput) (string, error) {
	metadata, err := marshalJSON(input.Metadata)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO entity_names (
		    team_id, entity_id, owner_profile_id, display_name, normalized_name,
		    name_kind, locale, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?::jsonb
		)
		RETURNING entity_name_id::text
	`, input.TeamID, input.EntityID, input.OwnerProfileID, input.DisplayName,
		normalizeName(input.DisplayName), input.NameKind, input.Locale,
		string(metadata)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var nameID string
	if err := rows.Scan(&nameID); err != nil {
		return "", err
	}
	return nameID, rows.Err()
}

func loadPredicateDefinition(ctx context.Context, tx *gorm.DB, teamID string, predicateKey string, version int) (*predicateDefinition, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		       relationship_kind, current_cardinality
		FROM team_predicate_definitions
		WHERE team_id = ?::uuid
		  AND predicate_key = ?
		  AND version = ?
		  AND lifecycle_state = 'active'
	`, teamID, predicateKey, version).Rows()
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
	var loaded predicateDefinition
	var subjectKinds pq.StringArray
	var objectKinds pq.StringArray
	if err := rows.Scan(&loaded.Key, &loaded.Version, &subjectKinds, &objectKinds,
		&loaded.RelationshipKind, &loaded.CurrentCardinality); err != nil {
		return nil, err
	}
	loaded.AllowedSubjectKinds = []string(subjectKinds)
	loaded.AllowedObjectKinds = []string(objectKinds)
	return &loaded, rows.Err()
}

func validateRelationshipEndpointKinds(ctx context.Context, tx *gorm.DB, input ApplyRelationshipDecisionInput, predicate *predicateDefinition) error {
	subjectKind, err := loadEntityKind(ctx, tx, input.TeamID, input.SubjectEntityID)
	if err != nil {
		return err
	}
	if len(predicate.AllowedSubjectKinds) > 0 && !contains(predicate.AllowedSubjectKinds, subjectKind) {
		return fmt.Errorf("predicate %q does not allow subject kind %q", predicate.Key, subjectKind)
	}
	var objectKind string
	if input.ObjectEntityID != "" {
		objectKind, err = loadEntityKind(ctx, tx, input.TeamID, input.ObjectEntityID)
	} else {
		objectKind, err = loadValueType(ctx, tx, input.TeamID, input.ObjectValueID)
	}
	if err != nil {
		return err
	}
	if len(predicate.AllowedObjectKinds) > 0 && !contains(predicate.AllowedObjectKinds, objectKind) {
		return fmt.Errorf("predicate %q does not allow object kind %q", predicate.Key, objectKind)
	}
	return nil
}

func loadEntityKind(ctx context.Context, tx *gorm.DB, teamID, entityID string) (string, error) {
	var kind string
	row := tx.WithContext(ctx).Raw(`
		SELECT entity_kind
		FROM entity_records
		WHERE team_id = ?::uuid
		  AND entity_id = ?::uuid
	`, teamID, entityID).Row()
	if err := row.Scan(&kind); err != nil {
		return "", err
	}
	return kind, nil
}

func loadValueType(ctx context.Context, tx *gorm.DB, teamID, valueID string) (string, error) {
	var valueType string
	row := tx.WithContext(ctx).Raw(`
		SELECT value_type
		FROM value_records
		WHERE team_id = ?::uuid
		  AND value_id = ?::uuid
	`, teamID, valueID).Row()
	if err := row.Scan(&valueType); err != nil {
		return "", err
	}
	return valueType, nil
}

func insertPredicateReview(
	ctx context.Context,
	tx *gorm.DB,
	input ApplyRelationshipDecisionInput,
) (*RelationshipDecisionResult, error) {
	observationInput := input
	observationInput.PredicateKey = ""
	observationInput.PredicateVersion = 0
	observationID, err := insertRelationshipObservation(ctx, tx, observationInput, "")
	if err != nil {
		return nil, err
	}
	verificationID, err := insertVerificationEvent(ctx, tx, input, observationID)
	if err != nil {
		return nil, err
	}
	payload, err := marshalJSON(map[string]any{
		"original_predicate":       input.OriginalPredicate,
		"predicate_key":            input.PredicateKey,
		"predicate_policy_version": domain.PredicatePolicyVersion,
	})
	if err != nil {
		return nil, err
	}
	dedupeKey := ""
	if input.PlacementItemID != "" && input.ProposalRef != "" {
		dedupeKey = "relationship:" + input.PlacementItemID + ":" + input.ProposalRef + ":predicate_needs_review"
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO review_tasks (
		    team_id, owner_profile_id, ingest_id, placement_item_id,
		    observation_id, task_type, status, reason, payload, dedupe_key, updated_at
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, NULLIF(?, '')::uuid,
		    ?::uuid, 'predicate_needs_review', 'open', 'unknown_predicate', ?::jsonb, ?, now()
		)
		ON CONFLICT (team_id, dedupe_key)
		WHERE dedupe_key <> '' AND status IN ('open', 'acknowledged')
		DO UPDATE SET updated_at = now()
		RETURNING review_task_id::text
	`, input.TeamID, input.OwnerProfileID, input.IngestID, input.PlacementItemID,
		observationID, string(payload), dedupeKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var reviewTaskID string
	if err := rows.Scan(&reviewTaskID); err != nil {
		return nil, err
	}
	return &RelationshipDecisionResult{
		ObservationID:       observationID,
		VerificationEventID: verificationID,
		ReviewTaskID:        reviewTaskID,
		ProposalID:          input.ProposalRef,
		OwnerProfileID:      input.OwnerProfileID,
		Category:            string(domain.OutcomePredicateNeedsReview),
		Reason:              "unknown_predicate",
	}, rows.Err()
}

func insertRelationshipObservation(ctx context.Context, tx *gorm.DB, input ApplyRelationshipDecisionInput, relationshipID string) (string, error) {
	evidence := []map[string]any{}
	if input.Support != nil {
		evidence = append(evidence, map[string]any{
			"fragment_id": input.Support.FragmentID,
			"start":       input.Support.SpanStart,
			"end":         input.Support.SpanEnd,
		})
	}
	evidenceJSON, err := marshalJSONArray(evidence)
	if err != nil {
		return "", err
	}
	metadata, err := marshalJSON(input.ObservationMetadata)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO relationship_observations (
		    team_id, relationship_id, ingest_id, placement_item_id, owner_profile_id,
		    subject_ref, original_predicate, object_ref, subject_entity_id,
		    predicate_key, predicate_version, object_entity_id, object_value_id,
		    polarity, scope_key, valid_from, valid_to, evidence, metadata
		) VALUES (
		    ?::uuid, NULLIF(?, '')::uuid, ?::uuid, NULLIF(?, '')::uuid, ?::uuid,
		    ?, ?, ?, NULLIF(?, '')::uuid, NULLIF(?, ''), NULLIF(?, 0),
		    NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?, NULLIF(?, ''),
		    ?, ?, ?::jsonb, ?::jsonb
		)
		RETURNING observation_id::text
	`, input.TeamID, relationshipID, input.IngestID, input.PlacementItemID, input.OwnerProfileID,
		input.SubjectRef, input.OriginalPredicate, input.ObjectRef, input.SubjectEntityID,
		input.PredicateKey, input.PredicateVersion, input.ObjectEntityID, input.ObjectValueID,
		input.Polarity, input.ScopeKey, timeArg(input.ValidFrom), timeArg(input.ValidTo), string(evidenceJSON),
		string(metadata)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var observationID string
	if err := rows.Scan(&observationID); err != nil {
		return "", err
	}
	return observationID, rows.Err()
}

func insertVerificationEvent(ctx context.Context, tx *gorm.DB, input ApplyRelationshipDecisionInput, observationID string) (string, error) {
	metadata, err := marshalJSON(nil)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO verification_events (
		    team_id, observation_id, owner_profile_id, evidence_verdict,
		    confidence, rationale, model, response_hash, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?, ?, ?::jsonb
		)
		RETURNING verification_event_id::text
	`, input.TeamID, observationID, input.OwnerProfileID, input.EvidenceVerdict,
		confidenceArg(input.Confidence), input.Rationale, input.Model, input.ResponseHash,
		string(metadata)).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", rows.Err()
	}
	var verificationID string
	if err := rows.Scan(&verificationID); err != nil {
		return "", err
	}
	return verificationID, rows.Err()
}

func upsertRelationshipRecord(
	ctx context.Context,
	tx *gorm.DB,
	input ApplyRelationshipDecisionInput,
	predicate *predicateDefinition,
	tier string,
	status string,
	semanticGroupKey string,
) (*relationshipRecordState, error) {
	metadata, err := marshalJSON(input.RelationshipMetadata)
	if err != nil {
		return nil, err
	}
	existing, err := selectRelationshipByIdentity(ctx, tx, input)
	if err == nil && !nullableTimesEqual(existing.ValidTo, input.ValidTo) {
		return &relationshipRecordState{
			Record:          existing,
			ValidToConflict: true,
		}, nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	if predicate.CurrentCardinality == string(domain.CurrentCardinalityOne) &&
		status == string(domain.RelationshipStatusActive) {
		keepRelationshipID := ""
		if existing != nil {
			keepRelationshipID = existing.RelationshipID
		}
		if err := supersedeOneCardinalityRelationships(ctx, tx, input, keepRelationshipID); err != nil {
			return nil, err
		}
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO relationship_records (
		    team_id, owner_profile_id, semantic_group_key, subject_entity_id,
		    predicate_key, predicate_version, object_entity_id, object_value_id,
		    relationship_kind, current_cardinality, tier, status, polarity,
		    scope_key, valid_from, valid_to, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?, ?::uuid, ?, ?, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid,
		    ?, ?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?::jsonb
		)
		ON CONFLICT (
		    team_id, owner_profile_id, subject_entity_id, predicate_key,
		    object_entity_id, object_value_id, polarity, valid_from, scope_key
		)
		WHERE identity_alias_of_relationship_id IS NULL
		DO NOTHING
		RETURNING team_id::text, relationship_id::text, owner_profile_id::text,
		          semantic_group_key, subject_entity_id::text, predicate_key,
		          predicate_version, COALESCE(object_entity_id::text, ''),
		          COALESCE(object_value_id::text, ''), relationship_kind,
		          current_cardinality, tier, status, polarity, COALESCE(scope_key, ''),
		          valid_from, valid_to,
		          COALESCE(identity_alias_of_relationship_id::text, ''),
		          support_count, source_group_count, version
	`, input.TeamID, input.OwnerProfileID, semanticGroupKey, input.SubjectEntityID,
		input.PredicateKey, input.PredicateVersion, input.ObjectEntityID, input.ObjectValueID,
		predicate.RelationshipKind, predicate.CurrentCardinality, tier, status, input.Polarity,
		input.ScopeKey, timeArg(input.ValidFrom), timeArg(input.ValidTo), string(metadata)).Rows()
	if err != nil {
		return nil, err
	}
	inserted, scanErr := scanRelationshipRows(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return nil, scanErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if inserted != nil {
		return &relationshipRecordState{Record: inserted, Created: true, Changed: true}, nil
	}
	existing, err = selectRelationshipByIdentity(ctx, tx, input)
	if err != nil {
		return nil, err
	}
	if !nullableTimesEqual(existing.ValidTo, input.ValidTo) {
		return &relationshipRecordState{
			Record:          existing,
			ValidToConflict: true,
		}, nil
	}
	state := &relationshipRecordState{Record: existing}
	if existing.PredicateVersion != input.PredicateVersion ||
		existing.Tier != tier ||
		existing.Status != status ||
		existing.RelationshipKind != predicate.RelationshipKind ||
		existing.CurrentCardinality != predicate.CurrentCardinality ||
		existing.SemanticGroupKey != semanticGroupKey {
		state.Changed = true
		state.FromTier = existing.Tier
		state.FromStatus = existing.Status
		result := tx.WithContext(ctx).Exec(`
			UPDATE relationship_records
			SET predicate_version = ?,
			    tier = ?,
			    status = ?,
			    recorded_to = CASE WHEN ? = 'active' THEN NULL ELSE recorded_to END,
			    relationship_kind = ?,
			    current_cardinality = ?,
			    semantic_group_key = ?,
			    version = version + 1,
			    updated_at = now()
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
			  AND owner_profile_id = ?::uuid
		`, input.PredicateVersion, tier, status, status, predicate.RelationshipKind, predicate.CurrentCardinality,
			semanticGroupKey, input.TeamID, existing.RelationshipID, input.OwnerProfileID)
		if result.Error != nil {
			return nil, result.Error
		}
		if result.RowsAffected == 0 {
			return nil, ErrSemanticOwnerMismatch
		}
		updated, err := loadRelationshipRecord(ctx, tx, input.TeamID, existing.RelationshipID)
		if err != nil {
			return nil, err
		}
		state.Record = updated
	}
	return state, nil
}

func selectRelationshipByIdentity(ctx context.Context, tx *gorm.DB, input ApplyRelationshipDecisionInput) (*RelationshipRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, relationship_id::text, owner_profile_id::text,
		       semantic_group_key, subject_entity_id::text, predicate_key,
		       predicate_version, COALESCE(object_entity_id::text, ''),
		       COALESCE(object_value_id::text, ''), relationship_kind,
		       current_cardinality, tier, status, polarity, COALESCE(scope_key, ''),
		       valid_from, valid_to,
		       COALESCE(identity_alias_of_relationship_id::text, ''),
		       support_count, source_group_count, version
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND subject_entity_id = ?::uuid
		  AND predicate_key = ?
		  AND object_entity_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND object_value_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND polarity = ?
		  AND valid_from IS NOT DISTINCT FROM ?
		  AND scope_key IS NOT DISTINCT FROM NULLIF(?, '')
		  AND identity_alias_of_relationship_id IS NULL
		FOR UPDATE
	`, input.TeamID, input.OwnerProfileID, input.SubjectEntityID, input.PredicateKey,
		input.ObjectEntityID, input.ObjectValueID, input.Polarity, timeArg(input.ValidFrom),
		input.ScopeKey).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	record, err := scanRelationshipRows(rows)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return record, rows.Err()
}

func loadRelationshipRecord(ctx context.Context, tx *gorm.DB, teamID, relationshipID string) (*RelationshipRecord, error) {
	return loadRelationshipRecordWithLock(ctx, tx, teamID, relationshipID, false)
}

func loadRelationshipRecordForUpdate(ctx context.Context, tx *gorm.DB, teamID, relationshipID string) (*RelationshipRecord, error) {
	return loadRelationshipRecordWithLock(ctx, tx, teamID, relationshipID, true)
}

func loadRelationshipRecordWithLock(ctx context.Context, tx *gorm.DB, teamID, relationshipID string, lock bool) (*RelationshipRecord, error) {
	lockClause := ""
	if lock {
		lockClause = "FOR UPDATE"
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, relationship_id::text, owner_profile_id::text,
		       semantic_group_key, subject_entity_id::text, predicate_key,
		       predicate_version, COALESCE(object_entity_id::text, ''),
		       COALESCE(object_value_id::text, ''), relationship_kind,
		       current_cardinality, tier, status, polarity, COALESCE(scope_key, ''),
		       valid_from, valid_to,
		       COALESCE(identity_alias_of_relationship_id::text, ''),
		       support_count, source_group_count, version
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		`+lockClause+`
	`, teamID, relationshipID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	record, err := scanRelationshipRows(rows)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, gorm.ErrRecordNotFound
	}
	return record, rows.Err()
}

func scanRelationshipRows(rows *sql.Rows) (*RelationshipRecord, error) {
	if !rows.Next() {
		return nil, rows.Err()
	}
	loaded := RelationshipRecord{}
	if err := rows.Scan(&loaded.TeamID, &loaded.RelationshipID, &loaded.OwnerProfileID,
		&loaded.SemanticGroupKey, &loaded.SubjectEntityID, &loaded.PredicateKey,
		&loaded.PredicateVersion, &loaded.ObjectEntityID, &loaded.ObjectValueID,
		&loaded.RelationshipKind, &loaded.CurrentCardinality, &loaded.Tier,
		&loaded.Status, &loaded.Polarity, &loaded.ScopeKey, &loaded.ValidFrom,
		&loaded.ValidTo, &loaded.IdentityAliasOfID, &loaded.SupportCount,
		&loaded.SourceGroupCount, &loaded.Version); err != nil {
		return nil, err
	}
	return &loaded, nil
}

func insertRelationshipTransition(ctx context.Context, tx *gorm.DB, input transitionInput) (string, error) {
	var transitionID string
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO relationship_transition_events (
		    team_id, relationship_id, owner_profile_id, from_tier, from_status,
		    to_tier, to_status, reason, verification_event_id, support_decision_id,
		    idempotency_key
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, NULLIF(?, ''), NULLIF(?, ''),
		    ?, ?, ?, NULLIF(?, '')::uuid, NULLIF(?, '')::uuid, ?
		)
		ON CONFLICT (team_id, owner_profile_id, idempotency_key)
		WHERE idempotency_key <> ''
		DO NOTHING
		RETURNING transition_id::text
	`, input.TeamID, input.RelationshipID, input.OwnerProfileID, input.FromTier,
		input.FromStatus, input.ToTier, input.ToStatus, input.Reason,
		input.VerificationEventID, input.SupportDecisionID, input.IdempotencyKey).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&transitionID); err != nil {
			return "", err
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if transitionID == "" {
		transitionID, err = loadRelationshipTransitionIDByIdempotency(ctx, tx, input)
		if err != nil {
			return "", err
		}
	}
	if transitionID == "" {
		return "", gorm.ErrRecordNotFound
	}
	return transitionID, nil
}

func loadRelationshipTransitionIDByIdempotency(ctx context.Context, tx *gorm.DB, input transitionInput) (string, error) {
	if strings.TrimSpace(input.IdempotencyKey) == "" {
		return "", nil
	}
	var transitionID string
	err := tx.WithContext(ctx).Raw(`
		SELECT transition_id::text
		FROM relationship_transition_events
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND idempotency_key = ?
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, input.IdempotencyKey).Scan(&transitionID).Error
	return transitionID, err
}

func relationshipTransitionIdempotencyKey(verificationEventID string, supportDecisionID string) string {
	if id := strings.TrimSpace(verificationEventID); id != "" {
		return "verification:" + id + ":relationship_transition"
	}
	if id := strings.TrimSpace(supportDecisionID); id != "" {
		return "support_decision:" + id + ":relationship_transition"
	}
	return ""
}

func tierStatusForVerdict(verdict string, promoteToFact bool) (string, string) {
	switch verdict {
	case string(domain.VerificationContradicted):
		return string(domain.RelationshipTierCandidate), string(domain.RelationshipStatusRejected)
	case string(domain.VerificationInsufficient):
		return string(domain.RelationshipTierCandidate), string(domain.RelationshipStatusPendingEvidence)
	default:
		if promoteToFact {
			return string(domain.RelationshipTierFact), string(domain.RelationshipStatusActive)
		}
		return string(domain.RelationshipTierValidatedClaim), string(domain.RelationshipStatusActive)
	}
}

func semanticGroupKey(input ApplyRelationshipDecisionInput) string {
	objectID := input.ObjectEntityID
	if objectID == "" {
		objectID = "value:" + input.ObjectValueID
	} else {
		objectID = "entity:" + objectID
	}
	parts := []string{
		input.SubjectEntityID,
		input.PredicateKey,
		objectID,
		input.Polarity,
		input.ScopeKey,
		timeKey(input.ValidFrom),
		"",
	}
	return "sg:" + strings.TrimPrefix(sha256Hex(strings.Join(parts, "\x00")), "sha256:")
}

func nullableTimesEqual(left *time.Time, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func timeKey(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func normalizeName(value string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(value))), " ")
}

func marshalJSONArray(value []map[string]any) ([]byte, error) {
	if value == nil {
		value = []map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal json array: %w", err)
	}
	return data, nil
}

func timeArg(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func confidenceArg(value *float64) any {
	if value == nil {
		return nil
	}
	return sql.NullFloat64{Float64: *value, Valid: true}
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
