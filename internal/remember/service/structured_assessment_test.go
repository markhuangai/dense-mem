package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/provider/structured"
	assessmentfixture "github.com/markhuangai/dense-mem/internal/testfixture/assessment"
	"github.com/stretchr/testify/require"
)

func TestStructuredRememberHTTPRepairsOnlyCompleteResponses(t *testing.T) {
	for _, exhaust := range []bool{false, true} {
		name := "recovers"
		if exhaust {
			name = "exhausts"
		}
		t.Run(name, func(t *testing.T) {
			fixture := synchronousAssessmentFixture(t)
			var outbound []string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var raw struct {
					Messages       []modelprovider.Message `json:"messages"`
					ResponseFormat struct {
						JSONSchema struct {
							Schema map[string]any `json:"schema"`
						} `json:"json_schema"`
					} `json:"response_format"`
				}
				if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
					http.Error(w, "invalid fixture request", 400)
					return
				}
				call := modelprovider.StructuredRequest{Messages: raw.Messages, Schema: raw.ResponseFormat.JSONSchema.Schema}
				request, err := assessmentfixture.Request(call)
				if err != nil {
					http.Error(w, "invalid assessment request", 400)
					return
				}
				turn := len(outbound)
				reply := validSynchronousAssessmentResponse(request)
				encoded, err := assessmentfixture.Result(call, reply)
				if err != nil {
					http.Error(w, "fixture could not serialize response", 500)
					return
				}
				body := encoded.Content
				if turn == 0 {
					body = `{"provider_turns":100,` + body[1:]
				} else if turn == 1 || exhaust {
					reply.RequestID = "outside-the-immutable-request"
					encoded, err = assessmentfixture.Result(call, reply)
					if err != nil {
						http.Error(w, "fixture could not serialize response", 500)
						return
					}
					body = encoded.Content
				}
				require.Len(t, raw.Messages, 2+2*turn)
				for index, previous := range outbound {
					require.Equal(t, previous, raw.Messages[2+index*2].Content)
				}
				if turn > 0 {
					var feedback struct {
						Issues []jsonstrict.Issue `json:"validation_errors"`
					}
					require.NoError(t, json.Unmarshal([]byte(raw.Messages[2*turn+1].Content), &feedback))
					require.NotEmpty(t, feedback.Issues)
					if turn == 1 {
						require.Contains(t, feedback.Issues, jsonstrict.Issue{Field: "provider_turns", Message: "is unknown"})
					}
				}
				outbound = append(outbound, body)
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": body}}}})
			})
			server := httptest.NewServer(handler)
			defer server.Close()
			fixture.deps.Provider = structured.NewOpenAI(&config.Config{AIVerifierAPIURL: server.URL, AIVerifierModel: "fixture"}, server.Client())
			result, err := AssessSynchronousRemember(context.Background(), fixture.deps, fixture.input)
			require.Len(t, outbound, 3)
			if exhaust {
				require.ErrorIs(t, err, ErrRememberProviderResponseInvalid)
				require.Equal(t, 3, SynchronousAssessmentProviderTurns(err))
				require.Nil(t, result)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 3, result.Response.ProviderTurns)
			require.Equal(t, result.Request.RequestID, result.Response.RequestID)
		})
	}
}

func TestStructuredRememberRepairsKeepAuthenticatedEnvelopeAndAllowlist(t *testing.T) {
	fixture := synchronousAssessmentFixture(t)
	fixture.provider.response = func(request assessor.SemanticAssessmentRequest, _ int) assessor.SemanticAssessmentResponse {
		reply := validSynchronousAssessmentResponse(request)
		reply.RequestID = "requires-repair"
		return reply
	}
	engine := newSynchronousAssessmentEngine(fixture.deps, fixture.input.Scope.TeamID, fixture.input.Scope.OwnerProfileID).assessmentEngine
	plan, err := buildSubmissionAssessmentPlan(fixture.input.Snapshot)
	require.NoError(t, err)
	request, err := engine.buildRequest(t.Context(), fixture.input.Scope, plan)
	require.NoError(t, err)
	_, err = engine.assessRememberSession(t.Context(), request, func(context.Context) (assessor.SemanticAssessmentRequest, error) {
		modified := request
		modified.OwnerProfileID = "different-owner"
		return modified, nil
	}, 0, nil)
	require.ErrorContains(t, err, "immutable assessment request")
	require.Equal(t, 1, fixture.provider.calls)
	require.Zero(t, fixture.provider.repairCalls)
}
