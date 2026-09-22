package dream

import (
	"context"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

type cycleAppConfigStub struct {
	cfg domain.DreamingRuntimeConfig
}

func (s cycleAppConfigStub) DreamingRuntimeConfig(context.Context) (domain.DreamingRuntimeConfig, error) {
	return s.cfg, nil
}

type dreamGeneratorStub struct {
	model     string
	generated []GeneratedDream
	err       error
	lastReq   GenerateRequest
	calls     int
}

func (s *dreamGeneratorStub) Generate(_ context.Context, _ string, req GenerateRequest) ([]GeneratedDream, error) {
	s.calls++
	s.lastReq = req
	return s.generated, s.err
}

func (s *dreamGeneratorStub) Model() string {
	if s.model != "" {
		return s.model
	}
	return "stub-model"
}

type testStringer string

func (s testStringer) String() string {
	return string(s)
}

type evidenceRepositoryStub struct {
	targets         []dreamcontract.EvidenceDiscoveryTargetInput
	evaluations     []dreamcontract.EvidenceDiscoveryEvaluationInput
	runTotals       dreamcontract.EvidenceDiscoveryRunTotals
	runTotalsErr    error
	attemptPasses   map[string]int
	lastLimit       int
	lastMaxContexts int
	targetsErr      error
	validateErr     error
	validateErrs    []error
	lockErr         error
	dispatchedErr   error
	validatedErr    error
	persistErr      error
	persistErrs     []error
	abandonErr      error
	validatedCalls  int
	dispatchedCalls int
	abandonCalls    int
	abandonCanceled bool
	validateCalls   int
	validatedInputs []dreamcontract.EvidenceContext
}

type errorAppConfigStub struct{ err error }

func (s errorAppConfigStub) DreamingRuntimeConfig(context.Context) (domain.DreamingRuntimeConfig, error) {
	return domain.DreamingRuntimeConfig{}, s.err
}

type errorTeamServiceStub struct{ err error }

func (s *errorTeamServiceStub) GetByID(context.Context, uuid.UUID) (*domain.Team, error) {
	return nil, s.err
}

func (s *errorTeamServiceStub) List(context.Context, int, int) ([]*domain.Team, error) {
	return nil, s.err
}

type evidenceTeamServiceStub struct {
	team *domain.Team
}

func (s *evidenceTeamServiceStub) GetByID(context.Context, uuid.UUID) (*domain.Team, error) {
	return s.team, nil
}

func (s *evidenceTeamServiceStub) List(context.Context, int, int) ([]*domain.Team, error) {
	if s.team == nil {
		return nil, nil
	}
	return []*domain.Team{s.team}, nil
}

func (s *evidenceRepositoryStub) ListEvidenceDiscoveryTargets(_ context.Context, _ string, limit, maxContexts int) ([]dreamcontract.EvidenceDiscoveryTargetInput, error) {
	s.lastLimit, s.lastMaxContexts = limit, maxContexts
	if s.targetsErr != nil {
		return nil, s.targetsErr
	}
	return append([]dreamcontract.EvidenceDiscoveryTargetInput(nil), s.targets...), nil
}

func (s *evidenceRepositoryStub) ValidateEvidenceDiscoveryInputs(_ context.Context, _ string, _ dreamcontract.EvidenceTarget, contexts []dreamcontract.EvidenceContext) error {
	s.validateCalls++
	s.validatedInputs = append([]dreamcontract.EvidenceContext(nil), contexts...)
	if len(s.validateErrs) > 0 {
		err := s.validateErrs[0]
		s.validateErrs = s.validateErrs[1:]
		return err
	}
	return s.validateErr
}

func (s *evidenceRepositoryStub) LoadEvidenceDiscoveryRunTotals(_ context.Context, _, _ string) (dreamcontract.EvidenceDiscoveryRunTotals, error) {
	return s.runTotals, s.runTotalsErr
}

func (s *evidenceRepositoryStub) PersistEvidenceDiscoveryEvaluation(_ context.Context, input dreamcontract.EvidenceDiscoveryEvaluationInput) (dreamcontract.DreamGenerationPersistResult, error) {
	if len(s.persistErrs) > 0 {
		err := s.persistErrs[0]
		s.persistErrs = s.persistErrs[1:]
		if err != nil {
			return dreamcontract.DreamGenerationPersistResult{}, err
		}
	}
	if s.persistErr != nil {
		return dreamcontract.DreamGenerationPersistResult{}, s.persistErr
	}
	s.evaluations = append(s.evaluations, input)
	return dreamcontract.DreamGenerationPersistResult{Created: len(input.Proposals)}, nil
}

func (s *evidenceRepositoryStub) WithEvidenceDiscoveryTargetLock(_ context.Context, _ string, targetID, contentHash string, fn func(dreamcontract.EvidenceDiscoveryAttempt) error) error {
	if s.lockErr != nil {
		return s.lockErr
	}
	if s.attemptPasses == nil {
		s.attemptPasses = map[string]int{}
	}
	key := targetID + ":" + contentHash
	pass := s.attemptPasses[key] + 1
	s.attemptPasses[key] = pass
	if pass > evidenceDiscoveryPassLimit {
		return fn(dreamcontract.EvidenceDiscoveryAttempt{})
	}
	return fn(dreamcontract.EvidenceDiscoveryAttempt{AttemptID: uuid.NewString(), ReservationToken: uuid.NewString(), PassNumber: pass})
}

func (s *evidenceRepositoryStub) MarkEvidenceDiscoveryAttemptValidated(_ context.Context, _ dreamcontract.EvidenceDiscoveryAttemptValidationInput) error {
	s.validatedCalls++
	if s.validatedErr != nil {
		return s.validatedErr
	}
	return nil
}

func (s *evidenceRepositoryStub) MarkEvidenceDiscoveryAttemptDispatched(_ context.Context, _ dreamcontract.EvidenceDiscoveryAttemptValidationInput) error {
	s.dispatchedCalls++
	if s.dispatchedErr != nil {
		return s.dispatchedErr
	}
	return nil
}

func (s *evidenceRepositoryStub) AbandonEvidenceDiscoveryAttempt(ctx context.Context, _, _, _ string) error {
	s.abandonCalls++
	s.abandonCanceled = ctx.Err() != nil
	if s.abandonErr != nil {
		return s.abandonErr
	}
	return nil
}

type evidenceGeneratorStub struct {
	model              string
	generated          []GeneratedDream
	generatedResponses [][]GeneratedDream
	generatedCalls     int
	skipAdmission      bool
	err                error
	errorDiagnostics   GenerationDiagnostics
	diagnostics        []GenerationDiagnostics
	requests           []EvidenceGenerationRequest
}

func (s *evidenceGeneratorStub) GenerateEvidence(ctx context.Context, _ string, request EvidenceGenerationRequest) ([]GeneratedDream, GenerationDiagnostics, error) {
	s.requests = append(s.requests, request)
	if !s.skipAdmission {
		if err := modelprovider.NotifyAdmission(ctx); err != nil {
			return nil, GenerationDiagnostics{}, err
		}
	}
	if s.err != nil {
		return nil, s.errorDiagnostics, s.err
	}
	callIndex := s.generatedCalls
	generated := append([]GeneratedDream(nil), s.generated...)
	if callIndex < len(s.generatedResponses) {
		generated = append([]GeneratedDream(nil), s.generatedResponses[callIndex]...)
	}
	s.generatedCalls++
	for index := range generated {
		for derivationIndex := range generated[index].EvidenceDerivations {
			derivation := &generated[index].EvidenceDerivations[derivationIndex]
			if derivation.EvidenceID == "" || derivation.EvidenceID != request.Target.EvidenceID {
				derivation.EvidenceID = request.Target.EvidenceID
				derivation.FragmentID = request.Target.FragmentID
				derivation.SourceGroupKey = request.Target.SourceGroupKey
				derivation.Authority = request.Target.Authority
				derivation.Quote = "target"
			}
		}
	}
	diagnostics := GenerationDiagnostics{ProviderTurns: 1, ProviderProposals: len(generated)}
	if callIndex < len(s.diagnostics) {
		diagnostics = s.diagnostics[callIndex]
	}
	return generated, diagnostics, nil
}

func (s *evidenceGeneratorStub) Model() string { return s.model }
