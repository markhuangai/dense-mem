package remember

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// ResultKind tells transitional consumers whether a result is a legacy queued
// receipt or a terminal synchronous outcome. It is not serialized directly.
type ResultKind string

const (
	ResultKindLegacyReceipt ResultKind = "legacy_receipt"
	ResultKindTerminal      ResultKind = "terminal"
)

// SynchronousProcessor is the request-owned boundary used by the future
// terminal Remember path. The current release service keeps using IntakePort.
type SynchronousProcessor interface {
	ProcessRemember(context.Context, RememberProcessRequest) (*SubmissionStatusResult, error)
}

// Processor is the narrow owner-scoped execution port retained for consumers
// that already have a staged submission identifier.
type Processor interface {
	Process(context.Context, ProcessRequest) (*SubmissionStatusResult, error)
}

type ProcessRequest struct {
	TeamID         string
	OwnerProfileID string
	SubmissionID   string
}

// RememberProcessError preserves a bounded terminal result alongside an
// operational cause, allowing transports to return correlated structured
// errors without inventing a submission identifier.
type RememberProcessError struct {
	Status *SubmissionStatusResult
	Result *TerminalRememberResult
	Err    error
}

func (e *RememberProcessError) Error() string {
	return "remember: processor failed"
}

func (e *RememberProcessError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type RememberProcessRequest struct {
	TeamID                   string
	OwnerProfileID           string
	SpaceID                  string
	SpaceGeneration          int64
	IdempotencyKey           string
	RequestHash              string
	MigratedRequestHash      string
	SourceSummary            string
	Proposal                 map[string]any
	Metadata                 map[string]any
	Evidence                 []EvidenceInput
	SecuritySignals          []SubmissionSecurityBatchSignal
	SecuritySignalsTruncated bool
	SecurityRejected         bool
}

type TerminalRememberResult struct {
	ContractVersion     string                         `json:"contract_version"`
	SubmissionID        string                         `json:"submission_id"`
	SubmissionKind      string                         `json:"submission_kind"`
	ProcessingState     string                         `json:"processing_state"`
	SearchState         string                         `json:"search_state"`
	CorrelationID       string                         `json:"correlation_id"`
	Evidence            []TerminalEvidenceResult       `json:"evidence"`
	RelationshipResults []SubmissionRelationshipResult `json:"relationship_results"`
	Errors              []SubmissionStatusError        `json:"errors"`
	Kind                ResultKind                     `json:"-"`
}

type TerminalEvidenceResult struct {
	Disposition           string   `json:"disposition"`
	EvidenceID            string   `json:"evidence_id,omitempty"`
	EvidenceIndex         int      `json:"evidence_index"`
	SupersededEvidenceIDs []string `json:"superseded_evidence_ids"`
	SearchState           string   `json:"search_state"`
	Reason                string   `json:"reason,omitempty"`
}

type TerminalProcessingState string

const (
	TerminalProcessingCompleted   TerminalProcessingState = "completed"
	TerminalProcessingRejected    TerminalProcessingState = "rejected"
	TerminalProcessingQuarantined TerminalProcessingState = "quarantined"
	TerminalProcessingFailed      TerminalProcessingState = "failed"
)

type TerminalSearchState string

const (
	TerminalSearchCurrent     TerminalSearchState = "current"
	TerminalSearchNotRequired TerminalSearchState = "not_required"
)

type TerminalErrorCode string

const (
	TerminalErrorNoSupportedMemory        TerminalErrorCode = "no_supported_memory"
	TerminalErrorStaleInput               TerminalErrorCode = "stale_input"
	TerminalErrorProviderUnavailable      TerminalErrorCode = "provider_unavailable"
	TerminalErrorProviderResponseInvalid  TerminalErrorCode = "provider_response_invalid"
	TerminalErrorInputBudgetExceeded      TerminalErrorCode = "input_budget_exceeded"
	TerminalErrorConfigurationInvalid     TerminalErrorCode = "configuration_invalid"
	TerminalErrorIdempotencyConflict      TerminalErrorCode = "idempotency_conflict"
	TerminalErrorEmbeddingUnavailable     TerminalErrorCode = "embedding_unavailable"
	TerminalErrorEmbeddingResponseInvalid TerminalErrorCode = "embedding_response_invalid"
	TerminalErrorCommitConflict           TerminalErrorCode = "commit_conflict"
	TerminalErrorDatabaseFailure          TerminalErrorCode = "database_failure"
	TerminalErrorRequestTimeout           TerminalErrorCode = "request_timeout"
	TerminalErrorRequestCancelled         TerminalErrorCode = "request_cancelled"
	TerminalErrorInternalFailure          TerminalErrorCode = "internal_failure"
	TerminalErrorQuarantined              TerminalErrorCode = "submission_quarantined"
)

var terminalErrorCodes = []TerminalErrorCode{
	TerminalErrorNoSupportedMemory,
	TerminalErrorStaleInput,
	TerminalErrorProviderUnavailable,
	TerminalErrorProviderResponseInvalid,
	TerminalErrorInputBudgetExceeded,
	TerminalErrorConfigurationInvalid,
	TerminalErrorIdempotencyConflict,
	TerminalErrorEmbeddingUnavailable,
	TerminalErrorEmbeddingResponseInvalid,
	TerminalErrorCommitConflict,
	TerminalErrorDatabaseFailure,
	TerminalErrorRequestTimeout,
	TerminalErrorRequestCancelled,
	TerminalErrorInternalFailure,
	TerminalErrorQuarantined,
}

func TerminalErrorCodes() []string {
	result := make([]string, 0, len(terminalErrorCodes))
	for _, code := range terminalErrorCodes {
		result = append(result, string(code))
	}
	return result
}

type TerminalNextAction string

const (
	TerminalNextActionRetrySameRequest TerminalNextAction = "retry_same_request"
	TerminalNextActionResubmitRemember TerminalNextAction = "resubmit_remember"
	TerminalNextActionRetryCorrection  TerminalNextAction = "retry_correction"
	TerminalNextActionContactOperator  TerminalNextAction = "contact_operator"
	TerminalNextActionNone             TerminalNextAction = "none"
)

var terminalNextActions = []TerminalNextAction{
	TerminalNextActionRetrySameRequest,
	TerminalNextActionResubmitRemember,
	TerminalNextActionRetryCorrection,
	TerminalNextActionContactOperator,
	TerminalNextActionNone,
}

func TerminalNextActions() []string {
	result := make([]string, 0, len(terminalNextActions))
	for _, action := range terminalNextActions {
		result = append(result, string(action))
	}
	return result
}

func TerminalStatusError(code TerminalErrorCode) SubmissionStatusError {
	code = normalizeTerminalErrorCode(code)
	retryable, action := terminalErrorGuidance(code)
	return SubmissionStatusError{
		Code:        string(code),
		Message:     terminalErrorMessage(code),
		Retryable:   retryable,
		NextAction:  string(action),
		Remediation: terminalErrorRemediation(action),
	}
}

func normalizeTerminalErrorCode(code TerminalErrorCode) TerminalErrorCode {
	for _, known := range terminalErrorCodes {
		if code == known {
			return code
		}
	}
	return TerminalErrorInternalFailure
}

// IsTerminalErrorCode reports whether code belongs to the closed terminal
// Remember error vocabulary.
func IsTerminalErrorCode(code TerminalErrorCode) bool {
	return normalizeTerminalErrorCode(code) == code
}

// IsTerminalNextAction reports whether action belongs to the closed terminal
// Remember remediation vocabulary.
func IsTerminalNextAction(action TerminalNextAction) bool {
	for _, known := range terminalNextActions {
		if action == known {
			return true
		}
	}
	return false
}

// ValidateTerminalStatusError checks the public code, action, and bounded
// message fields before they cross a synchronous transport boundary.
func ValidateTerminalStatusError(value SubmissionStatusError) error {
	code := TerminalErrorCode(strings.TrimSpace(value.Code))
	if value.Code != string(code) || !IsTerminalErrorCode(code) {
		return fmt.Errorf("remember: terminal error code %q is not allowed", value.Code)
	}
	if !IsTerminalNextAction(TerminalNextAction(value.NextAction)) {
		return fmt.Errorf("remember: terminal next action %q is not allowed", value.NextAction)
	}
	retryable, action := terminalErrorGuidance(code)
	if value.Retryable != retryable || value.NextAction != string(action) {
		return fmt.Errorf("remember: terminal error guidance for %q is inconsistent", code)
	}
	if strings.TrimSpace(value.Message) == "" || len(value.Message) > 256 {
		return errors.New("remember: terminal error message is missing or too long")
	}
	if strings.TrimSpace(value.Remediation) == "" || len(value.Remediation) > 512 {
		return errors.New("remember: terminal error remediation is missing or too long")
	}
	return nil
}

func terminalErrorGuidance(code TerminalErrorCode) (bool, TerminalNextAction) {
	switch code {
	case TerminalErrorProviderUnavailable, TerminalErrorProviderResponseInvalid,
		TerminalErrorEmbeddingUnavailable, TerminalErrorEmbeddingResponseInvalid,
		TerminalErrorCommitConflict, TerminalErrorDatabaseFailure,
		TerminalErrorRequestTimeout, TerminalErrorRequestCancelled,
		TerminalErrorInternalFailure:
		return true, TerminalNextActionRetrySameRequest
	case TerminalErrorNoSupportedMemory, TerminalErrorStaleInput,
		TerminalErrorIdempotencyConflict, TerminalErrorQuarantined:
		return true, TerminalNextActionResubmitRemember
	case TerminalErrorInputBudgetExceeded:
		return true, TerminalNextActionResubmitRemember
	case TerminalErrorConfigurationInvalid:
		return false, TerminalNextActionContactOperator
	default:
		return false, TerminalNextActionContactOperator
	}
}

func terminalErrorMessage(code TerminalErrorCode) string {
	switch code {
	case TerminalErrorNoSupportedMemory:
		return "no supported memory could be stored from this submission"
	case TerminalErrorStaleInput:
		return "an exact client-owned input changed before commit"
	case TerminalErrorProviderUnavailable:
		return "the semantic assessor was unavailable"
	case TerminalErrorProviderResponseInvalid:
		return "the semantic assessor returned an invalid response"
	case TerminalErrorInputBudgetExceeded:
		return "the semantic assessor input exceeded the configured budget"
	case TerminalErrorConfigurationInvalid:
		return "Dense-Mem provider or search configuration is invalid"
	case TerminalErrorIdempotencyConflict:
		return "the idempotency key is already bound to a different request"
	case TerminalErrorEmbeddingUnavailable:
		return "the embedding provider was unavailable"
	case TerminalErrorEmbeddingResponseInvalid:
		return "the embedding provider returned an invalid response"
	case TerminalErrorCommitConflict:
		return "server-owned state changed before commit"
	case TerminalErrorDatabaseFailure:
		return "Dense-Mem could not persist the submission"
	case TerminalErrorRequestTimeout:
		return "the bounded Remember request deadline was reached"
	case TerminalErrorRequestCancelled:
		return "the Remember request was cancelled before commit"
	case TerminalErrorQuarantined:
		return "the submission was quarantined by security policy"
	default:
		return "Dense-Mem could not complete the submission"
	}
}

func terminalErrorRemediation(action TerminalNextAction) string {
	switch action {
	case TerminalNextActionRetrySameRequest:
		return "Retry the same request with the same idempotency_key after the transient failure clears."
	case TerminalNextActionResubmitRemember:
		return "Submit the complete batch again with remember and a new idempotency_key after correcting the input."
	case TerminalNextActionRetryCorrection:
		return "Retry correct_relationship with current relationship state and a new idempotency_key."
	case TerminalNextActionNone:
		return "No action is required."
	default:
		return "Contact an operator with submission_id and correlation_id."
	}
}

func ValidateTerminalRememberResult(result *TerminalRememberResult, evidenceCount int, relationshipRefs []string) error {
	if result == nil {
		return errors.New("remember: terminal result is required")
	}
	if result.Kind != ResultKindTerminal {
		return fmt.Errorf("remember: result kind %q is not terminal", result.Kind)
	}
	if result.ContractVersion != domain.ContractVersion || result.SubmissionKind != "remember" {
		return fmt.Errorf("remember: terminal result identity is invalid")
	}
	if strings.TrimSpace(result.SubmissionID) == "" || strings.TrimSpace(result.CorrelationID) == "" {
		return errors.New("remember: terminal result identity fields are required")
	}
	if result.ProcessingState != string(TerminalProcessingCompleted) &&
		result.ProcessingState != string(TerminalProcessingRejected) &&
		result.ProcessingState != string(TerminalProcessingQuarantined) &&
		result.ProcessingState != string(TerminalProcessingFailed) {
		return fmt.Errorf("remember: invalid terminal processing state %q", result.ProcessingState)
	}
	if result.SearchState != string(TerminalSearchCurrent) && result.SearchState != string(TerminalSearchNotRequired) {
		return fmt.Errorf("remember: invalid terminal search state %q", result.SearchState)
	}
	if result.Errors == nil {
		return errors.New("remember: terminal errors are required")
	}
	if len(result.Evidence) != evidenceCount {
		return fmt.Errorf("remember: terminal evidence count %d, expected %d", len(result.Evidence), evidenceCount)
	}
	storedResult := false
	for index, item := range result.Evidence {
		if item.EvidenceIndex != index {
			return fmt.Errorf("remember: terminal evidence index %d is out of order", item.EvidenceIndex)
		}
		if item.Disposition != "stored" && item.Disposition != "not_stored" {
			return fmt.Errorf("remember: terminal evidence disposition %q is invalid", item.Disposition)
		}
		if item.Disposition == "stored" && item.EvidenceID == "" {
			return fmt.Errorf("remember: stored evidence %d has no evidence_id", index)
		}
		if item.Disposition == "stored" {
			if _, err := uuid.Parse(item.EvidenceID); err != nil {
				return fmt.Errorf("remember: stored evidence %d has invalid evidence_id", index)
			}
			storedResult = true
		}
		if item.Disposition == "not_stored" && item.EvidenceID != "" {
			return fmt.Errorf("remember: non-stored evidence %d has an evidence_id", index)
		}
		if item.SearchState != string(TerminalSearchCurrent) && item.SearchState != string(TerminalSearchNotRequired) {
			return fmt.Errorf("remember: terminal evidence %d has invalid search state %q", index, item.SearchState)
		}
	}
	for index, item := range result.Errors {
		if err := ValidateTerminalStatusError(item); err != nil {
			return fmt.Errorf("remember: terminal error %d: %w", index, err)
		}
	}
	if len(result.RelationshipResults) != len(relationshipRefs) {
		return fmt.Errorf("remember: terminal relationship count %d, expected %d", len(result.RelationshipResults), len(relationshipRefs))
	}
	seenRefs := make(map[string]struct{}, len(relationshipRefs))
	for index, item := range result.RelationshipResults {
		if item.RelationshipRef != relationshipRefs[index] {
			return fmt.Errorf("remember: terminal relationship ref %q is out of order", item.RelationshipRef)
		}
		if _, ok := seenRefs[item.RelationshipRef]; ok {
			return fmt.Errorf("remember: terminal relationship ref %q is duplicated", item.RelationshipRef)
		}
		seenRefs[item.RelationshipRef] = struct{}{}
		if item.Disposition != "stored" && item.Disposition != "not_stored" {
			return fmt.Errorf("remember: terminal relationship disposition %q is invalid", item.Disposition)
		}
		if item.Disposition == "not_stored" && len(item.Splits) > 0 {
			return fmt.Errorf("remember: non-stored relationship %q has splits", item.RelationshipRef)
		}
		if item.Disposition == "not_stored" && !terminalRelationshipNotStoredReasonAllowed(item.Reason) {
			return fmt.Errorf("remember: non-stored relationship %q has unsupported reason", item.RelationshipRef)
		}
		if item.Disposition == "stored" {
			if len(item.Splits) == 0 {
				return fmt.Errorf("remember: stored relationship %q has no split", item.RelationshipRef)
			}
			if strings.TrimSpace(item.Reason) != "" {
				return fmt.Errorf("remember: stored relationship %q has a reason", item.RelationshipRef)
			}
			for splitIndex, split := range item.Splits {
				if split.SplitIndex != splitIndex {
					return fmt.Errorf("remember: stored relationship %q split index %d is out of order", item.RelationshipRef, split.SplitIndex)
				}
				if _, err := uuid.Parse(split.RelationshipID); err != nil {
					return fmt.Errorf("remember: stored relationship %q split has invalid relationship_id", item.RelationshipRef)
				}
				if split.RelationshipVersion < 1 || split.Status != "active" {
					return fmt.Errorf("remember: stored relationship %q split is not active and versioned", item.RelationshipRef)
				}
			}
			storedResult = true
		}
	}
	switch result.ProcessingState {
	case string(TerminalProcessingCompleted):
		if len(result.Errors) > 0 {
			return errors.New("remember: completed terminal result cannot contain errors")
		}
		if !storedResult {
			return errors.New("remember: completed terminal result has no stored result")
		}
	case string(TerminalProcessingRejected), string(TerminalProcessingQuarantined), string(TerminalProcessingFailed):
		if len(result.Errors) == 0 {
			return fmt.Errorf("remember: %s terminal result requires an error", result.ProcessingState)
		}
		if storedResult {
			return fmt.Errorf("remember: %s terminal result cannot contain stored results", result.ProcessingState)
		}
	}
	return nil
}

func terminalRelationshipNotStoredReasonAllowed(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "not_supported_by_evidence", "stale_input", "security_quarantine", "internal_failure":
		return true
	default:
		return false
	}
}

// TerminalResultWithError builds a bounded failure projection for transport
// tests and future processor adapters without exposing provider/database text.
func TerminalResultWithError(result *TerminalRememberResult, code TerminalErrorCode) *RememberProcessError {
	if result == nil {
		result = &TerminalRememberResult{Kind: ResultKindTerminal}
	}
	result.Kind = ResultKindTerminal
	result.ProcessingState = string(TerminalProcessingFailed)
	if result.SearchState != string(TerminalSearchCurrent) && result.SearchState != string(TerminalSearchNotRequired) {
		result.SearchState = string(TerminalSearchNotRequired)
	}
	if result.Evidence == nil {
		result.Evidence = []TerminalEvidenceResult{}
	}
	if result.RelationshipResults == nil {
		result.RelationshipResults = []SubmissionRelationshipResult{}
	}
	status := TerminalStatusError(code)
	result.Errors = []SubmissionStatusError{status}
	return &RememberProcessError{Result: result, Err: errors.New(status.Message)}
}
