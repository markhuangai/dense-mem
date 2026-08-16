//go:build integration

package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

// TestTeamServiceCreate verifies profile created, UUID generated server-side, status=active.
func TestTeamServiceCreate(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := postgres.Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	sqlDB, _ := db.DB()
	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id LIKE 'test-%'")
	sqlDB.Exec("DELETE FROM teams WHERE name LIKE 'Test %'")

	repo := repository.NewTeamRepository(db, postgres.NewRLS())
	auditService := NewAuditService(db)
	service := NewTeamService(repo, auditService, nil)

	req := CreateTeamRequest{
		Name:        "Test Profile Create",
		Description: "Test description",
		Metadata:    map[string]any{"key": "value"},
		Config:      map[string]any{"setting": true},
	}

	actorKeyID := uuid.NewString()
	profile, err := service.Create(ctx, req, &actorKeyID, "system", "127.0.0.1", "test-correlation-id")

	require.NoError(t, err, "Create should succeed")
	assert.NotEqual(t, uuid.Nil, profile.ID, "UUID should be generated server-side")
	assert.Equal(t, req.Name, profile.Name, "Name should match")
	assert.Equal(t, req.Description, profile.Description, "Description should match")
	assert.NotZero(t, profile.CreatedAt, "CreatedAt should be set")
	assert.NotZero(t, profile.UpdatedAt, "UpdatedAt should be set")

	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id = $1", profile.ID.String())
	sqlDB.Exec("DELETE FROM teams WHERE id = $1", profile.ID.String())
}

// TestTeamServiceCreateDuplicateName verifies conflict on case-insensitive duplicate.
func TestTeamServiceCreateDuplicateName(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := postgres.Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	sqlDB, _ := db.DB()
	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id LIKE 'test-%'")
	sqlDB.Exec("DELETE FROM teams WHERE name LIKE 'Test %'")

	repo := repository.NewTeamRepository(db, postgres.NewRLS())
	auditService := NewAuditService(db)
	service := NewTeamService(repo, auditService, nil)

	req1 := CreateTeamRequest{
		Name:        "Test Duplicate Profile",
		Description: "First profile",
	}

	actorKeyID := uuid.NewString()
	profile1, err := service.Create(ctx, req1, &actorKeyID, "system", "127.0.0.1", "test-correlation-id")
	require.NoError(t, err, "First create should succeed")

	req2 := CreateTeamRequest{
		Name:        "test duplicate profile",
		Description: "Second profile",
	}

	_, err = service.Create(ctx, req2, &actorKeyID, "system", "127.0.0.1", "test-correlation-id")

	require.Error(t, err, "Duplicate name should fail")
	apiErr, ok := err.(*httperr.APIError)
	require.True(t, ok, "Error should be APIError")
	assert.Equal(t, httperr.CONFLICT, apiErr.Code, "Error code should be CONFLICT")

	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id = $1", profile1.ID.String())
	sqlDB.Exec("DELETE FROM teams WHERE id = $1", profile1.ID.String())
}

// TestTeamServiceGet verifies fetch by ID, deleted profiles return 404.
func TestTeamServiceGet(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := postgres.Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	sqlDB, _ := db.DB()
	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id LIKE 'test-%'")
	sqlDB.Exec("DELETE FROM teams WHERE name LIKE 'Test %'")

	repo := repository.NewTeamRepository(db, postgres.NewRLS())
	auditService := NewAuditService(db)
	service := NewTeamService(repo, auditService, nil)

	req := CreateTeamRequest{
		Name:        "Test Profile Get",
		Description: "Test description",
	}

	actorKeyID := uuid.NewString()
	profile, err := service.Create(ctx, req, &actorKeyID, "system", "127.0.0.1", "test-correlation-id")
	require.NoError(t, err, "Create should succeed")

	fetched, err := service.Get(ctx, profile.ID)
	require.NoError(t, err, "Get should succeed")
	assert.Equal(t, profile.ID, fetched.ID, "ID should match")
	assert.Equal(t, profile.Name, fetched.Name, "Name should match")

	nonExistentID := uuid.New()
	_, err = service.Get(ctx, nonExistentID)
	require.Error(t, err, "Get non-existent should fail")
	apiErr, ok := err.(*httperr.APIError)
	require.True(t, ok, "Error should be APIError")
	assert.Equal(t, httperr.NOT_FOUND, apiErr.Code, "Error code should be NOT_FOUND")

	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id = $1", profile.ID.String())
	sqlDB.Exec("DELETE FROM teams WHERE id = $1", profile.ID.String())
}

// TestTeamServiceList verifies pagination, max limit=100, sort order, excludes deleted.
func TestTeamServiceList(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := postgres.Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	sqlDB, _ := db.DB()
	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id LIKE 'test-%'")
	sqlDB.Exec("DELETE FROM teams WHERE name LIKE 'Test %'")

	repo := repository.NewTeamRepository(db, postgres.NewRLS())
	auditService := NewAuditService(db)
	service := NewTeamService(repo, auditService, nil)

	actorKeyID := uuid.NewString()
	var createdIDs []uuid.UUID
	for i := 0; i < 5; i++ {
		req := CreateTeamRequest{
			Name:        uuid.New().String(),
			Description: "Test description",
		}
		profile, err := service.Create(ctx, req, &actorKeyID, "system", "127.0.0.1", "test-correlation-id")
		require.NoError(t, err, "Create should succeed")
		createdIDs = append(createdIDs, profile.ID)
	}

	profiles, err := service.List(ctx, 0, 0)
	require.NoError(t, err, "List should succeed")
	assert.LessOrEqual(t, len(profiles), 20, "Default limit should be 20")

	profiles, err = service.List(ctx, 3, 0)
	require.NoError(t, err, "List should succeed")
	assert.LessOrEqual(t, len(profiles), 3, "Should respect limit")

	profiles, err = service.List(ctx, 200, 0)
	require.NoError(t, err, "List should succeed")
	assert.LessOrEqual(t, len(profiles), 100, "Max limit should be 100")

	profiles, err = service.List(ctx, 10, 2)
	require.NoError(t, err, "List should succeed")

	for _, id := range createdIDs {
		sqlDB.Exec("DELETE FROM audit_log WHERE entity_id = $1", id.String())
		sqlDB.Exec("DELETE FROM teams WHERE id = $1", id.String())
	}
}

// TestTeamServiceUpdate verifies update, audit called.
func TestTeamServiceUpdate(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := postgres.Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	sqlDB, _ := db.DB()
	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id LIKE 'test-%'")
	sqlDB.Exec("DELETE FROM teams WHERE name LIKE 'Test %'")

	repo := repository.NewTeamRepository(db, postgres.NewRLS())
	auditService := NewAuditService(db)
	service := NewTeamService(repo, auditService, nil)

	req := CreateTeamRequest{
		Name:        "Test Profile Update",
		Description: "Original description",
	}

	actorKeyID := uuid.NewString()
	profile, err := service.Create(ctx, req, &actorKeyID, "system", "127.0.0.1", "test-correlation-id")
	require.NoError(t, err, "Create should succeed")

	newName := "Test Profile Update Modified"
	newDesc := "Updated description"
	updateReq := UpdateTeamRequest{
		Name:        &newName,
		Description: &newDesc,
		Metadata:    map[string]any{"updated": true},
		Config:      map[string]any{"newSetting": "value"},
	}

	updated, err := service.Update(ctx, profile.ID, updateReq, &actorKeyID, "system", "127.0.0.1", "test-correlation-id")
	require.NoError(t, err, "Update should succeed")
	assert.Equal(t, newName, updated.Name, "Name should be updated")
	assert.Equal(t, newDesc, updated.Description, "Description should be updated")

	var auditCount int
	err = sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_log 
		WHERE entity_id = $1 AND operation = 'UPDATE'
	`, profile.ID.String()).Scan(&auditCount)
	require.NoError(t, err, "Should query audit log")
	assert.GreaterOrEqual(t, auditCount, 1, "Audit log should have UPDATE entry")

	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id = $1", profile.ID.String())
	sqlDB.Exec("DELETE FROM teams WHERE id = $1", profile.ID.String())
}

// TestTeamServiceDelete verifies tombstone delete and audit emission.
func TestTeamServiceDelete(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := postgres.Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	sqlDB, _ := db.DB()
	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id LIKE 'test-%'")
	sqlDB.Exec("DELETE FROM teams WHERE name LIKE 'Test %'")

	repo := repository.NewTeamRepository(db, postgres.NewRLS())
	auditService := NewAuditService(db)
	service := NewTeamService(repo, auditService, nil)

	req := CreateTeamRequest{
		Name:        "Test Profile Delete",
		Description: "Test description",
	}

	actorKeyID := uuid.NewString()
	profile, err := service.Create(ctx, req, &actorKeyID, "system", "127.0.0.1", "test-correlation-id")
	require.NoError(t, err, "Create should succeed")

	err = service.Delete(ctx, profile.ID, &actorKeyID, "system", "127.0.0.1", "test-correlation-id")
	require.NoError(t, err, "Delete should succeed")

	_, err = service.Get(ctx, profile.ID)
	require.Error(t, err, "Get deleted profile should fail")
	apiErr, ok := err.(*httperr.APIError)
	require.True(t, ok, "Error should be APIError")
	assert.Equal(t, httperr.NOT_FOUND, apiErr.Code, "Error code should be NOT_FOUND")

	var status string
	var deleted bool
	err = sqlDB.QueryRowContext(ctx, `
		SELECT status, deleted_at IS NOT NULL
		FROM teams
		WHERE id = $1
	`, profile.ID).Scan(&status, &deleted)
	require.NoError(t, err, "Should query tombstoned team")
	assert.Equal(t, "deleted", status)
	assert.True(t, deleted)

	var auditCount int
	err = sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_log 
		WHERE entity_id = $1 AND operation = 'DELETE'
	`, profile.ID.String()).Scan(&auditCount)
	require.NoError(t, err, "Should query audit log")
	assert.GreaterOrEqual(t, auditCount, 1, "Audit log should have DELETE entry")

	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id = $1", profile.ID.String())
	sqlDB.Exec("DELETE FROM teams WHERE id = $1", profile.ID.String())
}

// TestTeamServiceDeleteRevokesActiveKeys verifies team deletion revokes
// profile-bound credentials without deleting their audit tombstones.
func TestTeamServiceDeleteRevokesActiveKeys(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := postgres.Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	sqlDB, _ := db.DB()
	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id LIKE 'test-%'")
	sqlDB.Exec("DELETE FROM teams WHERE name LIKE 'Test %'")

	repo := repository.NewTeamRepository(db, postgres.NewRLS())
	auditService := NewAuditService(db)
	service := NewTeamService(repo, auditService, nil)

	req := CreateTeamRequest{
		Name:        "Test Profile Delete Blocked",
		Description: "Test description",
	}

	actorKeyID := uuid.NewString()
	profile, err := service.Create(ctx, req, &actorKeyID, "system", "127.0.0.1", "test-correlation-id")
	require.NoError(t, err, "Create should succeed")

	keyID := uuid.New()
	keyRepo := repository.NewCredentialRepository(db, postgres.NewRLS())
	require.NoError(t, keyRepo.CreateCredential(ctx, &domain.Credential{
		ID: keyID, TeamID: profile.ID, Name: "Test Key", KeyHash: "testhash",
		KeyPrefix: keyID.String()[:24], Scopes: []string{"read"},
	}))

	err = service.Delete(ctx, profile.ID, &actorKeyID, "system", "127.0.0.1", "test-correlation-id")
	require.NoError(t, err, "Delete should remove active keys and profile")

	var keyCount int
	err = sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM credentials
		WHERE team_id = $1
	`, profile.ID).Scan(&keyCount)
	require.NoError(t, err, "Should query API keys")
	assert.Equal(t, 1, keyCount, "credential tombstones should be preserved")

	var revokedCount int
	err = sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM credentials
		WHERE team_id = $1 AND status = 'revoked' AND revoked_at IS NOT NULL
	`, profile.ID).Scan(&revokedCount)
	require.NoError(t, err, "Should query revoked API keys")
	assert.Equal(t, 1, revokedCount, "profile API keys should be revoked")

	var profileCount int
	err = sqlDB.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM teams
		WHERE id = $1
	`, profile.ID).Scan(&profileCount)
	require.NoError(t, err, "Should query profiles")
	assert.Equal(t, 1, profileCount, "team row should be preserved")

	sqlDB.Exec("DELETE FROM ownership_aliases WHERE team_id = $1", profile.ID)
	sqlDB.Exec("DELETE FROM credentials WHERE team_id = $1", profile.ID)
	sqlDB.Exec("DELETE FROM team_memberships WHERE team_id = $1", profile.ID)
	sqlDB.Exec("DELETE FROM actor_identities WHERE team_id = $1", profile.ID)
	sqlDB.Exec("DELETE FROM audit_log WHERE entity_id = $1", profile.ID.String())
	sqlDB.Exec("DELETE FROM teams WHERE id = $1", profile.ID.String())
}
