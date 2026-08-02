package verifier

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSubmissionAssessmentRequiresOneBoundedSecurityVerdictPerEvidence(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	response := submissionAssessmentTestResponse()
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	decoded, err := DecodeSubmissionAssessmentResponseJSON(raw, limits)
	if err != nil {
		t.Fatalf("DecodeSubmissionAssessmentResponseJSON() error = %v", err)
	}
	validated, errs := PrepareSubmissionAssessmentResponse(prepared, decoded, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSubmissionAssessmentResponse() errors = %#v", errs)
	}
	if validated.HasSecurityConcern() {
		t.Fatal("valid no_concern response unexpectedly has a security concern")
	}
	if validated.OutputTokens <= 0 {
		t.Fatalf("OutputTokens = %d, want positive", validated.OutputTokens)
	}

	response.SecurityAssessments = []SubmissionSecurityAssessment{}
	if _, errs := PrepareSubmissionAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "exactly one assessment") {
		t.Fatalf("PrepareSubmissionAssessmentResponse() errors = %#v, want complete assessment rejection", errs)
	}

	response = submissionAssessmentTestResponse()
	response.SecurityAssessments[0].Verdict = "concern"
	response.SecurityAssessments[0].Signals = nil
	if _, errs := PrepareSubmissionAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "must identify at least one concern") {
		t.Fatalf("PrepareSubmissionAssessmentResponse() errors = %#v, want concern signal rejection", errs)
	}
}

func TestSubmissionAssessmentRejectsUnknownAndCrossEvidenceSecuritySignals(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}
	response := submissionAssessmentTestResponse()
	response.SecurityAssessments[0].Verdict = "concern"
	response.SecurityAssessments[0].Signals = []SemanticSecuritySignal{{
		EvidenceID: "unknown",
		Kind:       "instruction_override",
		Start:      0,
		End:        4,
	}}
	if _, errs := PrepareSubmissionAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "must match its assessment evidence_id") {
		t.Fatalf("PrepareSubmissionAssessmentResponse() errors = %#v, want cross-evidence rejection", errs)
	}
}

func TestSubmissionAssessmentSecurityConcernDiscardsSemanticOutput(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	req.RequiredSubmissionProposal = submissionAssessmentTestRequiredProposal()
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}

	response := submissionAssessmentTestResponse()
	response.SecurityAssessments[0] = SubmissionSecurityAssessment{
		EvidenceID: "ev-1",
		Verdict:    "concern",
		Signals: []SemanticSecuritySignal{{
			EvidenceID: "ev-1",
			Kind:       "tool_exfiltration",
			Start:      0,
			End:        4,
		}},
		Justification: "The evidence contains an active request to use a tool for data exfiltration.",
	}
	response.RelationshipResults[0].PredicateStatus = "needs_review"
	response.RelationshipResults[0].PredicateKey = nil
	response.RelationshipResults[0].PredicateVersion = nil
	response.RelationshipResults[0].PredicateCandidate = &SubmissionPredicateCandidate{
		PredicateKey:     "works_on",
		RelationshipKind: "state",
	}

	validated, validationErrors := PrepareSubmissionAssessmentResponse(prepared, response, limits)
	if len(validationErrors) != 0 {
		t.Fatalf("PrepareSubmissionAssessmentResponse() errors = %#v", validationErrors)
	}
	if !validated.HasSecurityConcern() {
		t.Fatal("validated concern response unexpectedly has no security concern")
	}
	if len(validated.EntityResults) != 0 || len(validated.RelationshipResults) != 0 {
		t.Fatalf("validated concern response retained semantic output: %#v", validated)
	}
	if validated.OutputTokens <= 0 {
		t.Fatalf("OutputTokens = %d, want positive", validated.OutputTokens)
	}
}

func TestSubmissionAssessmentSecurityConcernStillRequiresSecurityEnvelope(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}

	validConcern := func() SubmissionAssessmentResponse {
		response := submissionAssessmentTestResponse()
		response.SecurityAssessments[0] = SubmissionSecurityAssessment{
			EvidenceID: "ev-1",
			Verdict:    "concern",
			Signals: []SemanticSecuritySignal{{
				EvidenceID: "ev-1",
				Kind:       "tool_exfiltration",
				Start:      0,
				End:        4,
			}},
			Justification: "The evidence contains an active tool-exfiltration instruction.",
		}
		return response
	}

	response := validConcern()
	response.RequestID = "different-request"
	if _, validationErrors := PrepareSubmissionAssessmentResponse(prepared, response, limits); len(validationErrors) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(validationErrors), "expected") {
		t.Fatalf("PrepareSubmissionAssessmentResponse() errors = %#v, want request binding rejection", validationErrors)
	}

	response = validConcern()
	response.SecurityAssessments[0].Signals[0].End = 100
	if _, validationErrors := PrepareSubmissionAssessmentResponse(prepared, response, limits); len(validationErrors) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(validationErrors), "span") {
		t.Fatalf("PrepareSubmissionAssessmentResponse() errors = %#v, want exact signal span rejection", validationErrors)
	}
}

func TestSubmissionAssessmentRequiresExactClientProposalCorrespondence(t *testing.T) {
	req, limits := semanticAssessmentTestRequest(t)
	req.RequiredSubmissionProposal = submissionAssessmentTestRequiredProposal()
	prepared, errs := PrepareSemanticAssessmentRequest(req, limits)
	if len(errs) != 0 {
		t.Fatalf("PrepareSemanticAssessmentRequest() errors = %#v", errs)
	}

	response := submissionAssessmentTestResponse()
	if _, errs := PrepareSubmissionAssessmentResponse(prepared, response, limits); len(errs) == 0 || !strings.Contains(semanticAssessmentJoinedErrors(errs), "must retain a submitted entity ref") {
		t.Fatalf("PrepareSubmissionAssessmentResponse() errors = %#v, want client proposal correspondence rejection", errs)
	}

	response.EntityResults[0].Ref = "subject"
	response.EntityResults[1].Ref = "object"
	response.RelationshipResults[0].SubjectRef = "subject"
	response.RelationshipResults[0].ObjectRef = stringPointer("object")
	if _, errs := PrepareSubmissionAssessmentResponse(prepared, response, limits); len(errs) != 0 {
		t.Fatalf("PrepareSubmissionAssessmentResponse() errors = %#v", errs)
	}
}

func TestSubmissionAssessmentResponseSchemaIsClosed(t *testing.T) {
	schema := SubmissionAssessmentResponseSchema()
	assertClosedProviderObjects(t, schema, "submission assessment response")
	assertOpenAIStrictSchemaSubset(t, schema, "submission assessment response")
}

func submissionAssessmentTestResponse() SubmissionAssessmentResponse {
	semantic := semanticAssessmentTestResponse()
	return SubmissionAssessmentResponse{
		RequestID: semantic.RequestID,
		SecurityAssessments: []SubmissionSecurityAssessment{{
			EvidenceID:    "ev-1",
			Verdict:       "no_concern",
			Signals:       []SemanticSecuritySignal{},
			Justification: "No active prompt-injection or exfiltration instruction is present.",
		}},
		EntityResults: semantic.EntityResults,
		RelationshipResults: func() []SubmissionAssessmentRelationshipResult {
			results := make([]SubmissionAssessmentRelationshipResult, 0, len(semantic.RelationshipResults))
			for _, relationship := range semantic.RelationshipResults {
				results = append(results, SubmissionAssessmentRelationshipResult{SemanticAssessmentRelationshipResult: relationship})
			}
			return results
		}(),
	}
}

func submissionAssessmentTestRequiredProposal() *SubmissionAssessmentRequiredProposal {
	return &SubmissionAssessmentRequiredProposal{
		Entities: []SubmissionAssessmentRequiredEntity{
			{Ref: "subject", Surface: "Mark", EvidenceID: "ev-1", Start: 0, End: 4},
			{Ref: "object", Surface: "Dense-Mem", EvidenceID: "ev-1", Start: 14, End: 23},
		},
		Relationships: []SubmissionAssessmentRequiredRelationship{{
			ProposalID:          "relationship-1",
			SubjectRef:          "subject",
			OriginalPredicate:   "works on",
			PredicateEvidenceID: "ev-1",
			PredicateStart:      5,
			PredicateEnd:        13,
			ObjectRef:           "object",
			Polarity:            "+",
			Modality:            "statement",
			Evidence:            []SemanticAssessmentEvidenceSpan{{EvidenceID: "ev-1", Start: 0, End: 24}},
		}},
	}
}
