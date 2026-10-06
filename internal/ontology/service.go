package ontology

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type Service struct {
	repository contract.OrganizationRepository
	provider   *assessment.Provider
}

func NewService(repository contract.OrganizationRepository, provider *assessment.Provider) *Service {
	return &Service{repository: repository, provider: provider}
}

type OrganizationError struct {
	Code  string
	Cause error
}

func (e *OrganizationError) Error() string { return "ontology organization failed: " + e.Code }
func (e *OrganizationError) Unwrap() error { return e.Cause }

func (s *Service) Organize(ctx context.Context, teamID string, input contract.OrganizationInput) (contract.OrganizationResult, error) {
	if _, ok := requestctx.ActorFromContext(ctx); ok {
		return contract.OrganizationResult{}, contract.ErrUnauthorized
	}
	if s == nil || s.repository == nil || s.provider == nil {
		return contract.OrganizationResult{}, &OrganizationError{Code: "configuration_invalid"}
	}
	input, err := contract.PrepareOrganizationInput(input)
	if err != nil {
		return contract.OrganizationResult{}, err
	}
	identity := s.provider.Identity()
	if cached, found, err := s.repository.FindOrganization(ctx, teamID, input, identity); err != nil {
		return cached, err
	} else if found {
		return organizationResult(cached, nil)
	}
	sourceContext, contextErr := s.repository.ReadOrganization(ctx, teamID, input.Sources)
	if contextErr != nil && !errors.Is(contextErr, contract.ErrContextBound) {
		return contract.OrganizationResult{}, contextErr
	}
	inputHash, err := contract.OrganizationInputHash(input, identity)
	if err != nil {
		return contract.OrganizationResult{}, err
	}
	receipt := contract.OrganizationReceipt{ID: uuid.NewString(), OperationKey: input.OperationKey, InputHash: inputHash, ProviderIdentity: identity}
	receipt.ContextHash, err = contract.OrganizationContextHash(sourceContext)
	if err != nil {
		return contract.OrganizationResult{}, err
	}
	receipt.Result = contract.OrganizationResult{AssessmentID: receipt.ID, Outcomes: []contract.OrganizationOutcome{}, Attempts: []contract.AssessmentAttempt{}}
	for _, source := range sourceContext.Sources {
		fingerprint, err := contract.SourceFingerprint(source)
		if err != nil {
			return receipt.Result, err
		}
		receipt.Sources = append(receipt.Sources, contract.SourceDependency{SourceHandle: source.SourceHandle, Fingerprint: fingerprint})
		receipt.Result.Outcomes = append(receipt.Result.Outcomes, contract.OrganizationOutcome{Source: source.SourceHandle, Status: "unchanged"})
	}
	receipt.BatchHash, err = contract.OrganizationBatchHash(receipt.Sources, identity)
	if err != nil {
		return receipt.Result, err
	}
	if contextErr != nil {
		return s.finish(ctx, teamID, receipt, contract.Publication{}, contextErr)
	}
	request, binding, err := s.request(sourceContext, &receipt)
	if err != nil {
		return s.finish(ctx, teamID, receipt, contract.Publication{}, err)
	}
	receipt.Dependencies, err = organizationDependencies(sourceContext)
	if err != nil {
		return s.finish(ctx, teamID, receipt, contract.Publication{}, err)
	}
	response := assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{}, Items: []assessment.Decision{}, Equivalence: []assessment.Equivalence{}}
	if len(request.Items) > 0 {
		deterministic := true
		for _, item := range request.Items {
			if item.LockedDefinitionRef == "" {
				deterministic = false
			}
			response.Items = append(response.Items, assessment.Decision{Ref: item.Ref, Status: "classified", DefinitionRef: item.LockedDefinitionRef})
		}
		for _, pair := range request.Pairs {
			if pair.RequiredRelation == "" {
				deterministic = false
			}
			response.Equivalence = append(response.Equivalence, assessment.Equivalence{Ref: pair.Ref, Relation: pair.RequiredRelation})
		}
		if deterministic {
			err = assessment.Validate(request, response)
		} else {
			response, receipt.Result.Attempts, err = s.provider.Assess(ctx, request)
		}
		if err != nil {
			return s.finish(ctx, teamID, receipt, contract.Publication{}, err)
		}
	}
	for _, definition := range response.Definitions {
		heads, err := s.repository.ReadDefinitionHeads(ctx, teamID, proposedDefinition(teamID, definition))
		if err != nil {
			return s.finish(ctx, teamID, receipt, contract.Publication{}, err)
		}
		sourceContext.Records = append(sourceContext.Records, heads...)
	}
	publication, err := buildPublication(teamID, sourceContext, request, response, binding, &receipt)
	return s.finish(ctx, teamID, receipt, publication, err)
}

func organizationDependencies(context contract.OrganizationContext) ([]contract.RevisionRef, error) {
	var result []contract.RevisionRef
	seen := map[string]bool{}
	for _, view := range append(append([]contract.RecordView(nil), context.Records...), context.Candidates...) {
		if !view.Current || seen[view.ID] {
			continue
		}
		if len(result) == contract.MaxDependencyRecords {
			return nil, fmt.Errorf("%w: organization dependency context exceeds bound", contract.ErrInvalid)
		}
		seen[view.ID] = true
		result = append(result, contract.RevisionRef{ID: view.ID, Version: view.Version})
	}
	return result, nil
}

func (s *Service) finish(ctx context.Context, teamID string, receipt contract.OrganizationReceipt, publication contract.Publication, cause error) (contract.OrganizationResult, error) {
	if err := ctx.Err(); err != nil {
		return s.recordCancellation(ctx, teamID, receipt, err)
	}
	if cause != nil {
		markFailure(&receipt, cause)
	}
	result, err := s.repository.CommitOrganization(ctx, teamID, receipt, publication)
	if err != nil {
		if ctx.Err() != nil {
			return s.recordCancellation(ctx, teamID, receipt, ctx.Err())
		}
		if cause != nil {
			return receipt.Result, fmt.Errorf("record organization failure: %w", err)
		}
		markFailure(&receipt, err)
		result, recordErr := s.repository.CommitOrganization(ctx, teamID, receipt, contract.Publication{})
		if recordErr != nil {
			return receipt.Result, fmt.Errorf("record rejected organization publication: %w", recordErr)
		}
		return organizationResult(result, err)
	}
	return organizationResult(result, cause)
}

func (s *Service) recordCancellation(ctx context.Context, teamID string, receipt contract.OrganizationReceipt, cause error) (contract.OrganizationResult, error) {
	markFailure(&receipt, cause)
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	result, err := s.repository.CommitOrganization(recordCtx, teamID, receipt, contract.Publication{})
	if err != nil {
		return receipt.Result, fmt.Errorf("record cancelled organization: %w", errors.Join(cause, err))
	}
	return organizationResult(result, cause)
}

func organizationResult(result contract.OrganizationResult, cause error) (contract.OrganizationResult, error) {
	if result.FailureCode != "" {
		return result, &OrganizationError{Code: result.FailureCode, Cause: cause}
	}
	return result, nil
}

func markFailure(receipt *contract.OrganizationReceipt, cause error) {
	code := "internal_failure"
	maintenanceCode := contract.MaintenanceFailureCode(cause)
	switch {
	case maintenanceCode != "":
		code = maintenanceCode
	case errors.Is(cause, context.Canceled):
		code = "request_cancelled"
	case errors.Is(cause, context.DeadlineExceeded):
		code = "request_timeout"
	case errors.Is(cause, modelprovider.ErrVerifierMalformedResponse):
		code = "provider_response_invalid"
	case errors.Is(cause, modelprovider.ErrVerifierProvider), errors.Is(cause, modelprovider.ErrVerifierRateLimit), errors.Is(cause, modelprovider.ErrVerifierTimeout):
		code = "provider_unavailable"
	case errors.Is(cause, contract.ErrSourceStale):
		code = "stale_input"
	case errors.Is(cause, contract.ErrConflict):
		code = "commit_conflict"
	case errors.Is(cause, contract.ErrOverride):
		code = "manager_override_conflict"
	case errors.Is(cause, contract.ErrInvalid):
		code = "invalid_publication"
	}
	receipt.Result.FailureCode = code
	receipt.Result.Publication = nil
	receipt.Result.AmbiguousComparisons = nil
	receipt.Result.Current = false
	for i := range receipt.Result.Outcomes {
		outcome := &receipt.Result.Outcomes[i]
		preflightAmbiguity := outcome.Status == "ambiguous" && (outcome.Reason == "resubmit_complete_group" || outcome.Reason == "required_classification_unavailable")
		if outcome.Status != "oversized" && outcome.Status != "unavailable" && !preflightAmbiguity {
			outcome.Status = "failed"
			outcome.Reason = code
			outcome.RecordIDs = nil
		}
	}
}
