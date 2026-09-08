package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"gorm.io/gorm"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
)

// These compatibility helpers keep legacy fixtures on the knowledge owner's
// evidence/security SQL. They intentionally do not own a second write path.
func insertEvidenceQuarantine(ctx context.Context, tx *gorm.DB, input CreateIngestInput, ingestID, fragmentID, reason string) error {
	return knowledgepostgres.InsertEvidenceQuarantineTx(ctx, knowledgepostgres.LegacyTransaction(tx), input, ingestID, fragmentID, reason)
}

func insertSecurityEvent(ctx context.Context, tx *gorm.DB, input SecurityEventInput) (string, error) {
	return knowledgepostgres.InsertSecurityEventTx(ctx, knowledgepostgres.LegacyTransaction(tx), input)
}

func validateSecurityEventDraft(input SecurityEventDraft) error {
	return knowledgepostgres.ValidateSecurityEventDraft(input)
}

func isPostgresUniqueConstraint(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == constraint
}

func isPostgresForeignKeyConstraint(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503" && pgErr.ConstraintName == constraint
}

func translateSourceCreateError(err error) error {
	if err == nil {
		return nil
	}
	if isPostgresUniqueConstraint(err, "evidence_sources_owner_key_unique") {
		return fmt.Errorf("%w: source was created concurrently", ErrSourceRevisionConflict)
	}
	return err
}

func marshalJSON(value map[string]any) ([]byte, error) {
	if value == nil {
		value = map[string]any{}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal json: %w", err)
	}
	return data, nil
}

func pqStringArray(values []string) any {
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	return pq.Array(normalized)
}

func sha256Hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}
