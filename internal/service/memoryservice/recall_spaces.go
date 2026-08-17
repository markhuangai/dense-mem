package memoryservice

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const recallFusionRRFConstant = 60

type recallBranchContextKey struct{}
type recallBranchMetricsSuppressedContextKey struct{}

func withRecallBranch(ctx context.Context, branch domain.MemorySpaceAccess) context.Context {
	ctx = context.WithValue(ctx, recallBranchContextKey{}, branch)
	return context.WithValue(ctx, recallBranchMetricsSuppressedContextKey{}, true)
}

func recallBranchFromContext(ctx context.Context) (domain.MemorySpaceAccess, bool) {
	branch, ok := ctx.Value(recallBranchContextKey{}).(domain.MemorySpaceAccess)
	return branch, ok
}

func recallBranchMetricsSuppressed(ctx context.Context) bool {
	suppressed, _ := ctx.Value(recallBranchMetricsSuppressedContextKey{}).(bool)
	return suppressed
}

func branchID(branch domain.MemorySpaceAccess) string {
	if branch.ID == uuid.Nil {
		return ""
	}
	return branch.ID.String()
}

func branchKind(branch domain.MemorySpaceAccess) string {
	if branch.Kind.Valid() {
		return string(branch.Kind)
	}
	return string(domain.MemorySpaceTeamShared)
}

func (s *recallService) recallAcrossSpaces(ctx context.Context, req RecallRequest, actor requestctx.Actor) (fused *RecallResult, err error) {
	started := time.Now()
	defer func() {
		outcome := "ok"
		resultCount := 0
		if err != nil {
			outcome = "error"
		}
		if fused != nil {
			resultCount = len(fused.Results)
			recordRecallCommunityMetric(ctx, s.metrics, fused)
		}
		observability.RecordRecall(ctx, s.metrics, float64(time.Since(started).Microseconds())/1000, resultCount, outcome)
	}()
	req = normalizeRecallRequest(req)
	contract, err := s.search.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	req.recallContract = contract
	req.recallEmbeddingReady = true
	if req.Query != "" {
		embedCtx := observability.WithAIOperation(ctx, observability.AIOperationRecallEmbedding, 1)
		req.recallEmbedding, req.recallEmbeddingDegradation = s.queryEmbedding(embedCtx, contract, req.Query)
	}
	embeddingDegradation := req.recallEmbeddingDegradation
	req.recallEmbeddingDegradation = nil
	branches := append([]domain.MemorySpaceAccess(nil), actor.AllowedSpaces...)
	if len(branches) == 0 {
		branches = []domain.MemorySpaceAccess{{Kind: domain.MemorySpaceTeamShared}}
	}
	sort.SliceStable(branches, func(i, j int) bool {
		ki, kj := branchKind(branches[i]), branchKind(branches[j])
		if ki != kj {
			return ki < kj
		}
		return branchID(branches[i]) < branchID(branches[j])
	})
	results := make([]*RecallResult, 0, len(branches))
	var teamErr error
	for _, branch := range branches {
		branchResult, err := s.Recall(withRecallBranch(ctx, branch), req)
		if err != nil {
			if branch.Kind == domain.MemorySpaceTeamShared || branch.Kind == "" {
				teamErr = err
				break
			}
			results = append(results, &RecallResult{
				Results: []RecallResultItem{}, RelatedRelationships: []RelatedRelationshipSummary{},
				Degradations: []RecallDegradationResult{{Frontier: "evidence", Optional: true, Code: "space_branch_unavailable", Message: "authorized memory-space branch was unavailable"}},
			})
			continue
		}
		results = append(results, branchResult)
	}
	if teamErr != nil {
		return nil, teamErr
	}
	fused = fuseRecallResults(results, req.Limit, recallOptionalLimitValue(req.RelationshipLimit))
	if embeddingDegradation != nil {
		fused.Degradations = append([]RecallDegradationResult{*embeddingDegradation}, fused.Degradations...)
		fused.Degradation = &fused.Degradations[0]
	}
	return fused, nil
}

func applyRecallSpaceKind(result *RecallResult, kind string) {
	if result == nil {
		return
	}
	if kind == "" {
		kind = string(domain.MemorySpaceTeamShared)
	}
	for i := range result.Results {
		result.Results[i].SpaceKind = kind
	}
	for i := range result.RelatedRelationships {
		result.RelatedRelationships[i].SpaceKind = kind
	}
	for i := range result.RelatedCommunities {
		for j := range result.RelatedCommunities[i].CommunityRelationships {
			result.RelatedCommunities[i].CommunityRelationships[j].SpaceKind = kind
		}
	}
}

func fuseRecallResults(branches []*RecallResult, resultLimit, relationshipLimit int) *RecallResult {
	fused := &RecallResult{
		RecallID:             "rec_" + uuid.NewString(),
		Results:              []RecallResultItem{},
		Conflicts:            []RecallConflictSummary{},
		RelatedRelationships: []RelatedRelationshipSummary{},
		RelatedCommunities:   []RecallDiscoveryPath{},
		RelatedHypotheses:    []RelatedHypothesisSummary{},
		SearchStates:         RecallSearchStates{},
		Degradations:         []RecallDegradationResult{},
		DiscoveryPaths:       []RecallDiscoveryPath{},
		DiscoveryGuidance:    "No additional discovery guidance.",
	}
	type evidenceScore struct {
		item  RecallResultItem
		score float64
	}
	evidence := map[string]evidenceScore{}
	type relationshipScore struct {
		item  RelatedRelationshipSummary
		score float64
	}
	relationships := map[string]relationshipScore{}
	seenBranch := false
	for _, branch := range branches {
		if branch == nil {
			continue
		}
		if !seenBranch {
			fused.SearchStates = branch.SearchStates
			if strings.TrimSpace(fused.SearchStates.Evidence) == "" {
				fused.SearchStates.Evidence = branch.SearchState
			}
			seenBranch = true
		} else {
			fused.SearchStates.Evidence = fuseRecallSearchState(fused.SearchStates.Evidence, branch.SearchStates.Evidence)
			fused.SearchStates.Relationships = fuseRecallSearchState(fused.SearchStates.Relationships, branch.SearchStates.Relationships)
		}
		fused.Conflicts = append(fused.Conflicts, branch.Conflicts...)
		fused.RelatedCommunities = append(fused.RelatedCommunities, branch.RelatedCommunities...)
		fused.DiscoveryPaths = append(fused.DiscoveryPaths, branch.DiscoveryPaths...)
		fused.RelatedHypotheses = append(fused.RelatedHypotheses, branch.RelatedHypotheses...)
		fused.Degradations = append(fused.Degradations, branch.Degradations...)
		for _, item := range branch.Results {
			key := item.SpaceKind + ":" + item.EvidenceID
			score := 1 / (recallFusionRRFConstant + float64(maxInt(item.Rank, 1)))
			if current, ok := evidence[key]; !ok {
				evidence[key] = evidenceScore{item: item, score: score}
			} else {
				current.score += score
				if item.Rank < current.item.Rank {
					current.item = item
				}
				evidence[key] = current
			}
		}
		for rank, item := range branch.RelatedRelationships {
			key := item.SpaceKind + ":" + item.RelationshipID
			score := 1 / (recallFusionRRFConstant + float64(rank+1))
			if current, ok := relationships[key]; !ok {
				relationships[key] = relationshipScore{item: item, score: score}
			} else {
				current.score += score
				relationships[key] = current
			}
		}
	}
	if strings.TrimSpace(fused.SearchStates.Evidence) == "" {
		fused.SearchStates.Evidence = string(domain.SearchProjectionCurrent)
	}
	if strings.TrimSpace(fused.SearchStates.Relationships) == "" {
		fused.SearchStates.Relationships = string(domain.SearchProjectionCurrent)
	}
	evidenceItems := make([]evidenceScore, 0, len(evidence))
	for _, item := range evidence {
		evidenceItems = append(evidenceItems, item)
	}
	sort.SliceStable(evidenceItems, func(i, j int) bool {
		if evidenceItems[i].score != evidenceItems[j].score {
			return evidenceItems[i].score > evidenceItems[j].score
		}
		if evidenceItems[i].item.SpaceKind != evidenceItems[j].item.SpaceKind {
			return evidenceItems[i].item.SpaceKind != string(domain.MemorySpaceTeamShared)
		}
		return evidenceItems[i].item.EvidenceID < evidenceItems[j].item.EvidenceID
	})
	if resultLimit > len(evidenceItems) {
		resultLimit = len(evidenceItems)
	}
	if resultLimit < 0 {
		resultLimit = 0
	}
	for _, item := range evidenceItems[:resultLimit] {
		fused.Results = append(fused.Results, item.item)
	}
	for i := range fused.Results {
		fused.Results[i].Rank = i + 1
	}
	relationshipItems := make([]relationshipScore, 0, len(relationships))
	for _, item := range relationships {
		relationshipItems = append(relationshipItems, item)
	}
	sort.SliceStable(relationshipItems, func(i, j int) bool {
		if relationshipItems[i].score != relationshipItems[j].score {
			return relationshipItems[i].score > relationshipItems[j].score
		}
		if relationshipItems[i].item.SpaceKind != relationshipItems[j].item.SpaceKind {
			return relationshipItems[i].item.SpaceKind != string(domain.MemorySpaceTeamShared)
		}
		return relationshipItems[i].item.RelationshipID < relationshipItems[j].item.RelationshipID
	})
	if relationshipLimit > len(relationshipItems) {
		relationshipLimit = len(relationshipItems)
	}
	if relationshipLimit < 0 {
		relationshipLimit = 0
	}
	for _, item := range relationshipItems[:relationshipLimit] {
		fused.RelatedRelationships = append(fused.RelatedRelationships, item.item)
	}
	if len(fused.Degradations) > 0 {
		fused.Degradation = &fused.Degradations[0]
	}
	fused.SearchState = fused.SearchStates.Evidence
	return fused
}

func fuseRecallSearchState(left, right string) string {
	if left == string(domain.SearchProjectionFailed) || right == string(domain.SearchProjectionFailed) {
		return string(domain.SearchProjectionFailed)
	}
	if left == string(domain.SearchProjectionPending) || right == string(domain.SearchProjectionPending) {
		return string(domain.SearchProjectionPending)
	}
	if left == string(domain.SearchProjectionCurrent) || right == string(domain.SearchProjectionCurrent) {
		return string(domain.SearchProjectionCurrent)
	}
	if left == string(domain.SearchProjectionNotRequired) || right == string(domain.SearchProjectionNotRequired) {
		return string(domain.SearchProjectionNotRequired)
	}
	if left == "" {
		return right
	}
	return left
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (s *recallService) recallRelatedRelationships(
	ctx context.Context,
	teamID string,
	req RecallRequest,
	queryEmbedding []float32,
	excludedGroups map[string]struct{},
) ([]RelatedRelationshipSummary, string, *RecallDegradationResult, map[string]struct{}) {
	relationshipLimit := recallOptionalLimitValue(req.RelationshipLimit)
	if relationshipLimit <= 0 {
		return []RelatedRelationshipSummary{}, string(domain.SearchProjectionNotRequired), nil, map[string]struct{}{}
	}
	branch, _ := recallBranchFromContext(ctx)
	recalled, err := s.search.RecallRelationships(ctx, repository.RecallRelationshipsInput{
		TeamID:               teamID,
		Query:                req.Query,
		QueryEmbedding:       queryEmbedding,
		Limit:                relationshipLimit,
		ValidAt:              req.ValidAt,
		KnownAt:              req.KnownAt,
		KnownEvidenceIDs:     req.KnownEvidenceIDs,
		KnownRelationshipIDs: req.KnownRelationshipIDs,
		ExpandFromEntityIDs:  req.ExpandFromEntityIDs,
		ExcludedGroupKeys:    sortedGroupKeys(excludedGroups),
		SpaceID:              branchID(branch),
		SpaceKind:            branchKind(branch),
	})
	if err != nil {
		return []RelatedRelationshipSummary{}, string(domain.SearchProjectionFailed), &RecallDegradationResult{
			Frontier: "relationships",
			Optional: true,
			Code:     "relationship_discovery_unavailable",
			Message:  "relationship discovery was unavailable; primary evidence recall was used",
		}, map[string]struct{}{}
	}
	state := string(domain.SearchProjectionCurrent)
	if recalled != nil && recalled.SearchState != "" {
		state = recalled.SearchState
	}
	var degradation *RecallDegradationResult
	if strings.TrimSpace(req.Query) != "" && len(queryEmbedding) == 0 {
		degradation = relationshipVectorDegradation(state)
	} else if recalled != nil && recalled.VectorOmitted {
		degradation = relationshipVectorDegradation(state)
	}
	groups := map[string]struct{}{}
	if recalled != nil {
		for _, hit := range recalled.Results {
			if hit.SemanticGroupKey != "" {
				groups[hit.SemanticGroupKey] = struct{}{}
			}
		}
	}
	return relatedRelationshipSummaries(recalled), state, degradation, groups
}
