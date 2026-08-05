package memoryservice

import (
	"errors"
	"fmt"
	"sort"
	"strings"

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
	Target          verifier.SemanticAssessmentRequiredEntityRef
	PlacementItemID string
	FragmentID      string
	KnownEntityID   string
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
	for _, item := range items {
		plan.itemsByEvidenceID[item.EvidenceID] = item
	}

	rawRelationships, err := submissionAssessmentObjectArray(placement.Proposal, "relationship_hints", "relationships")
	if err != nil {
		return submissionAssessmentPlan{}, err
	}
	if len(rawRelationships) == 0 || len(rawRelationships) > verifier.SemanticAssessmentMaxRelationshipResults {
		return submissionAssessmentPlan{}, errors.New("submission assessment relationships must be present and bounded")
	}
	entitiesBySpan := map[string]submissionAssessmentEntityTarget{}
	for index, raw := range rawRelationships {
		target, entities, err := submissionAssessmentRelationshipTargetFromProposal(raw, index, plan.itemsByEvidenceID)
		if err != nil {
			return submissionAssessmentPlan{}, err
		}
		if _, exists := plan.relationshipsByRef[target.Target.ProposalID]; exists {
			return submissionAssessmentPlan{}, errors.New("submission assessment relationship ref is duplicated")
		}
		for _, entity := range entities {
			spanKey := submissionAssessmentEntitySpanKey(entity.Target)
			if prior, exists := entitiesBySpan[spanKey]; exists {
				if prior.Target.Kind != entity.Target.Kind || prior.Target.Surface != entity.Target.Surface ||
					(prior.KnownEntityID != "" && entity.KnownEntityID != "" && prior.KnownEntityID != entity.KnownEntityID) {
					return submissionAssessmentPlan{}, errors.New("submission assessment entity target is inconsistent")
				}
				if prior.KnownEntityID == "" && entity.KnownEntityID != "" {
					entitiesBySpan[spanKey] = entity
					plan.entityTargetsByRef[entity.Target.Ref] = entity
				}
				continue
			}
			entitiesBySpan[spanKey] = entity
			plan.entityTargetsByRef[entity.Target.Ref] = entity
		}
		plan.RelationshipTargets = append(plan.RelationshipTargets, target)
		plan.relationshipsByRef[target.Target.ProposalID] = target
	}
	for _, entity := range entitiesBySpan {
		plan.EntityTargets = append(plan.EntityTargets, entity)
	}
	if len(plan.EntityTargets) == 0 || len(plan.EntityTargets) > verifier.SemanticAssessmentMaxEntityResults {
		return submissionAssessmentPlan{}, errors.New("submission assessment entity targets must be present and bounded")
	}
	sort.Slice(plan.EntityTargets, func(i, j int) bool {
		left, right := plan.EntityTargets[i].Target, plan.EntityTargets[j].Target
		if left.EvidenceID != right.EvidenceID {
			return left.EvidenceID < right.EvidenceID
		}
		if left.Start != right.Start {
			return left.Start < right.Start
		}
		return left.End < right.End
	})
	sort.Slice(plan.RelationshipTargets, func(i, j int) bool {
		return plan.RelationshipTargets[i].Target.ProposalID < plan.RelationshipTargets[j].Target.ProposalID
	})
	return plan, nil
}

func submissionAssessmentEvidenceID(index int) string {
	return fmt.Sprintf("evidence:%d", index)
}

func submissionAssessmentEntitySpanKey(target verifier.SemanticAssessmentRequiredEntityRef) string {
	return fmt.Sprintf("%s:%d:%d", target.EvidenceID, target.Start, target.End)
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
				fields, ok := reviewMap(item)
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
	supports, err := submissionAssessmentSupportsFromProposal(raw, itemsByEvidenceID)
	if err != nil {
		return submissionAssessmentRelationshipTarget{}, nil, err
	}
	subjectRaw, ok := reviewMap(raw["subject"])
	if !ok {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship subject is required")
	}
	subject, err := submissionAssessmentEntityTargetFromProposal(subjectRaw, itemsByEvidenceID)
	if err != nil {
		return submissionAssessmentRelationshipTarget{}, nil, err
	}
	if !submissionAssessmentSpanCoveredBySupports(subject.Target.EvidenceID, subject.Target.Start, subject.Target.End, supports) {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment subject span is outside support")
	}

	predicateRaw, ok := reviewMap(raw["predicate"])
	if !ok {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship predicate is required")
	}
	proposedPredicate := strings.TrimSpace(submissionAssessmentRawString(predicateRaw, "proposed_key"))
	if proposedPredicate == "" || len([]rune(proposedPredicate)) > 128 {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment proposed predicate is required and bounded")
	}
	predicateSurface, predicateEvidenceID, predicateStart, predicateEnd, err := submissionAssessmentSurfaceSpanFromProposal(predicateRaw, "surface", itemsByEvidenceID)
	if err != nil {
		return submissionAssessmentRelationshipTarget{}, nil, err
	}
	if len([]rune(predicateSurface)) > 256 || !submissionAssessmentSpanCoveredBySupports(predicateEvidenceID, predicateStart, predicateEnd, supports) {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment predicate span is outside support")
	}

	objectRaw, ok := reviewMap(raw["object"])
	if !ok {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship object is required")
	}
	objectEntityRaw, hasEntity := reviewMap(objectRaw["entity"])
	objectValueRaw, hasValue := reviewMap(objectRaw["value"])
	if hasEntity == hasValue {
		return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment relationship requires exactly one object endpoint")
	}
	entities := []submissionAssessmentEntityTarget{subject}
	objectKind := ""
	var objectRef *string
	var objectValue *verifier.SemanticAssessmentValue
	if hasEntity {
		objectEntity, err := submissionAssessmentEntityTargetFromProposal(objectEntityRaw, itemsByEvidenceID)
		if err != nil {
			return submissionAssessmentRelationshipTarget{}, nil, err
		}
		if !submissionAssessmentSpanCoveredBySupports(objectEntity.Target.EvidenceID, objectEntity.Target.Start, objectEntity.Target.End, supports) {
			return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment object span is outside support")
		}
		entities = append(entities, objectEntity)
		objectKind = objectEntity.Target.Kind
		refCopy := objectEntity.Target.Ref
		objectRef = &refCopy
	} else {
		value, evidenceID, start, end, err := submissionAssessmentValueFromProposal(objectValueRaw, itemsByEvidenceID)
		if err != nil {
			return submissionAssessmentRelationshipTarget{}, nil, err
		}
		if !submissionAssessmentSpanCoveredBySupports(evidenceID, start, end, supports) {
			return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment value span is outside support")
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
	target := verifier.SemanticAssessmentRequiredRelationshipRef{
		ProposalID:        ref,
		Evidence:          supports,
		SubjectRef:        subject.Target.Ref,
		OriginalPredicate: predicateSurface,
		ObjectRef:         objectRef,
		ObjectValue:       objectValue,
		Polarity:          polarity,
		Modality:          modality,
	}
	entry := submissionAssessmentRelationshipTarget{
		Target:            target,
		ProposedPredicate: proposedPredicate,
		SubjectKind:       subject.Target.Kind,
		ObjectKind:        objectKind,
	}
	if _, exists := raw["correction_target"]; exists {
		correction, ok := placementReviewCorrectionTarget(raw)
		if !ok {
			return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment correction target is invalid")
		}
		entry.CorrectionTarget = &repository.PlacementCorrectionTargetInput{
			RelationshipID:  correction.RelationshipID,
			ExpectedVersion: correction.ExpectedVersion,
		}
	}
	if _, exists := raw["conflict_context"]; exists {
		conflict, ok := placementReviewConflictContext(raw)
		if !ok {
			return submissionAssessmentRelationshipTarget{}, nil, errors.New("submission assessment conflict context is invalid")
		}
		entry.ConflictContext = &repository.PlacementConflictContextInput{
			ConflictID:      conflict.ConflictID,
			ExpectedVersion: conflict.ExpectedVersion,
		}
	}
	_ = index
	return entry, entities, nil
}

func submissionAssessmentEntityTargetFromProposal(
	raw map[string]any,
	itemsByEvidenceID map[string]submissionAssessmentItem,
) (submissionAssessmentEntityTarget, error) {
	surface, evidenceID, start, end, err := submissionAssessmentSurfaceSpanFromProposal(raw, "name", itemsByEvidenceID)
	if err != nil {
		return submissionAssessmentEntityTarget{}, err
	}
	if len([]rune(surface)) > 256 {
		return submissionAssessmentEntityTarget{}, errors.New("submission assessment entity surface is too long")
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
	ref := fmt.Sprintf("entity:%s:%d:%d:%s", evidenceID, start, end, kind)
	item := itemsByEvidenceID[evidenceID]
	return submissionAssessmentEntityTarget{
		Target: verifier.SemanticAssessmentRequiredEntityRef{
			Ref:        ref,
			Surface:    surface,
			Kind:       kind,
			EvidenceID: evidenceID,
			Start:      start,
			End:        end,
		},
		PlacementItemID: item.PlacementItem.PlacementItemID,
		FragmentID:      item.Fragment.FragmentID,
		KnownEntityID:   knownEntityID,
	}, nil
}

func submissionAssessmentValueFromProposal(
	raw map[string]any,
	itemsByEvidenceID map[string]submissionAssessmentItem,
) (*verifier.SemanticAssessmentValue, string, int, int, error) {
	_, evidenceID, start, end, err := submissionAssessmentSurfaceSpanFromProposal(raw, "surface", itemsByEvidenceID)
	if err != nil {
		return nil, "", 0, 0, err
	}
	valueType := strings.TrimSpace(submissionAssessmentRawString(raw, "type"))
	if !submissionAssessmentContains(domain.ValueTypes(), valueType) {
		return nil, "", 0, 0, errors.New("submission assessment value type is unsupported")
	}
	canonicalValue := submissionAssessmentRawValueString(raw["value"])
	if canonicalValue == "" || len([]rune(canonicalValue)) > 4096 {
		return nil, "", 0, 0, errors.New("submission assessment canonical value is required and bounded")
	}
	value := &verifier.SemanticAssessmentValue{ValueType: valueType, CanonicalValue: canonicalValue}
	if display, exists := submissionAssessmentOptionalString(raw, "display"); exists {
		if len([]rune(display)) > 4096 {
			return nil, "", 0, 0, errors.New("submission assessment value display is too long")
		}
		value.Display = &display
	}
	if unit, exists := submissionAssessmentOptionalString(raw, "unit"); exists {
		if len([]rune(unit)) > 128 {
			return nil, "", 0, 0, errors.New("submission assessment value unit is too long")
		}
		value.Unit = &unit
	}
	return value, evidenceID, start, end, nil
}

func submissionAssessmentSurfaceSpanFromProposal(
	raw map[string]any,
	surfaceField string,
	itemsByEvidenceID map[string]submissionAssessmentItem,
) (string, string, int, int, error) {
	surface, ok := raw[surfaceField].(string)
	if !ok || strings.TrimSpace(surface) == "" {
		return "", "", 0, 0, errors.New("submission assessment surface is required")
	}
	spanRaw, ok := reviewMap(raw["span"])
	if !ok {
		return "", "", 0, 0, errors.New("submission assessment span is required")
	}
	evidenceIndex, hasIndex := reviewInt(spanRaw, "evidence_index")
	start, hasStart := reviewInt(spanRaw, "start")
	end, hasEnd := reviewInt(spanRaw, "end")
	if !hasIndex || !hasStart || !hasEnd {
		return "", "", 0, 0, errors.New("submission assessment span is invalid")
	}
	evidenceID := submissionAssessmentEvidenceID(evidenceIndex)
	item, exists := itemsByEvidenceID[evidenceID]
	if !exists {
		return "", "", 0, 0, errors.New("submission assessment span evidence is outside the run")
	}
	exact, err := verifier.SemanticEvidenceSpan(item.Fragment.Content, start, end)
	if err != nil || exact != surface {
		return "", "", 0, 0, errors.New("submission assessment surface does not match its evidence span")
	}
	return exact, evidenceID, start, end, nil
}

func submissionAssessmentSupportsFromProposal(
	raw map[string]any,
	itemsByEvidenceID map[string]submissionAssessmentItem,
) ([]verifier.SemanticAssessmentEvidenceSpan, error) {
	items, err := submissionAssessmentObjectArray(raw, "supports")
	if err != nil {
		return nil, err
	}
	if len(items) == 0 || len(items) > verifier.SemanticAssessmentMaxEvidenceSpans {
		return nil, errors.New("submission assessment relationship supports are required and bounded")
	}
	spans := make([]verifier.SemanticAssessmentEvidenceSpan, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, rawSpan := range items {
		evidenceIndex, hasIndex := reviewInt(rawSpan, "evidence_index")
		start, hasStart := reviewInt(rawSpan, "start")
		end, hasEnd := reviewInt(rawSpan, "end")
		if !hasIndex || !hasStart || !hasEnd {
			return nil, errors.New("submission assessment support span is invalid")
		}
		evidenceID := submissionAssessmentEvidenceID(evidenceIndex)
		item, exists := itemsByEvidenceID[evidenceID]
		if !exists {
			return nil, errors.New("submission assessment support evidence is outside the run")
		}
		if _, err := verifier.SemanticEvidenceSpan(item.Fragment.Content, start, end); err != nil {
			return nil, errors.New("submission assessment support span is invalid")
		}
		key := fmt.Sprintf("%s:%d:%d", evidenceID, start, end)
		if _, exists := seen[key]; exists {
			return nil, errors.New("submission assessment support span is duplicated")
		}
		seen[key] = struct{}{}
		spans = append(spans, verifier.SemanticAssessmentEvidenceSpan{EvidenceID: evidenceID, Start: start, End: end})
	}
	return spans, nil
}

func submissionAssessmentSpanCoveredBySupports(evidenceID string, start, end int, supports []verifier.SemanticAssessmentEvidenceSpan) bool {
	for _, support := range supports {
		if support.EvidenceID == evidenceID && support.Start <= start && support.End >= end {
			return true
		}
	}
	return false
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
