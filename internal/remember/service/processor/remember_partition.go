package processor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func (p *rememberSynchronousProcessor) partitionRemember(ctx context.Context, input remember.RememberProcessRequest, attemptID string, settings *domain.DecisionRuntimeConfig) (remember.RememberProcessRequest, *remember.RememberIntakeSecurity, error) {
	ctx, cancel := remember.ContextForPhase(ctx, remember.RememberPhaseAssessment)
	defer cancel()
	ctx = requestctx.WithProgressOperation(ctx, "partition")
	ctx = observability.WithMetricIdentity(ctx, input.TeamID, input.OwnerProfileID)
	ctx = observability.WithAIOperation(ctx, observability.AIOperationSemanticAssessment, len(input.Evidence))
	transport := p.provider
	if transport == nil {
		return input, nil, fmt.Errorf("%w: partition transport is unavailable", remember.ErrRememberProviderUnavailable)
	}
	remember.ReportProgress(ctx, remember.ProgressPreparingBoundaries)
	contents := make([]string, len(input.Evidence))
	for i, item := range input.Evidence {
		contents[i] = item.Content
	}
	reviewSecurity := !input.SkipAISecurityReview && (p.decisions == nil || settings == nil || !settings.Effective.SecurityReview)
	req := assessor.PreparePartitionRequest("partition:"+attemptID, contents, reviewSecurity)
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
	conversation := modelprovider.StructuredRequest{
		Model: p.provider.ModelName(), SchemaName: assessor.PartitionSchemaName,
		Temperature:    modelprovider.StructuredTemperature(limits.ProviderTemperatureDisabled),
		MaxInputTokens: limits.MaxInputTokens, MaxOutputTokens: outputLimit,
	}
	prepare := func() error {
		prompt := assessor.PartitionPrompt
		if req.ReviewSecurity {
			prompt = assessor.PartitionSecurityPrompt + prompt
		}
		encoded, err := json.Marshal(req)
		if err != nil {
			return err
		}
		conversation.Schema = assessor.PartitionResponseSchema(req.ReviewSecurity)
		conversation.Messages = []modelprovider.Message{{Role: "system", Content: prompt}, {Role: "user", Content: string(encoded)}}
		return nil
	}
	countTokens := func(turn int) (int, error) {
		tokens, countErr := modelprovider.CountStructuredRequestTokens(conversation, limits.Tokenizer)
		if countErr != nil {
			return 0, fmt.Errorf("%w: partition request sizing failed: %w", remember.ErrRememberInputBudgetExceeded, countErr)
		}
		// Reserve every remaining complete response and bounded correction instruction before dispatch.
		inputLimit := limits.MaxInputTokens - (assessor.SemanticAssessmentMaxProviderTurns-turn)*(outputLimit+4096)
		if tokens > inputLimit {
			return 0, fmt.Errorf("%w: partition conversation requires %d tokens, limit %d", remember.ErrRememberInputBudgetExceeded, tokens, inputLimit)
		}
		return tokens, nil
	}
	if err := prepare(); err != nil {
		return input, nil, err
	}
	tokens, err := countTokens(1)
	if err != nil {
		return input, nil, err
	}
	review := &remember.RememberIntakeSecurity{DecisionSnapshot: settings, Skipped: input.SkipAISecurityReview}
	if !input.SkipAISecurityReview {
		routing, err := remember.RouteRememberIntakeSecurity(requestctx.WithProgressOperation(ctx, "intake_security"), p.decisions, settings, req, p.logger)
		if err != nil {
			return input, nil, err
		}
		if routing != nil {
			review.DecisionStages = routing.Stages
		}
		if !req.ReviewSecurity && (routing == nil || routing.Security == nil) {
			req = assessor.PreparePartitionRequest(req.RequestID, contents, true)
			if err := prepare(); err != nil {
				return input, nil, err
			}
			tokens, err = countTokens(1)
			if err != nil {
				return input, nil, err
			}
		}
	}
	if root, ok := p.logger.(interface{ ContextSlog() *slog.Logger }); ok {
		observability.LogDiagnosticBody(ctx, root.ContextSlog(), p.protector, "remember_partition_manifest", []byte(conversation.Messages[1].Content), modelprovider.MaxProviderDiagnosticBodyBytes, slog.String("attempt_id", attemptID), slog.String("policy_version", knowledge.RememberPartitionPolicyVersion))
	}
	started := time.Now()
	for turn := 1; turn <= assessor.SemanticAssessmentMaxProviderTurns; turn++ {
		stage := remember.ProgressPartitioning
		if turn > 1 {
			stage = remember.ProgressRegeneratingPartitions
			tokens, err = countTokens(turn)
			if err != nil {
				return input, nil, err
			}
		}
		requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: string(stage), Attempt: turn, Total: assessor.SemanticAssessmentMaxProviderTurns})
		result, callErr := transport.Complete(ctx, conversation)
		if callErr != nil {
			return input, nil, fmt.Errorf("%w: partition provider failed: %w", remember.ErrRememberProviderUnavailable, callErr)
		}
		if result.PromptTokens > limits.MaxInputTokens {
			return input, nil, fmt.Errorf("%w: partition provider reported input overflow", remember.ErrRememberInputBudgetExceeded)
		}
		if err := ctx.Err(); err != nil {
			return input, nil, err
		}
		remember.ReportProgress(ctx, remember.ProgressValidatingPartitions)
		responseTokens, countErr := modelprovider.CountTokens(result.Content, limits.Tokenizer)
		var spans [][][2]int
		var validationErr error
		if countErr != nil || responseTokens > outputLimit || result.CompletionTokens > outputLimit {
			validationErr = fmt.Errorf("partition response exceeds output bound")
		} else {
			spans, validationErr = assessor.ValidatePartitionResponse(req, []byte(result.Content))
		}
		var rejected *assessor.PartitionSecurityRejection
		if errors.As(validationErr, &rejected) {
			return input, nil, remember.IntakeSecurityRejectionFailure(req, rejected)
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
			return output, review, nil
		}
		if turn < assessor.SemanticAssessmentMaxProviderTurns {
			// Oversized output is explicitly omitted, never truncated into a misleading partial response.
			if responseTokens <= outputLimit && len(result.Content) <= modelprovider.MaxProviderDiagnosticBodyBytes {
				conversation.Messages = append(conversation.Messages, modelprovider.Message{Role: "assistant", Content: result.Content})
			}
			feedback, _ := json.Marshal(map[string]any{"validation_error": validationErr.Error(), "instruction": "Return a complete replacement for the unchanged original request."})
			conversation.Messages = append(conversation.Messages, modelprovider.Message{Role: "user", Content: string(feedback)})
		}
	}
	return input, nil, fmt.Errorf("%w: partition response remained invalid after three complete attempts", remember.ErrRememberProviderResponseInvalid)
}
