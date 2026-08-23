package serverapp

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

const (
	rememberBoundaryTestRole     = "densemem_remember_test"
	rememberBoundaryTestPassword = "densemem_remember_test"
)

func TestRememberServiceAndLedgerAdapterRespectTeamProfileIsolation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupRememberBoundaryDB(t)
	defer cleanup()

	teamA := rememberBoundaryTeam(t, adminDB, rls, "remember-boundary-team-a")
	ownerA := rememberBoundaryProfile(t, adminDB, rls, teamA, "remember-boundary-owner-a")
	ownerB := rememberBoundaryProfile(t, adminDB, rls, teamA, "remember-boundary-owner-b")
	teamB := rememberBoundaryTeam(t, adminDB, rls, "remember-boundary-team-b")
	ownerC := rememberBoundaryProfile(t, adminDB, rls, teamB, "remember-boundary-owner-c")

	service := rememberapp.NewService(rememberapp.Dependencies{
		Intake: newRememberLedgerAdapter(repository.NewLedgerRepository(appDB, rls)),
	})

	ownerASubmission := rememberBoundaryRemember(t, service, teamA, ownerA, "remember-boundary-a")
	ownerBSubmission := rememberBoundaryRemember(t, service, teamA, ownerB, "remember-boundary-b")
	ownerCSubmission := rememberBoundaryRemember(t, service, teamB, ownerC, "remember-boundary-c")

	for name, target := range map[string]struct {
		teamID, ownerID, submissionID string
	}{
		"owner A reads own team A submission": {teamA, ownerA, ownerASubmission},
		"owner B reads own team A submission": {teamA, ownerB, ownerBSubmission},
		"owner C reads own team B submission": {teamB, ownerC, ownerCSubmission},
	} {
		t.Run(name, func(t *testing.T) {
			status, err := service.GetSubmissionStatus(rememberBoundaryActorContext(target.teamID, target.ownerID), rememberapp.GetSubmissionStatusRequest{SubmissionID: target.submissionID})
			require.NoError(t, err)
			require.Equal(t, target.submissionID, status.SubmissionID)
			require.NotNil(t, status.Evidence)
			require.NotNil(t, status.Errors)
			require.NotNil(t, status.Degradations)
		})
	}

	for name, target := range map[string]struct {
		teamID, ownerID, submissionID string
	}{
		"same team different owner": {teamA, ownerB, ownerASubmission},
		"different team":            {teamB, ownerC, ownerASubmission},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := service.GetSubmissionStatus(rememberBoundaryActorContext(target.teamID, target.ownerID), rememberapp.GetSubmissionStatusRequest{SubmissionID: target.submissionID})
			var apiErr *httperr.APIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, httperr.NOT_FOUND, apiErr.Code)
		})
	}

	require.NoError(t, rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE knowledge_ingests
			SET metadata = jsonb_set(metadata, '{contract_version}', to_jsonb('dense-mem.v2.5'::text))
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamA, ownerASubmission).Error
	}))
	_, err := service.GetSubmissionStatus(
		rememberBoundaryActorContext(teamA, ownerA),
		rememberapp.GetSubmissionStatusRequest{SubmissionID: ownerASubmission},
	)
	var apiErr *httperr.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, httperr.NOT_FOUND, apiErr.Code)

}

func rememberBoundaryRemember(t *testing.T, service rememberapp.Service, teamID, ownerID, key string) string {
	t.Helper()
	result, err := service.Remember(rememberBoundaryActorContext(teamID, ownerID), rememberapp.RememberRequest{
		IdempotencyKey: key,
		Evidence:       []rememberapp.RememberEvidenceInput{{Content: "Remember boundary evidence remains exact and owner-scoped."}},
		RelationshipHints: []map[string]any{{
			"evidence_indices": []any{0},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotEmpty(t, result.SubmissionID)
	return result.SubmissionID
}

func rememberBoundaryActorContext(teamID, ownerID string) context.Context {
	return requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID:       uuid.MustParse(teamID),
		OwnerID:      uuid.MustParse(ownerID),
		IdentityID:   uuid.New(),
		MembershipID: uuid.New(),
		Role:         "member",
		AuthMethod:   "api_key",
		Grants:       []string{"read", "write"},
	})
}

func setupRememberBoundaryDB(t *testing.T) (*gorm.DB, *gorm.DB, *storagepostgres.RLS, func()) {
	t.Helper()
	dsn, baseCleanup := rememberBoundaryDSN(t)

	db, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	migrator, err := storagepostgres.NewMigrator(db)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(context.Background()))

	rls := storagepostgres.NewRLS()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(fmt.Sprintf(`
			DO $$
			BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '%[1]s') THEN
					CREATE ROLE %[1]s LOGIN PASSWORD '%[2]s' NOSUPERUSER NOBYPASSRLS;
				ELSE
					ALTER ROLE %[1]s WITH LOGIN PASSWORD '%[2]s' NOSUPERUSER NOBYPASSRLS;
				END IF;
			END $$;
			GRANT USAGE ON SCHEMA public TO %[1]s;
			GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO %[1]s;
			GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO %[1]s;
			GRANT EXECUTE ON FUNCTION dense_mem_active_space_generation(UUID, UUID) TO %[1]s;
		`, rememberBoundaryTestRole, rememberBoundaryTestPassword)).Error
	}))

	appDSN := rememberBoundaryAppDSN(t, dsn)
	appDB, err := gorm.Open(gormpostgres.Open(appDSN), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(`TRUNCATE teams CASCADE`).Error
	}))

	cleanup := func() {
		_ = rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
			return tx.Exec(`TRUNCATE teams CASCADE`).Error
		})
		if sqlDB, err := appDB.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
			return tx.Exec(fmt.Sprintf(`
				REASSIGN OWNED BY %[1]s TO CURRENT_USER;
				DROP OWNED BY %[1]s;
				DROP ROLE IF EXISTS %[1]s;
			`, rememberBoundaryTestRole)).Error
		})
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		baseCleanup()
	}
	return db, appDB, rls, cleanup
}

func rememberBoundaryDSN(t *testing.T) (string, func()) {
	t.Helper()
	if dsn := storagepostgres.GetTestDSN(); dsn != "" {
		if os.Getenv("DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS") != "1" {
			t.Skip("set DENSE_MEM_ALLOW_DESTRUCTIVE_POSTGRES_TESTS=1 to run destructive Remember PostgreSQL integration tests against DATABASE_URL")
		}
		return dsn, func() {}
	}
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		t.Skip("set DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 to run disposable Remember PostgreSQL integration tests")
	}

	ctx := context.Background()
	container, err := tcpostgres.Run(ctx, "pgvector/pgvector:0.8.2-pg18-trixie",
		tcpostgres.WithDatabase("testdb"), tcpostgres.WithUsername("testuser"), tcpostgres.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(30*time.Second)),
	)
	if err != nil {
		t.Fatalf("start Postgres test container: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("get Postgres test container DSN: %v", err)
	}
	return dsn, func() { _ = container.Terminate(ctx) }
}

func rememberBoundaryAppDSN(t *testing.T, dsn string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		t.Skip("Remember RLS integration tests require DATABASE_URL in URL form")
	}
	parsed.User = url.UserPassword(rememberBoundaryTestRole, rememberBoundaryTestPassword)
	return parsed.String()
}

func rememberBoundaryTeam(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, name string) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO teams (id, name, description, metadata, config) VALUES (?::uuid, ?, '', '{}'::jsonb, '{}'::jsonb)`, id, name).Error
	}))
	return id
}

func rememberBoundaryProfile(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, teamID, name string) string {
	t.Helper()
	id := uuid.NewString()
	prefix := uuid.NewString()[:24]
	require.NoError(t, repository.NewCredentialRepository(db, rls).CreateCredential(context.Background(), &domain.Credential{
		ID: uuid.MustParse(id), TeamID: uuid.MustParse(teamID), Name: name,
		KeyHash: "hash-" + id, KeyPrefix: prefix, KeySuffix: prefix[:6], Scopes: []string{"read", "write"},
	}))
	return id
}
