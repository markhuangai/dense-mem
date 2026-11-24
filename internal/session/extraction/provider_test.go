package extraction

import (
	"context"
	"encoding/json"

	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/provider/structured"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/stretchr/testify/require"
)

type outboundFixture struct {
	bodies   []string
	requests []modelprovider.StructuredRequest
}

func (f *outboundFixture) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	f.requests = append(f.requests, request)
	index := min(len(f.requests)-1, len(f.bodies)-1)
	return modelprovider.StructuredResult{Content: f.bodies[index]}, nil
}

func extractionFixture() (session.ExtractionRequest, session.ExtractionResponse) {
	segment := session.Segment{Ref: "event:0:0", EventIndex: 0, Start: 0, End: 12, Text: "Ari uses Go."}
	request := session.ExtractionRequest{RequestID: "request-one", Window: session.Window{Index: 0, Core: []session.Segment{segment}}, Prior: []session.PriorEvent{}}
	response := session.ExtractionResponse{
		RequestID: request.RequestID, Coverage: []string{segment.Ref},
		Excerpts:        []session.Excerpt{{EventIndex: 0, Start: 0, End: 12, Content: segment.Text}},
		SecuritySignals: []session.ExtractionSignal{},
	}
	return request, response
}

func TestSessionProviderRegeneratesCompleteUnknownReferenceResponse(t *testing.T) {
	request, valid := extractionFixture()
	validJSON, err := json.Marshal(valid)
	require.NoError(t, err)
	invalid := valid
	invalid.Coverage = []string{"unknown"}
	invalidJSON, err := json.Marshal(invalid)
	require.NoError(t, err)
	transport := &outboundFixture{bodies: []string{string(invalidJSON), string(validJSON)}}
	provider := NewProvider(transport, "fixture-model", assessor.DefaultSemanticAssessmentLimits())
	var stages []string
	ctx := requestctx.WithProgressObserver(t.Context(), func(_ context.Context, update requestctx.ProgressUpdate) { stages = append(stages, update.Stage) })
	response, err := provider.Extract(ctx, request)
	require.NoError(t, err)
	require.Equal(t, valid, response)
	require.Equal(t, []string{"provider_request", "session_validation", "session_regeneration", "provider_request", "session_validation"}, stages)
	require.Len(t, transport.requests, 2)
	require.Equal(t, transport.requests[0].Messages[1], transport.requests[1].Messages[1])
	require.Equal(t, 8192, transport.requests[0].MaxOutputTokens)
	require.Equal(t, 32768, transport.requests[0].MaxInputTokens)
}

func TestSessionProviderRegeneratesWhitespaceCitationBeforeCheckpointing(t *testing.T) {
	request, valid := extractionFixture()
	request.Window.Core[0].Start, request.Window.Core[0].End = 512, 524
	blank := session.Segment{Ref: "blank", EventIndex: 0, Start: 0, End: 512, Text: strings.Repeat("\u2003", 512)}
	request.Window.Core = append([]session.Segment{blank}, request.Window.Core...)
	valid.Coverage = append([]string{blank.Ref}, valid.Coverage...)
	valid.Excerpts[0].Start, valid.Excerpts[0].End = 512, 524
	validJSON, err := json.Marshal(valid)
	require.NoError(t, err)
	invalid := valid
	invalid.Excerpts = []session.Excerpt{{EventIndex: 0, Start: 0, End: 512, Content: blank.Text}}
	invalidJSON, err := json.Marshal(invalid)
	require.NoError(t, err)
	transport := &outboundFixture{bodies: []string{string(invalidJSON), string(validJSON)}}
	provider := NewProvider(transport, "fixture-model", assessor.DefaultSemanticAssessmentLimits())
	response, err := provider.Extract(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, valid, response)
	require.Len(t, transport.requests, 2)
	require.Equal(t, transport.requests[0].Messages[1], transport.requests[1].Messages[1])
	require.Contains(t, transport.requests[1].Messages[len(transport.requests[1].Messages)-1].Content, "excerpt bounds")
	valid.Excerpts[0] = session.Excerpt{EventIndex: 0, Start: 0, End: 524, Content: blank.Text + request.Window.Core[1].Text}
	require.NoError(t, session.ValidateExtraction(request, valid))
}

func TestSessionProviderNeverSplicesIncompleteResponses(t *testing.T) {
	request, valid := extractionFixture()
	first := valid
	first.Excerpts = nil
	second := valid
	second.Coverage = nil
	firstJSON, err := json.Marshal(first)
	require.NoError(t, err)
	secondJSON, err := json.Marshal(second)
	require.NoError(t, err)
	transport := &outboundFixture{bodies: []string{string(firstJSON), string(secondJSON)}}
	provider := NewProvider(transport, "fixture-model", assessor.DefaultSemanticAssessmentLimits())
	_, err = provider.Extract(context.Background(), request)
	require.Error(t, err)
	var malformed *modelprovider.MalformedResponseError
	require.ErrorAs(t, err, &malformed)
	require.Equal(t, 3, malformed.Attempts)
	require.Len(t, transport.requests, 3)
}

func TestSessionProviderCancellationAndInputBudgetPreventOutboundCalls(t *testing.T) {
	request, valid := extractionFixture()
	body, err := json.Marshal(valid)
	require.NoError(t, err)
	transport := &outboundFixture{bodies: []string{string(body)}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = NewProvider(transport, "fixture", assessor.DefaultSemanticAssessmentLimits()).Extract(ctx, request)
	require.ErrorIs(t, err, context.Canceled)
	limits := assessor.DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1
	_, err = NewProvider(transport, "fixture", limits).Extract(context.Background(), request)
	require.ErrorIs(t, err, session.ErrBudget)
	require.Empty(t, transport.requests)
	_, err = NewProvider(nil, "fixture", limits).Extract(context.Background(), request)
	var unavailable *modelprovider.ProviderError
	require.ErrorAs(t, err, &unavailable)
}

func TestSessionProviderRealHTTPRecoveryAndExhaustion(t *testing.T) {
	request, valid := extractionFixture()
	for _, exhaust := range []bool{false, true} {
		name := "recovery"
		if exhaust {
			name = "exhaustion"
		}
		t.Run(name, func(t *testing.T) {
			calls := 0
			var original string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var envelope struct{ Messages []modelprovider.Message }
				if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
					http.Error(w, "invalid fixture request", http.StatusBadRequest)
					return
				}
				calls++
				require.Len(t, envelope.Messages, 2*calls)
				if calls == 1 {
					original = envelope.Messages[1].Content
				} else {
					require.Equal(t, original, envelope.Messages[1].Content)
					require.Equal(t, "assistant", envelope.Messages[2].Role)
					require.Contains(t, envelope.Messages[2*calls-1].Content, "validation_errors")
				}
				candidate := valid
				switch {
				case calls == 1:
					candidate.Coverage = []string{"non-current-ref"}
				case calls == 2 || exhaust:
					candidate.Excerpts = []session.Excerpt{{EventIndex: 0, Start: 0, End: 12, Content: "another text"}}
				}
				body, err := json.Marshal(candidate)
				if err != nil {
					http.Error(w, "fixture response error", http.StatusInternalServerError)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": string(body)}}}})
			}))
			defer upstream.Close()
			transport := structured.NewOpenAI(&config.Config{AIVerifierAPIURL: upstream.URL}, upstream.Client())
			response, err := NewProvider(transport, "fixture", assessor.DefaultSemanticAssessmentLimits()).Extract(t.Context(), request)
			require.Equal(t, 3, calls)
			if exhaust {
				require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
				require.Empty(t, response.Excerpts)
				return
			}
			require.NoError(t, err)
			require.Equal(t, valid, response)
		})
	}
}
