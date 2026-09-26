package recall

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
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
	batch, err := r.repository.ReadEvidenceCandidates(ctx, input, contract, recallOverfetchLimit(input.Limit))
	if err != nil {
		return nil, fmt.Errorf("recall: search evidence: %w", err)
	}
	_, fusion := observability.StartReadStage(ctx, observability.ReadOperationEvidenceRecall, observability.ReadStageFusion)
	candidates := fuseRecallCandidates(batch, "evidence", input.KnownEvidenceIDs)
	fusion.Finish(nil, len(candidates))
	if len(candidates) == 0 {
		return &recallcontract.RecallEvidenceResult{
			TeamID: input.TeamID, SearchState: batch.SearchState, Results: []recallcontract.RecallEvidenceHit{},
		}, nil
	}
	ids := recallCandidateIDs(candidates)
	hydrationCtx, hydration := observability.StartReadStage(ctx, observability.ReadOperationEvidenceRecall, observability.ReadStageHydration)
	hydrated, err := r.repository.HydrateEvidence(hydrationCtx, input, contract, ids)
	hydration.Finish(err, len(hydrated))
	if err != nil {
		return nil, fmt.Errorf("recall: hydrate evidence: %w", err)
	}
	_, selection := observability.StartReadStage(ctx, observability.ReadOperationEvidenceRecall, observability.ReadStageSelection)
	results := make([]recallcontract.RecallEvidenceHit, 0)
	searchState := batch.SearchState
	for _, candidate := range candidates {
		hit, ok := hydrated[candidate.ID]
		if !ok {
			continue
		}
		hit.Score = candidate.Score
		hit.SpaceKind = input.SpaceKind
		hit.SearchState = domain.CombineSearchProjectionStates(candidate.SearchState, hit.SearchState)
		if hit.SearchState == string(domain.SearchProjectionPending) || hit.SearchState == string(domain.SearchProjectionFailed) {
			searchState = domain.CombineSearchProjectionStates(searchState, hit.SearchState)
		}
		hit.Rank = len(results) + 1
		results = append(results, hit)
		if len(results) == input.Limit {
			break
		}
	}
	selection.Finish(nil, len(results))
	conflictsCtx, conflictsStage := observability.StartReadStage(ctx, observability.ReadOperationEvidenceRecall, observability.ReadStageConflicts)
	conflicts, err := r.repository.LoadRecallConflicts(conflictsCtx, input, results)
	count := 0
	if conflicts != nil {
		count = len(conflicts.Relationships) + len(conflicts.Evidence)
	}
	conflictsStage.Finish(err, count)
	if err != nil {
		return nil, fmt.Errorf("recall: load conflicts: %w", err)
	}
	return &recallcontract.RecallEvidenceResult{
		TeamID: input.TeamID, SearchState: searchState, Results: results,
		Conflicts: conflicts.Relationships, EvidenceConflicts: conflicts.Evidence,
	}, nil
}

func (r *Retrieval) RecallRelationships(ctx context.Context, input recallcontract.RecallRelationshipsInput) (result *recallcontract.RecallRelationshipsResult, err error) {
	ctx, total := observability.StartReadStage(ctx, observability.ReadOperationRelationshipRecall, observability.ReadStageTotal)
	defer func() {
		items := 0
		if result != nil {
			items = len(result.Results)
		}
		total.Finish(err, items)
	}()
	input = normalizeRecallRelationshipsInput(input)
	if err := validateRecallRelationshipsInput(input); err != nil {
		return nil, err
	}
	if r == nil || r.repository == nil {
		return nil, errors.New("recall: search repository is required")
	}
	contract, err := r.repository.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	batch, err := r.repository.ReadRelationshipCandidates(ctx, input, contract, recallOverfetchLimit(input.Limit))
	if err != nil {
		return nil, fmt.Errorf("recall: search relationships: %w", err)
	}
	_, fusion := observability.StartReadStage(ctx, observability.ReadOperationRelationshipRecall, observability.ReadStageFusion)
	candidates := fuseRecallCandidates(batch, "relationship", input.KnownRelationshipIDs)
	fusion.Finish(nil, len(candidates))
	vectorOmitted := len(input.QueryEmbedding) > 0 && batch.SearchState != string(domain.SearchProjectionCurrent)
	if len(candidates) == 0 {
		return &recallcontract.RecallRelationshipsResult{
			TeamID: input.TeamID, SearchState: batch.SearchState, VectorOmitted: vectorOmitted,
			Results: []recallcontract.RecallRelationshipHit{},
		}, nil
	}
	ids := recallCandidateIDs(candidates)
	hydrationCtx, hydration := observability.StartReadStage(ctx, observability.ReadOperationRelationshipRecall, observability.ReadStageHydration)
	hydrated, err := r.repository.HydrateRelationships(hydrationCtx, input, contract, ids)
	hydration.Finish(err, len(hydrated))
	if err != nil {
		return nil, fmt.Errorf("recall: hydrate relationships: %w", err)
	}
	_, selection := observability.StartReadStage(ctx, observability.ReadOperationRelationshipRecall, observability.ReadStageSelection)
	results := make([]recallcontract.RecallRelationshipHit, 0)
	seenGroups := map[string]struct{}{}
	searchState := batch.SearchState
	for _, candidate := range candidates {
		hit, ok := hydrated[candidate.ID]
		if !ok {
			continue
		}
		if hit.SemanticGroupKey != "" {
			if _, seen := seenGroups[hit.SemanticGroupKey]; seen {
				continue
			}
			seenGroups[hit.SemanticGroupKey] = struct{}{}
		}
		hit.Score = candidate.Score
		hit.SpaceKind = input.SpaceKind
		hit.SearchState = domain.CombineSearchProjectionStates(candidate.SearchState, hit.SearchState)
		if hit.SearchState == string(domain.SearchProjectionPending) || hit.SearchState == string(domain.SearchProjectionFailed) {
			searchState = domain.CombineSearchProjectionStates(searchState, hit.SearchState)
		}
		results = append(results, hit)
		if len(results) == input.Limit {
			break
		}
	}
	sortRecallRelationshipResults(results)
	for i := range results {
		results[i].Rank = i + 1
	}
	selection.Finish(nil, len(results))
	return &recallcontract.RecallRelationshipsResult{
		TeamID: input.TeamID, SearchState: searchState, VectorOmitted: vectorOmitted, Results: results,
	}, nil
}

func sortRecallRelationshipResults(results []recallcontract.RecallRelationshipHit) {
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if !results[i].CreatedAt.Equal(results[j].CreatedAt) {
			return results[i].CreatedAt.Before(results[j].CreatedAt)
		}
		return results[i].RelationshipID < results[j].RelationshipID
	})
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
		if kind == "relationship" && result[i].BestBranchRank != result[j].BestBranchRank {
			return result[i].BestBranchRank < result[j].BestBranchRank
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
	input.KnownEvidenceIDs = domain.NormalizeReadIDList(input.KnownEvidenceIDs)
	input.KnownRelationshipIDs = domain.NormalizeReadIDList(input.KnownRelationshipIDs)
	input.ExpandFromEntityIDs = domain.NormalizeReadIDList(input.ExpandFromEntityIDs)
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

func normalizeRecallRelationshipsInput(input recallcontract.RecallRelationshipsInput) recallcontract.RecallRelationshipsInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Query = strings.TrimSpace(input.Query)
	input.KnownEvidenceIDs = domain.NormalizeReadIDList(input.KnownEvidenceIDs)
	input.KnownRelationshipIDs = domain.NormalizeReadIDList(input.KnownRelationshipIDs)
	input.ExpandFromEntityIDs = domain.NormalizeReadIDList(input.ExpandFromEntityIDs)
	input.ExcludedGroupKeys = normalizeRecallGroupKeys(input.ExcludedGroupKeys)
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	input.SpaceKind = strings.TrimSpace(input.SpaceKind)
	if input.SpaceID == "" && input.SpaceKind == "" {
		input.SpaceKind = string(domain.MemorySpaceTeamShared)
	}
	if input.Limit <= 0 {
		input.Limit = defaultRelatedRelationshipLimit
	}
	if input.Limit > maxRelatedRelationshipLimit {
		input.Limit = maxRelatedRelationshipLimit
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
	return validateRecallRetrievalInput(input.TeamID, input.Query, input.ExpandFromEntityIDs,
		input.KnownEvidenceIDs, input.KnownRelationshipIDs, input.SpaceID, input.SpaceKind)
}

func validateRecallRelationshipsInput(input recallcontract.RecallRelationshipsInput) error {
	return validateRecallRetrievalInput(input.TeamID, input.Query, input.ExpandFromEntityIDs,
		input.KnownEvidenceIDs, input.KnownRelationshipIDs, input.SpaceID, input.SpaceKind)
}

func validateRecallRetrievalInput(teamID, query string, expandIDs, knownEvidenceIDs, knownRelationshipIDs []string, spaceID, spaceKind string) error {
	if _, err := uuid.Parse(teamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if spaceKind != "" && !domain.MemorySpaceKind(spaceKind).Valid() {
		return fmt.Errorf("space_kind is invalid: %s", spaceKind)
	}
	if spaceKind != "" && spaceKind != string(domain.MemorySpaceTeamShared) && spaceID == "" {
		return fmt.Errorf("space_id is required for private space kind %s", spaceKind)
	}
	if spaceID != "" {
		if _, err := uuid.Parse(spaceID); err != nil {
			return fmt.Errorf("space_id is invalid: %w", err)
		}
	}
	if query == "" && len(expandIDs) == 0 {
		return errors.New("query or expand_from_entity_ids is required")
	}
	for _, field := range []struct {
		label  string
		values []string
	}{{"known_evidence_ids", knownEvidenceIDs}, {"known_relationship_ids", knownRelationshipIDs}, {"expand_from_entity_ids", expandIDs}} {
		for _, value := range field.values {
			if _, err := uuid.Parse(value); err != nil {
				return fmt.Errorf("%s contains invalid UUID %q: %w", field.label, value, err)
			}
		}
	}
	return nil
}
