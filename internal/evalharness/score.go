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
	requiredEvidence, unmappedRequiredEvidence := remapJudgments(qrel.RequiredEvidenceRefs, mapping)
	badEvidence, unmappedBadEvidence := remapResultRefs(qrel.BadEvidenceRefs, mapping)
	requiredDreams, unmappedRequiredDreams := remapJudgments(qrel.RequiredDreamRefs, mapping)
	badDreams, unmappedBadDreams := remapResultRefs(qrel.BadDreamRefs, mapping)
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
		UnmappedSourceRefs: append(append(append(append(append(unmappedRequired, unmappedBad...), unmappedRequiredEvidence...), unmappedBadEvidence...), unmappedRequiredDreams...), unmappedBadDreams...),
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
	if trace.ContextEvidenceRefs != nil && (len(qrel.RequiredEvidenceRefs) > 0 || len(qrel.BadEvidenceRefs) > 0) {
		evidenceMetrics := recallquality.ScoreAtK(resultRefs(trace.ContextEvidenceRefs), requiredEvidence, badEvidence, k)
		score.EvidenceScored = true
		score.EvidenceRelevantAtK = evidenceMetrics.RelevantAtK
		score.EvidenceRelevantTotal = evidenceMetrics.RelevantTotal
		score.EvidenceBadAtK = evidenceMetrics.BadAtK
		score.EvidenceRecallAtK = evidenceMetrics.RecallAtK
		score.EvidenceMRR = evidenceMetrics.MRR
		score.EvidenceNDCGAtK = evidenceMetrics.NDCGAtK
		score.EvidenceFirstRequiredRank = firstRank(trace.ContextEvidenceRefs, qrel.RequiredEvidenceRefs, mapping)
		score.EvidenceFirstBadRank = firstRank(trace.ContextEvidenceRefs, qrel.BadEvidenceRefs, mapping)
		score.EvidenceMissingRequired = missingRequiredRefs(qrel.RequiredEvidenceRefs, trace.ContextEvidenceRefs, mapping, k)
		score.EvidenceBadRefsAtK = badRefsAtK(qrel.BadEvidenceRefs, trace.ContextEvidenceRefs, mapping, k)
	}
	if trace.DreamRefs != nil && (len(qrel.RequiredDreamRefs) > 0 || len(qrel.BadDreamRefs) > 0) {
		dreamMetrics := recallquality.ScoreAtK(resultRefs(trace.DreamRefs), requiredDreams, badDreams, k)
		score.DreamScored = true
		score.DreamRelevantAtK = dreamMetrics.RelevantAtK
		score.DreamRelevantTotal = dreamMetrics.RelevantTotal
		score.DreamBadAtK = dreamMetrics.BadAtK
		score.DreamRecallAtK = dreamMetrics.RecallAtK
		score.DreamMRR = dreamMetrics.MRR
		score.DreamNDCGAtK = dreamMetrics.NDCGAtK
		score.DreamFirstRequiredRank = firstRank(trace.DreamRefs, qrel.RequiredDreamRefs, mapping)
		score.DreamFirstBadRank = firstRank(trace.DreamRefs, qrel.BadDreamRefs, mapping)
		score.DreamMissingRequired = missingRequiredRefs(qrel.RequiredDreamRefs, trace.DreamRefs, mapping, k)
		score.DreamBadRefsAtK = badRefsAtK(qrel.BadDreamRefs, trace.DreamRefs, mapping, k)
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
		if score.EvidenceScored {
			summary.EvidenceScoredCaseCount++
			summary.AverageEvidenceRecallAtK += score.EvidenceRecallAtK
			summary.AverageEvidenceMRR += score.EvidenceMRR
			summary.AverageEvidenceNDCGAtK += score.EvidenceNDCGAtK
			summary.AverageEvidenceBadAtK += float64(score.EvidenceBadAtK)
			if score.EvidenceFirstRequiredRank == 1 {
				summary.EvidenceRequiredRank1Rate++
			}
			if score.EvidenceFirstBadRank == 1 {
				summary.EvidenceBadRank1Rate++
			}
		}
		if score.DreamScored {
			summary.DreamScoredCaseCount++
			summary.AverageDreamRecallAtK += score.DreamRecallAtK
			summary.AverageDreamMRR += score.DreamMRR
			summary.AverageDreamNDCGAtK += score.DreamNDCGAtK
			summary.AverageDreamBadAtK += float64(score.DreamBadAtK)
			if score.DreamFirstRequiredRank == 1 {
				summary.DreamRequiredRank1Rate++
			}
			if score.DreamFirstBadRank == 1 {
				summary.DreamBadRank1Rate++
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
	if summary.EvidenceScoredCaseCount > 0 {
		evidenceDenom := float64(summary.EvidenceScoredCaseCount)
		summary.AverageEvidenceRecallAtK /= evidenceDenom
		summary.AverageEvidenceMRR /= evidenceDenom
		summary.AverageEvidenceNDCGAtK /= evidenceDenom
		summary.AverageEvidenceBadAtK /= evidenceDenom
		summary.EvidenceRequiredRank1Rate /= evidenceDenom
		summary.EvidenceBadRank1Rate /= evidenceDenom
	}
	if summary.DreamScoredCaseCount > 0 {
		dreamDenom := float64(summary.DreamScoredCaseCount)
		summary.AverageDreamRecallAtK /= dreamDenom
		summary.AverageDreamMRR /= dreamDenom
		summary.AverageDreamNDCGAtK /= dreamDenom
		summary.AverageDreamBadAtK /= dreamDenom
		summary.DreamRequiredRank1Rate /= dreamDenom
		summary.DreamBadRank1Rate /= dreamDenom
	}

	type accum struct {
		count          int
		recall         float64
		mrr            float64
		ndcg           float64
		bad            float64
		contextCount   int
		contextRecall  float64
		contextMRR     float64
		contextNDCG    float64
		contextBad     float64
		evidenceCount  int
		evidenceRecall float64
		evidenceMRR    float64
		evidenceNDCG   float64
		evidenceBad    float64
		dreamCount     int
		dreamRecall    float64
		dreamMRR       float64
		dreamNDCG      float64
		dreamBad       float64
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
			if score.EvidenceScored {
				a.evidenceCount++
				a.evidenceRecall += score.EvidenceRecallAtK
				a.evidenceMRR += score.EvidenceMRR
				a.evidenceNDCG += score.EvidenceNDCGAtK
				a.evidenceBad += float64(score.EvidenceBadAtK)
			}
			if score.DreamScored {
				a.dreamCount++
				a.dreamRecall += score.DreamRecallAtK
				a.dreamMRR += score.DreamMRR
				a.dreamNDCG += score.DreamNDCGAtK
				a.dreamBad += float64(score.DreamBadAtK)
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
		if a.evidenceCount > 0 {
			evidenceDenom := float64(a.evidenceCount)
			avg.EvidenceScoredCaseCount = a.evidenceCount
			avg.AverageEvidenceRecallAtK = a.evidenceRecall / evidenceDenom
			avg.AverageEvidenceMRR = a.evidenceMRR / evidenceDenom
			avg.AverageEvidenceNDCGAtK = a.evidenceNDCG / evidenceDenom
			avg.AverageEvidenceBadAtK = a.evidenceBad / evidenceDenom
		}
		if a.dreamCount > 0 {
			dreamDenom := float64(a.dreamCount)
			avg.DreamScoredCaseCount = a.dreamCount
			avg.AverageDreamRecallAtK = a.dreamRecall / dreamDenom
			avg.AverageDreamMRR = a.dreamMRR / dreamDenom
			avg.AverageDreamNDCGAtK = a.dreamNDCG / dreamDenom
			avg.AverageDreamBadAtK = a.dreamBad / dreamDenom
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
		BaselineRunID:       baseline.RunID,
		CandidateRunID:      candidate.RunID,
		SeedHash:            baseline.SeedHash,
		RecallDelta:         candidate.AverageRecallAtK - baseline.AverageRecallAtK,
		MRRDelta:            candidate.AverageMRR - baseline.AverageMRR,
		NDCGDelta:           candidate.AverageNDCGAtK - baseline.AverageNDCGAtK,
		BadAtKDelta:         candidate.AverageBadAtK - baseline.AverageBadAtK,
		ContextRecallDelta:  candidate.AverageContextRecallAtK - baseline.AverageContextRecallAtK,
		ContextMRRDelta:     candidate.AverageContextMRR - baseline.AverageContextMRR,
		ContextNDCGDelta:    candidate.AverageContextNDCGAtK - baseline.AverageContextNDCGAtK,
		ContextBadAtKDelta:  candidate.AverageContextBadAtK - baseline.AverageContextBadAtK,
		EvidenceRecallDelta: candidate.AverageEvidenceRecallAtK - baseline.AverageEvidenceRecallAtK,
		EvidenceMRRDelta:    candidate.AverageEvidenceMRR - baseline.AverageEvidenceMRR,
		EvidenceNDCGDelta:   candidate.AverageEvidenceNDCGAtK - baseline.AverageEvidenceNDCGAtK,
		EvidenceBadAtKDelta: candidate.AverageEvidenceBadAtK - baseline.AverageEvidenceBadAtK,
		DreamRecallDelta:    candidate.AverageDreamRecallAtK - baseline.AverageDreamRecallAtK,
		DreamMRRDelta:       candidate.AverageDreamMRR - baseline.AverageDreamMRR,
		DreamNDCGDelta:      candidate.AverageDreamNDCGAtK - baseline.AverageDreamNDCGAtK,
		DreamBadAtKDelta:    candidate.AverageDreamBadAtK - baseline.AverageDreamBadAtK,
	}, nil
}

func EvaluateGates(summary Summary, gates GateOptions) GateResult {
	result := GateResult{
		Passed:     true,
		Thresholds: map[string]float64{},
		Metrics: map[string]float64{
			"average_recall_at_k":          summary.AverageRecallAtK,
			"required_rank1_rate":          summary.RequiredRank1Rate,
			"average_bad_at_k":             summary.AverageBadAtK,
			"bad_rank1_rate":               summary.BadRank1Rate,
			"average_context_recall_at_k":  summary.AverageContextRecallAtK,
			"context_required_rank1_rate":  summary.ContextRequiredRank1Rate,
			"average_context_bad_at_k":     summary.AverageContextBadAtK,
			"context_bad_rank1_rate":       summary.ContextBadRank1Rate,
			"average_evidence_recall_at_k": summary.AverageEvidenceRecallAtK,
			"evidence_required_rank1_rate": summary.EvidenceRequiredRank1Rate,
			"average_evidence_bad_at_k":    summary.AverageEvidenceBadAtK,
			"evidence_bad_rank1_rate":      summary.EvidenceBadRank1Rate,
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
	if gates.EvidenceAny() {
		if summary.EvidenceScoredCaseCount == 0 {
			result.Passed = false
			result.Failures = append(result.Failures, "evidence metrics unavailable: no scored traces included evidence qrels and context_evidence_refs")
		} else {
			checkMin("average_evidence_recall_at_k", summary.AverageEvidenceRecallAtK, gates.MinEvidenceRecallAtK)
			checkMin("evidence_required_rank1_rate", summary.EvidenceRequiredRank1Rate, gates.MinEvidenceRequiredRank1Rate)
			checkMax("average_evidence_bad_at_k", summary.AverageEvidenceBadAtK, gates.MaxAverageEvidenceBadAtK)
			checkMax("evidence_bad_rank1_rate", summary.EvidenceBadRank1Rate, gates.MaxEvidenceBadRank1Rate)
		}
	}
	return result
}

func (g GateOptions) Any() bool {
	return g.MinRecallAtK != nil ||
		g.MinRequiredRank1Rate != nil ||
		g.MaxAverageBadAtK != nil ||
		g.MaxBadRank1Rate != nil ||
		g.ContextAny() ||
		g.EvidenceAny()
}

func (g GateOptions) ContextAny() bool {
	return g.MinContextRecallAtK != nil ||
		g.MinContextRequiredRank1Rate != nil ||
		g.MaxAverageContextBadAtK != nil ||
		g.MaxContextBadRank1Rate != nil
}

func (g GateOptions) EvidenceAny() bool {
	return g.MinEvidenceRecallAtK != nil ||
		g.MinEvidenceRequiredRank1Rate != nil ||
		g.MaxAverageEvidenceBadAtK != nil ||
		g.MaxEvidenceBadRank1Rate != nil
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
