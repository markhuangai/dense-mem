package registry

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	appservice "github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

const feedbackTimeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func recordRecallFeedbackSnapshot(
	ctx context.Context,
	deps Dependencies,
	input map[string]any,
	req memoryservice.RecallRequest,
	res *memoryservice.RecallResult,
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
		ResultRefs:      recallFeedbackResultRefs(res),
		ContractVersion: domain.ContractVersion,
		SearchState:     res.SearchState,
		Degradation:     degradation,
		SnapshotMetadata: map[string]any{
			"result_schema": "v2.evidence_community_relationship_refs.v1",
		},
	})
	if err != nil {
		res.Degradations = append(res.Degradations, memoryservice.RecallDegradationResult{
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

func setRecallSuggestedActions(res *memoryservice.RecallResult, feedbackSnapshotStored, dreamingEnabled bool) {
	if res == nil {
		return
	}
	actions := make([]memoryservice.RecallSuggestedAction, 0, 2)
	if feedbackSnapshotStored && res.RecallID != "" {
		actions = append(actions, memoryservice.RecallSuggestedAction{
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
			actions = append(actions, memoryservice.RecallSuggestedAction{
				Tool:          ToolResolveDreamFeedback,
				HypothesisIDs: hypothesisIDs,
				Guidance:      "Confirm true or false only with independent evidence; leave uncertain hypotheses unresolved.",
			})
		}
	}
	res.SuggestedActions = actions
}

func submitRecallFeedback(ctx context.Context, deps Dependencies, input map[string]any) (map[string]any, error) {
	submissions := recallFeedbackSubmissions(input)
	recorded := 0
	for _, submission := range submissions {
		if err := deps.RecallFeedbackEvents.RecordRecallFeedback(ctx, submission); err != nil {
			//nolint:nilerr // Partial failure is reported through the tool response payload.
			result := map[string]any{
				"recorded":        recorded > 0,
				"recorded_count":  recorded,
				"partial_success": recorded > 0,
				"failed_index":    recorded,
				"error":           "recall feedback submission failed",
			}
			for key, value := range recallFeedbackFailureGuidance(err) {
				result[key] = value
			}
			return result, nil
		}
		observability.RecordRecallFeedback(ctx, deps.Metrics, observability.RecallFeedback{
			Used:            submission.Used,
			AnswerSupported: submission.AnswerSupported,
			Quality:         submission.Quality,
			MissingContext:  submission.MissingContext,
			Irrelevant:      submission.Irrelevant,
		})
		recorded++
	}
	return map[string]any{
		"recorded":       recorded > 0,
		"recorded_count": recorded,
	}, nil
}

func recallFeedbackFailureGuidance(err error) map[string]any {
	guidance := map[string]any{
		"error_code":  "degraded",
		"reason_code": "feedback_persistence_failed",
		"next_action": "retry_same_request",
		"remediation": "Retry the same feedback request with unchanged items after the service recovers.",
	}
	switch {
	case errors.Is(err, appservice.ErrRecallFeedbackInvalidResultRef):
		guidance["error_code"] = "invalid_input"
		guidance["reason_code"] = "result_reference_invalid"
		guidance["next_action"] = "correct_and_resubmit"
		guidance["remediation"] = "Correct or remove the invalid irrelevant_result_refs or hypothesis_feedback references and resubmit with the current recall_event_id."
	case errors.Is(err, repository.ErrRecallFeedbackEventNotFound):
		guidance["error_code"] = "invalid_input"
		guidance["reason_code"] = "reference_not_found"
		guidance["next_action"] = "correct_and_resubmit"
		guidance["remediation"] = "Use a current recall_event_id and resubmit the corrected feedback items."
	case errors.Is(err, appservice.ErrRecallFeedbackInvalidInput):
		guidance["error_code"] = "invalid_input"
		guidance["reason_code"] = "invalid_feedback"
		guidance["next_action"] = "correct_and_resubmit"
		guidance["remediation"] = "Correct the feedback fields and resubmit the request."
	case errors.Is(err, context.Canceled):
		guidance["error_code"] = "degraded"
		guidance["reason_code"] = "request_cancelled"
		guidance["next_action"] = "stop"
		guidance["remediation"] = "Stop this feedback submission; retry only if the caller still needs it."
	case errors.Is(err, context.DeadlineExceeded):
		guidance["reason_code"] = "request_timeout"
		guidance["remediation"] = "Retry the same feedback request with unchanged items after the timeout clears."
	}
	return guidance
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

func recallFeedbackToolArgs(input map[string]any, req memoryservice.RecallRequest) map[string]any {
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

func recallFeedbackResultRefs(res *memoryservice.RecallResult) []domain.RecallFeedbackResultRef {
	if res == nil {
		return []domain.RecallFeedbackResultRef{}
	}
	refs := make([]domain.RecallFeedbackResultRef, 0, len(res.Results)*2+len(res.RelatedRelationships)+len(res.RelatedCommunities))
	seen := map[string]int{}
	appendRef := func(ref domain.RecallFeedbackResultRef) {
		key := ref.Type + "\x00" + ref.ID
		if ref.ID == "" || ref.Type == "" {
			return
		}
		if index, ok := seen[key]; ok {
			if ref.Rank > 0 && (refs[index].Rank <= 0 || ref.Rank < refs[index].Rank) {
				refs[index].Rank = ref.Rank
			}
			return
		}
		seen[key] = len(refs)
		refs = append(refs, ref)
	}
	for resultIndex, item := range res.Results {
		rank := item.Rank
		if rank <= 0 {
			rank = resultIndex + 1
		}
		if item.EvidenceID != "" {
			appendRef(domain.RecallFeedbackResultRef{
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
			appendRef(domain.RecallFeedbackResultRef{
				Type:           domain.RecallFeedbackResultTypeRelationship,
				ID:             relationshipID,
				Rank:           rank,
				Tier:           "relationship",
				StatusAtRecall: res.SearchState,
			})
		}
	}
	for _, community := range res.RelatedCommunities {
		appendRef(domain.RecallFeedbackResultRef{Type: domain.RecallFeedbackResultTypeCommunity, ID: community.CommunityID, Rank: community.Rank, StatusAtRecall: res.SearchState})
		for _, relationship := range community.CommunityRelationships {
			appendRef(domain.RecallFeedbackResultRef{Type: domain.RecallFeedbackResultTypeRelationship, ID: relationship.RelationshipID, Rank: community.Rank, StatusAtRecall: firstNonEmpty(relationship.SearchState, res.SearchState)})
		}
	}
	for rank, relationship := range res.RelatedRelationships {
		appendRef(domain.RecallFeedbackResultRef{Type: domain.RecallFeedbackResultTypeRelationship, ID: relationship.RelationshipID, Rank: rank + 1, StatusAtRecall: relationship.SearchState})
	}
	for rank, hypothesis := range res.RelatedHypotheses {
		appendRef(domain.RecallFeedbackResultRef{Type: domain.RecallFeedbackResultTypeHypothesis, ID: hypothesis.HypothesisID, Rank: rank + 1, StatusAtRecall: hypothesis.Status})
	}
	return refs
}
