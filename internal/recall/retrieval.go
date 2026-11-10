package recall

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

const (
	recallOverfetchMultiple = 6
	recallOverfetchFloor    = 60
	recallRRFConstant       = 60
)

type Retrieval struct {
	repository recallcontract.SearchRepository
	decisions  modelprovider.DecisionProvider
	logger     observability.LogProvider
}

func NewRetrieval(repository recallcontract.SearchRepository) *Retrieval {
	return &Retrieval{repository: repository}
}

func (r *Retrieval) RecallEvidence(ctx context.Context, input recallcontract.RecallEvidenceInput) (result *recallcontract.RecallEvidenceResult, err error) {
	ctx, total := observability.StartReadStage(ctx, observability.ReadOperationEvidenceRecall, observability.ReadStageTotal)
	defer func() {
		items := 0
		if result != nil {
			items = len(result.Results)
		}
		total.Finish(err, items)
	}()
	input = normalizeRecallEvidenceInput(input)
	requestedInput := input
	decisionEnabled := false
	if _, enabled := recallDecisions(ctx); enabled && r != nil && r.decisions != nil {
		input.Limit = maxRecallResultLimit
		decisionEnabled = true
	}
	if err := validateRecallEvidenceInput(input); err != nil {
		return nil, err
	}
	if r == nil || r.repository == nil {
		return nil, errors.New("recall: search repository is required")
	}
	contract, err := r.repository.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	if snapshots, ok := r.repository.(recallcontract.EvidenceSnapshotRepository); ok && input.OrganizationEnabled && (input.SpaceKind == "" || input.SpaceKind == string(domain.MemorySpaceTeamShared)) {
		err = snapshots.WithEvidenceSnapshot(ctx, input.TeamID, func(repository recallcontract.SearchRepository) error {
			result, err = NewRetrieval(repository).recallEvidence(ctx, input, contract)
			return err
		})
		if err != nil {
			return nil, err
		}
		if decisionEnabled {
			return r.finishDecisionEvidence(ctx, requestedInput, result)
		}
		return result, nil
	}
	result, err = r.recallEvidence(ctx, input, contract)
	if err == nil && decisionEnabled {
		return r.finishDecisionEvidence(ctx, requestedInput, result)
	}
	return result, err
}

func (r *Retrieval) recallEvidence(ctx context.Context, input recallcontract.RecallEvidenceInput, contract *searchcontract.ActiveSearchContract) (*recallcontract.RecallEvidenceResult, error) {
	batch, err := r.repository.ReadEvidenceCandidates(ctx, input, contract, recallOverfetchLimit(input.Limit))
	if err != nil {
		return nil, fmt.Errorf("recall: search evidence: %w", err)
	}
	_, fusion := observability.StartReadStage(ctx, observability.ReadOperationEvidenceRecall, observability.ReadStageFusion)
	candidates := fuseRecallCandidates(batch, "evidence", input.KnownEvidenceIDs)
	fusion.Finish(nil, len(candidates))
	if len(candidates) == 0 {
		return &recallcontract.RecallEvidenceResult{
			TeamID: input.TeamID, SearchState: batch.SearchState, Degradations: organizationDegradation("evidence", batch.OrganizationDegradation), Results: []recallcontract.RecallEvidenceHit{},
		}, nil
	}
	ids := recallCandidateIDs(candidates)
	hydrationCtx, hydration := observability.StartReadStage(ctx, observability.ReadOperationEvidenceRecall, observability.ReadStageHydration)
	hydrated := &recallcontract.EvidenceHydration{}
	if organized, ok := r.repository.(recallcontract.OrganizedSearchRepository); ok && input.OrganizationEnabled {
		hydrated, err = organized.HydrateOrganizedEvidence(hydrationCtx, input, contract, ids)
	} else {
		hydrated.Hits, err = r.repository.HydrateEvidence(hydrationCtx, input, contract, ids)
	}
	hydratedCount := 0
	if hydrated != nil {
		hydratedCount = len(hydrated.Hits)
	}
	hydration.Finish(err, hydratedCount)
	if err != nil {
		return nil, fmt.Errorf("recall: hydrate evidence: %w", err)
	}
	_, selection := observability.StartReadStage(ctx, observability.ReadOperationEvidenceRecall, observability.ReadStageSelection)
	results, conflictSources, organizationCode := selectRecallEvidence(candidates, hydrated, input)
	searchState := batch.SearchState
	for _, hit := range results {
		searchState = domain.CombineSearchProjectionStates(searchState, hit.SearchState)
	}
	if organizationCode == "" {
		organizationCode = batch.OrganizationDegradation
	}
	selection.Finish(nil, len(results))
	conflictsCtx, conflictsStage := observability.StartReadStage(ctx, observability.ReadOperationEvidenceRecall, observability.ReadStageConflicts)
	conflicts, err := r.repository.LoadRecallConflicts(conflictsCtx, input, conflictSources)
	count := 0
	if conflicts != nil {
		count = len(conflicts.Evidence)
	}
	conflictsStage.Finish(err, count)
	if err != nil {
		return nil, fmt.Errorf("recall: load conflicts: %w", err)
	}
	return &recallcontract.RecallEvidenceResult{
		TeamID: input.TeamID, SearchState: searchState, Results: results, Degradations: organizationDegradation("evidence", organizationCode),
		EvidenceConflicts: conflicts.Evidence,
	}, nil
}

type recallCandidate struct {
	ID             string
	Score          float64
	BestBranchRank int
	SearchState    string
}

func fuseRecallCandidates(batch *recallcontract.RecallCandidateBatch, kind string, knownIDs []string) []recallCandidate {
	known := make(map[string]struct{}, len(knownIDs))
	for _, id := range knownIDs {
		known[id] = struct{}{}
	}
	candidates := map[string]*recallCandidate{}
	for _, branch := range []struct {
		hits   []searchcontract.SearchHit
		weight float64
	}{{batch.TextHits, 1}, {batch.VectorHits, 1}, {batch.ExpansionHits, 0.5}} {
		for index, hit := range branch.hits {
			if hit.SourceKind != kind || hit.SourceID == "" {
				continue
			}
			if _, known := known[hit.SourceID]; known {
				continue
			}
			rank := index + 1
			candidate := candidates[hit.SourceID]
			if candidate == nil {
				candidate = &recallCandidate{ID: hit.SourceID, BestBranchRank: rank, SearchState: hit.SearchState}
				candidates[hit.SourceID] = candidate
			}
			candidate.Score += branch.weight / (recallRRFConstant + float64(rank))
			if rank < candidate.BestBranchRank {
				candidate.BestBranchRank = rank
			}
			candidate.SearchState = domain.CombineSearchProjectionStates(candidate.SearchState, hit.SearchState)
		}
	}
	for _, hit := range batch.OntologyHits {
		if hit.SourceKind != kind || hit.SourceID == "" {
			continue
		}
		if _, excluded := known[hit.SourceID]; excluded {
			continue
		}
		if _, scored := candidates[hit.SourceID]; !scored {
			candidates[hit.SourceID] = &recallCandidate{ID: hit.SourceID, SearchState: hit.SearchState}
		}
	}
	result := make([]recallCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, *candidate)
	}
	sortRecallCandidates(result, kind)
	return result
}

func sortRecallCandidates(result []recallCandidate, kind string) {
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Score != result[j].Score {
			return result[i].Score > result[j].Score
		}
		return result[i].ID < result[j].ID
	})
}

func recallCandidateIDs(candidates []recallCandidate) []string {
	ids := make([]string, len(candidates))
	for i, candidate := range candidates {
		ids[i] = candidate.ID
	}
	return ids
}

func recallOverfetchLimit(limit int) int {
	overfetch := limit * recallOverfetchMultiple
	if overfetch < recallOverfetchFloor {
		overfetch = recallOverfetchFloor
	}
	if overfetch > recallcontract.MaxRecallCandidateCount {
		return recallcontract.MaxRecallCandidateCount
	}
	return overfetch
}

func normalizeRecallEvidenceInput(input recallcontract.RecallEvidenceInput) recallcontract.RecallEvidenceInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	input.KnownEvidenceIDs = normalizeRecallRequestIDs(input.KnownEvidenceIDs)
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	input.SpaceKind = strings.TrimSpace(input.SpaceKind)
	if input.SpaceID == "" && input.SpaceKind == "" {
		input.SpaceKind = string(domain.MemorySpaceTeamShared)
	}
	if input.Limit <= 0 {
		input.Limit = defaultRecallResultLimit
	}
	if input.Limit > maxRecallResultLimit {
		input.Limit = maxRecallResultLimit
	}
	return input
}

func normalizeRecallGroupKeys(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validateRecallEvidenceInput(input recallcontract.RecallEvidenceInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.SpaceKind != "" && !domain.MemorySpaceKind(input.SpaceKind).Valid() {
		return errors.New("invalid space_kind")
	}
	if input.SpaceKind != "" && input.SpaceKind != string(domain.MemorySpaceTeamShared) && input.SpaceID == "" {
		return errors.New("private space_id is required")
	}
	if input.SpaceID != "" {
		if _, err := uuid.Parse(input.SpaceID); err != nil {
			return err
		}
	}
	if input.Query == "" && len(input.TopicIDs) == 0 {
		return errors.New("query or topic_ids is required")
	}
	for _, id := range input.KnownEvidenceIDs {
		if _, err := uuid.Parse(id); err != nil {
			return errors.New("known_evidence_ids must contain UUIDs")
		}
	}
	return validateRecallRequest(RecallRequest{Query: input.Query, TopicIDs: input.TopicIDs})
}
