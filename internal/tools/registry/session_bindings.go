package registry

import (
	"context"
	"errors"

	session "github.com/markhuangai/dense-mem/internal/session/contract"
)

type SessionBindings struct{ Service session.API }

func bindSessionTool(tool Tool, deps Dependencies) Tool {
	if tool.Name != ToolIngestSession {
		return tool
	}
	if deps.SessionBindings.Service != nil {
		tool.Available = deps.SessionBindings.Service.Available
	}
	tool.Invoke = func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
		if deps.SessionBindings.Service == nil || !deps.SessionBindings.Service.Available(ctx) {
			return nil, ErrToolDisabled
		}
		if err := ValidateContractInput(tool, input, authenticatedScopes(ctx)); err != nil {
			return nil, err
		}
		var req session.Request
		if err := remapInput(input, &req); err != nil {
			return nil, err
		}
		result, err := deps.SessionBindings.Service.Ingest(ctx, req)
		if result != nil {
			output, mapErr := structToMap(result)
			if mapErr != nil {
				return nil, mapErr
			}
			if err != nil || result.ProcessingState == "failed" {
				return nil, NewToolResultError(output)
			}
			return output, nil
		}
		if err == nil {
			return nil, ErrToolUnavailable
		}
		reason, message, remediation := "session_input_invalid", "The session request is invalid.", "Correct the request using the discovered schema and submit the deliberate new operation."
		switch {
		case errors.Is(err, session.ErrEventConflict):
			reason = "session_event_conflict"
			message = "An immutable event ID has different text or occurrence metadata."
			remediation = "Retain the original event unchanged. A distinct user event needs a distinct event_id."
		case errors.Is(err, session.ErrRequestConflict):
			reason = "idempotency_conflict"
			message = "The retained operation key belongs to a different complete request."
			remediation = "Retry the unchanged original request with its retained key. Do not rotate keys to bypass this conflict."
		case errors.Is(err, session.ErrOriginalRequestRequired):
			reason = "session_event_retry_required"
			message = "An event belongs to an unfinished original request."
			remediation = "Resend that original complete request with its retained idempotency_key before submitting overlapping events."
		case errors.Is(err, session.ErrBudget):
			reason = "input_budget_exceeded"
			message = "The complete request exceeds a server processing bound."
			remediation = "Retain original events and submit a smaller event batch within the discovered processing limits."
		case errors.Is(err, session.ErrSecurity):
			reason = "submission_policy_rejected"
			message = "The user content was rejected by evidence security policy."
			remediation = "Do not retry rejected content unchanged."
		case errors.Is(err, session.ErrUnauthorized), errors.Is(err, session.ErrStale):
			return nil, NewToolResultError(ActionableAuthorizationData(ctx, ToolIngestSession))
		default:
			if !errors.Is(err, session.ErrInvalidInput) {
				return nil, NewToolResultError(ActionableErrorData(ctx, ToolIngestSession, err))
			}
		}
		return nil, NewToolResultError(ActionableInvalidInputData(ctx, ToolIngestSession, reason, message, remediation))
	}
	return tool
}
