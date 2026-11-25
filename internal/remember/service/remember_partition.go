package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/generation"
)

func (p *rememberSynchronousProcessor) partitionRemember(ctx context.Context, input RememberProcessRequest, attemptID string, settings *domain.DecisionRuntimeConfig) (RememberProcessRequest, *RememberIntakeSecurity, error) {
	ctx, cancel := ContextForPhase(ctx, RememberPhaseAssessment)
	defer cancel()
	ctx = requestctx.WithProgressOperation(ctx, "partition")
	ctx = observability.WithMetricIdentity(ctx, input.TeamID, input.OwnerProfileID)
	ctx = observability.WithAIOperation(ctx, observability.AIOperationSemanticAssessment, len(input.Evidence))
	transport := p.provider
	if transport == nil {
		return input, nil, fmt.Errorf("%w: partition transport is unavailable", ErrRememberProviderUnavailable)
	}
	ReportProgress(ctx, ProgressPreparingBoundaries)
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
	checkInitialTokens := func() error {
		tokens, countErr := modelprovider.CountStructuredRequestTokens(conversation, limits.Tokenizer)
		if countErr != nil {
			return fmt.Errorf("%w: partition request sizing failed: %w", ErrRememberInputBudgetExceeded, countErr)
		}
		// Reserve every remaining complete response and bounded correction instruction before dispatch.
		inputLimit := limits.MaxInputTokens - (assessor.SemanticAssessmentMaxProviderTurns-1)*(outputLimit+4096)
		if tokens > inputLimit {
			return fmt.Errorf("%w: partition conversation requires %d tokens, limit %d", ErrRememberInputBudgetExceeded, tokens, inputLimit)
		}
		return nil
	}
	if err := prepare(); err != nil {
		return input, nil, err
	}
	if err := checkInitialTokens(); err != nil {
		return input, nil, err
	}
	review := &RememberIntakeSecurity{DecisionSnapshot: settings, Skipped: input.SkipAISecurityReview}
	if !input.SkipAISecurityReview {
		routing, err := RouteRememberIntakeSecurity(requestctx.WithProgressOperation(ctx, "intake_security"), p.decisions, settings, req, p.logger)
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
			if err := checkInitialTokens(); err != nil {
				return input, nil, err
			}
		}
	}
	var logger *slog.Logger
	if root, ok := p.logger.(interface{ ContextSlog() *slog.Logger }); ok {
		logger = root.ContextSlog()
		observability.LogDiagnosticBody(ctx, logger, p.protector, "remember_partition_manifest", []byte(conversation.Messages[1].Content), modelprovider.MaxProviderDiagnosticBodyBytes, slog.String("attempt_id", attemptID), slog.String("policy_version", knowledge.RememberPartitionPolicyVersion))
	}
	var validated [][][2]int
	generated, err := (generation.Service[[][][2]int]{Transport: transport, Logger: logger}).Run(ctx, generation.Task[[][][2]int]{
		ID: req.RequestID, Capability: "remember_partition", Tokenizer: limits.Tokenizer,
		Correction: "Return a complete replacement for the unchanged original request.",
		Request:    conversation, RepairTokenReserve: outputLimit + 4096,
		BeforeAttempt: func(ctx context.Context, turn, _ int) (context.Context, error) {
			stage := ProgressPartitioning
			if turn > 1 {
				stage = ProgressRegeneratingPartitions
			}
			requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: string(stage), Attempt: turn, Total: assessor.SemanticAssessmentMaxProviderTurns})
			return ctx, ctx.Err()
		},
		Validate: func(ctx context.Context, raw []byte) ([][][2]int, error) {
			ReportProgress(ctx, ProgressValidatingPartitions)
			spans, err := assessor.ValidatePartitionResponse(req, raw)
			var rejection *assessor.PartitionSecurityRejection
			switch {
			case errors.As(err, &rejection):
				return nil, err
			case err != nil:
				return nil, &jsonstrict.ValidationError{Stage: "partition_response", Errors: []jsonstrict.Issue{{Field: "partition", Code: "invalid", Message: err.Error()}}}
			default:
				validated = spans
				return spans, nil
			}
		},
		Observe: func(ctx context.Context, attempt generation.Attempt) {
			if logger == nil {
				return
			}
			fields := []slog.Attr{slog.String("attempt_id", attemptID), slog.Int("partition_attempt", attempt.Number),
				slog.Int("input_tokens", attempt.InputTokens), slog.Int("output_tokens", attempt.OutputTokens),
				slog.String("model", conversation.Model), slog.String("policy_version", knowledge.RememberPartitionPolicyVersion),
				slog.Int64("elapsed_ms", attempt.Elapsed.Milliseconds())}
			if attempt.Validation != nil {
				logger.LogAttrs(ctx, slog.LevelWarn, "remember_partition_rejected", append(fields, slog.String("validation_reason", attempt.Validation.Error()))...)
			} else if attempt.Err == nil {
				logger.LogAttrs(ctx, slog.LevelInfo, "remember_partition_validated", append(fields, slog.Any("spans", validated))...)
			}
		},
	})
	if err != nil {
		var rejected *assessor.PartitionSecurityRejection
		var malformed *modelprovider.MalformedResponseError
		switch {
		case errors.As(err, &rejected):
			return input, nil, IntakeSecurityRejectionFailure(req, rejected)
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return input, nil, err
		case errors.As(err, &malformed) && malformed.FailureClass == "input_budget":
			return input, nil, fmt.Errorf("%w: %w", ErrRememberInputBudgetExceeded, err)
		case malformed != nil:
			return input, nil, fmt.Errorf("%w: %w", ErrRememberProviderResponseInvalid, err)
		default:
			return input, nil, fmt.Errorf("%w: partition provider failed: %w", ErrRememberProviderUnavailable, err)
		}
	}
	output := input
	output.Evidence = nil
	output.SubmittedItems = make([]knowledge.RememberSubmittedItem, len(contents))
	for i, ranges := range generated.Value {
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
