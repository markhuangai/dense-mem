//go:build integration

package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcnetwork "github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"gorm.io/gorm"

	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func newAuditService(db *gorm.DB) *auditapp.Service {
	return auditapp.New(auditpostgres.NewStore(db, postgres.NewRLS()))
}

// testConfig implements postgres.ConfigProvider for testing.
type testConfig struct {
	dsn string
}

func (c *testConfig) GetPostgresDSN() string {
	return c.dsn
}

// skipIfNoPostgres checks if postgres is available and returns a cleanup function.
func skipIfNoPostgres(t *testing.T, ctx context.Context) (string, func()) {
	dsn := postgres.GetTestDSN()
	baseCleanup := func() {}
	if dsn == "" {
		if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
			t.Skip("set DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 to run disposable service PostgreSQL integration tests")
		}
		containerOptions := []testcontainers.ContainerCustomizer{
			tcpostgres.WithDatabase("testdb"),
			tcpostgres.WithUsername("testuser"),
			tcpostgres.WithPassword("testpass"),
			testcontainers.WithWaitStrategy(
				wait.ForLog("database system is ready to accept connections").
					WithOccurrence(2).
					WithStartupTimeout(30 * time.Second),
			),
		}
		if networkName := os.Getenv("DENSE_MEM_CI_PRECHECK_NETWORK"); networkName != "" {
			containerOptions = append(containerOptions, tcnetwork.WithNetworkName([]string{"postgres"}, networkName))
		}
		container, err := tcpostgres.Run(ctx,
			"pgvector/pgvector:0.8.2-pg18-trixie",
			containerOptions...,
		)
		if err != nil {
			t.Skipf("Postgres not available: %v", err)
		}
		if os.Getenv("DENSE_MEM_CI_PRECHECK_NETWORK") != "" {
			dsn = "postgres://testuser:testpass@postgres:5432/testdb?sslmode=disable"
		} else {
			dsn, err = container.ConnectionString(ctx, "sslmode=disable")
		}
		if err != nil {
			_ = container.Terminate(ctx)
			t.Skipf("Postgres connection string unavailable: %v", err)
		}
		baseCleanup = func() { _ = container.Terminate(context.Background()) }
	}

	// Test connection
	db, err := postgres.Open(ctx, &testConfig{dsn: dsn})
	if err != nil {
		t.Skipf("Could not connect to postgres: %v", err)
	}

	cleanup := func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
		baseCleanup()
	}

	// Clean up any existing test data
	sqlDB, err := db.DB()
	if err == nil {
		// Delete test data but keep schema
		sqlDB.Exec("DELETE FROM audit_log WHERE entity_id LIKE 'test-%'")
		sqlDB.Exec("DELETE FROM credentials WHERE key_prefix LIKE 'test%'")
		sqlDB.Exec("DELETE FROM teams WHERE name LIKE 'Test %'")
	}

	return dsn, cleanup
}

// TestAuditLogAppendOnlyTriggerBlocksUpdate verifies UPDATE on audit_log raises an error.
func TestAuditLogAppendOnlyTriggerBlocksUpdate(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	cfg := &testConfig{dsn: dsn}

	db, err := postgres.Open(ctx, cfg)
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")

	// Run up migrations including 004_audit_immutability
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	sqlDB, err := db.DB()
	require.NoError(t, err, "should get underlying sql.DB")

	// Create a test profile first
	var profileID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO teams (id, name, description, metadata, config, status)
		VALUES (gen_random_uuid(), 'Test Profile for Update Block', '', '{}'::jsonb, '{}'::jsonb, 'active')
		RETURNING id
	`).Scan(&profileID)
	require.NoError(t, err, "should create test profile")

	// Create an audit log entry
	var auditLogID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO audit_log (id, team_id, operation, entity_type, entity_id)
		VALUES (gen_random_uuid(), $1, 'CREATE', 'test', 'test-update-block')
		RETURNING id
	`, profileID).Scan(&auditLogID)
	require.NoError(t, err, "should create audit log entry")

	// Attempt to UPDATE the audit log entry - should fail
	_, err = sqlDB.ExecContext(ctx, `
		UPDATE audit_log SET operation = 'MODIFIED' WHERE id = $1
	`, auditLogID)
	assert.Error(t, err, "UPDATE on audit_log should be blocked by trigger")
	assert.Contains(t, err.Error(), "append-only", "error should mention append-only")
}

// TestAuditLogAppendOnlyTriggerBlocksDelete verifies DELETE on audit_log raises an error.
func TestAuditLogAppendOnlyTriggerBlocksDelete(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	cfg := &testConfig{dsn: dsn}

	db, err := postgres.Open(ctx, cfg)
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")

	// Run up migrations including 004_audit_immutability
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	sqlDB, err := db.DB()
	require.NoError(t, err, "should get underlying sql.DB")

	// Create a test profile first
	var profileID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO teams (id, name, description, metadata, config, status)
		VALUES (gen_random_uuid(), 'Test Profile for Delete Block', '', '{}'::jsonb, '{}'::jsonb, 'active')
		RETURNING id
	`).Scan(&profileID)
	require.NoError(t, err, "should create test profile")

	// Create an audit log entry
	var auditLogID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO audit_log (id, team_id, operation, entity_type, entity_id)
		VALUES (gen_random_uuid(), $1, 'CREATE', 'test', 'test-delete-block')
		RETURNING id
	`, profileID).Scan(&auditLogID)
	require.NoError(t, err, "should create audit log entry")

	// Attempt to DELETE the audit log entry - should fail
	_, err = sqlDB.ExecContext(ctx, `DELETE FROM audit_log WHERE id = $1`, auditLogID)
	assert.Error(t, err, "DELETE on audit_log should be blocked by trigger")
	assert.Contains(t, err.Error(), "append-only", "error should mention append-only")
}

// TestAuditServiceAppend verifies an entry is written with correct fields.
func TestAuditServiceAppend(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	cfg := &testConfig{dsn: dsn}

	db, err := postgres.Open(ctx, cfg)
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")

	// Run up migrations
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	// Create audit service
	auditService := newAuditService(db)

	// Create a test profile
	sqlDB, err := db.DB()
	require.NoError(t, err, "should get underlying sql.DB")

	var profileID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO teams (id, name, description, metadata, config, status)
		VALUES (gen_random_uuid(), 'Test Profile for Service', '', '{}'::jsonb, '{}'::jsonb, 'active')
		RETURNING id
	`).Scan(&profileID)
	require.NoError(t, err, "should create test profile")

	// Create an API key for the actor
	var keyID string
	err = sqlDB.QueryRowContext(ctx, `SELECT gen_random_uuid()::text`).Scan(&keyID)
	require.NoError(t, err, "should create test credential identity")

	// Append an audit log entry
	entry := accessservice.AuditLogEntry{
		ProfileID:     &profileID,
		Operation:     "CREATE",
		EntityType:    "test_entity",
		EntityID:      "test-entity-123",
		AfterPayload:  map[string]interface{}{"name": "Test Entity", "value": 42},
		ActorKeyID:    &keyID,
		ActorRole:     "standard",
		ClientIP:      "192.168.1.1",
		CorrelationID: "corr-123-456",
		Metadata:      map[string]interface{}{"source": "test"},
	}

	err = auditService.Append(ctx, entry)
	require.NoError(t, err, "Append should succeed")

	// Verify the entry was written
	var retrievedProfileID, retrievedOperation, retrievedEntityType, retrievedEntityID string
	var retrievedActorRole, retrievedClientIP, retrievedCorrelationID string
	var retrievedActorKeyID sql.NullString

	err = sqlDB.QueryRowContext(ctx, `
		SELECT team_id, operation, entity_type, entity_id, actor_profile_id, actor_role, client_ip, correlation_id
		FROM audit_log
		WHERE entity_id = 'test-entity-123'
	`).Scan(&retrievedProfileID, &retrievedOperation, &retrievedEntityType, &retrievedEntityID,
		&retrievedActorKeyID, &retrievedActorRole, &retrievedClientIP, &retrievedCorrelationID)
	require.NoError(t, err, "should retrieve audit log entry")

	assert.Equal(t, profileID, retrievedProfileID, "team_id should match")
	assert.Equal(t, "CREATE", retrievedOperation, "operation should match")
	assert.Equal(t, "test_entity", retrievedEntityType, "entity_type should match")
	assert.Equal(t, "test-entity-123", retrievedEntityID, "entity_id should match")
	assert.Equal(t, keyID, retrievedActorKeyID.String, "actor_key_id should match")
	assert.Equal(t, "standard", retrievedActorRole, "actor_role should match")
	assert.Equal(t, "192.168.1.1", retrievedClientIP, "client_ip should match")
	assert.Equal(t, "corr-123-456", retrievedCorrelationID, "correlation_id should match")
}

func TestAuditServiceListHidesSealedPrivateSpaceRows(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	db, err := postgres.Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err)
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, m.RunUp(ctx))
	rlss := postgres.NewRLS()
	teamID := uuid.New()
	privateSpaceID := uuid.New()
	ownerCredentialID := uuid.New()
	require.NoError(t, rlss.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO teams (id, name)
			VALUES (?, ?)
		`, teamID, "audit-private-"+teamID.String()).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO memory_spaces (id, team_id, kind, owner_credential_id)
			VALUES (?, ?, 'credential_private', ?)
		`, privateSpaceID, teamID, ownerCredentialID).Error
	}))

	auditService := newAuditService(db)
	teamIDString := teamID.String()
	privateSpaceIDString := privateSpaceID.String()
	require.NoError(t, auditService.Append(ctx, accessservice.AuditLogEntry{
		ID:            uuid.NewString(),
		ProfileID:     &teamIDString,
		MemorySpaceID: &privateSpaceIDString,
		Operation:     "PRIVATE_CREATE",
		EntityType:    "private_fixture",
		EntityID:      "test-private-audit-row",
		AfterPayload:  map[string]interface{}{"sentinel": "private"},
	}))
	require.NoError(t, auditService.Append(ctx, accessservice.AuditLogEntry{
		ID:           uuid.NewString(),
		ProfileID:    &teamIDString,
		Operation:    "SHARED_CREATE",
		EntityType:   "shared_fixture",
		EntityID:     "test-shared-audit-row",
		AfterPayload: map[string]interface{}{"sentinel": "shared"},
	}))

	listContext := requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID,
		AllowedSpaces: []domain.MemorySpaceAccess{{
			ID:         privateSpaceID,
			Kind:       domain.MemorySpaceCredentialPrivate,
			Generation: 1,
		}},
	})
	entries, total, err := auditService.List(listContext, teamIDString, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, entries, 2)

	require.NoError(t, rlss.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET generation = 2, lifecycle_state = 'sealed'
			WHERE id = ? AND team_id = ?
		`, privateSpaceID, teamID).Error
	}))

	entries, total, err = auditService.List(listContext, teamIDString, 20, 0)
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, entries, 1)
	assert.Equal(t, "test-shared-audit-row", entries[0].EntityID)
}

func TestAuditServiceAppendUsesContextClientIPWhenEntryBlank(t *testing.T) {
	ctx := requestctx.WithClientIP(context.Background(), "192.168.1.101")

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
	require.NoError(t, m.RunUp(ctx), "RunUp should succeed")

	auditService := newAuditService(db)
	sqlDB, err := db.DB()
	require.NoError(t, err, "should get underlying sql.DB")

	var profileID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO teams (id, name, description, metadata, config, status)
		VALUES (gen_random_uuid(), 'Test Profile for Context Client IP', '', '{}'::jsonb, '{}'::jsonb, 'active')
		RETURNING id
	`).Scan(&profileID)
	require.NoError(t, err, "should create test profile")

	entry := accessservice.AuditLogEntry{
		ProfileID:  &profileID,
		Operation:  "CREATE",
		EntityType: "test_entity",
		EntityID:   "test-context-client-ip",
	}
	require.NoError(t, auditService.Append(ctx, entry), "Append should succeed")

	var retrievedClientIP string
	err = sqlDB.QueryRowContext(ctx, `
		SELECT client_ip::text
		FROM audit_log
		WHERE entity_id = 'test-context-client-ip'
	`).Scan(&retrievedClientIP)
	require.NoError(t, err, "should retrieve audit log entry")
	assert.Equal(t, "192.168.1.101/32", retrievedClientIP, "client_ip should come from context")
}

func TestAuditServiceAppendStoresNullClientIPWhenMissing(t *testing.T) {
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
	require.NoError(t, m.RunUp(ctx), "RunUp should succeed")

	auditService := newAuditService(db)
	sqlDB, err := db.DB()
	require.NoError(t, err, "should get underlying sql.DB")

	var profileID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO teams (id, name, description, metadata, config, status)
		VALUES (gen_random_uuid(), 'Test Profile for Null Client IP', '', '{}'::jsonb, '{}'::jsonb, 'active')
		RETURNING id
	`).Scan(&profileID)
	require.NoError(t, err, "should create test profile")

	entry := accessservice.AuditLogEntry{
		ProfileID:  &profileID,
		Operation:  "CREATE",
		EntityType: "test_entity",
		EntityID:   "test-null-client-ip",
		ClientIP:   " ",
	}
	require.NoError(t, auditService.Append(ctx, entry), "Append should succeed")

	var retrievedClientIP sql.NullString
	err = sqlDB.QueryRowContext(ctx, `
		SELECT client_ip::text
		FROM audit_log
		WHERE entity_id = 'test-null-client-ip'
	`).Scan(&retrievedClientIP)
	require.NoError(t, err, "should retrieve audit log entry")
	assert.False(t, retrievedClientIP.Valid, "client_ip should be SQL NULL")
}

// TestAuditServiceRedactsSecrets verifies key_hash, encrypted_secret, raw key, embedding absent from payloads.
func TestAuditServiceRedactsSecrets(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	cfg := &testConfig{dsn: dsn}

	db, err := postgres.Open(ctx, cfg)
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")

	// Run up migrations
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	// Create audit service
	auditService := newAuditService(db)

	// Create a test profile
	sqlDB, err := db.DB()
	require.NoError(t, err, "should get underlying sql.DB")

	var profileID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO teams (id, name, description, metadata, config, status)
		VALUES (gen_random_uuid(), 'Test Profile for Redaction', '', '{}'::jsonb, '{}'::jsonb, 'active')
		RETURNING id
	`).Scan(&profileID)
	require.NoError(t, err, "should create test profile")

	// Append an audit log entry with sensitive fields
	entry := accessservice.AuditLogEntry{
		ProfileID:  &profileID,
		Operation:  "CREATE",
		EntityType: "api_key",
		EntityID:   "test-redaction-123",
		AfterPayload: map[string]interface{}{
			"name":             "Test Key",
			"key_hash":         "super_secret_hash_abc123",
			"encrypted_secret": "encrypted_super_secret_xyz789",
			"api_key":          "raw-api-key-value",
			"raw_key":          "raw-key-value",
			"secret":           "secret-value",
			"password":         "password123",
			"token":            "bearer-token-abc",
			"embedding":        []float32{0.1, 0.2, 0.3},
			"embeddings":       [][]float32{{0.1, 0.2}, {0.3, 0.4}},
			"Authorization":    "Bearer payload-token",
			"nested":           map[string]interface{}{"access_token": "payload-access-token", "refreshToken": "payload-refresh-token", "safe": "keep"},
			"clientSecret":     "payload-client-secret",
			"team_id":          profileID,    // This should be preserved
			"label":            "test-label", // This should be preserved
		},
		ActorRole:     "system",
		ClientIP:      "192.168.1.1",
		CorrelationID: "corr-redact-789",
		Metadata: map[string]interface{}{
			"source":        "unit",
			"apiKey":        "metadata-api-key",
			"Authorization": "Bearer metadata-token",
			"clientSecret":  "metadata-client-secret",
			"nested":        map[string]interface{}{"refresh_token": "metadata-refresh-token", "refreshToken": "metadata-refresh-token-camel", "safe": "keep"},
		},
	}

	err = auditService.Append(ctx, entry)
	require.NoError(t, err, "Append should succeed")

	// Verify the entry was written and check the payload
	var afterPayload string
	var metadataPayload string
	err = sqlDB.QueryRowContext(ctx, `
			SELECT after_payload::text, metadata::text
			FROM audit_log
			WHERE entity_id = 'test-redaction-123'
		`).Scan(&afterPayload, &metadataPayload)
	require.NoError(t, err, "should retrieve audit log entry")

	// Verify sensitive fields are NOT present
	assert.NotContains(t, afterPayload, "super_secret_hash_abc123", "key_hash should be redacted")
	assert.NotContains(t, afterPayload, "encrypted_super_secret_xyz789", "encrypted_secret should be redacted")
	assert.NotContains(t, afterPayload, "raw-api-key-value", "api_key should be redacted")
	assert.NotContains(t, afterPayload, "raw-key-value", "raw_key should be redacted")
	assert.NotContains(t, afterPayload, "secret-value", "secret should be redacted")
	assert.NotContains(t, afterPayload, "password123", "password should be redacted")
	assert.NotContains(t, afterPayload, "bearer-token-abc", "token should be redacted")
	assert.NotContains(t, afterPayload, "Bearer payload-token", "authorization should be redacted")
	assert.NotContains(t, afterPayload, "payload-access-token", "nested access token should be redacted")
	assert.NotContains(t, afterPayload, "payload-refresh-token", "nested refreshToken should be redacted")
	assert.NotContains(t, afterPayload, "payload-client-secret", "clientSecret should be redacted")
	assert.NotContains(t, afterPayload, "0.1", "embedding should be redacted")
	assert.NotContains(t, afterPayload, "embeddings", "embeddings field should be redacted")
	assert.NotContains(t, metadataPayload, "metadata-api-key", "metadata apiKey should be redacted")
	assert.NotContains(t, metadataPayload, "Bearer metadata-token", "metadata authorization should be redacted")
	assert.NotContains(t, metadataPayload, "metadata-client-secret", "metadata clientSecret should be redacted")
	assert.NotContains(t, metadataPayload, "metadata-refresh-token", "nested metadata refresh token should be redacted")
	assert.NotContains(t, metadataPayload, "metadata-refresh-token-camel", "nested metadata refreshToken should be redacted")

	// Verify legitimate fields ARE present
	assert.Contains(t, afterPayload, "Test Key", "name should be preserved")
	assert.Contains(t, afterPayload, profileID, "team_id should be preserved")
	assert.Contains(t, afterPayload, "test-label", "label should be preserved")
	assert.Contains(t, metadataPayload, "unit", "metadata source should be preserved")
	assert.Contains(t, metadataPayload, "keep", "safe nested metadata should be preserved")
}

// verifyAuditEntry is a helper to verify audit log entries were created
func verifyAuditEntry(t *testing.T, sqlDB *sql.DB, ctx context.Context, entityID string, expectedOp string) {
	var operation string
	err := sqlDB.QueryRowContext(ctx, `
		SELECT operation
		FROM audit_log
		WHERE entity_id = $1
		ORDER BY timestamp DESC
		LIMIT 1
	`, entityID).Scan(&operation)
	require.NoError(t, err, "should retrieve audit log entry for entity %s", entityID)
	assert.Equal(t, expectedOp, operation, "operation should match for entity %s", entityID)
}

// TestAuditServiceHelperMethods verifies each named helper produces a correctly typed entry.
func TestAuditServiceHelperMethods(t *testing.T) {
	ctx := context.Background()

	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	cfg := &testConfig{dsn: dsn}

	db, err := postgres.Open(ctx, cfg)
	require.NoError(t, err, "Open should succeed")
	defer func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			sqlDB.Close()
		}
	}()

	m, err := postgres.NewMigrator(db)
	require.NoError(t, err, "NewMigrator should succeed")

	// Run up migrations
	err = m.RunUp(ctx)
	require.NoError(t, err, "RunUp should succeed")

	// Create audit service
	auditService := newAuditService(db)

	// Create a test profile and API key
	sqlDB, err := db.DB()
	require.NoError(t, err, "should get underlying sql.DB")

	var profileID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO teams (id, name, description, metadata, config, status)
		VALUES (gen_random_uuid(), 'Test Profile for Helpers', '', '{}'::jsonb, '{}'::jsonb, 'active')
		RETURNING id
	`).Scan(&profileID)
	require.NoError(t, err, "should create test profile")

	var keyID string
	err = sqlDB.QueryRowContext(ctx, `SELECT gen_random_uuid()::text`).Scan(&keyID)
	require.NoError(t, err, "should create test credential identity")

	correlationID := "corr-helper-001"
	clientIP := "10.0.0.1"

	// Test TeamCreated
	err = auditService.TeamCreated(ctx, profileID,
		map[string]interface{}{"name": "New Profile", "status": "active"},
		&keyID, "standard", clientIP, correlationID)
	require.NoError(t, err, "TeamCreated should succeed")
	verifyAuditEntry(t, sqlDB, ctx, profileID, "CREATE")

	// Test TeamUpdated
	err = auditService.TeamUpdated(ctx, profileID,
		map[string]interface{}{"name": "Old Name"},
		map[string]interface{}{"name": "New Name"},
		&keyID, "standard", clientIP, correlationID)
	require.NoError(t, err, "TeamUpdated should succeed")
	verifyAuditEntry(t, sqlDB, ctx, profileID, "UPDATE")

	// Test TeamDeleteBlocked
	err = auditService.TeamDeleteBlocked(ctx, profileID,
		map[string]interface{}{"name": "Profile to Delete", "status": "active"},
		&keyID, "standard", clientIP, correlationID, "profile has active resources")
	require.NoError(t, err, "TeamDeleteBlocked should succeed")
	verifyAuditEntry(t, sqlDB, ctx, profileID, "DELETE_BLOCKED")

	// Test TeamDeleted
	err = auditService.TeamDeleted(ctx, profileID,
		map[string]interface{}{"name": "Deleted Profile", "status": "deleted"},
		&keyID, "standard", clientIP, correlationID)
	require.NoError(t, err, "TeamDeleted should succeed")
	verifyAuditEntry(t, sqlDB, ctx, profileID, "DELETE")

	// Test CredentialCreated
	keyID2 := "test-key-id-002"
	err = auditService.CredentialCreated(ctx, &profileID, keyID2,
		map[string]interface{}{"label": "New API Key", "role": "standard"},
		&keyID, "standard", clientIP, correlationID)
	require.NoError(t, err, "CredentialCreated should succeed")
	verifyAuditEntry(t, sqlDB, ctx, keyID2, "CREATE")

	// Test CredentialRevoked
	err = auditService.CredentialRevoked(ctx, &profileID, keyID2,
		map[string]interface{}{"label": "API Key", "role": "standard", "revoked_at": "2024-01-01"},
		&keyID, "standard", clientIP, correlationID)
	require.NoError(t, err, "CredentialRevoked should succeed")
	verifyAuditEntry(t, sqlDB, ctx, keyID2, "REVOKE")

	// Test AuthFailure
	err = auditService.AuthFailure(ctx, &profileID, "api_key", "invalid-key-id",
		map[string]interface{}{"reason": "invalid_key"},
		clientIP, correlationID)
	require.NoError(t, err, "AuthFailure should succeed")
	verifyAuditEntry(t, sqlDB, ctx, "invalid-key-id", "AUTH_FAILURE")

	// Test CrossTeamDenied
	targetProfileID := uuid.NewString()
	err = auditService.CrossTeamDenied(ctx, profileID, targetProfileID, "read_profile",
		map[string]interface{}{"requested_resource": "sensitive_data"},
		clientIP, correlationID)
	require.NoError(t, err, "CrossTeamDenied should succeed")
	verifyAuditEntry(t, sqlDB, ctx, targetProfileID, "CROSS_PROFILE_DENIED")

	// Test RateLimited
	err = auditService.RateLimited(ctx, &profileID, "api_call",
		map[string]interface{}{"limit": 100, "window": "1m"},
		clientIP, correlationID)
	require.NoError(t, err, "RateLimited should succeed")
	verifyAuditEntry(t, sqlDB, ctx, correlationID, "RATE_LIMITED")

	// Test SystemQuery
	err = auditService.SystemQuery(ctx, "list_all_profiles",
		map[string]interface{}{"filters": map[string]string{"status": "active"}},
		&keyID, "system", clientIP, correlationID)
	require.NoError(t, err, "SystemQuery should succeed")
	verifyAuditEntry(t, sqlDB, ctx, "list_all_profiles", "SYSTEM_QUERY")

	// Test InvariantViolation
	err = auditService.InvariantViolation(ctx, "profile", profileID, "profile_count_mismatch",
		map[string]interface{}{"expected": 1, "actual": 0},
		clientIP, correlationID)
	require.NoError(t, err, "InvariantViolation should succeed")
	verifyAuditEntry(t, sqlDB, ctx, profileID, "INVARIANT_VIOLATION")
}

// TestAuditServiceListIsolatesTeamsForNonSuperuser proves the audit read path
// applies PostgreSQL RLS rather than relying on the service-side team filter.
func TestAuditServiceListIsolatesTeamsForNonSuperuser(t *testing.T) {
	ctx := context.Background()
	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()

	adminDB, err := postgres.Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err)
	adminSQL, err := adminDB.DB()
	require.NoError(t, err)
	defer adminSQL.Close()

	migrator, err := postgres.NewMigrator(adminDB)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(ctx))
	rlss := postgres.NewRLS()
	roleName := "densemem_audit_rls_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	rolePassword := "densemem_audit_rls_password"
	roleCreated := false
	defer func() {
		if roleCreated {
			err := rlss.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
				return tx.Exec(fmt.Sprintf(`
					REVOKE ALL ON ALL TABLES IN SCHEMA public FROM %s;
					REVOKE ALL ON SCHEMA public FROM %s;
					DROP OWNED BY %s;
					DROP ROLE IF EXISTS %s;
				`, roleName, roleName, roleName, roleName)).Error
			})
			require.NoError(t, err, "clean up audit RLS test role")
		}
	}()
	require.NoError(t, rlss.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(fmt.Sprintf(`
			CREATE ROLE %s LOGIN PASSWORD '%s' NOSUPERUSER NOBYPASSRLS;
			GRANT USAGE ON SCHEMA public TO %s;
			GRANT SELECT ON ALL TABLES IN SCHEMA public TO %s;
		`, roleName, rolePassword, roleName, roleName)).Error
	}))
	roleCreated = true

	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		t.Skip("audit RLS integration test requires a URL-form PostgreSQL DSN")
	}
	parsed.User = url.UserPassword(roleName, rolePassword)
	appDB, err := postgres.Open(ctx, &testConfig{dsn: parsed.String()})
	require.NoError(t, err)
	appSQL, err := appDB.DB()
	require.NoError(t, err)
	defer appSQL.Close()

	teamA := uuid.New()
	teamB := uuid.New()
	require.NoError(t, rlss.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec("INSERT INTO teams (id, name) VALUES (?, ?), (?, ?)", teamA, "Test Audit RLS A", teamB, "Test Audit RLS B").Error; err != nil {
			return err
		}
		return nil
	}))

	adminAudit := newAuditService(adminDB)
	teamAString := teamA.String()
	teamBString := teamB.String()
	require.NoError(t, adminAudit.Append(ctx, accessservice.AuditLogEntry{ProfileID: &teamAString, Operation: "CREATE", EntityType: "test", EntityID: "test-audit-rls-a"}))
	require.NoError(t, adminAudit.Append(ctx, accessservice.AuditLogEntry{ProfileID: &teamBString, Operation: "CREATE", EntityType: "test", EntityID: "test-audit-rls-b"}))

	entries, total, err := newAuditService(appDB).List(ctx, teamAString, 20, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, entries, 1)
	require.Equal(t, "test-audit-rls-a", entries[0].EntityID)

	var visibleEntityIDs []string
	require.NoError(t, rlss.WithTeamTx(ctx, appDB, teamAString, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT entity_id FROM audit_log ORDER BY entity_id`).Scan(&visibleEntityIDs).Error
	}))
	require.Equal(t, []string{"test-audit-rls-a"}, visibleEntityIDs)
}

func TestAuditExportTransactionFrontierPaginationAndScope(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 90*time.Second)
	defer stop()
	dsn, cleanup := skipIfNoPostgres(t, ctx)
	defer cleanup()
	db, err := postgres.Open(ctx, &testConfig{dsn: dsn})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	migrator, err := postgres.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(ctx))
	teamA, teamB, teamC := uuid.New(), uuid.New(), uuid.New()
	for _, team := range []uuid.UUID{teamA, teamB, teamC} {
		_, err = sqlDB.ExecContext(ctx, "INSERT INTO teams(id,name) VALUES ($1,$2)", team, "Test Audit Export "+team.String())
		require.NoError(t, err)
	}
	service := newAuditService(db)
	teamString := teamA.String()
	_, err = sqlDB.ExecContext(ctx, "INSERT INTO audit_log(id,operation,entity_type,entity_id) SELECT gen_random_uuid(),'CREATE','profile',gen_random_uuid()::text FROM generate_series(1,12)")
	require.NoError(t, err)
	oldID, slowID, fastID, rolledID, afterRollbackID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	require.NoError(t, service.Append(ctx, accessservice.AuditLogEntry{ID: oldID, ProfileID: &teamString, Operation: "CREATE", EntityType: "profile", EntityID: teamString, AfterPayload: map[string]interface{}{"prompt": "export-content-canary"}, CorrelationID: "export-correlation-canary"}))
	read := func(request auditapp.ExportRequest) ([]auditapp.ExportEvent, auditapp.ExportCheckpoint) {
		t.Helper()
		raw, err := service.ExportPage(ctx, request)
		require.NoError(t, err)
		require.LessOrEqual(t, len(raw), auditapp.ExportMaxBytes)
		require.NotContains(t, string(raw), "export-content-canary")
		require.NotContains(t, string(raw), "export-correlation-canary")
		decoder := json.NewDecoder(bytes.NewReader(raw))
		events := make([]auditapp.ExportEvent, 0)
		var checkpoint auditapp.ExportCheckpoint
		for {
			var line json.RawMessage
			err := decoder.Decode(&line)
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			var kind struct {
				Type string `json:"type"`
			}
			require.NoError(t, json.Unmarshal(line, &kind))
			if kind.Type == "event" {
				var event auditapp.ExportEvent
				require.NoError(t, json.Unmarshal(line, &event))
				events = append(events, event)
			} else {
				require.Equal(t, "checkpoint", kind.Type)
				require.NoError(t, json.Unmarshal(line, &checkpoint))
				require.ErrorIs(t, decoder.Decode(new(any)), io.EOF)
				break
			}
		}
		require.Equal(t, len(events), checkpoint.Events)
		require.NotEmpty(t, checkpoint.Cursor)
		return events, checkpoint
	}
	before, checkpoint := read(auditapp.ExportRequest{TeamID: &teamA, Limit: 1})
	require.Len(t, before, 1)
	require.Equal(t, oldID, before[0].ID)
	slow, err := sqlDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer slow.Rollback()
	_, err = slow.ExecContext(ctx, "INSERT INTO audit_log(id,team_id,timestamp,operation,entity_type,entity_id) VALUES($1,$2::uuid,$3,'CREATE','profile',$2::uuid::text)", slowID, teamA, time.Now().Add(time.Hour))
	require.NoError(t, err)
	teamBString := teamB.String()
	require.NoError(t, service.Append(ctx, accessservice.AuditLogEntry{ID: fastID, ProfileID: &teamBString, Timestamp: time.Now().Add(-time.Hour), Operation: "CREATE", EntityType: "profile", EntityID: teamBString}))
	hidden, delayed := read(auditapp.ExportRequest{TeamID: &teamB})
	require.Empty(t, hidden)
	require.True(t, delayed.Delayed)
	_, delayed = read(auditapp.ExportRequest{TeamID: &teamA, Cursor: checkpoint.Cursor})
	require.Equal(t, 0, delayed.Events)
	require.NoError(t, slow.Commit())
	resumed, resumedCheckpoint := read(auditapp.ExportRequest{TeamID: &teamA, Cursor: checkpoint.Cursor, Limit: 1})
	require.Len(t, resumed, 1)
	require.Equal(t, slowID, resumed[0].ID)
	b, _ := read(auditapp.ExportRequest{TeamID: &teamB})
	require.Len(t, b, 1)
	require.Equal(t, fastID, b[0].ID)
	c, _ := read(auditapp.ExportRequest{TeamID: &teamC})
	require.Empty(t, c)
	_, err = service.ExportPage(ctx, auditapp.ExportRequest{TeamID: &teamB, Cursor: resumedCheckpoint.Cursor})
	require.ErrorIs(t, err, auditapp.ErrInvalidExport)
	owned := map[string]bool{oldID: true, slowID: true, fastID: true, rolledID: true, afterRollbackID: true}
	readOwnedInstance := func(cursor string) ([]string, string) {
		t.Helper()
		var ids []string
		for {
			events, next := read(auditapp.ExportRequest{Cursor: cursor, Limit: 1})
			for _, event := range events {
				if owned[event.ID] {
					ids = append(ids, event.ID)
				}
			}
			cursor = next.Cursor
			if !next.More {
				return ids, cursor
			}
		}
	}
	instanceIDs, cursor := readOwnedInstance("")
	require.Equal(t, []string{oldID, slowID, fastID}, instanceIDs)
	replay, _ := read(auditapp.ExportRequest{TeamID: &teamA, Cursor: checkpoint.Cursor, Limit: 1})
	require.Equal(t, resumed, replay)
	rollback, err := sqlDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer rollback.Rollback()
	_, err = rollback.ExecContext(ctx, "INSERT INTO audit_log(id,team_id,operation,entity_type,entity_id) VALUES($1,$2::uuid,'CREATE','profile',$2::uuid::text)", rolledID, teamB)
	require.NoError(t, err)
	require.NoError(t, service.Append(ctx, accessservice.AuditLogEntry{ID: afterRollbackID, ProfileID: &teamBString, Operation: "CREATE", EntityType: "profile", EntityID: teamBString}))
	require.NoError(t, rollback.Rollback())
	after, _ := readOwnedInstance(cursor)
	require.Equal(t, []string{afterRollbackID}, after)
	lock, err := sqlDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer lock.Rollback()
	_, err = lock.ExecContext(ctx, "LOCK TABLE audit_log IN ACCESS EXCLUSIVE MODE")
	require.NoError(t, err)
	for attempt := 0; attempt < 5; attempt++ {
		cancelled, cancel := context.WithCancel(ctx)
		defer cancel()
		finished := make(chan error, 1)
		go func() {
			_, err := service.ExportPage(cancelled, auditapp.ExportRequest{TeamID: &teamA})
			finished <- err
		}()
		blocked := func() bool {
			var waiting bool
			err := sqlDB.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT insertion_xid::text,%')").Scan(&waiting)
			return assert.NoError(t, err) && waiting
		}
		require.Eventually(t, blocked, 3*time.Second, 10*time.Millisecond)
		cancel()
		select {
		case err := <-finished:
			require.ErrorIs(t, err, context.Canceled)
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled audit export did not return")
		}
		require.Eventually(t, func() bool { return !blocked() }, 3*time.Second, 10*time.Millisecond)
	}
	require.NoError(t, lock.Rollback())
	_, err = service.ExportPage(ctx, auditapp.ExportRequest{TeamID: &teamA})
	require.NoError(t, err)
	_, err = sqlDB.ExecContext(ctx, "UPDATE audit_log SET insertion_xid = '0'::xid8 WHERE id=$1", oldID)
	require.ErrorContains(t, err, "append-only")
	role := "densemem_audit_export_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	_, err = sqlDB.ExecContext(ctx, fmt.Sprintf("CREATE ROLE %s LOGIN PASSWORD 'audit-export-test' NOSUPERUSER NOBYPASSRLS; GRANT USAGE ON SCHEMA public TO %s; GRANT SELECT ON ALL TABLES IN SCHEMA public TO %s", role, role, role))
	require.NoError(t, err)
	defer func() {
		_, err := sqlDB.ExecContext(context.Background(), fmt.Sprintf("DROP OWNED BY %s; DROP ROLE %s", role, role))
		require.NoError(t, err)
	}()
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	parsed.User = url.UserPassword(role, "audit-export-test")
	appDB, err := postgres.Open(ctx, &testConfig{dsn: parsed.String()})
	require.NoError(t, err)
	appSQL, err := appDB.DB()
	require.NoError(t, err)
	defer appSQL.Close()
	appService := newAuditService(appDB)
	raw, err := appService.ExportPage(ctx, auditapp.ExportRequest{TeamID: &teamA})
	require.NoError(t, err)
	require.Contains(t, string(raw), oldID)
	require.NotContains(t, string(raw), fastID)
	raw, err = appService.ExportPage(ctx, auditapp.ExportRequest{})
	require.NoError(t, err)
	require.Contains(t, string(raw), oldID)
	require.Contains(t, string(raw), fastID)
}
