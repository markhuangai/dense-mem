package registry

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/recall"
)

func bindRecallTool(tool Tool, deps Dependencies) Tool {
	if tool.Name != ToolRecallMemory {
		return tool
	}
	tool.Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
		if deps.Recall == nil {
			return nil, ErrToolUnavailable
		}
		if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
			return nil, fmt.Errorf("recall_memory: invalid input: %w", err)
		}
		var req recall.RecallRequest
		if err := remapInput(input, &req); err != nil {
			return nil, fmt.Errorf("recall_memory: invalid input: %w", err)
		}
		dreamingEnabled := DreamingEnabled(ctx, deps.RecallDreaming)
		req.IncludeHypotheses = dreamingEnabled
		res, err := deps.Recall.Recall(ctx, req)
		if err != nil {
			return nil, err
		}
		if res != nil && !dreamingEnabled {
			res.RelatedHypotheses = []recall.RelatedHypothesisSummary{}
		}
		feedbackSnapshotStored := recordRecallFeedbackSnapshot(ctx, deps, input, req, res)
		setRecallSuggestedActions(res, feedbackSnapshotStored, dreamingEnabled)
		return recallContractOutput(res), nil
	}
	return tool
}

func bindRecallFeedbackTool(tool Tool, deps Dependencies) Tool {
	if tool.Name != ToolSubmitRecallSessionFeedback {
		return tool
	}
	tool.Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
		if deps.RecallFeedbackEvents == nil {
			return nil, ErrToolUnavailable
		}
		if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
			return nil, fmt.Errorf("submit_recall_session_feedback: invalid input: %w", err)
		}
		return submitRecallFeedback(ctx, deps, input)
	}
	return tool
}
