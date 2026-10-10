//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	sessionservice "github.com/markhuangai/dense-mem/internal/session/service"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func sessionIntakeFixture(t *testing.T, admin, app *gorm.DB, rls *storage.RLS, team, owner string, kind domain.MemorySpaceKind) (context.Context, session.Intake) {
	t.Helper()
	spaces := privacypostgres.NewMemorySpaceRepository(app, rls)
	var space *domain.MemorySpace
	var err error
	if kind == domain.MemorySpaceCredentialPrivate {
		space, err = spaces.EnsureCredentialPrivate(context.Background(), uuid.MustParse(team), uuid.New())
	} else {
		space, err = spaces.EnsureProfilePrivate(context.Background(), uuid.MustParse(team), uuid.MustParse(owner))
	}
	require.NoError(t, err)
	generation := privateSpaceGeneration(t, context.Background(), admin, rls, space.ID)
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: uuid.MustParse(team), OwnerID: uuid.MustParse(owner), Grants: []string{"read", "write"}, AllowedSpaces: []domain.MemorySpaceAccess{{ID: space.ID, Kind: kind, Generation: generation}}})
	req := session.Request{IdempotencyKey: "original-key", Framework: "generic", AppName: "notes", UserID: "external-user", SessionID: "external-session", Events: []session.Event{{EventID: "one", Text: "  Ari uses Go. café 🧭\n"}}}
	windows, err := sessionservice.BuildWindows(req, "o200k_base")
	require.NoError(t, err)
	hash, err := sessionservice.RequestHash(req)
	require.NoError(t, err)
	return ctx, session.Intake{Scope: session.Scope{TeamID: team, OwnerProfileID: owner, SpaceID: space.ID.String(), SpaceGeneration: generation}, Request: req, RequestHash: hash, Windows: windows, ExtractionVersion: session.ExtractionVersion}
}

func TestSessionIntakeExactReplayAndMixedConflictAreAtomic(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-intake")
	owner := createLedgerProfile(t, admin, rls, team, "session-owner")
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	first, err := store.StageSession(ctx, intake)
	require.NoError(t, err)
	require.Equal(t, 1, first.AcceptedEventCount)
	replayed, err := store.StageSession(ctx, intake)
	require.NoError(t, err)
	require.Equal(t, first.ID, replayed.ID)
	require.Equal(t, intake.Request, replayed.Intake.Request)
	var text string
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT body->>'text' FROM session_events WHERE team_id = ?::uuid AND space_id = ?::uuid`, team, intake.Scope.SpaceID).Row().Scan(&text)
	}))
	require.Equal(t, intake.Request.Events[0].Text, text)
	changed := intake
	changed.Request = intake.Request
	changed.Request.IdempotencyKey = "different-key"
	changed.Request.Events = []session.Event{{EventID: "new", Text: "Ari uses Rust."}, {EventID: "one", Text: "Changed immutable text."}}
	changed.RequestHash, err = sessionservice.RequestHash(changed.Request)
	require.NoError(t, err)
	_, err = store.StageSession(ctx, changed)
	require.ErrorIs(t, err, session.ErrEventConflict)
	var count int64
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM session_events WHERE team_id = ?::uuid`, team).Row().Scan(&count)
	}))
	require.EqualValues(t, 1, count)
	changed.Request.Events = []session.Event{{EventID: "new", Text: "Ari uses Rust."}, intake.Request.Events[0]}
	changed.RequestHash, err = sessionservice.RequestHash(changed.Request)
	require.NoError(t, err)
	_, err = store.StageSession(ctx, changed)
	require.ErrorIs(t, err, session.ErrOriginalRequestRequired)
	changed = intake
	changed.Request.AppName = "different-app"
	changed.RequestHash, err = sessionservice.RequestHash(changed.Request)
	require.NoError(t, err)
	_, err = store.StageSession(ctx, changed)
	require.ErrorIs(t, err, session.ErrRequestConflict)
}

func TestSessionPrivateRLSAndGenerationFence(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-isolation-a")
	ownerA := createLedgerProfile(t, admin, rls, team, "actor-a")
	ownerB := createLedgerProfile(t, admin, rls, team, "actor-b")
	teamC := createLedgerTeam(t, admin, rls, "session-isolation-c")
	ownerC := createLedgerProfile(t, admin, rls, teamC, "actor-c")
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	for _, kind := range []domain.MemorySpaceKind{domain.MemorySpaceProfilePrivate, domain.MemorySpaceCredentialPrivate} {
		t.Run(string(kind), func(t *testing.T) {
			ctxA, intakeA := sessionIntakeFixture(t, admin, app, rls, team, ownerA, kind)
			ctxB, intakeB := sessionIntakeFixture(t, admin, app, rls, team, ownerB, kind)
			ctxC, intakeC := sessionIntakeFixture(t, admin, app, rls, teamC, ownerC, kind)
			first, err := store.StageSession(ctxA, intakeA)
			require.NoError(t, err)
			for _, actor := range []struct {
				ctx    context.Context
				intake session.Intake
			}{{ctxB, intakeB}, {ctxC, intakeC}} {
				second, err := store.StageSession(actor.ctx, actor.intake)
				require.NoError(t, err)
				require.NotEqual(t, first.ID, second.ID)
				var count int64
				require.NoError(t, rls.WithTeamProfileTx(actor.ctx, app, actor.intake.Scope.TeamID, actor.intake.Scope.OwnerProfileID, func(tx *gorm.DB) error {
					return tx.Raw(`SELECT count(*) FROM session_events WHERE space_id = ?::uuid`, intakeA.Scope.SpaceID).Row().Scan(&count)
				}))
				require.Zero(t, count)
			}
			require.NoError(t, rls.WithSystemTx(ctxA, admin, func(tx *gorm.DB) error {
				return tx.Exec(`UPDATE memory_spaces SET lifecycle_state = 'sealed', generation = generation + 1, sealed_at = now() WHERE id = ?::uuid`, intakeA.Scope.SpaceID).Error
			}))
			require.ErrorIs(t, store.SaveSessionExtraction(ctxA, intakeA.Scope, first.ID, 0, json.RawMessage(`{}`)), session.ErrStale)
		})
	}
}

func TestSessionLockSerializesConcurrentSameKeyIntake(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-concurrent")
	owner := createLedgerProfile(t, admin, rls, team, "session-owner")
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	var group sync.WaitGroup
	ids := make(chan string, 4)
	errorsCh := make(chan error, 4)
	for range 4 {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsCh <- store.WithSessionLock(ctx, intake.Scope, intake.Request, func() error {
				staged, err := store.StageSession(ctx, intake)
				if err == nil {
					ids <- staged.ID
				}
				return err
			})
		}()
	}
	group.Wait()
	close(ids)
	var id string
	for staged := range ids {
		if id == "" {
			id = staged
		}
		require.Equal(t, id, staged)
	}
	for range 4 {
		require.NoError(t, <-errorsCh)
	}
	var count int64
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM session_events WHERE team_id = ?::uuid`, team).Row().Scan(&count)
	}))
	require.EqualValues(t, 1, count)
}

func TestSessionLockSerializesDifferentKeysAcrossStoreInstances(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-distinct-keys")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	first, second := NewStore(app, rls, ConflictRuntimeConfig{}), NewStore(app, rls, ConflictRuntimeConfig{})
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- first.WithSessionLock(ctx, intake.Scope, intake.Request, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	changed := intake.Request
	changed.IdempotencyKey = "second-key"
	waiting, secondDone := make(chan struct{}), make(chan error, 1)
	go func() {
		secondDone <- second.WithSessionLock(ctx, intake.Scope, changed, func() error { close(waiting); return nil })
	}()
	select {
	case <-waiting:
		t.Fatal("different operation key bypassed session lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-done)
	require.NoError(t, <-secondDone)
	<-waiting
}

func TestSessionConcurrentMixedConflictsStageOnlyTheWinningBatch(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	for _, kind := range []domain.MemorySpaceKind{domain.MemorySpaceProfilePrivate, domain.MemorySpaceCredentialPrivate} {
		t.Run(string(kind), func(t *testing.T) {
			team := createLedgerTeam(t, admin, rls, "session-concurrent-conflict-"+string(kind))
			owner := createLedgerProfile(t, admin, rls, team, "owner")
			ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, kind)
			start := make(chan struct{})
			outcomes := make(chan error, 2)
			for index := range 2 {
				req := intake.Request
				req.IdempotencyKey = fmt.Sprintf("conflict-%d", index)
				req.Events = []session.Event{{EventID: fmt.Sprintf("new-%d", index), Text: "Thanks."}, {EventID: "overlap", Text: fmt.Sprintf("Ari uses Language%d.", index)}}
				candidate := intake
				candidate.Request = req
				var err error
				candidate.RequestHash, err = sessionservice.RequestHash(req)
				require.NoError(t, err)
				candidate.Windows, err = sessionservice.BuildWindows(req, "o200k_base")
				require.NoError(t, err)
				store := NewStore(app, rls, ConflictRuntimeConfig{})
				go func() {
					<-start
					outcomes <- store.WithSessionLock(ctx, candidate.Scope, req, func() error { _, err := store.StageSession(ctx, candidate); return err })
				}()
			}
			close(start)
			success, conflicts := 0, 0
			for range 2 {
				err := <-outcomes
				if err == nil {
					success++
				} else {
					require.ErrorIs(t, err, session.ErrEventConflict)
					conflicts++
				}
			}
			require.Equal(t, 1, success)
			require.Equal(t, 1, conflicts)
			var events, submissions int64
			require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
				if err := tx.Raw(`SELECT count(*) FROM session_events WHERE team_id=?::uuid`, team).Row().Scan(&events); err != nil {
					return err
				}
				return tx.Raw(`SELECT count(*) FROM session_submissions WHERE team_id=?::uuid`, team).Row().Scan(&submissions)
			}))
			require.EqualValues(t, 2, events)
			require.EqualValues(t, 1, submissions)
		})
	}
}

func TestSessionDuplicatePreparationSupportsOneHundredPrivateExcerpts(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-many-excerpts")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	insertSearchTestContract(t, admin, rls, "session-many-excerpts", 2, "exact", "")
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	for _, size := range []int{21, 100} {
		input := RememberDuplicateCandidateInput{TeamID: team, OwnerProfileID: owner, SpaceID: intake.Scope.SpaceID, SpaceGeneration: intake.Scope.SpaceGeneration, MaxEvidenceItems: 100}
		for index := range size {
			text := fmt.Sprintf("Session excerpt %d.", index)
			input.Evidence = append(input.Evidence, EvidenceInput{FragmentID: uuid.NewString(), Content: text, ContentHash: sha256Hex(text)})
		}
		plan, err := store.PlanRememberDuplicateEmbeddings(ctx, input)
		require.NoError(t, err)
		require.Len(t, plan.Documents, size)
		resolution, err := store.ResolveRememberDuplicateCandidates(ctx, input, duplicatePlanEmbeddings(plan))
		require.NoError(t, err)
		require.Len(t, resolution.Candidates, size)
		input.MaxEvidenceItems = 0
		_, err = store.PlanRememberDuplicateEmbeddings(ctx, input)
		require.ErrorContains(t, err, "between 1 and 20")
	}
}
