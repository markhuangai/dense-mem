package conflictassessment

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareConflictAssessmentRequestRejectsIncompleteDossier(t *testing.T) {
	_, errs := PrepareConflictAssessmentRequest(ConflictAssessmentRequest{}, DefaultSemanticAssessmentLimits())
	require.NotEmpty(t, errs)
	assert.Contains(t, joinedErrors(errs), "request_id")
}

func TestValidateConflictAssessmentResponseRequiresKnownSelectedPosition(t *testing.T) {
	request := conflictAssessmentTestRequest(t)
	unknown := "unknown-position"
	errs := ValidateConflictAssessmentResponse(request, ConflictAssessmentResponse{
		Decision:   ConflictAssessmentDecisionSelect,
		PositionID: &unknown,
		Confidence: 0.9,
		Rationale:  "the dossier is clear",
	})
	require.Len(t, errs, 1)
	assert.Equal(t, "position_id", errs[0].Field)
}

func TestConflictAssessmentResponseSchemaIsClosed(t *testing.T) {
	schema, err := json.Marshal(ConflictAssessmentResponseSchema())
	require.NoError(t, err)
	assert.Contains(t, string(schema), `"additionalProperties":false`)
}

func TestPrepareConflictAssessmentRequestNormalizesAndBoundsDossier(t *testing.T) {
	localTime := time.Date(2026, 7, 31, 12, 0, 0, 0, time.FixedZone("test", -7*60*60))
	prepared, errs := PrepareConflictAssessmentRequest(ConflictAssessmentRequest{
		RequestID: " request-1 ",
		CaseID:    " case-1 ",
		Version:   1,
		Question:  " Which system is current? ",
		Positions: []ConflictAssessmentPosition{
			{PositionID: " position-b ", PositionKey: " entity:b ", SupporterCount: 1},
			{PositionID: " position-a ", PositionKey: " entity:a ", SupporterCount: 1},
		},
		Evidence: []ConflictAssessmentEvidence{
			{EvidenceID: " evidence-b ", PositionID: " position-b ", SupportID: " support-b ", SupporterRef: " supporter-b ", Authority: " primary ", AcceptedAt: localTime, EffectiveAt: &localTime, Content: " B is current. "},
			{EvidenceID: " evidence-a ", PositionID: " position-a ", SupportID: " support-a ", SupporterRef: " supporter-a ", Authority: " authoritative ", AcceptedAt: localTime.Add(-time.Hour), Content: " A is current. "},
		},
	}, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	assert.Equal(t, "request-1", prepared.RequestID)
	assert.Equal(t, "case-1", prepared.CaseID)
	assert.Equal(t, "Which system is current?", prepared.Question)
	assert.Equal(t, []string{"position-a", "position-b"}, []string{prepared.Positions[0].PositionID, prepared.Positions[1].PositionID})
	assert.Equal(t, []string{"evidence-a", "evidence-b"}, []string{prepared.Evidence[0].EvidenceID, prepared.Evidence[1].EvidenceID})
	assert.Equal(t, "primary", prepared.Evidence[1].Authority)
	require.NotNil(t, prepared.Evidence[1].EffectiveAt)
	assert.Equal(t, localTime.UTC(), *prepared.Evidence[1].EffectiveAt)

	limits := DefaultSemanticAssessmentLimits()
	limits.MaxInputTokens = 1
	_, errs = PrepareConflictAssessmentRequest(prepared, limits)
	require.Len(t, errs, 1)
	assert.Equal(t, "request", errs[0].Field)
	assert.Equal(t, "exceeds input token budget", errs[0].Message)
}

func TestPrepareConflictAssessmentRequestRejectsInvalidPositionAndEvidenceFields(t *testing.T) {
	request := conflictAssessmentTestRequest(t)
	request.Positions = []ConflictAssessmentPosition{
		{},
		{PositionID: "duplicate", PositionKey: ""},
		{PositionID: " duplicate ", PositionKey: "same", SupporterCount: -1},
	}
	request.Evidence = []ConflictAssessmentEvidence{
		{},
		{
			EvidenceID:   "evidence-unknown",
			PositionID:   "unknown",
			SupportID:    "support-unknown",
			SupporterRef: "supporter-unknown",
			AcceptedAt:   time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
			Content:      strings.Repeat("x", ConflictAssessmentMaxContent+1),
		},
	}

	_, errs := PrepareConflictAssessmentRequest(request, DefaultSemanticAssessmentLimits())
	summary := joinedErrors(errs)
	assert.Contains(t, summary, "positions[0].position_id")
	assert.Contains(t, summary, "positions[1].position_key")
	assert.Contains(t, summary, "positions[2].position_id")
	assert.Contains(t, summary, "supporter_count must not be negative")
	assert.Contains(t, summary, "evidence[0]")
	assert.Contains(t, summary, "evidence[0].accepted_at")
	assert.Contains(t, summary, "evidence[1].position_id")
	assert.Contains(t, summary, "evidence[1].content")
}

func TestPrepareConflictAssessmentRequestRejectsMissingSupporterRef(t *testing.T) {
	request := conflictAssessmentTestRequest(t)
	request.Evidence[0].SupporterRef = "  "

	_, errs := PrepareConflictAssessmentRequest(request, DefaultSemanticAssessmentLimits())
	require.NotEmpty(t, errs)
	summary := joinedErrors(errs)
	assert.Contains(t, summary, "evidence[0]")
	assert.Contains(t, summary, "supporter_ref")
}

func TestConflictAssessmentResponseValidationRejectsInvalidDecisionShapes(t *testing.T) {
	request := conflictAssessmentTestRequest(t)
	position := "position-a"
	testCases := []struct {
		name     string
		response ConflictAssessmentResponse
		field    string
	}{
		{
			name:     "select requires position",
			response: ConflictAssessmentResponse{Decision: ConflictAssessmentDecisionSelect, Confidence: 0.8, Rationale: "reason"},
			field:    "position_id",
		},
		{
			name:     "select confidence is bounded",
			response: ConflictAssessmentResponse{Decision: ConflictAssessmentDecisionSelect, PositionID: &position, Confidence: 1.1, Rationale: "reason"},
			field:    "confidence",
		},
		{
			name:     "abstain has no position or confidence",
			response: ConflictAssessmentResponse{Decision: ConflictAssessmentDecisionAbstain, PositionID: &position, Confidence: 0.1, Rationale: "reason"},
			field:    "position_id",
		},
		{
			name:     "decision is closed and rationale required",
			response: ConflictAssessmentResponse{Decision: "defer"},
			field:    "decision",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			errs := ValidateConflictAssessmentResponse(request, testCase.response)
			require.NotEmpty(t, errs)
			assert.Contains(t, joinedErrors(errs), testCase.field)
		})
	}
}

func TestConflictAssessmentResponseValidationCountsUnicodeRationaleRunes(t *testing.T) {
	request := conflictAssessmentTestRequest(t)
	valid := ConflictAssessmentResponse{
		Decision:   ConflictAssessmentDecisionAbstain,
		Confidence: 0,
		Rationale:  strings.Repeat("é", ConflictAssessmentMaxRationale),
	}
	assert.Empty(t, ValidateConflictAssessmentResponse(request, valid))

	valid.Rationale += "é"
	assert.Contains(t, joinedErrors(ValidateConflictAssessmentResponse(request, valid)), "rationale")
}

func TestDecodeConflictAssessmentResponseJSONRejectsInvalidPayloads(t *testing.T) {
	limits := DefaultSemanticAssessmentLimits()
	for _, payload := range []string{
		"not-json",
		`{"decision":"abstain","position_id":null,"rationale":"missing confidence"}`,
		`{"decision":"abstain","position_id":null,"confidence":0,"rationale":"extra field","unexpected":true}`,
		`{"decision":"abstain","decision":"select","position_id":null,"confidence":0,"rationale":"duplicate decision"}`,
		`{"decision":null,"position_id":null,"confidence":0,"rationale":"null decision"}`,
		`{"decision":"abstain","position_id":null,"confidence":0,"rationale":"trailing"} {}`,
	} {
		_, err := DecodeConflictAssessmentResponseJSON([]byte(payload), limits)
		require.Error(t, err, payload)
	}

	limits.MaxOutputTokens = 1
	_, err := DecodeConflictAssessmentResponseJSON([]byte(`{"decision":"abstain","position_id":null,"confidence":0,"rationale":"over budget"}`), limits)
	require.ErrorContains(t, err, "token limit")
}

func TestDecodeConflictAssessmentResponseRequiresPresentNullableFields(t *testing.T) {
	_, err := DecodeConflictAssessmentResponseJSON(
		[]byte(`{"decision":"abstain","confidence":0,"rationale":"position omitted"}`),
		DefaultSemanticAssessmentLimits(),
	)
	require.ErrorContains(t, err, "position_id: is required")
}

func conflictAssessmentTestRequest(t *testing.T) ConflictAssessmentRequest {
	t.Helper()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	request, errs := PrepareConflictAssessmentRequest(ConflictAssessmentRequest{
		RequestID: "request-1",
		CaseID:    "case-1",
		Version:   1,
		Question:  "Which system is current?",
		Positions: []ConflictAssessmentPosition{
			{PositionID: "position-a", PositionKey: "entity:a", SupporterCount: 1},
			{PositionID: "position-b", PositionKey: "entity:b", SupporterCount: 1},
		},
		Evidence: []ConflictAssessmentEvidence{
			{EvidenceID: "evidence-a", PositionID: "position-a", SupportID: "support-a", SupporterRef: "supporter-a", Authority: "primary", AcceptedAt: now, Content: "The current system is A."},
			{EvidenceID: "evidence-b", PositionID: "position-b", SupportID: "support-b", SupporterRef: "supporter-b", Authority: "primary", AcceptedAt: now, Content: "The current system is B."},
		},
	}, DefaultSemanticAssessmentLimits())
	require.Empty(t, errs)
	return request
}
