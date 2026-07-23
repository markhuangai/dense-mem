package memoryservice

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type v2ReviewStringer string

func (s v2ReviewStringer) String() string {
	return string(s)
}

type v2ReviewSourceLedgerStub struct {
	placement *repository.V2CreateIngestResult
	input     repository.V2GetPlacementRunInput
}

func (s *v2ReviewSourceLedgerStub) CreateIngest(context.Context, repository.V2CreateIngestInput) (*repository.V2CreateIngestResult, error) {
	return nil, errors.New("unexpected CreateIngest")
}

func (s *v2ReviewSourceLedgerStub) GetPlacementRun(_ context.Context, input repository.V2GetPlacementRunInput) (*repository.V2CreateIngestResult, error) {
	s.input = input
	return s.placement, nil
}

func (s *v2ReviewSourceLedgerStub) AdvanceSourceRevision(context.Context, repository.V2AdvanceSourceRevisionInput) (*repository.V2SourceRevisionResult, error) {
	return nil, errors.New("unexpected AdvanceSourceRevision")
}

func (s *v2ReviewSourceLedgerStub) AppendSecurityEvent(context.Context, repository.V2SecurityEventInput) (string, error) {
	return "", errors.New("unexpected AppendSecurityEvent")
}

func (s *v2ReviewSourceLedgerStub) AppendPlacementOutcome(context.Context, repository.V2PlacementOutcomeInput) (string, error) {
	return "", errors.New("unexpected AppendPlacementOutcome")
}

func (s *v2ReviewSourceLedgerStub) ClaimNextPlacementRun(context.Context, string, string, time.Duration) (*repository.V2PlacementRun, error) {
	return nil, errors.New("unexpected ClaimNextPlacementRun")
}

func (s *v2ReviewSourceLedgerStub) FinishPlacementRun(context.Context, string, string, string, string, string) error {
	return errors.New("unexpected FinishPlacementRun")
}

type v2ReviewSourceCatalogStub struct {
	predicateOptions    []string
	entityCandidates    map[string][]repository.V2SemanticReviewEntityCandidate
	predicateCandidates map[string][]repository.V2SemanticReviewPredicateCandidate
}

func (s *v2ReviewSourceCatalogStub) ListV2SemanticReviewEntityCandidates(_ context.Context, input repository.V2SemanticReviewEntityCandidateInput) ([]repository.V2SemanticReviewEntityCandidate, error) {
	return append([]repository.V2SemanticReviewEntityCandidate(nil), s.entityCandidates[input.Name]...), nil
}

func (s *v2ReviewSourceCatalogStub) ListV2SemanticReviewPredicateCandidates(_ context.Context, input repository.V2SemanticReviewPredicateCandidateInput) ([]repository.V2SemanticReviewPredicateCandidate, error) {
	return append([]repository.V2SemanticReviewPredicateCandidate(nil), s.predicateCandidates[input.Predicate]...), nil
}

func (s *v2ReviewSourceCatalogStub) ListV2SemanticReviewPredicateOptions(context.Context, repository.V2SemanticReviewPredicateOptionsInput) ([]string, error) {
	return append([]string(nil), s.predicateOptions...), nil
}

func (s *v2ReviewSourceCatalogStub) EnsureV2SemanticReviewPredicateCandidate(_ context.Context, input repository.V2EnsureSemanticPredicateCandidateInput) (*repository.V2SemanticReviewPredicateCandidate, error) {
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

type v2ReviewSourceProposalProviderStub struct {
	req       verifier.V2ProviderProposalRequest
	reqs      []verifier.V2ProviderProposalRequest
	proposal  verifier.V2ProviderProposal
	proposals []verifier.V2ProviderProposal
	errs      []error
	err       error
}

func (s *v2ReviewSourceProposalProviderStub) ProposeV2Semantic(_ context.Context, req verifier.V2ProviderProposalRequest) (verifier.V2ProviderProposal, error) {
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

func (s *v2ReviewSourceProposalProviderStub) ModelName() string {
	return "proposal-stub"
}

func v2SemanticReviewSourceStringPtr(value string) *string {
	return &value
}
