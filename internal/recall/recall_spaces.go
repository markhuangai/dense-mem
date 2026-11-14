package recall

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
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

func (s *recallService) recallAcrossSpaces(ctx context.Context, req recallExecutionRequest, actor requestctx.Actor) (fused *RecallResult, err error) {
	started := time.Now()
	defer func() {
		outcome := "ok"
		resultCount := 0
		if err != nil {
			outcome = "error"
		}
		if fused != nil {
			resultCount = len(fused.Results)
		}
		observability.RecordRecall(ctx, s.metrics, float64(time.Since(started).Microseconds())/1000, resultCount, outcome)
	}()
	req.RecallRequest = normalizeRecallRequest(req.RecallRequest)
	requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "recall_contract"})
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
		if req.TopicIDs != nil && branch.Kind != domain.MemorySpaceTeamShared {
			continue
		}
		branchResult, err := s.recallWithExecution(withRecallBranch(ctx, branch), req)
		if err != nil {
			if branch.Kind == domain.MemorySpaceTeamShared || branch.Kind == "" {
				teamErr = err
				break
			}
			results = append(results, &RecallResult{
				Results:      []RecallResultItem{},
				Degradations: []RecallDegradationResult{{Frontier: "evidence", Optional: true, Code: "space_branch_unavailable", Message: "authorized memory-space branch was unavailable"}},
			})
			continue
		}
		results = append(results, branchResult)
	}
	if req.TopicIDs != nil && len(results) == 0 && teamErr == nil {
		return nil, ErrTopicUnavailable
	}
	if teamErr != nil {
		return nil, teamErr
	}
	fused = fuseRecallResults(results, req.Limit)
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

}

func fuseRecallResults(branches []*RecallResult, resultLimit int) *RecallResult {
	fused := &RecallResult{
		RecallID:     "rec_" + uuid.NewString(),
		Results:      []RecallResultItem{},
		Conflicts:    []RecallConflictSummary{},
		SearchStates: RecallSearchStates{},
		Degradations: []RecallDegradationResult{},
	}
	type evidenceScore struct {
		item  RecallResultItem
		score float64
	}
	evidence := map[string]evidenceScore{}
	degradationSeen := map[RecallDegradationResult]struct{}{}
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
			fused.SearchStates.Evidence = domain.CombineSearchProjectionStates(fused.SearchStates.Evidence, branch.SearchStates.Evidence)
		}
		fused.Conflicts = append(fused.Conflicts, branch.Conflicts...)
		fused.DecisionStages = append(fused.DecisionStages, branch.DecisionStages...)
		for _, degradation := range branch.Degradations {
			if _, seen := degradationSeen[degradation]; seen {
				continue
			}
			degradationSeen[degradation] = struct{}{}
			fused.Degradations = append(fused.Degradations, degradation)
		}
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

	}
	if strings.TrimSpace(fused.SearchStates.Evidence) == "" {
		fused.SearchStates.Evidence = string(domain.SearchProjectionCurrent)
	}
	evidenceItems := make([]evidenceScore, 0, len(evidence))
	for _, item := range evidence {
		evidenceItems = append(evidenceItems, item)
	}
	sort.SliceStable(evidenceItems, func(i, j int) bool {
		if evidenceItems[i].score != evidenceItems[j].score {
			return evidenceItems[i].score > evidenceItems[j].score
		}
		if kindRank := compareRecallSpaceKinds(evidenceItems[i].item.SpaceKind, evidenceItems[j].item.SpaceKind); kindRank != 0 {
			return kindRank < 0
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
	fused.Conflicts = limitRecallConflictSummaries(fused.Conflicts, 20)
	if len(fused.Degradations) > 0 {
		fused.Degradation = &fused.Degradations[0]
	}
	fused.SearchState = fused.SearchStates.Evidence
	return fused
}

func compareRecallSpaceKinds(left, right string) int {
	if left == right {
		return 0
	}
	leftRank := 0
	rightRank := 0
	if left == string(domain.MemorySpaceTeamShared) {
		leftRank = 1
	}
	if right == string(domain.MemorySpaceTeamShared) {
		rightRank = 1
	}
	if leftRank != rightRank {
		if leftRank < rightRank {
			return -1
		}
		return 1
	}
	if left < right {
		return -1
	}
	return 1
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
