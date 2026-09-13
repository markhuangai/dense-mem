package repository

import (
	"context"
	"errors"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

func semanticKnowledgeOwner(r *SemanticRepositoryImpl) (*knowledgepostgres.Store, error) {
	if r == nil {
		return nil, errors.New("semantic: knowledge write owner is required")
	}
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("semantic: knowledge write owner is required")
	}
	return owner, nil
}

func (r *SemanticRepositoryImpl) ListSubmissionAssessmentEntityCatalog(
	ctx context.Context,
	input SubmissionAssessmentEntityCatalogInput,
) (SubmissionAssessmentEntityCatalogResult, error) {
	owner, err := semanticKnowledgeOwner(r)
	if err != nil {
		return SubmissionAssessmentEntityCatalogResult{}, err
	}
	nativeInput := knowledgecontract.SubmissionAssessmentEntityCatalogInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, SpaceID: input.SpaceID,
		CandidateLimit: input.CandidateLimit,
		Entities:       make([]knowledgecontract.SubmissionAssessmentEntityCatalogTarget, len(input.Entities)),
	}
	for i, target := range input.Entities {
		nativeInput.Entities[i] = knowledgecontract.SubmissionAssessmentEntityCatalogTarget{
			Ref: target.Ref, Surface: target.Surface, EntityKind: target.EntityKind, KnownEntityID: target.KnownEntityID,
		}
	}
	result, err := owner.ListSubmissionAssessmentEntityCatalog(ctx, nativeInput)
	if err != nil {
		return SubmissionAssessmentEntityCatalogResult{}, err
	}
	legacy := SubmissionAssessmentEntityCatalogResult{
		Complete: result.Complete,
		Groups:   make([]SubmissionAssessmentEntityCatalogGroup, len(result.Groups)),
	}
	for i, group := range result.Groups {
		legacy.Groups[i] = SubmissionAssessmentEntityCatalogGroup{
			Ref: group.Ref, Complete: group.Complete,
			Candidates: make([]SemanticReviewEntityCandidate, len(group.Candidates)),
		}
		for j, candidate := range group.Candidates {
			legacy.Groups[i].Candidates[j] = SemanticReviewEntityCandidate{
				TeamID: candidate.TeamID, EntityID: candidate.EntityID, EntityKind: candidate.EntityKind,
				CanonicalName: candidate.CanonicalName, ActiveNames: append([]string(nil), candidate.ActiveNames...),
				IdentityContext: candidate.IdentityContext, Status: candidate.Status,
			}
		}
	}
	return legacy, nil
}

func (r *SemanticRepositoryImpl) ResolveSemanticReviewPredicateCandidates(
	ctx context.Context,
	input SemanticReviewPredicateResolutionInput,
) ([]SemanticReviewPredicateResolution, error) {
	owner, err := semanticKnowledgeOwner(r)
	if err != nil {
		return nil, err
	}
	result, err := owner.ResolveSemanticReviewPredicateCandidates(ctx, knowledgecontract.SemanticReviewPredicateResolutionInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID,
		Predicates: append([]string(nil), input.Predicates...), Limit: input.Limit,
	})
	if err != nil {
		return nil, err
	}
	legacy := make([]SemanticReviewPredicateResolution, len(result))
	for i, resolution := range result {
		legacy[i] = SemanticReviewPredicateResolution{
			RequestedPredicate: resolution.RequestedPredicate,
			MatchKind:          resolution.MatchKind,
			Candidate:          legacySemanticReviewPredicateCandidate(resolution.Candidate),
		}
	}
	return legacy, nil
}

func (r *SemanticRepositoryImpl) ListSemanticAssessmentPredicateOptions(
	ctx context.Context,
	input SemanticAssessmentPredicateOptionsInput,
) ([]SemanticReviewPredicateCandidate, error) {
	owner, err := semanticKnowledgeOwner(r)
	if err != nil {
		return nil, err
	}
	result, err := owner.ListSemanticAssessmentPredicateOptions(ctx, knowledgecontract.SemanticAssessmentPredicateOptionsInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, QueryText: input.QueryText,
		ProposedKeys: append([]string(nil), input.ProposedKeys...), Limit: input.Limit,
	})
	if err != nil {
		return nil, err
	}
	legacy := make([]SemanticReviewPredicateCandidate, len(result))
	for i, candidate := range result {
		legacy[i] = legacySemanticReviewPredicateCandidate(candidate)
	}
	return legacy, nil
}

func (r *SemanticRepositoryImpl) ListSubmissionAssessmentKnownEvidence(
	ctx context.Context,
	input SubmissionAssessmentKnownEvidenceInput,
) (SubmissionAssessmentKnownEvidenceResult, error) {
	owner, err := semanticKnowledgeOwner(r)
	if err != nil {
		return SubmissionAssessmentKnownEvidenceResult{}, err
	}
	result, err := owner.ListSubmissionAssessmentKnownEvidence(ctx, knowledgecontract.SubmissionAssessmentKnownEvidenceInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, SpaceID: input.SpaceID,
		EvidenceIDs: append([]string(nil), input.EvidenceIDs...),
	})
	if err != nil {
		return SubmissionAssessmentKnownEvidenceResult{}, err
	}
	return SubmissionAssessmentKnownEvidenceResult{
		Evidence: fromKnowledgeSubmissionAssessmentKnownEvidenceList(result.Evidence),
	}, nil
}

func legacySemanticReviewPredicateCandidate(candidate knowledgecontract.SemanticReviewPredicateCandidate) SemanticReviewPredicateCandidate {
	return SemanticReviewPredicateCandidate{
		PredicateKey: candidate.PredicateKey, Version: candidate.Version,
		Aliases:             append([]string(nil), candidate.Aliases...),
		AllowedSubjectKinds: append([]string(nil), candidate.AllowedSubjectKinds...),
		AllowedObjectKinds:  append([]string(nil), candidate.AllowedObjectKinds...),
		RelationshipKind:    candidate.RelationshipKind, CurrentCardinality: candidate.CurrentCardinality,
		LifecycleState: candidate.LifecycleState,
	}
}
