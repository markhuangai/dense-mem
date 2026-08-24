package assessorprovider

import "strings"

import "github.com/markhuangai/dense-mem/internal/assessor"

const (
	maxOpenAIValidationSummaryErrors = 100
	maxOpenAIValidationSummaryRunes  = 2_048
	openAIValidationSummaryOmission  = "additional validation errors were omitted"
)

func openAIValidationSummary(errs []assessor.SemanticValidationError) string {
	parts := make([]string, 0, min(len(errs), maxOpenAIValidationSummaryErrors))
	for index, err := range errs {
		if index == maxOpenAIValidationSummaryErrors {
			parts = append(parts, openAIValidationSummaryOmission)
			break
		}
		parts = append(parts, err.Error())
	}
	summary := strings.Join(parts, "; ")
	if len([]rune(summary)) <= maxOpenAIValidationSummaryRunes {
		return summary
	}
	prefixRunes := maxOpenAIValidationSummaryRunes - len([]rune(openAIValidationSummaryOmission)) - len([]rune("; "))
	return string([]rune(summary)[:prefixRunes]) + "; " + openAIValidationSummaryOmission
}
