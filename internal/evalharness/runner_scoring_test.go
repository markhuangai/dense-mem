package evalharness

import (
	"strings"
	"testing"
)

func TestMapExpectedDreamsMatchesResolvedSourceRefSet(t *testing.T) {
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-employer", SourceDocID: "doc-employer"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-location", SourceDocID: "doc-location"}, false)
	addDreamSourceRefs(&mapping, "dream-good", []Ref{
		{Type: "fact", ID: "fact-location"},
		{Type: "fact", ID: "fact-employer"},
	})
	addDreamSourceRefs(&mapping, "dream-other", []Ref{
		{Type: "fact", ID: "fact-employer"},
	})

	mapExpectedDreams(&mapping, []ExpectedDream{
		{
			SourceDocID: "expected-dream",
			SourceRefs: []Ref{
				{Type: "fact", SourceDocID: "doc-employer"},
				{Type: "fact", SourceDocID: "doc-location"},
			},
		},
		{
			SourceDocID: "unmapped-dream",
			SourceRefs:  []Ref{{Type: "fact", SourceDocID: "missing-source"}},
		},
	})

	resolved, ok := resolveRef(Ref{Type: "dream", SourceDocID: "expected-dream"}, mapping)
	if !ok || resolved.ID != "dream-good" {
		t.Fatalf("expected dream resolved = %+v, %v", resolved, ok)
	}
	if _, ok := resolveRef(Ref{Type: "dream", SourceDocID: "unmapped-dream"}, mapping); ok {
		t.Fatal("unmapped expected dream resolved")
	}
	if !sameRefSet([]Ref{{Type: "fact", ID: "a"}, {Type: "fact", ID: "b"}}, []Ref{{Type: "fact", ID: "b"}, {Type: "fact", ID: "a"}}) {
		t.Fatal("sameRefSet should ignore ordering")
	}
	if sameRefSet([]Ref{{Type: "fact", ID: "a"}}, []Ref{{Type: "claim", ID: "a"}}) {
		t.Fatal("sameRefSet should include type")
	}
}

func TestResolveRefPrefersTypeAwareSourceMapping(t *testing.T) {
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "fragment-tiered", SourceDocID: "doc-tiered"}, true)
	addSourceMapping(&mapping, Ref{Type: "claim", ID: "claim-tiered", SourceDocID: "doc-tiered"}, false)
	addSourceMapping(&mapping, Ref{Type: "fact", ID: "fact-tiered", SourceDocID: "doc-tiered"}, false)

	fragment, ok := resolveRef(Ref{Type: "fragment", SourceDocID: "doc-tiered"}, mapping)
	if !ok || fragment.ID != "fragment-tiered" {
		t.Fatalf("fragment resolve = %+v, %v", fragment, ok)
	}
	claim, ok := resolveRef(Ref{Type: "claim", SourceDocID: "doc-tiered"}, mapping)
	if !ok || claim.ID != "claim-tiered" {
		t.Fatalf("claim resolve = %+v, %v", claim, ok)
	}
	fact, ok := resolveRef(Ref{Type: "fact", SourceDocID: "doc-tiered"}, mapping)
	if !ok || fact.ID != "fact-tiered" {
		t.Fatalf("fact resolve = %+v, %v", fact, ok)
	}
	fallback, ok := resolveRef(Ref{SourceDocID: "doc-tiered"}, mapping)
	if !ok || fallback.ID != "fragment-tiered" {
		t.Fatalf("fallback resolve = %+v, %v", fallback, ok)
	}
	legacySourceDoc, ok := resolveRef(Ref{Type: "source_doc", SourceDocID: "doc-tiered"}, mapping)
	if !ok || legacySourceDoc.ID != "fragment-tiered" || legacySourceDoc.Type != "fragment" {
		t.Fatalf("legacy source_doc resolve = %+v, %v", legacySourceDoc, ok)
	}

	fragmentOnly := newKnowledgeMapping()
	addSourceMapping(&fragmentOnly, Ref{Type: "fragment", ID: "fragment-only", SourceDocID: "doc-fragment-only"}, true)
	if resolved, ok := resolveRef(Ref{Type: "fact", SourceDocID: "doc-fragment-only"}, fragmentOnly); ok {
		t.Fatalf("fact ref resolved to fragment mapping: %+v", resolved)
	}

	multi := newKnowledgeMapping()
	addSourceMapping(&multi, Ref{Type: "claim", ID: "claim-1", SourceDocID: "doc-multi"}, false)
	addSourceMapping(&multi, Ref{Type: "claim", ID: "claim-2", SourceDocID: "doc-multi"}, false)
	addSourceMapping(&multi, Ref{Type: "claim", ID: "claim-1", SourceDocID: "doc-multi:claim:1"}, false)
	addSourceMapping(&multi, Ref{Type: "claim", ID: "claim-2", SourceDocID: "doc-multi:claim:2"}, false)
	if resolved, ok := resolveRef(Ref{Type: "claim", SourceDocID: "doc-multi"}, multi); ok {
		t.Fatalf("ambiguous claim ref resolved: %+v", resolved)
	}
	aliased, ok := resolveRef(Ref{Type: "claim", SourceDocID: "doc-multi:claim:2"}, multi)
	if !ok || aliased.ID != "claim-2" {
		t.Fatalf("alias claim resolve = %+v, %v", aliased, ok)
	}
}

func TestEvaluateGates(t *testing.T) {
	minRecall := 0.8
	minRank1 := 0.5
	maxBad := 0.25
	maxBadRank1 := 0.1
	gates := GateOptions{
		MinRecallAtK:         &minRecall,
		MinRequiredRank1Rate: &minRank1,
		MaxAverageBadAtK:     &maxBad,
		MaxBadRank1Rate:      &maxBadRank1,
	}
	passed := EvaluateGates(Summary{
		AverageRecallAtK:  0.9,
		RequiredRank1Rate: 0.6,
		AverageBadAtK:     0.1,
		BadRank1Rate:      0,
	}, gates)
	if !passed.Passed || len(passed.Failures) != 0 {
		t.Fatalf("passed gate = %+v", passed)
	}
	failed := EvaluateGates(Summary{
		AverageRecallAtK:  0.7,
		RequiredRank1Rate: 0.4,
		AverageBadAtK:     0.3,
		BadRank1Rate:      0.2,
	}, gates)
	if failed.Passed || len(failed.Failures) != 4 {
		t.Fatalf("failed gate = %+v; want four failures", failed)
	}
}

func TestEvaluateGatesChecksContextMetrics(t *testing.T) {
	minRecall := 0.8
	minRank1 := 0.5
	maxBad := 0.25
	maxBadRank1 := 0.1
	gates := GateOptions{
		MinContextRecallAtK:         &minRecall,
		MinContextRequiredRank1Rate: &minRank1,
		MaxAverageContextBadAtK:     &maxBad,
		MaxContextBadRank1Rate:      &maxBadRank1,
	}
	passed := EvaluateGates(Summary{
		ContextScoredCaseCount:   2,
		AverageContextRecallAtK:  0.9,
		ContextRequiredRank1Rate: 0.6,
		AverageContextBadAtK:     0.1,
		ContextBadRank1Rate:      0,
	}, gates)
	if !passed.Passed || len(passed.Failures) != 0 {
		t.Fatalf("passed context gate = %+v", passed)
	}
	failed := EvaluateGates(Summary{
		ContextScoredCaseCount:   2,
		AverageContextRecallAtK:  0.7,
		ContextRequiredRank1Rate: 0.4,
		AverageContextBadAtK:     0.3,
		ContextBadRank1Rate:      0.2,
	}, gates)
	if failed.Passed || len(failed.Failures) != 4 {
		t.Fatalf("failed context gate = %+v; want four failures", failed)
	}
	unavailable := EvaluateGates(Summary{}, gates)
	if unavailable.Passed || len(unavailable.Failures) != 1 || !strings.Contains(unavailable.Failures[0], "context metrics unavailable") {
		t.Fatalf("unavailable context gate = %+v", unavailable)
	}
}

func TestEvaluateGatesChecksEvidenceMetrics(t *testing.T) {
	minRecall := 0.8
	minRank1 := 0.5
	maxBad := 0.25
	maxBadRank1 := 0.1
	gates := GateOptions{
		MinEvidenceRecallAtK:         &minRecall,
		MinEvidenceRequiredRank1Rate: &minRank1,
		MaxAverageEvidenceBadAtK:     &maxBad,
		MaxEvidenceBadRank1Rate:      &maxBadRank1,
	}
	passed := EvaluateGates(Summary{
		EvidenceScoredCaseCount:   2,
		AverageEvidenceRecallAtK:  0.9,
		EvidenceRequiredRank1Rate: 0.6,
		AverageEvidenceBadAtK:     0.1,
		EvidenceBadRank1Rate:      0,
	}, gates)
	if !passed.Passed || len(passed.Failures) != 0 {
		t.Fatalf("passed evidence gate = %+v", passed)
	}
	failed := EvaluateGates(Summary{
		EvidenceScoredCaseCount:   2,
		AverageEvidenceRecallAtK:  0.7,
		EvidenceRequiredRank1Rate: 0.4,
		AverageEvidenceBadAtK:     0.3,
		EvidenceBadRank1Rate:      0.2,
	}, gates)
	if failed.Passed || len(failed.Failures) != 4 {
		t.Fatalf("failed evidence gate = %+v; want four failures", failed)
	}
	unavailable := EvaluateGates(Summary{}, gates)
	if unavailable.Passed || len(unavailable.Failures) != 1 || !strings.Contains(unavailable.Failures[0], "evidence metrics unavailable") {
		t.Fatalf("unavailable evidence gate = %+v", unavailable)
	}
}

func TestEvaluateGatesChecksDreamMetrics(t *testing.T) {
	minRecall := 0.8
	minRank1 := 0.5
	maxBad := 0.25
	maxBadRank1 := 0.1
	gates := GateOptions{
		MinDreamRecallAtK:         &minRecall,
		MinDreamRequiredRank1Rate: &minRank1,
		MaxAverageDreamBadAtK:     &maxBad,
		MaxDreamBadRank1Rate:      &maxBadRank1,
	}
	passed := EvaluateGates(Summary{
		DreamScoredCaseCount:   2,
		AverageDreamRecallAtK:  0.9,
		DreamRequiredRank1Rate: 0.6,
		AverageDreamBadAtK:     0.1,
		DreamBadRank1Rate:      0,
	}, gates)
	if !passed.Passed || len(passed.Failures) != 0 {
		t.Fatalf("passed dream gate = %+v", passed)
	}
	failed := EvaluateGates(Summary{
		DreamScoredCaseCount:   2,
		AverageDreamRecallAtK:  0.7,
		DreamRequiredRank1Rate: 0.4,
		AverageDreamBadAtK:     0.3,
		DreamBadRank1Rate:      0.2,
	}, gates)
	if failed.Passed || len(failed.Failures) != 4 {
		t.Fatalf("failed dream gate = %+v; want four failures", failed)
	}
	unavailable := EvaluateGates(Summary{}, gates)
	if unavailable.Passed || len(unavailable.Failures) != 1 || !strings.Contains(unavailable.Failures[0], "dream metrics unavailable") {
		t.Fatalf("unavailable dream gate = %+v", unavailable)
	}
}

func TestValidateRequiredQRelMappingsRejectsUnmappedRequiredRefs(t *testing.T) {
	qrels := map[string]QRel{
		"case-1": {
			CaseID:               "case-1",
			RequiredRefs:         []Ref{{Type: "fact", SourceDocID: "doc-required"}},
			BadRefs:              []Ref{{Type: "fact", SourceDocID: "doc-bad"}},
			RequiredEvidenceRefs: []Ref{{Type: "fragment", SourceDocID: "doc-required-evidence"}},
			BadEvidenceRefs:      []Ref{{Type: "fragment", SourceDocID: "doc-bad-evidence"}},
		},
	}
	suite := []SuiteCase{{CaseID: "case-1"}}
	mapping := KnowledgeMapping{
		BySourceDocID: map[string]Ref{
			"doc-required": {Type: "fragment", ID: "fragment-required", SourceDocID: "doc-required"},
			"doc-bad":      {Type: "fragment", ID: "fragment-bad", SourceDocID: "doc-bad"},
		},
		BySourceDocIDAndType: map[string]map[string][]Ref{
			"doc-bad": {
				"fragment": {{Type: "fragment", ID: "fragment-bad", SourceDocID: "doc-bad"}},
			},
		},
	}

	err := validateRequiredQRelMappings(qrels, suite, mapping)

	if err == nil || !strings.Contains(err.Error(), "required qrel ref for case \"case-1\" is unmapped") {
		t.Fatalf("validateRequiredQRelMappings err = %v", err)
	}
	mapping.BySourceDocIDAndType["doc-required"] = map[string][]Ref{
		"fact": {{Type: "fact", ID: "fact-required", SourceDocID: "doc-required"}},
	}
	if err := validateRequiredQRelMappings(qrels, suite, mapping); err == nil || !strings.Contains(err.Error(), "doc-required-evidence") {
		t.Fatalf("validateRequiredQRelMappings unmapped required evidence err = %v", err)
	}
	mapping.BySourceDocIDAndType["doc-required-evidence"] = map[string][]Ref{
		"fragment": {{Type: "fragment", ID: "fragment-required-evidence", SourceDocID: "doc-required-evidence"}},
	}
	if err := validateRequiredQRelMappings(qrels, suite, mapping); err == nil || !strings.Contains(err.Error(), "doc-bad") {
		t.Fatalf("validateRequiredQRelMappings unmapped bad ref err = %v", err)
	}
	mapping.BySourceDocIDAndType["doc-bad"] = map[string][]Ref{
		"fact": {{Type: "fact", ID: "fact-bad", SourceDocID: "doc-bad"}},
	}
	if err := validateRequiredQRelMappings(qrels, suite, mapping); err == nil || !strings.Contains(err.Error(), "doc-bad-evidence") {
		t.Fatalf("validateRequiredQRelMappings unmapped bad evidence err = %v", err)
	}
	mapping.BySourceDocIDAndType["doc-bad-evidence"] = map[string][]Ref{
		"fragment": {{Type: "fragment", ID: "fragment-bad-evidence", SourceDocID: "doc-bad-evidence"}},
	}
	if err := validateRequiredQRelMappings(qrels, suite, mapping); err != nil {
		t.Fatalf("validateRequiredQRelMappings mapped required and evidence refs: %v", err)
	}
}

func TestScoreTracesErrorsAndRefEdges(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1"}}
	cases := map[string]Case{"case-1": {CaseID: "case-1", Query: "query"}}
	_, _, err := ScoreTraces("run", "baseline", "seed", "sha256:test", "suite.jsonl", suite, cases, map[string]QRel{}, nil, KnowledgeMapping{})
	if err == nil {
		t.Fatal("ScoreTraces missing qrels error = nil")
	}
	qrels := map[string]QRel{"case-1": {CaseID: "case-1", RequiredRefs: []Ref{{Type: "fragment", ID: "frag-1"}}}}
	_, _, err = ScoreTraces("run", "baseline", "seed", "sha256:test", "suite.jsonl", suite, cases, qrels, nil, KnowledgeMapping{})
	if err == nil {
		t.Fatal("ScoreTraces missing trace error = nil")
	}
	if got := topKSet([]Ref{{Type: "fragment", ID: "frag-1"}}, -1); len(got) != 0 {
		t.Fatalf("topKSet negative k = %v; want empty", got)
	}
	if _, ok := resolveRef(Ref{Type: "fragment"}, KnowledgeMapping{}); ok {
		t.Fatal("resolveRef without id or source_doc_id ok = true")
	}
}

func TestCompareSummariesRejectsSeedMismatch(t *testing.T) {
	_, err := CompareSummaries(Summary{SeedHash: "sha256:a"}, Summary{SeedHash: "sha256:b"})
	if err == nil {
		t.Fatal("CompareSummaries expected seed mismatch error")
	}
}

func TestScoreTraceKeepsDirectMetricsSeparateFromDiscoveryExposure(t *testing.T) {
	score := ScoreTrace("case-1", 1, QRel{
		CaseID:       "case-1",
		RequiredRefs: []Ref{{Type: "relationship", ID: "relationship-required"}},
	}, RecallTrace{
		CaseID:       "case-1",
		RankedRefs:   []Ref{{Type: "relationship", ID: "relationship-other"}},
		FrontierRefs: []Ref{{Type: "relationship", ID: "relationship-required"}},
	}, KnowledgeMapping{})

	if score.RecallAtK != 0 || score.FirstRequiredRank != 0 {
		t.Fatalf("direct score = %+v; want required relationship absent from initial top-k", score)
	}
	if score.DiscoveryRecall != 1 || score.DiscoveryFirstRequiredRank != 2 || score.DiscoveryExposureCount != 2 || len(score.DiscoveryMissingRequired) != 0 {
		t.Fatalf("discovery score = %+v", score)
	}
}

func TestSummarizeScoresReportsLatencyPercentilesAndComparisonDeltas(t *testing.T) {
	suite := []SuiteCase{{CaseID: "case-1"}, {CaseID: "case-2"}, {CaseID: "case-3"}, {CaseID: "case-4"}}
	cases := map[string]Case{}
	for _, suiteCase := range suite {
		cases[suiteCase.CaseID] = Case{CaseID: suiteCase.CaseID}
	}
	baseline := SummarizeScores("v1", "baseline", "seed", "sha256:same", "suite.jsonl", suite, cases, []RetrievalScore{
		{CaseID: "case-1", LatencyMS: 10, DiscoveryRecall: 0.25, DiscoveryExposureCount: 2},
		{CaseID: "case-2", LatencyMS: 20, DiscoveryRecall: 0.50, DiscoveryExposureCount: 3},
		{CaseID: "case-3", LatencyMS: 30, DiscoveryRecall: 0.75, DiscoveryExposureCount: 4},
		{CaseID: "case-4", LatencyMS: 100, DiscoveryRecall: 1.00, DiscoveryExposureCount: 5},
	})
	if baseline.AverageLatencyMS != 40 || baseline.P50LatencyMS != 20 || baseline.P95LatencyMS != 100 {
		t.Fatalf("latency summary = %+v", baseline)
	}
	if baseline.AverageDiscoveryRecall != 0.625 || baseline.AverageDiscoveryExposure != 3.5 {
		t.Fatalf("discovery summary = %+v", baseline)
	}

	candidate := baseline
	candidate.RunID = "v2"
	candidate.AverageDiscoveryRecall = 0.75
	candidate.AverageLatencyMS = 35
	candidate.P50LatencyMS = 18
	candidate.P95LatencyMS = 90
	comparison, err := CompareSummaries(baseline, candidate)
	if err != nil {
		t.Fatalf("CompareSummaries: %v", err)
	}
	if comparison.DiscoveryRecallDelta != 0.125 || comparison.LatencyDeltaMS != -5 || comparison.P50LatencyDeltaMS != -2 || comparison.P95LatencyDeltaMS != -10 {
		t.Fatalf("comparison = %+v", comparison)
	}
}
