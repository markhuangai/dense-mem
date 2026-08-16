//go:build integration
// +build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRLSPoliciesCredentialIsolation(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupRLSTestDB(t)
	defer cleanup()

	rls := NewRLS()
	teamA := createTestTeam(t, db, "Team A")
	teamB := createTestTeam(t, db, "Team B")
	credentialA := createTestCredential(t, db, teamA, "Profile A")
	credentialB := createTestCredential(t, db, teamB, "Profile B")

	var count int64
	err := rls.WithTeamTx(ctx, db, teamA.String(), func(tx *gorm.DB) error {
		return tx.Model(&Credential{}).Count(&count).Error
	})
	if err != nil {
		t.Fatalf("failed to query as team A: %v", err)
	}
	if count != 1 {
		t.Fatalf("team A should see exactly 1 credential, got %d", count)
	}

	var found Credential
	err = rls.WithTeamTx(ctx, db, teamA.String(), func(tx *gorm.DB) error {
		return tx.First(&found, "id = ?", credentialB).Error
	})
	if err == nil {
		t.Fatal("team A should not read team B's credential")
	}

	err = rls.WithTeamTx(ctx, db, teamB.String(), func(tx *gorm.DB) error {
		return tx.Model(&Credential{}).Count(&count).Error
	})
	if err != nil {
		t.Fatalf("failed to query as team B: %v", err)
	}
	if count != 1 {
		t.Fatalf("team B should see exactly 1 credential, got %d", count)
	}

	err = rls.WithTeamTx(ctx, db, teamB.String(), func(tx *gorm.DB) error {
		return tx.First(&found, "id = ?", credentialA).Error
	})
	if err == nil {
		t.Fatal("team B should not read team A's credential")
	}
}

func TestRLSPoliciesSystemSeesAll(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupRLSTestDB(t)
	defer cleanup()

	rls := NewRLS()
	teamA := createTestTeam(t, db, "Team A")
	teamB := createTestTeam(t, db, "Team B")
	createTestCredential(t, db, teamA, "Profile A")
	createTestCredential(t, db, teamB, "Profile B")

	var count int64
	err := rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Model(&Credential{}).Count(&count).Error
	})
	if err != nil {
		t.Fatalf("failed to query credentials as system transaction: %v", err)
	}
	if count != 2 {
		t.Fatalf("system transaction should see exactly 2 credentials, got %d", count)
	}

	err = rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Model(&Team{}).Count(&count).Error
	})
	if err != nil {
		t.Fatalf("failed to query teams as system transaction: %v", err)
	}
	if count < 2 {
		t.Fatalf("system transaction should see at least 2 teams, got %d", count)
	}
}

func TestRLSPoliciesAuditLogAppendable(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupRLSTestDB(t)
	defer cleanup()

	rls := NewRLS()
	teamID := createTestTeam(t, db, "Audit Team")
	profileID := createTestCredential(t, db, teamID, "Audit Profile")

	err := rls.WithTeamTx(ctx, db, teamID.String(), func(tx *gorm.DB) error {
		return tx.Create(&AuditLog{
			ID:             uuid.New(),
			TeamID:         &teamID,
			ActorProfileID: &profileID,
			Operation:      "test_operation",
			EntityType:     "test_entity",
			EntityID:       "test-123",
			ActorRole:      "member",
			Timestamp:      time.Now(),
		}).Error
	})
	if err != nil {
		t.Fatalf("team transaction should insert audit log: %v", err)
	}

	err = rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Create(&AuditLog{
			ID:         uuid.New(),
			TeamID:     &teamID,
			Operation:  "system_test_operation",
			EntityType: "test_entity",
			EntityID:   "test-456",
			ActorRole:  "system",
			Timestamp:  time.Now(),
		}).Error
	})
	if err != nil {
		t.Fatalf("system transaction should insert audit log: %v", err)
	}

	var count int64
	err = rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Model(&AuditLog{}).Count(&count).Error
	})
	if err != nil {
		t.Fatalf("failed to count audit logs: %v", err)
	}
	if count < 2 {
		t.Fatalf("system transaction should see at least 2 audit log entries, got %d", count)
	}
}

func TestWithTeamTxSetsLegacyProfileSetting(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupRLSTestDB(t)
	defer cleanup()

	rls := NewRLS()
	teamID := uuid.New().String()

	err := rls.WithTeamTx(ctx, db, teamID, func(tx *gorm.DB) error {
		var currentProfileID string
		var currentTxMode string

		if err := tx.Raw("SELECT current_setting('app.current_profile_id', true)").Scan(&currentProfileID).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT current_setting('app.tx_mode', true)").Scan(&currentTxMode).Error; err != nil {
			return err
		}

		if currentProfileID != teamID {
			t.Fatalf("expected legacy profile setting %s, got %s", teamID, currentProfileID)
		}
		if currentTxMode != "team" {
			t.Fatalf("expected tx_mode team, got %s", currentTxMode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("transaction failed: %v", err)
	}

	var currentProfileID string
	var currentTxMode string
	if err := db.Raw("SELECT current_setting('app.current_profile_id', true)").Scan(&currentProfileID).Error; err != nil {
		t.Fatalf("failed to check profile_id outside transaction: %v", err)
	}
	if currentProfileID != "" {
		t.Fatalf("profile_id should be empty outside transaction, got %s", currentProfileID)
	}
	if err := db.Raw("SELECT current_setting('app.tx_mode', true)").Scan(&currentTxMode).Error; err != nil {
		t.Fatalf("failed to check tx_mode outside transaction: %v", err)
	}
	if currentTxMode != "" {
		t.Fatalf("tx_mode should be empty outside transaction, got %s", currentTxMode)
	}
}

func TestWithSystemTx(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupRLSTestDB(t)
	defer cleanup()

	rls := NewRLS()
	err := rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		var currentProfileID string
		var currentTxMode string

		if err := tx.Raw("SELECT current_setting('app.current_profile_id', true)").Scan(&currentProfileID).Error; err != nil {
			return err
		}
		if err := tx.Raw("SELECT current_setting('app.tx_mode', true)").Scan(&currentTxMode).Error; err != nil {
			return err
		}

		if currentProfileID != "" {
			t.Fatalf("expected empty profile_id for system transaction, got %s", currentProfileID)
		}
		if currentTxMode != "system" {
			t.Fatalf("expected tx_mode system, got %s", currentTxMode)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("transaction failed: %v", err)
	}
}

func TestReadOnlyRepeatableReadTransactionOptions(t *testing.T) {
	ctx := context.Background()
	db, cleanup := setupRLSTestDB(t)
	defer cleanup()

	rls := NewRLS()
	teamID := createTestTeam(t, db, "Read-only team")
	otherTeamID := createTestTeam(t, db, "Other read-only team")
	createTestCredential(t, db, teamID, "Authorized profile")
	createTestCredential(t, db, otherTeamID, "Foreign profile")
	var isolation, readOnly string
	var visibleProfiles int64
	err := rls.WithTeamReadOnlyRepeatableTx(ctx, db, teamID.String(), func(tx *gorm.DB) error {
		if err := tx.Raw("SHOW transaction_isolation").Scan(&isolation).Error; err != nil {
			return err
		}
		if err := tx.Raw("SHOW transaction_read_only").Scan(&readOnly).Error; err != nil {
			return err
		}
		if err := tx.Model(&Credential{}).Count(&visibleProfiles).Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE teams SET updated_at = updated_at WHERE id = ?", teamID).Error
	})

	if err == nil {
		t.Fatal("read-only repeatable-read transaction should reject writes")
	}
	if isolation != "repeatable read" {
		t.Fatalf("expected repeatable read isolation, got %q", isolation)
	}
	if readOnly != "on" {
		t.Fatalf("expected read-only transaction, got %q", readOnly)
	}
	if visibleProfiles != 1 {
		t.Fatalf("expected one team-scoped profile, got %d", visibleProfiles)
	}
}

func setupRLSTestDB(t *testing.T) (*gorm.DB, func()) {
	t.Helper()

	dsn := GetTestDSN()
	if dsn == "" {
		t.Skip("set DATABASE_URL to run RLS integration tests")
	}

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to connect to test database: %v", err)
	}

	ctx := context.Background()
	migrator, err := NewMigrator(db)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}
	if err := migrator.RunUp(ctx); err != nil {
		t.Fatalf("failed to run migrations: %v", err)
	}

	rls := NewRLS()
	if err := rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec("TRUNCATE ownership_aliases, membership_grants, credentials, team_memberships, identity_external_links, actor_identities, teams, audit_log CASCADE").Error
	}); err != nil {
		t.Fatalf("failed to truncate fixture tables before test: %v", err)
	}

	cleanup := func() {
		if err := rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
			return tx.Exec("TRUNCATE ownership_aliases, membership_grants, credentials, team_memberships, identity_external_links, actor_identities, teams, audit_log CASCADE").Error
		}); err != nil {
			t.Logf("warning: cleanup truncate failed: %v", err)
		}
		if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
			_ = sqlDB.Close()
		}
	}

	return db, cleanup
}

type Team struct {
	ID          uuid.UUID  `gorm:"type:uuid;primary_key"`
	Name        string     `gorm:"type:varchar(100);not null"`
	Description string     `gorm:"type:text;not null;default:''"`
	Metadata    string     `gorm:"type:jsonb;not null;default:'{}'"`
	Config      string     `gorm:"type:jsonb;not null;default:'{}'"`
	Status      string     `gorm:"type:varchar(20);not null;default:'active'"`
	CreatedAt   time.Time  `gorm:"type:timestamptz;not null;default:now()"`
	UpdatedAt   time.Time  `gorm:"type:timestamptz;not null;default:now()"`
	DeletedAt   *time.Time `gorm:"type:timestamptz"`
}

func (Team) TableName() string {
	return "teams"
}

type Credential struct {
	ID     uuid.UUID `gorm:"type:uuid;primary_key"`
	TeamID uuid.UUID `gorm:"type:uuid;not null"`
}

func (Credential) TableName() string {
	return "credentials"
}

type AuditLog struct {
	ID             uuid.UUID  `gorm:"type:uuid;primary_key"`
	TeamID         *uuid.UUID `gorm:"type:uuid"`
	Timestamp      time.Time  `gorm:"type:timestamptz;not null;default:now()"`
	Operation      string     `gorm:"type:varchar(64);not null"`
	EntityType     string     `gorm:"type:varchar(64);not null"`
	EntityID       string     `gorm:"type:text;not null"`
	BeforePayload  *string    `gorm:"type:jsonb"`
	AfterPayload   *string    `gorm:"type:jsonb"`
	ActorProfileID *uuid.UUID `gorm:"type:uuid"`
	ActorRole      string     `gorm:"type:varchar(20)"`
	ClientIP       *string    `gorm:"type:inet"`
	CorrelationID  *string    `gorm:"type:text"`
	Metadata       string     `gorm:"type:jsonb;not null;default:'{}'"`
}

func (AuditLog) TableName() string {
	return "audit_log"
}

func createTestTeam(t *testing.T, db *gorm.DB, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	team := Team{
		ID:          id,
		Name:        name,
		Description: "Test team",
		Metadata:    "{}",
		Config:      "{}",
		Status:      "active",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	err := NewRLS().WithTeamTx(context.Background(), db, id.String(), func(tx *gorm.DB) error {
		return tx.Create(&team).Error
	})
	if err != nil {
		t.Fatalf("failed to create test team: %v", err)
	}
	return id
}

func createTestCredential(t *testing.T, db *gorm.DB, teamID uuid.UUID, name string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	err := NewRLS().WithTeamTx(context.Background(), db, teamID.String(), func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO actor_identities (id, kind, team_id, display_name)
			VALUES (?, 'api_client', ?, ?)
		`, id, teamID, name).Error; err != nil {
			return err
		}
		var membershipID uuid.UUID
		if err := tx.Raw(`
			INSERT INTO team_memberships (actor_identity_id, team_id, maximum_grants)
			VALUES (?, ?, ARRAY['read']::text[])
			RETURNING id
		`, id, teamID).Row().Scan(&membershipID); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO membership_grants (membership_id, grant_name)
			VALUES (?, 'read')
		`, membershipID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO credentials (
				id, actor_identity_id, team_id, key_hash, key_prefix, key_suffix, name, scopes
			) VALUES (?, ?, ?, ?, ?, ?, ?, ARRAY['read']::text[])
		`, id, id, teamID, "test_hash_"+id.String(), id.String()[:24], id.String()[:6], name).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id, credential_id)
			VALUES (?, ?, ?, ?)
		`, teamID, id, id, id).Error
	})
	if err != nil {
		t.Fatalf("failed to create test credential: %v", err)
	}
	return id
}
