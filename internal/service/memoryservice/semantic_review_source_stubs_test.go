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
	placement             *repository.CreateIngestResult
	input                 repository.GetPlacementRunInput
	conflictContextInputs []repository.ValidateRelationshipConflictContextInput
	conflictContextErr    error
}

func (s *reviewSourceLedgerStub) CreateIngest(context.Context, repository.CreateIngestInput) (*repository.CreateIngestResult, error) {
	return nil, errors.New("unexpected CreateIngest")
}

func (s *reviewSourceLedgerStub) GetPlacementRun(_ context.Context, input repository.GetPlacementRunInput) (*repository.CreateIngestResult, error) {
	s.input = input
	return s.placement, nil
}

func (s *reviewSourceLedgerStub) ValidateRelationshipConflictContext(_ context.Context, input repository.ValidateRelationshipConflictContextInput) error {
	s.conflictContextInputs = append(s.conflictContextInputs, input)
	return s.conflictContextErr
}

func (s *reviewSourceLedgerStub) AdvanceSourceRevision(context.Context, repository.AdvanceSourceRevisionInput) (*repository.SourceRevisionResult, error) {
	return nil, errors.New("unexpected AdvanceSourceRevision")
}

func (s *reviewSourceLedgerStub) AppendSecurityEvent(context.Context, repository.SecurityEventInput) (string, error) {
	return "", errors.New("unexpected AppendSecurityEvent")
}

func (s *reviewSourceLedgerStub) AppendPlacementOutcome(context.Context, repository.PlacementOutcomeInput) (string, error) {
	return "", errors.New("unexpected AppendPlacementOutcome")
}

func (s *reviewSourceLedgerStub) ClaimNextPlacementRun(context.Context, string, string, time.Duration) (*repository.PlacementRun, error) {
	return nil, errors.New("unexpected ClaimNextPlacementRun")
}

func (s *reviewSourceLedgerStub) FinishPlacementRun(context.Context, string, string, string, string, string) error {
	return errors.New("unexpected FinishPlacementRun")
}

type reviewSourceCatalogStub struct {
	predicateOptions     []string
	predicateOptionsErr  error
	entityCandidates     map[string][]repository.SemanticReviewEntityCandidate
	predicateCandidates  map[string][]repository.SemanticReviewPredicateCandidate
	ensurePredicateCalls int
}

func (s *reviewSourceCatalogStub) ListSemanticReviewEntityCandidates(_ context.Context, input repository.SemanticReviewEntityCandidateInput) ([]repository.SemanticReviewEntityCandidate, error) {
	return append([]repository.SemanticReviewEntityCandidate(nil), s.entityCandidates[input.Name]...), nil
}

func (s *reviewSourceCatalogStub) ListSemanticReviewPredicateCandidates(_ context.Context, input repository.SemanticReviewPredicateCandidateInput) ([]repository.SemanticReviewPredicateCandidate, error) {
	return append([]repository.SemanticReviewPredicateCandidate(nil), s.predicateCandidates[input.Predicate]...), nil
}

func (s *reviewSourceCatalogStub) ResolveSemanticReviewPredicateCandidates(_ context.Context, input repository.SemanticReviewPredicateResolutionInput) ([]repository.SemanticReviewPredicateResolution, error) {
	out := []repository.SemanticReviewPredicateResolution{}
	for _, predicate := range input.Predicates {
		for _, candidate := range s.predicateCandidates[predicate] {
			matchKind := "alias"
			if candidate.PredicateKey == predicate {
				matchKind = "key"
			}
			out = append(out, repository.SemanticReviewPredicateResolution{
				RequestedPredicate: predicate,
				MatchKind:          matchKind,
				Candidate:          candidate,
			})
		}
	}
	return out, nil
}

func (s *reviewSourceCatalogStub) ListSemanticReviewPredicateOptions(context.Context, repository.SemanticReviewPredicateOptionsInput) ([]string, error) {
	if s.predicateOptionsErr != nil {
		return nil, s.predicateOptionsErr
	}
	return append([]string(nil), s.predicateOptions...), nil
}

func (s *reviewSourceCatalogStub) EnsureSemanticReviewPredicateCandidate(_ context.Context, input repository.EnsureSemanticPredicateCandidateInput) (*repository.SemanticReviewPredicateCandidate, error) {
	s.ensurePredicateCalls++
	candidate := repository.SemanticReviewPredicateCandidate{
		PredicateKey:        strings.ToLower(strings.ReplaceAll(strings.TrimSpace(input.Predicate), " ", "_")),
		Version:             1,
		AllowedSubjectKinds: []string{strings.TrimSpace(input.SubjectKind)},
		AllowedObjectKinds:  []string{strings.TrimSpace(input.ObjectKind)},
		RelationshipKind:    strings.TrimSpace(input.RelationshipKind),
		CurrentCardinality:  "many",
		LifecycleState:      "active",
	}
	if s.predicateCandidates == nil {
		s.predicateCandidates = map[string][]repository.SemanticReviewPredicateCandidate{}
	}
	s.predicateCandidates[input.Predicate] = append(s.predicateCandidates[input.Predicate], candidate)
	return &candidate, nil
}

type reviewSourceProposalProviderStub struct {
	req       verifier.ProviderProposalRequest
	reqs      []verifier.ProviderProposalRequest
	proposal  verifier.ProviderProposal
	proposals []verifier.ProviderProposal
	errs      []error
	err       error
}

func (s *reviewSourceProposalProviderStub) ProposeSemantic(_ context.Context, req verifier.ProviderProposalRequest) (verifier.ProviderProposal, error) {
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
