//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/correlation"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/observability"
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres/conflictread"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestEvidenceConflictSharedReadersPreserveFieldsAndIsolation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := t.Context()
	insertSearchTestContract(t, adminDB, rls, "shared-evidence-readers", 2, "exact", "")
	team := createLedgerTeam(t, adminDB, rls, "shared-evidence-readers")
	a := createLedgerProfile(t, adminDB, rls, team, "canonical-owner")
	b := createLedgerProfile(t, adminDB, rls, team, "occurrence-owner")
	c := createLedgerProfile(t, adminDB, rls, team, "shared-reader")
	space, generation := duplicateTeamSharedSpace(t, adminDB, rls, team)
	credentials := accesspostgres.NewCredentialRepository(appDB, rls, nil)
	scoped := func(owner string) context.Context {
		key, err := credentials.GetByIDForTeam(ctx, uuid.MustParse(team), uuid.MustParse(owner))
		require.NoError(t, err)
		return requestctx.WithActor(ctx, requestctx.Actor{
			TeamID: key.TeamID, IdentityID: key.ActorIdentityID, MembershipID: key.MembershipID,
			OwnerID: key.OwnerID, CredentialID: &key.ID, AuthMethod: "api_key",
			Grants: key.Scopes, AllowedSpaces: key.MemorySpaces(),
		})
	}
	repo := newConflictKnowledgeFixtureStore(appDB, rls)
	seed := citedEvidenceRememberInput(team, a, "reader-canonical", "Élan confirms the reader fields.", "Opposing reader fields remain exact.", space, generation)
	seed.Commit.EvidenceConflictResults = nil
	commitCitedEvidenceFixture(t, ctx, repo, seed)
	input := citedEvidenceRememberInput(team, b, "reader-occurrence", seed.Evidence[0].Content, seed.Evidence[1].Content, space, generation)
	input.Commit.EvidenceConflictResults = nil
	for i := range input.Evidence {
		input.DuplicateResolutions = append(input.DuplicateResolutions, knowledgepostgres.RememberDuplicateResolution{
			EvidenceIndex: i, EvidenceID: fmt.Sprintf("evidence:%d", i), InputFragmentID: input.Evidence[i].FragmentID,
			Disposition: "reuse", CandidateFragmentID: seed.Evidence[i].FragmentID, CandidateOwnerID: a,
		})
	}
	commitCitedEvidenceFixture(t, ctx, repo, input)
	var want EvidenceConflictCaseRecord
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		want = evidenceReaderFixture(t, ctx, tx, input, seed)
		return insertEvidenceReaderFixture(ctx, tx, want)
	}))
	detail, err := repo.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: team, ConflictID: want.ConflictID})
	require.NoError(t, err)
	got := *detail.Conflict
	got.Events = nil
	require.Equal(t, want, got)
	listed, err := repo.ListEvidenceConflicts(ctx, EvidenceConflictListInput{TeamID: team})
	require.NoError(t, err)
	require.Equal(t, []EvidenceConflictCaseRecord{want}, listed.Items)
	for _, owner := range []string{a, b, c} {
		readCtx := scoped(owner)
		require.NoError(t, rls.WithTeamProfileTx(readCtx, appDB, team, owner, func(tx *gorm.DB) error {
			current, err := conflictread.LoadCurrentEvidenceConflictCase(readCtx, tx, team, want.ConflictID)
			require.NoError(t, err)
			require.Equal(t, &want, current)
			positions, err := LoadEvidenceConflictPositions(readCtx, tx, team, want.ConflictID)
			require.NoError(t, err)
			require.Equal(t, want.Positions, positions)
			recalled, err := recallpostgres.LoadRecallEvidenceConflictRecords(readCtx, tx, recallpostgres.RecallEvidenceInput{TeamID: team}, []recallpostgres.RecallEvidenceHit{{EvidenceID: seed.Evidence[0].FragmentID}})
			require.NoError(t, err)
			require.Equal(t, []EvidenceConflictCaseRecord{want}, recalled)
			return nil
		}))
	}
	foreignTeam := createLedgerTeam(t, adminDB, rls, "foreign-evidence-readers")
	foreignOwner := createLedgerProfile(t, adminDB, rls, foreignTeam, "foreign-reader")
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, foreignTeam, foreignOwner, func(tx *gorm.DB) error {
		_, err := conflictread.LoadCurrentEvidenceConflictCase(ctx, tx, team, want.ConflictID)
		require.ErrorIs(t, err, ErrEvidenceConflictNotFound)
		positions, err := conflictread.LoadEvidenceConflictPositions(ctx, tx, team, want.ConflictID)
		require.NoError(t, err)
		require.Equal(t, []EvidenceConflictPositionRecord{}, positions)
		return nil
	}))
	_, err = repo.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: foreignTeam, ConflictID: want.ConflictID})
	require.ErrorIs(t, err, ErrEvidenceConflictNotFound)
	_, err = repo.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: team, ConflictID: uuid.NewString()})
	require.ErrorIs(t, err, ErrEvidenceConflictNotFound)
	rollback := errors.New("rollback reader fixture")
	uncommitted := want
	uncommitted.ConflictID = uuid.NewString()
	uncommitted.Positions = append([]EvidenceConflictPositionRecord(nil), want.Positions...)
	for i := range uncommitted.Positions {
		uncommitted.Positions[i].ConflictID = uncommitted.ConflictID
		uncommitted.Positions[i].PositionID = uuid.NewString()
	}
	err = rls.WithTeamProfileTx(ctx, appDB, team, b, func(tx *gorm.DB) error {
		require.NoError(t, insertEvidenceReaderFixture(ctx, tx, uncommitted))
		current, err := conflictread.LoadCurrentEvidenceConflictCase(ctx, tx, team, uncommitted.ConflictID)
		require.NoError(t, err)
		require.Equal(t, &uncommitted, current)
		_, err = repo.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: team, ConflictID: uncommitted.ConflictID})
		require.ErrorIs(t, err, ErrEvidenceConflictNotFound)
		return rollback
	})
	require.ErrorIs(t, err, rollback)
	_, err = repo.GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: team, ConflictID: uncommitted.ConflictID})
	require.ErrorIs(t, err, ErrEvidenceConflictNotFound)
	resolved, err := repo.ResolveEvidenceConflict(ctx, EvidenceConflictResolutionInput{
		TeamID: team, ConflictID: want.ConflictID, ExpectedVersion: 1, Decision: "resolve",
		PreferredPositionID: want.Positions[0].PositionID, Reason: "reader parity", ActorKind: "control",
	})
	require.NoError(t, err)
	require.Equal(t, want.Positions, resolved.Positions)
	require.Equal(t, "resolved", resolved.Status)
	require.Equal(t, 2, resolved.Version)
	require.Equal(t, want.Positions[0].PositionID, resolved.PreferredPositionID)
	require.Equal(t, "reader parity", resolved.ResolutionReason)
	require.NotNil(t, resolved.ResolvedAt)
	require.Equal(t, want.CreatedAt, resolved.CreatedAt)
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, team, c, func(tx *gorm.DB) error {
		current, err := recallpostgres.LoadRecallEvidenceConflictCase(ctx, tx, team, want.ConflictID, nil)
		require.NoError(t, err)
		require.Equal(t, resolved, current)
		return nil
	}))
	credential, err := credentials.GetByIDForTeam(ctx, uuid.MustParse(team), uuid.MustParse(a))
	require.NoError(t, err)
	privateCtx := scoped(a)
	private := citedEvidenceRememberInput(team, a, "reader-private", "Private reader evidence agrees.", "Private reader evidence disagrees.", credential.MemorySpaceID.String(), credential.MemorySpaceGeneration)
	private.Commit.EvidenceConflictResults = nil
	commitCitedEvidenceFixture(t, privateCtx, repo, private)
	var privateCase EvidenceConflictCaseRecord
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		privateCase = evidenceReaderFixture(t, ctx, tx, private, private)
		return insertEvidenceReaderFixture(ctx, tx, privateCase)
	}))
	for _, owner := range []string{a, b, c} {
		readCtx := scoped(owner)
		require.NoError(t, rls.WithTeamProfileTx(readCtx, appDB, team, owner, func(tx *gorm.DB) error {
			current, err := conflictread.LoadCurrentEvidenceConflictCase(readCtx, tx, team, privateCase.ConflictID)
			positions, positionsErr := conflictread.LoadEvidenceConflictPositions(readCtx, tx, team, privateCase.ConflictID)
			require.NoError(t, positionsErr)
			if owner == a {
				require.NoError(t, err)
				require.Equal(t, &privateCase, current)
				require.Equal(t, privateCase.Positions, positions)
			} else {
				require.ErrorIs(t, err, ErrEvidenceConflictNotFound)
				require.Nil(t, current)
				require.Empty(t, positions)
			}
			return nil
		}))
	}
}

func evidenceReaderFixture(t *testing.T, ctx context.Context, tx *gorm.DB, input, canonical knowledgepostgres.SynchronousRememberCommitInput) EvidenceConflictCaseRecord {
	t.Helper()
	// pgx scans timestamptz into time.Local, so structural comparisons use that location for the same seeded instant.
	at := time.Date(2026, 11, 6, 1, 2, 3, 456000, time.UTC).Local()
	item := EvidenceConflictCaseRecord{TeamID: input.TeamID, ConflictID: uuid.NewString(), SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, Kind: "evidence_conflict", Status: "open", Version: 1, CreatedAt: at, UpdatedAt: at.Add(time.Minute)}
	for i, evidence := range input.Evidence {
		position := EvidenceConflictPositionRecord{
			ConflictID: item.ConflictID, PositionID: uuid.NewString(), PositionKey: []string{"z-last", "a-first"}[i],
			CanonicalEvidenceID: canonical.Evidence[i].FragmentID, CanonicalOwnerProfileID: canonical.OwnerProfileID,
			OccurrenceOwnerProfileID: input.OwnerProfileID, Quote: string([]rune(evidence.Content)[:5]),
			SpanStart: 0, SpanEnd: 5, Authority: evidence.Authority, Submitted: i == 0, CreatedAt: at.Add(time.Duration(i) * time.Microsecond),
		}
		require.NoError(t, tx.WithContext(ctx).Raw(`SELECT occurrence_id::text FROM evidence_occurrences WHERE team_id = ?::uuid AND ingest_id = ?::uuid AND evidence_index = ?`, input.TeamID, input.IngestID, i).Row().Scan(&position.OccurrenceID))
		item.Positions = append(item.Positions, position)
	}
	sort.Slice(item.Positions, func(i, j int) bool { return item.Positions[i].PositionKey < item.Positions[j].PositionKey })
	return item
}

func insertEvidenceReaderFixture(ctx context.Context, tx *gorm.DB, item EvidenceConflictCaseRecord) error {
	if err := tx.WithContext(ctx).Exec(`INSERT INTO evidence_conflict_cases (team_id, conflict_id, space_id, space_generation, case_key, status, version, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.TeamID, item.ConflictID, item.SpaceID, item.SpaceGeneration, "reader:"+item.ConflictID, item.Status, item.Version, item.CreatedAt, item.UpdatedAt).Error; err != nil {
		return err
	}
	for i := len(item.Positions) - 1; i >= 0; i-- {
		p := item.Positions[i]
		if err := tx.WithContext(ctx).Exec(`INSERT INTO evidence_conflict_positions (team_id, conflict_id, space_id, space_generation, position_id, position_key, canonical_evidence_id, canonical_owner_profile_id, occurrence_id, occurrence_owner_profile_id, quote, span_start, span_end, authority, submitted, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.TeamID, item.ConflictID, item.SpaceID, item.SpaceGeneration, p.PositionID, p.PositionKey, p.CanonicalEvidenceID, p.CanonicalOwnerProfileID, p.OccurrenceID, p.OccurrenceOwnerProfileID, p.Quote, p.SpanStart, p.SpanEnd, p.Authority, p.Submitted, p.CreatedAt).Error; err != nil {
			return err
		}
	}
	snapshot, err := json.Marshal(item.Positions)
	if err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`INSERT INTO evidence_conflict_events (team_id, conflict_event_id, conflict_id, space_id, space_generation, ordinal, action, status_after, case_version, actor_kind, citation_snapshot, created_at) VALUES (?, ?, ?, ?, ?, 1, 'opened', 'open', 1, 'profile', ?::jsonb, ?)`, item.TeamID, uuid.NewString(), item.ConflictID, item.SpaceID, item.SpaceGeneration, string(snapshot), item.CreatedAt).Error
}

type evidenceReaderLogSink struct{ records []observability.LogRecord }

func (s *evidenceReaderLogSink) WriteLog(_ context.Context, record observability.LogRecord) error {
	s.records = append(s.records, record)
	return nil
}

type evidenceReaderDiagnosticConfig string

func (c evidenceReaderDiagnosticConfig) GetPostgresDSN() string { return string(c) }

func TestEvidenceConflictSharedReaderQueryFailurePreservesDiagnostics(t *testing.T) {
	adminDB, fixtureDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	team := createLedgerTeam(t, adminDB, rls, "reader-query-diagnostics")
	owner := createLedgerProfile(t, adminDB, rls, team, "reader-query-owner")
	insertSearchTestContract(t, adminDB, rls, "reader-query-diagnostics", 2, "exact", "")
	space, generation := duplicateTeamSharedSpace(t, adminDB, rls, team)
	input := citedEvidenceRememberInput(team, owner, "reader-query-diagnostics", "Reader query diagnostics agree.", "Reader query diagnostics disagree.", space, generation)
	commitCitedEvidenceFixture(t, t.Context(), newConflictKnowledgeFixtureStore(fixtureDB, rls), input)
	id := conflictIDForTest(t, adminDB, rls, team)
	for _, level := range []slog.Level{observability.LevelTrace, slog.LevelInfo} {
		t.Run(level.String(), func(t *testing.T) {
			ctx := requestctx.WithActor(correlation.WithID(t.Context(), uuid.NewString()), requestctx.Actor{TeamID: uuid.MustParse(team), OwnerID: uuid.MustParse(owner)})
			root := observability.New(level)
			sink := &evidenceReaderLogSink{}
			require.NoError(t, root.AttachSink(sink))
			db, err := storagepostgres.OpenWithLogger(ctx, evidenceReaderDiagnosticConfig(fixtureDB.Dialector.(*gormpostgres.Dialector).DSN), root)
			require.NoError(t, err)
			connection, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, connection.Close()) })
			grant := fmt.Sprintf("GRANT SELECT ON evidence_conflict_positions TO %s", ledgerTestRole)
			t.Cleanup(func() { require.NoError(t, adminDB.Exec(grant).Error) })
			require.NoError(t, adminDB.Exec(fmt.Sprintf("REVOKE SELECT ON evidence_conflict_positions FROM %s", ledgerTestRole)).Error)
			_, err = NewStore(db, rls, nil).GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: team, ConflictID: id})
			var cause *pgconn.PgError
			require.ErrorAs(t, err, &cause)
			require.Equal(t, "42501", cause.Code)
			found := false
			for _, record := range sink.records {
				if record.Message != "postgres query" || record.Severity != "ERROR" {
					continue
				}
				require.Equal(t, "database operation failed (SQLSTATE 42501)", record.Error)
				require.Equal(t, team, record.TeamID)
				require.Equal(t, owner, record.ProfileID)
				require.Equal(t, correlation.FromContext(ctx), record.CorrelationID)
				require.Contains(t, record.Attrs["sql"], "FROM evidence_conflict_positions")
				require.Contains(t, record.Attrs, "duration_ms")
				encoded, err := json.Marshal(record)
				require.NoError(t, err)
				require.NotContains(t, string(encoded), input.Evidence[0].Content)
				t.Log(string(encoded))
				found = true
			}
			require.True(t, found, "query cause must survive the root logger and sink filtering")
			require.NoError(t, adminDB.Exec(grant).Error)
			recovered, err := NewStore(db, rls, nil).GetEvidenceConflict(ctx, EvidenceConflictGetInput{TeamID: team, ConflictID: id})
			require.NoError(t, err)
			require.Len(t, recovered.Conflict.Positions, 2)
			t.Logf("correlation_id=%s cause=SQLSTATE-42501 action=restore-fixture-SELECT outcome=recovered", correlation.FromContext(ctx))
			if level == slog.LevelInfo {
				for _, record := range sink.records {
					require.False(t, strings.EqualFold(record.Severity, "DEBUG"))
				}
			}
		})
	}
}
