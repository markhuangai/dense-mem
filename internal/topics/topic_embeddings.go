package topics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
)

func (s *MaintenanceService) WithTopicEmbeddings(repository contract.TopicEmbeddingRepository, provider semanticwrite.PlainBatchProvider, timeout time.Duration) *MaintenanceService {
	s.deps.TopicEmbeddings = repository
	s.deps.EmbeddingExecutor = semanticwrite.NewProviderExecutor(provider)
	if timeout <= 0 {
		timeout = s.deps.ProviderTimeout
	}
	s.deps.EmbeddingTimeout = timeout
	return s
}

func (s *MaintenanceService) backfillTopicEmbeddings(ctx context.Context, turn contract.MaintenanceTurn, batch contract.TopicEmbeddingBatch) (runErr error) {
	started := time.Now()
	batchID := uuid.NewString()
	opCtx, cancel := context.WithDeadline(ctx, turn.LeaseUntil.Add(-5*time.Second))
	defer cancel()
	opCtx = observability.WithExchangeScope(opCtx, observability.ExchangeScope{TeamID: turn.TeamID, RunID: turn.RunID, BatchID: batchID, OperationID: batchID})
	defer func() {
		finishCtx, finishCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer finishCancel()
		if runErr != nil {
			runErr = errors.Join(runErr, s.deps.Repository.RecordMaintenanceFailure(finishCtx, turn.RunID, "topic_embedding_failed", s.deps.Now()))
		}
		runErr = errors.Join(runErr, s.deps.Repository.ReleaseMaintenanceTurn(finishCtx, turn))
	}()
	plan := semanticwrite.Plan{Fence: batch.Fence, Timeout: s.deps.EmbeddingTimeout}
	ids := make([]string, 0, len(batch.Topics))
	for _, item := range batch.Topics {
		ids = append(ids, item.ID)
		plan.Documents = append(plan.Documents, semanticwrite.Document{Hash: fmt.Sprintf("%s@%d", item.ID, item.Version), Text: item.Text})
	}
	result, err := s.deps.EmbeddingExecutor.Execute(opCtx, plan)
	committed := 0
	if err == nil {
		committed, err = s.deps.TopicEmbeddings.CompleteTopicEmbeddingBatch(opCtx, turn, batch, result)
	}
	if s.deps.Logger != nil {
		level, event, reason := slog.LevelInfo, "topic_embedding.backfill_completed", ""
		if err != nil {
			level, event, reason = slog.LevelError, "topic_embedding.backfill_failed", maintenanceDiagnosticError(err)
			if errors.Is(err, semanticwrite.ErrProviderUnavailable) || errors.Is(err, semanticwrite.ErrProviderTimeout) {
				level, reason = slog.LevelWarn, "provider_unavailable"
			} else if errors.Is(err, semanticwrite.ErrProviderResponseInvalid) {
				reason = "provider_response_invalid"
			}
		}
		s.deps.Logger.Log(opCtx, level, event, "team_id", turn.TeamID, "space_id", turn.SpaceID,
			"space_generation", turn.Generation, "run_id", turn.RunID, "batch_id", batchID,
			"stage", "topic_embedding_backfill", "topic_ids", ids, "embedding_contract_id", batch.Fence.EmbeddingContractID,
			"expected_dimensions", batch.Fence.Dimensions, "committed_count", committed, "stale_count", len(batch.Topics)-committed,
			"reason_code", reason, "duration_ms", time.Since(started).Milliseconds(), "error", err)
	}
	return err
}
