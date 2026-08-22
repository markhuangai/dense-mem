package memoryservice

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type submissionAssessmentItem struct {
	PlacementItem repository.PlacementItem
	Fragment      repository.EvidenceFragment
	EvidenceID    string
}

type submissionAssessmentEntityTarget struct {
	Target        verifier.SemanticAssessmentRequiredEntityRef
	KnownEntityID string
}

type submissionAssessmentRelationshipTarget struct {
	Target            verifier.SemanticAssessmentRequiredRelationshipRef
	ProposedPredicate string
	SubjectKind       string
	ObjectKind        string
	CorrectionTarget  *repository.PlacementCorrectionTargetInput
	ConflictContext   *repository.PlacementConflictContextInput
}

type submissionAssessmentPlan struct {
	Items               []submissionAssessmentItem
	EntityTargets       []submissionAssessmentEntityTarget
	RelationshipTargets []submissionAssessmentRelationshipTarget
	itemsByEvidenceID   map[string]submissionAssessmentItem
	entityTargetsByRef  map[string]submissionAssessmentEntityTarget
	relationshipsByRef  map[string]submissionAssessmentRelationshipTarget
}

func buildSubmissionAssessmentPlan(placement *repository.CreateIngestResult) (submissionAssessmentPlan, error) {
	if placement == nil {
		return submissionAssessmentPlan{}, errors.New("submission assessment placement is required")
	}
	fragmentsByIndex := make(map[int]repository.EvidenceFragment, len(placement.Evidence))
	for _, fragment := range placement.Evidence {
		if strings.TrimSpace(fragment.FragmentID) == "" || strings.TrimSpace(fragment.Content) == "" {
			return submissionAssessmentPlan{}, errors.New("submission assessment evidence is incomplete")
		}
		if _, exists := fragmentsByIndex[fragment.EvidenceIndex]; exists {
			return submissionAssessmentPlan{}, errors.New("submission assessment evidence index is duplicated")
		}
		fragmentsByIndex[fragment.EvidenceIndex] = fragment
	}
	if len(fragmentsByIndex) == 0 {
		return submissionAssessmentPlan{}, errors.New("submission assessment evidence is required")
	}

	items := make([]submissionAssessmentItem, 0, len(placement.Items))
	itemsByEvidenceID := make(map[string]submissionAssessmentItem, len(placement.Items))
	for _, item := range placement.Items {
		if item.Status != string(domain.PlacementRunQueued) && item.Status != string(domain.PlacementRunProcessing) {
			return submissionAssessmentPlan{}, errors.New("submission assessment placement item is not claimable")
		}
		fragment, ok := fragmentsByIndex[item.EvidenceIndex]
		if !ok || item.FragmentID != fragment.FragmentID || strings.TrimSpace(item.PlacementItemID) == "" {
			return submissionAssessmentPlan{}, errors.New("submission assessment placement item does not match evidence")
		}
		evidenceID := submissionAssessmentEvidenceID(item.EvidenceIndex)
		if _, exists := itemsByEvidenceID[evidenceID]; exists {
			return submissionAssessmentPlan{}, errors.New("submission assessment placement item evidence index is duplicated")
		}
		entry := submissionAssessmentItem{PlacementItem: item, Fragment: fragment, EvidenceID: evidenceID}
		items = append(items, entry)
		itemsByEvidenceID[evidenceID] = entry
	}
	if len(items) != len(fragmentsByIndex) {
		return submissionAssessmentPlan{}, errors.New("submission assessment must include every staged evidence item")
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].PlacementItem.EvidenceIndex < items[j].PlacementItem.EvidenceIndex
	})

	plan := submissionAssessmentPlan{
		Items:              items,
		itemsByEvidenceID:  itemsByEvidenceID,
		entityTargetsByRef: map[string]submissionAssessmentEntityTarget{},
		relationshipsByRef: map[string]submissionAssessmentRelationshipTarget{},
	}
	rawRelationships, err := submissionAssessmentObjectArray(placement.Proposal, "relationship_hints", "relationships")
	if err != nil {
		return submissionAssessmentPlan{}, err
	}
	if len(rawRelationships) == 0 || len(rawRelationships) > verifier.SemanticAssessmentMaxRelationshipResults {
		return submissionAssessmentPlan{}, errors.New("submission assessment relationships must be present and bounded")
	}
	coveredEvidence := make(map[string]struct{}, len(items))
	for index, raw := range rawRelationships {
		target, entities, err := submissionAssessmentRelationshipTargetFromProposal(raw, index, plan.itemsByEvidenceID)
		if err != nil {
			return submissionAssessmentPlan{}, err
		}
		if _, exists := plan.relationshipsByRef[target.Target.ProposalID]; exists {
			return submissionAssessmentPlan{}, errors.New("submission assessment relationship ref is duplicated")
		}
		for _, evidenceID := range target.Target.EvidenceIDs {
			coveredEvidence[evidenceID] = struct{}{}
		}
		for _, entity := range entities {
			if _, exists := plan.entityTargetsByRef[entity.Target.Ref]; exists {
				return submissionAssessmentPlan{}, errors.New("submission assessment entity ref is duplicated")
			}
			plan.EntityTargets = append(plan.EntityTargets, entity)
			plan.entityTargetsByRef[entity.Target.Ref] = entity
		}
		plan.RelationshipTargets = append(plan.RelationshipTargets, target)
		plan.relationshipsByRef[target.Target.ProposalID] = target
	}
	if len(plan.EntityTargets) == 0 || len(plan.EntityTargets) > verifier.SemanticAssessmentMaxEntityResults {
		return submissionAssessmentPlan{}, errors.New("submission assessment entity targets must be present and bounded")
	}
	if len(coveredEvidence) != len(items) {
		return submissionAssessmentPlan{}, errors.New("submission assessment relationships must cover every staged evidence item")
	}
	sort.Slice(plan.EntityTargets, func(i, j int) bool {
		return plan.EntityTargets[i].Target.Ref < plan.EntityTargets[j].Target.Ref
	})
	sort.Slice(plan.RelationshipTargets, func(i, j int) bool {
		return plan.RelationshipTargets[i].Target.ProposalID < plan.RelationshipTargets[j].Target.ProposalID
	})
	return plan, nil
}

func submissionAssessmentEvidenceID(index int) string {
	return fmt.Sprintf("evidence:%d", index)
}

func submissionAssessmentObjectArray(raw map[string]any, keys ...string) ([]map[string]any, error) {
	for _, key := range keys {
		value, exists := raw[key]
		if !exists {
			continue
		}
		switch typed := value.(type) {
		case []map[string]any:
			return append([]map[string]any(nil), typed...), nil
		case []any:
			out := make([]map[string]any, 0, len(typed))
			for _, item := range typed {
				fields, ok := proposalMap(item)
				if !ok {
					return nil, errors.New("submission assessment array contains a non-object")
				}
				out = append(out, fields)
			}
			return out, nil
		default:
			return nil, errors.New("submission assessment array is invalid")
		}
	}
	return nil, nil
}

func submissionAssessmentRelationshipTargetFromProposal(
	raw map[string]any,
	index int,
	itemsByEvidenceID map[string]submissionAssessmentItem,
) (submissionAssessmentRelationshipTarget, []submissionAssessmentEntityTarget, error) {
	ref := strings.TrimSpace(submissionAssessmentRawString(raw, "ref"))
	if ref == "" || len([]rune(ref)) > 128 {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship ref is required and bounded")
	}
	evidenceIDs, err := submissionAssessmentEvidenceIDsFromProposal(raw, itemsByEvidenceID)
	if err != nil {
		return submissionAssessmentRelationshipTarget{}, nil, err
	}

	subjectRaw, ok := proposalMap(raw["subject"])
	if !ok {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship subject is required")
	}
	subject, err := submissionAssessmentEntityTargetFromProposal(subjectRaw, fmt.Sprintf("entity:%d:subject", index), evidenceIDs)
	if err != nil {
		return submissionAssessmentRelationshipTarget{}, nil, err
	}

	predicateRaw, ok := proposalMap(raw["predicate"])
	if !ok {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship predicate is required")
	}
	proposedPredicate := strings.TrimSpace(submissionAssessmentRawString(predicateRaw, "proposed_key"))
	if proposedPredicate == "" || len([]rune(proposedPredicate)) > 128 {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment proposed predicate is required and bounded")
	}

	objectRaw, ok := proposalMap(raw["object"])
	if !ok {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship object is required")
	}
	objectEntityRaw, hasEntity := proposalMap(objectRaw["entity"])
	objectValueRaw, hasValue := proposalMap(objectRaw["value"])
	if hasEntity == hasValue {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship requires exactly one object endpoint")
	}
	entities := []submissionAssessmentEntityTarget{subject}
	objectKind := ""
	var objectRef *string
	var objectValue *verifier.SemanticAssessmentValue
	if hasEntity {
		objectEntity, err := submissionAssessmentEntityTargetFromProposal(objectEntityRaw, fmt.Sprintf("entity:%d:object", index), evidenceIDs)
		if err != nil {
			return submissionAssessmentRelationshipTarget{}, nil, err
		}
		entities = append(entities, objectEntity)
		objectKind = objectEntity.Target.Kind
		refCopy := objectEntity.Target.Ref
		objectRef = &refCopy
	} else {
		value, err := submissionAssessmentValueFromProposal(objectValueRaw)
		if err != nil {
			return submissionAssessmentRelationshipTarget{}, nil, err
		}
		objectKind = value.ValueType
		objectValue = value
	}

	polarity := strings.TrimSpace(submissionAssessmentRawString(raw, "polarity"))
	if polarity != "+" && polarity != "-" {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship polarity is unsupported")
	}
	modality := strings.TrimSpace(submissionAssessmentRawString(raw, "modality"))
	if !submissionAssessmentOneOf(modality, "statement", "question", "proposal", "speculation", "quoted") {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship modality is unsupported")
	}
	validFrom, err := proposalOptionalTime(raw, "valid_from")
	if err != nil {
		return submissionAssessmentRelationshipTarget{}, nil, err
	}
	validTo, err := proposalOptionalTime(raw, "valid_to")
	if err != nil {
		return submissionAssessmentRelationshipTarget{}, nil, err
	}
	if validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship valid_to must not be before valid_from")
	}
	target := verifier.SemanticAssessmentRequiredRelationshipRef{
		ProposalID:    ref,
		PredicateHint: proposedPredicate,
		EvidenceIDs:   append([]string(nil), evidenceIDs...),
		SubjectRef:    subject.Target.Ref,
		ObjectRef:     objectRef,
		ObjectValue:   objectValue,
		Polarity:      polarity,
		Modality:      modality,
		ValidFrom:     submissionAssessmentTimeString(validFrom),
		ValidTo:       submissionAssessmentTimeString(validTo),
	}
	entry := submissionAssessmentRelationshipTarget{
		Target:            target,
		ProposedPredicate: proposedPredicate,
		SubjectKind:       subject.Target.Kind,
		ObjectKind:        objectKind,
	}
	if _, exists := raw["correction_target"]; exists {
		correction, ok := placementProposalCorrectionTarget(raw)
		if !ok {
			return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment correction target is invalid")
		}
		entry.CorrectionTarget = &repository.PlacementCorrectionTargetInput{
			RelationshipID:  correction.RelationshipID,
			ExpectedVersion: correction.ExpectedVersion,
		}
	}
	if _, exists := raw["conflict_context"]; exists {
		conflict, ok := placementProposalConflictContext(raw)
		if !ok {
			return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment conflict context is invalid")
		}
		entry.ConflictContext = &repository.PlacementConflictContextInput{
			ConflictID:      conflict.ConflictID,
			ExpectedVersion: conflict.ExpectedVersion,
		}
	}
	return entry, entities, nil
}

func submissionAssessmentTimeString(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

func submissionAssessmentEvidenceIDsFromProposal(
	raw map[string]any,
	itemsByEvidenceID map[string]submissionAssessmentItem,
) ([]string, error) {
	values := rememberArrayValues(raw["evidence_indices"])
	if len(values) == 0 || len(values) > verifier.SemanticAssessmentMaxEvidenceSpans {
		return nil, errors.New("submission assessment relationship evidence_indices are required and bounded")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		index, ok := rememberEvidenceIndex(value)
		if !ok {
			return nil, errors.New("submission assessment relationship evidence index is invalid")
		}
		evidenceID := submissionAssessmentEvidenceID(index)
		if _, exists := itemsByEvidenceID[evidenceID]; !exists {
			return nil, errors.New("submission assessment relationship evidence is outside the run")
		}
		if _, exists := seen[evidenceID]; exists {
			return nil, errors.New("submission assessment relationship evidence index is duplicated")
		}
		seen[evidenceID] = struct{}{}
		result = append(result, evidenceID)
	}
	sort.Strings(result)
	return result, nil
}

func submissionAssessmentEntityTargetFromProposal(
	raw map[string]any,
	ref string,
	evidenceIDs []string,
) (submissionAssessmentEntityTarget, error) {
	name := strings.TrimSpace(submissionAssessmentRawString(raw, "name"))
	if name == "" || len([]rune(name)) > 256 {
		return submissionAssessmentEntityTarget{}, errors.New("submission assessment entity name is required and bounded")
	}
	kind := strings.TrimSpace(submissionAssessmentRawString(raw, "entity_kind"))
	if !submissionAssessmentContains(domain.EntityKinds(), kind) {
		return submissionAssessmentEntityTarget{}, errors.New("submission assessment entity kind is unsupported")
	}
	knownEntityID := strings.TrimSpace(submissionAssessmentRawString(raw, "known_entity_id"))
	if knownEntityID != "" {
		if _, err := uuid.Parse(knownEntityID); err != nil {
			return submissionAssessmentEntityTarget{}, errors.New("submission assessment known entity id is invalid")
		}
	}
	return submissionAssessmentEntityTarget{
		Target: verifier.SemanticAssessmentRequiredEntityRef{
			Ref:         ref,
			Name:        name,
			Kind:        kind,
			EvidenceIDs: append([]string(nil), evidenceIDs...),
		},
		KnownEntityID: knownEntityID,
	}, nil
}

func submissionAssessmentValueFromProposal(raw map[string]any) (*verifier.SemanticAssessmentValue, error) {
	valueType := strings.TrimSpace(submissionAssessmentRawString(raw, "type"))
	if !submissionAssessmentContains(domain.ValueTypes(), valueType) {
		return nil, errors.New("submission assessment value type is unsupported")
	}
	canonicalValue := submissionAssessmentRawValueString(raw["value"])
	if canonicalValue == "" || len([]rune(canonicalValue)) > 4096 {
		return nil, errors.New("submission assessment canonical value is required and bounded")
	}
	value := &verifier.SemanticAssessmentValue{ValueType: valueType, CanonicalValue: canonicalValue}
	if display, exists := submissionAssessmentOptionalString(raw, "display"); exists {
		if len([]rune(display)) > 4096 {
			return nil, errors.New("submission assessment value display is too long")
		}
		value.Display = &display
	}
	if unit, exists := submissionAssessmentOptionalString(raw, "unit"); exists {
		if len([]rune(unit)) > 128 {
			return nil, errors.New("submission assessment value unit is too long")
		}
		value.Unit = &unit
	}
	return value, nil
}

func submissionAssessmentRawString(raw map[string]any, key string) string {
	if raw == nil {
		return ""
	}
	value, _ := raw[key].(string)
	return value
}

func submissionAssessmentRawValueString(raw any) string {
	switch value := raw.(type) {
	case string:
		return strings.TrimSpace(value)
	case float64:
		return strings.TrimSpace(fmt.Sprintf("%g", value))
	case float32:
		return strings.TrimSpace(fmt.Sprintf("%g", value))
	case int:
		return fmt.Sprintf("%d", value)
	case int64:
		return fmt.Sprintf("%d", value)
	case bool:
		return fmt.Sprintf("%t", value)
	default:
		return ""
	}
}

func submissionAssessmentOptionalString(raw map[string]any, key string) (string, bool) {
	value, exists := raw[key]
	if !exists || value == nil {
		return "", false
	}
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(text), true
}

func submissionAssessmentContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func submissionAssessmentOneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if candidate == value {
			return true
		}
	}
	return false
}
