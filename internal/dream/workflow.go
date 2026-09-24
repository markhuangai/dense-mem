package dream

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

var ErrDreamAuthContext = errors.New("dream: authenticated actor context is required")

type dreamGenerationResult struct {
	proposals                  []dreamcontract.UpsertHypothesisInput
	rejected                   int
	paths                      []DreamPath
	model                      string
	candidatePaths             int
	candidateTargets           int
	availableTargets           int
	previouslyAssessedPaths    int
	targetLookupFailed         bool
	pathAssessmentLookupFailed bool
	providerTurns              int
	providerInputTokens        int
	providerOutputTokens       int
	providerProposals          int
	providerFailed             bool
	persistencePolicyRejected  int
	providerPayload            []byte
	providerCaptureState       string
	providerCaptureReason      string
	diagnosticPhasesTruncated  bool
	diagnosticPhases           []runDiagnosticPhase
}

func (s *service) runTeamCycle(
	ctx context.Context,
	teamID string,
	initiatedByProfileID string,
	cfg EffectiveConfig,
	req RunCycleRequest,
	scheduled bool,
	scheduledWindowAt time.Time,
) (*RunCycleResult, error) {
	started := time.Now()
	result, err := s.runTeamCycleCore(ctx, teamID, initiatedByProfileID, cfg, req, scheduled, scheduledWindowAt)
	s.recordDreamCycleMetrics(ctx, string(domain.DreamLaneGraph), started, result, err)
	return result, err
}

func (s *service) runTeamCycleCore(
	ctx context.Context,
	teamID string,
	initiatedByProfileID string,
	cfg EffectiveConfig,
	req RunCycleRequest,
	scheduled bool,
	scheduledWindowAt time.Time,
) (*RunCycleResult, error) {
	started := s.now().UTC()
	runDate := localRunDate(started, cfg)
	if scheduled {
		runDate = localRunDate(scheduledWindowAt, cfg)
	}
	if req.MaxOutputs > 0 {
		cfg.MaxOutputs = req.MaxOutputs
	}
	result := &RunCycleResult{
		TeamID:    teamID,
		RunDate:   runDate,
		StartedAt: started,
		Lane:      domain.DreamLaneGraph,
		Status:    "running",
	}
	windowKey := runDate
	if req.Manual {
		windowKey = "manual:" + uuid.NewString()
	}
	leaseDuration := s.cycleLease(scheduled)
	var scheduledFor *time.Time
	if scheduled {
		window := scheduledWindowAt.UTC()
		scheduledFor = &window
	}
	claimInput := dreamcontract.DreamCycleClaimInput{
		TeamID:               teamID,
		InitiatedByProfileID: initiatedByProfileID,
		RunDate:              runDate,
		WindowKey:            windowKey,
		ScheduledFor:         scheduledFor,
		LeaseToken:           uuid.NewString(),
		LeaseUntil:           started.Add(leaseDuration),
		Lane:                 domain.DreamLaneGraph,
	}
	var (
		claimed *dreamcontract.DreamCycleRun
		err     error
	)
	if scheduled {
		claimed, err = s.deps.ScheduledStore.ClaimScheduledDreamCycle(ctx, claimInput)
	} else {
		claimed, err = s.deps.Store.ClaimDreamCycle(ctx, claimInput)
	}
	if err != nil {
		err = translateDreamRepositoryError(err)
		result.CompletedAt = s.now().UTC()
		result.Status = "error"
		result.Error = err.Error()
		return result, err
	}
	result.RunID = claimed.RunID
	result.Lane = claimed.Lane
	if !claimed.Claimed && !req.Manual {
		result.CompletedAt = s.now().UTC()
		result.Status = "skipped"
		return result, nil
	}
	if !cfg.Enabled && !req.Manual {
		result.CompletedAt = s.now().UTC()
		result.Status = "skipped"
		result.OutcomeSummary = map[string]int{"disabled_before_evaluation": 1}
		appendRunDiagnosticPhase(result, "target", "skipped", "dreaming_disabled", map[string]any{"enabled": false})
		if err := s.completeTeamCycle(ctx, scheduled, dreamcontract.DreamCycleCompleteInput{
			TeamID: teamID, InitiatedByProfileID: initiatedByProfileID, RunID: claimed.RunID,
			LeaseToken: claimed.LeaseToken, Status: "skipped", OutcomeSummary: result.OutcomeSummary,
			Lane: claimed.Lane,
		}); err != nil {
			result.Status = "error"
			result.Error = err.Error()
			s.recordRunDiagnosticAfterCompletion(ctx, result, err)
			return result, err
		}
		s.recordRunDiagnostic(ctx, result)
		return result, nil
	}
	return s.runClaimedTeamCycle(ctx, teamID, initiatedByProfileID, cfg, req, scheduled, result, claimed)
}

func (s *service) runClaimedTeamCycle(
	ctx context.Context,
	teamID string,
	initiatedByProfileID string,
	cfg EffectiveConfig,
	req RunCycleRequest,
	scheduled bool,
	result *RunCycleResult,
	claimed *dreamcontract.DreamCycleRun,
) (*RunCycleResult, error) {
	if claimed == nil {
		return result, errors.New("dreaming cycle: missing durable claim")
	}
	result.RunID = claimed.RunID
	result.RunDate = claimed.RunDate
	result.AttemptCount = claimed.AttemptCount
	if !claimed.StartedAt.IsZero() {
		result.StartedAt = claimed.StartedAt
	}
	if claimed.ScheduledFor != nil {
		result.ScheduledFor = *claimed.ScheduledFor
	}
	inputs, err := s.deps.Store.ListDreamInputs(ctx, dreamcontract.DreamInputListInput{
		TeamID: teamID,
		Limit:  cfg.MaxOutputs * 4,
	})
	if err != nil {
		err = translateDreamRepositoryError(err)
		result.CompletedAt = s.now().UTC()
		result.Status = "error"
		result.Error = err.Error()
		result.OutcomeSummary = map[string]int{"input_selection_error": 1}
		appendRunDiagnosticPhase(result, "target", "failed", err.Error(), map[string]any{"selection": "input_relationships"})
		appendRunDiagnosticPhase(result, "validation", "failed", err.Error(), map[string]any{"selection": "input_relationships"})
		completeErr := s.completeTeamCycle(ctx, scheduled, dreamcontract.DreamCycleCompleteInput{
			TeamID:                   teamID,
			InitiatedByProfileID:     initiatedByProfileID,
			RunID:                    claimed.RunID,
			LeaseToken:               claimed.LeaseToken,
			Status:                   "failed",
			OutcomeSummary:           map[string]int{"input_selection_error": 1},
			Error:                    result.Error,
			Lane:                     claimed.Lane,
			EvidenceTargets:          result.EvidenceTargets,
			EvaluatedEvidenceTargets: result.EvaluatedEvidenceTargets,
		})
		if completeErr != nil {
			s.recordRunDiagnosticAfterCompletion(ctx, result, completeErr)
			return result, errors.Join(err, completeErr)
		}
		s.recordRunDiagnostic(ctx, result)
		return result, err
	}
	created, rejected, generation, runErr := s.persistHypotheses(ctx, teamID, initiatedByProfileID, claimed.RunID, claimed.LeaseToken, inputs, req.SeedDreams, cfg.MaxOutputs, scheduled)
	result.CompletedAt = s.now().UTC()
	result.InputRelationships = len(inputs)
	result.CreatedDreams = created
	result.RejectedDreams = rejected
	applyDreamGenerationDiagnostics(result, generation, len(inputs), created, rejected)
	result.Status = "completed"
	completeStatus := "completed"
	if runErr != nil {
		runErr = translateDreamRepositoryError(runErr)
		result.Status = "error"
		result.Error = runErr.Error()
		completeStatus = "failed"
	}
	completeErr := s.completeTeamCycle(ctx, scheduled, dreamcontract.DreamCycleCompleteInput{
		TeamID:                   teamID,
		InitiatedByProfileID:     initiatedByProfileID,
		RunID:                    claimed.RunID,
		LeaseToken:               claimed.LeaseToken,
		Status:                   completeStatus,
		InputCount:               len(inputs),
		CreatedHypotheses:        created,
		RejectedHypotheses:       rejected,
		SourceSnapshot:           dreamInputSnapshot(inputs),
		ProviderModel:            result.ProviderModel,
		ProviderTurns:            result.ProviderTurns,
		ProviderInputTokens:      result.ProviderInputTokens,
		ProviderOutputTokens:     result.ProviderOutputTokens,
		AttemptedPaths:           result.AttemptedPaths,
		ProviderProposals:        result.ProviderProposals,
		OutcomeSummary:           result.OutcomeSummary,
		Error:                    result.Error,
		Lane:                     claimed.Lane,
		EvidenceTargets:          result.EvidenceTargets,
		EvaluatedEvidenceTargets: result.EvaluatedEvidenceTargets,
	})
	if completeErr != nil && runErr == nil {
		err = completeErr
		result.Status = "error"
		result.Error = err.Error()
		s.recordRunDiagnosticAfterCompletion(ctx, result, err)
		return result, err
	}
	if errors.Is(completeErr, dreamcontract.ErrDreamCycleLeaseLost) {
		return result, runErr
	}
	if completeErr != nil {
		s.recordRunDiagnosticAfterCompletion(ctx, result, completeErr)
	} else {
		s.recordRunDiagnostic(ctx, result)
	}
	return result, runErr
}

func (s *service) completeTeamCycle(ctx context.Context, scheduled bool, input dreamcontract.DreamCycleCompleteInput) error {
	var err error
	if scheduled {
		err = s.deps.ScheduledStore.CompleteScheduledDreamCycle(ctx, input)
	} else {
		err = s.deps.Store.CompleteDreamCycle(ctx, input)
	}
	if err != nil {
		return translateDreamRepositoryError(err)
	}
	return nil
}

func applyDreamGenerationDiagnostics(
	result *RunCycleResult,
	generation dreamGenerationResult,
	inputRelationships int,
	created int,
	rejected int,
) {
	if result == nil {
		return
	}
	result.ProviderModel = generation.model
	result.ProviderTurns = generation.providerTurns
	result.ProviderInputTokens = generation.providerInputTokens
	result.ProviderOutputTokens = generation.providerOutputTokens
	result.AttemptedPaths = len(generation.paths)
	result.ProviderProposals = generation.providerProposals
	result.providerPayload = append([]byte(nil), generation.providerPayload...)
	result.providerCaptureState = generation.providerCaptureState
	result.providerCaptureReason = generation.providerCaptureReason
	result.diagnosticPhasesTruncated = generation.diagnosticPhasesTruncated
	result.diagnosticPhases = append([]runDiagnosticPhase(nil), generation.diagnosticPhases...)
	blockedTargets := max(0, generation.candidateTargets-generation.availableTargets)
	if generation.targetLookupFailed {
		blockedTargets = 0
	}
	result.OutcomeSummary = map[string]int{
		"eligible_relationships":     inputRelationships,
		"two_hop_paths":              generation.candidatePaths,
		"candidate_targets":          generation.candidateTargets,
		"blocked_targets":            blockedTargets,
		"previously_assessed_paths":  generation.previouslyAssessedPaths,
		"target_lookup_error":        boolToInt(generation.targetLookupFailed),
		"path_assessment_error":      boolToInt(generation.pathAssessmentLookupFailed),
		"attempted_paths":            len(generation.paths),
		"provider_proposals":         generation.providerProposals,
		"provider_failed":            boolToInt(generation.providerFailed),
		"invalid_provider_proposals": generation.rejected,
		"policy_rejections":          generation.persistencePolicyRejected,
		"created_hypotheses":         created,
		"rejected_hypotheses":        rejected,
	}
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalizeDreamTeamID(teamID string) (string, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return "", fmt.Errorf("dreaming cycle: invalid team id: %w", err)
	}
	return teamID, nil
}

func translateDreamRepositoryError(err error) error {
	if errors.Is(err, dreamcontract.ErrTeamInactive) {
		return httperr.New(httperr.NOT_FOUND, "team not found")
	}
	return err
}

func (s *service) persistHypotheses(
	ctx context.Context,
	teamID string,
	createdByProfileID string,
	runID string,
	leaseToken string,
	inputs []dreamcontract.DreamInput,
	seeds []SeedDream,
	maxOutputs int,
	scheduled bool,
) (int, int, dreamGenerationResult, error) {
	if maxOutputs <= 0 {
		maxOutputs = DefaultMaxOutputs
	}
	byID := make(map[string]dreamcontract.DreamInput, len(inputs))
	for _, input := range inputs {
		byID[input.RelationshipID] = input
	}
	var proposals []dreamcontract.UpsertHypothesisInput
	generatedRejected := 0
	generatedPaths := []DreamPath{}
	generatorModel := ""
	generation := dreamGenerationResult{}
	if len(seeds) > 0 {
		proposals = dreamProposalsFromSeeds(seeds, byID, maxOutputs)
		generation.diagnosticPhases = append(generation.diagnosticPhases,
			runDiagnosticPhase{phase: "target", outcome: "selected", details: map[string]any{"source": "seed_dreams", "count": len(proposals)}},
			runDiagnosticPhase{phase: "proposal", outcome: "generated", details: map[string]any{"accepted": len(proposals), "rejected": 0}},
		)
	} else {
		generated, err := s.generateDreamProposals(ctx, teamID, inputs, maxOutputs)
		generation = generated
		if err != nil {
			return 0, 0, generation, err
		}
		proposals = generated.proposals
		generatedRejected = generated.rejected
		generatedPaths = generated.paths
		generatorModel = generated.model
	}
	created := 0
	rejected := generatedRejected
	if len(generatedPaths) > 0 {
		input := dreamcontract.DreamGenerationPersistInput{
			TeamID:             teamID,
			CreatedByProfileID: createdByProfileID,
			RunID:              runID,
			LeaseToken:         leaseToken,
			ProviderModel:      generatorModel,
			Proposals:          proposals,
			EvaluatedPaths:     dreamPathEvaluationInputs(generatedPaths),
		}
		var (
			persisted dreamcontract.DreamGenerationPersistResult
			err       error
		)
		if scheduled {
			persisted, err = s.deps.ScheduledStore.PersistScheduledDreamGeneration(ctx, input)
		} else {
			persisted, err = s.deps.Store.PersistDreamGeneration(ctx, input)
		}
		if err != nil {
			generation.diagnosticPhases = append(generation.diagnosticPhases, runDiagnosticPhase{
				phase: "disposition", outcome: "failed", cause: err.Error(),
				details: map[string]any{"accepted": persisted.Created, "rejected": persisted.Rejected},
			})
			return 0, rejected, generation, err
		}
		generation.persistencePolicyRejected = persisted.Rejected
		generation.diagnosticPhases = append(generation.diagnosticPhases,
			runDiagnosticPhase{phase: "disposition", outcome: diagnosticHypothesisDispositionOutcome(persisted.Created, rejected+persisted.Rejected), details: map[string]any{
				"created": persisted.Created, "rejected": rejected + persisted.Rejected,
			}},
		)
		return persisted.Created, rejected + persisted.Rejected, generation, nil
	}
	for _, proposal := range proposals {
		proposal.TeamID = teamID
		proposal.CreatedByProfileID = createdByProfileID
		proposal.RunID = runID
		var (
			record   *dreamcontract.HypothesisRecord
			inserted bool
			err      error
		)
		if scheduled {
			record, inserted, err = s.deps.ScheduledStore.UpsertScheduledHypothesis(ctx, proposal)
		} else {
			record, inserted, err = s.deps.Store.UpsertHypothesis(ctx, proposal)
		}
		if err != nil {
			if errors.Is(err, dreamcontract.ErrDreamExactRelationshipExists) ||
				errors.Is(err, dreamcontract.ErrDreamExactHypothesisExists) ||
				errors.Is(err, dreamcontract.ErrDreamSourceStale) {
				rejected++
				continue
			}
			return created, rejected, generation, err
		}
		if record != nil && inserted {
			created++
		}
	}
	generation.diagnosticPhases = append(generation.diagnosticPhases,
		runDiagnosticPhase{phase: "disposition", outcome: diagnosticHypothesisDispositionOutcome(created, rejected), details: map[string]any{
			"created": created, "rejected": rejected,
		}},
	)
	return created, rejected, generation, nil
}

func diagnosticDispositionOutcome(accepted, rejected int) string {
	switch {
	case accepted > 0 && rejected > 0:
		return "partially_accepted"
	case accepted > 0:
		return "accepted"
	case rejected > 0:
		return "rejected"
	default:
		return "no_change"
	}
}

func diagnosticHypothesisDispositionOutcome(created, rejected int) string {
	switch {
	case created > 0 && rejected > 0:
		return "partially_proposed"
	case created > 0:
		return "proposed"
	case rejected > 0:
		return "rejected"
	default:
		return "no_change"
	}
}

func (s *service) generateDreamProposals(
	ctx context.Context,
	teamID string,
	inputs []dreamcontract.DreamInput,
	maxOutputs int,
) (dreamGenerationResult, error) {
	if len(inputs) == 0 || s.deps.Generator == nil {
		return dreamGenerationResult{diagnosticPhases: []runDiagnosticPhase{
			{phase: "target", outcome: "evaluated_zero", details: map[string]any{"input_relationships": len(inputs)}},
			{phase: "proposal", outcome: "no_change", details: map[string]any{"accepted": 0, "rejected": 0}},
		}}, nil
	}
	predicates, err := s.deps.Store.ListDreamTargetPredicates(ctx, teamID)
	if err != nil {
		return dreamGenerationResult{diagnosticPhases: []runDiagnosticPhase{{phase: "target", outcome: "failed", cause: err.Error(), details: map[string]any{"selection": "predicate_lookup"}}}}, err
	}
	paths := buildDreamPaths(inputs, predicates, maxOutputs)
	result := dreamGenerationResult{candidatePaths: len(paths)}
	if len(paths) > 0 {
		targets := dreamTargetCandidates(paths)
		result.candidateTargets = len(targets)
		availableTargets, err := s.deps.Store.ListAvailableDreamTargets(ctx, teamID, targets)
		if err != nil {
			result.targetLookupFailed = true
			result.diagnosticPhases = append(result.diagnosticPhases, runDiagnosticPhase{phase: "target", outcome: "failed", cause: err.Error(), details: map[string]any{
				"candidate_paths": len(paths), "candidate_targets": result.candidateTargets, "selection": "target_lookup",
			}})
			return result, err
		}
		result.availableTargets = len(availableTargets)
		paths = dreamPathsForAvailableTargets(paths, availableTargets)
	}
	if len(paths) > 0 {
		beforeAssessment := len(paths)
		unassessed, err := s.deps.Store.ListUnassessedDreamPaths(ctx, teamID, dreamPathEvaluationInputs(paths))
		if err != nil {
			result.pathAssessmentLookupFailed = true
			result.diagnosticPhases = append(result.diagnosticPhases, runDiagnosticPhase{phase: "validation", outcome: "failed", cause: err.Error(), details: map[string]any{
				"candidate_paths": beforeAssessment, "selection": "path_assessment_lookup",
			}})
			return result, err
		}
		paths = dreamPathsForEvaluationInputs(paths, unassessed)
		result.previouslyAssessedPaths = beforeAssessment - len(paths)
	}
	result.diagnosticPhases = append(result.diagnosticPhases, runDiagnosticPhase{
		phase: "target", outcome: func() string {
			if len(paths) == 0 {
				return "evaluated_zero"
			}
			return "selected"
		}(),
		details: map[string]any{
			"candidate_paths": len(paths), "candidate_targets": result.candidateTargets,
			"available_targets": result.availableTargets, "previously_assessed_paths": result.previouslyAssessedPaths,
			"path_refs": dreamDiagnosticPathRefs(paths),
		},
	})
	if len(paths) == 0 {
		return result, nil
	}
	generator := s.deps.Generator
	model := generator.Model()
	if strings.TrimSpace(model) == "" {
		result.paths = paths
		result.providerFailed = true
		result.diagnosticPhases = append(result.diagnosticPhases, runDiagnosticPhase{phase: "provider", outcome: "failed", cause: "provider model unavailable", details: map[string]any{"path_count": len(paths)}})
		return result, ErrDreamProviderUnavailable
	}
	result.paths = paths
	result.model = model
	ctx = observability.WithMetricIdentity(ctx, teamID, "")
	ctx = observability.WithAIOperation(ctx, observability.AIOperationDreamGeneration, len(paths))
	request := GenerateRequest{
		MaxOutputs:     maxOutputs,
		Paths:          paths,
		GeneratorModel: model,
	}
	exchangeRecorder := newDreamDiagnosticExchangeRecorder(s.deps.DiagnosticProtector)
	ctx = withDreamDiagnosticRecorder(ctx, exchangeRecorder)
	ctx = modelprovider.WithExchangeRecorder(ctx, exchangeRecorder)
	var diagnostics GenerationDiagnostics
	var generated []GeneratedDream
	if generatorWithDiagnostics, ok := generator.(DiagnosticsGenerator); ok {
		providerStarted := time.Now()
		generated, diagnostics, err = generatorWithDiagnostics.GenerateWithDiagnostics(ctx, teamID, request)
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		observability.RecordDreamProviderAttempt(s.deps.Metrics, "graph_generation", outcome, time.Since(providerStarted))
	} else {
		providerStarted := time.Now()
		generated, err = generator.Generate(ctx, teamID, request)
		outcome := "ok"
		if err != nil {
			outcome = "error"
		}
		observability.RecordDreamProviderAttempt(s.deps.Metrics, "graph_generation", outcome, time.Since(providerStarted))
		diagnostics.ProviderProposals = len(generated)
	}
	if err != nil {
		result.providerPayload = exchangeRecorder.Payload()
		result.providerCaptureState, result.providerCaptureReason = exchangeRecorder.State()
		result.providerFailed = true
		result.diagnosticPhases = append(result.diagnosticPhases, runDiagnosticPhase{phase: "provider", outcome: "failed", cause: err.Error(), details: map[string]any{
			"model": model, "provider_turns": diagnostics.ProviderTurns, "provider_proposals": diagnostics.ProviderProposals,
		}})
		return result, err
	}
	proposals, rejected, rejectionReasons := dreamProposalsFromPathsWithReasons(generated, paths, maxOutputs, model)
	result.proposals = proposals
	result.rejected = rejected
	result.providerTurns = diagnostics.ProviderTurns
	result.providerInputTokens = diagnostics.ProviderInputTokens
	result.providerOutputTokens = diagnostics.ProviderOutputTokens
	result.providerProposals = diagnostics.ProviderProposals
	result.providerPayload = exchangeRecorder.Payload()
	result.providerCaptureState, result.providerCaptureReason = exchangeRecorder.State()
	result.diagnosticPhases = append(result.diagnosticPhases,
		runDiagnosticPhase{phase: "provider", outcome: "completed", details: map[string]any{
			"model": model, "provider_turns": diagnostics.ProviderTurns, "provider_proposals": diagnostics.ProviderProposals,
		}},
		runDiagnosticPhase{phase: "validation", outcome: diagnosticValidationOutcome(len(proposals), rejected), details: map[string]any{
			"accepted": len(proposals), "rejected": rejected, "rejection_reasons": rejectionReasons,
		}},
		runDiagnosticPhase{phase: "proposal", outcome: diagnosticValidationOutcome(len(proposals), rejected), details: map[string]any{
			"accepted": len(proposals), "rejected": rejected, "path_refs": dreamDiagnosticPathRefs(paths),
		}},
	)
	return result, nil
}

func diagnosticValidationOutcome(accepted, rejected int) string {
	return diagnosticDispositionOutcome(accepted, rejected)
}

func dreamDiagnosticPathRefs(paths []DreamPath) []string {
	refs := make([]string, 0, min(len(paths), 64))
	for _, path := range paths {
		if len(refs) >= 64 {
			break
		}
		if ref := strings.TrimSpace(path.PathRef); ref != "" {
			refs = append(refs, ref)
		}
	}
	return refs
}

func (s *service) listDreams(ctx context.Context, opts ListOptions) ([]*domain.Dream, string, error) {
	teamID, _, err := dreamActor(ctx)
	if err != nil {
		return nil, "", err
	}
	records, next, err := s.deps.Store.ListHypotheses(ctx, dreamcontract.ListHypothesesInput{
		TeamID:    teamID,
		Status:    opts.Status,
		Limit:     opts.Limit,
		Cursor:    opts.Cursor,
		Sort:      opts.Sort,
		Direction: opts.Direction,
	})
	if err != nil {
		return nil, "", err
	}
	return dreamRecords(records), next, nil
}

func (s *service) getDream(ctx context.Context, dreamID string) (*domain.Dream, error) {
	teamID, _, err := dreamActor(ctx)
	if err != nil {
		return nil, err
	}
	record, err := s.deps.Store.GetHypothesis(ctx, dreamcontract.GetHypothesisInput{
		TeamID:       teamID,
		HypothesisID: dreamID,
	})
	if err != nil {
		if errors.Is(err, dreamcontract.ErrDreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, err
	}
	return dreamRecord(record), nil
}

func (s *service) listRuns(ctx context.Context, limit int) ([]*RunCycleResult, error) {
	teamID, _, err := dreamActor(ctx)
	if err != nil {
		return nil, err
	}
	runs, err := s.deps.Store.ListDreamCyclesForTeam(ctx, teamID, limit)
	if err != nil {
		return nil, err
	}
	results := make([]*RunCycleResult, 0, len(runs))
	for i := range runs {
		results = append(results, cycleRunResult(&runs[i]))
	}
	return results, nil
}

func (s *service) recallDreams(ctx context.Context, query string, limit int) ([]*domain.Dream, error) {
	teamID, _, err := dreamActor(ctx)
	if err != nil {
		return nil, err
	}
	records, err := s.deps.Store.RecallHypotheses(ctx, dreamcontract.RecallHypothesesInput{
		TeamID: teamID,
		Query:  query,
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	return dreamRecords(records), nil
}

func (s *service) status(ctx context.Context) (*StatusResult, error) {
	teamID, _, err := dreamActor(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := s.effectiveConfigForTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	pending, err := s.deps.Store.CountHypotheses(ctx, teamID, string(domain.DreamStatusProposed))
	if err != nil {
		return nil, err
	}
	runs, err := s.deps.Store.ListDreamCyclesForTeam(ctx, teamID, 1)
	if err != nil {
		return nil, err
	}
	var latestResult *RunCycleResult
	if len(runs) > 0 {
		latestResult = cycleRunResult(&runs[0])
	}
	return &StatusResult{EffectiveConfig: cfg, LatestRun: latestResult, PendingCount: pending}, nil
}

func dreamActor(ctx context.Context) (string, string, error) {
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return "", "", ErrDreamAuthContext
	}
	return actor.TeamID.String(), actor.OwnerID.String(), nil
}

func dreamInputSnapshot(inputs []dreamcontract.DreamInput) []map[string]any {
	out := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, map[string]any{
			"relationship_id": input.RelationshipID,
			"version":         input.Version,
			"status":          input.Status,
		})
	}
	return out
}

func dreamRecords(records []dreamcontract.HypothesisRecord) []*domain.Dream {
	out := make([]*domain.Dream, 0, len(records))
	for i := range records {
		out = append(out, dreamRecord(&records[i]))
	}
	return out
}

func dreamRecord(record *dreamcontract.HypothesisRecord) *domain.Dream {
	if record == nil {
		return nil
	}
	return &domain.Dream{
		DreamID:                        record.HypothesisID,
		TeamID:                         record.TeamID,
		Hypothesis:                     record.Statement,
		WhatIf:                         anyString(record.Payload["what_if"]),
		PossibleOutcome:                anyString(record.Payload["possible_outcome"]),
		Rationale:                      record.Rationale,
		Likelihood:                     floatPtrValue(record.Likelihood),
		Confidence:                     floatPtrValue(record.Confidence),
		SourceOwnerProfileIDs:          append([]string(nil), record.SourceOwnerProfileIDs...),
		SubjectEntityID:                record.SubjectEntityID,
		PredicateKey:                   record.PredicateKey,
		ObjectEntityID:                 record.ObjectEntityID,
		ObjectValueID:                  record.ObjectValueID,
		SourceRelationshipIDs:          dreamSourceIDs(record.SourceRefs, false),
		SourceCandidateRelationshipIDs: dreamSourceIDs(record.SourceRefs, true),
		SourceVersions:                 copyDreamSourceVersions(record.SourceVersions),
		GeneratorKind:                  firstNonEmpty(record.GeneratorKind, "deterministic"),
		GeneratorVersion:               firstNonEmpty(record.GeneratorVersion, "dream-v2"),
		Status:                         domain.DreamStatus(record.Status),
		Lane:                           record.Lane,
		CycleRunID:                     record.CycleRunID,
		GeneratorModel:                 firstNonEmpty(record.GeneratorVersion, record.GeneratorKind),
		ContentHash:                    record.ContentHash,
		SourceRefs:                     dreamSourceRefs(record.SourceRefs),
		Derivations:                    dreamDerivations(record.Derivations),
		SourceEvidenceIDs:              append([]string(nil), record.SourceEvidenceIDs...),
		EvidenceDerivations:            dreamEvidenceDerivations(record.EvidenceDerivations),
		InvalidatedReason:              record.InvalidatedReason,
		CreatedAt:                      record.CreatedAt,
		UpdatedAt:                      record.UpdatedAt,
	}
}

func dreamDerivations(derivations []dreamcontract.DreamDerivationSource) []domain.DreamDerivation {
	out := make([]domain.DreamDerivation, 0, len(derivations))
	for _, derivation := range derivations {
		out = append(out, domain.DreamDerivation{
			PremisePosition:     derivation.PremisePosition,
			RelationshipID:      derivation.RelationshipID,
			RelationshipVersion: derivation.RelationshipVersion,
			SourceGroupKey:      derivation.SourceGroupKey,
			Quote:               derivation.Quote,
			Authority:           derivation.Authority,
		})
	}
	return out
}

func dreamEvidenceDerivations(derivations []dreamcontract.EvidenceDerivationSource) []domain.DreamEvidenceDerivation {
	out := make([]domain.DreamEvidenceDerivation, 0, len(derivations))
	for _, derivation := range derivations {
		out = append(out, domain.DreamEvidenceDerivation{
			EvidenceID:     derivation.EvidenceID,
			SourceGroupKey: derivation.SourceGroupKey,
			SpanStart:      derivation.SpanStart,
			SpanEnd:        derivation.SpanEnd,
			Quote:          derivation.Quote,
			Authority:      derivation.Authority,
		})
	}
	return out
}

func dreamSourceIDs(refs []map[string]any, candidates bool) []string {
	out := []string{}
	for _, ref := range refs {
		refType := strings.TrimSpace(anyString(ref["type"]))
		if candidates {
			if refType != "candidate_relationship" {
				continue
			}
		} else if refType != "relationship" {
			continue
		}
		id := strings.TrimSpace(anyString(ref["id"]))
		if id != "" {
			out = append(out, id)
		}
	}
	return out
}

func copyDreamSourceVersions(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cycleRunResult(run *dreamcontract.DreamCycleRun) *RunCycleResult {
	if run == nil {
		return nil
	}
	completed := time.Time{}
	if run.CompletedAt != nil {
		completed = *run.CompletedAt
	}
	scheduledFor := time.Time{}
	if run.ScheduledFor != nil {
		scheduledFor = *run.ScheduledFor
	}
	return &RunCycleResult{
		RunID:                    run.RunID,
		TeamID:                   run.TeamID,
		RunDate:                  run.RunDate,
		StartedAt:                run.StartedAt,
		CompletedAt:              completed,
		InputRelationships:       run.InputCount,
		CreatedDreams:            run.CreatedHypotheses,
		RejectedDreams:           run.RejectedHypotheses,
		ScheduledFor:             scheduledFor,
		AttemptCount:             run.AttemptCount,
		ProviderModel:            run.ProviderModel,
		ProviderTurns:            run.ProviderTurns,
		ProviderInputTokens:      run.ProviderInputTokens,
		ProviderOutputTokens:     run.ProviderOutputTokens,
		AttemptedPaths:           run.AttemptedPaths,
		ProviderProposals:        run.ProviderProposals,
		OutcomeSummary:           copyDreamOutcomeSummary(run.OutcomeSummary),
		Status:                   run.Status,
		Error:                    run.Error,
		Lane:                     run.Lane,
		EvidenceTargets:          run.EvidenceTargets,
		EvaluatedEvidenceTargets: run.EvaluatedEvidenceTargets,
	}
}

func copyDreamOutcomeSummary(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func dreamSourceRefs(values []map[string]any) []domain.DreamSourceRef {
	out := make([]domain.DreamSourceRef, 0, len(values))
	for _, value := range values {
		ref := domain.DreamSourceRef{
			Type: strings.TrimSpace(anyString(value["type"])),
			ID:   strings.TrimSpace(anyString(value["id"])),
		}
		if ref.Type != "" && ref.ID != "" {
			out = append(out, ref)
		}
	}
	return out
}

func hypothesisContentHash(input dreamcontract.UpsertHypothesisInput) string {
	sources := make([]string, 0, len(input.SourceVersions))
	for id, version := range input.SourceVersions {
		sources = append(sources, fmt.Sprintf("%s:%d", id, version))
	}
	sort.Strings(sources)
	raw := strings.Join([]string{
		input.Statement,
		input.SubjectEntityID,
		input.PredicateKey,
		fmt.Sprintf("%d", input.PredicateVersion),
		input.ObjectEntityID,
		input.ObjectValueID,
		strings.Join(sources, ","),
	}, "\x00")
	sum := sha256.Sum256([]byte(raw))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func optionalProbability(value float64) *float64 {
	if value <= 0 {
		return nil
	}
	if value > 1 {
		value = 1
	}
	return &value
}

func floatPtrValue(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func dreamDisplay(name string, kind string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	kind = strings.TrimSpace(kind)
	if kind == "" {
		return "unnamed node"
	}
	return "unnamed " + kind
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func anyString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return ""
	}
}
