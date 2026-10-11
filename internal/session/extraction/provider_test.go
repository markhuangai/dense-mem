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
		Entities:        []session.EntityProposal{{Ref: "ari", Name: "Ari", Kind: "person"}, {Ref: "go", Name: "Go", Kind: "product"}},
		Relationships:   []session.RelationshipProposal{{Ref: "uses-go", SubjectRef: "ari", Predicate: "uses", ObjectRef: stringPointer("go"), Polarity: "+", Citations: []session.Citation{{StartRef: segment.Ref, EndRef: segment.Ref}}, KnownEvidenceIDs: []string{}}},
		SecuritySignals: []session.ExtractionSignal{},
	}
	return request, response
}

func stringPointer(value string) *string { return &value }

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
	validJSON, err := json.Marshal(valid)
	require.NoError(t, err)
	invalid := valid
	invalid.Relationships = append([]session.RelationshipProposal(nil), valid.Relationships...)
	invalid.Relationships[0].Citations = []session.Citation{{StartRef: blank.Ref, EndRef: blank.Ref}}
	invalidJSON, err := json.Marshal(invalid)
	require.NoError(t, err)
	transport := &outboundFixture{bodies: []string{string(invalidJSON), string(validJSON)}}
	provider := NewProvider(transport, "fixture-model", assessor.DefaultSemanticAssessmentLimits())
	response, err := provider.Extract(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, valid, response)
	require.Len(t, transport.requests, 2)
	require.Equal(t, transport.requests[0].Messages[1], transport.requests[1].Messages[1])
	require.Contains(t, transport.requests[1].Messages[2].Content, "non-whitespace")
	valid.Relationships[0].Citations = []session.Citation{{StartRef: blank.Ref, EndRef: request.Window.Core[1].Ref}}
	require.NoError(t, session.ValidateExtraction(request, valid))
}

func TestSessionProviderNeverSplicesIncompleteResponses(t *testing.T) {
	request, valid := extractionFixture()
	first := valid
	first.Relationships = nil
	second := valid
	second.Entities = nil
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

func TestSessionLinkingRequiresCompleteCompatibleGroups(t *testing.T) {
	request := session.LinkingRequest{RequestID: "link", Entities: []session.EntityProposal{
		{Ref: "first", Name: "Jordan", Kind: "person"}, {Ref: "second", Name: "Jordan", Kind: "person"}, {Ref: "product", Name: "Go", Kind: "product"},
	}}
	valid := session.LinkingResponse{RequestID: "link", Groups: []session.EntityGroup{
		{Ref: "designer", CanonicalRef: "first", Members: []string{"first"}},
		{Ref: "musician", CanonicalRef: "second", Members: []string{"second"}},
		{Ref: "go", CanonicalRef: "product", Members: []string{"product"}},
	}}
	require.NoError(t, session.ValidateLinking(request, valid))
	invalid := valid
	invalid.Groups = valid.Groups[:2]
	require.Error(t, session.ValidateLinking(request, invalid))
	invalid = valid
	invalid.Groups = []session.EntityGroup{{Ref: "mixed", CanonicalRef: "first", Members: []string{"first", "second", "product"}}}
	require.Error(t, session.ValidateLinking(request, invalid))
}

func TestSessionLinkingRegenerationIdentifiesWrongRequestID(t *testing.T) {
	request := session.LinkingRequest{RequestID: "session:556a0828-82d8-491e-84d2-5c24afb45717:linking", Entities: []session.EntityProposal{{Ref: "ari", Name: "Ari", Kind: "person"}}}
	valid := session.LinkingResponse{RequestID: request.RequestID, Groups: []session.EntityGroup{{Ref: "ari", CanonicalRef: "ari", Members: []string{"ari"}}}}
	invalid := valid
	invalid.RequestID = "session:556a0828-82d2-491e-84d2-5c24afb45717:linking"
	invalidJSON, err := json.Marshal(invalid)
	require.NoError(t, err)
	validJSON, err := json.Marshal(valid)
	require.NoError(t, err)
	transport := &outboundFixture{bodies: []string{string(invalidJSON), string(validJSON)}}
	response, err := NewProvider(transport, "fixture-model", assessor.DefaultSemanticAssessmentLimits()).Link(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, valid, response)
	require.Len(t, transport.requests, 2)
	require.Equal(t, transport.requests[0].Messages[1], transport.requests[1].Messages[1])
	require.Contains(t, transport.requests[1].Messages[2].Content, "request_id must equal")
	require.Contains(t, transport.requests[1].Messages[2].Content, request.RequestID)
	require.NotEqual(t, request.RequestID, invalid.RequestID)
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
