package topics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	"github.com/markhuangai/dense-mem/internal/topics/assignment"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

type testTransport func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error)

func (f testTransport) Complete(c context.Context, r modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return f(c, r)
}

type testDecisions func(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error)

func (f testDecisions) Decide(c context.Context, r modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
	return f(c, r)
}

type testDecisionSettings domain.DecisionRuntimeConfig

func (s testDecisionSettings) DecisionRuntimeConfig(context.Context) (domain.DecisionRuntimeConfig, error) {
	return domain.DecisionRuntimeConfig(s), nil
}

type testCatalog []assignment.Topic

func (c testCatalog) ReadAssignmentCandidates(context.Context, assignment.Scope, assignment.Slice) (assignment.Candidates, error) {
	return assignment.Candidates{Topics: c}, nil
}

type testConfig bool

func (p testConfig) TopicMaintenanceRuntimeConfig(context.Context) (domain.TopicMaintenanceConfig, error) {
	return domain.TopicMaintenanceConfig{Enabled: bool(p), ContextWindowTokens: 500000}, nil
}

type testAssignmentSettings domain.TopicMaintenanceConfig

func (p testAssignmentSettings) TopicMaintenanceRuntimeConfig(context.Context) (domain.TopicMaintenanceConfig, error) {
	return domain.TopicMaintenanceConfig(p), nil
}

func TestMaintenanceAssignmentUsesWindowGenerationSettings(t *testing.T) {
	ctx := context.Background()
	scope := assignment.Scope{TeamID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1, OperationID: uuid.NewString()}
	slices := []assignment.Slice{{Text: "Practice database recovery."}}
	limits := assessor.DefaultSemanticAssessmentLimits()
	window := domain.TopicMaintenanceConfig{Enabled: true, Model: "active-model", ContextWindowTokens: 300000}
	live := testAssignmentSettings{Enabled: true, Model: "next-model", ContextWindowTokens: 200000}
	for _, useDecisions := range []bool{false, true} {
		name := "decisions off"
		if useDecisions {
			name = "decision fallback"
		}
		t.Run(name, func(t *testing.T) {
			var requests []modelprovider.StructuredRequest
			generate := testTransport(func(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
				requests = append(requests, request)
				var input assignmentRequest
				require.NoError(t, json.Unmarshal([]byte(request.Messages[1].Content), &input))
				body, err := json.Marshal(modelAnswer{RequestID: input.RequestID, Status: "uncategorized", Matches: []modelMatch{}, Proposals: []assignment.Proposal{}})
				return modelprovider.StructuredResult{Content: string(body)}, err
			})
			decisionCalls := 0
			decide := testDecisions(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
				decisionCalls++
				require.NoError(t, modelprovider.ValidateDecisionRequest(request))
				uncertain := 0.5
				uncovered := 0.85
				return modelprovider.DecisionResult{Answers: map[string]modelprovider.DecisionAnswer{
					"match_0":   {Type: modelprovider.DecisionPredicate, Probability: &uncertain},
					"uncovered": {Type: modelprovider.DecisionPredicate, Probability: &uncovered},
				}}, nil
			})
			settings := &domain.DecisionRuntimeConfig{Effective: domain.DecisionTasks{TopicAssignment: useDecisions}}
			service := NewAssignmentService(testCatalog{{ID: uuid.NewString(), Version: 1, Label: "Databases"}}, live, generate, decide, limits, "default", nil)
			_, err := service.PrepareMaintenance(ctx, scope, slices, settings, window, nil)
			require.NoError(t, err)
			_, err = service.Prepare(ctx, scope, slices, settings)
			require.NoError(t, err)
			require.Len(t, requests, 2)
			require.Equal(t, window.Model, requests[0].Model)
			require.Equal(t, limits.MaxInputTokens, requests[0].MaxInputTokens)
			require.Equal(t, live.Model, requests[1].Model)
			require.Equal(t, int(live.ContextWindowTokens)-limits.MaxOutputTokens, requests[1].MaxInputTokens)
			if useDecisions {
				require.Equal(t, 2, decisionCalls)
			} else {
				require.Zero(t, decisionCalls)
			}
			service.config = testConfig(false)
			outcomes, err := service.PrepareMaintenance(ctx, scope, slices, settings, window, nil)
			require.NoError(t, err)
			require.Empty(t, outcomes)
			require.Len(t, requests, 2)
		})
	}
}

func TestAssignmentUncoveredSubjectAndPriorInputs(t *testing.T) {
	topic := assignment.Topic{ID: uuid.NewString(), Version: 3, Label: "Postgres"}
	scope := assignment.Scope{TeamID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1, OperationID: uuid.NewString()}
	source := assignment.Slice{Text: "Replicate Postgres across regions for disaster recovery."}
	genCalls, decisionCalls := 0, 0
	narrow := false
	gen := testTransport(func(_ context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		genCalls++
		var input assignmentRequest
		require.NoError(t, json.Unmarshal([]byte(req.Messages[1].Content), &input))
		matches := []map[string]string{}
		if !narrow {
			matches = append(matches, map[string]string{"id": topic.ID, "quote": "Postgres"})
		}
		output := map[string]any{"request_id": input.RequestID, "status": "assigned", "matches": matches, "proposals": []map[string]string{{"label": "Disaster recovery", "description": "Restoring service after failure.", "quote": "disaster recovery"}}}
		b, err := json.Marshal(output)
		return modelprovider.StructuredResult{Content: string(b)}, err
	})
	var uncovered float64
	decisions := testDecisions(func(_ context.Context, req modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		decisionCalls++
		require.NoError(t, modelprovider.ValidateDecisionRequest(req))
		yes := 0.999
		return modelprovider.DecisionResult{Answers: map[string]modelprovider.DecisionAnswer{"match_0": {Type: modelprovider.DecisionPredicate, Probability: &yes}, "uncovered": {Type: modelprovider.DecisionPredicate, Probability: &uncovered}}}, nil
	})
	settings := &domain.DecisionRuntimeConfig{Provider: domain.DecisionProviderStatus{Configured: true}, Effective: domain.DecisionTasks{TopicAssignment: true}}
	s := NewAssignmentService(testCatalog{topic}, testConfig(true), gen, decisions, assessor.DefaultSemanticAssessmentLimits(), "fixture", nil)
	result, err := s.Prepare(context.Background(), scope, []assignment.Slice{source}, settings)
	require.NoError(t, err)
	require.Equal(t, "decision", result[0].Method)
	require.Len(t, result[0].Matches, 1)
	require.Zero(t, genCalls)
	source.PriorInputHash = result[0].InputHash
	repeated, err := s.Prepare(context.Background(), scope, []assignment.Slice{source}, settings)
	require.NoError(t, err)
	require.True(t, repeated[0].Unchanged)
	require.Equal(t, 1, decisionCalls)
	source.PriorInputHash = ""
	uncovered = 0.999
	narrow = true
	result, err = s.Prepare(context.Background(), scope, []assignment.Slice{source}, settings)
	require.NoError(t, err)
	require.Len(t, result[0].Matches, 1)
	require.Len(t, result[0].Proposals, 1)
	require.Equal(t, "generation", result[0].Method)
	require.Equal(t, 1, genCalls)
	settings.Effective.TopicAssignment = false
	narrow = false
	_, err = s.Prepare(context.Background(), scope, []assignment.Slice{source}, settings)
	require.NoError(t, err)
	require.Equal(t, 2, genCalls)
	require.Equal(t, 2, decisionCalls)
}

func TestAssignmentAuthoritativeDecisionRoutingAndLogs(t *testing.T) {
	topic := assignment.Topic{ID: uuid.NewString(), Version: 1, Label: "Database"}
	for _, tc := range []struct {
		name                string
		match, uncovered    float64
		decisionErr         error
		matches, generation int
	}{
		{name: "match", match: .85, uncovered: .20, matches: 1},
		{name: "uncategorized", match: .20, uncovered: .20},
		{name: "uncovered", match: .85, uncovered: .85, matches: 1, generation: 1},
		{name: "budget", decisionErr: modelprovider.ErrDecisionInputBudgetExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
			calls := 0
			generate := testTransport(func(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
				calls++
				var input assignmentRequest
				require.NoError(t, json.Unmarshal([]byte(request.Messages[1].Content), &input))
				body, err := json.Marshal(modelAnswer{RequestID: input.RequestID, Status: "uncategorized", Matches: []modelMatch{}, Proposals: []assignment.Proposal{}})
				return modelprovider.StructuredResult{Content: string(body)}, err
			})
			decide := testDecisions(func(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
				return modelprovider.DecisionResult{Answers: map[string]modelprovider.DecisionAnswer{
					"match_0":   {Type: modelprovider.DecisionPredicate, Probability: &tc.match},
					"uncovered": {Type: modelprovider.DecisionPredicate, Probability: &tc.uncovered},
				}}, tc.decisionErr
			})
			service := NewAssignmentService(testCatalog{topic}, testConfig(true), generate, decide, assessor.DefaultSemanticAssessmentLimits(), "fixture", logger)
			scope := assignment.Scope{TeamID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1, OperationID: uuid.NewString()}
			settings := &domain.DecisionRuntimeConfig{Revision: "settings-1", Effective: domain.DecisionTasks{TopicAssignment: true}}
			outcomes, err := service.Prepare(t.Context(), scope, []assignment.Slice{{Text: "Database evidence."}}, settings)
			if tc.decisionErr != nil {
				require.ErrorIs(t, err, tc.decisionErr)
				require.Contains(t, logs.String(), `"reason_code":"decision_input_budget_exceeded"`)
				require.Contains(t, logs.String(), `"generation_triggered":false`)
			} else {
				require.NoError(t, err)
				require.Len(t, outcomes[0].Matches, tc.matches)
				require.Contains(t, logs.String(), `"policy_mode":"authoritative_closed_set"`)
				require.Contains(t, logs.String(), `"settings_version":"settings-1"`)
				require.Contains(t, logs.String(), fmt.Sprintf(`"generation_triggered":%t`, tc.generation > 0))
			}
			require.Contains(t, logs.String(), `"team_id":"`+scope.TeamID+`"`)
			require.Equal(t, tc.generation, calls)
		})
	}
}

func TestAssignmentAllowsAbstentionAndRejectsUngroundedOrMalformedResponses(t *testing.T) {
	input := assignmentRequest{RequestID: "fixture", Text: "Unclear."}
	empty := `{"request_id":"fixture","status":"uncategorized","matches":[],"proposals":[]}`
	answer, err := validateGeneration(input, []byte(empty))
	require.NoError(t, err)
	require.Empty(t, answer.Matches)
	for _, raw := range []string{
		`{"REQUEST_ID":"fixture","status":"uncategorized","matches":[],"proposals":[]}`,
		`{"request_id":"different","status":"uncategorized","matches":[],"proposals":[]}`,
		`{"request_id":"fixture","status":"assigned","matches":[],"proposals":[]}`,
		`{"request_id":"fixture","status":"uncategorized","status":"uncategorized","matches":[],"proposals":[]}`,
		`{"request_id":"fixture","status":"assigned","matches":[],"proposals":[{"label":"Travel","description":"Travel costs","quote":"ticket"}]}`,
		`{"request_id":"fixture","status":"assigned","matches":[{"id":"not-supplied","quote":"Unclear"}],"proposals":[]}`,
	} {
		_, err := validateGeneration(input, []byte(raw))
		require.Error(t, err)
	}
}

func TestRequiredDecisionOutageCannotCreateTopic(t *testing.T) {
	cause := errors.New("unavailable")
	s := NewAssignmentService(testCatalog{{ID: uuid.NewString(), Version: 1, Label: "Databases"}}, testConfig(true), nil, testDecisions(func(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		return modelprovider.DecisionResult{}, cause
	}), assessor.DefaultSemanticAssessmentLimits(), "fixture", nil)
	ctx := context.Background()
	scope := assignment.Scope{TeamID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1, OperationID: uuid.NewString()}
	slices := []assignment.Slice{{Text: "Database restore drill"}}
	settings := &domain.DecisionRuntimeConfig{Effective: domain.DecisionTasks{TopicAssignment: true}}
	out, err := s.Prepare(ctx, scope, slices, settings)
	require.ErrorIs(t, err, cause)
	require.Empty(t, out)
	s.config = testConfig(false)
	out, err = s.Prepare(ctx, scope, slices, settings)
	require.NoError(t, err)
	require.Empty(t, out)
}

func TestEquivalenceDecisionBatchThresholdAndCompleteValidation(t *testing.T) {
	items := []preparedEquivalence{
		{seed: contract.SourceHandle{ID: "first"}, request: assessment.Request{RequestID: "one", EquivalenceOnly: true,
			Items: []assessment.Item{{Ref: "a", Kind: contract.EvidenceSource}, {Ref: "b", Kind: contract.EvidenceSource}},
			Pairs: []assessment.Pair{{Ref: "p0", LeftRef: "a", RightRef: "b"}}}},
		{seed: contract.SourceHandle{ID: "second"}, request: assessment.Request{RequestID: "two", EquivalenceOnly: true,
			Items: []assessment.Item{{Ref: "a", Kind: contract.EvidenceSource}, {Ref: "b", Kind: contract.EvidenceSource}},
			Pairs: []assessment.Pair{{Ref: "p0", LeftRef: "a", RightRef: "b"}}}},
	}
	var calls int
	service := &MaintenanceService{deps: MaintenanceDependencies{Decisions: testDecisions(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		calls++
		require.Len(t, request.Groups, 2)
		yes, no := .95, .949
		return modelprovider.DecisionResult{Answers: map[string]modelprovider.DecisionAnswer{
			"e0_p0": {Type: modelprovider.DecisionPredicate, Probability: &yes},
			"e1_p0": {Type: modelprovider.DecisionPredicate, Probability: &no},
		}}, nil
	})}}
	responses, err := service.decideEquivalence(t.Context(), contract.MaintenanceClaim{ID: uuid.NewString()}, items, domain.DecisionRuntimeConfig{}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Equal(t, "equivalent", responses[0].Equivalence[0].Relation)
	require.Equal(t, "distinct", responses[1].Equivalence[0].Relation)

	items = items[:1]
	items[0].request.Items = append(items[0].request.Items, assessment.Item{Ref: "c", Kind: contract.EvidenceSource})
	items[0].request.Pairs = append(items[0].request.Pairs,
		assessment.Pair{Ref: "p1", LeftRef: "b", RightRef: "c"},
		assessment.Pair{Ref: "p2", LeftRef: "a", RightRef: "c"})
	calls = 0
	service.deps.Decisions = testDecisions(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		calls++
		yes, no := .99, .01
		return modelprovider.DecisionResult{Answers: map[string]modelprovider.DecisionAnswer{
			"e0_p0": {Type: modelprovider.DecisionPredicate, Probability: &yes},
			"e0_p1": {Type: modelprovider.DecisionPredicate, Probability: &yes},
			"e0_p2": {Type: modelprovider.DecisionPredicate, Probability: &no},
		}}, nil
	})
	responses, err = service.decideEquivalence(t.Context(), contract.MaintenanceClaim{ID: uuid.NewString()}, items, domain.DecisionRuntimeConfig{}, nil)
	require.Error(t, err)
	require.Nil(t, responses)
	require.Equal(t, modelprovider.StructuredResponseLimit, calls)
}

func TestTopicConsolidationDecisionThresholdAndCompleteValidation(t *testing.T) {
	pairs := []contract.TopicConsolidationPair{
		{Left: contract.CommunityCandidate{ID: "left-0"}, Right: contract.CommunityCandidate{ID: "right-0"}},
		{Left: contract.CommunityCandidate{ID: "left-1"}, Right: contract.CommunityCandidate{ID: "right-1"}},
	}
	complete := true
	service := &MaintenanceService{deps: MaintenanceDependencies{
		DecisionSettings: testDecisionSettings{Effective: domain.DecisionTasks{TopicAssignment: true}},
		Decisions: testDecisions(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
			require.NoError(t, modelprovider.ValidateDecisionRequest(request))
			require.Len(t, request.Groups, 2)
			yes, no := .95, .949
			answers := map[string]modelprovider.DecisionAnswer{
				"equivalent_0": {Type: modelprovider.DecisionPredicate, Probability: &yes},
			}
			if complete {
				answers["equivalent_1"] = modelprovider.DecisionAnswer{Type: modelprovider.DecisionPredicate, Probability: &no}
			}
			return modelprovider.DecisionResult{Answers: answers}, nil
		}),
	}}
	claim := contract.MaintenanceClaim{ID: uuid.NewString()}
	accepted, probabilities, failures, err := service.assessTopicPairs(t.Context(), claim, pairs, domain.TopicMaintenanceConfig{}, nil)
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, accepted)
	require.Equal(t, []float64{.95, .949}, probabilities)
	require.NoError(t, failures[0])
	require.NoError(t, failures[1])

	complete = false
	accepted, probabilities, failures, err = service.assessTopicPairs(t.Context(), claim, pairs, domain.TopicMaintenanceConfig{}, nil)
	require.Error(t, err)
	require.Nil(t, accepted)
	require.Nil(t, probabilities)
	require.Nil(t, failures)
}

func TestTopicConsolidationGenerationIsolatesInvalidResponses(t *testing.T) {
	pairs := []contract.TopicConsolidationPair{
		{Left: contract.CommunityCandidate{ID: "valid"}, Right: contract.CommunityCandidate{ID: "right-0"}},
		{Left: contract.CommunityCandidate{ID: "invalid"}, Right: contract.CommunityCandidate{ID: "right-1"}},
	}
	var invalidCalls atomic.Int32
	transport := testTransport(func(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		if strings.Contains(request.Messages[1].Content, `"invalid"`) {
			invalidCalls.Add(1)
			return modelprovider.StructuredResult{Content: `{"equivalent":null}`}, nil
		}
		return modelprovider.StructuredResult{Content: `{"equivalent":true}`}, nil
	})
	service := &MaintenanceService{deps: MaintenanceDependencies{Placement: CommunityAssessor{
		Transport: transport, Limits: assessor.DefaultSemanticAssessmentLimits(),
	}}}
	accepted, _, failures, err := service.assessTopicPairs(t.Context(), contract.MaintenanceClaim{ID: uuid.NewString()}, pairs,
		domain.TopicMaintenanceConfig{Model: "fixture"}, nil)
	require.NoError(t, err)
	require.Equal(t, []bool{true, false}, accepted)
	require.NoError(t, failures[0])
	require.Error(t, failures[1])
	require.EqualValues(t, modelprovider.StructuredResponseLimit, invalidCalls.Load())
}

func TestMaintenanceAssignmentBatchesDecisionsAndIsolatesNamingFailure(t *testing.T) {
	target := assignment.Topic{ID: uuid.NewString(), Version: 1, Label: "Databases"}
	var decisionCalls atomic.Int32
	decisions := testDecisions(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		decisionCalls.Add(1)
		require.Len(t, request.Groups, 20)
		require.NoError(t, modelprovider.ValidateDecisionRequest(request))
		answers := make(map[string]modelprovider.DecisionAnswer)
		for i, group := range request.Groups {
			for _, question := range group.Questions {
				probability := 0.90
				if strings.HasSuffix(question.Name, "uncovered") && i != 3 {
					probability = 0.10
				}
				answers[question.Name] = modelprovider.DecisionAnswer{Type: modelprovider.DecisionPredicate, Probability: &probability}
			}
		}
		return modelprovider.DecisionResult{Answers: answers}, nil
	})
	var generations atomic.Int32
	generate := testTransport(func(_ context.Context, _ modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		generations.Add(1)
		return modelprovider.StructuredResult{Content: `{}`}, nil
	})
	service := NewAssignmentService(testCatalog{target}, testConfig(true), generate, decisions, assessor.DefaultSemanticAssessmentLimits(), "fixture", nil)
	scope := assignment.Scope{TeamID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1, OperationID: uuid.NewString()}
	inputs := make([]assignment.Slice, 20)
	for i := range inputs {
		inputs[i] = assignment.Slice{Index: i, EvidenceID: uuid.NewString(), Text: fmt.Sprintf("Evidence %d", i)}
	}
	settings := &domain.DecisionRuntimeConfig{Effective: domain.DecisionTasks{TopicAssignment: true}}
	window := domain.TopicMaintenanceConfig{Enabled: true, Model: "fixture", ContextWindowTokens: 500000}
	outcomes, err := service.PrepareMaintenance(t.Context(), scope, inputs, settings, window, nil)
	require.NoError(t, err)
	require.EqualValues(t, 1, decisionCalls.Load())
	require.Greater(t, generations.Load(), int32(0))
	for i, outcome := range outcomes {
		if i == 3 {
			require.Error(t, outcome.Failure)
			continue
		}
		require.NoError(t, outcome.Failure)
		require.Equal(t, []assignment.Match{{ID: target.ID, Version: 1}}, outcome.Matches)
	}
}

func TestRememberAssignmentBatchesDecisionsAndFailsAtomically(t *testing.T) {
	target := assignment.Topic{ID: uuid.NewString(), Version: 1, Label: "Databases"}
	var sizes []int
	decisions := testDecisions(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		if err := modelprovider.ValidateDecisionRequest(request); err != nil {
			return modelprovider.DecisionResult{}, err
		}
		sizes = append(sizes, len(request.Groups))
		answers := make(map[string]modelprovider.DecisionAnswer)
		for _, group := range request.Groups {
			input := group.Input.(assignmentRequest)
			for _, question := range group.Questions {
				probability := 0.9
				if strings.HasSuffix(question.Name, "uncovered") && input.Text != "Evidence 3" {
					probability = 0.1
				}
				answers[question.Name] = modelprovider.DecisionAnswer{Type: modelprovider.DecisionPredicate, Probability: &probability}
			}
		}
		return modelprovider.DecisionResult{Answers: answers}, nil
	})
	var invalid atomic.Bool
	var calls atomic.Int32
	generate := testTransport(func(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		calls.Add(1)
		if invalid.Load() {
			return modelprovider.StructuredResult{Content: `{}`}, nil
		}
		var input assignmentRequest
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
			return modelprovider.StructuredResult{}, err
		}
		body, err := json.Marshal(modelAnswer{RequestID: input.RequestID, Status: "assigned", Matches: []modelMatch{},
			Proposals: []assignment.Proposal{{Label: "Evidence notes", Description: "Recorded notes", Quote: input.Text}}})
		return modelprovider.StructuredResult{Content: string(body)}, err
	})
	service := NewAssignmentService(testCatalog{target}, testConfig(true), generate, decisions, assessor.DefaultSemanticAssessmentLimits(), "fixture", nil)
	scope := assignment.Scope{TeamID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1, OperationID: uuid.NewString()}
	inputs := make([]assignment.Slice, 21)
	for i := range inputs {
		inputs[i] = assignment.Slice{Index: i, EvidenceID: uuid.NewString(), Text: fmt.Sprintf("Evidence %d", i)}
	}
	settings := &domain.DecisionRuntimeConfig{Effective: domain.DecisionTasks{TopicAssignment: true}}
	outcomes, err := service.Prepare(t.Context(), scope, inputs, settings)
	require.NoError(t, err)
	require.Equal(t, []int{20, 1}, sizes)
	require.EqualValues(t, 1, calls.Load())
	require.Len(t, outcomes, len(inputs))
	for i, outcome := range outcomes {
		require.Equal(t, i, outcome.Index)
		require.Equal(t, []assignment.Match{{ID: target.ID, Version: 1}}, outcome.Matches)
	}
	require.Len(t, outcomes[3].Proposals, 1)
	invalid.Store(true)
	outcomes, err = service.Prepare(t.Context(), scope, inputs, settings)
	require.Error(t, err)
	require.Nil(t, outcomes)
}

func TestAssignmentWithoutCandidatesStillUsesDecisionGate(t *testing.T) {
	var decisions, generations int
	provider := testDecisions(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		decisions++
		require.Len(t, request.Groups[0].Questions, 1)
		probability := 0.1
		return modelprovider.DecisionResult{Answers: map[string]modelprovider.DecisionAnswer{
			"uncovered": {Type: modelprovider.DecisionPredicate, Probability: &probability},
		}}, nil
	})
	generate := testTransport(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		generations++
		return modelprovider.StructuredResult{}, errors.New("unexpected generation")
	})
	service := NewAssignmentService(testCatalog{}, testConfig(true), generate, provider, assessor.DefaultSemanticAssessmentLimits(), "fixture", nil)
	scope := assignment.Scope{TeamID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1, OperationID: uuid.NewString()}
	settings := &domain.DecisionRuntimeConfig{Effective: domain.DecisionTasks{TopicAssignment: true}}
	outcomes, err := service.Prepare(t.Context(), scope, []assignment.Slice{{Text: "Evidence"}}, settings)
	require.NoError(t, err)
	require.Len(t, outcomes, 1)
	require.Empty(t, outcomes[0].Matches)
	require.Equal(t, 1, decisions)
	require.Zero(t, generations)
}
