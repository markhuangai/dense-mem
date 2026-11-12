package evalharness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/stretchr/testify/require"
)

const supportedAnswerJudgment = `{"answer_correct":true,"abstains":false,"claims":[{"claim":"Alpha is the first Greek letter.","supported":true}],"citation_support":[{"citation_index":0,"supported":true,"reason":"Exact source supports the claim."}],"reason":"Correct from the source."}`

func answerScoringFixture(t *testing.T) PairConfig {
	t.Helper()
	c := pairedFixture(t)
	root := filepath.Dir(c.SeedManifest)
	cases, qrels, suite, labels, traces := []Case{}, []QRel{}, []SuiteCase{}, []AnswerLabel{}, []RecallTrace{}
	for index := 0; index < 204; index++ {
		id := fmt.Sprintf("case-%03d", index)
		cases = append(cases, Case{CaseID: id, Query: "What is alpha?", Limit: 10})
		qrels = append(qrels, QRel{CaseID: id, RequiredRefs: []Ref{{Type: "source_doc", SourceDocID: "doc-alpha", Grade: 1}}})
		suite = append(suite, SuiteCase{CaseID: id})
		traces = append(traces, RecallTrace{CaseID: id, RankedRefs: []Ref{{Type: "fragment", ID: "fragment-alpha", Rank: 1}}})
		if index < 82 {
			labels = append(labels, AnswerLabel{CaseID: id, ReferenceAnswer: "Alpha is the first Greek letter."})
		}
	}
	require.NoError(t, writeJSONL(filepath.Join(root, "cases.jsonl"), cases))
	require.NoError(t, writeJSONL(filepath.Join(root, "qrels.jsonl"), qrels))
	require.NoError(t, writeJSONL(c.Suite, suite))
	references := filepath.Join(root, "answer-references.jsonl")
	require.NoError(t, writeJSONL(references, labels))
	_, _, _, _, _, seedHash, err := loadRunInputs(c.SeedManifest, c.Suite)
	require.NoError(t, err)
	c.SeedHash = seedHash
	c.SuiteSHA256, err = PairFileHash(c.Suite)
	require.NoError(t, err)
	c.Answers = &PairAnswerInputs{ContractSHA256: EvidenceOnlyAnswerContractHash(), References: PairPinnedFile{Path: references}, BaselineResults: map[string]PairPinnedFile{}}
	c.Answers.References.SHA256, err = PairFileHash(references)
	require.NoError(t, err)
	for _, policy := range answerPolicies {
		rows := []any{}
		for _, label := range labels {
			rows = append(rows, map[string]any{"event": "started", "case_id": label.CaseID, "policy": policy}, EvidenceOnlyHostResult{CaseID: label.CaseID, Policy: policy, Outcome: "completed", Answer: label.ReferenceAnswer, Citations: []EvidenceOnlyHostCitation{{EvidenceID: "fragment-alpha", Quote: "first Greek letter"}}, ToolCalls: []evidenceOnlyHostCall{{Tool: "recall_memory"}}})
		}
		path := filepath.Join(root, policy+"-answers.jsonl")
		require.NoError(t, writeJSONL(path, rows))
		hash, err := PairFileHash(path)
		require.NoError(t, err)
		c.Answers.BaselineResults[policy] = PairPinnedFile{Path: path, SHA256: hash}
		candidatePath := filepath.Join(c.OutDir, "answers", "candidate", policy, "host_results.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(candidatePath), 0700))
		require.NoError(t, writeJSONL(candidatePath, rows))
	}
	mapping := newKnowledgeMapping()
	for _, name := range []string{"alpha", "beta"} {
		addSourceMapping(&mapping, Ref{Type: "fragment", ID: "fragment-" + name, SourceDocID: "doc-" + name}, true)
	}
	tracePath, mappingPath := filepath.Join(root, "answer-traces.jsonl"), filepath.Join(root, "answer-mapping.json")
	require.NoError(t, writeJSONL(tracePath, traces))
	require.NoError(t, writeJSONFile(mappingPath, mapping))
	native := func(dir string) {
		t.Helper()
		_, err := Run(context.Background(), RunOptions{Mode: "baseline", SeedManifestPath: c.SeedManifest, SuitePath: c.Suite, OutDir: dir, TracesPath: tracePath, MappingPath: mappingPath})
		require.NoError(t, err)
	}
	c.Baseline.CaptureDir = filepath.Join(root, "answer-capture")
	native(c.Baseline.CaptureDir)
	observation := pairFixtureObservation(t, c, c.Baseline)
	files, err := pairCaptureHashes(c.Baseline.CaptureDir)
	require.NoError(t, err)
	require.NoError(t, writePairJSON(filepath.Join(c.Baseline.CaptureDir, "pair_capture.json"), PairCaptureProof{SchemaVersion: PairSchema, Identity: capturePairIdentity(c, c.Baseline), Before: observation, After: observation, Files: files}))
	c.Baseline.CaptureSHA256, err = PairFileHash(filepath.Join(c.Baseline.CaptureDir, "pair_capture.json"))
	require.NoError(t, err)
	process, err := PairProcessIdentity(os.Getpid())
	require.NoError(t, err)
	for _, stage := range []string{"import", "capture", "score"} {
		start, err := BeginPairStage(c, "candidate", stage, process)
		require.NoError(t, err)
		native(filepath.Join(start.Directory, "native"))
		if stage == "capture" || stage == "import" {
			observation := pairFixtureObservation(t, c, c.Candidate)
			if stage == "capture" {
				require.NoError(t, writePairJSON(filepath.Join(start.Directory, "before.json"), observation))
			}
			require.NoError(t, writePairJSON(filepath.Join(start.Directory, "observation.json"), observation))
		} else {
			capture, err := latestPairStage(c, "candidate", "capture")
			require.NoError(t, err)
			proof, err := VerifyPairCaptureProof(c, c.Candidate, filepath.Join(capture, "native"))
			require.NoError(t, err)
			require.NoError(t, writePairJSON(filepath.Join(start.Directory, "capture-proof.json"), proof))
			require.NoError(t, writePairJSON(filepath.Join(start.Directory, "scorer.json"), c.Scorer))
		}
		require.NoError(t, writePairJSON(filepath.Join(start.Directory, "native-exit.json"), map[string]any{"exit_code": 0, "finished_at": time.Now().UTC()}))
		require.NoError(t, FinishPairStage(c, start.Directory))
	}
	return c
}

func writeAnswerFixtureScores(t *testing.T, c PairConfig) {
	t.Helper()
	hash, err := pairConfigHash(c)
	require.NoError(t, err)
	for sideIndex, side := range []string{"baseline", "candidate"} {
		started := time.Now().UTC().Add(time.Duration(sideIndex*2-10) * time.Minute)
		score := PairAnswerScore{SchemaVersion: "dense-mem.eval.answers.v5", ConfigSHA256: hash, ContractSHA256: EvidenceOnlyAnswerContractHash(), SourceRevision: c.Scorer.SourceRevision, ScorerSHA256: c.Scorer.SHA256, Model: "dense-mem-testing", Side: side, StartedAt: started, FinishedAt: started.Add(time.Minute), Policies: map[string]AnswerPolicyScore{}}
		for _, policy := range answerPolicies {
			path := c.Answers.BaselineResults[policy].Path
			if side == "candidate" {
				path = filepath.Join(c.OutDir, "answers", "candidate", policy, "host_results.jsonl")
			}
			transport := answerTransportFunc(func(_ context.Context, r modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
				require.Equal(t, "dense_mem_evidence_only_judgment_v5", r.SchemaName)
				require.Contains(t, r.Messages[1].Content, `"citation_index":0`)
				require.Contains(t, r.Messages[1].Content, "original_sources")
				return modelprovider.StructuredResult{Content: supportedAnswerJudgment}, nil
			})
			result, err := scoreEvidenceOnlyPolicy(context.Background(), c, side, policy, path, transport, assessor.DefaultSemanticAssessmentLimits())
			require.NoError(t, err)
			require.Equal(t, 82, result.Cases)
			require.Equal(t, 82, result.Correct)
			require.Equal(t, 82, result.CitedAnswers)
			require.True(t, result.ExactQuotations && result.ClaimSupport && result.CitationSupport)
			score.Policies[policy] = result
			if side == "candidate" {
				require.NoError(t, writePairJSON(filepath.Join(filepath.Dir(path), "host_plan.json"), map[string]any{"answer_contract_sha256": EvidenceOnlyAnswerContractHash(), "policy": policy, "started_at": started}))
			}
		}
		require.NoError(t, writeProtectedAnswerJSON(filepath.Join(c.OutDir, "answers", side, "score.json"), score, nil))
	}
}

func TestEvidenceOnlyUnitAnswerScoringReproducesAndRejectsTampering(t *testing.T) {
	c := answerScoringFixture(t)
	writeAnswerFixtureScores(t, c)
	require.NoError(t, VerifyBaselineAnswerScoring(c))
	sides := map[string]PairSideReport{}
	stage, err := latestPairStage(c, "candidate", "score")
	require.NoError(t, err)
	var summary Summary
	require.NoError(t, readJSONFile(filepath.Join(stage, "native", "summary.json"), &summary))
	sides["candidate"] = PairSideReport{Scores: summary}
	quality, err := evaluateEvidenceOnlyQuality(c, sides)
	require.NoError(t, err)
	require.True(t, quality.Passed)
	require.Equal(t, 204, quality.CompleteSupport)
	path := filepath.Join(c.OutDir, "answers", "candidate", "score.json")
	var original PairAnswerScore
	require.NoError(t, readPairJSON(path, &original))
	for _, test := range []struct {
		name string
		edit func(*PairAnswerScore)
		want string
	}{
		{"identity", func(s *PairAnswerScore) { s.SourceRevision = strings.Repeat("f", 40) }, "identity or completeness"},
		{"coverage", func(s *PairAnswerScore) { p := s.Policies["one_call"]; p.Cases = 81; s.Policies["one_call"] = p }, "coverage changed"},
		{"duplicate", func(s *PairAnswerScore) {
			p := s.Policies["one_call"]
			p.CasesDetail[1] = p.CasesDetail[0]
			s.Policies["one_call"] = p
		}, "invalid scored answer receipt"},
		{"input hash", func(s *PairAnswerScore) {
			p := s.Policies["one_call"]
			p.CasesDetail[0].InputSHA256 = "sha256:" + strings.Repeat("f", 64)
			s.Policies["one_call"] = p
		}, "input does not reproduce"},
		{"judgment", func(s *PairAnswerScore) {
			p := s.Policies["one_call"]
			p.CasesDetail[0].Judgment.Correct = false
			s.Policies["one_call"] = p
		}, "differs from terminal"},
		{"aggregate", func(s *PairAnswerScore) { p := s.Policies["one_call"]; p.Correct--; s.Policies["one_call"] = p }, "aggregates do not reproduce"},
		{"foreign case", func(s *PairAnswerScore) {
			p := s.Policies["one_call"]
			p.CasesDetail[0].CaseID = "foreign"
			s.Policies["one_call"] = p
		}, "outside frozen results"},
		{"malformed terminal", func(s *PairAnswerScore) {
			p := s.Policies["one_call"]
			p.CasesDetail[0].Attempts[0].Content = "{"
			s.Policies["one_call"] = p
		}, "EOF"},
		{"missing terminal field", func(s *PairAnswerScore) {
			p := s.Policies["one_call"]
			p.CasesDetail[0].Attempts[0].Content = strings.Replace(supportedAnswerJudgment, `"answer_correct":true,`, "", 1)
			s.Policies["one_call"] = p
		}, "missing answer judgment field"},
		{"invalid complete judgment", func(s *PairAnswerScore) {
			p := s.Policies["one_call"]
			p.CasesDetail[0].Judgment.Claims = []AnswerClaimJudgment{}
			raw, err := json.Marshal(p.CasesDetail[0].Judgment)
			require.NoError(t, err)
			p.CasesDetail[0].Attempts[0].Content = string(raw)
			s.Policies["one_call"] = p
		}, "incomplete answer judgment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := json.Marshal(original)
			require.NoError(t, err)
			var changed PairAnswerScore
			require.NoError(t, json.Unmarshal(raw, &changed))
			test.edit(&changed)
			require.NoError(t, writePairJSON(path, changed))
			_, err = readAndVerifyAnswerScore(c, "candidate")
			require.ErrorContains(t, err, test.want)
		})
	}
	require.NoError(t, writePairJSON(path, original))
	bad := sides["candidate"]
	bad.Scores.AverageRecallAtK = 0
	bad.Scores.UnmappedSourceRefs = 1
	sides["candidate"] = bad
	quality, err = evaluateEvidenceOnlyQuality(c, sides)
	require.NoError(t, err)
	require.False(t, quality.Passed)
	require.Contains(t, quality.Failures, "matched retrieval thresholds not met")
	require.Contains(t, quality.Failures, "retrieval cohort incomplete or references unmapped")
}

func TestEvidenceOnlyUnitAnswerScoringPreservesFailuresAndRejectsChangedInputs(t *testing.T) {
	c := answerScoringFixture(t)
	path := c.Answers.BaselineResults["one_call"].Path
	transport := answerTransportFunc(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		return modelprovider.StructuredResult{}, context.DeadlineExceeded
	})
	score, err := scoreEvidenceOnlyPolicy(context.Background(), c, "baseline", "one_call", path, transport, assessor.DefaultSemanticAssessmentLimits())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Zero(t, score.Cases)
	require.Len(t, score.CasesDetail, 1)
	require.Len(t, score.CasesDetail[0].Attempts, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	score, err = scoreEvidenceOnlyPolicy(ctx, c, "baseline", "one_call", path, transport, assessor.DefaultSemanticAssessmentLimits())
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, score.CasesDetail)
	var labels []AnswerLabel
	require.NoError(t, readJSONL(c.Answers.References.Path, &labels))
	for _, test := range []struct {
		name   string
		labels []AnswerLabel
		want   string
	}{
		{"duplicate", append(append([]AnswerLabel{}, labels...), labels[0]), "duplicate answer reference"},
		{"missing", labels[:81], "frozen answer cohort changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.NoError(t, writeJSONL(c.Answers.References.Path, test.labels))
			_, err := loadAnswerPolicyInput(c, "baseline", "one_call", path)
			require.ErrorContains(t, err, test.want)
		})
	}
	require.NoError(t, writeJSONL(c.Answers.References.Path, labels))
	_, err = loadAnswerPolicyInput(c, "baseline", "one_call", path+"-missing")
	require.Error(t, err)
	changed := c
	changed.Baseline.CaptureSHA256 = "sha256:" + strings.Repeat("f", 64)
	_, err = loadAnswerPolicyInput(changed, "baseline", "one_call", path)
	require.ErrorContains(t, err, "proof hash mismatch")
	_, err = loadAnswerPolicyInput(c, "candidate", "one_call", path+"-missing")
	require.Error(t, err)
	_, err = loadTerminalAnswers(c.Answers.References.Path, "one_call", map[string]AnswerLabel{})
	require.Error(t, err)
	judgment := AnswerJudgment{Claims: []AnswerClaimJudgment{{Claim: " "}}, Citations: []AnswerCitationJudgment{}, Reason: "Complete reason."}
	require.ErrorContains(t, validateAnswerJudgment(judgment, 0), "invalid answer claim")
	for _, raw := range []string{"{", `{"answer_correct":true,"abstains":false,"claims":{},"citation_support":[],"reason":"reason"}`} {
		require.Error(t, validateAnswerJudgmentFields([]byte(raw)))
	}
}

func TestEvidenceOnlyUnitAnswerJudgmentRegenerationAndBudgets(t *testing.T) {
	for _, test := range []struct {
		name, first string
		wantCalls   int
		wantErr     string
		edit        func(*assessor.SemanticAssessmentLimits)
		providerErr error
	}{
		{name: "whole replacement", first: `{"answer_correct":true}`, wantCalls: 2},
		{name: "duplicate index", first: strings.Replace(supportedAnswerJudgment, `"citation_index":0`, `"citation_index":1`, 1), wantCalls: 2},
		{name: "exhausted", first: `{}`, wantCalls: 3, wantErr: "regeneration exhausted"},
		{name: "provider failure", wantCalls: 1, providerErr: context.DeadlineExceeded, wantErr: "deadline exceeded"},
		{name: "input budget", edit: func(l *assessor.SemanticAssessmentLimits) { l.MaxInputTokens = 1 }, wantErr: "input budget"},
		{name: "output budget", edit: func(l *assessor.SemanticAssessmentLimits) { l.MaxOutputTokens = 1 }, wantCalls: 3, wantErr: "token budget"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			transport := answerTransportFunc(func(_ context.Context, r modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
				calls++
				if calls > 1 {
					require.Len(t, r.Messages, 3)
					require.Contains(t, r.Messages[2].Content, "complete replacement")
				}
				content := supportedAnswerJudgment
				if calls == 1 || test.name == "exhausted" {
					if test.first != "" {
						content = test.first
					}
				}
				return modelprovider.StructuredResult{Content: content}, test.providerErr
			})
			limits := assessor.DefaultSemanticAssessmentLimits()
			if test.edit != nil {
				test.edit(&limits)
			}
			judgment, attempts, err := judgeEvidenceOnlyAnswer(context.Background(), transport, []byte(`{"question":"What is alpha?"}`), limits, 1)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
				require.True(t, judgment.Correct)
			}
			require.Len(t, attempts, test.wantCalls)
			require.Equal(t, test.wantCalls, calls)
		})
	}
}

func TestEvidenceOnlyUnitTerminalAnswersRejectIncompleteOrForeignRows(t *testing.T) {
	start := map[string]any{"event": "started", "case_id": "one", "policy": "one_call"}
	terminal := EvidenceOnlyHostResult{CaseID: "one", Policy: "one_call", Outcome: "completed", Answer: "An answer.", ToolCalls: []evidenceOnlyHostCall{{Tool: "recall_memory"}}}
	for _, test := range []struct {
		name string
		rows []any
		want string
	}{
		{"valid", []any{start, terminal}, ""},
		{"missing start", []any{terminal}, "unfinished"},
		{"duplicate start", []any{start, start, terminal}, "duplicate answer start"},
		{"duplicate terminal", []any{start, terminal, terminal}, "duplicate"},
		{"missing terminal", []any{start}, "every frozen answer case"},
		{"foreign case", []any{map[string]any{"event": "started", "case_id": "foreign", "policy": "one_call"}}, "frozen cohort"},
		{"wrong policy", []any{map[string]any{"event": "started", "case_id": "one", "policy": "iterative"}}, "frozen cohort"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "answers.jsonl")
			require.NoError(t, writeJSONL(path, test.rows))
			results, err := loadTerminalAnswers(path, "one_call", map[string]AnswerLabel{"one": {CaseID: "one"}})
			if test.want == "" {
				require.NoError(t, err)
				require.Len(t, results, 1)
			} else {
				require.ErrorContains(t, err, test.want)
			}
		})
	}
	for _, test := range []struct {
		name         string
		edit         func(*EvidenceOnlyHostResult)
		policy, want string
	}{
		{"failed", func(r *EvidenceOnlyHostResult) { r.Outcome = "failed" }, "one_call", "unfinished"},
		{"no calls", func(r *EvidenceOnlyHostResult) { r.ToolCalls = nil }, "one_call", "budget violated"},
		{"too many calls", func(r *EvidenceOnlyHostResult) { r.ToolCalls = make([]evidenceOnlyHostCall, 9) }, "iterative", "budget violated"},
		{"write", func(r *EvidenceOnlyHostResult) { r.ToolCalls = []evidenceOnlyHostCall{{Tool: "remember"}} }, "one_call", "unsupported"},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := terminal
			test.edit(&r)
			r.Policy = test.policy
			start := map[string]any{"event": "started", "case_id": "one", "policy": test.policy}
			path := filepath.Join(t.TempDir(), "answers.jsonl")
			require.NoError(t, writeJSONL(path, []any{start, r}))
			_, err := loadTerminalAnswers(path, test.policy, map[string]AnswerLabel{"one": {CaseID: "one"}})
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestEvidenceOnlyUnitAnswerPinsAndSourcesFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input.json")
	require.NoError(t, os.WriteFile(path, []byte(`{}`), 0600))
	hash, err := PairFileHash(path)
	require.NoError(t, err)
	require.NoError(t, verifyPinnedAnswerFile(PairPinnedFile{Path: path, SHA256: hash}))
	for _, pin := range []PairPinnedFile{{Path: "relative", SHA256: hash}, {Path: path, SHA256: "invalid"}, {Path: path, SHA256: "sha256:" + strings.Repeat("f", 64)}, {Path: path + "-missing", SHA256: hash}} {
		require.Error(t, verifyPinnedAnswerFile(pin))
	}
	require.ErrorContains(t, validatePairAnswers(PairConfig{}), "frozen answer contract")
	c := PairConfig{Answers: &PairAnswerInputs{ContractSHA256: EvidenceOnlyAnswerContractHash()}}
	require.ErrorContains(t, validatePairAnswers(c), "reference-answer identity")
	c.Answers.References.SHA256 = "sha256:a6cf3f37734473f47d36e89462a1e74e904f0362bd2b0fd5e7244b44fbb79fc4"
	require.ErrorContains(t, validatePairAnswers(c), "both retained answer policies")
	c.Answers.BaselineResults = map[string]PairPinnedFile{"one_call": {}, "iterative": {}}
	require.ErrorContains(t, validatePairAnswers(c), "retained answer identity changed")
	require.Error(t, RunPairAnswerScoring(context.Background(), c, "baseline", nil, assessor.DefaultSemanticAssessmentLimits(), nil))
	mapping := newKnowledgeMapping()
	addSourceMapping(&mapping, Ref{Type: "evidence", ID: "one", SourceDocID: "source"}, true)
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: "two", SourceDocID: "source"}, false)
	addSourceMapping(&mapping, Ref{Type: "relationship", ID: "retired", SourceDocID: "source"}, false)
	sources := answerSources(mapping, []CorpusItem{{SourceDocID: "source", Content: "Exact original."}})
	require.Contains(t, sources["one"], "Exact original.")
	require.Contains(t, sources["two"], "Exact original.")
	require.NotContains(t, sources, "retired")
}
