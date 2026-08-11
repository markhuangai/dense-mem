package memoryservice

import "github.com/markhuangai/dense-mem/internal/domain"

func appendEvidenceVectorFailureDegradation(result *RecallResult, searchState string) {
	if result == nil || searchState != string(domain.SearchProjectionFailed) {
		return
	}
	result.Degradations = append(result.Degradations, RecallDegradationResult{
		Frontier: "evidence",
		Optional: true,
		Code:     "evidence_vector_failed",
		Message:  "Some evidence vectors are unavailable; lexical recall remains available. Check the control portal for recovery guidance.",
	})
	result.Degradation = &result.Degradations[len(result.Degradations)-1]
}
