package assessorprovider

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/observability"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

type openAIStructuredChatResult struct {
	Content string
	// Usage is populated only when both token sides can be priced.
	Usage *openAIVerifierUsage
	// ReportedUsage retains raw provider values for token-budget enforcement.
	ReportedUsage *openAIVerifierUsage
}

func openAIVerifierUsageSupportsPricing(usage *openAIVerifierUsage) bool {
	return usage != nil && usage.PromptTokens > 0 && usage.CompletionTokens > 0
}

func (v *OpenAIAssessor) recordVerifierProviderUsage(ctx context.Context, model string, usage *openAIVerifierUsage) {
	if usage == nil {
		return
	}
	observability.RecordVerifierTokens(ctx, v.metrics, model, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	if !openAIVerifierUsageSupportsPricing(usage) {
		return
	}
	observability.RecordAIOperationUsage(ctx, v.metrics, observability.AIOperationUsage{
		Component:    observability.AIComponentVerifier,
		Model:        model,
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		Source:       observability.AITokenSourceProvider,
	})
}

func (v *OpenAIAssessor) recordVerifierMissingUsage(ctx context.Context, model string) {
	observability.RecordAIOperationUnpriced(ctx, v.metrics, observability.AIComponentVerifier, model, "missing_usage")
}

func (v *OpenAIAssessor) recordVerifierTokenizerUsage(ctx context.Context, model string, requestJSON []byte, content string) {
	if !observability.HasAIOperation(ctx) {
		return
	}
	inputTokens, err := assessor.CountTokens(string(requestJSON), v.assessmentLimits.Tokenizer)
	if err != nil {
		observability.RecordAIOperationUnpriced(ctx, v.metrics, observability.AIComponentVerifier, model, "tokenizer_error")
		return
	}
	outputTokens, err := assessor.CountTokens(content, v.assessmentLimits.Tokenizer)
	if err != nil {
		observability.RecordAIOperationUnpriced(ctx, v.metrics, observability.AIComponentVerifier, model, "tokenizer_error")
		return
	}
	observability.RecordAIOperationUsage(ctx, v.metrics, observability.AIOperationUsage{
		Component:    observability.AIComponentVerifier,
		Model:        model,
		InputTokens:  int64(inputTokens),
		OutputTokens: int64(outputTokens),
		Source:       observability.AITokenSourceTokenizer,
	})
}
