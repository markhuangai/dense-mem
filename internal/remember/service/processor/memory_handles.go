package processor

import (
	"context"
	"fmt"

	remember "github.com/markhuangai/dense-mem/internal/remember/service"
)

func (p *rememberSynchronousProcessor) resolveRememberEvidenceHandles(ctx context.Context, input remember.RememberProcessRequest) (remember.RememberProcessRequest, error) {
	var ids []string
	for _, evidence := range input.Evidence {
		ids = append(ids, evidence.SupersedesEvidenceIDs...)
	}
	if len(ids) == 0 {
		return input, nil
	}
	resolved, err := p.ledger.ResolveMemoryHandles(ctx, input.TeamID, input.OwnerProfileID, input.SpaceID, map[string][]string{"evidence": ids})
	if err != nil {
		return input, err
	}
	input.Evidence = append([]remember.EvidenceInput(nil), input.Evidence...)
	for index, evidence := range input.Evidence {
		input.Evidence[index].SupersedesEvidenceIDs = append([]string(nil), evidence.SupersedesEvidenceIDs...)
		for i, id := range evidence.SupersedesEvidenceIDs {
			value := resolved["evidence"][id]
			if value == "" {
				return input, fmt.Errorf("historical Evidence handle resolution was incomplete")
			}
			input.Evidence[index].SupersedesEvidenceIDs[i] = value
		}
	}
	return input, nil
}
