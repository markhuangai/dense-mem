package processor

import (
	"context"
	"encoding/json"
	"fmt"

	remember "github.com/markhuangai/dense-mem/internal/remember/service"
)

func (p *rememberSynchronousProcessor) resolveRememberHandles(ctx context.Context, input remember.RememberProcessRequest, snapshot remember.RememberAssessmentSnapshot) (remember.RememberProcessRequest, remember.RememberAssessmentSnapshot, error) {
	raw, err := json.Marshal(snapshot.Proposal)
	if err != nil {
		return input, snapshot, err
	}
	var proposal map[string]any
	if err := json.Unmarshal(raw, &proposal); err != nil {
		return input, snapshot, err
	}
	handles := map[string][]string{}
	walkRememberHandles(proposal, func(kind, id string) string { handles[kind] = append(handles[kind], id); return id })
	for _, evidence := range input.Evidence {
		handles["evidence"] = append(handles["evidence"], evidence.SupersedesEvidenceIDs...)
	}
	total := 0
	for _, ids := range handles {
		total += len(ids)
	}
	if total == 0 {
		return input, snapshot, nil
	}
	resolved, err := p.ledger.ResolveMemoryHandles(ctx, input.TeamID, input.OwnerProfileID, input.SpaceID, handles)
	if err != nil {
		return input, snapshot, err
	}
	for kind, ids := range handles {
		for _, id := range ids {
			if resolved[kind][id] == "" {
				return input, snapshot, fmt.Errorf("historical memory handle resolution was incomplete")
			}
		}
	}
	walkRememberHandles(proposal, func(kind, id string) string { return resolved[kind][id] })
	snapshot.Proposal = proposal
	input.Evidence = append([]remember.EvidenceInput(nil), input.Evidence...)
	for index, evidence := range input.Evidence {
		input.Evidence[index].SupersedesEvidenceIDs = append([]string(nil), evidence.SupersedesEvidenceIDs...)
		for i, id := range evidence.SupersedesEvidenceIDs {
			input.Evidence[index].SupersedesEvidenceIDs[i] = resolved["evidence"][id]
		}
	}
	return input, snapshot, nil
}

func walkRememberHandles(value any, visit func(string, string) string) {
	switch v := value.(type) {
	case []any:
		for _, item := range v {
			walkRememberHandles(item, visit)
		}
	case map[string]any:
		for key, item := range v {
			switch key {
			case "known_entity_id":
				if id, ok := item.(string); ok && id != "" {
					v[key] = visit("entity", id)
				}
			case "known_evidence_ids":
				if ids, ok := item.([]any); ok {
					for i, id := range ids {
						if text, ok := id.(string); ok {
							ids[i] = visit("evidence", text)
						}
					}
				}
			case "correction_target":
				if target, ok := item.(map[string]any); ok {
					if id, ok := target["relationship_id"].(string); ok && id != "" {
						target["relationship_id"] = visit("relationship", id)
					}
				}
			default:
				walkRememberHandles(item, visit)
			}
		}
	}
}
