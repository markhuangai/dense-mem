//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	processor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/markhuangai/dense-mem/internal/session/extraction"
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

	ordered := intake
	ordered.Request.IdempotencyKey = "ordered-batch"
	ordered.Request.SessionID = "ordered-history"
	ordered.Request.Events = []session.Event{}
	for index := 0; index < 12; index++ {
		ordered.Request.Events = append(ordered.Request.Events, session.Event{EventID: fmt.Sprintf("ordered-%d", index), Text: fmt.Sprintf("User turn %d.", index)})
	}
	sort.Slice(ordered.Request.Events, func(i, j int) bool {
		return session.IdentityHash(ordered.Request, ordered.Request.Events[i].EventID) > session.IdentityHash(ordered.Request, ordered.Request.Events[j].EventID)
	})
	ordered.RequestHash, err = sessionservice.RequestHash(ordered.Request)
	require.NoError(t, err)
	ordered.Windows, err = sessionservice.BuildWindows(ordered.Request, "o200k_base")
	require.NoError(t, err)
	batch, err := store.StageSession(ctx, ordered)
	require.NoError(t, err)
	for _, window := range batch.Intake.Windows {
		require.NoError(t, store.SaveSessionExtraction(ctx, ordered.Scope, batch.ID, window.Index, json.RawMessage(`{}`)))
	}
	terminal := session.Result{SubmissionID: batch.ID, AcceptedEventCount: 12, ProcessingState: "completed", SearchState: "not_required", Events: []session.EventResult{}}
	for _, event := range ordered.Request.Events {
		terminal.Events = append(terminal.Events, session.EventResult{EventID: event.EventID, Disposition: "accepted", ProcessingState: "completed", EvidenceIDs: []string{}})
	}
	_, err = store.CommitSession(ctx, ordered.Scope, batch.ID, nil, terminal)
	require.NoError(t, err)
	next := ordered
	next.Request.IdempotencyKey = "after-ordered-batch"
	next.Request.Events = []session.Event{{EventID: "after-batch", Text: "This remains my preference."}}
	next.RequestHash, err = sessionservice.RequestHash(next.Request)
	require.NoError(t, err)
	next.Windows, err = sessionservice.BuildWindows(next.Request, "o200k_base")
	require.NoError(t, err)
	following, err := store.StageSession(ctx, next)
	require.NoError(t, err)
	require.Len(t, following.Prior, session.ContextEvents)
	for index, event := range following.Prior {
		require.Equal(t, ordered.Request.Events[len(ordered.Request.Events)-1-index], event.Event)
	}
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

func TestSessionProfilePrivateOwnerAliasesAndIsolation(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-profile-aliases")
	foreignTeam := createLedgerTeam(t, admin, rls, "session-profile-aliases-foreign")
	spaces := privacypostgres.NewMemorySpaceRepository(app, rls)
	type actorScope struct {
		ctx      context.Context
		scope    session.Scope
		identity uuid.UUID
	}
	newOAuth := func(team string, preserved bool) actorScope {
		identity := createLedgerSSOIdentity(t, admin, rls, uuid.MustParse(team))
		var membership uuid.UUID
		require.NoError(t, rls.WithSystemTx(context.Background(), admin, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT id FROM team_memberships WHERE team_id = ?::uuid AND actor_identity_id = ?`, team, identity).Row().Scan(&membership)
		}))
		owner := membership
		if preserved {
			owner = uuid.New()
			require.NotEqual(t, membership, owner)
		}
		require.NotEqual(t, identity, owner)
		require.NoError(t, rls.WithSystemTx(context.Background(), admin, func(tx *gorm.DB) error {
			return tx.Exec(`INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id, reason)
				VALUES (?::uuid, ?, ?, 'session-alias-regression')`, team, owner, identity).Error
		}))
		space, err := spaces.EnsureProfilePrivate(context.Background(), uuid.MustParse(team), identity)
		require.NoError(t, err)
		generation := privateSpaceGeneration(t, context.Background(), admin, rls, space.ID)
		ctx := requestctx.WithActor(context.Background(), requestctx.Actor{
			TeamID: uuid.MustParse(team), IdentityID: identity, MembershipID: membership, OwnerID: owner,
			AuthMethod: "oauth", Grants: []string{"read", "write"},
			AllowedSpaces: []domain.MemorySpaceAccess{{ID: space.ID, Kind: space.Kind, Generation: generation}},
		})
		scope, err := sessionservice.PrivateScope(ctx)
		require.NoError(t, err)
		require.Equal(t, owner.String(), scope.OwnerProfileID)
		return actorScope{ctx: ctx, scope: scope, identity: identity}
	}
	a, b, c := newOAuth(team, false), newOAuth(team, true), newOAuth(foreignTeam, true)
	key := createOwnedCredential(t, accesspostgres.NewCredentialRepository(app, rls, nil), uuid.MustParse(team), a.identity, "profile-session-key", domain.CredentialBindingProfilePrivate)
	require.NotEqual(t, a.identity, key.OwnerID)
	keyCtx := requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: key.TeamID, IdentityID: key.ActorIdentityID, MembershipID: key.MembershipID, OwnerID: key.OwnerID,
		CredentialID: &key.ID, AuthMethod: "api_key", Grants: key.Scopes,
		AllowedSpaces: []domain.MemorySpaceAccess{{ID: key.MemorySpaceID, Kind: domain.MemorySpaceProfilePrivate, Generation: key.MemorySpaceGeneration}},
	})
	keyScope, err := sessionservice.PrivateScope(keyCtx)
	require.NoError(t, err)
	require.Equal(t, a.scope.SpaceID, keyScope.SpaceID)
	require.NotEqual(t, a.scope.OwnerProfileID, keyScope.OwnerProfileID)
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	api := sessionservice.NewService(sessionservice.Dependencies{
		Enabled: true, Repository: store, Tokenizer: "o200k_base",
		Preparer:  processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store}),
		Extractor: extraction.NewProvider(&sessionOutbound{}, "fixture", assessor.DefaultSemanticAssessmentLimits()),
	})
	req := session.Request{IdempotencyKey: "alias-replay", Framework: "generic", AppName: "notes", UserID: "same-external-user", SessionID: "same-session", Events: []session.Event{{EventID: "one", Text: "Thanks. Let's continue."}}}
	ids := map[string]bool{}
	keys := map[string]string{}
	for _, actor := range []actorScope{a, b, c, {ctx: keyCtx, scope: keyScope}} {
		actorRequest := req
		actorRequest.IdempotencyKey = uuid.NewString()
		actorRequest.SessionID = uuid.NewString()
		keys[actor.scope.OwnerProfileID] = actorRequest.IdempotencyKey
		result, err := api.Ingest(actor.ctx, actorRequest)
		require.NoError(t, err)
		require.Equal(t, "completed", result.ProcessingState)
		require.False(t, ids[result.SubmissionID])
		ids[result.SubmissionID] = true
		replay, err := api.Ingest(actor.ctx, actorRequest)
		require.NoError(t, err)
		expectedReceipt, err := json.Marshal(result)
		require.NoError(t, err)
		replayedReceipt, err := json.Marshal(replay)
		require.NoError(t, err)
		require.Equal(t, string(expectedReceipt), string(replayedReceipt))
		var owner string
		require.NoError(t, rls.WithSystemTx(context.Background(), admin, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT owner_profile_id::text FROM session_submissions WHERE submission_id = ?::uuid`, result.SubmissionID).Row().Scan(&owner)
		}))
		require.Equal(t, actor.scope.OwnerProfileID, owner)
	}
	_, err = store.LookupSession(keyCtx, a.scope, keys[a.scope.OwnerProfileID])
	require.ErrorIs(t, err, session.ErrUnauthorized)
	_, err = store.LookupSession(a.ctx, keyScope, keys[keyScope.OwnerProfileID])
	require.ErrorIs(t, err, session.ErrUnauthorized)
	for _, foreign := range []actorScope{b, c} {
		forged := foreign.scope
		forged.SpaceID, forged.SpaceGeneration = a.scope.SpaceID, a.scope.SpaceGeneration
		_, err = store.LookupSession(foreign.ctx, forged, keys[a.scope.OwnerProfileID])
		require.ErrorIs(t, err, session.ErrStale)
	}
	actor, ok := requestctx.ActorFromContext(a.ctx)
	require.True(t, ok)
	actor.Grants = []string{"read"}
	_, err = api.Ingest(requestctx.WithActor(context.Background(), actor), req)
	require.ErrorIs(t, err, session.ErrUnauthorized)
	actor.Grants = []string{"read", "write"}
	actor.AllowedSpaces[0].Kind = domain.MemorySpaceTeamShared
	_, err = api.Ingest(requestctx.WithActor(context.Background(), actor), req)
	require.ErrorIs(t, err, session.ErrUnauthorized)
	require.NoError(t, rls.WithSystemTx(context.Background(), admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation = generation + 1 WHERE id = ?::uuid`, a.scope.SpaceID).Error
	}))
	for _, actor := range []actorScope{a, {ctx: keyCtx, scope: keyScope}} {
		_, err = store.LookupSession(actor.ctx, actor.scope, keys[actor.scope.OwnerProfileID])
		require.ErrorIs(t, err, session.ErrStale)
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
