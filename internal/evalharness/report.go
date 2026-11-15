package evalharness

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
)

type CaseScore struct {
	QuestionID          string  `json:"question_id"`
	Condition           string  `json:"condition"`
	Policy              string  `json:"policy"`
	Completed           bool    `json:"completed"`
	Recall1             float64 `json:"recall_at_1"`
	Recall5             float64 `json:"recall_at_5"`
	Recall10            float64 `json:"recall_at_10"`
	AllSupport          bool    `json:"all_support_retrieved"`
	InitialTextCoverage float64 `json:"initial_supporting_text_coverage"`
	FinalTextCoverage   float64 `json:"final_supporting_text_coverage"`
	EM                  float64 `json:"exact_match"`
	F1                  float64 `json:"f1"`
	Citations           int     `json:"citations"`
	ValidCitations      int     `json:"valid_citations"`
	CitedGoldCoverage   float64 `json:"cited_gold_source_coverage"`
	MemoryCalls         int     `json:"memory_calls"`
	ProviderCalls       int     `json:"provider_calls"`
	UsageUnavailable    int     `json:"usage_unavailable"`
	UsageUnreconciled   int     `json:"usage_unreconciled"`
	InputTokens         int     `json:"reported_input_tokens"`
	OutputTokens        int     `json:"reported_output_tokens"`
	TotalTokens         int     `json:"reported_total_tokens"`
	ElapsedMS           float64 `json:"elapsed_ms"`
}

type AxisSummary struct {
	Condition           string    `json:"condition"`
	Policy              string    `json:"policy"`
	Expected            int       `json:"expected"`
	Observed            int       `json:"observed"`
	Completed           int       `json:"completed"`
	Failed              int       `json:"failed"`
	Missing             int       `json:"missing"`
	Mean                CaseScore `json:"mean"`
	Citations           int       `json:"citations"`
	ValidCitations      int       `json:"valid_citations"`
	NoCitations         int       `json:"answers_without_citations"`
	MemoryCalls         int       `json:"memory_calls"`
	ProviderCalls       int       `json:"provider_calls"`
	UsageUnavailable    int       `json:"usage_unavailable"`
	UsageUnreconciled   int       `json:"usage_unreconciled"`
	InputTokens         int       `json:"reported_input_tokens"`
	OutputTokens        int       `json:"reported_output_tokens"`
	TotalTokens         int       `json:"reported_total_tokens"`
	HostElapsedMS       float64   `json:"host_elapsed_ms"`
	ThroughputPerSecond *float64  `json:"successful_answers_per_host_second"`
	AllSupportRecovery  float64   `json:"all_support_recovery_rate"`
}

type Reproducibility struct {
	Version             int    `json:"version"`
	RunID               string `json:"run_id"`
	WorkflowRunID       string `json:"workflow_run_id"`
	SourceRevision      string `json:"source_revision"`
	Image               string `json:"image"`
	RegistryHash        string `json:"registry_sha256"`
	Dataset             string `json:"dataset"`
	Model               string `json:"model"`
	EmbeddingModel      string `json:"embedding_model"`
	EmbeddingDimensions int    `json:"embedding_dimensions"`
	ContextWindowTokens int    `json:"context_window_tokens"`
	CanonicalHash       string `json:"canonical_sha256"`
	ScoringPolicy       string `json:"scoring_policy"`
}

type Report struct {
	Identity           Reproducibility               `json:"identity"`
	Complete           bool                          `json:"complete"`
	CaptureComplete    bool                          `json:"capture_complete"`
	Failure            string                        `json:"failure,omitempty"`
	ExpectedAnswers    int                           `json:"expected_answers"`
	CompletedAnswers   int                           `json:"completed_answers"`
	CorpusRows         int                           `json:"corpus_rows"`
	ImportedRows       int                           `json:"imported_rows"`
	ElapsedMS          float64                       `json:"elapsed_ms"`
	Scores             []CaseScore                   `json:"scores"`
	Matrix             []AxisSummary                 `json:"comparison_matrix"`
	OntologyDelta      map[string]map[string]float64 `json:"ontology_delta"`
	NativeMeasurements []NativeObservation           `json:"native_measurements"`
	ServerUsage        map[string]UsageTotals        `json:"server_usage"`
	CostUSD            *float64                      `json:"measured_cost_usd"`
	Limitations        []string                      `json:"limitations"`
}

func NormalizeAnswer(answer string) string {
	answer = strings.ToLower(answer)
	answer = strings.Map(func(r rune) rune {
		if r < 128 && (r >= 33 && r <= 47 || r >= 58 && r <= 64 || r >= 91 && r <= 96 || r >= 123 && r <= 126) {
			return -1
		}
		return r
	}, answer)
	words := strings.FieldsFunc(answer, unicode.IsSpace)
	out := words[:0]
	for _, word := range words {
		if word != "a" && word != "an" && word != "the" {
			out = append(out, word)
		}
	}
	return strings.Join(out, " ")
}

func AnswerScore(answer string, aliases []string) (float64, float64) {
	normalized := NormalizeAnswer(answer)
	predicted := strings.Fields(normalized)
	em, f1 := 0.0, 0.0
	for _, alias := range aliases {
		gold := NormalizeAnswer(alias)
		if gold == normalized {
			em = 1
		}
		expected := strings.Fields(gold)
		if len(predicted) == 0 || len(expected) == 0 {
			if normalized == gold {
				f1 = 1
			}
			continue
		}
		counts := map[string]int{}
		for _, token := range expected {
			counts[token]++
		}
		common := 0
		for _, token := range predicted {
			if counts[token] > 0 {
				common++
				counts[token]--
			}
		}
		if common > 0 {
			candidate := 2 * float64(common) / float64(len(predicted)+len(expected))
			f1 = max(f1, candidate)
		}
	}
	return em, f1
}

func ScoreObservation(question Question, observation Observation, corpus map[string]CorpusItem, mapping KnowledgeMapping, canonical map[string]CanonicalEvidence) ([]CaseScore, error) {
	if question.ID != observation.QuestionID || (observation.Condition != "off" && observation.Condition != "on") {
		return nil, errors.New("observation identity mismatch")
	}
	gold := map[string]bool{}
	for _, id := range question.GoldSources {
		if _, ok := corpus[id]; !ok {
			return nil, errors.New("unmapped gold source")
		}
		gold[id] = true
	}
	if len(gold) == 0 {
		return nil, errors.New("question has no gold sources")
	}
	recall := func(k int) float64 {
		if observation.Initial.Error != "" {
			return 0
		}
		found := map[string]bool{}
		rows, _ := observation.Initial.Result["results"].([]any)
		for index, raw := range rows {
			if index >= k {
				break
			}
			row, _ := raw.(map[string]any)
			evidence := canonical[stringValue(row["evidence_id"])]
			if gold[evidence.SourceID] {
				found[evidence.SourceID] = true
			}
		}
		return float64(len(found)) / float64(len(gold))
	}
	coverage := func(calls []MemoryCall) float64 {
		delivered := map[string][]string{}
		for _, call := range calls {
			for id, texts := range deliveredText(call) {
				delivered[id] = append(delivered[id], texts...)
			}
		}
		recovered := 0
		for source := range gold {
			parts := mapping.Partitions[source]
			complete := len(parts) > 0
			for _, part := range parts {
				runes := []rune(corpus[source].Content)
				if part.SpanStart < 0 || part.SpanEnd > len(runes) || part.SpanEnd <= part.SpanStart {
					return 0
				}
				quote := string(runes[part.SpanStart:part.SpanEnd])
				found := false
				for _, text := range delivered[part.EvidenceID] {
					found = found || strings.Contains(text, quote)
				}
				complete = complete && found
			}
			if complete {
				recovered++
			}
		}
		return float64(recovered) / float64(len(gold))
	}
	result := []CaseScore{}
	seen := map[string]bool{}
	for _, host := range observation.Hosts {
		if seen[host.Policy] || (host.Policy != "single" && host.Policy != "multi_turn") {
			return nil, errors.New("duplicate or invalid answer policy")
		}
		seen[host.Policy] = true
		if len(host.Calls) == 0 || len(host.Calls) > 8 || host.Policy == "single" && len(host.Calls) != 1 {
			return nil, errors.New("invalid memory call count")
		}
		initial, _ := json.Marshal(observation.Initial)
		shared, _ := json.Marshal(host.Calls[0])
		if string(initial) != string(shared) {
			return nil, errors.New("host initial Recall packet changed")
		}
		score := CaseScore{QuestionID: question.ID, Condition: observation.Condition, Policy: host.Policy, Completed: host.Error == "", Recall1: recall(1), Recall5: recall(5), Recall10: recall(10), InitialTextCoverage: coverage([]MemoryCall{observation.Initial}), FinalTextCoverage: coverage(host.Calls), MemoryCalls: len(host.Calls), ProviderCalls: len(host.Attempts), ElapsedMS: host.ElapsedMS}
		score.AllSupport = score.Recall10 == 1
		if score.Completed {
			score.EM, score.F1 = AnswerScore(host.Answer, question.Answers)
		}
		texts := map[string][]string{}
		for _, call := range host.Calls {
			for id, values := range deliveredText(call) {
				texts[id] = append(texts[id], values...)
			}
		}
		cited := map[string]bool{}
		for _, citation := range host.Citations {
			score.Citations++
			valid := false
			for _, text := range texts[citation.EvidenceID] {
				valid = valid || citation.Quote != "" && strings.Contains(text, citation.Quote)
			}
			if _, ok := canonical[citation.EvidenceID]; !ok {
				valid = false
			}
			if !valid {
				continue
			}
			score.ValidCitations++
			for source := range gold {
				for _, part := range mapping.Partitions[source] {
					if part.EvidenceID == citation.EvidenceID && strings.Contains(corpus[source].Content, citation.Quote) {
						cited[source] = true
					}
				}
			}
		}
		score.CitedGoldCoverage = float64(len(cited)) / float64(len(gold))
		for _, attempt := range host.Attempts {
			switch attempt.UsageState {
			case "unavailable":
				score.UsageUnavailable++
			case "unreconciled":
				score.UsageUnreconciled++
			case "observed":
				score.InputTokens += attempt.Input
				score.OutputTokens += attempt.Output
				score.TotalTokens += attempt.Total
			default:
				return nil, errors.New("unknown answer usage state")
			}
		}
		result = append(result, score)
	}
	if len(result) != 2 {
		return nil, errors.New("observation lacks an answer policy")
	}
	return result, nil
}

func NewReport(manifest Manifest, corpusCount, questionCount int) Report {
	return Report{Identity: Reproducibility{Version: 1, RunID: manifest.RunID, WorkflowRunID: manifest.WorkflowRunID, SourceRevision: manifest.SourceRevision, Image: manifest.Image, RegistryHash: manifest.RegistryHash, Dataset: manifest.Dataset, Model: manifest.Model, EmbeddingModel: manifest.EmbeddingModel, EmbeddingDimensions: manifest.EmbeddingDimensions, ContextWindowTokens: manifest.ContextWindowTokens, ScoringPolicy: "public-rag-em-f1-canonical-sources-v1"}, ExpectedAnswers: questionCount * 4, CorpusRows: corpusCount, Scores: []CaseScore{}, NativeMeasurements: []NativeObservation{}, Limitations: []string{"Complete published curated packs; original upstream datasets are larger.", "Pinned dataset README supplies no clear common license; upstream attribution and licensing remain dataset-specific.", "Reported token totals include only available measurements; unavailable and partial measurements remain explicit.", "Actual gateway billing is unavailable; no monetary cost is inferred.", "Retrieval uses original canonical source IDs at the first 1/5/10 Evidence slots; partitions and equivalent IDs add no extra source credit."}}
}

func FinalizeReport(report *Report) error {
	questions := report.ExpectedAnswers / 4
	if questions <= 0 {
		return errors.New("invalid expected denominator")
	}
	report.Matrix = nil
	report.CompletedAnswers = 0
	seen := map[string]bool{}
	for _, score := range report.Scores {
		key := score.Condition + ":" + score.Policy + ":" + score.QuestionID
		if seen[key] {
			return errors.New("duplicate scored observation")
		}
		seen[key] = true
	}
	sort.Slice(report.Scores, func(i, j int) bool {
		a, b := report.Scores[i], report.Scores[j]
		return a.Condition+":"+a.Policy+":"+a.QuestionID < b.Condition+":"+b.Policy+":"+b.QuestionID
	})
	for _, condition := range []string{"off", "on"} {
		for _, policy := range []string{"single", "multi_turn"} {
			axis := AxisSummary{Condition: condition, Policy: policy, Expected: questions}
			for _, score := range report.Scores {
				if score.Condition != condition || score.Policy != policy {
					continue
				}
				axis.Observed++
				if score.Completed {
					axis.Completed++
					report.CompletedAnswers++
				} else {
					axis.Failed++
				}
				axis.Mean.Recall1 += score.Recall1
				axis.Mean.Recall5 += score.Recall5
				axis.Mean.Recall10 += score.Recall10
				if score.AllSupport {
					axis.AllSupportRecovery++
				}
				axis.Mean.InitialTextCoverage += score.InitialTextCoverage
				axis.Mean.FinalTextCoverage += score.FinalTextCoverage
				axis.Mean.EM += score.EM
				axis.Mean.F1 += score.F1
				axis.Mean.CitedGoldCoverage += score.CitedGoldCoverage
				axis.HostElapsedMS += score.ElapsedMS
				axis.Citations += score.Citations
				axis.ValidCitations += score.ValidCitations
				if score.Citations == 0 {
					axis.NoCitations++
				}
				axis.MemoryCalls += score.MemoryCalls
				axis.ProviderCalls += score.ProviderCalls
				axis.UsageUnavailable += score.UsageUnavailable
				axis.UsageUnreconciled += score.UsageUnreconciled
				axis.InputTokens += score.InputTokens
				axis.OutputTokens += score.OutputTokens
				axis.TotalTokens += score.TotalTokens
			}
			axis.Missing = questions - axis.Observed
			if axis.Missing < 0 {
				return errors.New("scored count exceeds public denominator")
			}
			denominator := float64(questions)
			axis.Mean.Recall1 /= denominator
			axis.Mean.Recall5 /= denominator
			axis.Mean.Recall10 /= denominator
			axis.AllSupportRecovery /= denominator
			axis.Mean.InitialTextCoverage /= denominator
			axis.Mean.FinalTextCoverage /= denominator
			axis.Mean.EM /= denominator
			axis.Mean.F1 /= denominator
			axis.Mean.CitedGoldCoverage /= denominator
			if axis.Observed > 0 {
				axis.Mean.ElapsedMS = axis.HostElapsedMS / float64(axis.Observed)
			}
			if axis.HostElapsedMS > 0 {
				throughput := float64(axis.Completed) * 1000 / axis.HostElapsedMS
				axis.ThroughputPerSecond = &throughput
			}
			report.Matrix = append(report.Matrix, axis)
		}
	}
	report.OntologyDelta = map[string]map[string]float64{}
	for _, policy := range []string{"single", "multi_turn"} {
		var off, on AxisSummary
		for _, axis := range report.Matrix {
			if axis.Policy == policy {
				if axis.Condition == "off" {
					off = axis
				} else {
					on = axis
				}
			}
		}
		report.OntologyDelta[policy] = map[string]float64{"recall_at_1": on.Mean.Recall1 - off.Mean.Recall1, "recall_at_5": on.Mean.Recall5 - off.Mean.Recall5, "recall_at_10": on.Mean.Recall10 - off.Mean.Recall10, "all_support_recovery_rate": on.AllSupportRecovery - off.AllSupportRecovery, "exact_match": on.Mean.EM - off.Mean.EM, "f1": on.Mean.F1 - off.Mean.F1, "supporting_text_coverage": on.Mean.FinalTextCoverage - off.Mean.FinalTextCoverage}
	}
	report.Complete = report.Failure == "" && report.CaptureComplete && report.CompletedAnswers == report.ExpectedAnswers && report.ImportedRows == report.CorpusRows
	return nil
}
