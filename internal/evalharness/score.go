package evalharness

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/service/recallquality"
)

func ScoreTraces(runID, mode, seedID, seedHash, suitePath string, suite []SuiteCase, cases map[string]Case, qrels map[string]QRel, traces []RecallTrace, mapping KnowledgeMapping) ([]RetrievalScore, Summary, error) {
	traceByCase := make(map[string]RecallTrace, len(traces))
	for _, trace := range traces {
		traceByCase[trace.CaseID] = trace
	}
	scores := make([]RetrievalScore, 0, len(suite))
	for _, suiteCase := range suite {
		qrel, ok := qrels[suiteCase.CaseID]
		if !ok {
			return nil, Summary{}, fmt.Errorf("suite case %q missing qrels", suiteCase.CaseID)
		}
		trace, ok := traceByCase[suiteCase.CaseID]
		if !ok {
			return nil, Summary{}, fmt.Errorf("suite case %q missing recall trace", suiteCase.CaseID)
		}
		k := cases[suiteCase.CaseID].Limit
		if k <= 0 {
			k = 10
		}
		scores = append(scores, ScoreTrace(suiteCase.CaseID, k, qrel, trace, mapping))
	}
	summary := SummarizeScores(runID, mode, seedID, seedHash, suitePath, suite, cases, scores)
	return scores, summary, nil
}

func ScoreTrace(caseID string, k int, qrel QRel, trace RecallTrace, mapping KnowledgeMapping) RetrievalScore {
	required, unmappedRequired := remapJudgments(qrel.RequiredRefs, mapping)
	bad, unmappedBad := remapResultRefs(qrel.BadRefs, mapping)
	ranked := resultRefs(trace.RankedRefs)
	metrics := recallquality.ScoreAtK(ranked, required, bad, k)
	score := RetrievalScore{
		CaseID:             caseID,
		K:                  metrics.K,
		RelevantAtK:        metrics.RelevantAtK,
		RelevantTotal:      metrics.RelevantTotal,
		BadAtK:             metrics.BadAtK,
		RecallAtK:          metrics.RecallAtK,
		MRR:                metrics.MRR,
		NDCGAtK:            metrics.NDCGAtK,
		FirstRequiredRank:  firstRank(trace.RankedRefs, qrel.RequiredRefs, mapping),
		FirstBadRank:       firstRank(trace.RankedRefs, qrel.BadRefs, mapping),
		MissingRequired:    missingRequiredRefs(qrel.RequiredRefs, trace.RankedRefs, mapping, k),
		BadRefsAtK:         badRefsAtK(qrel.BadRefs, trace.RankedRefs, mapping, k),
		UnmappedSourceRefs: append(unmappedRequired, unmappedBad...),
		LatencyMS:          trace.LatencyMS,
	}
	if trace.ContextRefs != nil {
		contextMetrics := recallquality.ScoreAtK(resultRefs(trace.ContextRefs), required, bad, k)
		score.ContextScored = true
		score.ContextRelevantAtK = contextMetrics.RelevantAtK
		score.ContextRelevantTotal = contextMetrics.RelevantTotal
		score.ContextBadAtK = contextMetrics.BadAtK
		score.ContextRecallAtK = contextMetrics.RecallAtK
		score.ContextMRR = contextMetrics.MRR
		score.ContextNDCGAtK = contextMetrics.NDCGAtK
		score.ContextFirstRequiredRank = firstRank(trace.ContextRefs, qrel.RequiredRefs, mapping)
		score.ContextFirstBadRank = firstRank(trace.ContextRefs, qrel.BadRefs, mapping)
		score.ContextMissingRequired = missingRequiredRefs(qrel.RequiredRefs, trace.ContextRefs, mapping, k)
		score.ContextBadRefsAtK = badRefsAtK(qrel.BadRefs, trace.ContextRefs, mapping, k)
	}
	return score
}

func SummarizeScores(runID, mode, seedID, seedHash, suitePath string, suite []SuiteCase, cases map[string]Case, scores []RetrievalScore) Summary {
	summary := Summary{
		RunID:           runID,
		Mode:            mode,
		SeedID:          seedID,
		SeedHash:        seedHash,
		SuitePath:       suitePath,
		CaseCount:       len(suite),
		ScoredCaseCount: len(scores),
		Slices:          map[string]SliceAvg{},
		CreatedAt:       time.Now().UTC(),
	}
	if len(scores) == 0 {
		return summary
	}
	scoreByCase := make(map[string]RetrievalScore, len(scores))
	for _, score := range scores {
		scoreByCase[score.CaseID] = score
		summary.AverageRecallAtK += score.RecallAtK
		summary.AverageMRR += score.MRR
		summary.AverageNDCGAtK += score.NDCGAtK
		summary.AverageBadAtK += float64(score.BadAtK)
		summary.UnmappedSourceRefs += len(score.UnmappedSourceRefs)
		if score.ContextScored {
			summary.ContextScoredCaseCount++
			summary.AverageContextRecallAtK += score.ContextRecallAtK
			summary.AverageContextMRR += score.ContextMRR
			summary.AverageContextNDCGAtK += score.ContextNDCGAtK
			summary.AverageContextBadAtK += float64(score.ContextBadAtK)
			if score.ContextFirstRequiredRank == 1 {
				summary.ContextRequiredRank1Rate++
			}
			if score.ContextFirstBadRank == 1 {
				summary.ContextBadRank1Rate++
			}
		}
		if score.FirstRequiredRank == 1 {
			summary.RequiredRank1Rate++
		}
		if score.FirstBadRank == 1 {
			summary.BadRank1Rate++
		}
	}
	denom := float64(len(scores))
	summary.AverageRecallAtK /= denom
	summary.AverageMRR /= denom
	summary.AverageNDCGAtK /= denom
	summary.AverageBadAtK /= denom
	summary.RequiredRank1Rate /= denom
	summary.BadRank1Rate /= denom
	if summary.ContextScoredCaseCount > 0 {
		contextDenom := float64(summary.ContextScoredCaseCount)
		summary.AverageContextRecallAtK /= contextDenom
		summary.AverageContextMRR /= contextDenom
		summary.AverageContextNDCGAtK /= contextDenom
		summary.AverageContextBadAtK /= contextDenom
		summary.ContextRequiredRank1Rate /= contextDenom
		summary.ContextBadRank1Rate /= contextDenom
	}

	type accum struct {
		count         int
		recall        float64
		mrr           float64
		ndcg          float64
		bad           float64
		contextCount  int
		contextRecall float64
		contextMRR    float64
		contextNDCG   float64
		contextBad    float64
	}
	accums := map[string]*accum{}
	for _, suiteCase := range suite {
		score, ok := scoreByCase[suiteCase.CaseID]
		if !ok {
			continue
		}
		slices := append([]string{}, suiteCase.Slices...)
		slices = append(slices, cases[suiteCase.CaseID].Slices...)
		for _, slice := range uniqueNonEmpty(slices) {
			a := accums[slice]
			if a == nil {
				a = &accum{}
				accums[slice] = a
			}
			a.count++
			a.recall += score.RecallAtK
			a.mrr += score.MRR
			a.ndcg += score.NDCGAtK
			a.bad += float64(score.BadAtK)
			if score.ContextScored {
				a.contextCount++
				a.contextRecall += score.ContextRecallAtK
				a.contextMRR += score.ContextMRR
				a.contextNDCG += score.ContextNDCGAtK
				a.contextBad += float64(score.ContextBadAtK)
			}
		}
	}
	for slice, a := range accums {
		denom := float64(a.count)
		avg := SliceAvg{
			CaseCount:        a.count,
			AverageRecallAtK: a.recall / denom,
			AverageMRR:       a.mrr / denom,
			AverageNDCGAtK:   a.ndcg / denom,
			AverageBadAtK:    a.bad / denom,
		}
		if a.contextCount > 0 {
			contextDenom := float64(a.contextCount)
			avg.ContextScoredCaseCount = a.contextCount
			avg.AverageContextRecallAtK = a.contextRecall / contextDenom
			avg.AverageContextMRR = a.contextMRR / contextDenom
			avg.AverageContextNDCGAtK = a.contextNDCG / contextDenom
			avg.AverageContextBadAtK = a.contextBad / contextDenom
		}
		summary.Slices[slice] = avg
	}
	return summary
}

func CompareSummaries(baseline, candidate Summary) (Comparison, error) {
	if baseline.SeedHash != candidate.SeedHash {
		return Comparison{}, fmt.Errorf("seed hash mismatch: baseline %s candidate %s", baseline.SeedHash, candidate.SeedHash)
	}
	return Comparison{
		BaselineRunID:      baseline.RunID,
		CandidateRunID:     candidate.RunID,
		SeedHash:           baseline.SeedHash,
		RecallDelta:        candidate.AverageRecallAtK - baseline.AverageRecallAtK,
		MRRDelta:           candidate.AverageMRR - baseline.AverageMRR,
		NDCGDelta:          candidate.AverageNDCGAtK - baseline.AverageNDCGAtK,
		BadAtKDelta:        candidate.AverageBadAtK - baseline.AverageBadAtK,
		ContextRecallDelta: candidate.AverageContextRecallAtK - baseline.AverageContextRecallAtK,
		ContextMRRDelta:    candidate.AverageContextMRR - baseline.AverageContextMRR,
		ContextNDCGDelta:   candidate.AverageContextNDCGAtK - baseline.AverageContextNDCGAtK,
		ContextBadAtKDelta: candidate.AverageContextBadAtK - baseline.AverageContextBadAtK,
	}, nil
}

func EvaluateGates(summary Summary, gates GateOptions) GateResult {
	result := GateResult{
		Passed:     true,
		Thresholds: map[string]float64{},
		Metrics: map[string]float64{
			"average_recall_at_k":         summary.AverageRecallAtK,
			"required_rank1_rate":         summary.RequiredRank1Rate,
			"average_bad_at_k":            summary.AverageBadAtK,
			"bad_rank1_rate":              summary.BadRank1Rate,
			"average_context_recall_at_k": summary.AverageContextRecallAtK,
			"context_required_rank1_rate": summary.ContextRequiredRank1Rate,
			"average_context_bad_at_k":    summary.AverageContextBadAtK,
			"context_bad_rank1_rate":      summary.ContextBadRank1Rate,
		},
	}
	checkMin := func(name string, value float64, threshold *float64) {
		if threshold == nil {
			return
		}
		result.Thresholds[name] = *threshold
		if value < *threshold {
			result.Passed = false
			result.Failures = append(result.Failures, fmt.Sprintf("%s %.4f below minimum %.4f", name, value, *threshold))
		}
	}
	checkMax := func(name string, value float64, threshold *float64) {
		if threshold == nil {
			return
		}
		result.Thresholds[name] = *threshold
		if value > *threshold {
			result.Passed = false
			result.Failures = append(result.Failures, fmt.Sprintf("%s %.4f above maximum %.4f", name, value, *threshold))
		}
	}
	checkMin("average_recall_at_k", summary.AverageRecallAtK, gates.MinRecallAtK)
	checkMin("required_rank1_rate", summary.RequiredRank1Rate, gates.MinRequiredRank1Rate)
	checkMax("average_bad_at_k", summary.AverageBadAtK, gates.MaxAverageBadAtK)
	checkMax("bad_rank1_rate", summary.BadRank1Rate, gates.MaxBadRank1Rate)
	if gates.ContextAny() {
		if summary.ContextScoredCaseCount == 0 {
			result.Passed = false
			result.Failures = append(result.Failures, "context metrics unavailable: no traces included context_refs")
		} else {
			checkMin("average_context_recall_at_k", summary.AverageContextRecallAtK, gates.MinContextRecallAtK)
			checkMin("context_required_rank1_rate", summary.ContextRequiredRank1Rate, gates.MinContextRequiredRank1Rate)
			checkMax("average_context_bad_at_k", summary.AverageContextBadAtK, gates.MaxAverageContextBadAtK)
			checkMax("context_bad_rank1_rate", summary.ContextBadRank1Rate, gates.MaxContextBadRank1Rate)
		}
	}
	return result
}

func (g GateOptions) Any() bool {
	return g.MinRecallAtK != nil ||
		g.MinRequiredRank1Rate != nil ||
		g.MaxAverageBadAtK != nil ||
		g.MaxBadRank1Rate != nil ||
		g.ContextAny()
}

func (g GateOptions) ContextAny() bool {
	return g.MinContextRecallAtK != nil ||
		g.MinContextRequiredRank1Rate != nil ||
		g.MaxAverageContextBadAtK != nil ||
		g.MaxContextBadRank1Rate != nil
}

func remapJudgments(refs []Ref, mapping KnowledgeMapping) ([]recallquality.Judgment, []Ref) {
	out := make([]recallquality.Judgment, 0, len(refs))
	unmapped := []Ref{}
	for _, ref := range refs {
		resolved, ok := resolveRef(ref, mapping)
		if !ok {
			unmapped = append(unmapped, ref)
			resolved = Ref{Type: ref.Type, ID: "unmapped:" + ref.SourceDocID}
		}
		grade := ref.Grade
		if grade <= 0 {
			grade = 1
		}
		out = append(out, recallquality.Judgment{Type: resolved.Type, ID: resolved.ID, Grade: grade})
	}
	return out, unmapped
}

func remapResultRefs(refs []Ref, mapping KnowledgeMapping) ([]recallquality.ResultRef, []Ref) {
	out := make([]recallquality.ResultRef, 0, len(refs))
	unmapped := []Ref{}
	for _, ref := range refs {
		resolved, ok := resolveRef(ref, mapping)
		if !ok {
			unmapped = append(unmapped, ref)
			continue
		}
		out = append(out, recallquality.ResultRef{Type: resolved.Type, ID: resolved.ID})
	}
	return out, unmapped
}

func resultRefs(refs []Ref) []recallquality.ResultRef {
	out := make([]recallquality.ResultRef, 0, len(refs))
	for _, ref := range refs {
		out = append(out, recallquality.ResultRef{Type: ref.Type, ID: ref.ID})
	}
	return out
}

func missingRequiredRefs(required []Ref, ranked []Ref, mapping KnowledgeMapping, k int) []Ref {
	seen := topKSet(ranked, k)
	missing := []Ref{}
	for _, ref := range required {
		resolved, ok := resolveRef(ref, mapping)
		if !ok || !seen[refKey(resolved.Type, resolved.ID)] {
			missing = append(missing, ref)
		}
	}
	return missing
}

func badRefsAtK(bad []Ref, ranked []Ref, mapping KnowledgeMapping, k int) []Ref {
	seen := topKSet(ranked, k)
	out := []Ref{}
	for _, ref := range bad {
		resolved, ok := resolveRef(ref, mapping)
		if ok && seen[refKey(resolved.Type, resolved.ID)] {
			out = append(out, ref)
		}
	}
	return out
}

func firstRank(ranked []Ref, refs []Ref, mapping KnowledgeMapping) int {
	keys := map[string]struct{}{}
	for _, ref := range refs {
		resolved, ok := resolveRef(ref, mapping)
		if !ok {
			continue
		}
		keys[refKey(resolved.Type, resolved.ID)] = struct{}{}
	}
	if len(keys) == 0 {
		return 0
	}
	for i, ref := range ranked {
		if _, ok := keys[refKey(ref.Type, ref.ID)]; ok {
			return i + 1
		}
	}
	return 0
}

func topKSet(ranked []Ref, k int) map[string]bool {
	if k > len(ranked) {
		k = len(ranked)
	}
	if k < 0 {
		k = 0
	}
	out := map[string]bool{}
	for i := 0; i < k; i++ {
		out[refKey(ranked[i].Type, ranked[i].ID)] = true
	}
	return out
}

func resolveRef(ref Ref, mapping KnowledgeMapping) (Ref, bool) {
	if strings.TrimSpace(ref.ID) != "" {
		return ref, true
	}
	if strings.TrimSpace(ref.SourceDocID) == "" {
		return Ref{}, false
	}
	return resolveSourceMapping(mapping, ref.SourceDocID, ref.Type)
}

func refKey(t, id string) string {
	return strings.TrimSpace(t) + ":" + strings.TrimSpace(id)
}

func uniqueNonEmpty(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
