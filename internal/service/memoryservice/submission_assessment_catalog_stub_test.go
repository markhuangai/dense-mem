package memoryservice

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type submissionAssessmentWorkerCatalogStub struct {
	entityInputs          []repository.SubmissionAssessmentEntityCatalogInput
	predicateInputs       []repository.SemanticReviewPredicateResolutionInput
	predicateOptionInputs []repository.SemanticAssessmentPredicateOptionsInput
	predicateOptions      []repository.SemanticReviewPredicateCandidate
	entityComplete        bool
	predicateComplete     bool
}

func (s *submissionAssessmentWorkerCatalogStub) ListSubmissionAssessmentEntityCatalog(_ context.Context, input repository.SubmissionAssessmentEntityCatalogInput) (repository.SubmissionAssessmentEntityCatalogResult, error) {
	s.entityInputs = append(s.entityInputs, input)
	groups := make([]repository.SubmissionAssessmentEntityCatalogGroup, 0, len(input.Entities))
	for _, entity := range input.Entities {
		groups = append(groups, repository.SubmissionAssessmentEntityCatalogGroup{Ref: entity.Ref, Candidates: []repository.SemanticReviewEntityCandidate{}, Complete: true})
	}
	return repository.SubmissionAssessmentEntityCatalogResult{Groups: groups, Complete: s.entityComplete}, nil
}

func (s *submissionAssessmentWorkerCatalogStub) ResolveSemanticReviewPredicateCandidates(_ context.Context, input repository.SemanticReviewPredicateResolutionInput) ([]repository.SemanticReviewPredicateResolution, error) {
	s.predicateInputs = append(s.predicateInputs, input)
	if !s.predicateComplete {
		resolutions := make([]repository.SemanticReviewPredicateResolution, 0, verifier.SemanticAssessmentMaxPredicateOptions+1)
		for index := 0; index <= verifier.SemanticAssessmentMaxPredicateOptions; index++ {
			resolutions = append(resolutions, repository.SemanticReviewPredicateResolution{
				RequestedPredicate: "overflow",
				MatchKind:          "key",
				Candidate: repository.SemanticReviewPredicateCandidate{
					PredicateKey:        fmt.Sprintf("overflow_%d", index),
					Version:             1,
					AllowedSubjectKinds: []string{"concept"},
					AllowedObjectKinds:  []string{"concept"},
					RelationshipKind:    "state",
					CurrentCardinality:  "many",
					LifecycleState:      "active",
				},
			})
		}
		return resolutions, nil
	}
	resolutions := make([]repository.SemanticReviewPredicateResolution, 0, len(input.Predicates))
	for _, requested := range input.Predicates {
		for _, candidate := range s.predicateOptions {
			if candidate.PredicateKey != requested {
				continue
			}
			resolutions = append(resolutions, repository.SemanticReviewPredicateResolution{
				RequestedPredicate: requested,
				MatchKind:          "key",
				Candidate:          candidate,
			})
		}
	}
	return resolutions, nil
}

func (s *submissionAssessmentWorkerCatalogStub) ListSemanticAssessmentPredicateOptions(_ context.Context, input repository.SemanticAssessmentPredicateOptionsInput) ([]repository.SemanticReviewPredicateCandidate, error) {
	s.predicateOptionInputs = append(s.predicateOptionInputs, input)
	return append([]repository.SemanticReviewPredicateCandidate(nil), s.predicateOptions...), nil
}
