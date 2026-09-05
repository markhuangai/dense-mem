package dreamservice

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	evidenceDiscoveryTargetLimit       = 20
	evidenceDiscoveryContextLimit      = 10
	evidenceDiscoveryRelatedLimit      = 5
	evidenceDiscoveryPassLimit         = 2
	evidenceDiscoveryRegenerationLimit = 2
)

func (s *service) runScheduledEvidenceCycle(ctx context.Context, teamID string, windowAt time.Time) (*RunCycleResult, error) {
	teamID, err := normalizeDreamTeamID(teamID)
	if err != nil {
		return nil, err
	}
	active, err := s.evidenceTeamIsActive(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if !active {
		return &RunCycleResult{TeamID: teamID, RunDate: windowAt.UTC().Format("2006-01-02"), ScheduledFor: windowAt.UTC(), Lane: domain.DreamLaneEvidenceDiscovery, Status: "skipped"}, nil
	}
	cfg, err := s.effectiveConfigForTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	windowAt = windowAt.UTC()
	windowAt = windowAt.Truncate(time.Hour)
	result := &RunCycleResult{
		TeamID: teamID, RunDate: windowAt.Format("2006-01-02"),
		ScheduledFor: windowAt, Lane: domain.DreamLaneEvidenceDiscovery,
		Status: "skipped",
	}
	if !cfg.Enabled {
		return result, nil
	}
	started := s.now().UTC()
	claim, err := s.deps.ScheduledStore.ClaimScheduledDreamCycle(ctx, repository.DreamCycleClaimInput{
		TeamID: teamID, RunDate: result.RunDate,
		WindowKey: evidenceDiscoveryWindowKey(windowAt), ScheduledFor: &windowAt,
		LeaseToken: uuid.NewString(), LeaseUntil: started.Add(s.evidenceCycleLease()),
		Lane: domain.DreamLaneEvidenceDiscovery,
	})
	if err != nil {
		return nil, translateDreamRepositoryError(err)
	}
	if claim == nil {
		return nil, errors.New("scheduled evidence dreaming cycle: missing durable claim")
	}
	result.RunID = claim.RunID
	result.AttemptCount = claim.AttemptCount
	result.Status = claim.Status
	if !claim.Claimed {
		result.Status = "skipped"
		return result, nil
	}
	return s.runClaimedEvidenceCycle(ctx, teamID, cfg, result, claim)
}

func evidenceDiscoveryWindowKey(windowAt time.Time) string {
	return "hour:" + windowAt.UTC().Format("2006-01-02T15")
}

func (s *service) RecoverScheduledEvidenceCycle(ctx context.Context, teamID string) (*RunCycleResult, error) {
	teamID, err := normalizeDreamTeamID(teamID)
	if err != nil {
		return nil, err
	}
	if s.deps.ScheduledStore == nil {
		return nil, fmt.Errorf("recover scheduled evidence dreaming cycle: scheduled dream repository is required")
	}
	active, err := s.evidenceTeamIsActive(ctx, teamID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, nil
	}
	cfg, err := s.effectiveConfigForTeam(ctx, teamID)
	if err != nil {
		return nil, err
	}
	started := s.now().UTC()
	claim, err := s.deps.ScheduledStore.ClaimRecoverableScheduledDreamCycle(ctx, repository.DreamCycleRecoveryClaimInput{
		TeamID: teamID, LeaseToken: uuid.NewString(), LeaseUntil: started.Add(s.evidenceCycleLease()),
		MaxAttempts: scheduledRecoveryAttempts, Lane: domain.DreamLaneEvidenceDiscovery,
	})
	if err != nil {
		return nil, translateDreamRepositoryError(err)
	}
	if claim == nil {
		return nil, nil
	}
	result := cycleRunResult(claim)
	if result == nil {
		return nil, errors.New("recover scheduled evidence dreaming cycle: missing claimed run")
	}
	result.StartedAt = started
	result.Status = "running"
	if !cfg.Enabled {
		result.CompletedAt = s.now().UTC()
		result.Status = "cancelled"
		result.OutcomeSummary = map[string]int{"disabled_before_recovery": 1}
		if err := s.completeTeamCycle(ctx, true, repository.DreamCycleCompleteInput{
			TeamID: teamID, RunID: claim.RunID, LeaseToken: claim.LeaseToken,
			Status: "cancelled", OutcomeSummary: result.OutcomeSummary,
			Lane: domain.DreamLaneEvidenceDiscovery,
		}); err != nil {
			return result, err
		}
		return result, nil
	}
	return s.runClaimedEvidenceCycle(ctx, teamID, cfg, result, claim)
}

func (s *service) evidenceTeamIsActive(ctx context.Context, teamID string) (bool, error) {
	if s == nil || s.deps.Teams == nil {
		return true, nil
	}
	id, err := uuid.Parse(teamID)
	if err != nil {
		return false, fmt.Errorf("evidence discovery team: invalid team id: %w", err)
	}
	team, err := s.deps.Teams.GetByID(ctx, id)
	if err != nil {
		return false, err
	}
	if team == nil {
		return false, nil
	}
	status := strings.ToLower(strings.TrimSpace(team.Status))
	return status == "" || status == "active", nil
}

func (s *service) runClaimedEvidenceCycle(
	ctx context.Context,
	teamID string,
	cfg EffectiveConfig,
	result *RunCycleResult,
	claimed *repository.DreamCycleRun,
) (*RunCycleResult, error) {
	if s.deps.Store == nil || s.deps.EvidenceStore == nil || s.deps.EvidenceGenerator == nil {
		return s.finishEvidenceCycle(ctx, teamID, cfg, result, claimed, 0, 0, 0, 0, 0, errors.New("evidence discovery store and provider are required"))
	}
	created, rejected, evaluated, providerProposals := claimed.CreatedHypotheses, claimed.RejectedHypotheses, claimed.EvaluatedEvidenceTargets, claimed.ProviderProposals
	providerTurns, providerInputTokens, providerOutputTokens := claimed.ProviderTurns, claimed.ProviderInputTokens, claimed.ProviderOutputTokens
	totals := repository.EvidenceDiscoveryRunTotals{}
	if claimed.AttemptCount > 1 {
		var totalsErr error
		totals, totalsErr = s.deps.EvidenceStore.LoadEvidenceDiscoveryRunTotals(ctx, teamID, claimed.RunID)
		if totalsErr != nil {
			return s.finishEvidenceCycle(ctx, teamID, cfg, result, claimed, created, rejected, evaluated, providerProposals, claimed.EvidenceTargets, totalsErr)
		}
		created = max(created, totals.Created)
		rejected = max(rejected, totals.Rejected)
		evaluated = max(evaluated, totals.Evaluated)
		providerProposals = max(providerProposals, totals.ProviderProposals)
		providerTurns = max(providerTurns, totals.ProviderTurns)
		providerInputTokens = max(providerInputTokens, totals.ProviderInputTokens)
		providerOutputTokens = max(providerOutputTokens, totals.ProviderOutputTokens)
	}
	targets, err := s.deps.EvidenceStore.ListEvidenceDiscoveryTargets(ctx, teamID, evidenceDiscoveryTargetLimit, evidenceDiscoveryContextLimit)
	if err != nil {
		targetCount := max(claimed.EvidenceTargets, totals.TargetCount)
		return s.finishEvidenceCycle(ctx, teamID, cfg, result, claimed, created, rejected, evaluated, providerProposals, targetCount, err)
	}
	if len(targets) > evidenceDiscoveryTargetLimit {
		targets = targets[:evidenceDiscoveryTargetLimit]
	}
	result.EvidenceTargets = evidenceDiscoveryTargetCount(claimed.EvidenceTargets, totals, targets)
	result.CreatedDreams = created
	result.RejectedDreams = rejected
	result.EvaluatedEvidenceTargets = evaluated
	result.ProviderTurns = providerTurns
	result.ProviderInputTokens = providerInputTokens
	result.ProviderOutputTokens = providerOutputTokens
	result.ProviderProposals = providerProposals
	// Related records are context only. They are read under the same team
	// boundary and never become evidence derivations.
	relationships, relationshipErr := s.deps.Store.ListDreamInputs(ctx, repository.DreamInputListInput{TeamID: teamID, Limit: 500})
	hypotheses, _, hypothesisErr := s.deps.Store.ListHypotheses(ctx, repository.ListHypothesesInput{
		TeamID: teamID, Status: string(domain.DreamStatusProposed), Limit: 100,
	})
	if relationshipErr != nil || hypothesisErr != nil {
		return s.finishEvidenceCycle(ctx, teamID, cfg, result, claimed, created, rejected, evaluated, providerProposals, result.EvidenceTargets, errors.Join(relationshipErr, hypothesisErr))
	}
	// Reinforced hypotheses are also allowed as non-supporting duplicate
	// context. Fetch them separately because the list contract accepts one
	// lifecycle status per call.
	reinforced, _, err := s.deps.Store.ListHypotheses(ctx, repository.ListHypothesesInput{
		TeamID: teamID, Status: string(domain.DreamStatusReinforced), Limit: 100,
	})
	if err != nil {
		return s.finishEvidenceCycle(ctx, teamID, cfg, result, claimed, created, rejected, evaluated, providerProposals, result.EvidenceTargets, err)
	}
	hypotheses = append(hypotheses, reinforced...)

	providerModel := s.deps.EvidenceGenerator.Model()
	if strings.TrimSpace(providerModel) == "" {
		return s.finishEvidenceCycle(ctx, teamID, cfg, result, claimed, created, rejected, evaluated, providerProposals, result.EvidenceTargets, ErrDreamProviderUnavailable)
	}
	result.ProviderModel = providerModel
	for index := range targets {
		target := targets[index]
		target.RelatedRelationships = selectEvidenceRelatedRelationships(target.Target, relationships, evidenceDiscoveryRelatedLimit)
		relatedHypothesisLimit := evidenceDiscoveryRelatedLimit - len(target.RelatedRelationships)
		if relatedHypothesisLimit < 0 {
			relatedHypothesisLimit = 0
		}
		target.RelatedHypotheses = selectEvidenceRelatedHypotheses(target.Target, hypotheses, relatedHypothesisLimit)
		continuePass := true
		for continuePass {
			continuePass = false
			targetErr := s.deps.EvidenceStore.WithEvidenceDiscoveryTargetLock(
				ctx, teamID, target.Target.EvidenceID, target.Target.ContentHash,
				func(attempt repository.EvidenceDiscoveryAttempt) error {
					if attempt.PassNumber < 1 || attempt.PassNumber > evidenceDiscoveryPassLimit {
						return nil
					}
					request := EvidenceGenerationRequest{
						Target: target.Target, Contexts: boundedEvidenceContexts(target.Contexts),
						Nodes: target.Nodes, AllowedPredicates: target.AllowedPredicates,
						RelatedRelationships: target.RelatedRelationships, RelatedHypotheses: target.RelatedHypotheses,
						MaxOutputs: cfg.MaxOutputs,
					}
					if request.MaxOutputs <= 0 || request.MaxOutputs > dreamgenerationMaxEvidenceOutputs {
						request.MaxOutputs = dreamgenerationMaxEvidenceOutputs
					}
					if err := s.deps.EvidenceStore.MarkEvidenceDiscoveryAttemptDispatched(ctx, repository.EvidenceDiscoveryAttemptValidationInput{
						TeamID: teamID, AttemptID: attempt.AttemptID, ReservationToken: attempt.ReservationToken,
					}); err != nil {
						return err
					}
					for regeneration := 0; regeneration < evidenceDiscoveryRegenerationLimit; regeneration++ {
						generation, diagnostics, generateErr := s.deps.EvidenceGenerator.GenerateEvidence(
							observability.WithAIOperation(observability.WithMetricIdentity(ctx, teamID, ""), observability.AIOperationEvidenceDiscovery, 1),
							teamID, request,
						)
						providerTurns += diagnostics.ProviderTurns
						providerInputTokens += diagnostics.ProviderInputTokens
						providerOutputTokens += diagnostics.ProviderOutputTokens
						providerProposals += diagnostics.ProviderProposals
						result.ProviderTurns = providerTurns
						result.ProviderInputTokens = providerInputTokens
						result.ProviderOutputTokens = providerOutputTokens
						result.ProviderProposals = providerProposals
						if generateErr != nil {
							if evidenceProviderFailureCanAbandon(generateErr) {
								abandonErr := s.deps.EvidenceStore.AbandonEvidenceDiscoveryAttempt(ctx, teamID, attempt.AttemptID, attempt.ReservationToken)
								if abandonErr != nil {
									return errors.Join(generateErr, abandonErr)
								}
							}
							return generateErr
						}
						proposals, invalid := evidenceProposalsFromGenerated(generation, target.Target, providerModel, request.MaxOutputs)
						persisted, persistErr := s.deps.EvidenceStore.PersistEvidenceDiscoveryEvaluation(ctx, repository.EvidenceDiscoveryEvaluationInput{
							TeamID: teamID, RunID: claimed.RunID, LeaseToken: claimed.LeaseToken,
							AttemptID: attempt.AttemptID, ReservationToken: attempt.ReservationToken,
							Target: target.Target, PassNumber: attempt.PassNumber, ProviderModel: providerModel,
							ProviderTurns: diagnostics.ProviderTurns, ProviderInputTokens: diagnostics.ProviderInputTokens,
							ProviderOutputTokens: diagnostics.ProviderOutputTokens, ProviderProposals: diagnostics.ProviderProposals,
							AcceptedProposals: len(proposals),
							RejectedProposals: invalid, CreatedHypotheses: len(proposals), Proposals: proposals,
						})
						if persistErr != nil {
							if evidencePersistenceDuplicate(persistErr) && regeneration+1 < evidenceDiscoveryRegenerationLimit {
								continue
							}
							if evidencePersistenceDuplicate(persistErr) {
								duplicateErr := &modelprovider.MalformedResponseError{
									Provider: providerModel, Message: "evidence discovery response duplicated an existing target",
									FailureClass: "duplicate_exhausted", Attempts: regeneration + 1,
								}
								abandonErr := s.deps.EvidenceStore.AbandonEvidenceDiscoveryAttempt(ctx, teamID, attempt.AttemptID, attempt.ReservationToken)
								if abandonErr != nil {
									return errors.Join(duplicateErr, abandonErr)
								}
								return duplicateErr
							}
							return persistErr
						}
						rejected += invalid
						created += persisted.Created
						rejected += persisted.Rejected
						evaluated++
						// A second pass is a bounded reinforcement check only when the first
						// validated pass produced at least one durable hypothesis.
						continuePass = attempt.PassNumber == 1 && persisted.Created > 0
						return nil
					}
					return errors.New("evidence discovery regeneration limit exhausted")
				},
			)
			if targetErr != nil {
				return s.finishEvidenceCycle(ctx, teamID, cfg, result, claimed, created, rejected, evaluated, providerProposals, result.EvidenceTargets, targetErr)
			}
		}
	}
	result.ProviderTurns = providerTurns
	result.ProviderInputTokens = providerInputTokens
	result.ProviderOutputTokens = providerOutputTokens
	result.ProviderProposals = providerProposals
	result.EvaluatedEvidenceTargets = evaluated
	result.OutcomeSummary = map[string]int{
		"evidence_targets": result.EvidenceTargets, "evaluated_evidence_targets": evaluated,
		"provider_proposals": providerProposals, "invalid_provider_proposals": rejected,
		"created_hypotheses": created, "rejected_hypotheses": rejected,
	}
	return s.finishEvidenceCycle(ctx, teamID, cfg, result, claimed, created, rejected, evaluated, providerProposals, result.EvidenceTargets, nil)
}

func evidencePersistenceDuplicate(err error) bool {
	return errors.Is(err, repository.ErrDreamExactRelationshipExists) ||
		errors.Is(err, repository.ErrDreamExactHypothesisExists)
}

const dreamgenerationMaxEvidenceOutputs = 50

func evidenceProviderFailureCanAbandon(err error) bool {
	if errors.Is(err, ErrDreamProviderUnavailable) {
		return true
	}
	var rateLimit *modelprovider.RateLimitError
	if errors.As(err, &rateLimit) {
		return true
	}
	var malformed *modelprovider.MalformedResponseError
	if errors.As(err, &malformed) {
		return true
	}
	var providerErr *modelprovider.ProviderError
	if errors.As(err, &providerErr) {
		if providerErr.FailureClass == modelprovider.ProviderFailureClassRequestInvalid {
			return true
		}
		return providerErr.FailureClass == modelprovider.ProviderFailureClassHTTPClient &&
			providerErr.StatusCode >= 400 && providerErr.StatusCode < 500
	}
	return false
}

func evidenceDiscoveryTargetKey(target repository.EvidenceTarget) string {
	return strings.TrimSpace(target.EvidenceID) + ":" + strings.TrimSpace(target.ContentHash)
}

func evidenceDiscoveryTargetCount(claimedCount int, totals repository.EvidenceDiscoveryRunTotals, targets []repository.EvidenceDiscoveryTargetInput) int {
	keys := make(map[string]struct{}, len(totals.TargetKeys)+len(targets))
	for _, key := range totals.TargetKeys {
		if strings.TrimSpace(key) != "" {
			keys[key] = struct{}{}
		}
	}
	for _, target := range targets {
		key := evidenceDiscoveryTargetKey(target.Target)
		if key != ":" {
			keys[key] = struct{}{}
		}
	}
	count := len(keys)
	count = max(count, totals.TargetCount)
	count = max(count, claimedCount)
	if count == 0 {
		count = len(targets)
	}
	return count
}

func evidenceProviderFailure(err error) bool {
	if errors.Is(err, ErrDreamProviderUnavailable) || errors.Is(err, modelprovider.ErrVerifierProvider) ||
		errors.Is(err, modelprovider.ErrVerifierRateLimit) || errors.Is(err, modelprovider.ErrVerifierMalformedResponse) ||
		errors.Is(err, modelprovider.ErrVerifierTimeout) {
		return true
	}
	var providerErr *modelprovider.ProviderError
	var rateLimit *modelprovider.RateLimitError
	var malformed *modelprovider.MalformedResponseError
	return errors.As(err, &providerErr) || errors.As(err, &rateLimit) || errors.As(err, &malformed)
}

func evidenceCyclePublicError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, ErrDreamProviderUnavailable) {
		return "evidence discovery provider unavailable"
	}
	if errors.Is(err, modelprovider.ErrVerifierRateLimit) {
		return "evidence discovery provider rate limited"
	}
	if errors.Is(err, modelprovider.ErrVerifierMalformedResponse) {
		return "evidence discovery provider response invalid"
	}
	if errors.Is(err, modelprovider.ErrVerifierTimeout) {
		return "evidence discovery provider timed out"
	}
	var providerErr *modelprovider.ProviderError
	if errors.As(err, &providerErr) {
		switch providerErr.FailureClass {
		case modelprovider.ProviderFailureClassRequestInvalid:
			return "evidence discovery provider rejected the request"
		case modelprovider.ProviderFailureClassRateLimited:
			return "evidence discovery provider rate limited"
		case modelprovider.ProviderFailureClassTimeout:
			return "evidence discovery provider timed out"
		case modelprovider.ProviderFailureClassProviderUnavailable, modelprovider.ProviderFailureClassTransport,
			modelprovider.ProviderFailureClassProtocol, modelprovider.ProviderFailureClassHTTPClient,
			modelprovider.ProviderFailureClassHTTPServer, modelprovider.ProviderFailureClassHTTPUnexpected:
			return "evidence discovery provider unavailable"
		}
		return "evidence discovery provider failed"
	}
	return "evidence discovery cycle failed"
}

func (s *service) finishEvidenceCycle(
	ctx context.Context,
	teamID string,
	_ EffectiveConfig,
	result *RunCycleResult,
	claimed *repository.DreamCycleRun,
	created, rejected, evaluated, providerProposals, targetCount int,
	runErr error,
) (*RunCycleResult, error) {
	if result == nil {
		result = &RunCycleResult{TeamID: teamID, Lane: domain.DreamLaneEvidenceDiscovery}
	}
	if claimed == nil {
		return result, errors.New("evidence discovery cycle: missing durable claim")
	}
	result.CreatedDreams = created
	result.RejectedDreams = rejected
	result.EvidenceTargets = targetCount
	result.EvaluatedEvidenceTargets = evaluated
	result.CompletedAt = s.now().UTC()
	result.Status = "completed"
	if runErr != nil {
		result.Status = "error"
		result.Error = evidenceCyclePublicError(runErr)
	}
	if result.OutcomeSummary == nil {
		result.OutcomeSummary = map[string]int{}
	}
	if evidenceProviderFailure(runErr) {
		result.OutcomeSummary["provider_failed"] = 1
	}
	result.OutcomeSummary["evidence_targets"] = targetCount
	result.OutcomeSummary["evaluated_evidence_targets"] = evaluated
	result.OutcomeSummary["created_hypotheses"] = created
	result.OutcomeSummary["rejected_hypotheses"] = rejected
	result.OutcomeSummary["provider_proposals"] = providerProposals
	status := "completed"
	if runErr != nil {
		status = "failed"
	}
	completeErr := s.completeTeamCycle(ctx, true, repository.DreamCycleCompleteInput{
		TeamID: teamID, RunID: claimed.RunID, LeaseToken: claimed.LeaseToken,
		Status: status, CreatedHypotheses: created, RejectedHypotheses: rejected,
		ProviderModel: result.ProviderModel, ProviderTurns: result.ProviderTurns,
		ProviderInputTokens: result.ProviderInputTokens, ProviderOutputTokens: result.ProviderOutputTokens,
		ProviderProposals: providerProposals, OutcomeSummary: result.OutcomeSummary, Error: result.Error,
		Lane: domain.DreamLaneEvidenceDiscovery, EvidenceTargets: targetCount, EvaluatedEvidenceTargets: evaluated,
	})
	if completeErr != nil {
		result.Status = "error"
		if runErr == nil {
			result.Error = evidenceCyclePublicError(completeErr)
		}
		if runErr != nil {
			return result, errors.Join(runErr, completeErr)
		}
		return result, completeErr
	}
	if runErr != nil {
		return result, runErr
	}
	return result, nil
}

func boundedEvidenceContexts(contexts []repository.EvidenceContext) []repository.EvidenceContext {
	if len(contexts) > evidenceDiscoveryContextLimit {
		return append([]repository.EvidenceContext(nil), contexts[:evidenceDiscoveryContextLimit]...)
	}
	return append([]repository.EvidenceContext(nil), contexts...)
}

func selectEvidenceRelatedRelationships(target repository.EvidenceTarget, inputs []repository.DreamInput, limit int) []repository.DreamInput {
	if limit <= 0 {
		return []repository.DreamInput{}
	}
	eligible := make([]repository.DreamInput, 0, len(inputs))
	for _, input := range inputs {
		if input.Status != "active" || input.RelationshipID == "" {
			continue
		}
		eligible = append(eligible, input)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		iSame := dreamInputSharesSource(eligible[i], target.SourceGroupKey)
		jSame := dreamInputSharesSource(eligible[j], target.SourceGroupKey)
		if iSame != jSame {
			return iSame
		}
		return eligible[i].RelationshipID < eligible[j].RelationshipID
	})
	if len(eligible) > limit {
		eligible = eligible[:limit]
	}
	return eligible
}

func selectEvidenceRelatedHypotheses(_ repository.EvidenceTarget, inputs []repository.HypothesisRecord, limit int) []repository.HypothesisRecord {
	if limit <= 0 {
		return []repository.HypothesisRecord{}
	}
	eligible := make([]repository.HypothesisRecord, 0, len(inputs))
	for _, input := range inputs {
		if input.Status != string(domain.DreamStatusProposed) && input.Status != string(domain.DreamStatusReinforced) {
			continue
		}
		eligible = append(eligible, input)
	}
	sort.SliceStable(eligible, func(i, j int) bool {
		return eligible[i].HypothesisID < eligible[j].HypothesisID
	})
	if len(eligible) > limit {
		eligible = eligible[:limit]
	}
	return eligible
}

func dreamInputSharesSource(input repository.DreamInput, sourceGroupKey string) bool {
	for _, evidence := range input.Evidence {
		if strings.TrimSpace(evidence.SourceGroupKey) == strings.TrimSpace(sourceGroupKey) {
			return true
		}
	}
	return false
}

func evidenceProposalsFromGenerated(generated []GeneratedDream, target repository.EvidenceTarget, model string, maxOutputs int) ([]repository.UpsertHypothesisInput, int) {
	if maxOutputs <= 0 {
		maxOutputs = DefaultMaxOutputs
	}
	proposals := make([]repository.UpsertHypothesisInput, 0, min(maxOutputs, len(generated)))
	rejected := 0
	for _, generatedDream := range generated {
		if len(proposals) >= maxOutputs || strings.TrimSpace(generatedDream.Hypothesis) == "" ||
			generatedDream.SubjectEntityID == "" || generatedDream.PredicateKey == "" ||
			(generatedDream.ObjectEntityID == "") == (generatedDream.ObjectValueID == "") {
			rejected++
			continue
		}
		derivations := append([]repository.EvidenceDerivationSource(nil), generatedDream.EvidenceDerivations...)
		targetCited := false
		evidenceIDs := []string{}
		seen := map[string]struct{}{}
		for _, derivation := range derivations {
			if derivation.EvidenceID == target.EvidenceID {
				targetCited = true
			}
			if _, exists := seen[derivation.EvidenceID]; !exists && derivation.EvidenceID != "" {
				evidenceIDs = append(evidenceIDs, derivation.EvidenceID)
				seen[derivation.EvidenceID] = struct{}{}
			}
		}
		if !targetCited || len(derivations) == 0 {
			rejected++
			continue
		}
		proposal := repository.UpsertHypothesisInput{
			Statement: strings.TrimSpace(generatedDream.Hypothesis), Rationale: strings.TrimSpace(generatedDream.Rationale),
			Likelihood: optionalProbability(generatedDream.Likelihood), Confidence: optionalProbability(generatedDream.Confidence),
			SubjectEntityID: generatedDream.SubjectEntityID, PredicateKey: generatedDream.PredicateKey,
			PredicateVersion: generatedDream.PredicateVersion, ObjectEntityID: generatedDream.ObjectEntityID,
			ObjectValueID: generatedDream.ObjectValueID, GeneratorKind: "provider",
			GeneratorVersion: firstNonEmpty(model, "dream-v3.evidence"), Lane: domain.DreamLaneEvidenceDiscovery,
			SourceEvidenceIDs: evidenceIDs, SourceOwnerProfileIDs: []string{target.OwnerProfileID}, EvidenceDerivations: derivations,
			SourceRefs: []map[string]any{{"type": "evidence", "id": target.EvidenceID}},
			Payload:    map[string]any{"what_if": strings.TrimSpace(generatedDream.WhatIf), "possible_outcome": strings.TrimSpace(generatedDream.PossibleOutcome)},
		}
		proposal.ContentHash = hypothesisContentHash(proposal)
		proposals = append(proposals, proposal)
	}
	return proposals, rejected
}
