package verifier

import (
	"context"
	"errors"
	"strings"

	communityprovider "github.com/markhuangai/dense-mem/internal/community/provider"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

// SummarizeCommunity keeps the OpenAI transport and its bounded error types in
// verifier while Community owns the summary schema and response policy.
func (v *OpenAIVerifier) SummarizeCommunity(ctx context.Context, input domain.CommunitySummaryInput) (domain.CommunitySummary, error) {
	if v == nil {
		return domain.CommunitySummary{}, &ProviderError{Provider: openAIVerifierProvider, Message: "community summary provider is unavailable"}
	}
	if strings.TrimSpace(input.CommunityID) == "" || len(input.Relationships) == 0 {
		return domain.CommunitySummary{}, &ProviderError{Provider: openAIVerifierProvider, Message: "community summary input is empty"}
	}
	ctx = observability.WithAIOperation(ctx, observability.AIOperationCommunitySummary, len(input.Relationships))
	result, err := communityprovider.SummarizeCommunity(ctx, v.model, input, func(
		ctx context.Context,
		model string,
		schemaName string,
		schema map[string]any,
		prompt string,
		payload any,
	) (string, error) {
		return v.openAIStructuredChatJSON(ctx, model, schemaName, schema, prompt, payload)
	})
	if err != nil {
		var malformed *communityprovider.MalformedResponseError
		if errors.As(err, &malformed) {
			return domain.CommunitySummary{}, &MalformedResponseError{
				Provider: openAIVerifierProvider,
				Message:  malformed.Message,
				RawJSON:  malformed.RawJSON,
			}
		}
		return domain.CommunitySummary{}, err
	}
	return result, nil
}

var _ interface {
	ModelName() string
	SummarizeCommunity(context.Context, domain.CommunitySummaryInput) (domain.CommunitySummary, error)
} = (*OpenAIVerifier)(nil)
