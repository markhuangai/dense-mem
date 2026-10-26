package modelprovider

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

const DecisionPolicyRevision = "typed-decisions-v1"

func ValidateDecisionResult(request DecisionRequest, result DecisionResult) error {
	questions := make([]DecisionQuestion, 0)
	for _, group := range request.Groups {
		questions = append(questions, group.Questions...)
	}
	if err := ValidateDecisionAnswers(questions, result.Answers); err != nil {
		return &MalformedResponseError{Provider: "decision", Message: err.Error(), FailureClass: ProviderFailureClassProtocol, ValidationStage: "task_complete_response"}
	}
	for _, answer := range result.Answers {
		if answer.Type == DecisionRefusal {
			return &ProviderError{Provider: "decision", Message: "decision task contains a refusal", FailureClass: "refusal"}
		}
	}
	return nil
}

func DecisionInputHash(input any) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}
