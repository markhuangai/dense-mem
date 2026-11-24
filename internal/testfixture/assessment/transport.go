// Package assessment supplies fixtures for the structured provider boundary.
package assessment

import (
	"encoding/json"
	"errors"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

func Request(call modelprovider.StructuredRequest) (assessor.SemanticAssessmentRequest, error) {
	var request assessor.SemanticAssessmentRequest
	if len(call.Messages) < 2 {
		return request, errors.New("fixture received no assessment payload")
	}
	err := json.Unmarshal([]byte(call.Messages[1].Content), &request)
	for i, evidence := range request.Evidence {
		request.Evidence[i] = assessor.PrepareSemanticAssessmentEvidence(evidence)
	}
	for i, evidence := range request.KnownEvidence {
		request.KnownEvidence[i] = assessor.PrepareSemanticAssessmentEvidence(evidence)
	}
	return request, err
}

func Result(call modelprovider.StructuredRequest, reply assessor.SemanticAssessmentResponse) (modelprovider.StructuredResult, error) {
	encoded, err := json.Marshal(reply)
	if err != nil {
		return modelprovider.StructuredResult{}, err
	}
	if properties, ok := call.Schema["properties"].(map[string]any); ok {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &object); err != nil {
			return modelprovider.StructuredResult{}, err
		}
		for _, field := range []string{"evidence_security_results", "evidence_equivalence_results"} {
			if _, sent := properties[field]; !sent {
				delete(object, field)
			}
		}
		encoded, err = json.Marshal(object)
	}
	return modelprovider.StructuredResult{Content: string(encoded)}, err
}
