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
	ReviewSemantic(ctx context.Context, req verifier.SemanticReviewRequest) (verifier.SemanticReviewResponse, error)
	ModelName() string
}

type SemanticReviewService interface {
	ReviewSemantic(ctx context.Context, job SemanticReviewJob) (*SemanticReviewResult, error)
}

type SemanticReviewDependencies struct {
	Provider    SemanticReviewProvider
	Ledger      repository.LedgerRepository
	MaxAttempts int
}

type semanticReviewService struct {
	provider    SemanticReviewProvider
	ledger      repository.LedgerRepository
	maxAttempts int
}

type SemanticReviewJob struct {
	TeamID                    string
	OwnerProfileID            string
	IngestID                  string
	PlacementRunID            string
	PlacementItemID           string
	Request                   verifier.SemanticReviewRequest
	ValidationErrors          []verifier.SemanticValidationError
	RetryableValidationErrors []verifier.SemanticValidationError
	FailureStage              string
	FailureClass              string
	MaxAttempts               int
}

type SemanticReviewResult struct {
	Status              string                                      `json:"status"`
	Attempts            int                                         `json:"attempts"`
	EntityResults       []verifier.SemanticEntityResult             `json:"entity_results,omitempty"`
	RelationshipResults []verifier.SemanticRelationshipReviewResult `json:"relationship_results,omitempty"`
	ValidationErrors    []verifier.SemanticValidationError          `json:"validation_errors,omitempty"`
	OutcomeIDs          []string                                    `json:"outcome_ids,omitempty"`
	ResponseHash        string                                      `json:"response_hash,omitempty"`
	FailureStage        string                                      `json:"failure_stage,omitempty"`
	FailureClass        string                                      `json:"failure_class,omitempty"`
	RetryableExhausted  bool                                        `json:"retryable_exhausted,omitempty"`
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
		result.Status = string(domain.SemanticReviewTerminalFailure)
		result.ValidationErrors = append([]verifier.SemanticValidationError(nil), job.ValidationErrors...)
		result.FailureStage = semanticFailureStageOrDefault(job.FailureStage, semanticFailureStagePreflight)
		result.FailureClass = semanticFailureClassOrDefault(job.FailureClass, semanticFailureClassValidationFailed)
		if err := s.appendFinalOutcome(ctx, job, result, "", nil); err != nil {
			return nil, err
		}
		return result, nil
	}
	if len(job.RetryableValidationErrors) > 0 {
		result.Status = string(domain.SemanticReviewRetryable)
		result.ValidationErrors = append([]verifier.SemanticValidationError(nil), job.RetryableValidationErrors...)
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
	request, validationErrors := verifier.PrepareSemanticReviewRequest(job.Request)
	job.Request = request
	if len(validationErrors) > 0 {
		result.Status = string(domain.SemanticReviewTerminalFailure)
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
					result.Status = string(domain.SemanticReviewRetryable)
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
			result.Status = string(domain.SemanticReviewRetryable)
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
		validationErrors = verifier.ValidateSemanticReviewResponse(request, response)
		if len(validationErrors) > 0 {
			result.Attempts = attempt
			result.ValidationErrors = validationErrors
			if err := s.appendAttemptOutcome(ctx, job, attempt, "invalid", responseHash, semanticValidationMessages(validationErrors), nil); err != nil {
				return nil, err
			}
			if attempt == maxAttempts {
				result.Status = string(domain.SemanticReviewRetryable)
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
		if result.Status == string(domain.SemanticReviewQuarantined) {
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

func (s *semanticReviewService) appendAttemptOutcome(ctx context.Context, job SemanticReviewJob, attempt int, status string, responseHash string, validationErrors []string, response *verifier.SemanticReviewResponse) error {
	payload := semanticAttemptPayload(job.Request, attempt, s.provider.ModelName(), responseHash, validationErrors, response)
	outcomeID, err := s.ledger.AppendPlacementOutcome(ctx, repository.PlacementOutcomeInput{
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

func (s *semanticReviewService) appendFinalOutcome(ctx context.Context, job SemanticReviewJob, result *SemanticReviewResult, responseHash string, response *verifier.SemanticReviewResponse) error {
	input := repository.PlacementOutcomeInput{
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

func (s *semanticReviewService) appendSecurityEvents(ctx context.Context, job SemanticReviewJob, req verifier.SemanticReviewRequest, resp verifier.SemanticReviewResponse, attempt int) error {
	evidenceByID := map[string]verifier.SemanticReviewEvidence{}
	for _, evidence := range req.Evidence {
		evidenceByID[evidence.EvidenceID] = evidence
	}
	for _, signal := range resp.SecuritySignals {
		evidence, ok := evidenceByID[signal.EvidenceID]
		if !ok || evidence.FragmentID == "" {
			return fmt.Errorf("semantic review: cannot persist security signal for evidence %q", signal.EvidenceID)
		}
		quote, err := verifier.SemanticEvidenceSpan(evidence.Content, signal.Start, signal.End)
		if err != nil {
			return fmt.Errorf("semantic review: cannot persist security signal span: %w", err)
		}
		if _, err := s.ledger.AppendSecurityEvent(ctx, repository.SecurityEventInput{
			TeamID:         job.TeamID,
			OwnerProfileID: job.OwnerProfileID,
			IngestID:       job.IngestID,
			FragmentID:     evidence.FragmentID,
			SecurityEventDraft: repository.SecurityEventDraft{
				EventKind:      "verifier_signal",
				Decision:       "quarantine",
				ScanPolicyHash: securityScanPolicyHash,
				Reason:         "semantic verifier reported security signal",
				Signals: []repository.SecuritySignalInput{{
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

func semanticReviewResultFromResponse(resp verifier.SemanticReviewResponse, attempts int, responseHash string) *SemanticReviewResult {
	result := &SemanticReviewResult{
		Status:              string(domain.SemanticReviewAccepted),
		Attempts:            attempts,
		EntityResults:       resp.EntityResults,
		RelationshipResults: resp.RelationshipResults,
		ResponseHash:        responseHash,
	}
	if len(resp.SecuritySignals) > 0 {
		result.Status = string(domain.SemanticReviewQuarantined)
		result.EntityResults = nil
		result.RelationshipResults = nil
		return result
	}
	return result
}

func semanticAttemptPayload(req verifier.SemanticReviewRequest, attempt int, model string, responseHash string, validationErrors []string, resp *verifier.SemanticReviewResponse) map[string]any {
	payload := map[string]any{
		"contract_version": domain.ContractVersion,
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

func semanticFinalPayload(req verifier.SemanticReviewRequest, result *SemanticReviewResult, model string, responseHash string, resp *verifier.SemanticReviewResponse) map[string]any {
	payload := map[string]any{
		"contract_version": domain.ContractVersion,
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

func semanticRequestSummary(req verifier.SemanticReviewRequest) map[string]any {
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

func semanticResponseSummary(resp verifier.SemanticReviewResponse) map[string]any {
	return map[string]any{
		"security_signal_count":     len(resp.SecuritySignals),
		"entity_result_count":       len(resp.EntityResults),
		"relationship_result_count": len(resp.RelationshipResults),
	}
}

func semanticNormalizedResults(resp verifier.SemanticReviewResponse) map[string]any {
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

func semanticReviewResponseHash(resp verifier.SemanticReviewResponse) (string, error) {
	data, err := json.Marshal(resp)
	if err != nil {
		return "", fmt.Errorf("semantic review: response hash: %w", err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func semanticValidationMessages(errs []verifier.SemanticValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return out
}

func semanticMalformedValidationErrors(field string, err error) []verifier.SemanticValidationError {
	return []verifier.SemanticValidationError{{
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
