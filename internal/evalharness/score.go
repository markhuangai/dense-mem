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
	scoring := newScoringContext(mapping)
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
		scores = append(scores, scoreTrace(suiteCase.CaseID, k, qrel, trace, scoring))
	}
	summary := SummarizeScores(runID, mode, seedID, seedHash, suitePath, suite, cases, scores)
	return scores, summary, nil
}

func ScoreTrace(caseID string, k int, qrel QRel, trace RecallTrace, mapping KnowledgeMapping) RetrievalScore {
	return scoreTrace(caseID, k, qrel, trace, newScoringContext(mapping))
}

func scoreTrace(caseID string, k int, qrel QRel, trace RecallTrace, scoring scoringContext) RetrievalScore {
	mapping := scoring.mapping
	required, unmappedRequired := remapJudgments(qrel.RequiredRefs, mapping)
	bad, unmappedBad := remapResultRefs(qrel.BadRefs, mapping)
	requiredEvidence, unmappedRequiredEvidence := remapJudgments(qrel.RequiredEvidenceRefs, mapping)
	badEvidence, unmappedBadEvidence := remapResultRefs(qrel.BadEvidenceRefs, mapping)
	requiredDreams, unmappedRequiredDreams := remapJudgments(qrel.RequiredDreamRefs, mapping)
	badDreams, unmappedBadDreams := remapResultRefs(qrel.BadDreamRefs, mapping)
	rankedRefs := scoring.refsForQRel(trace.RankedRefs, qrel.RequiredRefs, qrel.BadRefs)
	ranked := resultRefs(rankedRefs)
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
		FirstRequiredRank:  firstRank(rankedRefs, qrel.RequiredRefs, mapping),
		FirstBadRank:       firstRank(rankedRefs, qrel.BadRefs, mapping),
		MissingRequired:    missingRequiredRefs(qrel.RequiredRefs, rankedRefs, mapping, k),
		BadRefsAtK:         badRefsAtK(qrel.BadRefs, rankedRefs, mapping, k),
		UnmappedSourceRefs: append(append(append(append(append(unmappedRequired, unmappedBad...), unmappedRequiredEvidence...), unmappedBadEvidence...), unmappedRequiredDreams...), unmappedBadDreams...),
		LatencyMS:          trace.LatencyMS,
	}
	if trace.ContextRefs != nil {
		contextRefs := scoring.refsForQRel(trace.ContextRefs, qrel.RequiredRefs, qrel.BadRefs)
		contextMetrics := recallquality.ScoreAtK(resultRefs(contextRefs), required, bad, k)
		score.ContextScored = true
		score.ContextRelevantAtK = contextMetrics.RelevantAtK
		score.ContextRelevantTotal = contextMetrics.RelevantTotal
		score.ContextBadAtK = contextMetrics.BadAtK
		score.ContextRecallAtK = contextMetrics.RecallAtK
		score.ContextMRR = contextMetrics.MRR
		score.ContextNDCGAtK = contextMetrics.NDCGAtK
		score.ContextFirstRequiredRank = firstRank(contextRefs, qrel.RequiredRefs, mapping)
		score.ContextFirstBadRank = firstRank(contextRefs, qrel.BadRefs, mapping)
		score.ContextMissingRequired = missingRequiredRefs(qrel.RequiredRefs, contextRefs, mapping, k)
		score.ContextBadRefsAtK = badRefsAtK(qrel.BadRefs, contextRefs, mapping, k)
	}
	if len(qrel.RequiredEvidenceRefs) > 0 || len(qrel.BadEvidenceRefs) > 0 {
		evidenceRefs := scoring.refsForQRel(trace.ContextEvidenceRefs, qrel.RequiredEvidenceRefs, qrel.BadEvidenceRefs)
		evidenceAvailable := trace.ContextEvidenceRefs != nil
		if trace.ContextEvidenceRefs == nil && qrelTargetsOnlyType(qrel.RequiredEvidenceRefs, qrel.BadEvidenceRefs, mapping, "fragment") {
			evidenceRefs = fragmentRefs(trace.ContextRefs)
			evidenceAvailable = evidenceRefs != nil
		}
		if evidenceAvailable {
			evidenceMetrics := recallquality.ScoreAtK(resultRefs(evidenceRefs), requiredEvidence, badEvidence, k)
			score.EvidenceScored = true
			score.EvidenceRelevantAtK = evidenceMetrics.RelevantAtK
			score.EvidenceRelevantTotal = evidenceMetrics.RelevantTotal
			score.EvidenceBadAtK = evidenceMetrics.BadAtK
			score.EvidenceRecallAtK = evidenceMetrics.RecallAtK
			score.EvidenceMRR = evidenceMetrics.MRR
			score.EvidenceNDCGAtK = evidenceMetrics.NDCGAtK
			score.EvidenceFirstRequiredRank = firstRank(evidenceRefs, qrel.RequiredEvidenceRefs, mapping)
			score.EvidenceFirstBadRank = firstRank(evidenceRefs, qrel.BadEvidenceRefs, mapping)
			score.EvidenceMissingRequired = missingRequiredRefs(qrel.RequiredEvidenceRefs, evidenceRefs, mapping, k)
			score.EvidenceBadRefsAtK = badRefsAtK(qrel.BadEvidenceRefs, evidenceRefs, mapping, k)
		}
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
	return comparisonForSummaries(baseline, candidate, baseline.SeedHash), nil
}

func comparisonForSummaries(baseline, candidate Summary, seedHash string) Comparison {
	return Comparison{
		BaselineRunID:       baseline.RunID,
		CandidateRunID:      candidate.RunID,
		SeedHash:            seedHash,
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
	}
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
			"average_dream_recall_at_k":    summary.AverageDreamRecallAtK,
			"dream_required_rank1_rate":    summary.DreamRequiredRank1Rate,
			"average_dream_bad_at_k":       summary.AverageDreamBadAtK,
			"dream_bad_rank1_rate":         summary.DreamBadRank1Rate,
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
	if gates.DreamAny() {
		if summary.DreamScoredCaseCount == 0 {
			result.Passed = false
			result.Failures = append(result.Failures, "dream metrics unavailable: no scored traces included dream qrels and dream_refs")
		} else {
			checkMin("average_dream_recall_at_k", summary.AverageDreamRecallAtK, gates.MinDreamRecallAtK)
			checkMin("dream_required_rank1_rate", summary.DreamRequiredRank1Rate, gates.MinDreamRequiredRank1Rate)
			checkMax("average_dream_bad_at_k", summary.AverageDreamBadAtK, gates.MaxAverageDreamBadAtK)
			checkMax("dream_bad_rank1_rate", summary.DreamBadRank1Rate, gates.MaxDreamBadRank1Rate)
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
		g.EvidenceAny() ||
		g.DreamAny()
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

func (g GateOptions) DreamAny() bool {
	return g.MinDreamRecallAtK != nil ||
		g.MinDreamRequiredRank1Rate != nil ||
		g.MaxAverageDreamBadAtK != nil ||
		g.MaxDreamBadRank1Rate != nil
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

type scoringContext struct {
	mapping              KnowledgeMapping
	sourceDocIDsByRefKey map[string][]string
}

func newScoringContext(mapping KnowledgeMapping) scoringContext {
	scoring := scoringContext{
		mapping:              mapping,
		sourceDocIDsByRefKey: map[string][]string{},
	}
	add := func(sourceDocID string, ref Ref) {
		sourceDocID = strings.TrimSpace(sourceDocID)
		ref.Type = strings.TrimSpace(ref.Type)
		ref.ID = strings.TrimSpace(ref.ID)
		if sourceDocID == "" || ref.Type == "" || ref.ID == "" {
			return
		}
		key := refKey(ref.Type, ref.ID)
		if !containsString(scoring.sourceDocIDsByRefKey[key], sourceDocID) {
			scoring.sourceDocIDsByRefKey[key] = append(scoring.sourceDocIDsByRefKey[key], sourceDocID)
		}
	}
	for sourceDocID, ref := range mapping.BySourceDocID {
		add(sourceDocID, ref)
	}
	for sourceDocID, byType := range mapping.BySourceDocIDAndType {
		for refType, refs := range byType {
			for _, ref := range refs {
				if ref.Type == "" {
					ref.Type = refType
				}
				if ref.SourceDocID == "" {
					ref.SourceDocID = sourceDocID
				}
				add(sourceDocID, ref)
			}
		}
	}
	for key := range scoring.sourceDocIDsByRefKey {
		sort.Strings(scoring.sourceDocIDsByRefKey[key])
	}
	return scoring
}

func (s scoringContext) refsForQRel(refs []Ref, required []Ref, bad []Ref) []Ref {
	if refs == nil {
		return nil
	}
	targetTypes := qrelTargetTypes(required, bad, s.mapping)
	if len(targetTypes) == 0 {
		return refs
	}
	out := make([]Ref, 0, len(refs))
	for _, ref := range refs {
		resolved := ref
		if _, ok := targetTypes[strings.TrimSpace(ref.Type)]; !ok {
			if mapped, mappedOK := s.mapRefToTargetType(ref, targetTypes); mappedOK {
				resolved = mapped
				resolved.Rank = ref.Rank
			}
		}
		out = append(out, resolved)
	}
	return out
}

func (s scoringContext) mapRefToTargetType(ref Ref, targetTypes map[string]struct{}) (Ref, bool) {
	sourceDocIDs := s.sourceDocIDsByRefKey[refKey(ref.Type, ref.ID)]
	if len(sourceDocIDs) == 0 {
		return Ref{}, false
	}
	targetTypeList := sortedTargetTypes(targetTypes)
	for _, sourceDocID := range sourceDocIDs {
		byType := s.mapping.BySourceDocIDAndType[sourceDocID]
		if byType == nil {
			continue
		}
		for _, targetType := range targetTypeList {
			refs := byType[targetType]
			if len(refs) == 1 && strings.TrimSpace(refs[0].ID) != "" {
				return refs[0], true
			}
		}
	}
	return Ref{}, false
}

func qrelTargetTypes(required []Ref, bad []Ref, mapping KnowledgeMapping) map[string]struct{} {
	out := map[string]struct{}{}
	add := func(ref Ref) {
		resolved, ok := resolveRef(ref, mapping)
		if !ok {
			return
		}
		if refType := strings.TrimSpace(resolved.Type); refType != "" {
			out[refType] = struct{}{}
		}
	}
	for _, ref := range required {
		add(ref)
	}
	for _, ref := range bad {
		add(ref)
	}
	return out
}

func qrelTargetsOnlyType(required []Ref, bad []Ref, mapping KnowledgeMapping, targetType string) bool {
	targetTypes := qrelTargetTypes(required, bad, mapping)
	if len(targetTypes) != 1 {
		return false
	}
	_, ok := targetTypes[targetType]
	return ok
}

func fragmentRefs(refs []Ref) []Ref {
	if refs == nil {
		return nil
	}
	out := make([]Ref, 0, len(refs))
	for _, ref := range refs {
		if strings.TrimSpace(ref.Type) == "fragment" {
			out = append(out, ref)
		}
	}
	return out
}

func sortedTargetTypes(types map[string]struct{}) []string {
	out := make([]string, 0, len(types))
	for refType := range types {
		out = append(out, refType)
	}
	sort.Strings(out)
	return out
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
