package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/lifecycle"
)

func bindLifecycleTool(tool Tool, deps Dependencies) Tool {
	switch tool.Name {
	case ToolRetractEvidence:
		tool.Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
			if deps.Lifecycle == nil {
				return nil, ErrToolUnavailable
			}
			if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
				return nil, fmt.Errorf("retract_evidence: invalid input: %w", err)
			}
			var req lifecycle.RetractEvidenceRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("retract_evidence: invalid input: %w", err)
			}
			res, err := deps.Lifecycle.RetractEvidence(ctx, req)
			if err != nil {
				return nil, err
			}
			return structToMap(res)
		}
	case ToolCorrectRelationship:
		tool.Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
			if deps.Lifecycle == nil {
				return nil, ErrToolUnavailable
			}
			if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
				return nil, fmt.Errorf("correct_relationship: invalid input: %w", err)
			}
			var req lifecycle.CorrectRelationshipRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("correct_relationship: invalid input: %w", err)
			}
			res, err := deps.Lifecycle.CorrectRelationship(ctx, req)
			if err != nil {
				submissionID := ""
				if req.Action == "confirm" {
					submissionID = req.SubmissionID
				}
				return nil, correctionToolResultError(ctx, submissionID, err)
			}
			result, err := structToMap(res)
			if err != nil {
				return nil, err
			}
			if state := strings.TrimSpace(fmt.Sprint(result["processing_state"])); state == "rejected" || state == "failed" {
				return nil, NewToolResultError(result)
			}
			return result, nil
		}
	}
	return tool
}
