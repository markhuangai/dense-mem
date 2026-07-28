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

// ProfileRepository is the companion interface for profile data access.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type ProfileRepository interface {
	Create(ctx context.Context, profile *domain.Profile) error
	GetByID(ctx context.Context, id uuid.UUID) (*domain.Profile, error)
	List(ctx context.Context, limit, offset int) ([]*domain.Profile, error)
	Count(ctx context.Context) (int64, error)
	Update(ctx context.Context, profile *domain.Profile) error
	SoftDelete(ctx context.Context, id uuid.UUID) error
	HardDelete(ctx context.Context, id uuid.UUID) error
	CountActiveKeys(ctx context.Context, profileID uuid.UUID) (int64, error)
	NameExists(ctx context.Context, name string) (bool, error)
}

// ProfileRepositoryImpl implements the ProfileRepository interface.
// Every query runs inside an RLS-aware transaction so Postgres FORCE RLS
// policies (app.current_profile_id / app.tx_mode) enforce tenant isolation
// even if a caller ever reaches the repository without the service layer.
type ProfileRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

// Ensure ProfileRepositoryImpl implements ProfileRepository
var _ ProfileRepository = (*ProfileRepositoryImpl)(nil)

// NewProfileRepository creates a new profile repository instance.
// rls is required; nil causes a panic at first use. Callers should pass
// postgres.NewRLS() for production and an RLSHelper mock for unit tests.
func NewProfileRepository(db *gorm.DB, rls postgres.RLSHelper) *ProfileRepositoryImpl {
	return &ProfileRepositoryImpl{db: db, rls: rls}
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

func scanProfileRow(rows *sql.Rows) (*domain.Profile, error) {
	var profile domain.Profile
	var metadataJSON, configJSON string
	if err := rows.Scan(
		&profile.ID,
		&profile.Name,
		&profile.Description,
		&metadataJSON,
		&configJSON,
		&profile.CreatedAt,
		&profile.UpdatedAt,
		&profile.DeletedAt,
	); err != nil {
		return nil, err
	}

	metadata, err := decodeJSONBMap(metadataJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to decode profile metadata: %w", err)
	}
	config, err := decodeJSONBMap(configJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to decode profile config: %w", err)
	}
	profile.Metadata = metadata
	profile.Config = config
	return &profile, nil
}

// Create creates a new profile with server-side UUID generation.
// Enforces unique lower(name) among non-deleted rows and sets status='active'.
func (r *ProfileRepositoryImpl) Create(ctx context.Context, profile *domain.Profile) error {
	// Generate UUID server-side if not provided
	if profile.ID == uuid.Nil {
		profile.ID = uuid.New()
	}

	// Set timestamps
	now := time.Now().UTC()
	profile.CreatedAt = now
	profile.UpdatedAt = now

	metadata, err := marshalJSONBMap(profile.Metadata)
	if err != nil {
		return fmt.Errorf("failed to create profile: marshal metadata: %w", err)
	}
	config, err := marshalJSONBMap(profile.Config)
	if err != nil {
		return fmt.Errorf("failed to create profile: marshal config: %w", err)
	}

	// INSERT must satisfy profiles_self_access (id = app.current_profile_id);
	// seed the session with the new profile's id so the RLS policy passes.
	err = r.rls.WithProfileTx(ctx, r.db, profile.ID.String(), func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO teams (id, name, description, metadata, config, status, created_at, updated_at, deleted_at)
			VALUES ($1, $2, $3, $4::jsonb, $5::jsonb, 'active', $6, $7, NULL)
		`, profile.ID, profile.Name, profile.Description, metadata, config, profile.CreatedAt, profile.UpdatedAt).Error
	})

	if err != nil {
		return fmt.Errorf("failed to create profile: %w", err)
	}

	return nil
}

// GetByID retrieves a profile by ID, excluding soft-deleted profiles.
// Uses internal/system RLS context because callers include middleware paths that
// resolve profiles without yet knowing whether the requester is authorized.
// Authorization is enforced at the HTTP middleware layer, not here.
func (r *ProfileRepositoryImpl) GetByID(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
	var profile *domain.Profile

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
		scanned, err := scanProfileRow(rows)
		if err != nil {
			return err
		}
		profile = scanned
		return rows.Err()
	})

	if err != nil {
		return nil, fmt.Errorf("failed to get profile: %w", err)
	}

	return profile, nil
}

// List retrieves profiles with pagination, excluding soft-deleted rows.
// Default limit=20, max limit=100, sorted by created_at DESC, id ASC.
func (r *ProfileRepositoryImpl) List(ctx context.Context, limit, offset int) ([]*domain.Profile, error) {
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

	var profiles []*domain.Profile

	// List is a cross-profile read; system RLS context lets the
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
			profile, err := scanProfileRow(rows)
			if err != nil {
				return err
			}
			profiles = append(profiles, profile)
		}
		return rows.Err()
	})

	if err != nil {
		return nil, fmt.Errorf("failed to list profiles: %w", err)
	}

	return profiles, nil
}

// Count returns the total number of non-deleted profiles.
func (r *ProfileRepositoryImpl) Count(ctx context.Context) (int64, error) {
	var count int64

	err := r.rls.WithSystemTx(ctx, r.db, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM teams
			WHERE deleted_at IS NULL
		`).Scan(&count).Error
	})

	if err != nil {
		return 0, fmt.Errorf("failed to count profiles: %w", err)
	}

	return count, nil
}

// Update updates an existing profile, excluding soft-deleted rows.
func (r *ProfileRepositoryImpl) Update(ctx context.Context, profile *domain.Profile) error {
	profile.UpdatedAt = time.Now().UTC()

	metadata, err := marshalJSONBMap(profile.Metadata)
	if err != nil {
		return fmt.Errorf("failed to update profile: marshal metadata: %w", err)
	}
	config, err := marshalJSONBMap(profile.Config)
	if err != nil {
		return fmt.Errorf("failed to update profile: marshal config: %w", err)
	}

	// UPDATE must satisfy profiles_self_access (id = app.current_profile_id).
	err = r.rls.WithProfileTx(ctx, r.db, profile.ID.String(), func(tx *gorm.DB) error {
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
		`, profile.Name, profile.Description, metadata, config, profile.UpdatedAt, profile.ID).Error
	})

	if err != nil {
		return fmt.Errorf("failed to update profile: %w", err)
	}

	return nil
}

// SoftDelete tombstones a team and revokes its active profile credentials.
func (r *ProfileRepositoryImpl) SoftDelete(ctx context.Context, id uuid.UUID) error {
	now := time.Now().UTC()

	// Soft-delete is an UPDATE; must satisfy profiles_self_access (id = app.current_profile_id).
	err := r.rls.WithProfileTx(ctx, r.db, id.String(), func(tx *gorm.DB) error {
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
		return tx.Exec(`
			UPDATE team_profiles
			SET revoked_at = $1, updated_at = $1
			WHERE team_id = $2
			  AND revoked_at IS NULL
		`, now, id).Error
	})

	if err != nil {
		return fmt.Errorf("failed to soft delete profile: %w", err)
	}

	return nil
}

// HardDelete removes a profile row and its Postgres-owned child rows.
// audit_log is intentionally not deleted: it is append-only and no longer has
// live FKs to profiles/api_keys, so historical audit entries remain immutable.
func (r *ProfileRepositoryImpl) HardDelete(ctx context.Context, id uuid.UUID) error {
	err := r.rls.WithProfileTx(ctx, r.db, id.String(), func(tx *gorm.DB) error {
		if err := tx.Exec(`
			DELETE FROM team_profiles
			WHERE team_id = $1
		`, id).Error; err != nil {
			return err
		}
		return tx.Exec(`
			DELETE FROM teams
			WHERE id = $1 AND deleted_at IS NULL
		`, id).Error
	})

	if err != nil {
		return fmt.Errorf("failed to hard delete profile: %w", err)
	}

	return nil
}

// CountActiveKeys counts the number of non-revoked, non-expired active keys for a profile.
func (r *ProfileRepositoryImpl) CountActiveKeys(ctx context.Context, profileID uuid.UUID) (int64, error) {
	var count int64

	// SELECT is scoped to one profile; api_keys_self_access matches on profile_id.
	err := r.rls.WithProfileTx(ctx, r.db, profileID.String(), func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM team_profiles
			WHERE team_id = $1
				AND revoked_at IS NULL
				AND (expires_at IS NULL OR expires_at > NOW())
		`, profileID).Scan(&count).Error
	})

	if err != nil {
		return 0, fmt.Errorf("failed to count active keys: %w", err)
	}

	return count, nil
}

// NameExists checks if a profile name exists (case-insensitive) among non-deleted rows.
func (r *ProfileRepositoryImpl) NameExists(ctx context.Context, name string) (bool, error) {
	var count int64

	// NameExists must see all profiles (collision detection is cross-tenant);
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
