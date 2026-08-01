package memoryservice

import "github.com/markhuangai/dense-mem/internal/verifier"

func submissionRelationshipObjectMatches(
	expected submissionAssessmentRelationshipProposal,
	result verifier.SemanticAssessmentRelationshipResult,
) bool {
	if expected.ObjectRef != "" {
		return result.ObjectRef != nil && *result.ObjectRef == expected.ObjectRef && result.ObjectValue == nil
	}
	if result.ObjectRef != nil || result.ObjectValue == nil || expected.ObjectValue == nil {
		return false
	}
	expectedValue, ok := submissionProposalObjectValue(expected.ObjectValue)
	if !ok {
		return false
	}
	return result.ObjectValue.ValueType == expectedValue.ValueType &&
		result.ObjectValue.CanonicalValue == expectedValue.CanonicalValue &&
		submissionOptionalValueMatches(expectedValue.Display, result.ObjectValue.Display) &&
		submissionOptionalValueMatches(expectedValue.Unit, result.ObjectValue.Unit)
}

func submissionOptionalValueMatches(expected, actual *string) bool {
	if expected == nil || actual == nil {
		return expected == nil && actual == nil
	}
	return *expected == *actual
}
