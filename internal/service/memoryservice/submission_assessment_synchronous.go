package memoryservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

// SynchronousAssessmentDependencies are the provider-facing dependencies for a
// request-owned Remember assessment. No durable ledger or assessment
// repository is needed until the provider has returned a valid response.
type SynchronousAssessmentDependencies struct {
	Catalog  SubmissionAssessmentCatalog
	Provider assessor.Provider
	Limits   assessor.SemanticAssessmentLimits
	Metrics  observability.DiscoverabilityMetrics
	Logger   observability.LogProvider
}

// RememberAssessmentScope carries authenticated request identity into the
// assessor without exposing storage workflow identifiers.
type RememberAssessmentScope struct {
	TeamID          string
	OwnerProfileID  string
	IngestID        string
	SpaceID         string
	SpaceGeneration int64
}

type RememberAssessmentItem struct {
	ItemID     string
	Fragment   repository.EvidenceFragment
	EvidenceID string
}

// RememberAssessmentSnapshot is the in-memory request snapshot used by the
// assessor. IDs are generated before provider work and reused by the final
// durable commit.
type RememberAssessmentSnapshot struct {
	Scope    RememberAssessmentScope
	Proposal map[string]any
	Evidence []repository.EvidenceFragment
	Items    []RememberAssessmentItem
}

// SynchronousAssessmentInput is the in-memory request snapshot used by the
// assessor.
type SynchronousAssessmentInput struct {
	Scope    RememberAssessmentScope
	Snapshot RememberAssessmentSnapshot
}

// SynchronousAssessmentResult contains the validated whole response and its
// prepared request. The private plan is intentionally retained here so the
// existing deterministic commit-input builder remains the only policy path.
type SynchronousAssessmentResult struct {
	Response   assessor.SemanticAssessmentResponse
	Request    assessor.SemanticAssessmentRequest
	Plan       submissionAssessmentPlan
	Assessment repository.SubmissionAssessment
}

// SynchronousRememberCommitRequest contains the request-owned values needed
// to build the final repository transaction after provider work. The builder
// deliberately emits no workflow identity; evidence IDs are resolved in the
// resolved by its durable evidence fragment instead.
type SynchronousRememberCommitRequest struct {
	TeamID          string
	OwnerProfileID  string
	IngestID        string
	SpaceID         string
	SpaceGeneration int64
	IdempotencyKey  string
	RequestHash     string
	SourceSummary   string
	Proposal        map[string]any
	Metadata        map[string]any
	Evidence        []repository.EvidenceInput
	Assessment      *SynchronousAssessmentResult
	Duration        time.Duration
}

// BuildSynchronousRememberCommitInput converts the validated assessor result
// into the final request-owned transaction contract.
func BuildSynchronousRememberCommitInput(input SynchronousRememberCommitRequest) (repository.SynchronousRememberCommitInput, error) {
	if input.Assessment == nil {
		return repository.SynchronousRememberCommitInput{}, errors.New("synchronous assessment: prepared result is required")
	}
	securityResults, err := BuildSynchronousRememberEvidenceSecurityResults(input.Assessment)
	if err != nil {
		return repository.SynchronousRememberCommitInput{}, err
	}
	scope := repository.RememberCommitScope{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID}
	commit, err := submissionAssessmentCommitInput(scope, input.Assessment.Plan, input.Assessment.Response, &input.Assessment.Assessment, false)
	if err != nil {
		return repository.SynchronousRememberCommitInput{}, err
	}
	assessment := input.Assessment.Assessment
	providerTurns := assessment.ProviderTurns
	if providerTurns < 1 {
		providerTurns = 1
	}
	return repository.SynchronousRememberCommitInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, IdempotencyKey: input.IdempotencyKey,
		RequestHash:   input.RequestHash,
		SourceSummary: input.SourceSummary, Proposal: input.Proposal,
		Metadata: input.Metadata, Evidence: append([]repository.EvidenceInput(nil), input.Evidence...),
		AssessmentID: assessment.AssessmentID, AssessmentJSON: append(json.RawMessage(nil), assessment.NormalizedResponse...),
		EvidenceSecurityResults: append([]repository.EvidenceSecurityResult(nil), securityResults...),
		ProviderTurns:           providerTurns, InputTokens: assessment.InputTokens, OutputTokens: assessment.OutputTokens,
		AssessorTurns: providerTurns, Duration: input.Duration,
		Commit: commit,
	}, nil
}

// BuildSynchronousRememberEvidenceSecurityResults turns the validated assessor
// evidence-security table into one complete security disposition for every
// evidence item. A pass is safe; a reject is unsafe and is handled as a
// whole-batch policy failure by the Remember processor.
func BuildSynchronousRememberEvidenceSecurityResults(prepared *SynchronousAssessmentResult) ([]repository.EvidenceSecurityResult, error) {
	if prepared == nil {
		return nil, errors.New("synchronous assessment: prepared result is required")
	}
	signalsByEvidenceID := make(map[string][]repository.SecuritySignalInput)
	securityResultsByEvidenceID := make(map[string]assessor.SemanticAssessmentEvidenceSecurityResult, len(prepared.Response.EvidenceSecurityResults))
	for _, result := range prepared.Response.EvidenceSecurityResults {
		if _, exists := securityResultsByEvidenceID[result.EvidenceID]; exists {
			return nil, errors.New("submission assessor security result is duplicated")
		}
		securityResultsByEvidenceID[result.EvidenceID] = result
		for _, signal := range result.Signals {
			item, ok := prepared.Plan.itemsByEvidenceID[result.EvidenceID]
			if !ok {
				return nil, errors.New("submission assessor security result references unknown evidence")
			}
			quote, err := assessor.SemanticEvidenceSpan(item.Fragment.Content, signal.Start, signal.End)
			if err != nil {
				return nil, err
			}
			signalsByEvidenceID[result.EvidenceID] = append(signalsByEvidenceID[result.EvidenceID], repository.SecuritySignalInput{
				Kind: signal.Kind, Severity: "high", SpanStart: signal.Start, SpanEnd: signal.End, Quote: quote,
			})
		}
	}
	results := make([]repository.EvidenceSecurityResult, 0, len(prepared.Plan.Items))
	for _, item := range prepared.Plan.Items {
		securityResult, ok := securityResultsByEvidenceID[item.EvidenceID]
		if !ok {
			return nil, errors.New("submission assessor security result is missing")
		}
		signals := append([]repository.SecuritySignalInput(nil), signalsByEvidenceID[item.EvidenceID]...)
		decision := strings.TrimSpace(securityResult.Decision)
		if decision != "pass" && decision != "reject" {
			return nil, errors.New("submission assessor security result decision is unsupported")
		}
		if decision == "pass" && len(signals) > 0 {
			return nil, errors.New("submission assessor security result pass contains unsafe signals")
		}
		if decision == "reject" && len(signals) == 0 {
			return nil, errors.New("submission assessor reject result has no security signal")
		}
		results = append(results, repository.EvidenceSecurityResult{
			FragmentID: item.Fragment.FragmentID, EvidenceID: item.EvidenceID,
			EvidenceIndex: item.Fragment.EvidenceIndex, Decision: decision,
			Safe: decision == "pass", Signals: signals,
		})
	}
	return results, nil
}

// AssessSynchronousRemember performs catalog loading, one assessor
// conversation, and bounded complete-response repair without writing intake,
// assessment, or semantic rows.
func AssessSynchronousRemember(
	ctx context.Context,
	deps SynchronousAssessmentDependencies,
	input SynchronousAssessmentInput,
) (*SynchronousAssessmentResult, error) {
	assessmentCtx, cancel := rememberapp.ContextForPhase(ctx, rememberapp.RememberPhaseAssessment)
	defer cancel()
	ctx = assessmentCtx
	if deps.Catalog == nil {
		return nil, errors.New("synchronous assessment: catalog is required")
	}
	if deps.Provider == nil {
		return nil, errors.New("synchronous assessment: provider is required")
	}
	if strings.TrimSpace(input.Scope.TeamID) == "" || strings.TrimSpace(input.Scope.OwnerProfileID) == "" {
		return nil, errors.New("synchronous assessment: authenticated scope is required")
	}
	plan, err := buildSubmissionAssessmentPlan(input.Snapshot)
	if err != nil {
		return nil, err
	}
	concrete := newSynchronousAssessmentEngine(deps, input.Scope.TeamID, input.Scope.OwnerProfileID)
	request, err := concrete.buildRequest(ctx, input.Scope, plan, input.Snapshot.Proposal)
	if err != nil {
		return nil, normalizeSynchronousAssessmentPreflightError(err)
	}
	refresh := func(refreshCtx context.Context) (assessor.SemanticAssessmentRequest, error) {
		request, err := concrete.buildRequest(refreshCtx, input.Scope, plan, input.Snapshot.Proposal)
		if err != nil {
			return assessor.SemanticAssessmentRequest{}, normalizeSynchronousAssessmentPreflightError(err)
		}
		return request, nil
	}
	providerCtx := observability.WithMetricIdentity(ctx, input.Scope.TeamID, input.Scope.OwnerProfileID)
	providerCtx = observability.WithAIOperation(providerCtx, observability.AIOperationSemanticAssessment, 1)
	started := time.Now()
	response, _, finalRequest, err := concrete.assessRememberSession(providerCtx, request, refresh, 0)
	if err != nil {
		providerTurns := SynchronousAssessmentProviderTurns(err)
		outcome := "provider_error"
		if errors.Is(err, assessor.ErrVerifierMalformedResponse) {
			outcome = "malformed_exhausted"
		}
		observability.RecordAssessorCall(deps.Metrics, request.InputTokens, 0, time.Since(started).Seconds(), outcome)
		var mapped error
		if errors.Is(err, context.DeadlineExceeded) {
			mapped = fmt.Errorf("%w: assessor phase exceeded 160 seconds", rememberapp.ErrRememberRequestTimeout)
		} else if errors.Is(err, context.Canceled) {
			mapped = context.Canceled
		} else if errors.Is(err, rememberapp.ErrRememberInputBudgetExceeded) {
			mapped = fmt.Errorf("%w: refreshed assessor input exceeded the deterministic budget", rememberapp.ErrRememberInputBudgetExceeded)
		} else if errors.Is(err, assessor.ErrVerifierMalformedResponse) {
			mapped = fmt.Errorf("%w: complete assessor response remained invalid", rememberapp.ErrRememberProviderResponseInvalid)
		} else {
			mapped = fmt.Errorf("%w: assessor provider request failed", rememberapp.ErrRememberProviderUnavailable)
		}
		if providerTurns > 0 {
			mapped = &submissionAssessmentConsumedTurnsError{cause: mapped, providerTurns: providerTurns}
		}
		return nil, mapped
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("synchronous assessment: encode response: %w", err)
	}
	canonicalJSON, err := assessor.CanonicalJSON(encoded)
	if err != nil {
		return nil, fmt.Errorf("synchronous assessment: canonicalize response: %w", err)
	}
	assessmentID := uuid.NewString()
	providerTurns := response.ProviderTurns
	if providerTurns < 1 {
		providerTurns = 1
	}
	inputTokens := response.InputTokens
	if inputTokens <= 0 {
		inputTokens = finalRequest.InputTokens
	}
	observability.RecordAssessorCall(deps.Metrics, inputTokens, response.OutputTokens, time.Since(started).Seconds(), "ok")
	now := time.Now().UTC()
	assessment := repository.SubmissionAssessment{
		TeamID: input.Scope.TeamID, AssessmentID: assessmentID, OwnerProfileID: input.Scope.OwnerProfileID,
		IngestID:  input.Scope.IngestID,
		RequestID: finalRequest.RequestID, AssessorContractVersion: domain.ContractVersion,
		Model: deps.Provider.ModelName(), Tokenizer: assessmentTokenizer(deps.Limits),
		RevisionNumber: 1, ProviderTurns: providerTurns, InputTokens: inputTokens,
		OutputTokens: response.OutputTokens, CandidateContextTokens: finalRequest.CandidateContextTokens,
		CandidateContextTruncated: finalRequest.CandidateContextTruncated,
		NormalizedResponse:        canonicalJSON, ResponseHash: semanticAssessmentHash(canonicalJSON),
		ValidatedAt: now, CreatedAt: now,
	}
	return &SynchronousAssessmentResult{Response: response, Request: finalRequest, Plan: plan, Assessment: assessment}, nil
}

func normalizeSynchronousAssessmentPreflightError(err error) error {
	if err == nil {
		return nil
	}
	stage, _ := semanticAssessmentPreflightFailure(err)
	switch stage {
	case "entity_catalog", "catalog_context", "assessment_input", "predicate_options_overflow":
		return fmt.Errorf("%w: %v", rememberapp.ErrRememberInputBudgetExceeded, err)
	default:
		return err
	}
}
