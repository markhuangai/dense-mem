package service

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/requestctx"
)

type auditJSONArg struct {
	present map[string]interface{}
	absent  []string
}

func (a auditJSONArg) Match(value driver.Value) bool {
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return false
	}

	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		return false
	}
	for key, want := range a.present {
		if !reflect.DeepEqual(got[key], want) {
			return false
		}
	}
	for _, key := range a.absent {
		if auditJSONContainsKey(got, key) {
			return false
		}
	}
	return true
}

func auditJSONContainsKey(value interface{}, key string) bool {
	switch typed := value.(type) {
	case map[string]interface{}:
		if _, ok := typed[key]; ok {
			return true
		}
		for _, child := range typed {
			if auditJSONContainsKey(child, key) {
				return true
			}
		}
	case []interface{}:
		for _, child := range typed {
			if auditJSONContainsKey(child, key) {
				return true
			}
		}
	}
	return false
}

func newMockAuditService(t *testing.T) (*AuditServiceImpl, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)

	gormDB, err := gorm.Open(postgres.New(postgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
	})
	require.NoError(t, err)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewAuditServiceWithLogger(gormDB, logger)
	svc.rls = nil
	return svc, mock, func() {
		_ = sqlDB.Close()
	}
}

func expectAnyAuditInsert(mock sqlmock.Sqlmock) {
	args := make([]driver.Value, 14)
	for i := range args {
		args[i] = sqlmock.AnyArg()
	}
	mock.ExpectExec("INSERT INTO audit_log").WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
}

func TestAuditClientIPValueUsesEntryClientIP(t *testing.T) {
	ctx := requestctx.WithClientIP(context.Background(), "192.168.1.101")
	got := auditClientIPValue(ctx, AuditLogEntry{ClientIP: "203.0.113.10"})
	if got != "203.0.113.10" {
		t.Errorf("auditClientIPValue() = %#v; want 203.0.113.10", got)
	}
}

func TestAuditClientIPValueFallsBackToContextClientIP(t *testing.T) {
	ctx := requestctx.WithClientIP(context.Background(), "192.168.1.101")
	got := auditClientIPValue(ctx, AuditLogEntry{})
	if got != "192.168.1.101" {
		t.Errorf("auditClientIPValue() = %#v; want 192.168.1.101", got)
	}
}

func TestAuditClientIPValueReturnsNilWhenMissing(t *testing.T) {
	got := auditClientIPValue(context.Background(), AuditLogEntry{ClientIP: "   "})
	if got != nil {
		t.Errorf("auditClientIPValue() = %#v; want nil", got)
	}
}

func TestAuditServiceConstructorsInitializeDependencies(t *testing.T) {
	defaultSvc := NewAuditService(nil)
	require.NotNil(t, defaultSvc.logger)
	require.NotNil(t, defaultSvc.rls)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	customSvc := NewAuditServiceWithLogger(nil, logger)
	require.Same(t, logger, customSvc.logger)
	require.NotNil(t, customSvc.rls)
}

func TestAuditAppendRedactsPayloadsAndWritesInsert(t *testing.T) {
	svc, mock, cleanup := newMockAuditService(t)
	defer cleanup()

	profileID := "profile-1"
	actorKeyID := "key-1"
	timestamp := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	mock.ExpectExec("INSERT INTO audit_log").WithArgs(
		"audit-1",
		sqlmock.AnyArg(),
		sqlmock.AnyArg(),
		"CREATE",
		"secret_entity",
		"entity-1",
		auditJSONArg{
			present: map[string]interface{}{"name": "before"},
			absent:  []string{"secret", "token", "Authorization", "refreshToken"},
		},
		auditJSONArg{
			present: map[string]interface{}{"name": "after"},
			absent:  []string{"api_key", "embedding", "access_token", "clientSecret"},
		},
		sqlmock.AnyArg(),
		"admin",
		"203.0.113.10",
		"corr-1",
		auditJSONArg{
			present: map[string]interface{}{"source": "unit"},
			absent:  []string{"password", "apiKey", "refresh_token", "clientSecret", "refreshToken"},
		},
		sqlmock.AnyArg(),
	).WillReturnResult(sqlmock.NewResult(0, 1))

	err := svc.Append(context.Background(), AuditLogEntry{
		ID:         "audit-1",
		ProfileID:  &profileID,
		Timestamp:  timestamp,
		Operation:  "CREATE",
		EntityType: "secret_entity",
		EntityID:   "entity-1",
		BeforePayload: map[string]interface{}{
			"name":          "before",
			"secret":        "remove",
			"Authorization": "remove",
			"nested":        map[string]interface{}{"token": "remove", "refreshToken": "remove", "safe": "keep"},
		},
		AfterPayload: map[string]interface{}{
			"name":         "after",
			"api_key":      "remove",
			"access_token": "remove",
			"clientSecret": "remove",
			"embedding":    []float64{1, 2, 3},
		},
		ActorKeyID:    &actorKeyID,
		ActorRole:     "admin",
		ClientIP:      "203.0.113.10",
		CorrelationID: "corr-1",
		Metadata: map[string]interface{}{
			"source":         "unit",
			"password":       "remove",
			"apiKey":         "remove",
			"nested":         map[string]string{"refresh_token": "remove", "refreshToken": "remove", "safe": "keep"},
			"array_payloads": []map[string]interface{}{{"client_secret": "remove", "clientSecret": "remove", "name": "kept"}},
		},
	})

	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuditAppendInfersCredentialMemorySpaceWithinTeam(t *testing.T) {
	teamID := uuid.New()
	credentialID := uuid.New()
	spaceID := uuid.New()

	for _, test := range []struct {
		name           string
		memorySpaceArg any
		rows           *sqlmock.Rows
	}{
		{
			name:           "matching credential",
			memorySpaceArg: spaceID.String(),
			rows:           sqlmock.NewRows([]string{"memory_space_id"}).AddRow(spaceID.String()),
		},
		{
			name:           "deleted credential",
			memorySpaceArg: nil,
			rows:           sqlmock.NewRows([]string{"memory_space_id"}),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc, mock, cleanup := newMockAuditService(t)
			defer cleanup()

			mock.ExpectQuery("SELECT memory_space_id::text\\s+FROM credentials\\s+WHERE id = \\$1 AND team_id = \\$2").
				WithArgs(credentialID, teamID).
				WillReturnRows(test.rows)
			args := make([]driver.Value, 13)
			for index := range args {
				args[index] = sqlmock.AnyArg()
			}
			args = append(args, test.memorySpaceArg)
			mock.ExpectExec("INSERT INTO audit_log").WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))

			profileID := teamID.String()
			err := svc.Append(context.Background(), AuditLogEntry{
				ProfileID: &profileID, EntityType: "api_key", EntityID: credentialID.String(), Operation: "DELETE",
			})
			require.NoError(t, err)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAuditAppendMarshalAndDatabaseErrors(t *testing.T) {
	svc := &AuditServiceImpl{}
	err := svc.Append(context.Background(), AuditLogEntry{
		BeforePayload: map[string]interface{}{"bad": func() {}},
	})
	require.ErrorContains(t, err, "failed to marshal before_payload")

	err = svc.Append(context.Background(), AuditLogEntry{
		AfterPayload: map[string]interface{}{"bad": func() {}},
	})
	require.ErrorContains(t, err, "failed to marshal after_payload")

	err = svc.Append(context.Background(), AuditLogEntry{
		Metadata: map[string]interface{}{"bad": func() {}},
	})
	require.ErrorContains(t, err, "failed to marshal metadata")

	dbSvc, mock, cleanup := newMockAuditService(t)
	defer cleanup()
	mock.ExpectExec("INSERT INTO audit_log").WillReturnError(errors.New("insert failed"))
	err = dbSvc.Append(context.Background(), AuditLogEntry{Operation: "CREATE"})
	require.ErrorContains(t, err, "failed to append audit log entry")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuditListScansRowsAndCountsTotal(t *testing.T) {
	svc, mock, cleanup := newMockAuditService(t)
	defer cleanup()

	profileID := "profile-1"
	actorKeyID := "key-1"
	timestamp := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	rows := sqlmock.NewRows([]string{
		"id", "team_id", "timestamp", "operation", "entity_type", "entity_id",
		"before_payload", "after_payload", "actor_profile_id", "actor_role",
		"client_ip", "correlation_id", "metadata",
		"memory_space_id",
	}).AddRow(
		"audit-1",
		profileID,
		timestamp,
		"UPDATE",
		"profile",
		profileID,
		[]byte(`{"name":"before"}`),
		[]byte(`{"name":"after"}`),
		actorKeyID,
		"admin",
		"203.0.113.10",
		"corr-1",
		[]byte(`{"source":"unit"}`),
		"space-1",
	)
	mock.ExpectQuery("SELECT id, team_id, timestamp, operation, entity_type, entity_id").
		WithArgs(profileID, 2, 1).
		WillReturnRows(rows)
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM audit_log WHERE team_id = \\$1").
		WithArgs(profileID).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	entries, total, err := svc.List(context.Background(), profileID, 2, 1)

	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, entries, 1)
	require.Equal(t, "audit-1", entries[0].ID)
	require.Equal(t, profileID, *entries[0].ProfileID)
	require.Equal(t, actorKeyID, *entries[0].ActorKeyID)
	require.Equal(t, "before", entries[0].BeforePayload["name"])
	require.Equal(t, "after", entries[0].AfterPayload["name"])
	require.Equal(t, "unit", entries[0].Metadata["source"])
	require.Equal(t, "space-1", *entries[0].MemorySpaceID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuditListHandlesQueryAndCountErrors(t *testing.T) {
	svc, mock, cleanup := newMockAuditService(t)
	defer cleanup()

	mock.ExpectQuery("SELECT id, team_id, timestamp, operation, entity_type, entity_id").
		WithArgs("profile-1", 20, 0).
		WillReturnError(errors.New("query failed"))
	_, _, err := svc.List(context.Background(), "profile-1", 20, 0)
	require.ErrorContains(t, err, "failed to query audit log")
	require.NoError(t, mock.ExpectationsWereMet())

	svc, mock, cleanup = newMockAuditService(t)
	defer cleanup()
	mock.ExpectQuery("SELECT id, team_id, timestamp, operation, entity_type, entity_id").
		WithArgs("profile-1", 20, 0).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "team_id", "timestamp", "operation", "entity_type", "entity_id",
			"before_payload", "after_payload", "actor_profile_id", "actor_role",
			"client_ip", "correlation_id", "metadata",
			"memory_space_id",
		}))
	mock.ExpectQuery("SELECT COUNT\\(\\*\\) FROM audit_log WHERE team_id = \\$1").
		WithArgs("profile-1").
		WillReturnError(errors.New("count failed"))
	_, _, err = svc.List(context.Background(), "profile-1", 20, 0)
	require.ErrorContains(t, err, "failed to count audit log entries")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuditEventHelpersAppendExpectedOperations(t *testing.T) {
	svc, mock, cleanup := newMockAuditService(t)
	defer cleanup()

	profileID := "profile-1"
	keyID := "key-1"
	calls := []func() error{
		func() error {
			return svc.TeamCreated(context.Background(), profileID, map[string]interface{}{"name": "new"}, &keyID, "admin", "203.0.113.10", "corr-create")
		},
		func() error {
			return svc.TeamUpdated(context.Background(), profileID, map[string]interface{}{"name": "old"}, map[string]interface{}{"name": "new"}, &keyID, "admin", "203.0.113.10", "corr-update")
		},
		func() error {
			return svc.TeamDeleteBlocked(context.Background(), profileID, map[string]interface{}{"name": "old"}, &keyID, "admin", "203.0.113.10", "corr-block", "active keys")
		},
		func() error {
			return svc.TeamDeleted(context.Background(), profileID, map[string]interface{}{"name": "old"}, &keyID, "admin", "203.0.113.10", "corr-delete")
		},
		func() error {
			return svc.CredentialCreated(context.Background(), &profileID, keyID, map[string]interface{}{"name": "key"}, &keyID, "admin", "203.0.113.10", "corr-key-create")
		},
		func() error {
			return svc.CredentialRevoked(context.Background(), &profileID, keyID, map[string]interface{}{"name": "key"}, &keyID, "admin", "203.0.113.10", "corr-key-revoke")
		},
		func() error {
			return svc.AuthFailure(context.Background(), &profileID, "api_key", keyID, map[string]interface{}{"reason": "AUTH_INVALID"}, "203.0.113.10", "corr-auth")
		},
		func() error {
			return svc.CrossTeamDenied(context.Background(), "actor-profile", profileID, "read", nil, "203.0.113.10", "corr-cross")
		},
		func() error {
			return svc.RateLimited(context.Background(), &profileID, "POST /v1/fragments", nil, "203.0.113.10", "corr-rate")
		},
		func() error {
			return svc.SystemQuery(context.Background(), "graph", nil, &keyID, "admin", "203.0.113.10", "corr-system")
		},
		func() error {
			return svc.InvariantViolation(context.Background(), "claim", "claim-1", "missing fact", nil, "203.0.113.10", "corr-invariant")
		},
	}
	for range calls {
		expectAnyAuditInsert(mock)
	}
	for _, call := range calls {
		require.NoError(t, call())
	}
	require.NoError(t, mock.ExpectationsWereMet())
}
