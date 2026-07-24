package memoryservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

const (
	semanticReviewDefaultMaxAttempts           = 5
	SemanticPlacementDefaultVerifierCallBudget = semanticProposalDefaultMaxAttempts + semanticReviewDefaultMaxAttempts
	semanticReviewOutcomeKind                  = "semantic_review"
	semanticReviewAttemptOutcomeKind           = "semantic_review_provider_attempt"

	semanticFailureStagePredicateCatalog = "predicate_catalog"
	semanticFailureStageExtraction       = "extraction"
	semanticFailureStageVerification     = "verification"
	semanticFailureStagePreflight        = "preflight"
	semanticFailureStageUnknown          = "unknown"

	semanticFailureClassTimeout             = "timeout"
	semanticFailureClassRateLimited         = "rate_limited"
	semanticFailureClassProviderUnavailable = "provider_unavailable"
	semanticFailureClassMalformedResponse   = "malformed_response"
	semanticFailureClassContextCanceled     = "context_canceled"
	semanticFailureClassLookupFailed        = "lookup_failed"
	semanticFailureClassValidationFailed    = "validation_failed"
	semanticFailureClassUnknown             = "unknown"
)

type SemanticReviewProvider interface {
	ReviewSemantic(ctx context.Context, req verifier.V2SemanticReviewRequest) (verifier.V2SemanticReviewResponse, error)
	ModelName() string
}

type SemanticReviewService interface {
	ReviewSemantic(ctx context.Context, job SemanticReviewJob) (*SemanticReviewResult, error)
}

type SemanticReviewDependencies struct {
	Provider    SemanticReviewProvider
	Ledger      repository.V2LedgerRepository
	MaxAttempts int
}

type semanticReviewService struct {
	provider    SemanticReviewProvider
	ledger      repository.V2LedgerRepository
	maxAttempts int
}

type SemanticReviewJob struct {
	TeamID                    string
	OwnerProfileID            string
	IngestID                  string
	PlacementRunID            string
	PlacementItemID           string
	Request                   verifier.V2SemanticReviewRequest
	ValidationErrors          []verifier.V2SemanticValidationError
	RetryableValidationErrors []verifier.V2SemanticValidationError
	FailureStage              string
	FailureClass              string
	MaxAttempts               int
}

type SemanticReviewResult struct {
	Status              string                                        `json:"status"`
	Attempts            int                                           `json:"attempts"`
	EntityResults       []verifier.V2SemanticEntityResult             `json:"entity_results,omitempty"`
	RelationshipResults []verifier.V2SemanticRelationshipReviewResult `json:"relationship_results,omitempty"`
	ValidationErrors    []verifier.V2SemanticValidationError          `json:"validation_errors,omitempty"`
	OutcomeIDs          []string                                      `json:"outcome_ids,omitempty"`
	ResponseHash        string                                        `json:"response_hash,omitempty"`
	FailureStage        string                                        `json:"failure_stage,omitempty"`
	FailureClass        string                                        `json:"failure_class,omitempty"`
	RetryableExhausted  bool                                          `json:"retryable_exhausted,omitempty"`
}

func NewSemanticReviewService(deps SemanticReviewDependencies) SemanticReviewService {
	return &semanticReviewService{
		provider:    deps.Provider,
		ledger:      deps.Ledger,
		maxAttempts: deps.MaxAttempts,
	}
}

func (s *semanticReviewService) ReviewSemantic(ctx context.Context, job SemanticReviewJob) (*SemanticReviewResult, error) {
	if s.ledger == nil {
		return nil, errors.New("semantic review: ledger repository is required")
	}
	job = normalizeSemanticReviewJob(job)
	result := &SemanticReviewResult{}
	if len(job.ValidationErrors) > 0 {
		result.Status = string(domain.V2SemanticReviewTerminalFailure)
		result.ValidationErrors = append([]verifier.V2SemanticValidationError(nil), job.ValidationErrors...)
		result.FailureStage = semanticFailureStageOrDefault(job.FailureStage, semanticFailureStagePreflight)
		result.FailureClass = semanticFailureClassOrDefault(job.FailureClass, semanticFailureClassValidationFailed)
		if err := s.appendFinalOutcome(ctx, job, result, "", nil); err != nil {
			return nil, err
		}
		return result, nil
	}
	if len(job.RetryableValidationErrors) > 0 {
		result.Status = string(domain.V2SemanticReviewRetryable)
		result.ValidationErrors = append([]verifier.V2SemanticValidationError(nil), job.RetryableValidationErrors...)
		result.FailureStage = semanticFailureStageOrDefault(job.FailureStage, semanticFailureStagePreflight)
		result.FailureClass = semanticFailureClassOrDefault(job.FailureClass, semanticFailureClassUnknown)
		if err := s.appendFinalOutcome(ctx, job, result, "", nil); err != nil {
			return nil, err
		}
		return result, nil
	}
	if s.provider == nil {
		return nil, errors.New("semantic review: provider is required")
	}
	request, validationErrors := verifier.PrepareV2SemanticReviewRequest(job.Request)
	job.Request = request
	if len(validationErrors) > 0 {
		result.Status = string(domain.V2SemanticReviewTerminalFailure)
		result.ValidationErrors = validationErrors
		if err := s.appendFinalOutcome(ctx, job, result, "", nil); err != nil {
			return nil, err
		}
		return result, nil
	}
	maxAttempts := s.maxAttemptsFor(job)
	feedback := make([]string, 0)
	previousHash := ""
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attemptReq := request
		attemptReq.Attempt = attempt
		attemptReq.ValidationFeedback = feedback
		attemptReq.PreviousResponseHash = previousHash
		response, err := s.provider.ReviewSemantic(ctx, attemptReq)
		if err != nil {
			if errors.Is(err, verifier.ErrVerifierMalformedResponse) {
				validationErrors = semanticMalformedValidationErrors("provider_response", err)
				result.Attempts = attempt
				result.ValidationErrors = validationErrors
				if err := s.appendAttemptOutcome(ctx, job, attempt, "invalid", "", semanticValidationMessages(validationErrors), nil); err != nil {
					return nil, err
				}
				if attempt == maxAttempts {
					result.Status = string(domain.V2SemanticReviewRetryable)
					result.FailureStage = semanticFailureStageVerification
					result.FailureClass = semanticFailureClassMalformedResponse
					if err := s.appendFinalOutcome(ctx, job, result, "", nil); err != nil {
						return nil, err
					}
					return result, nil
				}
				feedback = semanticValidationMessages(validationErrors)
				previousHash = ""
				continue
			}
			result.Status = string(domain.V2SemanticReviewRetryable)
			result.Attempts = attempt
			result.FailureStage = semanticFailureStageVerification
			result.FailureClass = semanticProviderFailureClass(err)
			if outcomeErr := s.appendAttemptOutcome(ctx, job, attempt, "provider_error", "", []string{result.FailureClass}, nil); outcomeErr != nil {
				return nil, outcomeErr
			}
			if outcomeErr := s.appendFinalOutcome(ctx, job, result, "", nil); outcomeErr != nil {
				return nil, outcomeErr
			}
			return result, nil
		}
		responseHash, err := semanticReviewResponseHash(response)
		if err != nil {
			return nil, err
		}
		validationErrors = verifier.ValidateV2SemanticReviewResponse(request, response)
		if len(validationErrors) > 0 {
			result.Attempts = attempt
			result.ValidationErrors = validationErrors
			if err := s.appendAttemptOutcome(ctx, job, attempt, "invalid", responseHash, semanticValidationMessages(validationErrors), nil); err != nil {
				return nil, err
			}
			if attempt == maxAttempts {
				result.Status = string(domain.V2SemanticReviewRetryable)
				result.ResponseHash = responseHash
				result.FailureStage = semanticFailureStageVerification
				result.FailureClass = semanticFailureClassMalformedResponse
				if err := s.appendFinalOutcome(ctx, job, result, responseHash, nil); err != nil {
					return nil, err
				}
				return result, nil
			}
			feedback = semanticValidationMessages(validationErrors)
			previousHash = responseHash
			continue
		}
		if err := s.appendAttemptOutcome(ctx, job, attempt, "valid", responseHash, nil, &response); err != nil {
			return nil, err
		}
		result = semanticReviewResultFromResponse(response, attempt, responseHash)
		if result.Status == string(domain.V2SemanticReviewQuarantined) {
			if err := s.appendSecurityEvents(ctx, job, request, response, attempt); err != nil {
				return nil, err
			}
		}
		if err := s.appendFinalOutcome(ctx, job, result, responseHash, &response); err != nil {
			return nil, err
		}
		return result, nil
	}
	return result, nil
}

func normalizeSemanticReviewJob(job SemanticReviewJob) SemanticReviewJob {
	job.TeamID = strings.TrimSpace(job.TeamID)
	job.OwnerProfileID = strings.TrimSpace(job.OwnerProfileID)
	job.IngestID = strings.TrimSpace(job.IngestID)
	job.PlacementRunID = strings.TrimSpace(job.PlacementRunID)
	job.PlacementItemID = strings.TrimSpace(job.PlacementItemID)
	job.FailureStage = strings.TrimSpace(job.FailureStage)
	job.FailureClass = strings.TrimSpace(job.FailureClass)
	job.Request.TeamID = job.TeamID
	job.Request.OwnerProfileID = job.OwnerProfileID
	return job
}

func (s *semanticReviewService) maxAttemptsFor(job SemanticReviewJob) int {
	maxAttempts := job.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = s.maxAttempts
	}
	if maxAttempts <= 0 {
		maxAttempts = semanticReviewDefaultMaxAttempts
	}
	return maxAttempts
}

func (s *semanticReviewService) appendAttemptOutcome(ctx context.Context, job SemanticReviewJob, attempt int, status string, responseHash string, validationErrors []string, response *verifier.V2SemanticReviewResponse) error {
	payload := semanticAttemptPayload(job.Request, attempt, s.provider.ModelName(), responseHash, validationErrors, response)
	outcomeID, err := s.ledger.AppendPlacementOutcome(ctx, repository.V2PlacementOutcomeInput{
		TeamID:          job.TeamID,
		OwnerProfileID:  job.OwnerProfileID,
		PlacementRunID:  job.PlacementRunID,
		PlacementItemID: job.PlacementItemID,
		OutcomeKind:     semanticReviewAttemptOutcomeKind,
		Status:          status,
		Payload:         payload,
	})
	if err != nil {
		return err
	}
	_ = outcomeID
	return nil
}

func (s *semanticReviewService) appendFinalOutcome(ctx context.Context, job SemanticReviewJob, result *SemanticReviewResult, responseHash string, response *verifier.V2SemanticReviewResponse) error {
	input := repository.V2PlacementOutcomeInput{
		TeamID:          job.TeamID,
		OwnerProfileID:  job.OwnerProfileID,
		PlacementRunID:  job.PlacementRunID,
		PlacementItemID: job.PlacementItemID,
		OutcomeKind:     semanticReviewOutcomeKind,
		Status:          result.Status,
		Payload:         semanticFinalPayload(job.Request, result, s.provider.ModelName(), responseHash, response),
	}
	outcomeID, err := s.ledger.AppendPlacementOutcome(ctx, input)
	if err != nil {
		return err
	}
	result.OutcomeIDs = append(result.OutcomeIDs, outcomeID)
	return nil
}

func (s *semanticReviewService) appendSecurityEvents(ctx context.Context, job SemanticReviewJob, req verifier.V2SemanticReviewRequest, resp verifier.V2SemanticReviewResponse, attempt int) error {
	evidenceByID := map[string]verifier.V2SemanticReviewEvidence{}
	for _, evidence := range req.Evidence {
		evidenceByID[evidence.EvidenceID] = evidence
	}
	for _, signal := range resp.SecuritySignals {
		evidence, ok := evidenceByID[signal.EvidenceID]
		if !ok || evidence.FragmentID == "" {
			return fmt.Errorf("semantic review: cannot persist security signal for evidence %q", signal.EvidenceID)
		}
		quote, err := verifier.V2SemanticEvidenceSpan(evidence.Content, signal.Start, signal.End)
		if err != nil {
			return fmt.Errorf("semantic review: cannot persist security signal span: %w", err)
		}
		if _, err := s.ledger.AppendSecurityEvent(ctx, repository.V2SecurityEventInput{
			TeamID:         job.TeamID,
			OwnerProfileID: job.OwnerProfileID,
			IngestID:       job.IngestID,
			FragmentID:     evidence.FragmentID,
			V2SecurityEventDraft: repository.V2SecurityEventDraft{
				EventKind:      "verifier_signal",
				Decision:       "quarantine",
				ScanPolicyHash: securityScanPolicyHash,
				Reason:         "semantic verifier reported security signal",
				Signals: []repository.V2SecuritySignalInput{{
					Kind:      signal.Kind,
					Severity:  semanticSignalSeverity(signal.Kind),
					SpanStart: signal.Start,
					SpanEnd:   signal.End,
					Quote:     quote,
				}},
				Metadata: map[string]any{
					"request_id": req.RequestID,
					"attempt":    attempt,
					"model":      s.provider.ModelName(),
				},
			},
		}); err != nil {
			return err
		}
	}
	return nil
}

func semanticReviewResultFromResponse(resp verifier.V2SemanticReviewResponse, attempts int, responseHash string) *SemanticReviewResult {
	result := &SemanticReviewResult{
		Status:              string(domain.V2SemanticReviewAccepted),
		Attempts:            attempts,
		EntityResults:       resp.EntityResults,
		RelationshipResults: resp.RelationshipResults,
		ResponseHash:        responseHash,
	}
	if len(resp.SecuritySignals) > 0 {
		result.Status = string(domain.V2SemanticReviewQuarantined)
		result.EntityResults = nil
		result.RelationshipResults = nil
		return result
	}
	return result
}

func semanticAttemptPayload(req verifier.V2SemanticReviewRequest, attempt int, model string, responseHash string, validationErrors []string, resp *verifier.V2SemanticReviewResponse) map[string]any {
	payload := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"request_id":       req.RequestID,
		"attempt":          attempt,
		"model":            model,
		"request_summary":  semanticRequestSummary(req),
		"redaction":        "raw provider request and response are not stored",
	}
	if responseHash != "" {
		payload["response_hash"] = responseHash
	}
	if len(validationErrors) > 0 {
		payload["validation_errors"] = validationErrors
	}
	if resp != nil {
		payload["response_summary"] = semanticResponseSummary(*resp)
	}
	return payload
}

func semanticFinalPayload(req verifier.V2SemanticReviewRequest, result *SemanticReviewResult, model string, responseHash string, resp *verifier.V2SemanticReviewResponse) map[string]any {
	payload := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"request_id":       req.RequestID,
		"status":           result.Status,
		"attempts":         result.Attempts,
		"model":            model,
		"request_summary":  semanticRequestSummary(req),
		"redaction":        "raw provider request and response are not stored",
	}
	if responseHash != "" {
		payload["response_hash"] = responseHash
	}
	if len(result.ValidationErrors) > 0 {
		payload["validation_errors"] = semanticValidationMessages(result.ValidationErrors)
	}
	if result.FailureStage != "" {
		payload["failure_stage"] = result.FailureStage
	}
	if result.FailureClass != "" {
		payload["failure_class"] = result.FailureClass
	}
	if result.RetryableExhausted {
		payload["retryable_exhausted"] = true
	}
	if resp != nil {
		payload["response_summary"] = semanticResponseSummary(*resp)
		payload["normalized_results"] = semanticNormalizedResults(*resp)
	}
	return payload
}

func semanticRequestSummary(req verifier.V2SemanticReviewRequest) map[string]any {
	candidateCount := 0
	predicateCount := 0
	evidenceIDs := make([]string, 0, len(req.Evidence))
	for _, evidence := range req.Evidence {
		evidenceIDs = append(evidenceIDs, evidence.EvidenceID)
	}
	for _, mention := range req.EntityMentions {
		candidateCount += len(mention.Candidates)
	}
	for _, obs := range req.RelationshipObservations {
		predicateCount += len(obs.PredicateCandidates)
	}
	return map[string]any{
		"evidence_ids":              evidenceIDs,
		"evidence_count":            len(req.Evidence),
		"entity_mention_count":      len(req.EntityMentions),
		"relationship_count":        len(req.RelationshipObservations),
		"entity_candidate_count":    candidateCount,
		"predicate_candidate_count": predicateCount,
	}
}

func semanticResponseSummary(resp verifier.V2SemanticReviewResponse) map[string]any {
	return map[string]any{
		"security_signal_count":     len(resp.SecuritySignals),
		"entity_result_count":       len(resp.EntityResults),
		"relationship_result_count": len(resp.RelationshipResults),
	}
}

func semanticNormalizedResults(resp verifier.V2SemanticReviewResponse) map[string]any {
	entities := make([]map[string]any, 0, len(resp.EntityResults))
	for _, entity := range resp.EntityResults {
		item := map[string]any{
			"ref":        entity.Ref,
			"action":     entity.Action,
			"confidence": entity.Confidence,
		}
		if entity.CandidateEntityID != nil {
			item["candidate_entity_id"] = *entity.CandidateEntityID
		}
		entities = append(entities, item)
	}
	relationships := make([]map[string]any, 0, len(resp.RelationshipResults))
	for _, relationship := range resp.RelationshipResults {
		item := map[string]any{
			"ref":              relationship.Ref,
			"predicate_status": relationship.PredicateStatus,
			"evidence_verdict": relationship.EvidenceVerdict,
			"confidence":       relationship.Confidence,
		}
		if relationship.PredicateKey != nil {
			item["predicate_key"] = *relationship.PredicateKey
		}
		relationships = append(relationships, item)
	}
	return map[string]any{
		"entity_results":       entities,
		"relationship_results": relationships,
	}
}

func semanticReviewResponseHash(resp verifier.V2SemanticReviewResponse) (string, error) {
	data, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("semantic review: response hash: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func semanticValidationMessages(errs []verifier.V2SemanticValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return out
}

func semanticMalformedValidationErrors(field string, err error) []verifier.V2SemanticValidationError {
	return []verifier.V2SemanticValidationError{{
		Field:   field,
		Message: semanticMalformedValidationMessage(err),
	}}
}

func semanticMalformedValidationMessage(err error) string {
	message := "provider returned malformed structured response; regenerate the complete response using the required schema"
	if err == nil {
		return message
	}
	if errText := strings.TrimSpace(err.Error()); errText != "" {
		message += ": " + errText
	}
	return message
}

func semanticSignalSeverity(kind string) string {
	if strings.TrimSpace(kind) == "hidden_control_markup" {
		return "critical"
	}
	return "high"
}

func semanticFailureStageOrDefault(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallback
}

func semanticFailureClassOrDefault(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value != "" {
		return value
	}
	return fallback
}

func semanticProviderFailureClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, verifier.ErrVerifierTimeout), errors.Is(err, context.DeadlineExceeded):
		return semanticFailureClassTimeout
	case errors.Is(err, verifier.ErrVerifierRateLimit):
		return semanticFailureClassRateLimited
	case errors.Is(err, verifier.ErrVerifierMalformedResponse):
		return semanticFailureClassMalformedResponse
	case errors.Is(err, context.Canceled):
		return semanticFailureClassContextCanceled
	case errors.Is(err, verifier.ErrVerifierProvider):
		return semanticFailureClassProviderUnavailable
	default:
		return semanticFailureClassUnknown
	}
}
