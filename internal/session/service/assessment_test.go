package service

import (
	"encoding/json"
	"strings"
	"testing"

	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/stretchr/testify/require"
)

func TestSessionCombinedAssessmentPreservesExactOffsetsAndOneEntityRef(t *testing.T) {
	text := " 🧭 Ari uses Go. Ari uses Go."
	req := sessionRequest(session.Event{EventID: "one", Text: text})
	segment := session.Segment{Ref: "event:0:0", EventIndex: 0, Start: 0, End: len([]rune(text)), Text: text}
	goRef := "go"
	proposal := session.RelationshipProposal{Ref: "r", SubjectRef: "ari", Predicate: "uses", ObjectRef: &goRef, Polarity: "+", Citations: []session.Citation{{StartRef: segment.Ref, EndRef: segment.Ref}}, KnownEvidenceIDs: []string{}}
	request := session.LinkingRequest{Entities: []session.EntityProposal{{Ref: "ari", Name: "Ari", Kind: "person"}, {Ref: "go", Name: "Go", Kind: "product"}}, Relationships: []session.RelationshipProposal{proposal, proposal}, Segments: []session.Segment{segment}}
	linked := session.LinkingResponse{Groups: []session.EntityGroup{{Ref: "ari", CanonicalRef: "ari", Members: []string{"ari"}}, {Ref: "go", CanonicalRef: "go", Members: []string{"go"}}}}
	evidence, input, err := buildSessionAssessment(&session.Submission{Intake: session.Intake{Request: req}}, request, linked)
	require.NoError(t, err)
	require.Len(t, evidence, 1)
	require.Equal(t, "🧭 Ari uses Go. Ari uses Go.", evidence[0].Content)
	provenance := evidence[0].Metadata["session"].(map[string]any)
	require.Equal(t, 1, provenance["span_start"])
	require.Equal(t, len([]rune(text)), provenance["span_end"])
	require.Equal(t, "one", provenance["event_id"])
	proposals := input["relationship_hints"].([]map[string]any)
	require.Len(t, proposals, 1)
	require.Equal(t, []int{0}, proposals[0]["evidence_indices"])
	require.Equal(t, "entity:0", proposals[0]["subject"].(map[string]any)["session_ref"])
}

func TestSessionExtractionNumericValueReachesCombinedProposalExactly(t *testing.T) {
	for _, number := range []string{"42", "9007199254740993", "0.1234567890123456789"} {
		req := sessionRequest(session.Event{EventID: "one", Text: "Ari records number " + number + "."})
		segment := session.Segment{Ref: "event:0:0", EventIndex: 0, Start: 0, End: len([]rune(req.Events[0].Text)), Text: req.Events[0].Text}
		raw := []byte(`{"overflow":false,"request_id":"numeric","coverage":["event:0:0"],"entities":[{"ref":"ari","name":"Ari","entity_kind":"person"}],"relationships":[{"ref":"numeric","subject_ref":"ari","predicate":"records_number","object_ref":null,"object_value":{"type":"number","value":` + number + `,"unit":"","display":""},"polarity":"+","citations":[{"start_ref":"event:0:0","end_ref":"event:0:0"}],"known_evidence_ids":[],"valid_from":null,"valid_to":null}],"security_signals":[]}`)
		response, err := session.DecodeExtraction(raw)
		require.NoError(t, err)
		require.NoError(t, session.ValidateExtraction(session.ExtractionRequest{RequestID: "numeric", Window: session.Window{Core: []session.Segment{segment}}}, response))
		input := session.LinkingRequest{Entities: response.Entities, Relationships: response.Relationships, Segments: []session.Segment{segment}}
		linked := session.LinkingResponse{Groups: []session.EntityGroup{{Ref: "ari", CanonicalRef: "ari", Members: []string{"ari"}}}}
		_, proposal, err := buildSessionAssessment(&session.Submission{Intake: session.Intake{Request: req}}, input, linked)
		require.NoError(t, err)
		value := proposal["relationship_hints"].([]map[string]any)[0]["object"].(map[string]any)["value"].(map[string]any)["value"]
		require.Equal(t, json.Number(number), value)
	}
}

func TestSessionCombinedAssessmentPreservesUnitsTimesAndKnownEvidence(t *testing.T) {
	text := "  Ari records 42 ms on 2026-10-10.  "
	observed := "2026-10-10T12:00:00Z"
	request := sessionRequest(session.Event{EventID: "one", Text: text, OccurredAt: &observed})
	segment := session.Segment{Ref: "current", EventIndex: 0, Start: 0, End: len([]rune(text)), Text: text}
	value := &session.ValueProposal{Type: "number", Value: json.RawMessage(`42`), Unit: "ms", Display: "42 ms"}
	proposal := session.RelationshipProposal{Ref: "first", SubjectRef: "ari", Predicate: "records", ObjectValue: value, Polarity: "+", Citations: []session.Citation{{StartRef: segment.Ref, EndRef: segment.Ref}}, KnownEvidenceIDs: []string{"history"}, ValidFrom: &observed, ValidTo: &observed}
	second := proposal
	second.Ref, second.KnownEvidenceIDs = "second", []string{"history", "other-history"}
	input := session.LinkingRequest{Entities: []session.EntityProposal{{Ref: "ari", Name: "Ari", Kind: "person"}}, Relationships: []session.RelationshipProposal{proposal, second}, Segments: []session.Segment{segment}}
	linked := session.LinkingResponse{Groups: []session.EntityGroup{{Ref: "ari", CanonicalRef: "ari", Members: []string{"ari"}}}}
	evidence, combined, err := buildSessionAssessment(&session.Submission{Intake: session.Intake{Request: request}}, input, linked)
	require.NoError(t, err)
	require.Len(t, evidence, 1)
	require.Equal(t, observed, evidence[0].Metadata["session"].(map[string]any)["occurred_at"])
	proposals := combined["relationship_hints"].([]map[string]any)
	require.Len(t, proposals, 1)
	require.Equal(t, []string{"history", "other-history"}, proposals[0]["known_evidence_ids"])
	require.Equal(t, observed, proposals[0]["valid_from"])
	require.Equal(t, observed, proposals[0]["valid_to"])
	object := proposals[0]["object"].(map[string]any)["value"].(map[string]any)
	require.Equal(t, json.Number("42"), object["value"])
	require.Equal(t, "ms", object["unit"])
	require.Equal(t, "42 ms", object["display"])
	input.Relationships[0].Citations[0].StartRef = "unknown"
	_, _, err = buildSessionAssessment(&session.Submission{Intake: session.Intake{Request: request}}, input, linked)
	require.ErrorIs(t, err, session.ErrInvalidInput)
	input.Relationships[0].Citations[0].StartRef = segment.Ref
	request.Events[0].Text = strings.Repeat(" ", 512) + "Ari uses Go."
	windows, err := BuildWindows(request, "o200k_base")
	require.NoError(t, err)
	input.Segments[0] = windows[0].Core[0]
	for index := range input.Relationships {
		input.Relationships[index].Citations = []session.Citation{{StartRef: input.Segments[0].Ref, EndRef: input.Segments[0].Ref}}
	}
	_, _, err = buildSessionAssessment(&session.Submission{Intake: session.Intake{Request: request}}, input, linked)
	require.ErrorIs(t, err, session.ErrInvalidInput)
}
