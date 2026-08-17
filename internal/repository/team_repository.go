package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// emptyMetadata is an empty JSON object for postgres jsonb columns
var emptyMetadata = map[string]any{}

// TeamRepository is the companion interface for team data access.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type TeamRepository interface {
	Create(ctx context.Context, team *domain.Team) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error)
	List(ctx context.Context, limit, offset int) ([]*domain.Team, error)
	Count(ctx context.Context) (int64, error)
	Update(ctx context.Context, team *domain.Team) error
	SoftDelete(ctx context.Context, id uuid.UUID) error
	HardDelete(ctx context.Context, id uuid.UUID) error
	CountActiveKeys(ctx context.Context, teamID uuid.UUID) (int64, error)
	NameExists(ctx context.Context, name string) (bool, error)
}

// TeamRepositoryImpl implements the TeamRepository interface.
// Every query runs inside an RLS-aware transaction so Postgres FORCE RLS
// policies (app.current_profile_id / app.tx_mode) enforce tenant isolation
// even if a caller ever reaches the repository without the service layer.
type TeamRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

// Ensure TeamRepositoryImpl implements TeamRepository
var _ TeamRepository = (*TeamRepositoryImpl)(nil)

// NewTeamRepository creates a new team repository instance.
// rls is required; nil causes a panic at first use. Callers should pass
// postgres.NewRLS() for production and an RLSHelper mock for unit tests.
func NewTeamRepository(db *gorm.DB, rls postgres.RLSHelper) *TeamRepositoryImpl {
	return &TeamRepositoryImpl{db: db, rls: rls}
}

func marshalJSONBMap(value map[string]any) (string, error) {
	if value == nil {
		value = emptyMetadata
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeJSONBMap(raw string) (map[string]any, error) {
	if raw == "" {
		return map[string]any{}, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, err
	}
	if decoded == nil {
		return map[string]any{}, nil
	}
	return decoded, nil
}

func scanTeamRow(rows *sql.Rows) (*domain.Team, error) {
	var team domain.Team
	var metadataJSON, configJSON string
	if err := rows.Scan(
		&team.ID,
		&team.Name,
		&team.Description,
		&metadataJSON,
		&configJSON,
		&team.CreatedAt,
		&team.UpdatedAt,
		&team.DeletedAt,
	); err != nil {
		return nil, err
	}

	metadata, err := decodeJSONBMap(metadataJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to decode team metadata: %w", err)
	}
	config, err := decodeJSONBMap(configJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to decode team config: %w", err)
	}
	team.Metadata = metadata
	team.Config = config
	return &team, nil
}

// Create creates a new team with server-side UUID generation.
// Enforces unique lower(name) among non-deleted rows and sets status='active'.
func (r *TeamRepositoryImpl) Create(ctx context.Context, team *domain.Team) error {
	// Generate UUID server-side if not provided
	if team.ID == uuid.Nil {
		team.ID = uuid.New()
	}

	// Set timestamps
	now := time.Now().UTC()
	team.CreatedAt = now
	team.UpdatedAt = now

	metadata, err := marshalJSONBMap(team.Metadata)
	if err != nil {
		return fmt.Errorf("failed to create team: marshal metadata: %w", err)
	}
	config, err := marshalJSONBMap(team.Config)
	if err != nil {
		return fmt.Errorf("failed to create team: marshal config: %w", err)
	}

	// INSERT must satisfy profiles_self_access (id = app.current_profile_id);
	// seed the session with the new team's id so the RLS policy passes.
	err = r.rls.WithTeamTx(ctx, r.db, team.ID.String(), func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO teams (id, name, description, metadata, config, status, created_at, updated_at, deleted_at)
			VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, 'active', $6, $7, NULL)
		`, team.ID, team.Name, team.Description, metadata, config, team.CreatedAt, team.UpdatedAt).Error
	})

	if err != nil {
		return fmt.Errorf("failed to create team: %w", err)
	}

	return nil
}

// GetByID retrieves a team by ID, excluding soft-deleted teams.
// Uses internal/system RLS context because callers include middleware paths that
// resolve teams without yet knowing whether the requester is authorized.
// Authorization is enforced at the HTTP middleware layer, not here.
func (r *TeamRepositoryImpl) GetByID(ctx context.Context, id uuid.UUID) (*domain.Team, error) {
	var team *domain.Team

	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT id, name, description, metadata::text, config::text, created_at, updated_at, deleted_at
			FROM teams
			WHERE id = $1 AND deleted_at IS NULL
		`, id).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()

		if !rows.Next() {
			return rows.Err()
		}
		scanned, err := scanTeamRow(rows)
		if err != nil {
			return err
		}
		team = scanned
		return rows.Err()
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get team: %w", err)
	}

	return team, nil
}

// List retrieves teams with pagination, excluding soft-deleted rows.
// Default limit=20, max limit=100, sorted by created_at DESC, id ASC.
func (r *TeamRepositoryImpl) List(ctx context.Context, limit, offset int) ([]*domain.Team, error) {
	// Apply defaults and limits
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}

	var teams []*domain.Team

	// List is a cross-team read; system RLS context lets the
	// profiles_system_read_access policy return every non-deleted row.
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT id, name, description, metadata::text, config::text, created_at, updated_at, deleted_at
			FROM teams
			WHERE deleted_at IS NULL
			ORDER BY created_at DESC, id ASC
			LIMIT $1 OFFSET $2
		`, limit, offset).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			team, err := scanTeamRow(rows)
			if err != nil {
				return err
			}
			teams = append(teams, team)
		}
		return rows.Err()
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list teams: %w", err)
	}

	return teams, nil
}

// Count returns the total number of non-deleted teams.
func (r *TeamRepositoryImpl) Count(ctx context.Context) (int64, error) {
	var count int64

	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM teams
			WHERE deleted_at IS NULL
		`).Scan(&count).Error
	})

	if err != nil {
		return 0, fmt.Errorf("failed to count teams: %w", err)
	}

	return count, nil
}

// Update updates an existing team, excluding soft-deleted rows.
func (r *TeamRepositoryImpl) Update(ctx context.Context, team *domain.Team) error {
	team.UpdatedAt = time.Now().UTC()

	metadata, err := marshalJSONBMap(team.Metadata)
	if err != nil {
		return fmt.Errorf("failed to update team: marshal metadata: %w", err)
	}
	config, err := marshalJSONBMap(team.Config)
	if err != nil {
		return fmt.Errorf("failed to update team: marshal config: %w", err)
	}

	// UPDATE must satisfy profiles_self_access (id = app.current_profile_id).
	err = r.rls.WithTeamTx(ctx, r.db, team.ID.String(), func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE teams
			SET name = $1,
			    description = $2,
			    metadata = $3::jsonb,
			    config = $4::jsonb,
			    config_version = CASE
			        WHEN config IS DISTINCT FROM $4::jsonb THEN config_version + 1
			        ELSE config_version
			    END,
			    updated_at = $5
			WHERE id = $6 AND deleted_at IS NULL
		`, team.Name, team.Description, metadata, config, team.UpdatedAt, team.ID).Error
	})

	if err != nil {
		return fmt.Errorf("failed to update team: %w", err)
	}

	return nil
}

// SoftDelete tombstones a team and revokes its active team credentials.
func (r *TeamRepositoryImpl) SoftDelete(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()

	// Soft-delete is an UPDATE; must satisfy profiles_self_access (id = app.current_profile_id).
	err := r.rls.WithTeamTx(ctx, r.db, id.String(), func(tx *gorm.DB) error {
		var lockedID string
		if err := tx.Raw(`
			SELECT id::text
			FROM teams
			WHERE id = $1 AND deleted_at IS NULL
			FOR UPDATE
		`, id).Row().Scan(&lockedID); errors.Is(err, sql.ErrNoRows) {
			return gorm.ErrRecordNotFound
		} else if err != nil {
			return err
		}
		if lockedID == "" {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Exec(`
			UPDATE teams
			SET status = 'deleted', deleted_at = $1, updated_at = $1
			WHERE id = $2 AND deleted_at IS NULL
		`, now, id).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'stale',
			    error = 'team deleted before embedding processing',
			    completed_at = $1,
			    lease_until = NULL,
			    worker_id = '',
			    updated_at = $1
			WHERE team_id = $2
			  AND status IN ('queued', 'processing')
		`, now, id).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE credentials
			SET status = 'revoked', revoked_at = COALESCE(revoked_at, $1), updated_at = $1
			WHERE team_id = $2 AND status = 'active'
		`, now, id).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE team_memberships
			SET status = 'revoked', updated_at = $1
			WHERE team_id = $2 AND status = 'active'
		`, now, id).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE actor_identities
			SET active = false, updated_at = $1
			WHERE team_id = $2 AND active = true
		`, now, id).Error
	})

	if err != nil {
		return fmt.Errorf("failed to soft delete team: %w", err)
	}

	return nil
}

// HardDelete removes a team row and its Postgres-owned child rows.
// audit_log is intentionally not deleted: it is append-only and no longer has
// live FKs to teams/api_keys, so historical audit entries remain immutable.
func (r *TeamRepositoryImpl) HardDelete(ctx context.Context, id uuid.UUID) error {
	err := r.rls.WithTeamTx(ctx, r.db, id.String(), func(tx *gorm.DB) error {
		if err := tx.Exec(`
			DELETE FROM ownership_aliases
			WHERE team_id = $1
		`, id).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM credentials WHERE team_id = $1`, id).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM team_memberships WHERE team_id = $1`, id).Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM actor_identities WHERE team_id = $1`, id).Error; err != nil {
			return err
		}
		// The catalog is system-owned and has no team-mode DELETE policy. Remove
		// it after credential deletion while retaining RESTRICT protection for
		// any semantic/search rows that still reference a space.
		if err := tx.Exec("SELECT set_config('app.tx_mode', 'system', true)").Error; err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM memory_spaces WHERE team_id = $1`, id).Error; err != nil {
			return err
		}
		if err := tx.Exec("SELECT set_config('app.tx_mode', 'team', true)").Error; err != nil {
			return err
		}
		return tx.Exec(`
			DELETE FROM teams
			WHERE id = $1 AND deleted_at IS NULL
		`, id).Error
	})

	if err != nil {
		return fmt.Errorf("failed to hard delete team: %w", err)
	}

	return nil
}

// CountActiveKeys counts the number of non-revoked, non-expired active keys for a team.
func (r *TeamRepositoryImpl) CountActiveKeys(ctx context.Context, teamID uuid.UUID) (int64, error) {
	var count int64

	err := r.rls.WithTeamTx(ctx, r.db, teamID.String(), func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM credentials
			WHERE team_id = $1
				AND kind = 'api_key'
				AND status = 'active'
				AND revoked_at IS NULL
				AND (expires_at IS NULL OR expires_at > NOW())
		`, teamID).Scan(&count).Error
	})

	if err != nil {
		return 0, fmt.Errorf("failed to count active keys: %w", err)
	}

	return count, nil
}

// NameExists checks if a team name exists (case-insensitive) among non-deleted rows.
func (r *TeamRepositoryImpl) NameExists(ctx context.Context, name string) (bool, error) {
	var count int64

	// NameExists must see all teams (collision detection is cross-tenant);
	// system RLS context enables the profiles_system_read_access SELECT policy.
	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM teams
			WHERE lower(name) = lower($1) AND deleted_at IS NULL
		`, name).Scan(&count).Error
	})

	if err != nil {
		return false, fmt.Errorf("failed to check name existence: %w", err)
	}

	return count > 0, nil
}
