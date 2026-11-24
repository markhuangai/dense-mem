//go:build hosted_evaluation

package topicassignment

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	decisionprovider "github.com/markhuangai/dense-mem/internal/provider/decision"
	"github.com/markhuangai/dense-mem/internal/topicassignment/contract"
	"github.com/stretchr/testify/require"
)

const holdoutDigest = "1f915ba60267d563ad32aeb602ebbb3003f35899a55387030d7d83ce9473ec9c"

type assignmentJudgment struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Expected  []int  `json:"expected"`
	Uncovered bool   `json:"uncovered"`
}

type calibrationCase struct {
	assignmentJudgment
	Clear       *bool                                   `json:"clear"`
	Matched     []string                                `json:"matched_topic_ids"`
	ExpectedIDs []string                                `json:"expected_topic_ids"`
	Exact       *bool                                   `json:"clear_set_exact"`
	Answers     map[string]modelprovider.DecisionAnswer `json:"predicate_answers"`
	Receipts    []modelprovider.DecisionReceipt         `json:"provider_exchanges"`
}

type calibrationReport struct {
	RunID            string                      `json:"workflow_run_id"`
	Source           string                      `json:"source_revision"`
	HoldoutHash      string                      `json:"judgments_sha256"`
	Policy           string                      `json:"policy_version"`
	Method           string                      `json:"method"`
	Provider         string                      `json:"provider"`
	Model            string                      `json:"requested_model"`
	PositiveGate     float64                     `json:"minimum_match_probability"`
	NegativeGate     float64                     `json:"maximum_nonmatch_or_uncovered_probability"`
	Status           string                      `json:"status"`
	Reason           string                      `json:"reason,omitempty"`
	Expected         int                         `json:"expected_cases"`
	Completed        int                         `json:"completed_cases"`
	Clear            int                         `json:"clear_sets"`
	Correct          int                         `json:"exact_clear_sets"`
	Precision        *float64                    `json:"clear_set_precision"`
	Coverage         *float64                    `json:"clear_set_coverage"`
	AllClearExact    *bool                       `json:"all_clear_sets_exact"`
	Calls            int                         `json:"provider_exchanges"`
	UnknownUsage     int                         `json:"exchanges_without_usage"`
	Tokens           modelprovider.DecisionUsage `json:"reported_tokens"`
	Cost             *float64                    `json:"measured_cost_usd"`
	CostAvailability string                      `json:"billing_availability"`
	Topics           []Topic                     `json:"topics"`
	Cases            []calibrationCase           `json:"cases"`
}

func TestHostedTopicAssignmentDecisionHoldout(t *testing.T) {
	require.NoError(t, evalharness.RequireRunner())
	output := os.Getenv("EVAL_DECISION_CALIBRATION_OUTPUT")
	require.True(t, filepath.IsAbs(output))
	data, err := os.ReadFile("../../tests/eval/source_locks/topic_assignment_holdout_v1.json")
	require.NoError(t, err)
	require.Equal(t, holdoutDigest, fmt.Sprintf("%x", sha256.Sum256(data)))
	var lock struct {
		Version string
		Policy  string
		Topics  []Topic
		Cases   []assignmentJudgment
	}
	require.NoError(t, json.Unmarshal(data, &lock))
	require.Equal(t, contract.PolicyVersion, lock.Policy)
	require.Len(t, lock.Cases, 24)
	report := calibrationReport{RunID: os.Getenv("GITHUB_RUN_ID"), Source: os.Getenv("EVAL_SOURCE_REVISION"), HoldoutHash: holdoutDigest, Policy: lock.Policy,
		Method: "Remember Topic assignment Decision routing with a locked candidate catalog", Provider: os.Getenv("EVAL_DECISION_PROVIDER"),
		PositiveGate: positiveGate, NegativeGate: negativeGate, Expected: len(lock.Cases), Status: "incomplete", Topics: lock.Topics, CostAvailability: "vendor responses supply token usage only"}
	require.Regexp(t, "^[a-f0-9]{40}$", report.Source)
	require.Equal(t, report.Source, os.Getenv("GITHUB_SHA"))
	for _, label := range lock.Cases {
		item := calibrationCase{assignmentJudgment: label, ExpectedIDs: []string{}}
		for _, i := range label.Expected {
			require.GreaterOrEqual(t, i, 0)
			require.Less(t, i, len(lock.Topics))
			item.ExpectedIDs = append(item.ExpectedIDs, lock.Topics[i].ID)
		}
		report.Cases = append(report.Cases, item)
	}
	key := os.Getenv("DENSE_MEM_EVAL_DECISION_API_KEY")
	protector := observability.NewCredentialProtector(key)
	t.Cleanup(func() {
		body, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		if key != "" {
			protected, reason := observability.ProtectDiagnosticText(context.Background(), protector, body, 1<<20)
			require.Equal(t, observability.CredentialProtectionAvailable, reason)
			body = protected
		}
		require.NoError(t, os.WriteFile(output, append(body, '\n'), 0600))
	})
	if key == "" {
		report.Status, report.Reason = "unavailable", "dedicated_decision_credential_missing"
		return
	}
	cfg := config.DecisionProviderConfig{Provider: report.Provider, APIKey: key, MaxInputTokens: 32000, MaxQuestionInputTokens: 32000, TimeoutSeconds: 40, MaxConcurrency: 1}
	switch cfg.Provider {
	case "openai":
		cfg.APIURL, cfg.Model = "https://api.openai.com/v1", "gpt-6-luna"
	case "typesafe":
		cfg.APIURL, cfg.Model = "https://api.typesafe.ai/v1", "jev-1.14.0"
	default:
		report.Status, report.Reason = "unavailable", "dedicated_decision_provider_not_selected"
		return
	}
	report.Model = cfg.Model
	logger := observability.NewWithProtector(slog.LevelWarn, protector)
	provider, err := decisionprovider.New(cfg, config.DefaultAIVerifierTokenizer, nil, logger, protector, nil)
	require.NoError(t, err)
	var result modelprovider.DecisionResult
	record := testDecisions(func(ctx context.Context, r modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		var err error
		result, err = provider.Decide(ctx, r)
		return result, err
	})
	abstained := errors.New("calibration observed routing to generation")
	fallback := testTransport(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		return modelprovider.StructuredResult{}, abstained
	})
	owner := NewService(testCatalog(lock.Topics), testConfig(true), fallback, record, assessor.DefaultSemanticAssessmentLimits(), "holdout", nil)
	settings := domain.DecisionRuntimeConfig{Revision: contract.PolicyVersion, Effective: domain.DecisionTasks{TopicAssignment: true},
		Provider: domain.DecisionProviderStatus{Configured: true, Provider: cfg.Provider, Model: cfg.Model}}
	scope := Scope{TeamID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1}
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Minute)
	defer cancel()
	for i := range report.Cases {
		item := &report.Cases[i]
		item.Matched = []string{}
		scope.OperationID = uuid.NewString()
		result = modelprovider.DecisionResult{}
		outcomes, err := owner.Prepare(ctx, scope, []Slice{{Text: item.Text}}, &settings)
		clear := err == nil
		item.Answers = result.Answers
		for _, r := range result.Receipts {
			// The calibration receipt needs numeric usage, model identity and content hashes. Raw exchanges stay in the protected provider boundary.
			r.Diagnostics = nil
			r.ProviderRequestID = ""
			item.Receipts = append(item.Receipts, r)
			report.Calls++
			if r.Usage == nil {
				report.UnknownUsage++
			} else {
				report.Tokens.InputTokens += r.Usage.InputTokens
				report.Tokens.OutputTokens += r.Usage.OutputTokens
				report.Tokens.CachedTokens += r.Usage.CachedTokens
				report.Tokens.CacheWriteTokens += r.Usage.CacheWriteTokens
			}
		}
		if err != nil && !errors.Is(err, abstained) {
			report.Reason = "decision_provider_or_validation_failure"
			t.Fatal("real Topic assignment Decision call failed; see the bounded calibration artifact")
		}
		require.NotEmpty(t, item.Receipts)
		item.Clear = new(clear)
		if clear {
			require.Len(t, outcomes, 1)
			require.Equal(t, "decision", outcomes[0].Method)
			for _, match := range outcomes[0].Matches {
				item.Matched = append(item.Matched, match.ID)
			}
			report.Clear++
			slices.Sort(item.ExpectedIDs)
			slices.Sort(item.Matched)
			item.Exact = new(!item.Uncovered && slices.Equal(item.Matched, item.ExpectedIDs))
			if *item.Exact {
				report.Correct++
			}
		}
		report.Completed++
	}
	report.Status = "measured"
	report.Coverage = new(float64(report.Clear) / float64(report.Expected))
	if report.Clear > 0 {
		report.Precision = new(float64(report.Correct) / float64(report.Clear))
		report.AllClearExact = new(report.Correct == report.Clear)
	} else {
		report.Reason = "no_clear_sets_to_score"
	}
}
