package memoryservice

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

func (s *submissionAssessmentPlacementWorkerService) buildRequest(
	ctx context.Context,
	run repository.PlacementRun,
	plan submissionAssessmentPlan,
	proposal map[string]any,
) (verifier.SemanticAssessmentRequest, error) {
	entityInputs := make([]repository.SubmissionAssessmentEntityCatalogTarget, 0, len(plan.EntityTargets))
	contract := verifier.SemanticAssessmentSubmissionContract{
		Entities:      make([]verifier.SemanticAssessmentRequiredEntityRef, 0, len(plan.EntityTargets)),
		Relationships: make([]verifier.SemanticAssessmentRequiredRelationshipRef, 0, len(plan.RelationshipTargets)),
	}
	for _, entity := range plan.EntityTargets {
		contract.Entities = append(contract.Entities, entity.Target)
		entityInputs = append(entityInputs, repository.SubmissionAssessmentEntityCatalogTarget{
			Ref:           entity.Target.Ref,
			Surface:       entity.Target.Surface,
			EntityKind:    entity.Target.Kind,
			KnownEntityID: entity.KnownEntityID,
		})
	}
	for _, relationship := range plan.RelationshipTargets {
		contract.Relationships = append(contract.Relationships, relationship.Target)
	}
	entityCatalog, err := s.catalog.ListSubmissionAssessmentEntityCatalog(ctx, repository.SubmissionAssessmentEntityCatalogInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		Entities:       entityInputs,
		CandidateLimit: s.limits.MaxCandidatesPerSurface,
	})
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, fmt.Errorf("load submission entity catalog: %w", err)
	}
	if !entityCatalog.Complete {
		return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError("catalog_context_overflow", "submission entity catalog exceeds its configured complete bound")
	}
	entityGroups, err := submissionAssessmentEntityCandidateGroups(plan, entityCatalog)
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError("catalog_context_validation", "submission entity catalog is invalid")
	}
	predicateOptions, err := s.submissionAssessmentPredicateOptions(ctx, run, plan)
	if err != nil {
		return verifier.SemanticAssessmentRequest{}, err
	}
	evidence := make([]verifier.SemanticReviewEvidence, 0, len(plan.Items))
	for _, item := range plan.Items {
		evidence = append(evidence, semanticReviewEvidence(item.Fragment, item.EvidenceID))
	}
	req := verifier.SemanticAssessmentRequest{
		RequestID:                "submission-assessment:" + run.PlacementRunID,
		TeamID:                   run.TeamID,
		OwnerProfileID:           run.OwnerProfileID,
		Evidence:                 evidence,
		ClientProposal:           assessmentClientProposalWithoutTrustedContext(proposal),
		EntityCandidateGroups:    entityGroups,
		PredicateOptions:         predicateOptions,
		RequiredRelationshipRefs: append([]verifier.SemanticAssessmentRequiredRelationshipRef(nil), contract.Relationships...),
		SubmissionContract:       &contract,
	}
	prepared, validationErrors := verifier.PrepareSemanticAssessmentRequest(req, s.limits)
	if len(validationErrors) == 0 {
		return prepared, nil
	}
	for _, validationError := range validationErrors {
		switch validationError.Field {
		case "candidate_context_tokens":
			return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError("catalog_context_overflow", "submission assessment catalog exceeds its configured token budget")
		case "input_tokens":
			return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError("assessment_input_overflow", "submission assessment input exceeds its configured token budget")
		}
	}
	return verifier.SemanticAssessmentRequest{}, deterministicSemanticAssessmentPreflightError("trusted_context_validation", "submission assessment contract is invalid")
}

func (s *submissionAssessmentPlacementWorkerService) submissionAssessmentPredicateOptions(
	ctx context.Context,
	run repository.PlacementRun,
	plan submissionAssessmentPlan,
) ([]verifier.SemanticAssessmentPredicateOption, error) {
	limit := s.limits.MaxPredicateOptions
	if limit <= 0 {
		limit = verifier.DefaultSemanticAssessmentLimits().MaxPredicateOptions
	}
	if limit > verifier.SemanticAssessmentMaxPredicateOptions {
		limit = verifier.SemanticAssessmentMaxPredicateOptions
	}

	proposedKeys := make([]string, 0, len(plan.RelationshipTargets))
	seenKeys := make(map[string]struct{}, len(plan.RelationshipTargets))
	queryParts := make([]string, 0, len(plan.Items)+len(plan.RelationshipTargets))
	for _, item := range plan.Items {
		queryParts = append(queryParts, item.Fragment.Content)
	}
	for _, relationship := range plan.RelationshipTargets {
		key := strings.TrimSpace(relationship.ProposedPredicate)
		if key == "" {
			continue
		}
		queryParts = append(queryParts, key)
		if _, exists := seenKeys[key]; exists {
			continue
		}
		seenKeys[key] = struct{}{}
		proposedKeys = append(proposedKeys, key)
	}

	resolved, err := s.catalog.ResolveSemanticReviewPredicateCandidates(ctx, repository.SemanticReviewPredicateResolutionInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		Predicates:     proposedKeys,
		Limit:          limit + 1,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve submission predicate candidates: %w", err)
	}

	candidates := make([]repository.SemanticReviewPredicateCandidate, 0, limit)
	seenCandidates := make(map[string]struct{}, len(resolved))
	appendCandidate := func(candidate repository.SemanticReviewPredicateCandidate) {
		if strings.TrimSpace(candidate.PredicateKey) == "" || candidate.Version < 1 || candidate.LifecycleState != "active" {
			return
		}
		key := fmt.Sprintf("%s:%d", candidate.PredicateKey, candidate.Version)
		if _, exists := seenCandidates[key]; exists {
			return
		}
		seenCandidates[key] = struct{}{}
		candidates = append(candidates, candidate)
	}
	for _, resolution := range resolved {
		appendCandidate(resolution.Candidate)
	}
	if len(candidates) > limit {
		return nil, deterministicSemanticAssessmentPreflightError("predicate_options_overflow", "submission predicate candidates exceed the configured request bound")
	}

	ranked, err := s.catalog.ListSemanticAssessmentPredicateOptions(ctx, repository.SemanticAssessmentPredicateOptionsInput{
		TeamID:         run.TeamID,
		OwnerProfileID: run.OwnerProfileID,
		QueryText:      strings.Join(queryParts, "\n"),
		ProposedKeys:   proposedKeys,
		Limit:          limit,
	})
	if err != nil {
		return nil, fmt.Errorf("load submission predicate options: %w", err)
	}
	for _, candidate := range ranked {
		if len(candidates) == limit {
			break
		}
		appendCandidate(candidate)
	}

	options := make([]verifier.SemanticAssessmentPredicateOption, 0, len(candidates))
	for _, candidate := range candidates {
		options = append(options, verifier.SemanticAssessmentPredicateOption{
			PredicateKey:        candidate.PredicateKey,
			Version:             candidate.Version,
			Aliases:             append([]string(nil), candidate.Aliases...),
			AllowedSubjectKinds: append([]string(nil), candidate.AllowedSubjectKinds...),
			AllowedObjectKinds:  append([]string(nil), candidate.AllowedObjectKinds...),
			RelationshipKind:    candidate.RelationshipKind,
			CurrentCardinality:  candidate.CurrentCardinality,
		})
	}
	return options, nil
}

func submissionAssessmentEntityCandidateGroups(
	plan submissionAssessmentPlan,
	catalog repository.SubmissionAssessmentEntityCatalogResult,
) ([]verifier.SemanticAssessmentEntityCandidateGroup, error) {
	groupsByRef := make(map[string]repository.SubmissionAssessmentEntityCatalogGroup, len(catalog.Groups))
	for _, group := range catalog.Groups {
		if _, exists := groupsByRef[group.Ref]; exists || !group.Complete {
			return nil, errors.New("entity catalog group is duplicated or incomplete")
		}
		groupsByRef[group.Ref] = group
	}
	groups := make([]verifier.SemanticAssessmentEntityCandidateGroup, 0, len(plan.EntityTargets))
	for _, entity := range plan.EntityTargets {
		catalogGroup, ok := groupsByRef[entity.Target.Ref]
		if !ok {
			return nil, errors.New("entity catalog target is missing")
		}
		if len(catalogGroup.Candidates) > verifier.SemanticAssessmentMaxEntityCandidatesPerSurface {
			return nil, errors.New("entity catalog candidate bound is exceeded")
		}
		group := verifier.SemanticAssessmentEntityCandidateGroup{
			Surface:    entity.Target.Surface,
			EvidenceID: entity.Target.EvidenceID,
			Start:      entity.Target.Start,
			End:        entity.Target.End,
			Candidates: make([]verifier.SemanticAssessmentEntityCandidate, 0, len(catalogGroup.Candidates)),
		}
		for _, candidate := range catalogGroup.Candidates {
			identityContext := map[string]any{}
			for key, value := range candidate.IdentityContext {
				identityContext[key] = value
			}
			group.Candidates = append(group.Candidates, verifier.SemanticAssessmentEntityCandidate{
				EntityID:        candidate.EntityID,
				CanonicalName:   candidate.CanonicalName,
				Kind:            candidate.EntityKind,
				IdentityContext: identityContext,
			})
		}
		groups = append(groups, group)
	}
	if len(groupsByRef) != len(groups) {
		return nil, errors.New("entity catalog includes an unknown target")
	}
	return groups, nil
}
