package registry

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type contractSourceRevision struct {
	revision  string
	previous  string
	authority string
	envelope  string
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
		revision:  stringField(fields, "source_revision"),
		previous:  stringField(fields, "previous_source_revision"),
		authority: stringField(fields, "authority"),
	}
	if current.authority == "" {
		current.authority = "primary"
	}
	envelope, err := json.Marshal(map[string]any{
		"source_type":  fields["source_type"],
		"source":       fields["source"],
		"source_group": fields["source_group"],
		"metadata":     fields["metadata"],
	})
	if err != nil {
		return fmt.Errorf("evidence[%d].metadata: source revision provenance is invalid", index)
	}
	current.envelope = string(envelope)
	prior, exists := seen[sourceKey]
	if !exists {
		seen[sourceKey] = current
		return nil
	}
	if prior != current {
		field := "source_revision"
		if prior.revision == current.revision {
			if prior.previous != current.previous {
				field = "previous_source_revision"
			} else {
				field = "source_key"
			}
		}
		return fmt.Errorf("evidence[%d].%s: source_key revision fields must match earlier item in request, including source provenance", index, field)
	}
	return nil
}

func validateDirectEvidenceSupersessions(evidence []any) error {
	targets := map[string]int{}
	for index, item := range evidence {
		fields, ok := objectFields(item)
		if !ok {
			continue
		}
		targetIDs, _ := fields["supersedes_evidence_ids"].([]any)
		if len(targetIDs) == 0 {
			continue
		}
		for targetIndex, rawID := range targetIDs {
			if err := validateContractUUID(rawID, fmt.Sprintf("evidence[%d].supersedes_evidence_ids[%d]", index, targetIndex)); err != nil {
				return err
			}
			targetID, _ := rawID.(string)
			if previous, exists := targets[targetID]; exists {
				return fmt.Errorf("evidence[%d].supersedes_evidence_ids: duplicates target from evidence[%d]", index, previous)
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

func validateContractUUID(value any, path string) error {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return fmt.Errorf("%s must be a valid UUID", path)
	}
	if _, err := uuid.Parse(strings.TrimSpace(text)); err != nil {
		return fmt.Errorf("%s must be a valid UUID", path)
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
			return fmt.Errorf("recalls[%d].recall_event_id: duplicate value", i)
		}
		seen[recallID] = struct{}{}
		comment, _ := item["feedback_comment"].(string)
		quality, _ := item["quality"].(string)
		missing, _ := item["missing_context"].(bool)
		irrelevant, _ := item["irrelevant"].(bool)
		if (quality == "low" || missing || irrelevant) && strings.TrimSpace(comment) == "" {
			return fmt.Errorf("recalls[%d].feedback_comment is required for negative feedback", i)
		}
	}
	return nil
}
