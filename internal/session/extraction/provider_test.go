package extraction

import (
	"context"
	"encoding/json"

	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
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
	response, err := provider.Extract(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, valid, response)
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
	require.Contains(t, transport.requests[1].Messages[2].Content, "excerpt bounds")
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
