package conflictassessment

import (
	"strings"
	"testing"
	"time"
)

func TestConflictAssessmentResponseRejectsUnknownAndDuplicateFields(t *testing.T) {
	_, err := DecodeConflictAssessmentResponseJSON(
		[]byte(`{"decision":"abstain","position_id":null,"confidence":0,"rationale":"no clear answer","extra":true}`),
		DefaultSemanticAssessmentLimits(),
	)
	if err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("DecodeConflictAssessmentResponseJSON() error = %v, want unknown field", err)
	}
	_, err = DecodeConflictAssessmentResponseJSON(
		[]byte(`{"decision":"abstain","position_id":null,"confidence":0,"confidence":0,"rationale":"no clear answer"}`),
		DefaultSemanticAssessmentLimits(),
	)
	if err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("DecodeConflictAssessmentResponseJSON() error = %v, want duplicate field", err)
	}
}

func TestPrepareConflictAssessmentRequestNormalizesOrdering(t *testing.T) {
	accepted := time.Date(2026, 8, 24, 12, 0, 0, 0, time.FixedZone("UTC+2", 2*60*60))
	prepared, errs := PrepareConflictAssessmentRequest(ConflictAssessmentRequest{
		RequestID: "request",
		CaseID:    "case",
		Version:   1,
		Question:  "which position is supported?",
		Positions: []ConflictAssessmentPosition{
			{PositionID: "b", PositionKey: "B"},
			{PositionID: "a", PositionKey: "A"},
		},
		Evidence: []ConflictAssessmentEvidence{
			{EvidenceID: "e", PositionID: "a", SupportID: "s", SupporterRef: "p", Content: "evidence", AcceptedAt: accepted},
		},
	}, DefaultSemanticAssessmentLimits())
	if len(errs) != 0 {
		t.Fatalf("PrepareConflictAssessmentRequest() errors = %#v", errs)
	}
	if prepared.Positions[0].PositionID != "a" || prepared.Evidence[0].AcceptedAt.Location() != time.UTC {
		t.Fatalf("prepared request = %#v", prepared)
	}
}
