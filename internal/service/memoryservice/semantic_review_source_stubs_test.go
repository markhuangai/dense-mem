package memoryservice

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type reviewStringer string

func (s reviewStringer) String() string {
	return string(s)
}

type reviewSourceLedgerStub struct {
	placement *repository.V2CreateIngestResult
	input     repository.V2GetPlacementRunInput
}

func (s *reviewSourceLedgerStub) CreateIngest(context.Context, repository.V2CreateIngestInput) (*repository.V2CreateIngestResult, error) {
	return nil, errors.New("unexpected CreateIngest")
}

func (s *reviewSourceLedgerStub) GetPlacementRun(_ context.Context, input repository.V2GetPlacementRunInput) (*repository.V2CreateIngestResult, error) {
	s.input = input
	return s.placement, nil
}

func (s *reviewSourceLedgerStub) AdvanceSourceRevision(context.Context, repository.V2AdvanceSourceRevisionInput) (*repository.V2SourceRevisionResult, error) {
	return nil, errors.New("unexpected AdvanceSourceRevision")
}

func (s *reviewSourceLedgerStub) AppendSecurityEvent(context.Context, repository.V2SecurityEventInput) (string, error) {
	return "", errors.New("unexpected AppendSecurityEvent")
}

func (s *reviewSourceLedgerStub) AppendPlacementOutcome(context.Context, repository.V2PlacementOutcomeInput) (string, error) {
	return "", errors.New("unexpected AppendPlacementOutcome")
}

func (s *reviewSourceLedgerStub) ClaimNextPlacementRun(context.Context, string, string, time.Duration) (*repository.V2PlacementRun, error) {
	return nil, errors.New("unexpected ClaimNextPlacementRun")
}

func (s *reviewSourceLedgerStub) FinishPlacementRun(context.Context, string, string, string, string, string) error {
	return errors.New("unexpected FinishPlacementRun")
}

type reviewSourceCatalogStub struct {
	predicateOptions     []string
	predicateOptionsErr  error
	entityCandidates     map[string][]repository.V2SemanticReviewEntityCandidate
	predicateCandidates  map[string][]repository.V2SemanticReviewPredicateCandidate
	ensurePredicateCalls int
}

func (s *reviewSourceCatalogStub) ListV2SemanticReviewEntityCandidates(_ context.Context, input repository.V2SemanticReviewEntityCandidateInput) ([]repository.V2SemanticReviewEntityCandidate, error) {
	return append([]repository.V2SemanticReviewEntityCandidate(nil), s.entityCandidates[input.Name]...), nil
}

func (s *reviewSourceCatalogStub) ListV2SemanticReviewPredicateCandidates(_ context.Context, input repository.V2SemanticReviewPredicateCandidateInput) ([]repository.V2SemanticReviewPredicateCandidate, error) {
	return append([]repository.V2SemanticReviewPredicateCandidate(nil), s.predicateCandidates[input.Predicate]...), nil
}

func (s *reviewSourceCatalogStub) ResolveV2SemanticReviewPredicateCandidates(_ context.Context, input repository.V2SemanticReviewPredicateResolutionInput) ([]repository.V2SemanticReviewPredicateResolution, error) {
	out := []repository.V2SemanticReviewPredicateResolution{}
	for _, predicate := range input.Predicates {
		for _, candidate := range s.predicateCandidates[predicate] {
			matchKind := "alias"
			if candidate.PredicateKey == predicate {
				matchKind = "key"
			}
			out = append(out, repository.V2SemanticReviewPredicateResolution{
				RequestedPredicate: predicate,
				MatchKind:          matchKind,
				Candidate:          candidate,
			})
		}
	}
	return out, nil
}

func (s *reviewSourceCatalogStub) ListV2SemanticReviewPredicateOptions(context.Context, repository.V2SemanticReviewPredicateOptionsInput) ([]string, error) {
	if s.predicateOptionsErr != nil {
		return nil, s.predicateOptionsErr
	}
	return append([]string(nil), s.predicateOptions...), nil
}

func (s *reviewSourceCatalogStub) EnsureV2SemanticReviewPredicateCandidate(_ context.Context, input repository.V2EnsureSemanticPredicateCandidateInput) (*repository.V2SemanticReviewPredicateCandidate, error) {
	s.ensurePredicateCalls++
	candidate := repository.V2SemanticReviewPredicateCandidate{
		PredicateKey:        strings.ToLower(strings.ReplaceAll(strings.TrimSpace(input.Predicate), " ", "_")),
		Version:             1,
		AllowedSubjectKinds: []string{strings.TrimSpace(input.SubjectKind)},
		AllowedObjectKinds:  []string{strings.TrimSpace(input.ObjectKind)},
		RelationshipKind:    strings.TrimSpace(input.RelationshipKind),
		CurrentCardinality:  "many",
		LifecycleState:      "active",
	}
	if s.predicateCandidates == nil {
		s.predicateCandidates = map[string][]repository.V2SemanticReviewPredicateCandidate{}
	}
	s.predicateCandidates[input.Predicate] = append(s.predicateCandidates[input.Predicate], candidate)
	return &candidate, nil
}

type reviewSourceProposalProviderStub struct {
	req       verifier.V2ProviderProposalRequest
	reqs      []verifier.V2ProviderProposalRequest
	proposal  verifier.V2ProviderProposal
	proposals []verifier.V2ProviderProposal
	errs      []error
	err       error
}

func (s *reviewSourceProposalProviderStub) ProposeV2Semantic(_ context.Context, req verifier.V2ProviderProposalRequest) (verifier.V2ProviderProposal, error) {
	s.req = req
	s.reqs = append(s.reqs, req)
	err := s.err
	if len(s.errs) > 0 {
		index := len(s.reqs) - 1
		if index >= len(s.errs) {
			index = len(s.errs) - 1
		}
		err = s.errs[index]
	}
	if len(s.proposals) > 0 {
		index := len(s.reqs) - 1
		if index >= len(s.proposals) {
			index = len(s.proposals) - 1
		}
		return s.proposals[index], err
	}
	return s.proposal, err
}

func (s *reviewSourceProposalProviderStub) ModelName() string {
	return "proposal-stub"
}

func semanticReviewSourceStringPtr(value string) *string {
	return &value
}
