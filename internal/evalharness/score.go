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
		MissingRequired:    missingRequiredRefs(qrel.RequiredRefs, trace.RankedRefs, mapping, k),
		BadRefsAtK:         badRefsAtK(qrel.BadRefs, trace.RankedRefs, mapping, k),
		UnmappedSourceRefs: append(unmappedRequired, unmappedBad...),
		LatencyMS:          trace.LatencyMS,
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
	}
	denom := float64(len(scores))
	summary.AverageRecallAtK /= denom
	summary.AverageMRR /= denom
	summary.AverageNDCGAtK /= denom
	summary.AverageBadAtK /= denom

	type accum struct {
		count  int
		recall float64
		mrr    float64
		ndcg   float64
		bad    float64
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
		}
	}
	for slice, a := range accums {
		denom := float64(a.count)
		summary.Slices[slice] = SliceAvg{
			CaseCount:        a.count,
			AverageRecallAtK: a.recall / denom,
			AverageMRR:       a.mrr / denom,
			AverageNDCGAtK:   a.ndcg / denom,
			AverageBadAtK:    a.bad / denom,
		}
	}
	return summary
}

func CompareSummaries(baseline, candidate Summary) (Comparison, error) {
	if baseline.SeedHash != candidate.SeedHash {
		return Comparison{}, fmt.Errorf("seed hash mismatch: baseline %s candidate %s", baseline.SeedHash, candidate.SeedHash)
	}
	return Comparison{
		BaselineRunID:  baseline.RunID,
		CandidateRunID: candidate.RunID,
		SeedHash:       baseline.SeedHash,
		RecallDelta:    candidate.AverageRecallAtK - baseline.AverageRecallAtK,
		MRRDelta:       candidate.AverageMRR - baseline.AverageMRR,
		NDCGDelta:      candidate.AverageNDCGAtK - baseline.AverageNDCGAtK,
		BadAtKDelta:    candidate.AverageBadAtK - baseline.AverageBadAtK,
	}, nil
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
	resolved, ok := mapping.BySourceDocID[ref.SourceDocID]
	if !ok || strings.TrimSpace(resolved.ID) == "" {
		return Ref{}, false
	}
	if resolved.Type == "" {
		resolved.Type = ref.Type
	}
	return resolved, true
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
