package registry

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

type v2ContractSourceRevision struct {
	revision   string
	previous   string
	supersedes string
}

func validateV2SourceRevisionBatch(
	index int,
	fields map[string]any,
	seen map[string]v2ContractSourceRevision,
) error {
	sourceKey, _ := fields["source_key"].(string)
	if sourceKey == "" {
		return nil
	}
	supersedes, _ := fields["supersedes_fragment_ids"].([]any)
	current := v2ContractSourceRevision{
		revision:   v2StringField(fields, "source_revision"),
		previous:   v2StringField(fields, "previous_source_revision"),
		supersedes: fmt.Sprint(supersedes),
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

func v2StringField(fields map[string]any, field string) string {
	value, _ := fields[field].(string)
	return value
}

func validateV2DecisionFields(args map[string]any, fields ...string) error {
	decision, ok := objectFields(args["decision"])
	if !ok {
		return fmt.Errorf("decision is required for action %s", args["action"])
	}
	for _, field := range fields {
		value, exists := decision[field]
		if !exists || v2ContractValueEmpty(value) {
			return fmt.Errorf("decision.%s is required for action %s", field, args["action"])
		}
	}
	return nil
}

func validateV2CorrectEntityResolution(args map[string]any) error {
	if err := validateV2UniqueStringArray(args, "owned_observation_ids"); err != nil {
		return err
	}
	owned, _ := args["owned_observation_ids"].([]any)
	if len(owned) == 0 {
		return fmt.Errorf("owned_observation_ids is required")
	}
	operation, _ := args["operation"].(string)
	target, hasTarget := args["target_entity_id"]
	switch operation {
	case "merge":
		if !hasTarget || v2ContractValueEmpty(target) {
			return fmt.Errorf("target_entity_id is required for operation merge")
		}
	case "split":
		if !hasTarget || target != nil {
			return fmt.Errorf("target_entity_id must be null for operation split")
		}
	}
	dryRun, _ := args["dry_run"].(bool)
	if !dryRun {
		if v2ContractValueEmpty(args["impact_token"]) {
			return fmt.Errorf("impact_token is required for correction apply")
		}
	}
	return nil
}

func validateV2RecallFeedback(args map[string]any) error {
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

func validateV2DreamFeedback(args map[string]any) error {
	decision, _ := args["decision"].(string)
	switch decision {
	case "confirm_true", "confirm_false":
		return validateV2RequiredFields(args, "evidence")
	default:
		return validateV2RequiredFields(args, "reason")
	}
}

func validateV2MemoryPackSource(args map[string]any) error {
	artifact, hasArtifact := nonEmptyString(args["artifact_json"])
	rawURL, hasURL := nonEmptyString(args["url"])
	if hasArtifact == hasURL {
		return fmt.Errorf("exactly one of artifact_json or url is required")
	}
	if hasArtifact && utf8.RuneCountInString(artifact) > v2MemoryPackArtifactMaxLength {
		return fmt.Errorf("artifact_json exceeds maximum length of %d", v2MemoryPackArtifactMaxLength)
	}
	if hasURL {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("url must be an absolute HTTPS URL")
		}
		if _, ok := args["expected_sha256"]; !ok {
			return fmt.Errorf("expected_sha256 is required for url")
		}
	}
	if digest, ok := args["expected_sha256"].(string); ok {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 {
			return fmt.Errorf("expected_sha256 must be a 64-character hexadecimal SHA-256")
		}
	}
	return nil
}

func nonEmptyString(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	return text, text != ""
}

func validateV2RollbackMemoryPackImport(args map[string]any) error {
	dryRun, _ := args["dry_run"].(bool)
	if dryRun {
		return nil
	}
	if v2ContractValueEmpty(args["impact_token"]) {
		return fmt.Errorf("impact_token is required for rollback apply")
	}
	return nil
}

func validateV2UniqueObjectRefsIn(raw any, path string, refField string) error {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	seen := map[string]struct{}{}
	for i, item := range items {
		fields, ok := objectFields(item)
		if !ok {
			continue
		}
		value, _ := fields[refField].(string)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s[%d].%s: duplicate ref %q", path, i, refField, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateV2RelationshipObjectChoiceIn(raw any, path string) error {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	for i, item := range items {
		fields, ok := objectFields(item)
		if !ok {
			continue
		}
		objectRef, hasObjectRef := fields["object_ref"].(string)
		hasObjectRef = hasObjectRef && strings.TrimSpace(objectRef) != ""
		_, hasObjectValue := fields["object_value"]
		if hasObjectRef == hasObjectValue {
			return fmt.Errorf("%s[%d]: exactly one of object_ref or object_value is required", path, i)
		}
	}
	return nil
}

func validateV2ProposalReferencesAndSpans(proposal map[string]any, evidence []any) error {
	entityRefs := map[string]struct{}{}
	entities, _ := proposal["entities"].([]any)
	for _, raw := range entities {
		entity, _ := objectFields(raw)
		ref, _ := entity["ref"].(string)
		entityRefs[ref] = struct{}{}
	}
	relationships, _ := proposal["relationships"].([]any)
	for i, raw := range relationships {
		relationship, _ := objectFields(raw)
		for _, field := range []string{"subject_ref", "object_ref"} {
			ref, ok := relationship[field].(string)
			if !ok || ref == "" {
				continue
			}
			if _, exists := entityRefs[ref]; !exists {
				return fmt.Errorf("proposal.relationships[%d].%s references unknown Entity ref %q", i, field, ref)
			}
		}
		spans, _ := relationship["evidence"].([]any)
		for j, rawSpan := range spans {
			span, _ := objectFields(rawSpan)
			indexNumber, _ := schemaNumber(span["evidence_index"])
			index := int(indexNumber)
			if index < 0 || index >= len(evidence) {
				return fmt.Errorf("proposal.relationships[%d].evidence[%d].evidence_index is outside submitted evidence", i, j)
			}
			startNumber, _ := schemaNumber(span["start"])
			endNumber, _ := schemaNumber(span["end"])
			start, end := int(startNumber), int(endNumber)
			evidenceItem, _ := objectFields(evidence[index])
			content, _ := evidenceItem["content"].(string)
			if start < 0 || end <= start || end > utf8.RuneCountInString(content) {
				return fmt.Errorf("proposal.relationships[%d].evidence[%d] has invalid code-point span", i, j)
			}
		}
		if objectValue, ok := objectFields(relationship["object_value"]); ok {
			if err := validateV2TypedValue(objectValue, fmt.Sprintf("proposal.relationships[%d].object_value", i)); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateV2TypedValue(value map[string]any, path string) error {
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

func v2ContractValueEmpty(value any) bool {
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
