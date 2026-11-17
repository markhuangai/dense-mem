//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEvidenceInsertPreservesDriverFailures(t *testing.T) {
	admin, _, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	team := createLedgerTeam(t, admin, rls, "insert-driver-failures")
	owner := createLedgerProfile(t, admin, rls, team, "insert-owner")
	for _, mode := range []string{"database_cancel", "deadline", "empty_result"} {
		t.Run(mode, func(t *testing.T) {
			name := "seed_insert_" + strings.ReplaceAll(uuid.NewString(), "-", "")
			body := "RAISE EXCEPTION 'seed insert cancelled' USING ERRCODE='57014';"
			if mode == "deadline" {
				body = "PERFORM pg_sleep(5); RETURN NEW;"
			}
			if mode == "empty_result" {
				body = "RETURN NULL;"
			}
			require.NoError(t, admin.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.team_id='%s'::uuid THEN %s END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON evidence_fragments FOR EACH ROW EXECUTE FUNCTION %s()`, name, team, body, name, name)).Error)
			defer func() {
				require.NoError(t, admin.Exec(fmt.Sprintf("DROP TRIGGER %s ON evidence_fragments; DROP FUNCTION %s()", name, name)).Error)
			}()
			err := rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
				id := uuid.NewString()
				input := CreateIngestInput{TeamID: team, OwnerProfileID: owner, IngestID: id, IdempotencyKey: id, RequestHash: sha256Hex(id), Status: "completed"}
				if _, _, err := insertKnowledgeIngest(ctx, tx, input); err != nil {
					return err
				}
				insertCtx := ctx
				if mode == "deadline" {
					var cancel context.CancelFunc
					insertCtx, cancel = context.WithTimeout(ctx, 100*time.Millisecond)
					defer cancel()
				}
				_, err := insertEvidenceFragment(insertCtx, tx, input, id, 0, EvidenceInput{Content: "public seed Evidence", ContentHash: sha256Hex("public seed Evidence"), SourceType: "manual", Authority: "primary"}, nil)
				return err
			})
			require.Error(t, err)
			switch mode {
			case "database_cancel":
				var databaseError *pgconn.PgError
				require.ErrorAs(t, err, &databaseError)
				require.Equal(t, "57014", databaseError.Code)
			case "deadline":
				require.ErrorIs(t, err, context.DeadlineExceeded)
			case "empty_result":
				require.ErrorIs(t, err, sql.ErrNoRows)
			}
		})
	}
}

func TestLedgerSourceRevisionCompareAndSet(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-source")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "source-owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "source-other-owner")
	repo := NewStore(appDB, rls)

	first, err := repo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKey: "doc://policy",
		RevisionToken: "rev-1", ContentHash: "sha256:first",
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.SourceID)

	second, err := repo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKey: "doc://policy",
		RevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
		ContentHash: "sha256:second",
	})
	require.NoError(t, err)
	assert.Equal(t, first.SourceID, second.SourceID)
	assert.NotEqual(t, first.SourceRevisionID, second.SourceRevisionID)

	matching, err := repo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKey: "doc://policy",
		RevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
		ContentHash: "sha256:second",
	})
	require.NoError(t, err)
	assert.Equal(t, second.SourceRevisionID, matching.SourceRevisionID)

	_, err = repo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: otherOwnerID, SourceKey: "doc://policy",
		RevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
		ContentHash: "sha256:second",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceRevisionConflict), fmt.Sprintf("err=%v", err))

	_, err = repo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKey: "doc://policy",
		RevisionToken: "rev-3", ExpectedPreviousRevisionToken: "rev-1",
		ContentHash: "sha256:third",
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSourceRevisionConflict), fmt.Sprintf("err=%v", err))
}

func TestLedgerAuthorityConstraintsUseCanonicalValues(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "team-authority")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner-authority")

	err := rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO evidence_sources (team_id, owner_profile_id, source_key, source_kind, authority)
			VALUES (?::uuid, ?::uuid, 'doc://canonical-authority', 'document', 'unknown')
		`, teamID, ownerID).Error
	})
	require.NoError(t, err)

	err = rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO evidence_sources (team_id, owner_profile_id, source_key, source_kind, authority)
			VALUES (?::uuid, ?::uuid, 'doc://legacy-derived-authority', 'document', 'derived')
		`, teamID, ownerID).Error
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "evidence_sources_authority_check")

	_ = appDB
}
