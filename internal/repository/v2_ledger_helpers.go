package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

func insertV2SecuritySignals(ctx context.Context, tx *gorm.DB, teamID, ownerProfileID, eventID string, signals []V2SecuritySignalInput) error {
	if len(signals) == 0 {
		return nil
	}

	values := make([]string, 0, len(signals))
	args := make([]any, 0, len(signals)*10)
	for i, signal := range signals {
		metadata, err := marshalV2JSON(signal.Metadata)
		if err != nil {
			return err
		}
		values = append(values, "(?::uuid, ?::uuid, ?, ?::uuid, ?, ?, ?, ?, ?, ?::jsonb)")
		args = append(args,
			teamID,
			eventID,
			i,
			ownerProfileID,
			signal.Kind,
			signal.Severity,
			signal.SpanStart,
			signal.SpanEnd,
			signal.Quote,
			string(metadata),
		)
	}

	return tx.WithContext(ctx).Exec(`
		INSERT INTO evidence_security_signals (
		    team_id, security_event_id, signal_index, owner_profile_id,
		    kind, severity, span_start, span_end, quote, metadata
		) VALUES `+strings.Join(values, ", "), args...).Error
}

func ensureV2EvidenceEventOwnership(ctx context.Context, tx *gorm.DB, input V2SecurityEventInput) error {
	var exists bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM evidence_fragments AS fragment
			JOIN knowledge_ingests AS ingest
			  ON ingest.team_id = fragment.team_id
			 AND ingest.ingest_id = fragment.ingest_id
			 AND ingest.owner_profile_id = fragment.owner_profile_id
			WHERE fragment.team_id = ?::uuid
			  AND fragment.fragment_id = ?::uuid
			  AND fragment.ingest_id = ?::uuid
			  AND fragment.owner_profile_id = ?::uuid
		)
	`, input.TeamID, input.FragmentID, input.IngestID, input.OwnerProfileID).Scan(&exists).Error; err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: security event target evidence does not belong to owner and ingest", ErrV2SemanticOwnerMismatch)
	}
	return nil
}

func isPostgresUniqueConstraint(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func translateV2SourceCreateError(err error) error {
	if err == nil {
		return nil
	}
	if isPostgresUniqueConstraint(err, "evidence_sources_owner_key_unique") {
		return fmt.Errorf("%w: source was created concurrently", ErrV2SourceRevisionConflict)
	}
	return err
}
