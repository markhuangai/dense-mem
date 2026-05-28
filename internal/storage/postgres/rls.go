package postgres

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// RLSHelper is the companion interface for the RLS helper struct.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type RLSHelper interface {
	WithProfileTx(ctx context.Context, db *gorm.DB, profileID string, fn func(tx *gorm.DB) error) error
	WithTeamTx(ctx context.Context, db *gorm.DB, teamID string, fn func(tx *gorm.DB) error) error
	WithSystemTx(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error
}

// RLS provides helper methods for executing transactions with Row Level Security
// session variables set via SET LOCAL (transaction-scoped).
type RLS struct{}

// Ensure RLS implements RLSHelper interface
var _ RLSHelper = (*RLS)(nil)

// NewRLS creates a new RLS helper instance.
func NewRLS() *RLS {
	return &RLS{}
}

// WithProfileTx executes fn inside a transaction with profile session variables set via set_config.
// The session variables are scoped to the transaction (is_local=true) so they cannot bleed
// into subsequent queries on a pooled connection.
//
// We use set_config(setting, value, is_local) rather than SET LOCAL because SET LOCAL does not
// accept parameterized values — it requires a SQL literal. set_config is a regular function that
// accepts a bound parameter, which both avoids SQL-injection risk and lets gorm/pgx bind the value.
func (r *RLS) WithProfileTx(ctx context.Context, db *gorm.DB, profileID string, fn func(tx *gorm.DB) error) error {
	return r.WithTeamTx(ctx, db, profileID, fn)
}

// WithTeamTx executes fn inside a transaction with team session variables set.
func (r *RLS) WithTeamTx(ctx context.Context, db *gorm.DB, teamID string, fn func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.current_team_id', ?, true)", teamID).Error; err != nil {
			return fmt.Errorf("failed to set app.current_team_id: %w", err)
		}
		if err := tx.Exec("SELECT set_config('app.current_profile_id', ?, true)", teamID).Error; err != nil {
			return fmt.Errorf("failed to set app.current_profile_id: %w", err)
		}
		if err := tx.Exec("SELECT set_config('app.tx_mode', 'team', true)").Error; err != nil {
			return fmt.Errorf("failed to set app.tx_mode: %w", err)
		}
		return fn(tx)
	})
}

// WithSystemTx executes fn inside a transaction with internal/system session
// variables set via set_config. The session variables are scoped to the
// transaction (is_local=true) so they cannot bleed into subsequent queries on a
// pooled connection.
func (r *RLS) WithSystemTx(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT set_config('app.current_team_id', '', true)").Error; err != nil {
			return fmt.Errorf("failed to set app.current_team_id: %w", err)
		}
		if err := tx.Exec("SELECT set_config('app.current_profile_id', '', true)").Error; err != nil {
			return fmt.Errorf("failed to set app.current_profile_id: %w", err)
		}
		if err := tx.Exec("SELECT set_config('app.tx_mode', 'system', true)").Error; err != nil {
			return fmt.Errorf("failed to set app.tx_mode: %w", err)
		}
		if err := tx.Exec("SELECT set_config('app.role', 'admin', true)").Error; err != nil {
			return fmt.Errorf("failed to set app.role: %w", err)
		}
		return fn(tx)
	})
}
