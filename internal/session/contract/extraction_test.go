package contract

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func responseValidationFixture() (ExtractionRequest, ExtractionResponse) {
	segment := Segment{Ref: "event:0:0", EventIndex: 0, Start: 0, End: 12, Text: "Ari uses Go."}
	return ExtractionRequest{RequestID: "request", Window: Window{Core: []Segment{segment}}}, ExtractionResponse{RequestID: "request", Coverage: []string{segment.Ref}, Excerpts: []Excerpt{{EventIndex: 0, Start: 0, End: 12, Content: segment.Text}}, SecuritySignals: []ExtractionSignal{}}
}

func TestEvidenceOnlyUnitSessionExtractionRejectsIncompleteAndOutOfAllowlistResponses(t *testing.T) {
	for _, mutate := range []func(*ExtractionResponse){
		func(r *ExtractionResponse) { r.RequestID = "different" },
		func(r *ExtractionResponse) { r.Coverage = nil },
		func(r *ExtractionResponse) { r.Coverage = []string{} },
		func(r *ExtractionResponse) { r.Coverage = append(r.Coverage, r.Coverage[0]) },
		func(r *ExtractionResponse) { r.Coverage[0] = "outside" },
		func(r *ExtractionResponse) { r.Excerpts = nil },
		func(r *ExtractionResponse) { r.Excerpts[0].Content = "invented" },
		func(r *ExtractionResponse) { r.Excerpts[0].Start = -1 },
		func(r *ExtractionResponse) { r.Excerpts[0].End = 99 },
		func(r *ExtractionResponse) { r.Excerpts[0].EventIndex = 1 },
		func(r *ExtractionResponse) { r.Excerpts = append(r.Excerpts, r.Excerpts[0]) },
		func(r *ExtractionResponse) { r.SecuritySignals = nil },
		func(r *ExtractionResponse) {
			r.SecuritySignals = []ExtractionSignal{{SegmentRef: "prior", Kind: "prompt_injection"}}
		},
		func(r *ExtractionResponse) {
			r.SecuritySignals = []ExtractionSignal{{SegmentRef: "event:0:0", Kind: "unknown"}}
		},
	} {
		request, response := responseValidationFixture()
		mutate(&response)
		require.Error(t, ValidateExtraction(request, response))
	}
	request, response := responseValidationFixture()
	response.Overflow = true
	require.ErrorIs(t, ValidateExtraction(request, response), ErrBudget)
}

func TestEvidenceOnlyUnitSessionExtractionDecoderRequiresCompleteClosedObjects(t *testing.T) {
	request, response := responseValidationFixture()
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	decoded, err := DecodeExtraction(raw)
	require.NoError(t, err)
	require.NoError(t, ValidateExtraction(request, decoded))
	for _, body := range []string{
		`{"request_id":"request","overflow":false,"coverage":[],"security_signals":[]}`,
		`{"request_id":"request","overflow":false,"coverage":[],"excerpts":null,"security_signals":[]}`,
		`{"request_id":"request","overflow":false,"coverage":[],"excerpts":[{"event_index":0,"start":0,"content":"Ari"}],"security_signals":[]}`,
		`{"request_id":"request","request_id":"other","overflow":false,"coverage":[],"excerpts":[],"security_signals":[]}`,
		`{"request_id":"request","overflow":false,"coverage":[],"excerpts":[],"security_signals":[],"relationships":[]}`,
	} {
		_, err := DecodeExtraction([]byte(body))
		require.Error(t, err)
	}
}
