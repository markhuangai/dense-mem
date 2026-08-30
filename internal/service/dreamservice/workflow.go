package dreamservice

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
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

var ErrDreamAuthContext = errors.New("dream: authenticated actor context is required")

type dreamGenerationResult struct {
	proposals                  []repository.UpsertHypothesisInput
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
	started := s.now().UTC()
	runDate := localRunDate(started, cfg)
	if scheduled {
		runDate = localRunDate(scheduledWindowAt, cfg)
	}
	if !cfg.Enabled && !req.Manual {
		return &RunCycleResult{TeamID: teamID, RunDate: runDate, Status: "skipped"}, nil
	}
	if req.MaxOutputs > 0 {
		cfg.MaxOutputs = req.MaxOutputs
	}
	result := &RunCycleResult{
		TeamID:    teamID,
		RunDate:   runDate,
		StartedAt: started,
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
	claimInput := repository.DreamCycleClaimInput{
		TeamID:               teamID,
		InitiatedByProfileID: initiatedByProfileID,
		RunDate:              runDate,
		WindowKey:            windowKey,
		ScheduledFor:         scheduledFor,
		LeaseToken:           uuid.NewString(),
		LeaseUntil:           started.Add(leaseDuration),
	}
	var (
		claimed *repository.DreamCycleRun
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
	if !claimed.Claimed && !req.Manual {
		result.CompletedAt = s.now().UTC()
		result.Status = "skipped"
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
	claimed *repository.DreamCycleRun,
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
	inputs, err := s.deps.Store.ListDreamInputs(ctx, repository.DreamInputListInput{
		TeamID: teamID,
		Limit:  cfg.MaxOutputs * 4,
	})
	if err != nil {
		err = translateDreamRepositoryError(err)
		result.CompletedAt = s.now().UTC()
		result.Status = "error"
		result.Error = err.Error()
		result.OutcomeSummary = map[string]int{"input_selection_error": 1}
		completeErr := s.completeTeamCycle(ctx, scheduled, repository.DreamCycleCompleteInput{
			TeamID:               teamID,
			InitiatedByProfileID: initiatedByProfileID,
			RunID:                claimed.RunID,
			LeaseToken:           claimed.LeaseToken,
			Status:               "failed",
			OutcomeSummary:       map[string]int{"input_selection_error": 1},
			Error:                result.Error,
		})
		if completeErr != nil {
			return result, errors.Join(err, completeErr)
		}
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
	if err := s.completeTeamCycle(ctx, scheduled, repository.DreamCycleCompleteInput{
		TeamID:               teamID,
		InitiatedByProfileID: initiatedByProfileID,
		RunID:                claimed.RunID,
		LeaseToken:           claimed.LeaseToken,
		Status:               completeStatus,
		InputCount:           len(inputs),
		CreatedHypotheses:    created,
		RejectedHypotheses:   rejected,
		SourceSnapshot:       dreamInputSnapshot(inputs),
		ProviderModel:        result.ProviderModel,
		ProviderTurns:        result.ProviderTurns,
		ProviderInputTokens:  result.ProviderInputTokens,
		ProviderOutputTokens: result.ProviderOutputTokens,
		AttemptedPaths:       result.AttemptedPaths,
		ProviderProposals:    result.ProviderProposals,
		OutcomeSummary:       result.OutcomeSummary,
		Error:                result.Error,
	}); err != nil && runErr == nil {
		err = translateDreamRepositoryError(err)
		result.Status = "error"
		result.Error = err.Error()
		return result, err
	}
	return result, runErr
}

func (s *service) completeTeamCycle(ctx context.Context, scheduled bool, input repository.DreamCycleCompleteInput) error {
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
	if errors.Is(err, repository.ErrTeamInactive) {
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
	inputs []repository.DreamInput,
	seeds []SeedDream,
	maxOutputs int,
	scheduled bool,
) (int, int, dreamGenerationResult, error) {
	if maxOutputs <= 0 {
		maxOutputs = DefaultMaxOutputs
	}
	byID := make(map[string]repository.DreamInput, len(inputs))
	for _, input := range inputs {
		byID[input.RelationshipID] = input
	}
	var proposals []repository.UpsertHypothesisInput
	generatedRejected := 0
	generatedPaths := []DreamPath{}
	generatorModel := ""
	generation := dreamGenerationResult{}
	if len(seeds) > 0 {
		proposals = dreamProposalsFromSeeds(seeds, byID, maxOutputs)
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
		input := repository.DreamGenerationPersistInput{
			TeamID:             teamID,
			CreatedByProfileID: createdByProfileID,
			RunID:              runID,
			LeaseToken:         leaseToken,
			ProviderModel:      generatorModel,
			Proposals:          proposals,
			EvaluatedPaths:     dreamPathEvaluationInputs(generatedPaths),
		}
		var (
			persisted repository.DreamGenerationPersistResult
			err       error
		)
		if scheduled {
			persisted, err = s.deps.ScheduledStore.PersistScheduledDreamGeneration(ctx, input)
		} else {
			persisted, err = s.deps.Store.PersistDreamGeneration(ctx, input)
		}
		if err != nil {
			return 0, rejected, generation, err
		}
		generation.persistencePolicyRejected = persisted.Rejected
		return persisted.Created, rejected + persisted.Rejected, generation, nil
	}
	for _, proposal := range proposals {
		proposal.TeamID = teamID
		proposal.CreatedByProfileID = createdByProfileID
		proposal.RunID = runID
		var (
			record   *repository.HypothesisRecord
			inserted bool
			err      error
		)
		if scheduled {
			record, inserted, err = s.deps.ScheduledStore.UpsertScheduledHypothesis(ctx, proposal)
		} else {
			record, inserted, err = s.deps.Store.UpsertHypothesis(ctx, proposal)
		}
		if err != nil {
			if errors.Is(err, repository.ErrDreamExactRelationshipExists) ||
				errors.Is(err, repository.ErrDreamExactHypothesisExists) ||
				errors.Is(err, repository.ErrDreamSourceStale) {
				rejected++
				continue
			}
			return created, rejected, generation, err
		}
		if record != nil && inserted {
			created++
		}
	}
	return created, rejected, generation, nil
}

func (s *service) generateDreamProposals(
	ctx context.Context,
	teamID string,
	inputs []repository.DreamInput,
	maxOutputs int,
) (dreamGenerationResult, error) {
	if len(inputs) == 0 || s.deps.Generator == nil {
		return dreamGenerationResult{}, nil
	}
	predicates, err := s.deps.Store.ListDreamTargetPredicates(ctx, teamID)
	if err != nil {
		return dreamGenerationResult{}, err
	}
	paths := buildDreamPaths(inputs, predicates, maxOutputs)
	result := dreamGenerationResult{candidatePaths: len(paths)}
	if len(paths) > 0 {
		targets := dreamTargetCandidates(paths)
		result.candidateTargets = len(targets)
		availableTargets, err := s.deps.Store.ListAvailableDreamTargets(ctx, teamID, targets)
		if err != nil {
			result.targetLookupFailed = true
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
			return result, err
		}
		paths = dreamPathsForEvaluationInputs(paths, unassessed)
		result.previouslyAssessedPaths = beforeAssessment - len(paths)
	}
	if len(paths) == 0 {
		return result, nil
	}
	generator := s.deps.Generator
	model := generator.Model()
	if strings.TrimSpace(model) == "" {
		result.paths = paths
		result.providerFailed = true
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
	var diagnostics GenerationDiagnostics
	var generated []GeneratedDream
	if generatorWithDiagnostics, ok := generator.(DiagnosticsGenerator); ok {
		generated, diagnostics, err = generatorWithDiagnostics.GenerateWithDiagnostics(ctx, teamID, request)
	} else {
		generated, err = generator.Generate(ctx, teamID, request)
		diagnostics.ProviderProposals = len(generated)
	}
	if err != nil {
		result.providerFailed = true
		return result, err
	}
	proposals, rejected := dreamProposalsFromPaths(generated, paths, maxOutputs, model)
	result.proposals = proposals
	result.rejected = rejected
	result.providerTurns = diagnostics.ProviderTurns
	result.providerInputTokens = diagnostics.ProviderInputTokens
	result.providerOutputTokens = diagnostics.ProviderOutputTokens
	result.providerProposals = diagnostics.ProviderProposals
	return result, nil
}

func (s *service) listDreams(ctx context.Context, opts ListOptions) ([]*domain.Dream, string, error) {
	teamID, _, err := dreamActor(ctx)
	if err != nil {
		return nil, "", err
	}
	records, next, err := s.deps.Store.ListHypotheses(ctx, repository.ListHypothesesInput{
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
	record, err := s.deps.Store.GetHypothesis(ctx, repository.GetHypothesisInput{
		TeamID:       teamID,
		HypothesisID: dreamID,
	})
	if err != nil {
		if errors.Is(err, repository.ErrDreamHypothesisNotFound) {
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
	records, err := s.deps.Store.RecallHypotheses(ctx, repository.RecallHypothesesInput{
		TeamID: teamID,
		Query:  query,
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	return dreamRecords(records), nil
}

func (s *service) resolveFeedback(ctx context.Context, req ResolveFeedbackRequest) (*ResolveFeedbackResult, error) {
	teamID, actorProfileID, err := dreamActor(ctx)
	if err != nil {
		return nil, err
	}
	dreamID := strings.TrimSpace(req.DreamID)
	if dreamID == "" {
		return nil, fmt.Errorf("resolve dream feedback: dream_id is required")
	}
	decision := strings.TrimSpace(req.Decision)
	if isDreamConfirmationDecision(decision) {
		return s.resolveConfirmationWithLock(ctx, teamID, actorProfileID, dreamID, decision, req)
	}
	record, err := s.deps.Store.GetHypothesis(ctx, repository.GetHypothesisInput{
		TeamID:       teamID,
		HypothesisID: dreamID,
	})
	if err != nil {
		s.recordDreamFeedback(ctx, decision, nil, "error")
		if errors.Is(err, repository.ErrDreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, err
	}
	dream := dreamRecord(record)
	switch decision {
	case "reject":
		updated, err := s.deps.Store.UpdateHypothesisStatus(ctx, repository.UpdateHypothesisStatusInput{
			TeamID:            teamID,
			ActorProfileID:    actorProfileID,
			HypothesisID:      dreamID,
			Status:            string(domain.DreamStatusRejected),
			Decision:          decision,
			InvalidatedReason: req.Feedback,
		})
		return s.feedbackResult(ctx, decision, dream, updated, nil, err)
	case "stale":
		updated, err := s.deps.Store.UpdateHypothesisStatus(ctx, repository.UpdateHypothesisStatusInput{
			TeamID:            teamID,
			ActorProfileID:    actorProfileID,
			HypothesisID:      dreamID,
			Status:            string(domain.DreamStatusStale),
			Decision:          decision,
			InvalidatedReason: req.Feedback,
		})
		return s.feedbackResult(ctx, decision, dream, updated, nil, err)
	case "reinforce":
		updated, err := s.deps.Store.UpdateHypothesisStatus(ctx, repository.UpdateHypothesisStatusInput{
			TeamID:            teamID,
			ActorProfileID:    actorProfileID,
			HypothesisID:      dreamID,
			Status:            string(domain.DreamStatusReinforced),
			Decision:          decision,
			InvalidatedReason: req.Feedback,
		})
		return s.feedbackResult(ctx, decision, dream, updated, nil, err)
	case "ignore":
		s.recordDreamFeedback(ctx, decision, dream, "ok")
		return &ResolveFeedbackResult{Dream: dream}, nil
	default:
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, fmt.Errorf("%w: %s", ErrInvalidDreamStatus, decision)
	}
}

func (s *service) feedbackResult(
	ctx context.Context,
	decision string,
	original *domain.Dream,
	updated *repository.HypothesisRecord,
	remember *rememberapp.RememberResult,
	err error,
) (*ResolveFeedbackResult, error) {
	if err != nil {
		s.recordDreamFeedback(ctx, decision, original, "error")
		if errors.Is(err, repository.ErrDreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, err
	}
	s.recordDreamFeedback(ctx, decision, original, "ok")
	return &ResolveFeedbackResult{Dream: dreamRecord(updated), Memory: remember}, nil
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

func dreamInputSnapshot(inputs []repository.DreamInput) []map[string]any {
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

func dreamRecords(records []repository.HypothesisRecord) []*domain.Dream {
	out := make([]*domain.Dream, 0, len(records))
	for i := range records {
		out = append(out, dreamRecord(&records[i]))
	}
	return out
}

func dreamRecord(record *repository.HypothesisRecord) *domain.Dream {
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
		CycleRunID:                     record.CycleRunID,
		GeneratorModel:                 firstNonEmpty(record.GeneratorVersion, record.GeneratorKind),
		ContentHash:                    record.ContentHash,
		SourceRefs:                     dreamSourceRefs(record.SourceRefs),
		Derivations:                    dreamDerivations(record.Derivations),
		InvalidatedReason:              record.InvalidatedReason,
		CreatedAt:                      record.CreatedAt,
		UpdatedAt:                      record.UpdatedAt,
	}
}

func dreamDerivations(derivations []repository.DreamDerivationSource) []domain.DreamDerivation {
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

func dreamSourceIDs(refs []map[string]any, candidates bool) []string {
	out := []string{}
	for _, ref := range refs {
		refType := strings.TrimSpace(anyString(ref["type"]))
		isCandidate := refType == "candidate_relationship"
		if isCandidate != candidates {
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

func cycleRunResult(run *repository.DreamCycleRun) *RunCycleResult {
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
		RunID:                run.RunID,
		TeamID:               run.TeamID,
		RunDate:              run.RunDate,
		StartedAt:            run.StartedAt,
		CompletedAt:          completed,
		InputRelationships:   run.InputCount,
		CreatedDreams:        run.CreatedHypotheses,
		RejectedDreams:       run.RejectedHypotheses,
		ScheduledFor:         scheduledFor,
		AttemptCount:         run.AttemptCount,
		ProviderModel:        run.ProviderModel,
		ProviderTurns:        run.ProviderTurns,
		ProviderInputTokens:  run.ProviderInputTokens,
		ProviderOutputTokens: run.ProviderOutputTokens,
		AttemptedPaths:       run.AttemptedPaths,
		ProviderProposals:    run.ProviderProposals,
		OutcomeSummary:       copyDreamOutcomeSummary(run.OutcomeSummary),
		Status:               run.Status,
		Error:                run.Error,
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

func hypothesisContentHash(input repository.UpsertHypothesisInput) string {
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
