package processor

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func (p *rememberSynchronousProcessor) partitionRemember(ctx context.Context, input remember.RememberProcessRequest, attemptID string) (remember.RememberProcessRequest, error) {
	ctx, cancel := remember.ContextForPhase(ctx, remember.RememberPhaseAssessment)
	defer cancel()
	ctx = requestctx.WithProgressOperation(ctx, "partition")
	ctx = observability.WithMetricIdentity(ctx, input.TeamID, input.OwnerProfileID)
	ctx = observability.WithAIOperation(ctx, observability.AIOperationSemanticAssessment, len(input.Evidence))
	transport, ok := p.provider.(modelprovider.StructuredTransport)
	if !ok {
		return input, fmt.Errorf("%w: partition transport is unavailable", remember.ErrRememberProviderUnavailable)
	}
	remember.ReportProgress(ctx, remember.ProgressPreparingBoundaries)
	contents := make([]string, len(input.Evidence))
	for i, item := range input.Evidence {
		contents[i] = item.Content
	}
	req := assessor.PreparePartitionRequest("partition:"+attemptID, contents)
	encoded, err := json.Marshal(req)
	if err != nil {
		return input, err
	}
	limits := p.limits
	defaults := assessor.DefaultSemanticAssessmentLimits()
	if limits.Tokenizer == "" {
		limits.Tokenizer = defaults.Tokenizer
	}
	if limits.MaxInputTokens <= 0 {
		limits.MaxInputTokens = defaults.MaxInputTokens
	}
	if limits.MaxOutputTokens <= 0 {
		limits.MaxOutputTokens = defaults.MaxOutputTokens
	}
	outputLimit := min(limits.MaxOutputTokens, 8192)
	messages := []modelprovider.Message{{Role: "system", Content: assessor.PartitionPrompt}, {Role: "user", Content: string(encoded)}}
	if root, ok := p.logger.(interface{ ContextSlog() *slog.Logger }); ok {
		observability.LogDiagnosticBody(ctx, root.ContextSlog(), p.protector, "remember_partition_manifest", encoded, modelprovider.MaxProviderDiagnosticBodyBytes, slog.String("attempt_id", attemptID), slog.String("policy_version", knowledge.RememberPartitionPolicyVersion))
	}
	started := time.Now()
	for turn := 1; turn <= assessor.SemanticAssessmentMaxProviderTurns; turn++ {
		converted := make([]assessor.SemanticAssessmentProviderMessage, len(messages))
		for i, m := range messages {
			converted[i] = assessor.SemanticAssessmentProviderMessage{Role: m.Role, Content: m.Content}
		}
		tokens, countErr := assessor.CountSemanticAssessmentProviderRequestTokens(p.provider.ModelName(), assessor.PartitionSchemaName, assessor.PartitionResponseSchema(), limits.ProviderTemperatureDisabled, converted, limits.Tokenizer)
		if countErr != nil {
			return input, fmt.Errorf("%w: partition request sizing failed: %w", remember.ErrRememberInputBudgetExceeded, countErr)
		}
		// Reserve every remaining complete response and bounded correction instruction before dispatch.
		inputLimit := limits.MaxInputTokens - (assessor.SemanticAssessmentMaxProviderTurns-turn)*(outputLimit+4096)
		if tokens > inputLimit {
			return input, fmt.Errorf("%w: partition conversation requires %d tokens, limit %d", remember.ErrRememberInputBudgetExceeded, tokens, inputLimit)
		}
		stage := remember.ProgressPartitioning
		if turn > 1 {
			stage = remember.ProgressRegeneratingPartitions
		}
		requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: string(stage), Attempt: turn, Total: assessor.SemanticAssessmentMaxProviderTurns})
		result, callErr := transport.Complete(ctx, modelprovider.StructuredRequest{Model: p.provider.ModelName(), Messages: messages, SchemaName: assessor.PartitionSchemaName, Schema: assessor.PartitionResponseSchema(), MaxInputTokens: limits.MaxInputTokens, MaxOutputTokens: outputLimit})
		if callErr != nil {
			return input, fmt.Errorf("%w: partition provider failed: %w", remember.ErrRememberProviderUnavailable, callErr)
		}
		if result.PromptTokens > limits.MaxInputTokens {
			return input, fmt.Errorf("%w: partition provider reported input overflow", remember.ErrRememberInputBudgetExceeded)
		}
		remember.ReportProgress(ctx, remember.ProgressValidatingPartitions)
		responseTokens, countErr := assessor.CountTokens(result.Content, limits.Tokenizer)
		var spans [][][2]int
		var validationErr error
		if countErr != nil || responseTokens > outputLimit || result.CompletionTokens > outputLimit {
			validationErr = fmt.Errorf("partition response exceeds output bound")
		} else {
			spans, validationErr = assessor.ValidatePartitionResponse(req, []byte(result.Content))
		}
		if logger, ok := p.logger.(observability.ContextLogProvider); ok {
			attrs := []observability.LogAttr{observability.String("attempt_id", attemptID), observability.Int("partition_attempt", turn), observability.Int("input_tokens", tokens), observability.Int("output_tokens", responseTokens), observability.String("model", p.provider.ModelName()), observability.String("policy_version", knowledge.RememberPartitionPolicyVersion), {Key: "elapsed_ms", Value: time.Since(started).Milliseconds()}}
			if validationErr != nil {
				logger.WarnContext(ctx, "remember_partition_rejected", append(attrs, observability.String("validation_reason", validationErr.Error()))...)
			} else {
				logger.InfoContext(ctx, "remember_partition_validated", append(attrs, observability.LogAttr{Key: "spans", Value: spans})...)
			}
		}
		if validationErr == nil {
			output := input
			output.Evidence = nil
			output.SubmittedItems = make([]knowledge.RememberSubmittedItem, len(contents))
			for i, ranges := range spans {
				id := uuid.NewString()
				output.SubmittedItems[i] = knowledge.RememberSubmittedItem{ID: id, Index: i, Content: contents[i], ContentHash: knowledge.EvidenceContentHash(contents[i]), PolicyVersion: knowledge.RememberPartitionPolicyVersion}
				runes := []rune(contents[i])
				for j, span := range ranges {
					item := input.Evidence[i]
					item.Content = string(runes[span[0]:span[1]])
					item.ContentHash = knowledge.EvidenceContentHash(item.Content)
					item.Partition = &knowledge.EvidencePartition{SourceItemID: id, SubmittedIndex: i, Index: j, Start: span[0], End: span[1]}
					output.Evidence = append(output.Evidence, item)
				}
			}
			return output, nil
		}
		if turn < assessor.SemanticAssessmentMaxProviderTurns {
			// Oversized output is explicitly omitted, never truncated into a misleading partial response.
			if responseTokens <= outputLimit && len(result.Content) <= modelprovider.MaxProviderDiagnosticBodyBytes {
				messages = append(messages, modelprovider.Message{Role: "assistant", Content: result.Content})
			}
			feedback, _ := json.Marshal(map[string]any{"validation_error": validationErr.Error(), "instruction": "Return a complete replacement for the unchanged original request."})
			messages = append(messages, modelprovider.Message{Role: "user", Content: string(feedback)})
		}
	}
	return input, fmt.Errorf("%w: partition response remained invalid after three complete attempts", remember.ErrRememberProviderResponseInvalid)
}
