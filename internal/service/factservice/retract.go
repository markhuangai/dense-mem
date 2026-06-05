package factservice

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/ownership"
	neo4jstorage "github.com/markhuangai/dense-mem/internal/storage/neo4j"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// retractFactDB is the minimal Neo4j surface required to retract facts.
type retractFactDB interface {
	ScopedWriteTx(ctx context.Context, profileID string, fn func(tx neo4j.ManagedTransaction) error) error
}

type retractFactService struct {
	db     retractFactDB
	audit  AuditEmitter
	logger *slog.Logger
}

var _ RetractFactService = (*retractFactService)(nil)

// NewRetractFactService constructs a RetractFactService.
func NewRetractFactService(db retractFactDB, audit AuditEmitter, logger *slog.Logger) RetractFactService {
	return &retractFactService{
		db:     db,
		audit:  audit,
		logger: logger,
	}
}

// Retract marks a fact as withdrawn from current knowledge while preserving
// the validity interval. recorded_to closes known-time reads; valid_to remains
// untouched because retraction does not prove the statement became false.
func (s *retractFactService) Retract(ctx context.Context, profileID, factID string) error {
	now := time.Now().UTC()

	err := s.db.ScopedWriteTx(ctx, profileID, func(tx neo4j.ManagedTransaction) error {
		existsResult, err := neo4jstorage.RunScoped(ctx, tx, profileID,
			`MATCH (f:Fact {team_id: $profileId, fact_id: $factId})
			 RETURN f.fact_id AS fact_id,
			        coalesce(f.owner_profile_id, f.created_by_profile_id, f.promoted_by_profile_id, '') AS owner_profile_id
			 LIMIT 1`,
			map[string]any{"factId": factID},
		)
		if err != nil {
			return fmt.Errorf("existence check: %w", err)
		}
		existsRecords, err := existsResult.Collect(ctx)
		if err != nil {
			return fmt.Errorf("existence collect: %w", err)
		}
		if len(existsRecords) == 0 {
			return ErrFactNotFound
		}
		ownerID, _ := existsRecords[0].AsMap()["owner_profile_id"].(string)
		if err := ownership.RequireOwner(ctx, ownerID); err != nil {
			return err
		}

		result, err := neo4jstorage.RunScoped(ctx, tx, profileID,
			`MATCH (f:Fact {team_id: $profileId, fact_id: $factId})
			 SET f.status = $status,
			     f.recorded_to = coalesce(f.recorded_to, $now),
			     f.retracted_at = coalesce(f.retracted_at, $now)`,
			map[string]any{
				"factId": factID,
				"status": string(domain.FactStatusRetracted),
				"now":    now,
			},
		)
		if err != nil {
			return fmt.Errorf("retract fact: %w", err)
		}
		if _, err := result.Consume(ctx); err != nil {
			return fmt.Errorf("retract consume: %w", err)
		}
		return nil
	})
	if err != nil {
		if s.logger != nil && !errors.Is(err, ErrFactNotFound) && !errors.Is(err, ownership.ErrOwnerMismatch) {
			s.logger.Warn("fact retract transaction failed",
				slog.String("team_id", profileID),
				slog.String("fact_id", factID),
				slog.String("error", err.Error()),
			)
		}
		return err
	}

	if s.audit != nil {
		entry := AuditLogEntry{
			ProfileID:     profileID,
			Timestamp:     now,
			Operation:     "fact.retract",
			EntityType:    "fact",
			EntityID:      factID,
			CorrelationID: correlation.FromContext(ctx),
			AfterPayload: map[string]any{
				"fact_id": factID,
				"team_id": profileID,
				"status":  string(domain.FactStatusRetracted),
			},
		}
		if err := s.audit.Append(ctx, entry); err != nil && s.logger != nil {
			s.logger.Warn("failed to emit audit event for fact retraction",
				slog.String("team_id", profileID),
				slog.String("fact_id", factID),
				slog.String("error", err.Error()),
			)
		}
	}

	return nil
}
