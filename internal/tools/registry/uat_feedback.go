package registry

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/recall"
)

const feedbackTimeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func recordRecallFeedbackSnapshot(
	ctx context.Context,
	deps Dependencies,
	input map[string]any,
	req recall.RecallRequest,
	res *recall.RecallResult,
) bool {
	if res == nil || res.RecallID == "" || deps.RecallFeedbackEvents == nil {
		return false
	}
	if !RecallFeedbackEnabled(ctx, deps.RecallFeedbackConfig) || deps.Metrics == nil {
		return false
	}
	degradation := map[string]any{}
	if res.Degradation != nil {
		if mapped, err := structToMap(res.Degradation); err == nil {
			degradation = mapped
		}
	}
	err := deps.RecallFeedbackEvents.RecordRecallSnapshot(ctx, domain.RecallFeedbackEvent{
		RecallID:        res.RecallID,
		ToolName:        ToolRecallMemory,
		Query:           req.Query,
		ToolArgs:        recallFeedbackToolArgs(input, req),
		ResultRefs:      recall.FeedbackResultRefs(res),
		ContractVersion: domain.ContractVersion,
		SearchState:     res.SearchState,
		Degradation:     degradation,
		SnapshotMetadata: map[string]any{
			"result_schema": "v2.evidence_community_relationship_refs.v1",
		},
	})
	if err != nil {
		res.Degradations = append(res.Degradations, recall.RecallDegradationResult{
			Frontier: "feedback",
			Optional: true,
			Code:     "recall_feedback_snapshot_unavailable",
			Message:  "Recall succeeded, but session feedback is unavailable for this result.",
		})
		res.Degradation = &res.Degradations[0]
		return false
	}
	return true
}

func setRecallSuggestedActions(res *recall.RecallResult, feedbackSnapshotStored, dreamingEnabled bool) {
	if res == nil {
		return
	}
	actions := make([]recall.RecallSuggestedAction, 0, 2)
	if feedbackSnapshotStored && res.RecallID != "" {
		actions = append(actions, recall.RecallSuggestedAction{
			Tool:          ToolSubmitRecallSessionFeedback,
			RecallEventID: res.RecallID,
			Guidance:      "After using this recall, report the session outcome with this recall_event_id.",
		})
	}
	if dreamingEnabled && len(res.RelatedHypotheses) > 0 {
		hypothesisIDs := make([]string, 0, len(res.RelatedHypotheses))
		for _, hypothesis := range res.RelatedHypotheses {
			if hypothesis.HypothesisID != "" {
				hypothesisIDs = append(hypothesisIDs, hypothesis.HypothesisID)
			}
		}
		if len(hypothesisIDs) > 0 {
			actions = append(actions, recall.RecallSuggestedAction{
				Tool:          ToolResolveDreamFeedback,
				HypothesisIDs: hypothesisIDs,
				Guidance:      "Confirm true or false only with independent evidence; leave uncertain hypotheses unresolved.",
			})
		}
	}
	res.SuggestedActions = actions
}

func submitRecallFeedback(ctx context.Context, deps Dependencies, input map[string]any) (map[string]any, error) {
	batch := recall.SubmitRecallFeedbackBatch(ctx, deps.RecallFeedbackEvents, deps.Metrics, recallFeedbackSubmissions(input))
	result := map[string]any{
		"recorded":       batch.Recorded,
		"recorded_count": batch.RecordedCount,
	}
	if batch.Failure != nil {
		result["partial_success"] = batch.Failure.PartialSuccess
		result["failed_index"] = batch.Failure.FailedIndex
		result["error"] = batch.Failure.Error
		result["error_code"] = batch.Failure.ErrorCode
		result["reason_code"] = batch.Failure.ReasonCode
		result["next_action"] = batch.Failure.NextAction
		result["remediation"] = batch.Failure.Remediation
	}
	return result, nil
}

func recallFeedbackSubmissions(input map[string]any) []domain.RecallFeedbackSubmission {
	recalls := objectArray(input["recalls"])
	out := make([]domain.RecallFeedbackSubmission, 0, len(recalls))
	for _, item := range recalls {
		out = append(out, domain.RecallFeedbackSubmission{
			RecallID:        stringInput(item["recall_event_id"]),
			Used:            boolInput(item["used"]),
			AnswerSupported: boolInput(item["answer_supported"]),
			Quality:         stringInput(item["quality"]),
			MissingContext:  boolInput(item["missing_context"]),
			Irrelevant:      boolInput(item["irrelevant"]),
			FeedbackComment: stringInput(item["feedback_comment"]),
			IrrelevantRefs:  recallFeedbackJudgedRefs(item["irrelevant_result_refs"]),
			DreamFeedback:   recallHypothesisFeedback(item["hypothesis_feedback"]),
		})
	}
	return out
}

func recallFeedbackJudgedRefs(value any) []domain.RecallFeedbackJudgedResultRef {
	rawRefs := objectArray(value)
	refs := make([]domain.RecallFeedbackJudgedResultRef, 0, len(rawRefs))
	for _, raw := range rawRefs {
		rank, _ := intInput(raw["rank"])
		refs = append(refs, domain.RecallFeedbackJudgedResultRef{
			Type: stringInput(raw["type"]),
			ID:   stringInput(raw["id"]),
			Rank: rank,
		})
	}
	return refs
}

func recallHypothesisFeedback(value any) []domain.RecallFeedbackDreamFeedback {
	rawItems := objectArray(value)
	items := make([]domain.RecallFeedbackDreamFeedback, 0, len(rawItems))
	for _, raw := range rawItems {
		items = append(items, domain.RecallFeedbackDreamFeedback{
			DreamID:         stringInput(raw["hypothesis_id"]),
			Used:            boolInput(raw["used"]),
			Quality:         stringInput(raw["quality"]),
			Contradicted:    boolInput(raw["contradicted"]),
			FeedbackComment: stringInput(raw["feedback_comment"]),
		})
	}
	return items
}

func recallFeedbackToolArgs(input map[string]any, req recall.RecallRequest) map[string]any {
	effective := map[string]any{
		"query": req.Query,
		"limit": req.Limit,
	}
	if req.RelationshipLimit != nil {
		effective["relationship_limit"] = *req.RelationshipLimit
	}
	if req.CommunityLimit != nil {
		effective["community_limit"] = *req.CommunityLimit
	}
	if req.CommunityRelationshipLimit != nil {
		effective["community_relationship_limit"] = *req.CommunityRelationshipLimit
	}
	if req.ValidAt != nil {
		effective["valid_at"] = req.ValidAt.UTC().Format(feedbackTimeFormat)
	}
	if req.KnownAt != nil {
		effective["known_at"] = req.KnownAt.UTC().Format(feedbackTimeFormat)
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
		"input":     recallFeedbackInputCopy(input),
		"effective": effective,
	}
}

func recallFeedbackInputCopy(input map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{
		"query",
		"limit",
		"valid_at",
		"known_at",
		"known_evidence_ids",
		"known_relationship_ids",
		"expand_from_entity_ids",
		"relationship_limit",
		"community_limit",
		"community_relationship_limit",
	} {
		if value, ok := input[key]; ok {
			out[key] = value
		}
	}
	return out
}
