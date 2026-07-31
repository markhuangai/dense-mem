package verifier

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/observability"
)

func (v *OpenAIVerifier) recordVerifierProviderUsage(ctx context.Context, model string, usage *openAIVerifierUsage) {
	if usage == nil {
		return
	}
	observability.RecordVerifierTokens(ctx, v.metrics, model, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	observability.RecordAIOperationUsage(ctx, v.metrics, observability.AIOperationUsage{
		Component:    observability.AIComponentVerifier,
		Model:        model,
		InputTokens:  usage.PromptTokens,
		OutputTokens: usage.CompletionTokens,
		Source:       observability.AITokenSourceProvider,
	})
}

func (v *OpenAIVerifier) recordVerifierMissingUsage(ctx context.Context, model string) {
	observability.RecordAIOperationUnpriced(ctx, v.metrics, observability.AIComponentVerifier, model, "missing_usage")
}

func (v *OpenAIVerifier) recordVerifierTokenizerUsage(ctx context.Context, model string, messages []openAIVerifierMessage, content string) {
	if !observability.HasAIOperation(ctx) {
		return
	}
	inputTokens, err := semanticAssessmentMessageTokens(messages, v.assessmentLimits.Tokenizer)
	if err != nil {
		observability.RecordAIOperationUnpriced(ctx, v.metrics, observability.AIComponentVerifier, model, "tokenizer_error")
		return
	}
	outputTokens, err := CountTokens(content, v.assessmentLimits.Tokenizer)
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
