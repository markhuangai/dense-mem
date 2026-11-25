package processor

import (
	"context"
	"errors"
	"github.com/markhuangai/dense-mem/internal/observability"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func rememberAttemptStatusForRequest(
	attempt *repository.RememberAttempt,
	input rememberapp.RememberProcessRequest,
) (*rememberapp.SubmissionStatusResult, error) {
	status, err := rememberAttemptStatus(attempt)
	if err != nil {
		return nil, err
	}
	return status, nil
}

func rememberAttemptReplay(attempt *repository.RememberAttempt, input rememberapp.RememberProcessRequest) (*rememberapp.SubmissionStatusResult, error) {
	if attempt == nil {
		return nil, rememberConflictProcessError(input, "", rememberapp.ErrRememberConflict)
	}
	if !domain.ContractVersionCompatible(strings.TrimSpace(attempt.ContractVersion)) {
		return nil, retiredRememberContractError(input, attempt)
	}
	if !rememberAttemptMatchesRequest(attempt, input) {
		return nil, rememberConflictProcessError(input, attempt.AttemptID, rememberapp.ErrRememberConflict)
	}
	status, err := rememberAttemptStatusForRequest(attempt, input)
	if err != nil {
		return nil, err
	}
	switch strings.TrimSpace(attempt.Outcome) {
	case "completed":
		if strings.TrimSpace(status.ProcessingState) == "completed" {
			return status, nil
		}
	case "failed":
		if strings.TrimSpace(status.ProcessingState) == "failed" {
			return nil, &rememberapp.RememberProcessError{Status: status, Err: rememberapp.ErrRememberPersistence}
		}
	}
	// Historical rejected, quarantined, and replayed rows are retained for
	// audit, but they are not a supported Remember result and must never be
	// replayed or used to authorize a retry.
	return nil, rememberConflictProcessError(input, attempt.AttemptID, rememberapp.ErrRememberConflict)
}

func retiredRememberContractError(input rememberapp.RememberProcessRequest, attempt *repository.RememberAttempt) *rememberapp.RememberProcessError {
	result := rememberConflictProcessError(input, attempt.AttemptID, errors.Join(rememberapp.ErrRememberConflict, rememberapp.ErrRememberRetiredContract))
	result.Status.Errors = []rememberapp.SubmissionStatusError{rememberapp.TerminalStatusErrorWithDetails(rememberapp.TerminalErrorRetiredContract, "retired_contract", map[string]any{"component": "remember.idempotency", "original_contract": attempt.ContractVersion, "original_outcome": attempt.Outcome, "payload_retired": true})}
	return result
}

func (p *rememberSynchronousProcessor) retiredRememberContract(ctx context.Context, input rememberapp.RememberProcessRequest, attempt *repository.RememberAttempt) *rememberapp.RememberProcessError {
	if p.logger != nil {
		attrs := []observability.LogAttr{
			observability.String("team_id", input.TeamID), observability.String("owner_profile_id", input.OwnerProfileID),
			observability.String("attempt_id", attempt.AttemptID), observability.String("idempotency_key", input.IdempotencyKey),
			observability.String("original_request_hash", attempt.RequestHash), observability.String("original_contract", attempt.ContractVersion),
			observability.String("original_outcome", attempt.Outcome), observability.String("contract_version", domain.ContractVersion),
			observability.String("reason", "factual_graph_removed"), observability.String("outcome", "retired_contract"),
		}
		if logger, ok := p.logger.(observability.ContextLogProvider); ok {
			logger.WarnContext(ctx, "memory_contract_retired", attrs...)
		} else {
			p.logger.Warn("memory_contract_retired", attrs...)
		}
	}
	return retiredRememberContractError(input, attempt)
}
