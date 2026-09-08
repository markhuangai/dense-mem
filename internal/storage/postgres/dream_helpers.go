package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"

	"github.com/lib/pq"
	"gorm.io/gorm"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

// PredicateDefinition is the read-only predicate catalog row shared by
// capability adapters. The caller owns the transaction and its RLS context.
type PredicateDefinition struct {
	Key                 string
	Version             int
	AllowedSubjectKinds []string
	AllowedObjectKinds  []string
	RelationshipKind    string
	CurrentCardinality  string
}

var ErrTeamInactive = knowledgecontract.ErrTeamInactive

// SeedTeamPredicateDefinitions copies the immutable catalog inside the
// caller-supplied transaction. It never opens or commits a transaction.
func SeedTeamPredicateDefinitions(ctx context.Context, tx *gorm.DB, teamID string) error {
	if tx == nil {
		return errors.New("predicate catalog: transaction is required")
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO team_predicate_definitions (
		    team_id, predicate_key, version, aliases, allowed_subject_kinds,
		    allowed_object_kinds, relationship_kind, current_cardinality,
		    lifecycle_state, origin, metadata, created_at
		)
		SELECT ?::uuid, predicate_key, version, aliases, allowed_subject_kinds,
		       allowed_object_kinds, relationship_kind, current_cardinality,
		       lifecycle_state, 'built_in',
		       metadata || jsonb_build_object('source', 'predicate_definitions'),
		       created_at
		FROM predicate_definitions
		ON CONFLICT (team_id, predicate_key, version) DO NOTHING
	`, teamID).Error
}

func LoadPredicateDefinition(ctx context.Context, tx *gorm.DB, teamID, predicateKey string, version int) (*PredicateDefinition, error) {
	if tx == nil {
		return nil, errors.New("predicate catalog: transaction is required")
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
		       relationship_kind, current_cardinality
		FROM team_predicate_definitions
		WHERE team_id = ?::uuid
		  AND predicate_key = ?
		  AND version = ?
		  AND lifecycle_state = 'active'
	`, teamID, predicateKey, version).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, gorm.ErrRecordNotFound
	}
	var loaded PredicateDefinition
	var subjectKinds, objectKinds pq.StringArray
	if err := rows.Scan(&loaded.Key, &loaded.Version, &subjectKinds, &objectKinds,
		&loaded.RelationshipKind, &loaded.CurrentCardinality); err != nil {
		return nil, err
	}
	loaded.AllowedSubjectKinds = []string(subjectKinds)
	loaded.AllowedObjectKinds = []string(objectKinds)
	return &loaded, rows.Err()
}

func EnsureActiveTeamForMutation(ctx context.Context, tx *gorm.DB, teamID string) error {
	if tx == nil {
		return errors.New("team mutation: transaction is required")
	}
	row := tx.WithContext(ctx).Raw(`
		SELECT id::text
		FROM teams
		WHERE id = ?::uuid
		  AND status = 'active'
		  AND deleted_at IS NULL
		FOR SHARE
	`, teamID).Row()
	var id string
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTeamInactive
		}
		return err
	}
	return nil
}

// DiscardAdvisoryLockConnection marks a connection bad so a session-bound
// PostgreSQL advisory lock cannot return to the pool accidentally.
func DiscardAdvisoryLockConnection(lockConn *sql.Conn) error {
	if lockConn == nil {
		return nil
	}
	err := lockConn.Raw(func(any) error { return driver.ErrBadConn })
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("advisory lock discard: %w", err)
	}
	return errors.New("advisory lock discard: connection was not discarded")
}
