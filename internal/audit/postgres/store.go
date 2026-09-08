// Package postgres implements audit persistence and RLS transaction mechanics.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/audit/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// Store is the PostgreSQL implementation of the audit persistence port.
type Store struct {
	db  *gorm.DB
	rls storagepostgres.RLSHelper
}

var _ contract.Store = (*Store)(nil)

// NewStore constructs an audit PostgreSQL adapter. Callers may provide the
// shared RLS helper explicitly; the live composition uses the default helper.
func NewStore(db *gorm.DB, helpers ...storagepostgres.RLSHelper) *Store {
	rls := storagepostgres.RLSHelper(storagepostgres.NewRLS())
	if len(helpers) > 0 {
		rls = helpers[0]
	}
	return &Store{db: db, rls: rls}
}

// Append inserts one already-redacted audit entry. Credential memory-space
// inference remains inside this transaction so the audit row cannot cross teams.
func (s *Store) Append(ctx context.Context, entry contract.Entry) error {
	if s == nil || s.db == nil {
		return errors.New("audit: database is required")
	}

	insert := func(tx *gorm.DB) error {
		memorySpaceID := entry.MemorySpaceID
		if memorySpaceID == nil && entry.CredentialMemorySpaceLookup != nil {
			lookup := entry.CredentialMemorySpaceLookup
			var candidate sql.NullString
			lookupErr := tx.WithContext(ctx).Raw(`
					SELECT memory_space_id::text
					FROM credentials
					WHERE id = $1 AND team_id = $2
				`, lookup.CredentialID, lookup.TeamID).Row().Scan(&candidate)
			if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
				return lookupErr
			}
			if candidate.Valid && strings.TrimSpace(candidate.String) != "" {
				memorySpaceID = &candidate.String
			}
		}

		return tx.WithContext(ctx).Exec(`
			INSERT INTO audit_log (
				id, team_id, timestamp, operation, entity_type, entity_id,
				before_payload, after_payload, actor_profile_id, actor_role,
				client_ip, correlation_id, metadata, memory_space_id
			) VALUES (
				$1, $2, $3, $4, $5, $6,
				$7, $8, $9, $10, $11, $12, $13, $14
			)
		`, entry.ID, entry.ProfileID, entry.Timestamp, entry.Operation, entry.EntityType,
			entry.EntityID, entry.BeforePayload, entry.AfterPayload, entry.ActorKeyID,
			entry.ActorRole, entry.ClientIP, entry.CorrelationID, entry.Metadata, memorySpaceID).Error
	}

	if s.rls != nil {
		return s.rls.WithSystemTx(ctx, s.db, insert)
	}
	return insert(s.db.WithContext(ctx))
}

// List returns active-space-visible audit entries and the matching total count.
func (s *Store) List(ctx context.Context, teamID string, limit, offset int) ([]contract.Entry, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("audit: database is required")
	}

	entries := make([]contract.Entry, 0)
	query := func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT id, team_id, timestamp, operation, entity_type, entity_id,
			       before_payload, after_payload, actor_profile_id, actor_role,
			       client_ip, correlation_id, metadata, memory_space_id::text
			FROM audit_log AS audit
			WHERE audit.team_id = $1
			  AND (
				  audit.memory_space_id IS NULL
				  OR EXISTS (
					  SELECT 1
					  FROM memory_spaces AS space
					  WHERE space.id = audit.memory_space_id
					    AND space.team_id = audit.team_id
					    AND space.lifecycle_state = 'active'
				  )
			  )
			ORDER BY timestamp DESC
			LIMIT $2 OFFSET $3
		`, teamID, limit, offset).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var entry contract.Entry
			var profileID, actorKeyID, clientIP, memorySpaceID sql.NullString
			if err := rows.Scan(
				&entry.ID, &profileID, &entry.Timestamp, &entry.Operation,
				&entry.EntityType, &entry.EntityID, &entry.BeforePayload,
				&entry.AfterPayload, &actorKeyID, &entry.ActorRole, &clientIP,
				&entry.CorrelationID, &entry.Metadata, &memorySpaceID,
			); err != nil {
				return err
			}
			if profileID.Valid {
				entry.ProfileID = &profileID.String
			}
			if actorKeyID.Valid {
				entry.ActorKeyID = &actorKeyID.String
			}
			if clientIP.Valid {
				entry.ClientIP = clientIP.String
			}
			if memorySpaceID.Valid {
				entry.MemorySpaceID = &memorySpaceID.String
			}
			entries = append(entries, entry)
		}
		return rows.Err()
	}

	if s.rls != nil {
		if err := s.rls.WithTeamTx(ctx, s.db, teamID, query); err != nil {
			return nil, err
		}
	} else {
		if err := query(s.db.WithContext(ctx)); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

// Count returns the active-space-visible total for one team.
func (s *Store) Count(ctx context.Context, teamID string) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("audit: database is required")
	}
	count := 0
	countQuery := func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT COUNT(*)
			FROM audit_log AS audit
			WHERE audit.team_id = $1
			  AND (
				  audit.memory_space_id IS NULL
				  OR EXISTS (
					  SELECT 1
					  FROM memory_spaces AS space
					  WHERE space.id = audit.memory_space_id
					    AND space.team_id = audit.team_id
					    AND space.lifecycle_state = 'active'
				  )
			  )
		`, teamID).Scan(&count).Error
	}
	if s.rls != nil {
		if err := s.rls.WithTeamTx(ctx, s.db, teamID, countQuery); err != nil {
			return 0, err
		}
	} else if err := countQuery(s.db.WithContext(ctx)); err != nil {
		return 0, err
	}
	return count, nil
}
