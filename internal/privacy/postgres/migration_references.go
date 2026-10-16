package postgres

import (
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"time"

	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

var privateMigrationJSONReferences = map[string][]string{
	"relationship_observations":           {"evidence"},
	"relationship_correction_submissions": {"patch", "supports", "candidates", "selection"},
	"relationship_correction_events":      {"patch", "supports"},
	"submission_relationship_results":     {"splits"},
}

func (p *privateMigrationPlan) addMoveReferences(moves []*privateMigrationMove) error {
	byDestination := map[string]map[string]string{}
	for _, move := range moves {
		if move.Sealed {
			continue
		}
		table := p.Tables[move.Table]
		var oldKey, newKey []json.RawMessage
		if err := json.Unmarshal([]byte(move.SourceKey), &oldKey); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(move.DestinationKey), &newKey); err != nil {
			return err
		}
		key := move.Team + ":" + move.DestinationSpace
		if byDestination[key] == nil {
			byDestination[key] = map[string]string{}
		}
		for i, column := range table.Key {
			if column == "team_id" || column == "space_id" {
				continue
			}
			oldID, newID := migrationString(oldKey[i]), migrationString(newKey[i])
			if oldID == "" || newID == "" || oldID == newID {
				continue
			}
			if previous := byDestination[key][oldID]; previous != "" && previous != newID {
				return fmt.Errorf("ambiguous historical identity in %s", move.Table)
			}
			byDestination[key][oldID] = newID
		}
	}
	records := map[string]*privateMigrationRecord{}
	for _, record := range p.Records {
		records[record.Table.Name+":"+record.Key] = record
	}
	for _, move := range moves {
		all := byDestination[move.Team+":"+move.DestinationSpace]
		ids := map[string]string{}
		for _, raw := range records[move.Table+":"+move.SourceKey].Fields {
			var value any
			if err := json.Unmarshal(raw, &value); err != nil {
				return err
			}
			for _, id := range privateMigrationUUIDs(value) {
				if next := all[id]; next != "" {
					ids[id] = next
				}
			}
		}
		if len(ids) > 0 {
			raw, err := json.Marshal(ids)
			if err != nil {
				return err
			}
			move.Replacements["_historical_ids"] = raw
		}
	}
	return nil
}

func applyPrivateMigrationReferences(move *privateMigrationMove, body map[string]json.RawMessage) error {
	var ids map[string]string
	if raw := move.Replacements["_historical_ids"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &ids); err != nil {
			return err
		}
	}
	if move.Table == "search_documents" {
		if next := ids[migrationString(body["source_id"])]; next != "" {
			body["source_id"], _ = json.Marshal(next)
		}
	}
	for _, column := range privateMigrationJSONReferences[move.Table] {
		var value any
		if err := json.Unmarshal(body[column], &value); err != nil {
			return err
		}
		body[column], _ = json.Marshal(remapPrivateMigrationJSON(value, ids))
	}
	if move.Table == "relationship_records" {
		var from *time.Time
		if raw := body["valid_from"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &from); err != nil {
				return err
			}
		}
		key := knowledge.RelationshipSemanticGroupKey(migrationString(body["subject_entity_id"]), migrationString(body["predicate_key"]), migrationString(body["object_entity_id"]), migrationString(body["object_value_id"]), migrationString(body["polarity"]), migrationString(body["scope_key"]), from)
		body["semantic_group_key"], _ = json.Marshal(key)
	}
	return nil
}

func remapPrivateMigrationJSON(value any, ids map[string]string) any {
	switch v := value.(type) {
	case string:
		if next := ids[v]; next != "" {
			return next
		}
	case []any:
		for i, item := range v {
			v[i] = remapPrivateMigrationJSON(item, ids)
		}
	case map[string]any:
		for key, item := range v {
			if privateMigrationReferenceField(key) {
				v[key] = remapPrivateMigrationJSON(item, ids)
			} else {
				switch item.(type) {
				case []any, map[string]any:
					v[key] = remapPrivateMigrationJSON(item, ids)
				}
			}
		}
	}
	return value
}

func privateMigrationUUIDs(value any) []string {
	var result []string
	switch v := value.(type) {
	case string:
		if _, err := uuid.Parse(v); err == nil {
			result = append(result, v)
		}
	case []any:
		for _, item := range v {
			result = append(result, privateMigrationUUIDs(item)...)
		}
	case map[string]any:
		for _, item := range v {
			result = append(result, privateMigrationUUIDs(item)...)
		}
	}
	return result
}

func privateMigrationReferenceField(key string) bool {
	switch key {
	case "evidence_id", "fragment_id", "entity_id", "subject_entity_id", "object_entity_id", "relationship_id", "successor_relationship_id", "occurrence_id", "evidence_ids", "relationship_ids", "entity_ids":
		return true
	default:
		return false
	}
}
func privateMigrationReferenceIDs(value any) []string {
	var result []string
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			result = append(result, privateMigrationReferenceIDs(item)...)
		}
	case map[string]any:
		for key, item := range v {
			if privateMigrationReferenceField(key) {
				result = append(result, privateMigrationUUIDs(item)...)
			} else {
				result = append(result, privateMigrationReferenceIDs(item)...)
			}
		}
	}
	return result
}
