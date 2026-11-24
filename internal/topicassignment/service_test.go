package topicassignment

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/topicassignment/contract"
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

type testCatalog []Topic

func (c testCatalog) ReadAssignmentCandidates(context.Context, Scope, Slice) (contract.Candidates, error) {
	return contract.Candidates{Topics: c}, nil
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
	scope := Scope{TeamID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1, OperationID: uuid.NewString()}
	slices := []Slice{{Text: "Practice database recovery."}}
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
				body, err := json.Marshal(modelAnswer{RequestID: input.RequestID, Status: "uncategorized", Matches: []modelMatch{}, Proposals: []contract.Proposal{}})
				return modelprovider.StructuredResult{Content: string(body)}, err
			})
			decisionCalls := 0
			decide := testDecisions(func(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
				decisionCalls++
				require.NoError(t, modelprovider.ValidateDecisionRequest(request))
				uncertain := 0.5
				return modelprovider.DecisionResult{Answers: map[string]modelprovider.DecisionAnswer{
					"match_0":   {Type: modelprovider.DecisionPredicate, Probability: &uncertain},
					"uncovered": {Type: modelprovider.DecisionPredicate, Probability: &uncertain},
				}}, nil
			})
			settings := &domain.DecisionRuntimeConfig{Effective: domain.DecisionTasks{TopicAssignment: useDecisions}}
			service := NewService(testCatalog{{ID: uuid.NewString(), Version: 1, Label: "Databases"}}, live, generate, decide, limits, "default", nil)
			_, err := service.PrepareMaintenance(ctx, scope, slices, settings, window)
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
			outcomes, err := service.PrepareMaintenance(ctx, scope, slices, settings, window)
			require.NoError(t, err)
			require.Empty(t, outcomes)
			require.Len(t, requests, 2)
		})
	}
}

func TestAssignmentUncoveredSubjectAndPriorInputs(t *testing.T) {
	topic := Topic{ID: uuid.NewString(), Version: 3, Label: "Postgres"}
	scope := Scope{TeamID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1, OperationID: uuid.NewString()}
	source := Slice{Text: "Replicate Postgres across regions for disaster recovery."}
	genCalls, decisionCalls := 0, 0
	gen := testTransport(func(_ context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		genCalls++
		var input assignmentRequest
		require.NoError(t, json.Unmarshal([]byte(req.Messages[1].Content), &input))
		output := map[string]any{"request_id": input.RequestID, "status": "assigned", "matches": []map[string]string{{"id": topic.ID, "quote": "Postgres"}}, "proposals": []map[string]string{{"label": "Disaster recovery", "description": "Restoring service after failure.", "quote": "disaster recovery"}}}
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
	s := NewService(testCatalog{topic}, testConfig(true), gen, decisions, assessor.DefaultSemanticAssessmentLimits(), "fixture", nil)
	result, err := s.Prepare(context.Background(), scope, []Slice{source}, settings)
	require.NoError(t, err)
	require.Equal(t, "decision", result[0].Method)
	require.Len(t, result[0].Matches, 1)
	require.Zero(t, genCalls)
	source.PriorInputHash = result[0].InputHash
	repeated, err := s.Prepare(context.Background(), scope, []Slice{source}, settings)
	require.NoError(t, err)
	require.True(t, repeated[0].Unchanged)
	require.Equal(t, 1, decisionCalls)
	source.PriorInputHash = ""
	uncovered = 0.999
	result, err = s.Prepare(context.Background(), scope, []Slice{source}, settings)
	require.NoError(t, err)
	require.Len(t, result[0].Matches, 1)
	require.Len(t, result[0].Proposals, 1)
	require.Equal(t, "generation", result[0].Method)
	require.Equal(t, 1, genCalls)
	settings.Effective.TopicAssignment = false
	_, err = s.Prepare(context.Background(), scope, []Slice{source}, settings)
	require.NoError(t, err)
	require.Equal(t, 2, genCalls)
	require.Equal(t, 2, decisionCalls)
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
	s := NewService(testCatalog{{ID: uuid.NewString(), Version: 1, Label: "Databases"}}, testConfig(true), nil, testDecisions(func(context.Context, modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
		return modelprovider.DecisionResult{}, cause
	}), assessor.DefaultSemanticAssessmentLimits(), "fixture", nil)
	ctx := context.Background()
	scope := Scope{TeamID: uuid.NewString(), SpaceID: uuid.NewString(), Generation: 1, OperationID: uuid.NewString()}
	slices := []Slice{{Text: "Database restore drill"}}
	settings := &domain.DecisionRuntimeConfig{Effective: domain.DecisionTasks{TopicAssignment: true}}
	out, err := s.Prepare(ctx, scope, slices, settings)
	require.ErrorIs(t, err, cause)
	require.Empty(t, out)
	s.config = testConfig(false)
	out, err = s.Prepare(ctx, scope, slices, settings)
	require.NoError(t, err)
	require.Empty(t, out)
}
