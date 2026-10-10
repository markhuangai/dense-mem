//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestSessionDiagnosticsRetentionHonorsLegalHold(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-diagnostic-hold")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	staged, err := store.StageSession(ctx, intake)
	require.NoError(t, err)
	require.NoError(t, store.RecordSessionDiagnostic(ctx, intake.Scope, staged.ID, json.RawMessage(`{"capture_state":"captured","payload":{}}`)))
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO session_submission_diagnostics (team_id,owner_profile_id,space_id,space_generation,submission_id,body,created_at,expires_at) SELECT ?::uuid,?::uuid,?::uuid,?,?::uuid,'{}',now()-interval '7 days',now()-interval '1 second' FROM generate_series(1,250)`, team, owner, intake.Scope.SpaceID, intake.Scope.SpaceGeneration, staged.ID).Error
	}))
	holds := privacy.NewPrivateMemoryRepository(app, rls)
	_, _, err = holds.PlaceLegalHold(context.Background(), uuid.MustParse(intake.Scope.SpaceID), "session_hold")
	require.NoError(t, err)
	deleted, err := store.purgeRememberAttemptDiagnostics(ctx)
	require.NoError(t, err)
	require.Zero(t, deleted)
	_, _, err = holds.ReleaseLegalHold(ctx, uuid.MustParse(intake.Scope.SpaceID))
	require.NoError(t, err)
	deleted, err = store.purgeRememberAttemptDiagnostics(ctx)
	require.NoError(t, err)
	require.Equal(t, 250, deleted)
	var count int64
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM session_submission_diagnostics WHERE submission_id=?::uuid`, staged.ID).Row().Scan(&count)
	}))
	require.EqualValues(t, 1, count)
	require.Error(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM session_submission_diagnostics WHERE submission_id=?::uuid`, staged.ID).Error
	}))
}
