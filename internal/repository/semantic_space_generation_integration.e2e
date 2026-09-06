package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestActiveSpaceGenerationHelperHonorsLifecycleAndExecutionPrivilege(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "active-space-generation-helper")
	space, err := NewMemorySpaceRepository(appDB, rls).EnsureCredentialPrivate(ctx, uuid.MustParse(teamID), uuid.New())
	require.NoError(t, err)

	var forcedRLS, publicCanExecute, runtimeCanExecute bool
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT memory_spaces.relforcerowsecurity,
			       has_function_privilege('public', 'dense_mem_active_space_generation(uuid, uuid)', 'EXECUTE'),
			       has_function_privilege(?::name, 'dense_mem_active_space_generation(uuid, uuid)', 'EXECUTE')
			FROM pg_class AS memory_spaces
			WHERE memory_spaces.oid = 'public.memory_spaces'::regclass
		`, ledgerTestRole).Row().Scan(&forcedRLS, &publicCanExecute, &runtimeCanExecute)
	}))
	assert.True(t, forcedRLS)
	assert.False(t, publicCanExecute)
	assert.True(t, runtimeCanExecute)

	var storedGeneration int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT generation
			FROM memory_spaces
			WHERE team_id = ?::uuid AND id = ?::uuid
		`, teamID, space.ID).Row().Scan(&storedGeneration)
	}))
	require.Positive(t, storedGeneration)

	var activeGeneration int64
	require.NoError(t, appDB.Raw(`
		SELECT dense_mem_active_space_generation(?::uuid, ?::uuid)
	`, teamID, space.ID).Row().Scan(&activeGeneration))
	assert.Equal(t, storedGeneration, activeGeneration)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET lifecycle_state = 'sealed', generation = generation + 1,
			    sealed_at = now(), updated_at = now()
			WHERE team_id = ?::uuid AND id = ?::uuid
		`, teamID, space.ID).Error
	}))

	var sealedGeneration sql.NullInt64
	require.NoError(t, appDB.Raw(`
		SELECT dense_mem_active_space_generation(?::uuid, ?::uuid)
	`, teamID, space.ID).Row().Scan(&sealedGeneration))
	assert.False(t, sealedGeneration.Valid)
}
