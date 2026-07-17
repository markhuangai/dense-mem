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
	v2SemanticReviewDefaultMaxAttempts = 2
	v2SemanticReviewOutcomeKind        = "semantic_review"
	v2SemanticReviewAttemptOutcomeKind = "semantic_review_provider_attempt"
)

type V2SemanticReviewProvider interface {
	ReviewV2Semantic(ctx context.Context, req verifier.V2SemanticReviewRequest) (verifier.V2SemanticReviewResponse, error)
	ModelName() string
}

type V2SemanticReviewService interface {
	ReviewV2Semantic(ctx context.Context, job V2SemanticReviewJob) (*V2SemanticReviewResult, error)
}

type V2SemanticReviewDependencies struct {
	Provider    V2SemanticReviewProvider
	Ledger      repository.V2LedgerRepository
	MaxAttempts int
}

type v2SemanticReviewService struct {
	provider    V2SemanticReviewProvider
	ledger      repository.V2LedgerRepository
	maxAttempts int
}

type V2SemanticReviewJob struct {
	TeamID          string
	OwnerProfileID  string
	IngestID        string
	PlacementRunID  string
	PlacementItemID string
	Request         verifier.V2SemanticReviewRequest
	MaxAttempts     int
}

type V2SemanticReviewResult struct {
	Status              string                                        `json:"status"`
	Attempts            int                                           `json:"attempts"`
	EntityResults       []verifier.V2SemanticEntityResult             `json:"entity_results,omitempty"`
	RelationshipResults []verifier.V2SemanticRelationshipReviewResult `json:"relationship_results,omitempty"`
	ValidationErrors    []verifier.V2SemanticValidationError          `json:"validation_errors,omitempty"`
	OutcomeIDs          []string                                      `json:"outcome_ids,omitempty"`
	ResponseHash        string                                        `json:"response_hash,omitempty"`
}

func NewV2SemanticReviewService(deps V2SemanticReviewDependencies) V2SemanticReviewService {
	return &v2SemanticReviewService{
		provider:    deps.Provider,
		ledger:      deps.Ledger,
		maxAttempts: deps.MaxAttempts,
	}
}

func (s *v2SemanticReviewService) ReviewV2Semantic(ctx context.Context, job V2SemanticReviewJob) (*V2SemanticReviewResult, error) {
	if s.provider == nil {
		return nil, errors.New("v2 semantic review: provider is required")
	}
	if s.ledger == nil {
		return nil, errors.New("v2 semantic review: ledger repository is required")
	}
	job = normalizeV2SemanticReviewJob(job)
	request, validationErrors := verifier.PrepareV2SemanticReviewRequest(job.Request)
	job.Request = request
	result := &V2SemanticReviewResult{}
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
		response, err := s.provider.ReviewV2Semantic(ctx, attemptReq)
		if err != nil {
			result.Status = string(domain.V2SemanticReviewRetryable)
			result.Attempts = attempt
			if outcomeErr := s.appendAttemptOutcome(ctx, job, attempt, "provider_error", "", []string{v2SemanticErrorClass(err)}, nil); outcomeErr != nil {
				return nil, outcomeErr
			}
			if outcomeErr := s.appendFinalOutcome(ctx, job, result, "", nil); outcomeErr != nil {
				return nil, outcomeErr
			}
			return result, nil
		}
		responseHash := v2SemanticReviewResponseHash(response)
		validationErrors = verifier.ValidateV2SemanticReviewResponse(request, response)
		if len(validationErrors) > 0 {
			result.Attempts = attempt
			result.ValidationErrors = validationErrors
			if err := s.appendAttemptOutcome(ctx, job, attempt, "invalid", responseHash, v2SemanticValidationMessages(validationErrors), nil); err != nil {
				return nil, err
			}
			if attempt == maxAttempts {
				result.Status = string(domain.V2SemanticReviewTerminalFailure)
				result.ResponseHash = responseHash
				if err := s.appendFinalOutcome(ctx, job, result, responseHash, nil); err != nil {
					return nil, err
				}
				return result, nil
			}
			feedback = v2SemanticValidationMessages(validationErrors)
			previousHash = responseHash
			continue
		}
		if err := s.appendAttemptOutcome(ctx, job, attempt, "valid", responseHash, nil, &response); err != nil {
			return nil, err
		}
		result = v2SemanticReviewResultFromResponse(response, attempt, responseHash)
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

func normalizeV2SemanticReviewJob(job V2SemanticReviewJob) V2SemanticReviewJob {
	job.TeamID = strings.TrimSpace(job.TeamID)
	job.OwnerProfileID = strings.TrimSpace(job.OwnerProfileID)
	job.IngestID = strings.TrimSpace(job.IngestID)
	job.PlacementRunID = strings.TrimSpace(job.PlacementRunID)
	job.PlacementItemID = strings.TrimSpace(job.PlacementItemID)
	job.Request.TeamID = strings.TrimSpace(job.Request.TeamID)
	if job.Request.TeamID == "" {
		job.Request.TeamID = job.TeamID
	}
	job.Request.OwnerProfileID = strings.TrimSpace(job.Request.OwnerProfileID)
	if job.Request.OwnerProfileID == "" {
		job.Request.OwnerProfileID = job.OwnerProfileID
	}
	return job
}

func (s *v2SemanticReviewService) maxAttemptsFor(job V2SemanticReviewJob) int {
	maxAttempts := job.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = s.maxAttempts
	}
	if maxAttempts <= 0 {
		maxAttempts = v2SemanticReviewDefaultMaxAttempts
	}
	return maxAttempts
}

func (s *v2SemanticReviewService) appendAttemptOutcome(ctx context.Context, job V2SemanticReviewJob, attempt int, status string, responseHash string, validationErrors []string, response *verifier.V2SemanticReviewResponse) error {
	payload := v2SemanticAttemptPayload(job.Request, attempt, s.provider.ModelName(), responseHash, validationErrors, response)
	outcomeID, err := s.ledger.AppendPlacementOutcome(ctx, repository.V2PlacementOutcomeInput{
		TeamID:          job.TeamID,
		OwnerProfileID:  job.OwnerProfileID,
		PlacementRunID:  job.PlacementRunID,
		PlacementItemID: job.PlacementItemID,
		OutcomeKind:     v2SemanticReviewAttemptOutcomeKind,
		Status:          status,
		Payload:         payload,
	})
	if err != nil {
		return err
	}
	_ = outcomeID
	return nil
}

func (s *v2SemanticReviewService) appendFinalOutcome(ctx context.Context, job V2SemanticReviewJob, result *V2SemanticReviewResult, responseHash string, response *verifier.V2SemanticReviewResponse) error {
	input := repository.V2PlacementOutcomeInput{
		TeamID:          job.TeamID,
		OwnerProfileID:  job.OwnerProfileID,
		PlacementRunID:  job.PlacementRunID,
		PlacementItemID: job.PlacementItemID,
		OutcomeKind:     v2SemanticReviewOutcomeKind,
		Status:          result.Status,
		Payload:         v2SemanticFinalPayload(job.Request, result, s.provider.ModelName(), responseHash, response),
	}
	switch result.Status {
	case string(domain.V2SemanticReviewQuarantined):
		input.UpdateItemStatus = "quarantined"
		input.UpdateItemCategory = "quarantined"
	case string(domain.V2SemanticReviewTerminalFailure):
		input.UpdateItemStatus = "failed"
		input.UpdateItemCategory = "failed"
	}
	outcomeID, err := s.ledger.AppendPlacementOutcome(ctx, input)
	if err != nil {
		return err
	}
	result.OutcomeIDs = append(result.OutcomeIDs, outcomeID)
	return nil
}

func (s *v2SemanticReviewService) appendSecurityEvents(ctx context.Context, job V2SemanticReviewJob, req verifier.V2SemanticReviewRequest, resp verifier.V2SemanticReviewResponse, attempt int) error {
	evidenceByID := map[string]verifier.V2SemanticReviewEvidence{}
	for _, evidence := range req.Evidence {
		evidenceByID[evidence.EvidenceID] = evidence
	}
	for _, signal := range resp.SecuritySignals {
		evidence, ok := evidenceByID[signal.EvidenceID]
		if !ok || evidence.FragmentID == "" {
			return fmt.Errorf("v2 semantic review: cannot persist security signal for evidence %q", signal.EvidenceID)
		}
		if _, err := s.ledger.AppendSecurityEvent(ctx, repository.V2SecurityEventInput{
			TeamID:         job.TeamID,
			OwnerProfileID: job.OwnerProfileID,
			IngestID:       job.IngestID,
			FragmentID:     evidence.FragmentID,
			V2SecurityEventDraft: repository.V2SecurityEventDraft{
				EventKind:      "verifier_signal",
				Decision:       "quarantine",
				ScanPolicyHash: v2SecurityScanPolicyHash,
				Reason:         "semantic verifier reported security signal",
				Signals: []repository.V2SecuritySignalInput{{
					Kind:      signal.Kind,
					Severity:  v2SemanticSignalSeverity(signal.Kind),
					SpanStart: signal.Start,
					SpanEnd:   signal.End,
					Quote:     evidence.Content[signal.Start:signal.End],
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

func v2SemanticReviewResultFromResponse(resp verifier.V2SemanticReviewResponse, attempts int, responseHash string) *V2SemanticReviewResult {
	result := &V2SemanticReviewResult{
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
	relationshipCount := 0
	contradictedCount := 0
	for _, entity := range resp.EntityResults {
		if entity.Action == string(domain.V2EntityResolutionAmbiguous) {
			result.Status = string(domain.V2SemanticReviewReviewRequired)
		}
	}
	for _, relationship := range resp.RelationshipResults {
		relationshipCount++
		if relationship.PredicateStatus == "needs_review" || relationship.EvidenceVerdict == string(domain.V2VerificationInsufficient) {
			result.Status = string(domain.V2SemanticReviewReviewRequired)
		}
		if relationship.EvidenceVerdict == string(domain.V2VerificationContradicted) {
			contradictedCount++
		}
	}
	if relationshipCount > 0 && contradictedCount == relationshipCount {
		result.Status = string(domain.V2SemanticReviewRejected)
	}
	return result
}

func v2SemanticAttemptPayload(req verifier.V2SemanticReviewRequest, attempt int, model string, responseHash string, validationErrors []string, resp *verifier.V2SemanticReviewResponse) map[string]any {
	payload := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"request_id":       req.RequestID,
		"attempt":          attempt,
		"model":            model,
		"request_summary":  v2SemanticRequestSummary(req),
		"redaction":        "raw provider request and response are not stored",
	}
	if responseHash != "" {
		payload["response_hash"] = responseHash
	}
	if len(validationErrors) > 0 {
		payload["validation_errors"] = validationErrors
	}
	if resp != nil {
		payload["response_summary"] = v2SemanticResponseSummary(*resp)
	}
	return payload
}

func v2SemanticFinalPayload(req verifier.V2SemanticReviewRequest, result *V2SemanticReviewResult, model string, responseHash string, resp *verifier.V2SemanticReviewResponse) map[string]any {
	payload := map[string]any{
		"contract_version": domain.V2ContractVersion,
		"request_id":       req.RequestID,
		"status":           result.Status,
		"attempts":         result.Attempts,
		"model":            model,
		"request_summary":  v2SemanticRequestSummary(req),
		"redaction":        "raw provider request and response are not stored",
	}
	if responseHash != "" {
		payload["response_hash"] = responseHash
	}
	if len(result.ValidationErrors) > 0 {
		payload["validation_errors"] = v2SemanticValidationMessages(result.ValidationErrors)
	}
	if resp != nil {
		payload["response_summary"] = v2SemanticResponseSummary(*resp)
		payload["normalized_results"] = v2SemanticNormalizedResults(*resp)
	}
	return payload
}

func v2SemanticRequestSummary(req verifier.V2SemanticReviewRequest) map[string]any {
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

func v2SemanticResponseSummary(resp verifier.V2SemanticReviewResponse) map[string]any {
	return map[string]any{
		"security_signal_count":     len(resp.SecuritySignals),
		"entity_result_count":       len(resp.EntityResults),
		"relationship_result_count": len(resp.RelationshipResults),
	}
}

func v2SemanticNormalizedResults(resp verifier.V2SemanticReviewResponse) map[string]any {
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

func v2SemanticReviewResponseHash(resp verifier.V2SemanticReviewResponse) string {
	data, _ := json.Marshal(resp)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func v2SemanticValidationMessages(errs []verifier.V2SemanticValidationError) []string {
	out := make([]string, 0, len(errs))
	for _, err := range errs {
		out = append(out, err.Error())
	}
	return out
}

func v2SemanticErrorClass(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("%T", err)
}

func v2SemanticSignalSeverity(kind string) string {
	if strings.TrimSpace(kind) == "hidden_control_markup" {
		return "critical"
	}
	return "high"
}
