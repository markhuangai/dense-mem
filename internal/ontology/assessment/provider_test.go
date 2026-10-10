package assessment

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

type fixtureTransport func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error)

func (f fixtureTransport) Complete(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return f(ctx, request)
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
	require.ErrorContains(t, Validate(request, response), "not transitive")
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
	require.EqualError(t, Validate(request, response), `response.definitions is only for new definitions; omit supplied ref "d0" and reuse it in items[].definition_ref`)
	response.Definitions[0].Ref = ""
	require.EqualError(t, Validate(request, response), "new definition ref is invalid or duplicated")
	response.Definitions[0].Ref = "d1"
	response.Definitions = append(response.Definitions, response.Definitions[0])
	require.EqualError(t, Validate(request, response), "new definition ref is invalid or duplicated")
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
			var correction map[string]string
			require.NoError(t, json.Unmarshal([]byte(req.Messages[3].Content), &correction))
			require.Equal(t, echoError.Error(), correction["validation_errors"])
			require.Contains(t, correction["validation_errors"], `"d0"`)
			require.NotContains(t, correction["validation_errors"], `"d1"`)
		}
		encoded, err := json.Marshal(responses[calls])
		require.NoError(t, err)
		calls++
		return modelprovider.StructuredResult{Content: string(encoded)}, nil
	}), "model", assessor.DefaultSemanticAssessmentLimits())
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
		require.Equal(t, ResponseSchema(), req.Schema)
		if calls == 1 {
			return modelprovider.StructuredResult{Content: `{"request_id":"request","definitions":[],"items":[],"equivalence":[]}`}, nil
		}
		require.Len(t, req.Messages, 4)
		require.Contains(t, req.Messages[3].Content, "validation_errors")
		return modelprovider.StructuredResult{Content: string(valid), PromptTokens: 500, CompletionTokens: 100}, nil
	})
	provider := NewProvider(transport, "model", assessor.DefaultSemanticAssessmentLimits())
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
	}), "model", assessor.DefaultSemanticAssessmentLimits())
	_, attempts, err = provider.Assess(context.Background(), request)
	require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
	require.Equal(t, 3, calls)
	require.Len(t, attempts, 3)
}

func TestOrganizationInitialBudgetAllowsBothCompleteRegenerations(t *testing.T) {
	request, response := assessmentFixture()
	request.Items[0].Text = strings.Repeat("context detail ", 4000)
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 2048
	headroom := limits.MaxInputTokens - assessor.SemanticAssessmentConversationInputLimit(limits)
	measurement, err := NewProvider(nil, "model", limits).Measure(request)
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
	}), "model", limits)
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

func TestOrganizationLargeDiagnosticsDoNotConsumeRepairHeadroom(t *testing.T) {
	request, response := assessmentFixture()
	request.Items[0].Text = strings.Repeat("context detail ", 4000)
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.MaxOutputTokens = 16384
	headroom := limits.MaxInputTokens - assessor.SemanticAssessmentConversationInputLimit(limits)
	measurement, err := NewProvider(nil, "model", limits).Measure(request)
	require.NoError(t, err)
	limits.MaxInputTokens = measurement + headroom
	invalid := `{"雪` + strings.Repeat("invalid ", limits.MaxOutputTokens-100) + `":true}`
	invalidTokens, err := assessor.CountTokens(invalid, limits.Tokenizer)
	require.NoError(t, err)
	require.LessOrEqual(t, invalidTokens, limits.MaxOutputTokens)
	require.Greater(t, invalidTokens, limits.MaxOutputTokens/2)
	_, decodeErr := Decode(invalid)
	require.ErrorContains(t, decodeErr, "unknown field")
	require.Greater(t, len([]rune(decodeErr.Error())), maxCorrectionErrorRunes)
	valid, err := json.Marshal(response)
	require.NoError(t, err)
	calls := 0
	provider := NewProvider(fixtureTransport(func(_ context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		calls++
		if calls > 1 {
			var correction map[string]string
			require.NoError(t, json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &correction))
			require.Len(t, []rune(correction["validation_errors"]), maxCorrectionErrorRunes)
			require.Contains(t, correction["validation_errors"], "雪")
			require.Contains(t, correction["validation_errors"], "diagnostic truncated")
			require.NotContains(t, correction["validation_errors"], "\uFFFD")
			require.Equal(t, invalid, req.Messages[len(req.Messages)-2].Content)
		}
		if calls < 3 {
			return modelprovider.StructuredResult{Content: invalid}, nil
		}
		return modelprovider.StructuredResult{Content: string(valid)}, nil
	}), "model", limits)
	result, attempts, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, response, result)
	require.Equal(t, 3, calls)
	require.Len(t, attempts, 3)
	for _, attempt := range attempts {
		require.LessOrEqual(t, attempt.EstimatedInputTokens, limits.MaxInputTokens)
	}
	require.Empty(t, attempts[2].FailureCode)
}

func TestOrganizationProviderFailuresBudgetsAndCancellation(t *testing.T) {
	request, _ := assessmentFixture()
	limits := assessor.DefaultSemanticAssessmentLimits()
	calls := 0
	transport := fixtureTransport(func(ctx context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		calls++
		return modelprovider.StructuredResult{}, &modelprovider.ProviderError{Provider: "fixture", Message: "failed"}
	})
	provider := NewProvider(transport, "model", limits)
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
	provider = NewProvider(transport, "model", limits)
	_, _, err = provider.Assess(context.Background(), request)
	require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
	require.Equal(t, 1, calls)
	limits = assessor.DefaultSemanticAssessmentLimits()
	limits.Tokenizer = "unknown"
	provider = NewProvider(transport, "model", limits)
	_, _, err = provider.Assess(context.Background(), request)
	require.Error(t, err)
	require.Equal(t, 1, calls)
	limits = assessor.DefaultSemanticAssessmentLimits()
	provider = NewProvider(nil, "model", limits)
	_, _, err = provider.Assess(context.Background(), request)
	require.ErrorIs(t, err, modelprovider.ErrVerifierProvider)
	provider = NewProvider(fixtureTransport(func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		return modelprovider.StructuredResult{Content: "{}", PromptTokens: limits.MaxInputTokens + 1}, nil
	}), "model", limits)
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
			var correction map[string]string
			require.Len(t, req.Messages, 4)
			require.NoError(t, json.Unmarshal([]byte(req.Messages[3].Content), &correction))
			require.Equal(t, coverageError.Error(), correction["validation_errors"])
			result = response
		}
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		return modelprovider.StructuredResult{Content: string(encoded)}, nil
	}), "model", assessor.DefaultSemanticAssessmentLimits())
	result, attempts, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, response, result)
	require.Equal(t, 2, calls)
	require.Len(t, attempts, 2)
	require.Equal(t, "response_invalid", attempts[0].FailureCode)
	require.Empty(t, attempts[1].FailureCode)
	require.Greater(t, attempts[1].EstimatedInputTokens, attempts[0].EstimatedInputTokens)
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
	require.EqualError(t, Validate(request, missing), `item "b" definition_ref "d1" is outside allowlist; include a new definition in response.definitions or reuse a supplied ref`)
	unlocked := valid
	unlocked.Items = append([]Decision{}, valid.Items...)
	unlocked.Items[0].DefinitionRef = "d1"
	require.EqualError(t, Validate(request, unlocked), "classification changes deterministic reuse")
	responses := []Response{missing, unlocked, valid}
	calls := 0
	provider := NewProvider(fixtureTransport(func(_ context.Context, req modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		require.Less(t, calls, len(responses))
		if calls > 0 {
			var correction map[string]string
			require.NoError(t, json.Unmarshal([]byte(req.Messages[len(req.Messages)-1].Content), &correction))
			require.Equal(t, Validate(request, responses[calls-1]).Error(), correction["validation_errors"])
		}
		encoded, err := json.Marshal(responses[calls])
		require.NoError(t, err)
		calls++
		return modelprovider.StructuredResult{Content: string(encoded)}, nil
	}), "model", assessor.DefaultSemanticAssessmentLimits())
	result, attempts, err := provider.Assess(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, valid, result)
	require.Len(t, attempts, 3)
	require.Equal(t, 3, calls)
}
