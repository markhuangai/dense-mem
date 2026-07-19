package registry

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

const v2FeedbackTimeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func recordV2RecallFeedbackSnapshot(
	ctx context.Context,
	deps Dependencies,
	input map[string]any,
	req memoryservice.V2RecallRequest,
	res *memoryservice.V2RecallResult,
) {
	if res == nil || res.RecallID == "" || deps.RecallFeedbackEvents == nil {
		return
	}
	if !RecallFeedbackEnabled(ctx, deps.RecallFeedbackConfig) || deps.Metrics == nil {
		return
	}
	degradation := map[string]any{}
	if res.Degradation != nil {
		if mapped, err := structToMap(res.Degradation); err == nil {
			degradation = mapped
		}
	}
	err := deps.RecallFeedbackEvents.RecordRecallSnapshot(ctx, domain.RecallFeedbackEvent{
		RecallID:        res.RecallID,
		ToolName:        V2ToolRecallMemory,
		Query:           req.Query,
		ToolArgs:        v2RecallFeedbackToolArgs(input, req),
		ResultRefs:      v2RecallFeedbackResultRefs(res),
		ContractVersion: domain.V2ContractVersion,
		SearchState:     res.SearchState,
		Degradation:     degradation,
		SnapshotMetadata: map[string]any{
			"result_schema": "v2.evidence_relationship_refs.v1",
		},
	})
	if err != nil {
		res.RecallID = ""
	}
}

func v2RecallFeedbackToolArgs(input map[string]any, req memoryservice.V2RecallRequest) map[string]any {
	effective := map[string]any{
		"query": req.Query,
		"limit": req.Limit,
	}
	if req.ValidAt != nil {
		effective["valid_at"] = req.ValidAt.UTC().Format(v2FeedbackTimeFormat)
	}
	if req.KnownAt != nil {
		effective["known_at"] = req.KnownAt.UTC().Format(v2FeedbackTimeFormat)
	}
	if len(req.KnownEvidenceIDs) > 0 {
		effective["known_evidence_ids"] = append([]string(nil), req.KnownEvidenceIDs...)
	}
	if len(req.KnownRelationshipIDs) > 0 {
		effective["known_relationship_ids"] = append([]string(nil), req.KnownRelationshipIDs...)
	}
	if len(req.ExpandFromEntityIDs) > 0 {
		effective["expand_from_entity_ids"] = append([]string(nil), req.ExpandFromEntityIDs...)
	}
	return map[string]any{
		"input":     v2RecallFeedbackInputCopy(input),
		"effective": effective,
	}
}

func v2RecallFeedbackInputCopy(input map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{
		"query",
		"limit",
		"valid_at",
		"known_at",
		"known_evidence_ids",
		"known_relationship_ids",
		"expand_from_entity_ids",
	} {
		if value, ok := input[key]; ok {
			out[key] = value
		}
	}
	return out
}

func v2RecallFeedbackResultRefs(res *memoryservice.V2RecallResult) []domain.RecallFeedbackResultRef {
	if res == nil {
		return []domain.RecallFeedbackResultRef{}
	}
	refs := make([]domain.RecallFeedbackResultRef, 0, len(res.Results)*2)
	for _, item := range res.Results {
		rank := item.Rank
		if rank <= 0 {
			rank = len(refs) + 1
		}
		if item.EvidenceID != "" {
			refs = append(refs, domain.RecallFeedbackResultRef{
				Type:           domain.RecallFeedbackResultTypeEvidence,
				ID:             item.EvidenceID,
				Rank:           rank,
				Tier:           "evidence",
				StatusAtRecall: res.SearchState,
			})
		}
		for _, relationshipID := range item.RelationshipIDs {
			if relationshipID == "" {
				continue
			}
			refs = append(refs, domain.RecallFeedbackResultRef{
				Type:           domain.RecallFeedbackResultTypeRelationship,
				ID:             relationshipID,
				Rank:           rank,
				Tier:           "relationship",
				StatusAtRecall: res.SearchState,
			})
		}
	}
	return refs
}
