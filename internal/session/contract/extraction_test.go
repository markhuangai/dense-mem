package contract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func responseValidationFixture() (ExtractionRequest, ExtractionResponse) {
	core := Segment{Ref: "current", EventIndex: 1, Start: 0, End: 12, Text: "Ari uses Go."}
	prior := Segment{Ref: "neighbor", EventIndex: 0, Start: 0, End: 12, Text: "Ari uses Go."}
	request := ExtractionRequest{RequestID: "immutable-request", Window: Window{Core: []Segment{core}, Before: &prior}, Prior: []PriorEvent{{EvidenceIDs: []string{"prior-evidence"}}}}
	response := ExtractionResponse{RequestID: request.RequestID, Coverage: []string{core.Ref}, Entities: []EntityProposal{{Ref: "ari", Name: "Ari", Kind: "person"}}, Relationships: []RelationshipProposal{{Ref: "uses", SubjectRef: "ari", Predicate: "uses", ObjectValue: &ValueProposal{Type: "string", Value: json.RawMessage(`"Go"`)}, Polarity: "+", Citations: []Citation{{StartRef: core.Ref, EndRef: core.Ref}}, KnownEvidenceIDs: []string{}}}, SecuritySignals: []ExtractionSignal{}}
	return request, response
}

func TestSessionExtractionRejectsIncompleteAndOutOfAllowlistResponses(t *testing.T) {
	tests := []struct {
		name   string
		change func(*ExtractionResponse)
	}{
		{"wrong operation", func(r *ExtractionResponse) { r.RequestID = "another-request" }},
		{"missing arrays", func(r *ExtractionResponse) { r.Entities = nil }},
		{"missing coverage", func(r *ExtractionResponse) { r.Coverage = []string{} }},
		{"context coverage", func(r *ExtractionResponse) { r.Coverage = []string{"neighbor"} }},
		{"duplicate coverage", func(r *ExtractionResponse) { r.Coverage = []string{"current", "current"} }},
		{"unknown kind", func(r *ExtractionResponse) { r.Entities[0].Kind = "provider-kind" }},
		{"duplicate entity", func(r *ExtractionResponse) { r.Entities = append(r.Entities, r.Entities[0]) }},
		{"unused entity", func(r *ExtractionResponse) {
			r.Entities = append(r.Entities, EntityProposal{Ref: "unused", Name: "Rust", Kind: "product"})
		}},
		{"duplicate relationship", func(r *ExtractionResponse) { r.Relationships = append(r.Relationships, r.Relationships[0]) }},
		{"unknown subject", func(r *ExtractionResponse) { r.Relationships[0].SubjectRef = "unknown" }},
		{"two endpoints", func(r *ExtractionResponse) { ref := "ari"; r.Relationships[0].ObjectRef = &ref }},
		{"unknown object", func(r *ExtractionResponse) {
			ref := "unknown"
			r.Relationships[0].ObjectRef = &ref
			r.Relationships[0].ObjectValue = nil
		}},
		{"unknown polarity", func(r *ExtractionResponse) { r.Relationships[0].Polarity = "?" }},
		{"missing citations", func(r *ExtractionResponse) { r.Relationships[0].Citations = nil }},
		{"unknown source", func(r *ExtractionResponse) { r.Relationships[0].Citations[0].StartRef = "unknown" }},
		{"cross-event range", func(r *ExtractionResponse) { r.Relationships[0].Citations[0].EndRef = "neighbor" }},
		{"context-only citation", func(r *ExtractionResponse) {
			r.Relationships[0].Citations = []Citation{{StartRef: "neighbor", EndRef: "neighbor"}}
		}},
		{"unknown known evidence", func(r *ExtractionResponse) {
			r.Relationships[0].KnownEvidenceIDs = []string{"another-profile-evidence"}
		}},
		{"duplicate known evidence", func(r *ExtractionResponse) {
			r.Relationships[0].KnownEvidenceIDs = []string{"prior-evidence", "prior-evidence"}
		}},
		{"unknown signal", func(r *ExtractionResponse) {
			r.SecuritySignals = []ExtractionSignal{{SegmentRef: "current", Kind: "provider-signal"}}
		}},
		{"duplicate signal", func(r *ExtractionResponse) {
			signal := ExtractionSignal{SegmentRef: "current", Kind: "prompt_injection"}
			r.SecuritySignals = []ExtractionSignal{signal, signal}
		}},
		{"closed bound", func(r *ExtractionResponse) { r.Entities = make([]EntityProposal, 401) }},
		{"explicit overflow", func(r *ExtractionResponse) { r.Overflow = true }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, response := responseValidationFixture()
			test.change(&response)
			before, err := json.Marshal(response)
			require.NoError(t, err)
			require.Error(t, ValidateExtraction(request, response))
			after, err := json.Marshal(response)
			require.NoError(t, err)
			require.Equal(t, string(before), string(after))
		})
	}
	request, response := responseValidationFixture()
	response.Relationships[0].KnownEvidenceIDs = []string{"prior-evidence"}
	response.SecuritySignals = []ExtractionSignal{{SegmentRef: "current", Kind: "prompt_injection"}}
	require.NoError(t, ValidateExtraction(request, response))
}

func TestSessionExtractionTypedValuesAndTemporalBounds(t *testing.T) {
	for _, test := range []struct {
		kind, value string
		valid       bool
	}{
		{"number", `9007199254740993`, true}, {"number", `"42"`, false},
		{"boolean", `true`, true}, {"boolean", `"true"`, false},
		{"date", `"2026-10-10"`, true}, {"date", `"2026-02-31"`, false},
		{"date_time", `"2026-10-10T12:00:00Z"`, true}, {"date_time", `"yesterday"`, false},
		{"string", `42`, false}, {"string", `""`, false}, {"string", `{`, false}, {"unknown", `"Go"`, false},
	} {
		t.Run(test.kind+test.value, func(t *testing.T) {
			request, response := responseValidationFixture()
			response.Relationships[0].ObjectValue = &ValueProposal{Type: test.kind, Value: json.RawMessage(test.value)}
			err := ValidateExtraction(request, response)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
	request, response := responseValidationFixture()
	from, to := "2026-10-10T12:00:00Z", "2026-10-11T12:00:00Z"
	response.Relationships[0].ValidFrom, response.Relationships[0].ValidTo = &from, &to
	require.NoError(t, ValidateExtraction(request, response))
	response.Relationships[0].ValidFrom, response.Relationships[0].ValidTo = &to, &from
	require.Error(t, ValidateExtraction(request, response))
	bad := "not-a-time"
	response.Relationships[0].ValidFrom = &bad
	require.Error(t, ValidateExtraction(request, response))
	response.Relationships[0].ValidFrom, response.Relationships[0].ValidTo = nil, &bad
	require.Error(t, ValidateExtraction(request, response))
}

func TestSessionExtractionDecoderRequiresCompleteClosedObjects(t *testing.T) {
	_, response := responseValidationFixture()
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)
	for _, invalid := range []string{
		strings.Replace(string(encoded), `"entities":`, `"unknown":`, 1),
		strings.Replace(string(encoded), `"name":"Ari"`, `"name":null`, 1),
		strings.Replace(string(encoded), `"entity_kind":"person"`, `"unknown":"person"`, 1),
		strings.Replace(string(encoded), `"unit":""`, `"unit":null`, 1),
		string(encoded) + `{}`,
	} {
		_, err := DecodeExtraction([]byte(invalid))
		require.Error(t, err)
	}
	decoded, err := DecodeExtraction(encoded)
	require.NoError(t, err)
	require.Equal(t, response, decoded)
}

func TestSessionLinkingRejectsWrongIdentityAndIncompleteGroups(t *testing.T) {
	request := LinkingRequest{RequestID: "immutable-request", Entities: []EntityProposal{{Ref: "ari", Name: "Ari", Kind: "person"}, {Ref: "go", Name: "Go", Kind: "product"}}}
	for _, response := range []LinkingResponse{
		{RequestID: "wrong", Groups: []EntityGroup{}},
		{RequestID: request.RequestID},
		{RequestID: request.RequestID, Groups: []EntityGroup{{Ref: "unknown", CanonicalRef: "unknown", Members: []string{"unknown"}}}},
		{RequestID: request.RequestID, Groups: []EntityGroup{{Ref: "ari", CanonicalRef: "ari", Members: []string{"ari"}}}},
		{RequestID: request.RequestID, Groups: []EntityGroup{{Ref: "ari", CanonicalRef: "ari", Members: []string{"go"}}}},
		{RequestID: request.RequestID, Groups: []EntityGroup{{Ref: "ari", CanonicalRef: "ari", Members: []string{"ari", "ari"}}}},
	} {
		require.Error(t, ValidateLinking(request, response))
	}
	request.Entities = append(request.Entities, request.Entities[0])
	require.Error(t, ValidateLinking(request, LinkingResponse{RequestID: request.RequestID, Groups: []EntityGroup{}}))
}
