package assessment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

type fixtureTransport func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error)

func TestOrganizationExplicitContextWindow(t *testing.T) {
	request, response := assessmentFixture()
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 512
	limits.MaxInputTokens = 16000
	valid, err := json.Marshal(response)
	require.NoError(t, err)
	calls := 0
	transport := fixtureTransport(func(_ context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		calls++
		input, err := NewProvider(nil, "model", limits, 300000).messageTokens(req.Messages, req.Schema)
		require.NoError(t, err)
		require.LessOrEqual(t, input+req.MaxOutputTokens, req.MaxInputTokens+req.MaxOutputTokens)
		return modelprovider.StructuredResult{Content: string(valid)}, nil
	})
	probe := NewProvider(transport, "model", limits, 300000)
	measured, err := probe.Measure(request)
	require.NoError(t, err)
	headroom := limits.MaxInputTokens - assessor.SemanticAssessmentConversationInputLimit(limits)
	capacity := measured + headroom + limits.MaxOutputTokens
	for _, contextWindow := range []int{0, -1, 1, limits.MaxOutputTokens, 1000000001} {
		provider := NewProvider(transport, "model", limits, contextWindow)
		_, attempts, err := provider.Assess(context.Background(), request)
		require.ErrorIs(t, err, ontology.ErrConfiguration)
		require.Empty(t, attempts)
	}
	require.Zero(t, calls)
	provider := NewProvider(transport, "model", limits, capacity)
	require.NoError(t, provider.ConfigurationError())
	require.Equal(t, measured, provider.MaxInitialInputTokens())
	_, attempts, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, attempts, 1)
	require.Equal(t, 1, calls)
	provider = NewProvider(transport, "model", limits, capacity-1)
	_, attempts, err = provider.Assess(context.Background(), request)
	require.Error(t, err)
	require.Empty(t, attempts)
	require.Equal(t, 1, calls)
	require.NotEqual(t, probe.Identity(), provider.Identity())
	inputOnly, err := assessor.CountTokens(request.Items[0].Text+request.Items[1].Text, limits.Tokenizer)
	require.NoError(t, err)
	require.Greater(t, measured, inputOnly, "complete schema and message framing must be counted")
}

func TestOrganizationRepairContractInvalidatesPriorIdentity(t *testing.T) {
	limits := assessor.DefaultSemanticAssessmentLimits()
	provider := NewProvider(nil, "model", limits, 300000)
	require.NoError(t, provider.ConfigurationError())
	// These identities freeze the string and repeated-context feedback contracts with identical model and limits.
	for _, priorIdentity := range []string{
		"sha256:b929583c6d97c7cefdc6c23ac69de1644408b970dfbddb658ee1f947be5403c5",
		"sha256:7a1a311acdce46400d7df74166abb914ad9b51328954997d621274a343ef7b5c",
	} {
		require.NotEqual(t, priorIdentity, provider.Identity())
	}
	require.Equal(t, provider.Identity(), NewProvider(nil, "model", limits, 300000).Identity())
}

func (f fixtureTransport) Complete(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return f(ctx, request)
}

func TestOrganizationValidResponseOutputLimits(t *testing.T) {
	request, response := assessmentFixture()
	require.NoError(t, Validate(request, response))
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	limits := assessor.DefaultSemanticAssessmentLimits()
	measured, err := assessor.CountTokens(string(encoded), limits.Tokenizer)
	require.NoError(t, err)
	for _, testCase := range []struct {
		name     string
		maximum  int
		reported int
		fail     bool
	}{
		{"exact_fit", measured, measured, false},
		{"measured_overflow", measured - 1, 0, true},
		{"reported_overflow", measured, measured + 1, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			limits.MaxOutputTokens = testCase.maximum
			calls := 0
			provider := NewProvider(fixtureTransport(func(_ context.Context, outbound modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
				calls++
				require.Equal(t, testCase.maximum, outbound.MaxOutputTokens)
				return modelprovider.StructuredResult{Content: string(encoded), CompletionTokens: testCase.reported}, nil
			}), "model", limits, 300000)
			require.NoError(t, provider.ConfigurationError())
			result, attempts, err := provider.Assess(context.Background(), request)
			if testCase.fail {
				require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
				require.Empty(t, result.Items)
				require.Len(t, attempts, 3)
				require.Equal(t, 3, calls)
				for _, attempt := range attempts {
					require.Equal(t, "response_invalid", attempt.FailureCode)
				}
			} else {
				require.NoError(t, err)
				require.Equal(t, response, result)
				require.Len(t, attempts, 1)
				require.Equal(t, 1, calls)
			}
		})
	}
}

func TestOrganizationContextWindowIncludesRegenerationHistory(t *testing.T) {
	request, response := assessmentFixture()
	valid, err := json.Marshal(response)
	require.NoError(t, err)
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 1024
	measurement, err := NewProvider(nil, "model", limits, 300000).Measure(request)
	require.NoError(t, err)
	headroom := limits.MaxInputTokens - assessor.SemanticAssessmentConversationInputLimit(limits)
	capacity := measurement + headroom + limits.MaxOutputTokens
	for _, overflow := range []bool{false, true} {
		calls := 0
		provider := NewProvider(fixtureTransport(func(_ context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
			calls++
			tokens, err := NewProvider(nil, "model", limits, 300000).messageTokens(req.Messages, req.Schema)
			require.NoError(t, err)
			require.LessOrEqual(t, tokens+req.MaxOutputTokens, capacity)
			if overflow {
				return modelprovider.StructuredResult{Content: strings.Repeat("invalid ", capacity)}, nil
			}
			if calls < 3 {
				return modelprovider.StructuredResult{Content: "{}"}, nil
			}
			return modelprovider.StructuredResult{Content: string(valid)}, nil
		}), "model", limits, capacity)
		result, attempts, err := provider.Assess(context.Background(), request)
		if overflow {
			require.Error(t, err)
			require.Equal(t, 1, calls, "oversized regeneration history must stop before a second dispatch")
			require.Len(t, attempts, 1)
			require.Empty(t, result.Items)
		} else {
			require.NoError(t, err)
			require.Equal(t, response, result)
			require.Equal(t, 3, calls)
			require.Len(t, attempts, 3)
			require.Greater(t, attempts[2].EstimatedInputTokens, attempts[0].EstimatedInputTokens)
		}
	}
}

func assessmentFixture() (Request, Response) {
	request := Request{RequestID: "request", Items: []Item{{Ref: "a", Kind: ontology.EvidenceSource, Text: "Atlas stores data in PostgreSQL."}, {Ref: "b", Kind: ontology.EvidenceSource, Text: "PostgreSQL is Atlas's data store."}}, Definitions: []Definition{}, Pairs: []Pair{{Ref: "pair", LeftRef: "a", RightRef: "b"}}}
	response := Response{RequestID: "request", Definitions: []Definition{{Ref: "new-topic", Kind: ontology.Topic, Key: "storage", Label: "Storage", Description: "Data storage", Aliases: []string{}}}, Items: []Decision{{Ref: "a", Status: "classified", DefinitionRef: "new-topic"}, {Ref: "b", Status: "classified", DefinitionRef: "new-topic"}}, Equivalence: []Equivalence{{Ref: "pair", Relation: "equivalent"}}}
	return request, response
}

func TestOrganizationResponseRequiresCompleteClosedAllowlists(t *testing.T) {
	request, response := assessmentFixture()
	require.NoError(t, Validate(request, response))
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	for name, raw := range map[string]string{"unknown": strings.Replace(string(encoded), "{", `{"team_id":"chosen",`, 1), "duplicate": strings.Replace(string(encoded), "{", `{"request_id":"first",`, 1), "trailing": string(encoded) + " {}", "null": "null", "missing": `{"request_id":"request","items":[],"equivalence":[]}`, "missing_nested": strings.Replace(string(encoded), `"parent_ref":"",`, "", 1)} {
		t.Run(name, func(t *testing.T) { _, err := Decode(raw); require.Error(t, err) })
	}
	for _, change := range []func(*Response){
		func(r *Response) { r.Items = r.Items[:1] }, func(r *Response) { r.Items[1].Ref = "a" }, func(r *Response) { r.Items[0].DefinitionRef = "unknown" }, func(r *Response) { r.Equivalence = nil }, func(r *Response) { r.Equivalence = append(r.Equivalence, r.Equivalence[0]) }, func(r *Response) { r.Equivalence[0].Relation = "similar" }, func(r *Response) { r.Definitions[0].Kind = ontology.EntityClass }, func(r *Response) { r.Definitions[0].ParentRef = "outside" },
	} {
		_, fresh := assessmentFixture()
		change(&fresh)
		require.Error(t, Validate(request, fresh))
	}
	request.Pairs[0].RequiredRelation = "distinct"
	require.Error(t, Validate(request, response))
	request.Pairs[0].RequiredRelation = ""
	request.Items[0].LockedDefinitionRef = "pinned"
	require.Error(t, Validate(request, response))
}

func TestOrganizationEquivalenceRejectsInconsistentChains(t *testing.T) {
	request, response := assessmentFixture()
	request.Items = append(request.Items, Item{Ref: "c", Kind: ontology.EvidenceSource})
	request.Pairs = append(request.Pairs, Pair{Ref: "bc", LeftRef: "b", RightRef: "c"}, Pair{Ref: "ac", LeftRef: "a", RightRef: "c"})
	response.Items = append(response.Items, Decision{Ref: "c", Status: "classified", DefinitionRef: "new-topic"})
	response.Equivalence = append(response.Equivalence, Equivalence{Ref: "bc", Relation: "equivalent"}, Equivalence{Ref: "ac", Relation: "distinct"})
	err := Validate(request, response)
	require.ErrorContains(t, err, "not transitive")
	requireValidation(t, err, "validate", "equivalence_transitivity", "equivalence")
}

func TestOrganizationSuppliedDefinitionsAreReusedWithoutEchoes(t *testing.T) {
	request, response := assessmentFixture()
	supplied := response.Definitions[0]
	supplied.Ref = "d0"
	request.Definitions = []Definition{supplied}
	response.Definitions = []Definition{}
	for i := range response.Items {
		response.Items[i].DefinitionRef = supplied.Ref
	}
	require.NoError(t, Validate(request, response))
	response.Definitions = []Definition{supplied}
	require.ErrorContains(t, Validate(request, response), `definitions[0].ref: response.definitions is only for new definitions; omit supplied ref "d0" and reuse it in items[].definition_ref`)
	response.Definitions[0].Ref = ""
	requireValidation(t, Validate(request, response), "validate", "definition_ref_invalid", "definitions")
	response.Definitions[0].Ref = "d1"
	response.Definitions = append(response.Definitions, response.Definitions[0])
	requireValidation(t, Validate(request, response), "validate", "definition_ref_duplicate", "definitions")
}

func TestOrganizationEchoCorrectionRegeneratesCompleteResponses(t *testing.T) {
	request, response := assessmentFixture()
	supplied := response.Definitions[0]
	supplied.Ref = "d0"
	request.Definitions = []Definition{supplied}
	request.Pairs[0].RequiredRelation = "distinct"
	response.Equivalence[0].Relation = "distinct"
	response.Definitions[0].Ref = "d1"
	response.Definitions[0].Key = "postgresql"
	response.Definitions[0].Label = "PostgreSQL"
	response.Items[0].DefinitionRef = "d0"
	response.Items[1].DefinitionRef = "d1"
	echoed := response
	echoed.Definitions = append([]Definition{response.Definitions[0]}, supplied)
	echoError := Validate(request, echoed)
	require.Error(t, echoError)
	incomplete := response
	incomplete.Items = incomplete.Items[:1]
	responses := []Response{echoed, incomplete, response}
	calls := 0
	provider := NewProvider(fixtureTransport(func(_ context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		require.Less(t, calls, len(responses))
		if calls == 1 {
			var correction correctionFeedback
			require.NoError(t, json.Unmarshal([]byte(req.Messages[3].Content), &correction))
			requireCorrectionErrors(t, echoError, correction.ValidationErrors)
			require.Contains(t, correctionMessages(correction), `"d0"`)
			require.NotContains(t, correctionMessages(correction), `"d1"`)
		}
		encoded, err := json.Marshal(responses[calls])
		require.NoError(t, err)
		calls++
		return modelprovider.StructuredResult{Content: string(encoded)}, nil
	}), "model", assessor.DefaultSemanticAssessmentLimits(), 300000)
	result, attempts, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, response, result)
	require.Len(t, attempts, 3)
	require.Equal(t, 3, calls)
	require.Equal(t, "response_invalid", attempts[0].FailureCode)
	require.Equal(t, "response_invalid", attempts[1].FailureCode)
	require.Empty(t, attempts[2].FailureCode)
}

func TestOrganizationCompleteRegenerationAndTokenAccounting(t *testing.T) {
	request, response := assessmentFixture()
	valid, err := json.Marshal(response)
	require.NoError(t, err)
	calls := 0
	transport := fixtureTransport(func(ctx context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		calls++
		require.Equal(t, SchemaName, req.SchemaName)
		require.Equal(t, ResponseSchema(request.RequestID), req.Schema)
		if calls == 1 {
			return modelprovider.StructuredResult{Content: `{"request_id":"request","definitions":[],"items":[],"equivalence":[]}`}, nil
		}
		require.Len(t, req.Messages, 4)
		require.Contains(t, req.Messages[3].Content, "validation_errors")
		return modelprovider.StructuredResult{Content: string(valid), PromptTokens: 500, CompletionTokens: 100}, nil
	})
	provider := NewProvider(transport, "model", assessor.DefaultSemanticAssessmentLimits(), 300000)
	result, attempts, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, response, result)
	require.Len(t, attempts, 2)
	require.Equal(t, 2, calls)
	require.Greater(t, attempts[1].EstimatedInputTokens, attempts[0].EstimatedInputTokens)
	require.False(t, attempts[0].ReportedUsageAvailable)
	require.True(t, attempts[1].ReportedUsageAvailable)
	require.Equal(t, 500, attempts[1].ReportedInputTokens)
	calls = 0
	provider = NewProvider(fixtureTransport(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		calls++
		return modelprovider.StructuredResult{Content: "{}"}, nil
	}), "model", assessor.DefaultSemanticAssessmentLimits(), 300000)
	_, attempts, err = provider.Assess(context.Background(), request)
	require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
	require.Equal(t, 3, calls)
	require.Len(t, attempts, 3)
}

func TestOrganizationRequestIdentitySchemaAndCompleteRepair(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(map[bool]string{false: "exhausted", true: "repaired"}[repair], func(t *testing.T) {
			request, response := assessmentFixture()
			request.RequestID = "1479d3a1-800a-4fc7-a1c7-a007f8d8ce23"
			limits := assessor.DefaultSemanticAssessmentLimits()
			calls := []modelprovider.StructuredRequest{}
			provider := NewProvider(fixtureTransport(func(_ context.Context, outbound modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
				calls = append(calls, outbound)
				properties := outbound.Schema["properties"].(map[string]any)
				require.Equal(t, map[string]any{"type": "string", "enum": []string{request.RequestID}}, properties["request_id"])
				var original Request
				require.NoError(t, json.Unmarshal([]byte(outbound.Messages[1].Content), &original))
				require.Equal(t, request, original)
				if len(calls) > 1 {
					var correction correctionFeedback
					require.NoError(t, json.Unmarshal([]byte(outbound.Messages[len(outbound.Messages)-1].Content), &correction))
					require.Contains(t, correction.Instruction, request.RequestID)
					prior, err := Decode(outbound.Messages[len(outbound.Messages)-2].Content)
					require.NoError(t, err)
					require.Equal(t, "a74b4d9b-e96a-4936-93c8-62fe93405efc", prior.RequestID)
				}
				response.RequestID = "a74b4d9b-e96a-4936-93c8-62fe93405efc"
				if repair && len(calls) == 3 {
					response.RequestID = request.RequestID
				}
				encoded, err := json.Marshal(response)
				require.NoError(t, err)
				return modelprovider.StructuredResult{Content: string(encoded)}, nil
			}), "model", limits, 300000)
			measured, err := provider.Measure(request)
			require.NoError(t, err)
			result, attempts, err := provider.Assess(context.Background(), request)
			require.Len(t, calls, 3)
			require.Len(t, attempts, 3)
			require.Equal(t, measured, attempts[0].EstimatedInputTokens)
			for i, outbound := range calls {
				messages := make([]assessor.SemanticAssessmentProviderMessage, len(outbound.Messages))
				for j, message := range outbound.Messages {
					messages[j] = assessor.SemanticAssessmentProviderMessage{Role: message.Role, Content: message.Content}
				}
				tokens, countErr := assessor.CountSemanticAssessmentProviderRequestTokens(outbound.Model, outbound.SchemaName, outbound.Schema, limits.ProviderTemperatureDisabled, messages, limits.Tokenizer)
				require.NoError(t, countErr)
				require.Equal(t, tokens, attempts[i].EstimatedInputTokens)
				require.Equal(t, calls[0].Schema, outbound.Schema)
				if i < 2 || !repair {
					require.Equal(t, "request_id_mismatch", attempts[i].Validation.Code)
				}
			}
			if repair {
				require.NoError(t, err)
				require.Equal(t, response, result)
			} else {
				require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
				require.Empty(t, result)
			}
		})
	}
}

func TestOrganizationDefinitionKeyValidation(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		valid     bool
	}{
		{"empty", "", false},
		{"whitespace", " \t", false},
		{"nul", "topic\x00", false},
		{"over_limit", strings.Repeat("界", 129), false},
		{"unicode_boundary", strings.Repeat("界", 128), true},
		{"internal_space", "storage systems", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, response := assessmentFixture()
			response.Definitions[0].Key = tc.key
			err := Validate(request, response)
			if tc.valid {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ontology.ErrInvalid)
			require.ErrorContains(t, err, "definition key must be nonblank, at most 128 characters, and contain no NUL")
			requireValidation(t, err, "validate", "definition_record", "definitions")
		})
	}
}

func TestOrganizationEmptyDefinitionKeyCompleteRepair(t *testing.T) {
	for _, repair := range []bool{false, true} {
		t.Run(map[bool]string{false: "exhausted", true: "repaired"}[repair], func(t *testing.T) {
			request, response := assessmentFixture()
			original, err := json.Marshal(request)
			require.NoError(t, err)
			calls := 0
			provider := NewProvider(fixtureTransport(func(_ context.Context, outbound modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
				calls++
				require.Equal(t, string(original), outbound.Messages[1].Content)
				properties := outbound.Schema["properties"].(map[string]any)
				definition := properties["definitions"].(map[string]any)["items"].(map[string]any)
				key := definition["properties"].(map[string]any)["key"]
				require.Equal(t, map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, key)
				if calls > 1 {
					var correction correctionFeedback
					require.NoError(t, json.Unmarshal([]byte(outbound.Messages[len(outbound.Messages)-1].Content), &correction))
					require.Contains(t, correctionMessages(correction), "definition key must be nonblank")
					previous, err := Decode(outbound.Messages[len(outbound.Messages)-2].Content)
					require.NoError(t, err)
					require.Empty(t, previous.Definitions[0].Key)
				}
				response.Definitions[0].Key = ""
				if repair && calls == 3 {
					response.Definitions[0].Key = "storage"
				}
				encoded, err := json.Marshal(response)
				require.NoError(t, err)
				return modelprovider.StructuredResult{Content: string(encoded)}, nil
			}), "model", assessor.DefaultSemanticAssessmentLimits(), 300000)
			result, attempts, err := provider.Assess(context.Background(), request)
			require.Equal(t, 3, calls)
			require.Len(t, attempts, 3)
			for i, attempt := range attempts {
				if repair && i == 2 {
					require.Nil(t, attempt.Validation)
					continue
				}
				require.Equal(t, "definition_record", attempt.Validation.Code)
			}
			if repair {
				require.NoError(t, err)
				require.Equal(t, response, result)
			} else {
				require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
				require.Empty(t, result)
			}
		})
	}
}

func TestOrganizationInitialBudgetAllowsBothCompleteRegenerations(t *testing.T) {
	request, response := assessmentFixture()
	request.Items[0].Text = strings.Repeat("context detail ", 4000)
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 2048
	headroom := limits.MaxInputTokens - assessor.SemanticAssessmentConversationInputLimit(limits)
	measurement, err := NewProvider(nil, "model", limits, 300000).Measure(request)
	require.NoError(t, err)
	limits.MaxInputTokens = measurement + headroom
	valid, err := json.Marshal(response)
	require.NoError(t, err)
	invalid := `{"invalid":"` + strings.Repeat("invalid ", limits.MaxOutputTokens-100) + `"}`
	invalidTokens, err := assessor.CountTokens(invalid, limits.Tokenizer)
	require.NoError(t, err)
	require.LessOrEqual(t, invalidTokens, limits.MaxOutputTokens)
	require.Greater(t, invalidTokens, limits.MaxOutputTokens/2)
	calls := 0
	provider := NewProvider(fixtureTransport(func(_ context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		calls++
		require.Len(t, req.Messages, 2*calls)
		var original Request
		require.NoError(t, json.Unmarshal([]byte(req.Messages[1].Content), &original))
		require.Equal(t, request.Items[0].Text, original.Items[0].Text)
		content := invalid
		if calls == 3 {
			content = string(valid)
		}
		return modelprovider.StructuredResult{Content: content}, nil
	}), "model", limits, 300000)
	require.Equal(t, measurement, provider.MaxInitialInputTokens())
	result, attempts, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, response, result)
	require.Equal(t, 3, calls)
	require.Len(t, attempts, 3)
	for _, attempt := range attempts {
		require.LessOrEqual(t, attempt.EstimatedInputTokens, limits.MaxInputTokens)
	}
	require.Equal(t, "response_invalid", attempts[0].FailureCode)
	require.Equal(t, "response_invalid", attempts[1].FailureCode)
	require.Empty(t, attempts[2].FailureCode)
	request.Items[0].Text += strings.Repeat("excess ", 100)
	_, attempts, err = provider.Assess(context.Background(), request)
	require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
	require.Empty(t, attempts)
	require.Equal(t, 3, calls)
}

func TestOrganizationLargeDiagnosticsCannotOverflowRepairContext(t *testing.T) {
	request, _ := assessmentFixture()
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 16384
	headroom := limits.MaxInputTokens - assessor.SemanticAssessmentConversationInputLimit(limits)
	measurement, err := NewProvider(nil, "model", limits, 300000).Measure(request)
	require.NoError(t, err)
	limits.MaxInputTokens = measurement + headroom
	invalid := `{"雪` + strings.Repeat("invalid ", limits.MaxOutputTokens-100) + `":true}`
	invalidTokens, err := assessor.CountTokens(invalid, limits.Tokenizer)
	require.NoError(t, err)
	require.LessOrEqual(t, invalidTokens, limits.MaxOutputTokens)
	_, decodeErr := Decode(invalid)
	var rejected *assessor.SemanticAssessmentResponseValidationError
	require.ErrorAs(t, decodeErr, &rejected)
	require.Contains(t, decodeErr.Error(), "is unknown")
	require.Contains(t, decodeErr.Error(), "雪")
	calls := 0
	provider := NewProvider(fixtureTransport(func(_ context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		calls++
		require.LessOrEqual(t, len(req.Messages), 4)
		return modelprovider.StructuredResult{Content: invalid}, nil
	}), "model", limits, 300000)
	result, attempts, err := provider.Assess(context.Background(), request)
	require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
	require.ErrorContains(t, err, "input exceeds token budget")
	require.Empty(t, result)
	require.Len(t, attempts, 2)
	require.Equal(t, 2, calls)
	require.Equal(t, "response_invalid", attempts[0].FailureCode)
}

func TestOrganizationProviderFailuresBudgetsAndCancellation(t *testing.T) {
	request, _ := assessmentFixture()
	limits := assessor.DefaultSemanticAssessmentLimits()
	calls := 0
	transport := fixtureTransport(func(ctx context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		calls++
		return modelprovider.StructuredResult{}, &modelprovider.ProviderError{Provider: "fixture", Message: "failed"}
	})
	provider := NewProvider(transport, "model", limits, 300000)
	_, attempts, err := provider.Assess(context.Background(), request)
	require.ErrorIs(t, err, modelprovider.ErrVerifierProvider)
	require.Len(t, attempts, 1)
	require.Equal(t, 1, calls)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = provider.Assess(cancelled, request)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, calls)
	limits.MaxInputTokens = 1
	provider = NewProvider(transport, "model", limits, 300000)
	_, _, err = provider.Assess(context.Background(), request)
	require.ErrorIs(t, err, ontology.ErrConfiguration)
	require.Equal(t, 1, calls)
	limits = assessor.DefaultSemanticAssessmentLimits()
	limits.Tokenizer = "unknown"
	provider = NewProvider(transport, "model", limits, 300000)
	_, _, err = provider.Assess(context.Background(), request)
	require.Error(t, err)
	require.Equal(t, 1, calls)
	limits = assessor.DefaultSemanticAssessmentLimits()
	provider = NewProvider(nil, "model", limits, 300000)
	_, _, err = provider.Assess(context.Background(), request)
	require.ErrorIs(t, err, modelprovider.ErrVerifierProvider)
	provider = NewProvider(fixtureTransport(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		return modelprovider.StructuredResult{Content: "{}", PromptTokens: limits.MaxInputTokens + 1}, nil
	}), "model", limits, 300000)
	_, attempts, err = provider.Assess(context.Background(), request)
	require.True(t, errors.Is(err, modelprovider.ErrVerifierMalformedResponse))
	require.Len(t, attempts, 1)
}

func TestOrganizationPairCoverageIncludesNegativeAndLockedDecisions(t *testing.T) {
	request, response := assessmentFixture()
	for _, decisions := range [][]Equivalence{{}, {{Ref: "pair", Relation: "distinct"}, {Ref: "pair", Relation: "distinct"}}} {
		response.Equivalence = decisions
		require.ErrorContains(t, Validate(request, response), "exactly one decision per supplied pair ref")
	}
	for _, relation := range []string{"distinct", "ambiguous", "equivalent"} {
		response.Equivalence = []Equivalence{{Ref: "pair", Relation: relation}}
		require.NoError(t, Validate(request, response))
		if relation != "ambiguous" {
			request.Pairs[0].RequiredRelation = relation
			require.NoError(t, Validate(request, response))
			response.Equivalence[0].Relation = "ambiguous"
			require.ErrorContains(t, Validate(request, response), "deterministic context or manager rule")
			request.Pairs[0].RequiredRelation = ""
		}
	}
	request.Pairs = []Pair{}
	response.Equivalence = []Equivalence{}
	require.NoError(t, Validate(request, response))
	response.Equivalence = []Equivalence{{Ref: "pair", Relation: "distinct"}}
	require.ErrorContains(t, Validate(request, response), "outside allowlist")
}

func TestOrganizationPairCoverageRegenerationAcceptsCompleteNegativeResults(t *testing.T) {
	request, response := assessmentFixture()
	request.Items[1].Text = "Atlas stores data in Redis."
	response.Equivalence[0].Relation = "distinct"
	missing := response
	missing.Equivalence = []Equivalence{}
	coverageError := Validate(request, missing)
	require.Error(t, coverageError)
	calls := 0
	provider := NewProvider(fixtureTransport(func(_ context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		calls++
		result := missing
		if calls == 2 {
			var correction correctionFeedback
			require.Len(t, req.Messages, 4)
			require.NoError(t, json.Unmarshal([]byte(req.Messages[3].Content), &correction))
			requireCorrectionErrors(t, coverageError, correction.ValidationErrors)
			result = response
		}
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		return modelprovider.StructuredResult{Content: string(encoded)}, nil
	}), "model", assessor.DefaultSemanticAssessmentLimits(), 300000)
	result, attempts, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, response, result)
	require.Equal(t, 2, calls)
	require.Len(t, attempts, 2)
	require.Equal(t, "response_invalid", attempts[0].FailureCode)
	require.Empty(t, attempts[1].FailureCode)
	require.Greater(t, attempts[1].EstimatedInputTokens, attempts[0].EstimatedInputTokens)
}

func TestOrganizationFullPairCoverageRepairFitsConversation(t *testing.T) {
	request, response := assessmentFixture()
	request.Items, request.Pairs = nil, nil
	response.Items, response.Equivalence = nil, nil
	for i := range 20 {
		ref := fmt.Sprintf("s%d", i)
		request.Items = append(request.Items, Item{Ref: ref, Kind: ontology.EvidenceSource, Text: fmt.Sprintf("Atlas fact %d.", i), OwnerRef: "owner", Context: map[string]string{}})
		response.Items = append(response.Items, Decision{Ref: ref, Status: "classified", DefinitionRef: response.Definitions[0].Ref})
	}
	var pairRefs []string
	for left := range request.Items {
		for right := left + 1; right < len(request.Items); right++ {
			ref := fmt.Sprintf("p%d", len(request.Pairs))
			request.Pairs = append(request.Pairs, Pair{Ref: ref, LeftRef: request.Items[left].Ref, RightRef: request.Items[right].Ref})
			response.Equivalence = append(response.Equivalence, Equivalence{Ref: ref, Relation: "distinct"})
			pairRefs = append(pairRefs, ref)
		}
	}
	require.Len(t, request.Pairs, 190)
	require.NoError(t, Validate(request, response))
	original, err := json.Marshal(request)
	require.NoError(t, err)
	valid, err := json.Marshal(response)
	require.NoError(t, err)
	limits := assessor.DefaultSemanticAssessmentLimits()
	outputTokens, err := assessor.CountTokens(string(valid), limits.Tokenizer)
	require.NoError(t, err)
	limits.MaxInputTokens, limits.MaxOutputTokens, limits.MaxCandidateContextTokens = 32000, outputTokens+128, 512
	contextWindow := limits.MaxInputTokens + limits.MaxOutputTokens

	for _, mode := range []string{"missing", "duplicate", "unknown"} {
		for _, repair := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/repair=%t", mode, repair), func(t *testing.T) {
				invalid := response
				invalid.Equivalence = append([]Equivalence(nil), response.Equivalence...)
				last := len(invalid.Equivalence) - 1
				switch mode {
				case "missing":
					invalid.Equivalence = []Equivalence{}
				case "duplicate":
					invalid.Equivalence[last] = invalid.Equivalence[0]
				case "unknown":
					invalid.Equivalence[last].Ref = "unexpected-pair"
				}
				bad, marshalErr := json.Marshal(invalid)
				require.NoError(t, marshalErr)
				received, marshalErr := json.Marshal(invalid.Equivalence)
				require.NoError(t, marshalErr)
				calls := 0
				var provider *Provider
				provider = NewProvider(fixtureTransport(func(_ context.Context, outbound modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
					calls++
					require.Len(t, outbound.Messages, 2*calls)
					require.Equal(t, SystemPrompt, outbound.Messages[0].Content)
					require.Equal(t, string(original), outbound.Messages[1].Content)
					require.Equal(t, ResponseSchema(request.RequestID), outbound.Schema)
					inputTokens, countErr := provider.messageTokens(outbound.Messages, outbound.Schema)
					require.NoError(t, countErr)
					require.LessOrEqual(t, inputTokens, outbound.MaxInputTokens)
					require.LessOrEqual(t, inputTokens+outbound.MaxOutputTokens, contextWindow)
					for turn := 1; turn < calls; turn++ {
						require.Equal(t, string(bad), outbound.Messages[2*turn].Content)
					}
					if calls > 1 {
						var feedback correctionFeedback
						require.NoError(t, json.Unmarshal([]byte(outbound.Messages[len(outbound.Messages)-1].Content), &feedback))
						require.LessOrEqual(t, len(feedback.ValidationErrors), 4)
						messages := correctionMessages(feedback)
						require.Equal(t, 1, strings.Count(messages, fmt.Sprintf("%q", pairRefs)))
						require.Equal(t, 1, strings.Count(messages, string(received)))
						if mode != "missing" {
							require.Contains(t, messages, fmt.Sprintf("equivalence[%d].ref", last))
						}
					}
					if repair && calls == 3 {
						return modelprovider.StructuredResult{Content: string(valid)}, nil
					}
					return modelprovider.StructuredResult{Content: string(bad)}, nil
				}), "model", limits, contextWindow)
				require.NoError(t, provider.ConfigurationError())
				result, attempts, assessErr := provider.Assess(context.Background(), request)
				require.Equal(t, 3, calls)
				require.Len(t, attempts, 3)
				if repair {
					require.NoError(t, assessErr)
					require.Equal(t, response, result)
					require.Nil(t, attempts[2].Validation)
				} else {
					var malformed *modelprovider.MalformedResponseError
					require.ErrorAs(t, assessErr, &malformed)
					require.Equal(t, "malformed_exhausted", malformed.FailureClass)
					require.Empty(t, result)
				}
			})
		}
	}
}

func TestOrganizationEvidenceClassificationUsesTopics(t *testing.T) {
	request, response := assessmentFixture()
	response.Definitions[0].Kind = ontology.EntityClass
	response.Definitions[0].BaseEntityKind = "project"
	require.ErrorContains(t, Validate(request, response), "incompatible source kind")
	response.Definitions[0].Kind = ontology.Topic
	response.Definitions[0].BaseEntityKind = ""
	require.NoError(t, Validate(request, response))
}

func TestOrganizationMissingDefinitionCorrectionPreservesLockedReuse(t *testing.T) {
	request, valid := assessmentFixture()
	supplied := valid.Definitions[0]
	supplied.Ref = "d0"
	request.Definitions = []Definition{supplied}
	request.Items[0].LockedDefinitionRef = supplied.Ref
	valid.Definitions[0].Ref = "d1"
	valid.Definitions[0].Key = "postgresql"
	valid.Definitions[0].Label = "PostgreSQL"
	valid.Items[0].DefinitionRef = supplied.Ref
	valid.Items[1].DefinitionRef = "d1"
	missing := valid
	missing.Definitions = []Definition{}
	require.ErrorContains(t, Validate(request, missing), `items[1].definition_ref: item "b" definition_ref "d1" is outside allowlist`)
	unlocked := valid
	unlocked.Items = append([]Decision{}, valid.Items...)
	unlocked.Items[0].DefinitionRef = "d1"
	require.ErrorContains(t, Validate(request, unlocked), `item "a" classification changes deterministic reuse: must equal locked_definition_ref "d0"; received "d1"`)
	responses := []Response{missing, unlocked, valid}
	calls := 0
	provider := NewProvider(fixtureTransport(func(_ context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		require.Less(t, calls, len(responses))
		if calls > 0 {
			var correction correctionFeedback
			require.NoError(t, json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &correction))
			requireCorrectionErrors(t, Validate(request, responses[calls-1]), correction.ValidationErrors)
		}
		encoded, err := json.Marshal(responses[calls])
		require.NoError(t, err)
		calls++
		return modelprovider.StructuredResult{Content: string(encoded)}, nil
	}), "model", assessor.DefaultSemanticAssessmentLimits(), 300000)
	result, attempts, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, valid, result)
	require.Len(t, attempts, 3)
	require.Equal(t, 3, calls)
}
