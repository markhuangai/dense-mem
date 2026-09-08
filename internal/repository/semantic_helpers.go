package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

type predicateDefinition struct {
	Key                 string
	Version             int
	AllowedSubjectKinds []string
	AllowedObjectKinds  []string
	RelationshipKind    string
	CurrentCardinality  string
}

type transitionInput struct {
	TeamID              string
	OwnerProfileID      string
	RelationshipID      string
	SpaceID             string
	IdempotencyKey      string
	FromStatus          string
	ToStatus            string
	Reason              string
	VerificationEventID string
	SupportDecisionID   string
}

func statusForVerdict(verdict string) string {
	switch verdict {
	case string(domain.VerificationContradicted):
		return string(domain.RelationshipStatusRejected)
	case string(domain.VerificationInsufficient):
		return string(domain.RelationshipStatusPendingEvidence)
	default:
		return string(domain.RelationshipStatusActive)
	}
}

func statusForRelationshipDecision(input ApplyRelationshipDecisionInput) string {
	if input.AssessorAccepted {
		return string(domain.RelationshipStatusActive)
	}
	if input.SuppressSupport && input.EvidenceVerdict == string(domain.VerificationEntailed) {
		return string(domain.RelationshipStatusPendingEvidence)
	}
	return statusForVerdict(input.EvidenceVerdict)
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
	input.AssessmentID = strings.TrimSpace(input.AssessmentID)
	input.AssessmentPolicyVersion = strings.TrimSpace(input.AssessmentPolicyVersion)
	input.GateResult = strings.TrimSpace(input.GateResult)
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
	if input.EvidenceVerdict == "" && !input.AssessorAccepted {
		input.EvidenceVerdict = string(domain.VerificationEntailed)
	}
	input.Support, input.Supports = normalizeEvidenceSupports(input.Support, input.Supports)
	return input
}

func normalizeEvidenceSupports(primary *EvidenceSupportInput, additional []EvidenceSupportInput) (*EvidenceSupportInput, []EvidenceSupportInput) {
	if primary != nil {
		normalized := normalizeEvidenceSupport(*primary)
		primary = &normalized
	}
	if additional == nil {
		return primary, nil
	}
	normalized := make([]EvidenceSupportInput, len(additional))
	for index, support := range additional {
		normalized[index] = normalizeEvidenceSupport(support)
	}
	return primary, normalized
}

func normalizeEvidenceSupport(input EvidenceSupportInput) EvidenceSupportInput {
	input.FragmentID = strings.TrimSpace(input.FragmentID)
	input.EvidenceOwnerProfileID = strings.TrimSpace(input.EvidenceOwnerProfileID)
	input.OccurrenceOwnerProfileID = strings.TrimSpace(input.OccurrenceOwnerProfileID)
	input.OccurrenceID = strings.TrimSpace(input.OccurrenceID)
	input.SourceGroupKey = strings.TrimSpace(input.SourceGroupKey)
	input.SourceID = strings.TrimSpace(input.SourceID)
	input.SourceRevisionID = strings.TrimSpace(input.SourceRevisionID)
	input.Quote = strings.TrimSpace(input.Quote)
	input.Authority = strings.TrimSpace(input.Authority)
	if input.Authority == "" {
		input.Authority = string(domain.AuthorityPrimary)
	}
	return input
}

func relationshipEvidenceSupports(primary *EvidenceSupportInput, additional []EvidenceSupportInput) []EvidenceSupportInput {
	result := make([]EvidenceSupportInput, 0, len(additional)+1)
	if primary != nil {
		result = append(result, *primary)
	}
	return append(result, additional...)
}

func validateRelationshipEvidenceSupports(primary *EvidenceSupportInput, additional []EvidenceSupportInput) error {
	seen := make(map[string]struct{}, len(additional)+1)
	for _, support := range relationshipEvidenceSupports(primary, additional) {
		if err := validateEvidenceSupportInput(support); err != nil {
			return err
		}
		key := support.FragmentID + "\x00" + strconv.Itoa(support.SpanStart) + "\x00" + strconv.Itoa(support.SpanEnd)
		if _, exists := seen[key]; exists {
			return errors.New("relationship evidence supports must not duplicate a fragment span")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateApplyRelationshipDecisionInput(input ApplyRelationshipDecisionInput) error {
	for label, value := range map[string]string{
		"team_id": input.TeamID, "owner_profile_id": input.OwnerProfileID,
		"ingest_id": input.IngestID, "subject_entity_id": input.SubjectEntityID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
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
	if input.AssessorAccepted {
		if _, err := uuid.Parse(input.AssessmentID); err != nil {
			return fmt.Errorf("assessor accepted relationship assessment_id is required: %w", err)
		}
		if input.SuppressSupport {
			return errors.New("assessor accepted relationship cannot carry legacy policy fields")
		}
	} else {
		if !contains(domain.VerificationVerdicts(), input.EvidenceVerdict) {
			return fmt.Errorf("unsupported evidence_verdict %q", input.EvidenceVerdict)
		}
		if input.Confidence != nil && (*input.Confidence < 0 || *input.Confidence > 1) {
			return errors.New("confidence must be between 0 and 1")
		}
		if err := knowledgecontract.ValidateAssessmentDecisionAudit(input.AssessmentID, input.AssessmentPolicyVersion, input.ThresholdUsed, input.GateResult, input.SuppressSupport); err != nil {
			return err
		}
	}
	if input.ValidFrom != nil && input.ValidTo != nil && input.ValidTo.Before(*input.ValidFrom) {
		return errors.New("valid_to must be greater than or equal to valid_from")
	}
	if err := validateRelationshipEvidenceSupports(input.Support, input.Supports); err != nil {
		return err
	}
	if len(relationshipEvidenceSupports(input.Support, input.Supports)) == 0 {
		if input.AssessorAccepted {
			return errors.New("accepted relationship decisions require support")
		}
		if input.EvidenceVerdict == string(domain.VerificationEntailed) {
			return errors.New("entailed relationship decisions require support")
		}
	}
	if input.SuppressSupport && input.EvidenceVerdict != string(domain.VerificationEntailed) {
		return errors.New("support suppression requires an entailed relationship decision")
	}
	return nil
}

func validateEvidenceSupportInput(input EvidenceSupportInput) error {
	if _, err := uuid.Parse(input.FragmentID); err != nil {
		return fmt.Errorf("support.fragment_id is required: %w", err)
	}
	if input.EvidenceOwnerProfileID != "" {
		if _, err := uuid.Parse(input.EvidenceOwnerProfileID); err != nil {
			return fmt.Errorf("support.evidence_owner_profile_id is invalid: %w", err)
		}
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
	for label, value := range map[string]string{"team_id": input.TeamID, "owner_profile_id": input.OwnerProfileID, "relationship_id": input.RelationshipID} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
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
		"team_id": input.TeamID, "author_profile_id": input.AuthorProfileID,
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

// Dream retains ownership of hypothesis creation until its capability cutover.
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

// loadPredicateDefinition and loadRelationshipRecord are read helpers retained
// by Dream and search-reconciliation adapters; canonical writes use Store.
func loadPredicateDefinition(ctx context.Context, tx *gorm.DB, teamID, predicateKey string, version int) (*predicateDefinition, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		       relationship_kind, current_cardinality
		FROM team_predicate_definitions
		WHERE team_id = ?::uuid AND predicate_key = ? AND version = ? AND lifecycle_state = 'active'
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
	var subjectKinds, objectKinds pq.StringArray
	if err := rows.Scan(&loaded.Key, &loaded.Version, &subjectKinds, &objectKinds, &loaded.RelationshipKind, &loaded.CurrentCardinality); err != nil {
		return nil, err
	}
	loaded.AllowedSubjectKinds = []string(subjectKinds)
	loaded.AllowedObjectKinds = []string(objectKinds)
	return &loaded, rows.Err()
}

func loadRelationshipRecord(ctx context.Context, tx *gorm.DB, teamID, relationshipID string) (*RelationshipRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, relationship_id::text, owner_profile_id::text, space_id::text,
		       space_generation, semantic_group_key, subject_entity_id::text, predicate_key,
		       predicate_version, COALESCE(object_entity_id::text, ''), COALESCE(object_value_id::text, ''),
		       relationship_kind, current_cardinality, status, polarity, COALESCE(scope_key, ''),
		       valid_from, valid_to, COALESCE(identity_alias_of_relationship_id::text, ''),
		       support_count, source_group_count, version
		FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid
	`, teamID, relationshipID).Rows()
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
	loaded := RelationshipRecord{}
	if err := rows.Scan(&loaded.TeamID, &loaded.RelationshipID, &loaded.OwnerProfileID, &loaded.SpaceID, &loaded.SpaceGeneration,
		&loaded.SemanticGroupKey, &loaded.SubjectEntityID, &loaded.PredicateKey, &loaded.PredicateVersion,
		&loaded.ObjectEntityID, &loaded.ObjectValueID, &loaded.RelationshipKind, &loaded.CurrentCardinality,
		&loaded.Status, &loaded.Polarity, &loaded.ScopeKey, &loaded.ValidFrom, &loaded.ValidTo,
		&loaded.IdentityAliasOfID, &loaded.SupportCount, &loaded.SourceGroupCount, &loaded.Version); err != nil {
		return nil, err
	}
	return &loaded, rows.Err()
}

func insertRelationshipTransition(ctx context.Context, tx *gorm.DB, input transitionInput) (string, error) {
	return knowledgepostgres.InsertRelationshipTransitionTx(ctx, knowledgepostgres.LegacyTransaction(tx),
		input.TeamID, input.OwnerProfileID, input.RelationshipID, input.SpaceID,
		input.FromStatus, input.ToStatus, input.Reason, input.VerificationEventID,
		input.SupportDecisionID, input.IdempotencyKey)
}

func relationshipTransitionIdempotencyKey(verificationEventID, supportDecisionID string) string {
	if id := strings.TrimSpace(verificationEventID); id != "" {
		return "verification:" + id + ":relationship_transition"
	}
	if id := strings.TrimSpace(supportDecisionID); id != "" {
		return "support_decision:" + id + ":relationship_transition"
	}
	return ""
}

func semanticGroupKey(input ApplyRelationshipDecisionInput) string {
	objectID := input.ObjectEntityID
	if objectID == "" {
		objectID = "value:" + input.ObjectValueID
	} else {
		objectID = "entity:" + objectID
	}
	parts := []string{input.SubjectEntityID, input.PredicateKey, objectID, input.Polarity, input.ScopeKey, timeKey(input.ValidFrom), ""}
	return "sg:" + strings.TrimPrefix(sha256Hex(strings.Join(parts, "\x00")), "sha256:")
}

func nullableTimesEqual(left, right *time.Time) bool {
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
