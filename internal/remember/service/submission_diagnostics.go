package service

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/markhuangai/dense-mem/internal/assessor"
	embeddingcontract "github.com/markhuangai/dense-mem/internal/embedding/contract"
	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

const maxSubmissionDiagnosticIssues = 20

var diagnosticInputPath = regexp.MustCompile(`^/(?:evidence(?:/[0-9]+(?:/(?:content|source_key|source_revision|previous_source_revision))?)?|relationships(?:/[0-9]+(?:/(?:client_comment|ref|subject|object|predicate|known_evidence_ids|correction_target|conflict_context|valid_from|valid_to)(?:/[a-z_]+)*)?)?)$`)

var securityDiagnosticMessages = map[string]string{
	"role_control_spoofing":    "The security check detected text presented as a system or developer instruction.",
	"instruction_override":     "The security check detected instructions to override the service's existing instructions.",
	"prompt_secret_extraction": "The security check detected a request to reveal protected instructions or authentication material.",
	"tool_exfiltration":        "The security check detected instructions to send protected information through a tool or network request.",
	"obfuscated_instruction":   "The security check detected encoded or disguised instructions rather than readable evidence.",
	"hidden_control_markup":    "The security check detected hidden control characters or executable markup in the submitted text.",
	"encoded_payload":          "The security check detected an encoded payload; this intake requires readable evidence.",
}

func submissionErrorRemediationForCode(code SubmissionErrorCode, action SubmissionNextAction) string {
	switch code {
	case SubmissionErrorRequestCancelled:
		return "If you still need this operation, retry the exact original request with the same idempotency_key to obtain or complete its authoritative outcome."
	case SubmissionErrorRequestTimeout, SubmissionErrorDatabaseFailure:
		return "Retry the exact original request with the same idempotency_key to confirm its final stored outcome. If the failure persists, contact an operator with submission_id and correlation_id."
	case SubmissionErrorPolicyRejected:
		return "Submit readable evidence without commands directed at the service, using a new idempotency_key. Preserve the factual meaning. If you dispute the security classification, contact an operator with submission_id and correlation_id."
	case SubmissionErrorStaleInput:
		return "Refresh the authorized source revisions and exact references, check that the intended change still applies, then submit the complete updated request with a new idempotency_key."
	case SubmissionErrorIdempotencyConflict:
		return "Reuse this idempotency_key only for the original unchanged request. Submit a deliberately changed request with a new idempotency_key."
	case SubmissionErrorInputBudgetExceeded:
		return "Reduce or split only the identified caller-controlled input while preserving its meaning, then submit with a new idempotency_key. If the excess is server-owned context, contact an operator."
	case SubmissionErrorRelationshipVersionStale, SubmissionErrorRelationshipChanged:
		return "Read the current relationship state, check that your correction still applies, then call correct_relationship with the current version and a new idempotency_key."
	case SubmissionErrorSupportSetMismatch, SubmissionErrorSupportSetChanged:
		return "Trace the relationship, retain only effective granted or reinstated evidence supports, and supply their exact evidence IDs and spans with a new idempotency_key."
	case SubmissionErrorConfirmationExpired, SubmissionErrorPersistentAmbiguity:
		return "Start correct_relationship again with refreshed state and a new idempotency_key, then use the newly returned candidate or confirmation before it expires."
	case SubmissionErrorPredicateNotFound, SubmissionErrorPredicateSubjectKindMismatch, SubmissionErrorPredicateObjectKindMismatch:
		return "Refresh the authorized predicate catalog and choose an active predicate whose subject and object kinds permit this correction; resubmit with a new idempotency_key."
	case SubmissionErrorEntityNotFound, SubmissionErrorTooManyEntityCandidates:
		return "Refresh authorized Entity candidates and select an available exact Entity identity before retrying correct_relationship with a new idempotency_key."
	case SubmissionErrorRelationshipNotActive, SubmissionErrorInactiveRelationshipCollision:
		return "Trace the current relationship and its lifecycle state. Select an eligible active relationship or ask the operator to review the historical collision before starting a new correction."
	case SubmissionErrorObjectKindChangeForbidden:
		return "Keep the corrected object a typed Value. A change from Value to Entity requires a separately supported operation; contact an operator if that change is intended."
	default:
		return submissionErrorRemediation(action)
	}
}

func applySubmissionDiagnosticGuidance(result *SubmissionStatusError) {
	if result == nil {
		return
	}
	if issues, _ := boundedSubmissionDiagnosticIssues(result.Details["issues"]); len(issues) > 0 {
		first := issues[0].(map[string]any)
		result.Message = boundedStatusErrorText(fmt.Sprintf("%s Input location: %s.", first["message"], first["path"]), 512)
		if result.Code == string(SubmissionErrorPolicyRejected) {
			result.Message = boundedStatusErrorText(result.Message+" This batch was not accepted as memory.", 512)
		}
	}
	switch result.ReasonCode {
	case "security_rejection":
		issues, _ := boundedSubmissionDiagnosticIssues(result.Details["issues"])
		if len(issues) == 0 {
			result.Message = "A security check rejected this batch. The available result does not retain its specific category or affected input location."
		}
	case "legacy_policy_rejection", "remember_assessment_failed":
		issues, _ := boundedSubmissionDiagnosticIssues(result.Details["issues"])
		if result.Code == string(SubmissionErrorPolicyRejected) && len(issues) == 0 {
			result.Message = "The saved result records that this batch was rejected by policy. Its specific cause and affected input location were not retained."
			result.Remediation = "Review the original evidence and proposals without changing their factual meaning. If the missing detail prevents correction, contact an operator with submission_id and correlation_id. Submit a deliberately changed request with a new idempotency_key."
		}
	case "legacy_details_unavailable":
		result.Message = boundedStatusErrorText(result.Message+" More specific cause and input-location details were not retained in this result.", 512)
	case "provider_rate_limited":
		result.Message = "The required provider limited the service's request rate. This is a service limit, not a judgment that your evidence is false."
	case "provider_timeout":
		result.Message = "The required provider did not respond within the allowed time, so processing could not finish."
	case "provider_server_failure":
		result.Message = "The required provider reported a server failure while processing this operation."
	case "provider_transport_failure":
		result.Message = "The connection to the required provider failed before a usable response was received."
	case "provider_request_rejected":
		result.Message = "The required provider rejected the server's request. Ask an operator to review provider configuration if retrying the unchanged request does not resolve it."
	case "embedding_rate_limited":
		result.Message = "The embedding service limited the service's request rate, so it could not produce all required vectors."
	case "embedding_timeout":
		result.Message = "The embedding service did not produce all required vectors within the allowed time."
	case "embedding_server_failure":
		result.Message = "The embedding service reported a server failure before all required vectors were available."
	case "embedding_transport_failure":
		result.Message = "The connection to the embedding service failed before all required vectors were available."
	case "embedding_request_rejected":
		result.Message = "The embedding service rejected the server's request. Ask an operator to review provider configuration."
	}
	if result.Code == string(SubmissionErrorInputBudgetExceeded) && len(result.Details) > 0 {
		if observed, ok := result.Details["observed"]; ok && result.Details["limit"] != nil {
			result.Message = boundedStatusErrorText(fmt.Sprintf("The %s processing limit was exceeded: observed %v %s, allowed %v. Input ownership: %s.", result.Details["component"], observed, result.Details["unit"], result.Details["limit"], diagnosticInputOwner(result.Details)), 512)
		} else {
			result.Message = fmt.Sprintf("An assessment processing limit was exceeded for %s. The available result does not retain observed or allowed counts.", diagnosticInputOwner(result.Details))
		}
	}
	if result.Code != string(SubmissionErrorPolicyRejected) && genericRememberFailureDetailsUnavailable(*result) {
		result.Message = boundedStatusErrorText(result.Message+" More specific cause and affected input-location details were not retained in this result.", 512)
	}
}

func genericRememberFailureDetailsUnavailable(value SubmissionStatusError) bool {
	switch value.ReasonCode {
	case "remember_assessment_failed", "remember_embedding_failed", "remember_commit_failed":
		for key := range value.Details {
			if key != "component" && key != "server_owned" {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func diagnosticInputOwner(details map[string]any) string {
	if statusErrorServerOwned(details) {
		return "server-owned context"
	}
	return "submitted evidence or proposals"
}

func boundedSubmissionDiagnosticIssues(value any) ([]any, bool) {
	var input []any
	switch value := value.(type) {
	case []any:
		input = value
	case []map[string]any:
		for _, entry := range value {
			input = append(input, entry)
		}
	}
	result := make([]any, 0, min(len(input), maxSubmissionDiagnosticIssues))
	truncated := len(input) > maxSubmissionDiagnosticIssues
	for _, raw := range input {
		if len(result) >= maxSubmissionDiagnosticIssues {
			break
		}
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		path, _ := entry["path"].(string)
		code, _ := entry["code"].(string)
		message := submissionDiagnosticIssueMessage(code)
		if !diagnosticInputPath.MatchString(path) || len(path) > 256 || message == "" {
			continue
		}
		projected := map[string]any{"path": path, "code": code, "message": message}
		start, startOK := diagnosticSpan(entry["span_start"])
		end, endOK := diagnosticSpan(entry["span_end"])
		if startOK && endOK && end > start {
			projected["span_start"], projected["span_end"] = entry["span_start"], entry["span_end"]
		}
		result = append(result, projected)
	}
	return result, truncated
}

func diagnosticSpan(value any) (int, bool) {
	switch value := value.(type) {
	case int:
		return value, value >= 0 && value <= 100000
	case float64:
		return int(value), value >= 0 && value <= 100000 && value == float64(int(value))
	}
	return 0, false
}

func submissionDiagnosticIssueMessage(code string) string {
	if message := securityDiagnosticMessages[code]; message != "" {
		return message
	}
	switch code {
	case "source_revision_changed":
		return "A submitted source revision changed or is unavailable in your authorized context."
	case "known_evidence_changed":
		return "Referenced supporting evidence changed or is no longer available in your authorized context."
	case "exact_reference_changed":
		return "The submitted exact Entity or relationship reference could not be confirmed in your authorized context."
	case "correction_target_changed":
		return "The relationship selected for correction changed before the correction could be applied."
	case "conflict_target_changed":
		return "The conflict state selected by this request changed before the operation could be applied."
	}
	return ""
}

type submissionDiagnosticFailure struct {
	cause   error
	reason  string
	details map[string]any
}

func (e *submissionDiagnosticFailure) Error() string { return e.cause.Error() }
func (e *submissionDiagnosticFailure) Unwrap() error { return e.cause }

func SecurityRejectionFailure(signals []SubmissionSecurityBatchSignal, truncated bool, stage string) error {
	issues := make([]any, 0, len(signals))
	for _, signal := range signals {
		path := signal.Path
		if path == "" {
			path = "/relationships"
			if signal.Source == SecuritySourceEvidence {
				path = fmt.Sprintf("/evidence/%d/content", signal.EvidenceIndex)
			}
		}
		kind := signal.Kind
		if signal.Encoded {
			kind = "encoded_payload"
		}
		issues = append(issues, map[string]any{"path": path, "code": kind, "span_start": signal.Start, "span_end": signal.End})
	}
	return &submissionDiagnosticFailure{cause: ErrRememberPolicyRejected, reason: "security_rejection", details: map[string]any{
		"component": "remember." + stage, "client_controlled": true, "issues": issues, "issues_truncated": truncated,
	}}
}

func AssessmentSecurityRejectionFailure(prepared *SynchronousAssessmentResult) error {
	if prepared == nil {
		return ErrRememberProviderResponseInvalid
	}
	var signals []SubmissionSecurityBatchSignal
	for _, result := range prepared.Response.EvidenceSecurityResults {
		if result.Decision != "reject" {
			continue
		}
		item, ok := prepared.Plan.itemsByEvidenceID[result.EvidenceID]
		if !ok {
			return ErrRememberProviderResponseInvalid
		}
		for _, signal := range result.Signals {
			signals = append(signals, SubmissionSecurityBatchSignal{EvidenceIndex: item.Fragment.EvidenceIndex, Source: SecuritySourceEvidence,
				SubmissionSecuritySignal: SubmissionSecuritySignal{Kind: signal.Kind, Start: signal.Start, End: signal.End}})
		}
	}
	return SecurityRejectionFailure(signals, false, "assessment")
}

func RememberFailureDetails(err error, phase string) (string, map[string]any) {
	var sourceConflict *repository.RememberSourceRevisionConflictError
	if errors.As(err, &sourceConflict) && sourceConflict.EvidenceIndex >= 0 {
		return "source_revision_changed", map[string]any{"component": "remember." + phase, "client_controlled": true,
			"issues": []any{map[string]any{"path": fmt.Sprintf("/evidence/%d/source_revision", sourceConflict.EvidenceIndex), "code": "source_revision_changed"}}}
	}
	if reason, details := SynchronousAssessmentFailureDetails(err); reason != "" {
		return reason, details
	}
	var malformed *assessor.MalformedResponseError
	if errors.As(err, &malformed) {
		return "assessment_response_invalid", map[string]any{"component": "remember.assessment", "server_owned": true, "attempts": malformed.Attempts}
	}
	for _, condition := range []struct {
		cause        error
		reason, path string
	}{
		{repository.ErrSourceRevisionConflict, "source_revision_changed", "/evidence"},
		{repository.ErrSubmissionAssessmentKnownEvidenceStale, "known_evidence_changed", "/relationships"},
		{repository.ErrCorrectionTargetStale, "correction_target_changed", "/relationships"},
		{repository.ErrConflictContextStale, "conflict_target_changed", "/relationships"},
		{repository.ErrEvidenceConflictStaleInput, "conflict_target_changed", "/evidence"},
		{repository.ErrRememberExactReferenceStale, "exact_reference_changed", "/relationships"},
		{errSubmissionAssessmentStaleInput, "exact_reference_changed", "/relationships"},
		{repository.ErrRememberDuplicateCandidateStale, "known_evidence_changed", "/evidence"},
	} {
		if errors.Is(err, condition.cause) {
			return condition.reason, map[string]any{"component": "remember." + phase, "client_controlled": true,
				"issues": []any{map[string]any{"path": condition.path, "code": condition.reason}}}
		}
	}
	if phase == "embedding" {
		metadata := embeddingcontract.ClassifyFailure(err)
		reason := ""
		switch metadata.Code {
		case "provider_rate_limited":
			reason = "embedding_rate_limited"
		case "provider_timeout":
			reason = "embedding_timeout"
		case "provider_server_error":
			reason = "embedding_server_failure"
		case "provider_network_error":
			reason = "embedding_transport_failure"
		case "provider_authentication_failed", "provider_permission_denied", "provider_contract_rejected", "provider_quota_exhausted", "embedding_input_rejected":
			reason = "embedding_request_rejected"
		}
		if reason != "" {
			details := map[string]any{"component": "remember.embedding", "server_owned": true}
			if metadata.RetryAfter > 0 {
				details["retry_after_seconds"] = int(metadata.RetryAfter.Seconds())
			}
			return reason, details
		}
	}
	provider := modelprovider.ProviderFailureDetails(err)
	var providerError *modelprovider.ProviderError
	var rateLimit *modelprovider.RateLimitError
	if errors.As(err, &providerError) || errors.As(err, &rateLimit) || errors.Is(err, modelprovider.ErrVerifierTimeout) {
		details := map[string]any{"component": "remember." + phase, "server_owned": true}
		if provider.RetryAfter > 0 {
			details["retry_after_seconds"] = int(provider.RetryAfter.Seconds())
		}
		reason := "provider_unavailable"
		switch provider.Class {
		case modelprovider.ProviderFailureClassTimeout:
			reason = "provider_timeout"
		case modelprovider.ProviderFailureClassRateLimited:
			reason = "provider_rate_limited"
		case modelprovider.ProviderFailureClassHTTPClient:
			reason = "provider_request_rejected"
		case modelprovider.ProviderFailureClassHTTPServer:
			reason = "provider_server_failure"
		case modelprovider.ProviderFailureClassTransport:
			reason = "provider_transport_failure"
		}
		return reason, details
	}
	return "", nil
}

func NotStoredGuidance(reason string) (string, string) {
	if reason == "not_supported_by_evidence" {
		return "The supplied evidence did not establish this proposed relationship, so the relationship was not stored.", "Provide independent evidence that establishes the proposed relationship, or clarify the proposal without changing the evidence's factual meaning. Submit the changed request with a new idempotency_key."
	}
	code := SubmissionErrorInternalFailure
	if reason == "submission_policy_rejected" || reason == "security_quarantine" {
		code = SubmissionErrorPolicyRejected
	}
	if reason == "stale_input" {
		code = SubmissionErrorStaleInput
	}
	if reason == "idempotency_conflict" {
		code = SubmissionErrorIdempotencyConflict
	}
	value := StatusError(code)
	return value.Message, value.Remediation
}

func validateNotStoredExplanation(disposition, reason, message, remediation string) error {
	if message == "" && remediation == "" {
		return nil
	}
	if disposition != "not_stored" {
		return errors.New("stored item cannot have rejection guidance")
	}
	expectedMessage, expectedRemediation := NotStoredGuidance(reason)
	legacyMessage, legacyRemediation := legacyNotStoredGuidance(reason)
	if message == legacyMessage && remediation == legacyRemediation {
		return nil
	}
	if message != expectedMessage || remediation != expectedRemediation {
		return errors.New("rejected item explanation is not a server-owned template")
	}
	return nil
}

func legacyNotStoredGuidance(reason string) (string, string) {
	if reason == "submission_policy_rejected" {
		legacy := TerminalStatusErrorWithDetails(TerminalErrorPolicyRejected, "legacy_policy_rejection", map[string]any{"component": "remember.replay"})
		return legacy.Message, legacy.Remediation
	}
	message, remediation := NotStoredGuidance(reason)
	return boundedStatusErrorText(message+" More specific cause and affected input-location details were not retained in this result.", 512), remediation
}

func UpgradeRememberResultDiagnostics(status *SubmissionStatusResult) {
	for index, item := range status.Errors {
		if item.ReasonCode == "" && len(item.Details) == 0 {
			item.ReasonCode = "legacy_details_unavailable"
			if item.Code == string(SubmissionErrorPolicyRejected) {
				item.ReasonCode = "legacy_policy_rejection"
			}
			item.Details = map[string]any{"component": "remember.replay"}
		}
		value := TerminalStatusErrorWithDetails(TerminalErrorCode(item.Code), item.ReasonCode, item.Details)
		if item.NextAction != "" {
			value.Retryable, value.NextAction = item.Retryable, item.NextAction
		}
		if item.NextAction == string(TerminalNextActionRetryDreamFeedback) {
			value.NextAction, value.Remediation = item.NextAction, item.Remediation
		}
		status.Errors[index] = value
	}
	legacy := legacyRememberDiagnosticError(status)
	for index, item := range status.Evidence {
		if item.Disposition == "not_stored" && item.Message == "" {
			status.Evidence[index].Message, status.Evidence[index].Remediation = NotStoredGuidance(item.Reason)
			if legacy != nil && item.Reason != "not_supported_by_evidence" {
				status.Evidence[index].Message, status.Evidence[index].Remediation = legacyNotStoredGuidance(item.Reason)
			}
		}
	}
	for index, item := range status.RelationshipResults {
		if item.Disposition == "not_stored" && item.Message == "" {
			status.RelationshipResults[index].Message, status.RelationshipResults[index].Remediation = NotStoredGuidance(item.Reason)
			if legacy != nil && item.Reason != "not_supported_by_evidence" {
				status.RelationshipResults[index].Message, status.RelationshipResults[index].Remediation = legacyNotStoredGuidance(item.Reason)
			}
		}
	}
}

func legacyRememberDiagnosticError(status *SubmissionStatusResult) *SubmissionStatusError {
	for index := range status.Errors {
		value := status.Errors[index]
		if value.ReasonCode == "legacy_policy_rejection" || value.ReasonCode == "legacy_details_unavailable" || genericRememberFailureDetailsUnavailable(value) {
			return &status.Errors[index]
		}
	}
	return nil
}

func staleEntityReferenceFailure(target submissionAssessmentEntityTarget) error {
	path := target.InputPath
	if !diagnosticInputPath.MatchString(path) {
		path = "/relationships"
	}
	return &submissionDiagnosticFailure{cause: errSubmissionAssessmentStaleInput, reason: "exact_reference_changed",
		details: map[string]any{"component": "remember.entity_catalog", "client_controlled": true,
			"issues": []any{map[string]any{"path": path, "code": "exact_reference_changed"}}}}
}

func diagnosticProposalPath(path string) string {
	path = strings.Replace(path, "/relationship_hints", "/relationships", 1)
	if diagnosticInputPath.MatchString(path) {
		return path
	}
	return "/relationships"
}

func diagnosticArrayPath(path string, index int) string { return path + "/" + strconv.Itoa(index) }
