//go:build integration

package service

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

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
	if dsn == "" {
		t.Skip("POSTGRES_TEST_DSN not set, skipping integration test")
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
	}

	// Clean up any existing test data
	sqlDB, err := db.DB()
	if err == nil {
		// Delete test data but keep schema
		sqlDB.Exec("DELETE FROM audit_log WHERE entity_id LIKE 'test-%'")
		sqlDB.Exec("DELETE FROM api_keys WHERE key_prefix LIKE 'test%'")
		sqlDB.Exec("DELETE FROM profiles WHERE name LIKE 'Test %'")
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
		INSERT INTO profiles (id, name, status)
		VALUES (gen_random_uuid(), 'Test Profile for Update Block', 'active')
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
		INSERT INTO profiles (id, name, status)
		VALUES (gen_random_uuid(), 'Test Profile for Delete Block', 'active')
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
	auditService := NewAuditService(db)

	// Create a test profile
	sqlDB, err := db.DB()
	require.NoError(t, err, "should get underlying sql.DB")

	var profileID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO profiles (id, name, status)
		VALUES (gen_random_uuid(), 'Test Profile for Service', 'active')
		RETURNING id
	`).Scan(&profileID)
	require.NoError(t, err, "should create test profile")

	// Create an API key for the actor
	var keyID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO api_keys (id, team_id, key_hash, key_prefix, label, scopes)
		VALUES (gen_random_uuid(), $1, 'testhash123', 'testsvc', 'service-test', ARRAY['read'])
		RETURNING id
	`, profileID).Scan(&keyID)
	require.NoError(t, err, "should create test api key")

	// Append an audit log entry
	entry := AuditLogEntry{
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
		SELECT team_id, operation, entity_type, entity_id, actor_key_id, actor_role, client_ip, correlation_id
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

	auditService := NewAuditService(db)
	sqlDB, err := db.DB()
	require.NoError(t, err, "should get underlying sql.DB")

	var profileID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO profiles (id, name, status)
		VALUES (gen_random_uuid(), 'Test Profile for Context Client IP', 'active')
		RETURNING id
	`).Scan(&profileID)
	require.NoError(t, err, "should create test profile")

	entry := AuditLogEntry{
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
	assert.Equal(t, "192.168.1.101", retrievedClientIP, "client_ip should come from context")
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

	auditService := NewAuditService(db)
	sqlDB, err := db.DB()
	require.NoError(t, err, "should get underlying sql.DB")

	var profileID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO profiles (id, name, status)
		VALUES (gen_random_uuid(), 'Test Profile for Null Client IP', 'active')
		RETURNING id
	`).Scan(&profileID)
	require.NoError(t, err, "should create test profile")

	entry := AuditLogEntry{
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
	auditService := NewAuditService(db)

	// Create a test profile
	sqlDB, err := db.DB()
	require.NoError(t, err, "should get underlying sql.DB")

	var profileID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO profiles (id, name, status)
		VALUES (gen_random_uuid(), 'Test Profile for Redaction', 'active')
		RETURNING id
	`).Scan(&profileID)
	require.NoError(t, err, "should create test profile")

	// Append an audit log entry with sensitive fields
	entry := AuditLogEntry{
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
			"team_id":          profileID,    // This should be preserved
			"label":            "test-label", // This should be preserved
		},
		ActorRole:     "system",
		ClientIP:      "192.168.1.1",
		CorrelationID: "corr-redact-789",
	}

	err = auditService.Append(ctx, entry)
	require.NoError(t, err, "Append should succeed")

	// Verify the entry was written and check the payload
	var afterPayload string
	err = sqlDB.QueryRowContext(ctx, `
		SELECT after_payload::text
		FROM audit_log
		WHERE entity_id = 'test-redaction-123'
	`).Scan(&afterPayload)
	require.NoError(t, err, "should retrieve audit log entry")

	// Verify sensitive fields are NOT present
	assert.NotContains(t, afterPayload, "super_secret_hash_abc123", "key_hash should be redacted")
	assert.NotContains(t, afterPayload, "encrypted_super_secret_xyz789", "encrypted_secret should be redacted")
	assert.NotContains(t, afterPayload, "raw-api-key-value", "api_key should be redacted")
	assert.NotContains(t, afterPayload, "raw-key-value", "raw_key should be redacted")
	assert.NotContains(t, afterPayload, "secret-value", "secret should be redacted")
	assert.NotContains(t, afterPayload, "password123", "password should be redacted")
	assert.NotContains(t, afterPayload, "bearer-token-abc", "token should be redacted")
	assert.NotContains(t, afterPayload, "0.1", "embedding should be redacted")
	assert.NotContains(t, afterPayload, "embeddings", "embeddings field should be redacted")

	// Verify legitimate fields ARE present
	assert.Contains(t, afterPayload, "Test Key", "name should be preserved")
	assert.Contains(t, afterPayload, profileID, "team_id should be preserved")
	assert.Contains(t, afterPayload, "test-label", "label should be preserved")
}

// verifyAuditEntry is a helper to verify audit log entries were created
func verifyAuditEntry(t *testing.T, sqlDB *sql.DB, ctx context.Context, entityID string, expectedOp string) {
	var operation string
	err := sqlDB.QueryRowContext(ctx, `
		SELECT operation FROM audit_log WHERE entity_id = $1
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
	auditService := NewAuditService(db)

	// Create a test profile and API key
	sqlDB, err := db.DB()
	require.NoError(t, err, "should get underlying sql.DB")

	var profileID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO profiles (id, name, status)
		VALUES (gen_random_uuid(), 'Test Profile for Helpers', 'active')
		RETURNING id
	`).Scan(&profileID)
	require.NoError(t, err, "should create test profile")

	var keyID string
	err = sqlDB.QueryRowContext(ctx, `
		INSERT INTO api_keys (id, team_id, key_hash, key_prefix, label, scopes)
		VALUES (gen_random_uuid(), $1, 'testhash456', 'testhlp', 'helper-test', ARRAY['read'])
		RETURNING id
	`, profileID).Scan(&keyID)
	require.NoError(t, err, "should create test api key")

	correlationID := "corr-helper-001"
	clientIP := "10.0.0.1"

	// Test ProfileCreated
	err = auditService.ProfileCreated(ctx, profileID,
		map[string]interface{}{"name": "New Profile", "status": "active"},
		&keyID, "standard", clientIP, correlationID)
	require.NoError(t, err, "ProfileCreated should succeed")
	verifyAuditEntry(t, sqlDB, ctx, profileID, "CREATE")

	// Test ProfileUpdated
	err = auditService.ProfileUpdated(ctx, profileID,
		map[string]interface{}{"name": "Old Name"},
		map[string]interface{}{"name": "New Name"},
		&keyID, "standard", clientIP, correlationID)
	require.NoError(t, err, "ProfileUpdated should succeed")
	verifyAuditEntry(t, sqlDB, ctx, profileID, "UPDATE")

	// Test ProfileDeleteBlocked
	err = auditService.ProfileDeleteBlocked(ctx, profileID,
		map[string]interface{}{"name": "Profile to Delete", "status": "active"},
		&keyID, "standard", clientIP, correlationID, "profile has active resources")
	require.NoError(t, err, "ProfileDeleteBlocked should succeed")
	verifyAuditEntry(t, sqlDB, ctx, profileID, "DELETE_BLOCKED")

	// Test ProfileDeleted
	err = auditService.ProfileDeleted(ctx, profileID,
		map[string]interface{}{"name": "Deleted Profile", "status": "deleted"},
		&keyID, "standard", clientIP, correlationID)
	require.NoError(t, err, "ProfileDeleted should succeed")
	verifyAuditEntry(t, sqlDB, ctx, profileID, "DELETE")

	// Test APIKeyCreated
	keyID2 := "test-key-id-002"
	err = auditService.APIKeyCreated(ctx, &profileID, keyID2,
		map[string]interface{}{"label": "New API Key", "role": "standard"},
		&keyID, "standard", clientIP, correlationID)
	require.NoError(t, err, "APIKeyCreated should succeed")
	verifyAuditEntry(t, sqlDB, ctx, keyID2, "CREATE")

	// Test APIKeyRevoked
	err = auditService.APIKeyRevoked(ctx, &profileID, keyID2,
		map[string]interface{}{"label": "API Key", "role": "standard", "revoked_at": "2024-01-01"},
		&keyID, "standard", clientIP, correlationID)
	require.NoError(t, err, "APIKeyRevoked should succeed")
	verifyAuditEntry(t, sqlDB, ctx, keyID2, "REVOKE")

	// Test AuthFailure
	err = auditService.AuthFailure(ctx, &profileID, "api_key", "invalid-key-id",
		map[string]interface{}{"reason": "invalid_key"},
		clientIP, correlationID)
	require.NoError(t, err, "AuthFailure should succeed")
	verifyAuditEntry(t, sqlDB, ctx, "invalid-key-id", "AUTH_FAILURE")

	// Test CrossProfileDenied
	targetProfileID := "target-profile-uuid"
	err = auditService.CrossProfileDenied(ctx, profileID, targetProfileID, "read_profile",
		map[string]interface{}{"requested_resource": "sensitive_data"},
		clientIP, correlationID)
	require.NoError(t, err, "CrossProfileDenied should succeed")
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
