package memoryservice

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

type submissionProposalSpan struct {
	EvidenceIndex int
	Start         int
	End           int
}

// ValidateSubmissionProposal validates the client-side structured proposal
// before it is staged or made available to an assessor.
func ValidateSubmissionProposal(req RememberRequest) error {
	if len(req.EntityHints) == 0 || len(req.RelationshipHints) == 0 {
		return fmt.Errorf("remember: proposal.entities and proposal.relationships are required")
	}
	if len(req.EntityHints) > 100 || len(req.RelationshipHints) > 200 {
		return fmt.Errorf("remember: proposal exceeds supported bounds")
	}
	entityRefs := make(map[string]struct{}, len(req.EntityHints))
	for index, entity := range req.EntityHints {
		if err := validateSubmissionProposalFields(entity, []string{"ref", "name", "entity_kind", "known_entity_id", "evidence"}, fmt.Sprintf("proposal.entities[%d]", index)); err != nil {
			return err
		}
		ref, err := submissionProposalIdentifier(entity, "ref", fmt.Sprintf("proposal.entities[%d].ref", index))
		if err != nil {
			return err
		}
		name, err := submissionProposalExactString(entity, "name", fmt.Sprintf("proposal.entities[%d].name", index))
		if err != nil {
			return err
		}
		if ref == "" || name == "" {
			return fmt.Errorf("remember: proposal.entities[%d] requires ref and name", index)
		}
		if _, exists := entityRefs[ref]; exists {
			return fmt.Errorf("remember: proposal.entities[%d].ref is duplicated", index)
		}
		entityRefs[ref] = struct{}{}
		if rawKnownID, exists := entity["known_entity_id"]; exists && rawKnownID != nil {
			knownID, err := submissionProposalExactString(entity, "known_entity_id", fmt.Sprintf("proposal.entities[%d].known_entity_id", index))
			if err != nil {
				return err
			}
			if _, err := uuid.Parse(knownID); err != nil {
				return fmt.Errorf("remember: proposal.entities[%d].known_entity_id is invalid", index)
			}
		}
		if kind := submissionProposalString(entity, "entity_kind"); kind != "" && !submissionProposalOneOf(kind, domain.EntityKinds()) {
			return fmt.Errorf("remember: proposal.entities[%d].entity_kind is unsupported", index)
		}
		spans, err := submissionProposalSpans(entity["evidence"], len(req.Evidence), fmt.Sprintf("proposal.entities[%d].evidence", index))
		if err != nil {
			return err
		}
		if len(spans) != 1 {
			return fmt.Errorf("remember: proposal.entities[%d].evidence requires exactly one span", index)
		}
		for _, span := range spans {
			surface, err := submissionEvidenceSpan(req.Evidence, span)
			if err != nil || surface != name {
				return fmt.Errorf("remember: proposal.entities[%d] name must match each exact evidence span", index)
			}
		}
	}

	relationshipsByID := make(map[string]struct{}, len(req.RelationshipHints))
	coveredEvidence := make(map[int]struct{}, len(req.Evidence))
	for index, relationship := range req.RelationshipHints {
		if err := validateSubmissionProposalFields(relationship, []string{"proposal_id", "subject_ref", "predicate", "object_ref", "object_value", "polarity", "modality", "evidence"}, fmt.Sprintf("proposal.relationships[%d]", index)); err != nil {
			return err
		}
		proposalID, err := submissionProposalIdentifier(relationship, "proposal_id", fmt.Sprintf("proposal.relationships[%d].proposal_id", index))
		if err != nil {
			return err
		}
		subjectRef, err := submissionProposalIdentifier(relationship, "subject_ref", fmt.Sprintf("proposal.relationships[%d].subject_ref", index))
		if err != nil {
			return err
		}
		if proposalID == "" || subjectRef == "" {
			return fmt.Errorf("remember: proposal.relationships[%d] requires proposal_id and subject_ref", index)
		}
		if _, exists := relationshipsByID[proposalID]; exists {
			return fmt.Errorf("remember: proposal.relationships[%d].proposal_id is duplicated", index)
		}
		relationshipsByID[proposalID] = struct{}{}
		if _, exists := entityRefs[subjectRef]; !exists {
			return fmt.Errorf("remember: proposal.relationships[%d].subject_ref is unknown", index)
		}
		objectRef := submissionProposalString(relationship, "object_ref")
		objectValue, hasObjectValue := submissionProposalObject(relationship["object_value"])
		if (objectRef == "") == !hasObjectValue {
			return fmt.Errorf("remember: proposal.relationships[%d] requires exactly one object endpoint", index)
		}
		if objectRef != "" {
			objectRef, err = submissionProposalIdentifier(relationship, "object_ref", fmt.Sprintf("proposal.relationships[%d].object_ref", index))
			if err != nil {
				return err
			}
			if _, exists := entityRefs[objectRef]; !exists {
				return fmt.Errorf("remember: proposal.relationships[%d].object_ref is unknown", index)
			}
		}
		predicate, ok := submissionProposalObject(relationship["predicate"])
		if !ok {
			return fmt.Errorf("remember: proposal.relationships[%d].predicate is required", index)
		}
		if err := validateSubmissionProposalFields(predicate, []string{"surface", "evidence_index", "start", "end"}, fmt.Sprintf("proposal.relationships[%d].predicate", index)); err != nil {
			return err
		}
		predicateSurface, err := submissionProposalExactString(predicate, "surface", fmt.Sprintf("proposal.relationships[%d].predicate.surface", index))
		if err != nil {
			return err
		}
		predicateSpan, err := submissionProposalSpanFromFields(predicate, "proposal.relationships[%d].predicate", index)
		if err != nil || predicateSurface == "" {
			return fmt.Errorf("remember: proposal.relationships[%d].predicate must be span-grounded", index)
		}
		if surface, err := submissionEvidenceSpan(req.Evidence, predicateSpan); err != nil || surface != predicateSurface {
			return fmt.Errorf("remember: proposal.relationships[%d].predicate surface must match its exact evidence span", index)
		}
		if polarity := submissionProposalString(relationship, "polarity"); polarity != "" && polarity != "+" && polarity != "-" {
			return fmt.Errorf("remember: proposal.relationships[%d].polarity is unsupported", index)
		}
		if modality := submissionProposalString(relationship, "modality"); modality != "" && !submissionProposalOneOf(modality, []string{"statement", "question", "proposal", "speculation", "quoted"}) {
			return fmt.Errorf("remember: proposal.relationships[%d].modality is unsupported", index)
		}
		spans, err := submissionProposalSpans(relationship["evidence"], len(req.Evidence), fmt.Sprintf("proposal.relationships[%d].evidence", index))
		if err != nil {
			return err
		}
		for _, span := range spans {
			if _, err := submissionEvidenceSpan(req.Evidence, span); err != nil {
				return fmt.Errorf("remember: proposal.relationships[%d].evidence contains an invalid span", index)
			}
			coveredEvidence[span.EvidenceIndex] = struct{}{}
		}
		if hasObjectValue {
			if err := validateSubmissionObjectValue(objectValue, index, req.Evidence, spans); err != nil {
				return err
			}
		}
	}
	for index := range req.Evidence {
		if _, covered := coveredEvidence[index]; !covered {
			return fmt.Errorf("remember: evidence[%d] requires at least one relationship proposal", index)
		}
	}
	return nil
}

func submissionProposalString(fields map[string]any, key string) string {
	value, ok := fields[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func submissionProposalExactString(fields map[string]any, key, label string) (string, error) {
	value, ok := fields[key].(string)
	if !ok {
		return "", fmt.Errorf("remember: %s is required", label)
	}
	if value == "" || value != strings.TrimSpace(value) {
		return "", fmt.Errorf("remember: %s must be a non-empty exact string", label)
	}
	return value, nil
}

func submissionProposalIdentifier(fields map[string]any, key, label string) (string, error) {
	value, err := submissionProposalExactString(fields, key, label)
	if err != nil {
		return "", err
	}
	if len([]rune(value)) > 128 {
		return "", fmt.Errorf("remember: %s exceeds the supported length", label)
	}
	for _, runeValue := range value {
		if (runeValue >= 'a' && runeValue <= 'z') || (runeValue >= 'A' && runeValue <= 'Z') ||
			(runeValue >= '0' && runeValue <= '9') || strings.ContainsRune("_:-", runeValue) {
			continue
		}
		return "", fmt.Errorf("remember: %s must be a token identifier", label)
	}
	return value, nil
}

func submissionProposalObject(value any) (map[string]any, bool) {
	fields, ok := value.(map[string]any)
	return fields, ok
}

func submissionProposalSpans(value any, evidenceCount int, label string) ([]submissionProposalSpan, error) {
	items, ok := value.([]any)
	if !ok || len(items) == 0 || len(items) > 20 {
		return nil, fmt.Errorf("remember: %s requires between 1 and 20 spans", label)
	}
	seen := make(map[string]struct{}, len(items))
	spans := make([]submissionProposalSpan, 0, len(items))
	for index, item := range items {
		fields, ok := submissionProposalObject(item)
		if !ok {
			return nil, fmt.Errorf("remember: %s[%d] is invalid", label, index)
		}
		if err := validateSubmissionProposalFields(fields, []string{"evidence_index", "start", "end"}, fmt.Sprintf("%s[%d]", label, index)); err != nil {
			return nil, err
		}
		span, err := submissionProposalSpanFromFields(fields, fmt.Sprintf("%s[%d]", label, index), -1)
		if err != nil {
			return nil, err
		}
		if span.EvidenceIndex < 0 || span.EvidenceIndex >= evidenceCount {
			return nil, fmt.Errorf("remember: %s[%d].evidence_index is invalid", label, index)
		}
		key := fmt.Sprintf("%d:%d:%d", span.EvidenceIndex, span.Start, span.End)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("remember: %s[%d] is duplicated", label, index)
		}
		seen[key] = struct{}{}
		spans = append(spans, span)
	}
	return spans, nil
}

func validateSubmissionProposalFields(fields map[string]any, allowed []string, label string) error {
	if fields == nil {
		return fmt.Errorf("remember: %s is invalid", label)
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	for key := range fields {
		if _, exists := allowedSet[key]; !exists {
			return fmt.Errorf("remember: %s.%s is unsupported", label, key)
		}
	}
	return nil
}

func validateSubmissionObjectValue(
	value map[string]any,
	relationshipIndex int,
	evidence []RememberEvidenceInput,
	spans []submissionProposalSpan,
) error {
	if err := validateSubmissionProposalFields(value, []string{"type", "value", "display", "unit"}, fmt.Sprintf("proposal.relationships[%d].object_value", relationshipIndex)); err != nil {
		return err
	}
	if !submissionProposalOneOf(submissionProposalString(value, "type"), domain.ValueTypes()) {
		return fmt.Errorf("remember: proposal.relationships[%d].object_value.type is unsupported", relationshipIndex)
	}
	raw, exists := value["value"]
	if !exists {
		return fmt.Errorf("remember: proposal.relationships[%d].object_value.value is required", relationshipIndex)
	}
	switch typed := raw.(type) {
	case string:
		if err := validateSubmissionGroundedText(typed, evidence, spans, fmt.Sprintf("proposal.relationships[%d].object_value.value", relationshipIndex)); err != nil {
			return err
		}
	case bool, float64, int, int32, int64, json.Number:
	default:
		return fmt.Errorf("remember: proposal.relationships[%d].object_value.value is invalid", relationshipIndex)
	}
	for _, key := range []string{"display", "unit"} {
		if raw, exists := value[key]; exists {
			text, ok := raw.(string)
			if !ok {
				return fmt.Errorf("remember: proposal.relationships[%d].object_value.%s is invalid", relationshipIndex, key)
			}
			if err := validateSubmissionGroundedText(text, evidence, spans, fmt.Sprintf("proposal.relationships[%d].object_value.%s", relationshipIndex, key)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSubmissionGroundedText(value string, evidence []RememberEvidenceInput, spans []submissionProposalSpan, label string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("remember: %s must be a non-empty exact string", label)
	}
	if _, err := ScanSubmissionEvidence(value); err != nil {
		return err
	}
	for _, span := range spans {
		surface, err := submissionEvidenceSpan(evidence, span)
		if err == nil && strings.Contains(surface, value) {
			return nil
		}
	}
	return fmt.Errorf("remember: %s must be grounded in relationship evidence", label)
}

func submissionProposalOneOf(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func submissionProposalSpanFromFields(fields map[string]any, label string, index int) (submissionProposalSpan, error) {
	evidenceIndex, ok := submissionProposalInt(fields["evidence_index"])
	if !ok {
		return submissionProposalSpan{}, fmt.Errorf("remember: %s evidence_index is required", submissionProposalLabel(label, index))
	}
	start, ok := submissionProposalInt(fields["start"])
	if !ok {
		return submissionProposalSpan{}, fmt.Errorf("remember: %s start is required", submissionProposalLabel(label, index))
	}
	end, ok := submissionProposalInt(fields["end"])
	if !ok || end <= start || start < 0 {
		return submissionProposalSpan{}, fmt.Errorf("remember: %s end is invalid", submissionProposalLabel(label, index))
	}
	return submissionProposalSpan{EvidenceIndex: evidenceIndex, Start: start, End: end}, nil
}

func submissionProposalLabel(label string, index int) string {
	if index < 0 {
		return label
	}
	return fmt.Sprintf(label, index)
}

func submissionProposalInt(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int32:
		return int(typed), true
	case int64:
		if typed > math.MaxInt || typed < math.MinInt {
			return 0, false
		}
		return int(typed), true
	case float64:
		if math.Trunc(typed) != typed || typed > float64(math.MaxInt) || typed < float64(math.MinInt) {
			return 0, false
		}
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil || parsed > math.MaxInt || parsed < math.MinInt {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

func submissionEvidenceSpan(evidence []RememberEvidenceInput, span submissionProposalSpan) (string, error) {
	if span.EvidenceIndex < 0 || span.EvidenceIndex >= len(evidence) {
		return "", fmt.Errorf("evidence index is invalid")
	}
	runes := []rune(evidence[span.EvidenceIndex].Content)
	if span.Start < 0 || span.End <= span.Start || span.End > len(runes) {
		return "", fmt.Errorf("evidence span is invalid")
	}
	return string(runes[span.Start:span.End]), nil
}

func submissionRepositoryEvidence(item RememberEvidenceInput) repository.SubmissionEvidenceInput {
	metadata := make(map[string]any, len(item.Metadata))
	for key, value := range item.Metadata {
		metadata[key] = value
	}
	return repository.SubmissionEvidenceInput{
		Content:                item.Content,
		SourceType:             item.SourceType,
		Source:                 item.Source,
		SourceGroup:            item.SourceGroup,
		Authority:              item.Authority,
		SourceKey:              item.SourceKey,
		SourceRevision:         item.SourceRevision,
		PreviousSourceRevision: item.PreviousSourceRevision,
		SupersedesEvidenceIDs:  append([]string(nil), item.SupersedesEvidenceIDs...),
		IdempotencyKey:         item.IdempotencyKey,
		Labels:                 append([]string(nil), item.Labels...),
		Metadata:               metadata,
	}
}
