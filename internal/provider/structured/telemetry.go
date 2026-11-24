package structured

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func openAIVerifierUsageSupportsPricing(usage *openAIVerifierUsage) bool {
	return usage != nil && usage.PromptTokens > 0 && usage.CompletionTokens > 0
}

func (v *OpenAI) recordVerifierProviderUsage(ctx context.Context, model string, usage *openAIVerifierUsage) {
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

func (v *OpenAI) recordVerifierMissingUsage(ctx context.Context, model string) {
	observability.RecordAIOperationUnpriced(ctx, v.metrics, observability.AIComponentVerifier, model, "missing_usage")
}

func (v *OpenAI) recordVerifierTokenizerUsage(ctx context.Context, model string, requestJSON []byte, content string) {
	if !observability.HasAIOperation(ctx) {
		return
	}
	inputTokens, err := modelprovider.CountTokens(string(requestJSON), v.tokenizer)
	if err != nil {
		observability.RecordAIOperationUnpriced(ctx, v.metrics, observability.AIComponentVerifier, model, "tokenizer_error")
		return
	}
	outputTokens, err := modelprovider.CountTokens(content, v.tokenizer)
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
