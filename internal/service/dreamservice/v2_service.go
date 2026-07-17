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
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

var ErrV2DreamAuthContext = errors.New("v2 dream: authenticated actor context is required")

func (s *service) runV2Cycle(ctx context.Context, req RunCycleRequest) (*RunCycleResult, error) {
	teamID, ownerID, err := v2DreamActor(ctx)
	if err != nil {
		return nil, err
	}
	cfg, err := s.EffectiveConfig(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	started := s.now().UTC()
	runDate := localRunDate(started, cfg)
	if !cfg.Enabled && !req.Manual {
		return &RunCycleResult{ProfileID: ownerID, RunDate: runDate, Status: "skipped"}, nil
	}
	if req.MaxOutputs > 0 {
		cfg.MaxOutputs = req.MaxOutputs
	}
	dreamEnabled := boolValue(req.DreamEnabled, cfg.DreamEnabled)
	result := &RunCycleResult{
		ProfileID: ownerID,
		RunDate:   runDate,
		StartedAt: started,
		Status:    "running",
	}
	if !dreamEnabled {
		result.CompletedAt = s.now().UTC()
		result.Status = "completed"
		return result, nil
	}
	inputs, err := s.deps.V2Dreams.ListV2DreamInputs(ctx, repository.V2DreamInputListInput{
		TeamID: teamID,
		Limit:  cfg.MaxOutputs * 4,
	})
	if err != nil {
		result.CompletedAt = s.now().UTC()
		result.Status = "error"
		result.Error = err.Error()
		return result, err
	}
	windowKey := runDate
	if req.Manual {
		windowKey = "manual:" + uuid.NewString()
	}
	claimed, err := s.deps.V2Dreams.ClaimV2DreamCycle(ctx, repository.V2DreamCycleClaimInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		RunDate:        runDate,
		WindowKey:      windowKey,
		LeaseUntil:     started.Add(lockTimeout),
		SourceSnapshot: v2DreamInputSnapshot(inputs),
	})
	if err != nil {
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
	created, rejected, runErr := s.persistV2Hypotheses(ctx, teamID, ownerID, claimed.RunID, inputs, req.SeedDreams, cfg.MaxOutputs)
	result.CompletedAt = s.now().UTC()
	result.DreamRan = true
	result.CreatedDreams = created
	result.CandidateClaims = v2CandidateInputCount(inputs)
	result.Status = "completed"
	completeStatus := "completed"
	if runErr != nil {
		result.Status = "error"
		result.Error = runErr.Error()
		completeStatus = "failed"
	}
	if err := s.deps.V2Dreams.CompleteV2DreamCycle(ctx, repository.V2DreamCycleCompleteInput{
		TeamID:             teamID,
		OwnerProfileID:     ownerID,
		RunID:              claimed.RunID,
		Status:             completeStatus,
		InputCount:         len(inputs),
		CreatedHypotheses:  created,
		RejectedHypotheses: rejected,
		Error:              result.Error,
	}); err != nil && runErr == nil {
		result.Status = "error"
		result.Error = err.Error()
		return result, err
	}
	return result, runErr
}

func (s *service) persistV2Hypotheses(
	ctx context.Context,
	teamID string,
	ownerID string,
	runID string,
	inputs []repository.V2DreamInput,
	seeds []SeedDream,
	maxOutputs int,
) (int, int, error) {
	if maxOutputs <= 0 {
		maxOutputs = DefaultMaxOutputs
	}
	byID := make(map[string]repository.V2DreamInput, len(inputs))
	for _, input := range inputs {
		byID[input.RelationshipID] = input
	}
	proposals := []repository.V2UpsertHypothesisInput{}
	generatedRejected := 0
	generatedAttempted := false
	if len(seeds) > 0 {
		proposals = v2DreamProposalsFromSeeds(seeds, byID, maxOutputs)
	} else {
		var err error
		proposals, generatedRejected, generatedAttempted, err = s.generateV2DreamProposals(ctx, ownerID, inputs, byID, maxOutputs)
		if err != nil {
			return 0, 0, err
		}
	}
	if len(proposals) == 0 && len(seeds) == 0 && !generatedAttempted {
		proposals = v2DreamProposalsFromCandidates(inputs, maxOutputs)
	}
	created := 0
	rejected := generatedRejected
	for _, proposal := range proposals {
		proposal.TeamID = teamID
		proposal.OwnerProfileID = ownerID
		proposal.RunID = runID
		record, inserted, err := s.deps.V2Dreams.UpsertV2Hypothesis(ctx, proposal)
		if err != nil {
			if errors.Is(err, repository.ErrV2DreamExactRelationshipExists) ||
				errors.Is(err, repository.ErrV2DreamSourceStale) {
				rejected++
				continue
			}
			return created, rejected, err
		}
		if record != nil && inserted {
			created++
		}
	}
	return created, rejected, nil
}

func (s *service) generateV2DreamProposals(
	ctx context.Context,
	ownerID string,
	inputs []repository.V2DreamInput,
	byID map[string]repository.V2DreamInput,
	maxOutputs int,
) ([]repository.V2UpsertHypothesisInput, int, bool, error) {
	if len(inputs) == 0 || s.deps.Generator == nil {
		return nil, 0, false, nil
	}
	generator := s.deps.Generator
	model := generator.Model()
	generated, err := generator.Generate(ctx, ownerID, GenerateRequest{
		MaxOutputs:     maxOutputs,
		Inputs:         v2DreamGeneratorInputs(inputs),
		GeneratorModel: model,
	})
	if err != nil {
		return nil, 0, true, err
	}
	proposals, rejected := v2DreamProposalsFromGenerated(generated, byID, maxOutputs, model)
	return proposals, rejected, len(generated) > 0, nil
}

func v2DreamGeneratorInputs(inputs []repository.V2DreamInput) []DreamInput {
	out := make([]DreamInput, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, DreamInput{
			Type:      v2DreamSourceType(input),
			ID:        input.RelationshipID,
			Subject:   v2DreamDisplay(input.SubjectName, input.SubjectEntityID),
			Predicate: input.PredicateKey,
			Object:    v2DreamDisplay(input.ObjectName, firstNonEmpty(input.ObjectEntityID, input.ObjectValueID)),
			Status:    input.Status,
		})
	}
	return out
}

func v2DreamProposalsFromCandidates(inputs []repository.V2DreamInput, maxOutputs int) []repository.V2UpsertHypothesisInput {
	out := make([]repository.V2UpsertHypothesisInput, 0, maxOutputs)
	for _, input := range inputs {
		if input.Tier != "candidate" || input.Status != "pending_evidence" {
			continue
		}
		proposal := v2DreamProposalFromInput(input, fmt.Sprintf(
			"%s may %s %s.",
			v2DreamDisplay(input.SubjectName, input.SubjectEntityID),
			strings.ReplaceAll(input.PredicateKey, "_", " "),
			v2DreamDisplay(input.ObjectName, firstNonEmpty(input.ObjectEntityID, input.ObjectValueID)),
		))
		out = append(out, proposal)
		if len(out) >= maxOutputs {
			break
		}
	}
	return out
}

func v2DreamProposalsFromGenerated(
	generated []GeneratedDream,
	inputs map[string]repository.V2DreamInput,
	maxOutputs int,
	generatorModel string,
) ([]repository.V2UpsertHypothesisInput, int) {
	out := make([]repository.V2UpsertHypothesisInput, 0, maxOutputs)
	rejected := 0
	allowed := buildV2DreamEndpointAllowlist(inputs)
	for _, item := range generated {
		statement := strings.TrimSpace(item.Hypothesis)
		if statement == "" {
			rejected++
			continue
		}
		sources, ok := v2DreamInputsFromRefs(item.SourceRefs, inputs)
		if !ok {
			rejected++
			continue
		}
		proposal := v2DreamProposalFromSources(sources, statement)
		proposal.Rationale = strings.TrimSpace(item.Rationale)
		if proposal.Rationale == "" {
			proposal.Rationale = "Provider proposed an edge-shaped hypothesis from eligible Relationship inputs."
		}
		proposal.Likelihood = optionalProbability(item.Likelihood)
		proposal.Confidence = optionalProbability(item.Confidence)
		proposal.Payload["what_if"] = strings.TrimSpace(item.WhatIf)
		proposal.Payload["possible_outcome"] = strings.TrimSpace(item.PossibleOutcome)
		proposal.GeneratorKind = "provider"
		proposal.GeneratorVersion = firstNonEmpty(generatorModel, "dream-v2.provider")
		if !applyV2GeneratedTarget(&proposal, item, allowed) {
			rejected++
			continue
		}
		proposal.ContentHash = v2HypothesisContentHash(proposal)
		out = append(out, proposal)
		if len(out) >= maxOutputs {
			break
		}
	}
	return out, rejected
}

func v2DreamProposalsFromSeeds(
	seeds []SeedDream,
	inputs map[string]repository.V2DreamInput,
	maxOutputs int,
) []repository.V2UpsertHypothesisInput {
	out := make([]repository.V2UpsertHypothesisInput, 0, maxOutputs)
	for _, seed := range seeds {
		statement := strings.TrimSpace(seed.Hypothesis)
		if statement == "" {
			continue
		}
		sourceInput, ok := firstV2DreamSeedSource(seed.SourceRefs, inputs)
		if !ok {
			continue
		}
		proposal := v2DreamProposalFromInput(sourceInput, statement)
		proposal.Rationale = strings.TrimSpace(seed.Rationale)
		proposal.Likelihood = optionalProbability(seed.Likelihood)
		proposal.Confidence = optionalProbability(seed.Confidence)
		proposal.Payload["what_if"] = strings.TrimSpace(seed.WhatIf)
		proposal.Payload["possible_outcome"] = strings.TrimSpace(seed.PossibleOutcome)
		out = append(out, proposal)
		if len(out) >= maxOutputs {
			break
		}
	}
	return out
}

func v2DreamProposalFromInput(input repository.V2DreamInput, statement string) repository.V2UpsertHypothesisInput {
	return v2DreamProposalFromSources([]repository.V2DreamInput{input}, statement)
}

func v2DreamProposalFromSources(sources []repository.V2DreamInput, statement string) repository.V2UpsertHypothesisInput {
	input := sources[0]
	sourceRefs := make([]map[string]any, 0, len(sources))
	sourceVersions := make(map[string]int, len(sources))
	sourceOwnerProfileIDs := make([]string, 0, len(sources))
	seenOwners := map[string]struct{}{}
	sourceTiers := make([]string, 0, len(sources))
	sourceStatuses := make([]string, 0, len(sources))
	for _, source := range sources {
		sourceRefs = append(sourceRefs, map[string]any{
			"type": v2DreamSourceType(source),
			"id":   source.RelationshipID,
		})
		sourceVersions[source.RelationshipID] = source.Version
		if source.OwnerProfileID != "" {
			if _, ok := seenOwners[source.OwnerProfileID]; !ok {
				seenOwners[source.OwnerProfileID] = struct{}{}
				sourceOwnerProfileIDs = append(sourceOwnerProfileIDs, source.OwnerProfileID)
			}
		}
		sourceTiers = append(sourceTiers, source.Tier)
		sourceStatuses = append(sourceStatuses, source.Status)
	}
	input = preferredV2DreamTargetSource(sources)
	payload := map[string]any{
		"source_tiers":    sourceTiers,
		"source_statuses": sourceStatuses,
	}
	proposal := repository.V2UpsertHypothesisInput{
		Statement:             strings.TrimSpace(statement),
		Rationale:             "Eligible pending relationship needs independent evidence before semantic commitment.",
		SubjectEntityID:       input.SubjectEntityID,
		PredicateKey:          input.PredicateKey,
		PredicateVersion:      input.PredicateVersion,
		ObjectEntityID:        input.ObjectEntityID,
		ObjectValueID:         input.ObjectValueID,
		SourceRefs:            sourceRefs,
		SourceVersions:        sourceVersions,
		SourceOwnerProfileIDs: sourceOwnerProfileIDs,
		GeneratorKind:         "server",
		GeneratorVersion:      "dream-v2.candidate-safe",
		Payload:               payload,
	}
	proposal.ContentHash = v2HypothesisContentHash(proposal)
	return proposal
}

func v2DreamSourceType(input repository.V2DreamInput) string {
	switch input.Tier {
	case "candidate":
		return "candidate_relationship"
	case "fact":
		return "fact"
	case "validated_claim":
		return "claim"
	default:
		return "relationship"
	}
}

func preferredV2DreamTargetSource(sources []repository.V2DreamInput) repository.V2DreamInput {
	for _, source := range sources {
		if source.Tier == "candidate" && source.Status == "pending_evidence" {
			return source
		}
	}
	return sources[0]
}

func v2DreamInputsFromRefs(
	refs []domain.DreamSourceRef,
	inputs map[string]repository.V2DreamInput,
) ([]repository.V2DreamInput, bool) {
	out := make([]repository.V2DreamInput, 0, len(refs))
	seen := map[string]struct{}{}
	for _, ref := range refs {
		switch ref.Type {
		case "relationship", "candidate_relationship", "fact", "claim":
		default:
			return nil, false
		}
		input, ok := inputs[ref.ID]
		if !ok {
			return nil, false
		}
		if _, ok := seen[input.RelationshipID]; ok {
			continue
		}
		seen[input.RelationshipID] = struct{}{}
		out = append(out, input)
	}
	return out, len(out) > 0
}

type v2DreamEndpointSet struct {
	entities map[string]struct{}
	values   map[string]struct{}
}

func buildV2DreamEndpointAllowlist(inputs map[string]repository.V2DreamInput) v2DreamEndpointSet {
	allowed := v2DreamEndpointSet{
		entities: map[string]struct{}{},
		values:   map[string]struct{}{},
	}
	for _, input := range inputs {
		if input.SubjectEntityID != "" {
			allowed.entities[input.SubjectEntityID] = struct{}{}
		}
		if input.ObjectEntityID != "" {
			allowed.entities[input.ObjectEntityID] = struct{}{}
		}
		if input.ObjectValueID != "" {
			allowed.values[input.ObjectValueID] = struct{}{}
		}
	}
	return allowed
}

func applyV2GeneratedTarget(
	proposal *repository.V2UpsertHypothesisInput,
	item GeneratedDream,
	allowed v2DreamEndpointSet,
) bool {
	if item.SubjectEntityID != "" {
		if _, ok := allowed.entities[item.SubjectEntityID]; !ok {
			return false
		}
		proposal.SubjectEntityID = strings.TrimSpace(item.SubjectEntityID)
	}
	if item.PredicateKey != "" {
		proposal.PredicateKey = strings.TrimSpace(item.PredicateKey)
	}
	if item.PredicateVersion > 0 {
		proposal.PredicateVersion = item.PredicateVersion
	}
	if item.ObjectEntityID != "" || item.ObjectValueID != "" {
		if (item.ObjectEntityID == "") == (item.ObjectValueID == "") {
			return false
		}
		if item.ObjectEntityID != "" {
			if _, ok := allowed.entities[item.ObjectEntityID]; !ok {
				return false
			}
			proposal.ObjectEntityID = strings.TrimSpace(item.ObjectEntityID)
			proposal.ObjectValueID = ""
		}
		if item.ObjectValueID != "" {
			if _, ok := allowed.values[item.ObjectValueID]; !ok {
				return false
			}
			proposal.ObjectEntityID = ""
			proposal.ObjectValueID = strings.TrimSpace(item.ObjectValueID)
		}
	}
	return proposal.SubjectEntityID != "" &&
		proposal.PredicateKey != "" &&
		(proposal.ObjectEntityID != "") != (proposal.ObjectValueID != "")
}

func firstV2DreamSeedSource(refs []domain.DreamSourceRef, inputs map[string]repository.V2DreamInput) (repository.V2DreamInput, bool) {
	for _, ref := range refs {
		switch ref.Type {
		case "relationship", "candidate_relationship", "fact", "claim":
			if input, ok := inputs[ref.ID]; ok {
				return input, true
			}
		}
	}
	return repository.V2DreamInput{}, false
}

func (s *service) listV2Dreams(ctx context.Context, opts ListOptions) ([]*domain.Dream, string, error) {
	teamID, ownerID, err := v2DreamActor(ctx)
	if err != nil {
		return nil, "", err
	}
	if err := s.refreshV2DreamStaleness(ctx, teamID, ownerID); err != nil {
		return nil, "", err
	}
	records, next, err := s.deps.V2Dreams.ListV2Hypotheses(ctx, repository.V2ListHypothesesInput{
		TeamID: teamID,
		Status: opts.Status,
		Limit:  opts.Limit,
		Cursor: opts.Cursor,
	})
	if err != nil {
		return nil, "", err
	}
	return v2DreamRecords(records), next, nil
}

func (s *service) getV2Dream(ctx context.Context, dreamID string) (*domain.Dream, error) {
	teamID, ownerID, err := v2DreamActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.refreshV2DreamStaleness(ctx, teamID, ownerID); err != nil {
		return nil, err
	}
	record, err := s.deps.V2Dreams.GetV2Hypothesis(ctx, repository.V2GetHypothesisInput{
		TeamID:       teamID,
		HypothesisID: dreamID,
	})
	if err != nil {
		if errors.Is(err, repository.ErrV2DreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, err
	}
	return v2DreamRecord(record), nil
}

func (s *service) listV2Runs(ctx context.Context, limit int) ([]*RunCycleResult, error) {
	teamID, ownerID, err := v2DreamActor(ctx)
	if err != nil {
		return nil, err
	}
	latest, err := s.deps.V2Dreams.LatestV2DreamCycle(ctx, teamID, ownerID)
	if err != nil || latest == nil {
		return nil, err
	}
	return []*RunCycleResult{v2CycleRunResult(latest)}, nil
}

func (s *service) recallV2Dreams(ctx context.Context, query string, limit int) ([]*domain.Dream, error) {
	teamID, ownerID, err := v2DreamActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.refreshV2DreamStaleness(ctx, teamID, ownerID); err != nil {
		return nil, err
	}
	records, err := s.deps.V2Dreams.RecallV2Hypotheses(ctx, repository.V2RecallHypothesesInput{
		TeamID: teamID,
		Query:  query,
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	return v2DreamRecords(records), nil
}

func (s *service) resolveV2Feedback(ctx context.Context, req ResolveFeedbackRequest) (*ResolveFeedbackResult, error) {
	teamID, ownerID, err := v2DreamActor(ctx)
	if err != nil {
		return nil, err
	}
	dreamID := strings.TrimSpace(req.DreamID)
	if dreamID == "" {
		return nil, fmt.Errorf("resolve dream feedback: dream_id is required")
	}
	decision := strings.TrimSpace(req.Decision)
	record, err := s.deps.V2Dreams.GetV2Hypothesis(ctx, repository.V2GetHypothesisInput{
		TeamID:       teamID,
		HypothesisID: dreamID,
	})
	if err != nil {
		s.recordDreamFeedback(ctx, decision, nil, "error")
		if errors.Is(err, repository.ErrV2DreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, err
	}
	dream := v2DreamRecord(record)
	switch decision {
	case "reject":
		updated, err := s.deps.V2Dreams.UpdateV2HypothesisStatus(ctx, repository.V2UpdateHypothesisStatusInput{
			TeamID:            teamID,
			OwnerProfileID:    ownerID,
			HypothesisID:      dreamID,
			Status:            string(domain.DreamStatusRejected),
			InvalidatedReason: req.Feedback,
		})
		return s.v2FeedbackResult(ctx, decision, dream, updated, nil, err)
	case "stale":
		updated, err := s.deps.V2Dreams.UpdateV2HypothesisStatus(ctx, repository.V2UpdateHypothesisStatusInput{
			TeamID:            teamID,
			OwnerProfileID:    ownerID,
			HypothesisID:      dreamID,
			Status:            string(domain.DreamStatusStale),
			InvalidatedReason: req.Feedback,
		})
		return s.v2FeedbackResult(ctx, decision, dream, updated, nil, err)
	case "reinforce":
		updated, err := s.deps.V2Dreams.UpdateV2HypothesisStatus(ctx, repository.V2UpdateHypothesisStatusInput{
			TeamID:            teamID,
			OwnerProfileID:    ownerID,
			HypothesisID:      dreamID,
			Status:            string(domain.DreamStatusReinforced),
			InvalidatedReason: req.Feedback,
		})
		return s.v2FeedbackResult(ctx, decision, dream, updated, nil, err)
	case "ignore":
		s.recordDreamFeedback(ctx, decision, dream, "ok")
		return &ResolveFeedbackResult{Dream: dream}, nil
	case "confirm_true", "promote_candidate":
		if s.deps.V2Remember == nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, fmt.Errorf("resolve dream feedback: v2 remember service is required")
		}
		evidence, err := v2DreamSubmissionEvidence(req, record)
		if err != nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
		remember, err := s.deps.V2Remember.RememberV2(ctx, memoryservice.V2RememberRequest{
			ContractVersion:   domain.V2ContractVersion,
			Evidence:          evidence,
			EntityHints:       req.EntityHints,
			RelationshipHints: req.RelationshipHints,
			IdempotencyKey:    v2DreamFeedbackIdempotency(req, dreamID, decision),
		})
		if err != nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
		updated, err := s.deps.V2Dreams.SubmitV2Hypothesis(ctx, repository.V2SubmitHypothesisInput{
			TeamID:            teamID,
			OwnerProfileID:    ownerID,
			HypothesisID:      dreamID,
			SubmittedIngestID: remember.IngestID,
			InvalidatedReason: req.Feedback,
		})
		return s.v2FeedbackResult(ctx, decision, dream, updated, remember, err)
	case "confirm_false":
		if s.deps.V2Remember == nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, fmt.Errorf("resolve dream feedback: v2 remember service is required")
		}
		evidence, err := v2DreamSubmissionEvidence(req, record)
		if err != nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
		remember, err := s.deps.V2Remember.RememberV2(ctx, memoryservice.V2RememberRequest{
			ContractVersion:   domain.V2ContractVersion,
			Evidence:          evidence,
			EntityHints:       req.EntityHints,
			RelationshipHints: req.RelationshipHints,
			IdempotencyKey:    v2DreamFeedbackIdempotency(req, dreamID, decision),
		})
		if err != nil {
			s.recordDreamFeedback(ctx, decision, dream, "error")
			return nil, err
		}
		updated, err := s.deps.V2Dreams.UpdateV2HypothesisStatus(ctx, repository.V2UpdateHypothesisStatusInput{
			TeamID:            teamID,
			OwnerProfileID:    ownerID,
			HypothesisID:      dreamID,
			Status:            string(domain.DreamStatusRejected),
			InvalidatedReason: req.Feedback,
		})
		return s.v2FeedbackResult(ctx, decision, dream, updated, remember, err)
	default:
		s.recordDreamFeedback(ctx, decision, dream, "error")
		return nil, fmt.Errorf("%w: %s", ErrInvalidDreamStatus, decision)
	}
}

func (s *service) v2FeedbackResult(
	ctx context.Context,
	decision string,
	original *domain.Dream,
	updated *repository.V2HypothesisRecord,
	remember *memoryservice.V2RememberResult,
	err error,
) (*ResolveFeedbackResult, error) {
	if err != nil {
		s.recordDreamFeedback(ctx, decision, original, "error")
		if errors.Is(err, repository.ErrV2DreamHypothesisNotFound) {
			return nil, ErrDreamNotFound
		}
		return nil, err
	}
	s.recordDreamFeedback(ctx, decision, original, "ok")
	return &ResolveFeedbackResult{Dream: v2DreamRecord(updated), V2Memory: remember}, nil
}

func (s *service) v2Status(ctx context.Context) (*StatusResult, error) {
	teamID, ownerID, err := v2DreamActor(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.refreshV2DreamStaleness(ctx, teamID, ownerID); err != nil {
		return nil, err
	}
	cfg, err := s.EffectiveConfig(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	pending, _, err := s.deps.V2Dreams.ListV2Hypotheses(ctx, repository.V2ListHypothesesInput{
		TeamID: teamID,
		Status: string(domain.DreamStatusProposed),
		Limit:  100,
	})
	if err != nil {
		return nil, err
	}
	latest, err := s.deps.V2Dreams.LatestV2DreamCycle(ctx, teamID, ownerID)
	if err != nil {
		return nil, err
	}
	var latestResult *RunCycleResult
	if latest != nil {
		latestResult = v2CycleRunResult(latest)
	}
	return &StatusResult{EffectiveConfig: cfg, LatestRun: latestResult, PendingCount: len(pending)}, nil
}

func (s *service) refreshV2DreamStaleness(ctx context.Context, teamID string, ownerID string) error {
	_, err := s.deps.V2Dreams.RefreshV2HypothesisStaleness(ctx, repository.V2RefreshHypothesisStalenessInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Limit:          200,
	})
	return err
}

func v2DreamActor(ctx context.Context) (string, string, error) {
	actor, ok := requestctx.ActorProfileFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.ProfileID == uuid.Nil {
		return "", "", ErrV2DreamAuthContext
	}
	return actor.TeamID.String(), actor.ProfileID.String(), nil
}

func v2DreamInputSnapshot(inputs []repository.V2DreamInput) []map[string]any {
	out := make([]map[string]any, 0, len(inputs))
	for _, input := range inputs {
		out = append(out, map[string]any{
			"relationship_id": input.RelationshipID,
			"version":         input.Version,
			"tier":            input.Tier,
			"status":          input.Status,
		})
	}
	return out
}

func v2CandidateInputCount(inputs []repository.V2DreamInput) int {
	count := 0
	for _, input := range inputs {
		if input.Tier == "candidate" && input.Status == "pending_evidence" {
			count++
		}
	}
	return count
}

func v2DreamRecords(records []repository.V2HypothesisRecord) []*domain.Dream {
	out := make([]*domain.Dream, 0, len(records))
	for i := range records {
		out = append(out, v2DreamRecord(&records[i]))
	}
	return out
}

func v2DreamRecord(record *repository.V2HypothesisRecord) *domain.Dream {
	if record == nil {
		return nil
	}
	return &domain.Dream{
		DreamID:           record.HypothesisID,
		ProfileID:         record.OwnerProfileID,
		Hypothesis:        record.Statement,
		WhatIf:            anyString(record.Payload["what_if"]),
		PossibleOutcome:   anyString(record.Payload["possible_outcome"]),
		Rationale:         record.Rationale,
		Likelihood:        floatPtrValue(record.Likelihood),
		Confidence:        floatPtrValue(record.Confidence),
		Status:            domain.DreamStatus(record.Status),
		Cycle:             CycleDream,
		CycleRunID:        record.CycleRunID,
		GeneratorModel:    firstNonEmpty(record.GeneratorVersion, record.GeneratorKind),
		ContentHash:       record.ContentHash,
		SourceRefs:        v2DreamSourceRefs(record.SourceRefs),
		InvalidatedReason: record.InvalidatedReason,
		CreatedAt:         record.CreatedAt,
		UpdatedAt:         record.UpdatedAt,
	}
}

func v2CycleRunResult(run *repository.V2DreamCycleRun) *RunCycleResult {
	if run == nil {
		return nil
	}
	completed := time.Time{}
	if run.CompletedAt != nil {
		completed = *run.CompletedAt
	}
	return &RunCycleResult{
		RunID:         run.RunID,
		ProfileID:     run.OwnerProfileID,
		RunDate:       run.RunDate,
		StartedAt:     run.StartedAt,
		CompletedAt:   completed,
		DreamRan:      true,
		CreatedDreams: run.CreatedHypotheses,
		Status:        run.Status,
		Error:         run.Error,
	}
}

func v2DreamSourceRefs(values []map[string]any) []domain.DreamSourceRef {
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

func v2HypothesisContentHash(input repository.V2UpsertHypothesisInput) string {
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

func v2DreamSubmissionEvidence(
	req ResolveFeedbackRequest,
	record *repository.V2HypothesisRecord,
) ([]memoryservice.V2RememberEvidenceInput, error) {
	if len(req.Evidence) == 0 {
		return nil, errors.New("resolve dream feedback: independent evidence is required")
	}
	out := make([]memoryservice.V2RememberEvidenceInput, 0, len(req.Evidence))
	for i, item := range req.Evidence {
		item.Content = strings.TrimSpace(item.Content)
		if item.Content == "" {
			return nil, fmt.Errorf("resolve dream feedback: evidence[%d].content is required", i)
		}
		if strings.EqualFold(item.Content, strings.TrimSpace(record.Statement)) {
			return nil, errors.New("resolve dream feedback: hypothesis text cannot be submitted as its own evidence")
		}
		if item.SourceType == "" {
			item.SourceType = "manual"
		}
		if item.Source == "" {
			item.Source = "dream_feedback:" + record.HypothesisID
		}
		if item.IdempotencyKey == "" {
			item.IdempotencyKey = fmt.Sprintf("dream-feedback:%s:%d", record.HypothesisID, i)
		}
		if item.Metadata == nil {
			item.Metadata = map[string]any{}
		}
		item.Metadata["hypothesis_id"] = record.HypothesisID
		item.Metadata["hypothesis_status_before"] = record.Status
		out = append(out, item)
	}
	return out, nil
}

func v2DreamFeedbackIdempotency(req ResolveFeedbackRequest, dreamID string, decision string) string {
	if value := strings.TrimSpace(req.IdempotencyKey); value != "" {
		return value
	}
	return "dream-feedback:" + dreamID + ":" + decision
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

func v2DreamDisplay(name string, fallback string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	return fallback
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
