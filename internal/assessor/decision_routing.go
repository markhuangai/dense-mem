package assessor

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

type SemanticAssessmentRouting struct {
	Security    []SemanticAssessmentEvidenceSecurityResult
	Equivalence []SemanticAssessmentEvidenceEquivalenceResult
	Stages      []modelprovider.DecisionStage
}

func SemanticSecurityHazards() []string { return semanticSecurityKinds() }

func semanticAssessmentGenerationJSON(response SemanticAssessmentResponse) ([]byte, error) {
	response.DecisionStages = nil
	return json.Marshal(response)
}

func SemanticAssessmentGenerationSchema(request SemanticAssessmentRequest, limits SemanticAssessmentLimits) map[string]any {
	schema := SemanticAssessmentResponseSchemaWithLimits(limits)
	if request.DecisionRouting == nil {
		return schema
	}
	properties := schema["properties"].(map[string]any)
	var required []string
	for _, field := range schema["required"].([]string) {
		if field == "evidence_security_results" && request.DecisionRouting.Security != nil || field == "evidence_equivalence_results" && request.DecisionRouting.Equivalence != nil {
			delete(properties, field)
			continue
		}
		required = append(required, field)
	}
	schema["required"] = required
	return schema
}

func SemanticAssessmentGenerationInstruction(request SemanticAssessmentRequest) string {
	if request.DecisionRouting == nil {
		return ""
	}
	instruction := ""
	if request.DecisionRouting.Security != nil {
		instruction += "\nThe server has completed evidence security review. Do not classify security or return evidence_security_results. Continue evaluating all evidence for the remaining fields."
	}
	if request.DecisionRouting.Equivalence != nil {
		instruction += "\nThe server has completed evidence deduplication. Do not classify equivalence or return evidence_equivalence_results. Candidate evidence remains read-only context for cited conflicts."
	}
	return instruction
}

func DecodeSemanticAssessmentGeneration(raw []byte, request SemanticAssessmentRequest, limits SemanticAssessmentLimits) (SemanticAssessmentResponse, error) {
	if request.DecisionRouting == nil {
		return DecodeSemanticAssessmentResponseJSON(raw, limits)
	}
	if err := jsonstrict.RejectDuplicateFields(raw); err != nil {
		return SemanticAssessmentResponse{}, err
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return SemanticAssessmentResponse{}, fmt.Errorf("generation must return a complete object")
	}
	for _, field := range []struct {
		name     string
		value    any
		supplied bool
	}{
		{"evidence_security_results", request.DecisionRouting.Security, request.DecisionRouting.Security != nil},
		{"evidence_equivalence_results", request.DecisionRouting.Equivalence, request.DecisionRouting.Equivalence != nil},
	} {
		if !field.supplied {
			continue
		}
		if _, exists := object[field.name]; exists {
			return SemanticAssessmentResponse{}, fmt.Errorf("generation repeated server-completed field %s", field.name)
		}
		encoded, err := json.Marshal(field.value)
		if err != nil {
			return SemanticAssessmentResponse{}, err
		}
		object[field.name] = encoded
	}
	assembled, err := json.Marshal(object)
	if err != nil {
		return SemanticAssessmentResponse{}, err
	}
	response, err := DecodeSemanticAssessmentResponseJSON(assembled, limits)
	if err == nil {
		response.DecisionStages = append([]modelprovider.DecisionStage(nil), request.DecisionRouting.Stages...)
	}
	return response, err
}

func SameSemanticAssessmentRouting(left, right *SemanticAssessmentRouting) bool {
	l, lerr := json.Marshal(left)
	r, rerr := json.Marshal(right)
	return lerr == nil && rerr == nil && bytes.Equal(l, r)
}
