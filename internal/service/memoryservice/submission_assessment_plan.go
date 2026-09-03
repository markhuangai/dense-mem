package memoryservice

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type submissionAssessmentItem struct {
	ItemID                      string
	Fragment                    repository.EvidenceFragment
	EvidenceID                  string
	DuplicateAssessmentRequired bool
}

type submissionAssessmentEntityTarget struct {
	Target        assessor.SemanticAssessmentRequiredEntityRef
	KnownEntityID string
}

type submissionAssessmentRelationshipTarget struct {
	Target                   assessor.SemanticAssessmentRequiredRelationshipRef
	ProposedPredicate        string
	KnownPredicateKey        string
	KnownEvidenceIDs         []string
	KnownEvidenceUnavailable bool
	SubjectKind              string
	ObjectKind               string
	CorrectionTarget         *repository.SemanticCorrectionTargetInput
	ConflictContext          *repository.SemanticConflictContextInput
}

type submissionAssessmentPlan struct {
	Items                           []submissionAssessmentItem
	EntityTargets                   []submissionAssessmentEntityTarget
	RelationshipTargets             []submissionAssessmentRelationshipTarget
	itemsByEvidenceID               map[string]submissionAssessmentItem
	knownEvidenceByID               map[string]repository.SubmissionAssessmentKnownEvidence
	entityTargetsByRef              map[string]submissionAssessmentEntityTarget
	relationshipsByRef              map[string]submissionAssessmentRelationshipTarget
	duplicateCandidatesByEvidenceID map[string]repository.RememberDuplicateCandidateGroup
	exactDuplicateByEvidenceID      map[string]repository.RememberDuplicateResolution
}

func buildSubmissionAssessmentPlan(snapshot RememberAssessmentSnapshot) (submissionAssessmentPlan, error) {
	fragmentsByIndex := make(map[int]repository.EvidenceFragment, len(snapshot.Evidence))
	for _, fragment := range snapshot.Evidence {
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
	if len(fragmentsByIndex) > assessor.SemanticAssessmentMaxEvidenceSpans {
		return submissionAssessmentPlan{}, fmt.Errorf("submission assessment evidence must contain at most %d entries", assessor.SemanticAssessmentMaxEvidenceSpans)
	}

	items := make([]submissionAssessmentItem, 0, len(snapshot.Items))
	itemsByEvidenceID := make(map[string]submissionAssessmentItem, len(snapshot.Items))
	for _, item := range snapshot.Items {
		fragment, ok := fragmentsByIndex[item.Fragment.EvidenceIndex]
		if !ok || item.Fragment.FragmentID != fragment.FragmentID || strings.TrimSpace(item.ItemID) == "" {
			return submissionAssessmentPlan{}, errors.New("submission assessment evidence item does not match evidence")
		}
		evidenceID := submissionAssessmentEvidenceID(item.Fragment.EvidenceIndex)
		if _, exists := itemsByEvidenceID[evidenceID]; exists {
			return submissionAssessmentPlan{}, errors.New("submission assessment evidence index is duplicated")
		}
		entry := submissionAssessmentItem{
			ItemID: item.ItemID, Fragment: fragment, EvidenceID: evidenceID,
			DuplicateAssessmentRequired: item.DuplicateAssessmentRequired,
		}
		items = append(items, entry)
		itemsByEvidenceID[evidenceID] = entry
	}
	if len(items) != len(fragmentsByIndex) {
		return submissionAssessmentPlan{}, errors.New("submission assessment must include every staged evidence item")
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Fragment.EvidenceIndex < items[j].Fragment.EvidenceIndex
	})

	plan := submissionAssessmentPlan{
		Items:                           items,
		itemsByEvidenceID:               itemsByEvidenceID,
		knownEvidenceByID:               map[string]repository.SubmissionAssessmentKnownEvidence{},
		entityTargetsByRef:              map[string]submissionAssessmentEntityTarget{},
		relationshipsByRef:              map[string]submissionAssessmentRelationshipTarget{},
		duplicateCandidatesByEvidenceID: map[string]repository.RememberDuplicateCandidateGroup{},
		exactDuplicateByEvidenceID:      map[string]repository.RememberDuplicateResolution{},
	}
	for index, resolution := range snapshot.ExactDuplicateEvidence {
		if index < 0 || index >= len(snapshot.Evidence) {
			return submissionAssessmentPlan{}, errors.New("submission assessment exact duplicate index is invalid")
		}
		resolution.EvidenceIndex = index
		resolution.EvidenceID = submissionAssessmentEvidenceID(index)
		plan.exactDuplicateByEvidenceID[resolution.EvidenceID] = resolution
	}
	for _, group := range snapshot.DuplicateCandidates {
		if group.EvidenceIndex < 0 || group.EvidenceIndex >= len(snapshot.Evidence) {
			return submissionAssessmentPlan{}, errors.New("submission assessment duplicate candidate index is invalid")
		}
		group.EvidenceID = submissionAssessmentEvidenceID(group.EvidenceIndex)
		if _, exists := plan.duplicateCandidatesByEvidenceID[group.EvidenceID]; exists {
			return submissionAssessmentPlan{}, errors.New("submission assessment duplicate candidate index is duplicated")
		}
		plan.duplicateCandidatesByEvidenceID[group.EvidenceID] = group
	}
	rawRelationships, err := submissionAssessmentObjectArray(snapshot.Proposal, "relationship_hints", "relationships")
	if err != nil {
		return submissionAssessmentPlan{}, err
	}
	if len(rawRelationships) > assessor.SemanticAssessmentMaxRelationshipResults {
		return submissionAssessmentPlan{}, errors.New("submission assessment relationships exceed the configured bound")
	}
	for index, raw := range rawRelationships {
		target, entities, err := submissionAssessmentRelationshipTargetFromProposal(raw, index, plan.itemsByEvidenceID)
		if err != nil {
			return submissionAssessmentPlan{}, err
		}
		if _, exists := plan.relationshipsByRef[target.Target.ProposalID]; exists {
			return submissionAssessmentPlan{}, errors.New("submission assessment relationship ref is duplicated")
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
	if len(plan.RelationshipTargets) > 0 && len(plan.EntityTargets) == 0 {
		return submissionAssessmentPlan{}, errors.New("submission assessment relationship targets require entities")
	}
	if len(plan.EntityTargets) > assessor.SemanticAssessmentMaxEntityResults {
		return submissionAssessmentPlan{}, errors.New("submission assessment entity targets must be present and bounded")
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
		if !exists || value == nil {
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
	knownEvidenceIDs, err := submissionAssessmentKnownEvidenceIDsFromProposal(raw)
	if err != nil {
		return submissionAssessmentRelationshipTarget{}, nil, err
	}

	subjectRaw, ok := proposalMap(raw["subject"])
	if !ok {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship subject is required")
	}
	subject, err := submissionAssessmentEntityTargetFromProposal(subjectRaw, fmt.Sprintf("entity:%d:subject", index), evidenceIDs, knownEvidenceIDs)
	if err != nil {
		return submissionAssessmentRelationshipTarget{}, nil, err
	}

	predicateRaw, ok := proposalMap(raw["predicate"])
	if !ok {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship predicate is required")
	}
	proposedPredicate := strings.TrimSpace(submissionAssessmentRawString(predicateRaw, "proposed_key"))
	knownPredicateKey := strings.TrimSpace(submissionAssessmentRawString(predicateRaw, "known_predicate_key"))
	if knownPredicateKey != "" {
		proposedPredicate = knownPredicateKey
	}
	if proposedPredicate == "" || len([]rune(proposedPredicate)) > 128 {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment predicate hint is required and bounded")
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
	var objectValue *assessor.SemanticAssessmentValue
	if hasEntity {
		objectEntity, err := submissionAssessmentEntityTargetFromProposal(objectEntityRaw, fmt.Sprintf("entity:%d:object", index), evidenceIDs, knownEvidenceIDs)
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
	target := assessor.SemanticAssessmentRequiredRelationshipRef{
		ProposalID:       ref,
		PredicateHint:    proposedPredicate,
		EvidenceIDs:      append([]string(nil), evidenceIDs...),
		KnownEvidenceIDs: append([]string(nil), knownEvidenceIDs...),
		SubjectRef:       subject.Target.Ref,
		ObjectRef:        objectRef,
		ObjectValue:      objectValue,
		Polarity:         polarity,
		ValidFrom:        submissionAssessmentTimeString(validFrom),
		ValidTo:          submissionAssessmentTimeString(validTo),
	}
	entry := submissionAssessmentRelationshipTarget{
		Target:            target,
		ProposedPredicate: proposedPredicate,
		KnownPredicateKey: knownPredicateKey,
		KnownEvidenceIDs:  append([]string(nil), knownEvidenceIDs...),
		SubjectKind:       subject.Target.Kind,
		ObjectKind:        objectKind,
	}
	if _, exists := raw["correction_target"]; exists {
		correction, ok := semanticProposalCorrectionTarget(raw)
		if !ok {
			return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment correction target is invalid")
		}
		entry.CorrectionTarget = &repository.SemanticCorrectionTargetInput{
			RelationshipID:  correction.RelationshipID,
			ExpectedVersion: correction.ExpectedVersion,
		}
	}
	if _, exists := raw["conflict_context"]; exists {
		conflict, ok := semanticProposalConflictContext(raw)
		if !ok {
			return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment conflict context is invalid")
		}
		entry.ConflictContext = &repository.SemanticConflictContextInput{
			ConflictID:      conflict.ConflictID,
			ExpectedVersion: conflict.ExpectedVersion,
		}
	}
	return entry, entities, nil
}

func submissionAssessmentKnownEvidenceIDsFromProposal(raw map[string]any) ([]string, error) {
	values, exists := raw["known_evidence_ids"]
	if !exists || values == nil {
		return nil, nil
	}
	items := rememberArrayValues(values)
	if items == nil {
		return nil, errors.New("submission assessment relationship known_evidence_ids must be an array")
	}
	if len(items) > 20 {
		return nil, errors.New("submission assessment relationship known_evidence_ids must contain at most 20 IDs")
	}
	result := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, value := range items {
		id, ok := value.(string)
		if !ok {
			return nil, errors.New("submission assessment relationship known_evidence_ids must contain UUIDs")
		}
		id = strings.TrimSpace(id)
		parsed, err := uuid.Parse(id)
		if err != nil {
			return nil, errors.New("submission assessment relationship known_evidence_ids must contain UUIDs")
		}
		id = parsed.String()
		if _, exists := seen[id]; exists {
			return nil, errors.New("submission assessment relationship known_evidence_ids contains a duplicate")
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
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
	if len(values) == 0 || len(values) > assessor.SemanticAssessmentMaxEvidenceSpans {
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
	knownEvidenceIDs []string,
) (submissionAssessmentEntityTarget, error) {
	name := strings.TrimSpace(submissionAssessmentRawString(raw, "name"))
	if name != "" && len([]rune(name)) > 256 {
		return submissionAssessmentEntityTarget{}, errors.New("submission assessment entity name is too long")
	}
	kind := strings.TrimSpace(submissionAssessmentRawString(raw, "entity_kind"))
	if kind != "" && !submissionAssessmentContains(domain.EntityKinds(), kind) {
		return submissionAssessmentEntityTarget{}, errors.New("submission assessment entity kind is unsupported")
	}
	knownEntityID := strings.TrimSpace(submissionAssessmentRawString(raw, "known_entity_id"))
	if name == "" && knownEntityID == "" {
		return submissionAssessmentEntityTarget{}, errors.New("submission assessment entity requires name or known_entity_id")
	}
	if knownEntityID != "" {
		if _, err := uuid.Parse(knownEntityID); err != nil {
			return submissionAssessmentEntityTarget{}, errors.New("submission assessment known entity id is invalid")
		}
	}
	return submissionAssessmentEntityTarget{
		Target: assessor.SemanticAssessmentRequiredEntityRef{
			Ref:              ref,
			Name:             name,
			Kind:             kind,
			EvidenceIDs:      append([]string(nil), evidenceIDs...),
			KnownEvidenceIDs: append([]string(nil), knownEvidenceIDs...),
		},
		KnownEntityID: knownEntityID,
	}, nil
}

func submissionAssessmentValueFromProposal(raw map[string]any) (*assessor.SemanticAssessmentValue, error) {
	valueType := strings.TrimSpace(submissionAssessmentRawString(raw, "type"))
	if !submissionAssessmentContains(domain.ValueTypes(), valueType) {
		return nil, errors.New("submission assessment value type is unsupported")
	}
	canonicalValue := submissionAssessmentRawValueString(raw["value"])
	if canonicalValue == "" || len([]rune(canonicalValue)) > 4096 {
		return nil, errors.New("submission assessment canonical value is required and bounded")
	}
	value := &assessor.SemanticAssessmentValue{ValueType: valueType, CanonicalValue: canonicalValue}
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
