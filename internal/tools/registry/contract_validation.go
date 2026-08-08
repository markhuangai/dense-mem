package registry

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type contractSourceRevision struct {
	revision string
	previous string
}

func validateSourceRevisionBatch(
	index int,
	fields map[string]any,
	seen map[string]contractSourceRevision,
) error {
	sourceKey, _ := fields["source_key"].(string)
	if sourceKey == "" {
		return nil
	}
	current := contractSourceRevision{
		revision: stringField(fields, "source_revision"),
		previous: stringField(fields, "previous_source_revision"),
	}
	prior, exists := seen[sourceKey]
	if !exists {
		seen[sourceKey] = current
		return nil
	}
	if prior != current {
		return fmt.Errorf("evidence[%d]: source_key %q revision fields must match earlier item in request", index, sourceKey)
	}
	return nil
}

func validateDirectEvidenceSupersessions(evidence []any) error {
	targets := map[string]int{}
	keys := map[string]int{}
	for index, item := range evidence {
		fields, ok := objectFields(item)
		if !ok {
			continue
		}
		targetIDs, _ := fields["supersedes_evidence_ids"].([]any)
		if len(targetIDs) == 0 {
			continue
		}
		key := strings.TrimSpace(stringField(fields, "idempotency_key"))
		if key == "" {
			return fmt.Errorf("evidence[%d]: idempotency_key is required when supersedes_evidence_ids is set", index)
		}
		if previous, exists := keys[key]; exists {
			return fmt.Errorf("evidence[%d]: idempotency_key duplicates evidence[%d] direct supersession", index, previous)
		}
		keys[key] = index
		for _, rawID := range targetIDs {
			targetID, _ := rawID.(string)
			if previous, exists := targets[targetID]; exists {
				return fmt.Errorf("evidence[%d]: supersedes_evidence_ids duplicates target from evidence[%d]", index, previous)
			}
			targets[targetID] = index
		}
	}
	return nil
}

func stringField(fields map[string]any, field string) string {
	value, _ := fields[field].(string)
	return value
}

func validateDecisionFields(args map[string]any, fields ...string) error {
	decision, ok := objectFields(args["decision"])
	if !ok {
		return fmt.Errorf("decision is required for action %s", args["action"])
	}
	for _, field := range fields {
		value, exists := decision[field]
		if !exists || contractValueEmpty(value) {
			return fmt.Errorf("decision.%s is required for action %s", field, args["action"])
		}
	}
	return nil
}

func validateCorrectRelationship(args map[string]any) error {
	action, _ := args["action"].(string)
	switch action {
	case "submit":
		for _, field := range []string{"relationship_id", "expected_version", "patch", "supports", "reason"} {
			if contractValueEmpty(args[field]) {
				return fmt.Errorf("%s is required for action submit", field)
			}
		}
		for _, field := range []string{"submission_id", "confirmation_token", "selection"} {
			if !contractValueEmpty(args[field]) {
				return fmt.Errorf("%s is not accepted for action submit", field)
			}
		}
		patch, _ := objectFields(args["patch"])
		if len(patch) == 0 {
			return fmt.Errorf("patch must change at least one relationship field")
		}
		supports, _ := args["supports"].([]any)
		seen := make(map[string]struct{}, len(supports))
		for index, raw := range supports {
			support, _ := objectFields(raw)
			key := fmt.Sprintf("%s\x00%v\x00%v", stringField(support, "evidence_id"), support["start"], support["end"])
			if _, exists := seen[key]; exists {
				return fmt.Errorf("supports[%d] duplicates an evidence span", index)
			}
			seen[key] = struct{}{}
		}
	case "confirm":
		for _, field := range []string{"submission_id", "confirmation_token", "selection"} {
			if contractValueEmpty(args[field]) {
				return fmt.Errorf("%s is required for action confirm", field)
			}
		}
		for _, field := range []string{"relationship_id", "expected_version", "patch", "supports", "reason"} {
			if !contractValueEmpty(args[field]) {
				return fmt.Errorf("%s is not accepted for action confirm", field)
			}
		}
		selection, _ := objectFields(args["selection"])
		if contractValueEmpty(selection["subject_entity_id"]) && contractValueEmpty(selection["object_entity_id"]) {
			return fmt.Errorf("selection must choose at least one Entity candidate")
		}
	default:
		return errors.New("action must be submit or confirm")
	}
	return nil
}

func validateRecallFeedback(args map[string]any) error {
	recalls, _ := args["recalls"].([]any)
	seen := map[string]struct{}{}
	for i, raw := range recalls {
		item, _ := objectFields(raw)
		recallID, _ := item["recall_event_id"].(string)
		if _, exists := seen[recallID]; exists {
			return fmt.Errorf("recalls[%d].recall_event_id: duplicate value %q", i, recallID)
		}
		seen[recallID] = struct{}{}
		comment, _ := item["feedback_comment"].(string)
		quality, _ := item["quality"].(string)
		missing, _ := item["missing_context"].(bool)
		irrelevant, _ := item["irrelevant"].(bool)
		if (quality == "low" || missing || irrelevant) && strings.TrimSpace(comment) == "" {
			return fmt.Errorf("recalls[%d].feedback_comment is required for negative feedback", i)
		}
		hypotheses, _ := item["hypothesis_feedback"].([]any)
		for j, hypothesisRaw := range hypotheses {
			hypothesis, _ := objectFields(hypothesisRaw)
			hypothesisQuality, _ := hypothesis["quality"].(string)
			contradicted, _ := hypothesis["contradicted"].(bool)
			hypothesisComment, _ := hypothesis["feedback_comment"].(string)
			if (hypothesisQuality == "low" || contradicted) && strings.TrimSpace(hypothesisComment) == "" {
				return fmt.Errorf("recalls[%d].hypothesis_feedback[%d].feedback_comment is required for negative feedback", i, j)
			}
		}
	}
	return nil
}

func validateDreamFeedback(args map[string]any) error {
	decision, _ := args["decision"].(string)
	switch decision {
	case "confirm_true", "confirm_false":
		if err := validateRequiredFields(args, "evidence"); err != nil {
			return err
		}
		evidence, _ := args["evidence"].([]any)
		sourceRevisions := map[string]contractSourceRevision{}
		for i, item := range evidence {
			fields, ok := objectFields(item)
			if !ok {
				continue
			}
			if err := validateSourceRevisionFields(i, fields); err != nil {
				return err
			}
			if err := validateSourceRevisionBatch(i, fields, sourceRevisions); err != nil {
				return err
			}
		}
		if err := validateDirectEvidenceSupersessions(evidence); err != nil {
			return err
		}
		if err := validateRequiredFields(args, "relationships"); err != nil {
			return err
		}
		return validateSubmittedRelationships(args["relationships"], evidence, "relationships")
	default:
		return validateRequiredFields(args, "reason")
	}
}

type submittedRelationshipSpan struct {
	evidenceIndex int
	start         int
	end           int
}

func validateSubmittedRelationships(raw any, evidence []any, path string) error {
	relationships, ok := raw.([]any)
	if !ok {
		return nil
	}
	seenRefs := make(map[string]struct{}, len(relationships))
	for index, item := range relationships {
		relationship, ok := objectFields(item)
		if !ok {
			continue
		}
		ref, _ := relationship["ref"].(string)
		normalizedRef := strings.TrimSpace(ref)
		if normalizedRef == "" {
			return fmt.Errorf("%s[%d].ref: must not be blank", path, index)
		}
		if _, exists := seenRefs[normalizedRef]; exists {
			return fmt.Errorf("%s[%d].ref: duplicate ref %q", path, index, normalizedRef)
		}
		seenRefs[normalizedRef] = struct{}{}
		if err := validateSubmittedRelationship(relationship, evidence, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateSubmittedEvidenceCoverage(raw any, evidence []any, path string) error {
	covered := make([]bool, len(evidence))
	relationships, ok := raw.([]any)
	if !ok || len(relationships) == 0 {
		return nil
	}
	for _, item := range relationships {
		relationship, ok := objectFields(item)
		if !ok {
			continue
		}
		supports, ok := relationship["supports"].([]any)
		if !ok {
			continue
		}
		for _, support := range supports {
			fields, ok := objectFields(support)
			if !ok {
				continue
			}
			index, ok := schemaNumber(fields["evidence_index"])
			if ok && int(index) >= 0 && int(index) < len(covered) {
				covered[int(index)] = true
			}
		}
	}
	missing := make([]int, 0)
	for index, present := range covered {
		if !present {
			missing = append(missing, index)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s must cover every evidence item; missing evidence indexes: %v", path, missing)
	}
	return nil
}

func validateSubmittedRelationship(relationship map[string]any, evidence []any, path string) error {
	supports, err := submittedRelationshipSupports(relationship["supports"], evidence, path+".supports")
	if err != nil {
		return err
	}

	subject, ok := objectFields(relationship["subject"])
	if !ok {
		return fmt.Errorf("%s.subject must be an object", path)
	}
	subjectSpan, err := validateSubmittedEntity(subject, evidence, path+".subject")
	if err != nil {
		return err
	}
	if !submittedSpanCoveredBySupport(subjectSpan, supports) {
		return fmt.Errorf("%s.subject.span is not covered by a support span", path)
	}

	predicate, ok := objectFields(relationship["predicate"])
	if !ok {
		return fmt.Errorf("%s.predicate must be an object", path)
	}
	predicateSpan, err := validateSubmittedSurfaceSpan(predicate, "surface", evidence, path+".predicate")
	if err != nil {
		return err
	}
	if !submittedSpanCoveredBySupport(predicateSpan, supports) {
		return fmt.Errorf("%s.predicate.span is not covered by a support span", path)
	}

	object, ok := objectFields(relationship["object"])
	if !ok {
		return fmt.Errorf("%s.object must be an object", path)
	}
	_, hasEntity := object["entity"]
	_, hasValue := object["value"]
	if hasEntity == hasValue {
		return fmt.Errorf("%s.object requires exactly one entity or value", path)
	}
	if hasEntity {
		entity, ok := objectFields(object["entity"])
		if !ok {
			return fmt.Errorf("%s.object.entity must be an object", path)
		}
		objectSpan, err := validateSubmittedEntity(entity, evidence, path+".object.entity")
		if err != nil {
			return err
		}
		if !submittedSpanCoveredBySupport(objectSpan, supports) {
			return fmt.Errorf("%s.object.entity.span is not covered by a support span", path)
		}
	} else {
		value, ok := objectFields(object["value"])
		if !ok {
			return fmt.Errorf("%s.object.value must be an object", path)
		}
		if err := validateTypedValue(value, path+".object.value"); err != nil {
			return err
		}
		valueSpan, err := validateSubmittedSurfaceSpan(value, "surface", evidence, path+".object.value")
		if err != nil {
			return err
		}
		if !submittedSpanCoveredBySupport(valueSpan, supports) {
			return fmt.Errorf("%s.object.value.span is not covered by a support span", path)
		}
		for _, field := range []string{"display", "unit"} {
			if valueText, ok := value[field].(string); ok && valueText != "" && !submittedSupportContains(valueText, supports, evidence) {
				return fmt.Errorf("%s.object.value.%s must occur within a support span", path, field)
			}
		}
	}

	if target, ok := objectFields(relationship["correction_target"]); ok {
		if err := validateCorrectionTarget(target, path+".correction_target"); err != nil {
			return err
		}
	}
	if context, ok := objectFields(relationship["conflict_context"]); ok {
		if err := validateConflictContext(context, path+".conflict_context"); err != nil {
			return err
		}
	}
	return validateRelationshipValidityWindow(relationship, path)
}

func validateSubmittedEntity(entity map[string]any, evidence []any, path string) (submittedRelationshipSpan, error) {
	span, err := validateSubmittedSurfaceSpan(entity, "name", evidence, path)
	if err != nil {
		return submittedRelationshipSpan{}, err
	}
	return span, nil
}

func validateSubmittedSurfaceSpan(fields map[string]any, surfaceField string, evidence []any, path string) (submittedRelationshipSpan, error) {
	spanFields, ok := objectFields(fields["span"])
	if !ok {
		return submittedRelationshipSpan{}, fmt.Errorf("%s.span must be an object", path)
	}
	span, text, err := submittedRelationshipSpanFromFields(spanFields, evidence, path+".span")
	if err != nil {
		return submittedRelationshipSpan{}, err
	}
	surface, _ := fields[surfaceField].(string)
	if surface != text {
		return submittedRelationshipSpan{}, fmt.Errorf("%s.%s must equal its exact evidence span", path, surfaceField)
	}
	return span, nil
}

func submittedRelationshipSupports(raw any, evidence []any, path string) ([]submittedRelationshipSpan, error) {
	items, ok := raw.([]any)
	if !ok {
		return nil, nil
	}
	spans := make([]submittedRelationshipSpan, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for index, item := range items {
		fields, ok := objectFields(item)
		if !ok {
			continue
		}
		span, _, err := submittedRelationshipSpanFromFields(fields, evidence, fmt.Sprintf("%s[%d]", path, index))
		if err != nil {
			return nil, err
		}
		key := fmt.Sprintf("%d:%d:%d", span.evidenceIndex, span.start, span.end)
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("%s[%d] duplicates a support span", path, index)
		}
		seen[key] = struct{}{}
		spans = append(spans, span)
	}
	return spans, nil
}

func submittedRelationshipSpanFromFields(fields map[string]any, evidence []any, path string) (submittedRelationshipSpan, string, error) {
	indexNumber, indexOK := schemaNumber(fields["evidence_index"])
	startNumber, startOK := schemaNumber(fields["start"])
	endNumber, endOK := schemaNumber(fields["end"])
	if !indexOK || !startOK || !endOK {
		return submittedRelationshipSpan{}, "", fmt.Errorf("%s must contain integer evidence_index, start, and end", path)
	}
	index, start, end := int(indexNumber), int(startNumber), int(endNumber)
	if index < 0 || index >= len(evidence) {
		return submittedRelationshipSpan{}, "", fmt.Errorf("%s.evidence_index is outside submitted evidence", path)
	}
	evidenceItem, ok := objectFields(evidence[index])
	if !ok {
		return submittedRelationshipSpan{}, "", fmt.Errorf("%s.evidence_index is outside submitted evidence", path)
	}
	content, _ := evidenceItem["content"].(string)
	runes := []rune(content)
	if start < 0 || end <= start || end > len(runes) {
		return submittedRelationshipSpan{}, "", fmt.Errorf("%s has invalid code-point span", path)
	}
	return submittedRelationshipSpan{evidenceIndex: index, start: start, end: end}, string(runes[start:end]), nil
}

func submittedSpanCoveredBySupport(component submittedRelationshipSpan, supports []submittedRelationshipSpan) bool {
	for _, support := range supports {
		if support.evidenceIndex == component.evidenceIndex && support.start <= component.start && support.end >= component.end {
			return true
		}
	}
	return false
}

func submittedSupportContains(value string, supports []submittedRelationshipSpan, evidence []any) bool {
	for _, support := range supports {
		if support.evidenceIndex < 0 || support.evidenceIndex >= len(evidence) {
			continue
		}
		evidenceItem, ok := objectFields(evidence[support.evidenceIndex])
		if !ok {
			continue
		}
		content, _ := evidenceItem["content"].(string)
		runes := []rune(content)
		if support.start < 0 || support.end > len(runes) || support.end <= support.start {
			continue
		}
		if strings.Contains(string(runes[support.start:support.end]), value) {
			return true
		}
	}
	return false
}

func validateRelationshipValidityWindow(relationship map[string]any, path string) error {
	validFrom, err := contractOptionalTime(relationship["valid_from"], path+".valid_from")
	if err != nil {
		return err
	}
	validTo, err := contractOptionalTime(relationship["valid_to"], path+".valid_to")
	if err != nil {
		return err
	}
	if validFrom != nil && validTo != nil && validTo.Before(*validFrom) {
		return fmt.Errorf("%s.valid_to must not be before valid_from", path)
	}
	return nil
}

func contractOptionalTime(raw any, path string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	text, ok := raw.(string)
	if !ok {
		return nil, fmt.Errorf("%s must be an RFC3339 timestamp", path)
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return nil, fmt.Errorf("%s must be an RFC3339 timestamp", path)
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func validateCorrectionTarget(target map[string]any, path string) error {
	relationshipID, _ := target["relationship_id"].(string)
	if strings.TrimSpace(relationshipID) == "" {
		return fmt.Errorf("%s.relationship_id is required", path)
	}
	version, ok := schemaNumber(target["expected_version"])
	if !ok || int(version) < 1 {
		return fmt.Errorf("%s.expected_version must be a positive integer", path)
	}
	return nil
}

func validateConflictContext(context map[string]any, path string) error {
	conflictID, _ := context["conflict_id"].(string)
	if strings.TrimSpace(conflictID) == "" {
		return fmt.Errorf("%s.conflict_id is required", path)
	}
	version, ok := schemaNumber(context["expected_version"])
	if !ok || int(version) < 1 {
		return fmt.Errorf("%s.expected_version must be a positive integer", path)
	}
	return nil
}

func validateTypedValue(value map[string]any, path string) error {
	valueType, _ := value["type"].(string)
	canonical := value["value"]
	switch valueType {
	case "string", "date", "date_time":
		if _, ok := canonical.(string); !ok {
			return fmt.Errorf("%s.value must be a string for type %s", path, valueType)
		}
	case "number":
		if _, ok := schemaNumber(canonical); !ok {
			return fmt.Errorf("%s.value must be a number for type number", path)
		}
	case "boolean":
		if _, ok := canonical.(bool); !ok {
			return fmt.Errorf("%s.value must be a boolean for type boolean", path)
		}
	}
	return nil
}

func contractValueEmpty(value any) bool {
	if value == nil {
		return true
	}
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text) == ""
	}
	if items, ok := value.([]any); ok {
		return len(items) == 0
	}
	return false
}
