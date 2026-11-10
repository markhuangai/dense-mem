package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/stretchr/testify/require"
)

func TestOntologyContextAndRepairWitnessesUseNativeHTTPHistory(t *testing.T) {
	for _, mode := range []string{"missing_decision_exhausted", "unused_definition_exhausted", "repair_succeeds", "present_context", "empty_lock", "legitimate_ambiguity"} {
		t.Run(mode, func(t *testing.T) {
			topicRef := "d19"
			if mode == "empty_lock" {
				topicRef = "d0"
			}
			request := assessment.Request{RequestID: "reconstructed-" + mode, Definitions: []assessment.Definition{{Ref: topicRef, Kind: contract.Topic,
				Key: "storage", Label: "Storage", Description: "A topic for exact Evidence about storage", Aliases: []string{}}}, Pairs: []assessment.Pair{}}
			for index := range 20 {
				request.Items = append(request.Items, assessment.Item{Ref: fmt.Sprintf("s%d", index), Kind: contract.EvidenceSource,
					Text: "Frozen reconstructed source.", OwnerRef: "owner", Context: map[string]string{}})
			}
			request.Items[0].LockedDefinitionRef = topicRef
			request.Items[19].Context["source"] = "original exact document"
			var calls int
			var mu sync.Mutex
			var previous []string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				calls++
				var envelope struct{ Messages []modelprovider.Message }
				require.NoError(t, json.NewDecoder(r.Body).Decode(&envelope))
				require.Len(t, envelope.Messages, 2+2*(calls-1))
				require.Contains(t, envelope.Messages[0].Content, "supplied owner and provenance context")
				require.Contains(t, envelope.Messages[0].Content, "Copy nonempty locked_definition_ref")
				var immutable assessment.Request
				require.NoError(t, json.Unmarshal([]byte(envelope.Messages[1].Content), &immutable))
				require.Equal(t, request, immutable)
				for index, response := range previous {
					require.Equal(t, "assistant", envelope.Messages[2+2*index].Role)
					require.Equal(t, response, envelope.Messages[2+2*index].Content)
				}
				if calls > 1 {
					var feedback struct {
						ValidationErrors []assessor.SemanticValidationError `json:"validation_errors"`
						Instruction      string                             `json:"instruction"`
					}
					require.NoError(t, json.Unmarshal([]byte(envelope.Messages[len(envelope.Messages)-1].Content), &feedback))
					require.NotEmpty(t, feedback.ValidationErrors)
					require.Contains(t, feedback.Instruction, "complete corrected object")
					encoded, err := json.Marshal(feedback.ValidationErrors)
					require.NoError(t, err)
					if mode == "unused_definition_exhausted" {
						require.Contains(t, string(encoded), "has no source assignment")
					} else {
						require.Contains(t, string(encoded), "missing decision")
					}
				}
				response := assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{}, Items: []assessment.Decision{}, Equivalence: []assessment.Equivalence{}}
				for _, item := range request.Items {
					response.Items = append(response.Items, assessment.Decision{Ref: item.Ref, Status: "classified", DefinitionRef: topicRef})
				}
				if mode == "legitimate_ambiguity" {
					response.Items[19] = assessment.Decision{Ref: "s19", Status: "ambiguous", Reason: "The supplied meaning is insufficient."}
				} else if mode == "missing_decision_exhausted" || mode == "repair_succeeds" && calls < 3 {
					response.Items = response.Items[:19]
				}
				if mode == "empty_lock" {
					response.Items[8] = assessment.Decision{Ref: "s8", Status: "ambiguous", Reason: "The evidence does not establish a suitable topic."}
				}
				if mode == "unused_definition_exhausted" {
					response.Definitions = append(response.Definitions, assessment.Definition{Ref: fmt.Sprintf("unused%d", calls), Kind: contract.Topic,
						Key: fmt.Sprintf("unused_topic_%d", calls), Label: "Unused proposal", Description: "A fresh unused proposal on each repair turn.", Aliases: []string{}})
				}
				encoded, err := json.Marshal(response)
				require.NoError(t, err)
				previous = append(previous, string(encoded))
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(encoded)}}}}))
			}))
			defer upstream.Close()
			cfg := &config.Config{AIVerifierAPIURL: upstream.URL, AIVerifierAPIKey: "synthetic-provider-key", AIVerifierModel: "fixture-model"}
			provider := assessment.NewProvider(assessorprovider.NewOpenAIAssessor(cfg, upstream.Client()), cfg.AIVerifierModel, assessor.DefaultSemanticAssessmentLimits(), 300000)
			response, attempts, err := provider.Assess(context.Background(), request)
			mu.Lock()
			observedCalls := calls
			mu.Unlock()
			if strings.HasSuffix(mode, "exhausted") {
				require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
				require.Equal(t, 3, observedCalls)
				require.Len(t, attempts, 3)
				require.Empty(t, response.Items)
				code := "item_count_mismatch"
				if mode == "unused_definition_exhausted" {
					code = "definition_unused"
				}
				require.Equal(t, code, attempts[2].Validation.Code)
				return
			}
			require.NoError(t, err)
			require.Len(t, response.Items, 20)
			if mode == "repair_succeeds" {
				require.Equal(t, 3, observedCalls)
			} else {
				require.Equal(t, 1, observedCalls)
			}
			if mode == "legitimate_ambiguity" {
				require.Equal(t, "ambiguous", response.Items[19].Status)
			} else {
				require.Equal(t, topicRef, response.Items[19].DefinitionRef)
			}
			if mode == "empty_lock" {
				require.Equal(t, "ambiguous", response.Items[8].Status)
			} else {
				require.Equal(t, topicRef, response.Items[8].DefinitionRef)
			}
		})
	}
}
