//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	processor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/markhuangai/dense-mem/internal/session/extraction"
	service "github.com/markhuangai/dense-mem/internal/session/service"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSessionContextIndexBoundsPrivateLookupAcrossUnrelatedHistory(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	team := createLedgerTeam(t, admin, rls, "session-context-index")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpacePrivate)
	intake.Request.Framework = strings.Repeat("🧭", 256)
	intake.Request.AppName = strings.Repeat("🎼", 256)
	intake.Request.UserID = strings.Repeat("🦉", 256)
	intake.Request.SessionID = strings.Repeat("🌍", 256)
	intake.Request.Events = nil
	for i := range session.MaxEvents {
		intake.Request.Events = append(intake.Request.Events, session.Event{EventID: fmt.Sprintf("target-%d", i), Text: "Thanks. Let's continue."})
	}
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	completeSessionContextFixture(t, ctx, store, intake.Request)
	plans := map[string]json.RawMessage{}
	read := func(actor context.Context, scope session.Scope, req session.Request) *session.Submission {
		result := &session.Submission{}
		require.NoError(t, store.withTeamProfileTx(actor, scope.TeamID, scope.OwnerProfileID, func(tx *gorm.DB) error {
			return loadPriorSessionEvents(actor, tx, scope, req, result)
		}))
		return result
	}
	before := read(ctx, intake.Scope, intake.Request)
	require.Len(t, before.Prior, session.ContextEvents)
	for index, item := range before.Prior {
		require.Equal(t, fmt.Sprintf("target-%d", 19-index), item.Event.EventID)
	}
	seedSessionContextHistory(t, admin, rls, intake, 200000)
	require.NoError(t, admin.Exec("ANALYZE session_events").Error)
	var superuser, bypass, forceRLS bool
	require.NoError(t, app.Raw(`SELECT relforcerowsecurity FROM pg_class WHERE oid='session_events'::regclass`).Row().Scan(&forceRLS))
	require.True(t, forceRLS)
	require.NoError(t, app.Raw(`SELECT rolsuper,rolbypassrls FROM pg_roles WHERE rolname=current_user`).Row().Scan(&superuser, &bypass))
	require.False(t, superuser)
	require.False(t, bypass)
	for _, name := range []string{"dormant", "new", "other_owner", "other_generation"} {
		req, scope := intake.Request, intake.Scope
		actor := ctx
		switch name {
		case "new":
			req.SessionID = "new-session"
		case "other_owner":
			other := createLedgerProfile(t, admin, rls, team, "different-owner")
			otherActor, otherIntake := sessionIntakeFixture(t, admin, app, rls, team, other, domain.MemorySpacePrivate)
			actor, scope = otherActor, otherIntake.Scope
		case "other_generation":
			scope.SpaceGeneration++
		}
		result := read(actor, scope, req)
		if name == "dormant" {
			require.Equal(t, before.Prior, result.Prior)
		} else {
			require.Empty(t, result.Prior)
		}
		var encoded []byte
		require.NoError(t, store.withTeamProfileTx(actor, scope.TeamID, scope.OwnerProfileID, func(tx *gorm.DB) error {
			return tx.Raw("EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+priorSessionEventsSQL,
				scope.TeamID, scope.SpaceID, scope.OwnerProfileID, scope.SpaceGeneration,
				req.Framework, req.AppName, req.UserID, req.SessionID,
				req.Framework, req.AppName, req.UserID, req.SessionID, session.ContextEvents).Row().Scan(&encoded)
		}))
		var plan []map[string]any
		require.NoError(t, json.Unmarshal(encoded, &plan))
		assertSessionContextPlan(t, plan[0]["Plan"].(map[string]any))
		if name == "dormant" || name == "new" {
			require.Contains(t, string(encoded), "session_events_session_context_idx")
		}
		plans[name] = append(json.RawMessage(nil), encoded...)
	}
	next := intake
	next.Request.IdempotencyKey = "next-admitted-request"
	next.Request.Events = []session.Event{{EventID: "next", Text: "Continue the conversation."}}
	hash, err := service.RequestHash(next.Request)
	next.RequestHash = hash
	require.NoError(t, err)
	admitted, err := store.StageSession(ctx, next)
	require.NoError(t, err)
	require.Equal(t, before.Prior, admitted.Prior)
	if directory := os.Getenv("DENSE_MEM_ORGANIZATION_REPORT_DIR"); directory != "" {
		require.NoError(t, os.MkdirAll(directory, 0700))
		data, err := json.MarshalIndent(map[string]any{"unrelated_events": 200000, "force_rls": forceRLS, "superuser": superuser, "bypass_rls": bypass, "plans": plans}, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "session-context-plans.json"), append(data, '\n'), 0600))
	}
}

func completeSessionContextFixture(t *testing.T, ctx context.Context, store *Store, request session.Request) {
	t.Helper()
	api := service.NewService(service.Dependencies{Enabled: true, Repository: store, Preparer: processor.NewSynchronousProcessor(processor.ProcessorDependencies{Ledger: store}), Tokenizer: "o200k_base", Extractor: extraction.NewProvider(&sessionOutbound{}, "fixture", assessor.DefaultSemanticAssessmentLimits())})
	result, err := api.Ingest(ctx, request)
	require.NoError(t, err)
	require.Equal(t, "completed", result.ProcessingState)
}

func seedSessionContextHistory(t *testing.T, db *gorm.DB, rls *storage.RLS, base session.Intake, count int) {
	t.Helper()
	require.Zero(t, count%session.MaxEvents)
	type seededEvent struct {
		Identity string
		Body     session.Event
		Result   session.EventResult
	}
	type seededSubmission struct {
		ID     string
		Intake session.Intake
		Result session.Result
		Events []seededEvent
	}
	// Keep private-space activity-trigger update chains short while retaining production triggers.
	const batchSubmissions = 10
	for first := 0; first < count/session.MaxEvents; first += batchSubmissions {
		rows := []seededSubmission{}
		for i := first; i < min(first+batchSubmissions, count/session.MaxEvents); i++ {
			intake := base
			intake.Request = session.Request{IdempotencyKey: fmt.Sprintf("history-%d", i), Framework: "generic", AppName: "other-notes", UserID: "history-user", SessionID: fmt.Sprintf("history-%d", i)}
			intake.Windows = nil
			row := seededSubmission{ID: uuid.NewString()}
			row.Result = session.Result{SubmissionID: row.ID, AcceptedEventCount: session.MaxEvents, ProcessingState: "completed", SearchState: "not_required"}
			for j := range session.MaxEvents {
				event := session.Event{EventID: fmt.Sprintf("history-%d-%d", i, j), Text: "Thanks. Let's continue."}
				result := session.EventResult{EventID: event.EventID, Disposition: "accepted", ProcessingState: "completed", EvidenceIDs: []string{}}
				intake.Request.Events = append(intake.Request.Events, event)
				row.Events = append(row.Events, seededEvent{Identity: session.IdentityHash(intake.Request, event.EventID), Body: event, Result: result})
				row.Result.Events = append(row.Result.Events, result)
			}
			var err error
			intake.RequestHash, err = service.RequestHash(intake.Request)
			require.NoError(t, err)
			intake.Windows, err = service.BuildWindows(intake.Request, "o200k_base")
			require.NoError(t, err)
			row.Intake = intake
			rows = append(rows, row)
		}
		encoded, err := json.Marshal(rows)
		require.NoError(t, err)
		require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
			return tx.Exec(`WITH fixtures AS MATERIALIZED (SELECT value AS row FROM jsonb_array_elements(?::jsonb)),
    submissions AS (
     INSERT INTO session_submissions(team_id,space_id,owner_profile_id,space_generation,submission_id,idempotency_key,request_hash,intake,prior_context,new_event_indices,accepted_event_count,duplicate_event_count,result)
     SELECT ?::uuid,?::uuid,?::uuid,?,(row->>'ID')::uuid,row->'Intake'->'Request'->>'idempotency_key',row->'Intake'->>'RequestHash',row->'Intake','[]'::jsonb,'[0,1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19]'::jsonb,20,0,row->'Result' FROM fixtures
     RETURNING submission_id
    )
    INSERT INTO session_events(team_id,space_id,owner_profile_id,space_generation,identity_hash,submission_id,event_index,framework,app_name,user_id,session_id,event_id,body,result)
    SELECT ?::uuid,?::uuid,?::uuid,?,event->>'Identity',submissions.submission_id,ordinality-1,
     row->'Intake'->'Request'->>'framework',row->'Intake'->'Request'->>'app_name',row->'Intake'->'Request'->>'user_id',row->'Intake'->'Request'->>'session_id',event->'Body'->>'event_id',event->'Body',event->'Result'
    FROM fixtures JOIN submissions ON submissions.submission_id=(row->>'ID')::uuid CROSS JOIN LATERAL jsonb_array_elements(row->'Events') WITH ORDINALITY AS events(event,ordinality)`, string(encoded),
				base.Scope.TeamID, base.Scope.SpaceID, base.Scope.OwnerProfileID, base.Scope.SpaceGeneration,
				base.Scope.TeamID, base.Scope.SpaceID, base.Scope.OwnerProfileID, base.Scope.SpaceGeneration).Error
		}))
	}
}

func assertSessionContextPlan(t *testing.T, node map[string]any) {
	t.Helper()
	if node["Relation Name"] == "session_events" {
		require.NotEqual(t, "Seq Scan", node["Node Type"])
		require.LessOrEqual(t, node["Actual Rows"].(float64), float64(session.ContextEvents))
		if removed, ok := node["Rows Removed by Filter"].(float64); ok {
			require.LessOrEqual(t, removed, float64(session.ContextEvents))
		}
	}
	if children, ok := node["Plans"].([]any); ok {
		for _, child := range children {
			assertSessionContextPlan(t, child.(map[string]any))
		}
	}
}

func TestSessionContextIndexMigrationRetainsIntakeAndRepairsInterruptedBuild(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	team := createLedgerTeam(t, admin, rls, "session-context-migration")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpacePrivate)
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	completeSessionContextFixture(t, ctx, store, intake.Request)
	snapshot := func() string {
		var rows string
		require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT jsonb_agg(to_jsonb(event) ORDER BY identity_hash)::text FROM session_events AS event`).Row().Scan(&rows)
		}))
		return rows
	}
	before := snapshot()
	require.NoError(t, admin.Exec(fmt.Sprintf(`GRANT CREATE ON SCHEMA public TO %s`, ledgerTestRole)).Error)
	require.NoError(t, admin.Exec(fmt.Sprintf(`ALTER TABLE session_events OWNER TO %s`, ledgerTestRole)).Error)
	const migration = "20261016020000_session_context_index.sql"
	contents, err := os.ReadFile(filepath.Join("../../../migrations/postgres/v2_6", migration))
	require.NoError(t, err)
	sqlDB, err := app.DB()
	require.NoError(t, err)
	migrator, err := goose.NewProvider(goose.DialectPostgres, sqlDB, fstest.MapFS{migration: &fstest.MapFile{Data: contents}},
		goose.WithDisableGlobalRegistry(true), goose.WithAllowOutofOrder(true))
	require.NoError(t, err)
	_, err = migrator.ApplyVersion(context.Background(), 20261016020000, false)
	require.NoError(t, err)
	require.Equal(t, before, snapshot())
	require.Error(t, app.Exec(`CREATE INDEX CONCURRENTLY session_events_session_context_idx ON session_events ((1/event_index))`).Error)
	var valid bool
	require.NoError(t, app.Raw(`SELECT indisvalid FROM pg_index WHERE indexrelid='session_events_session_context_idx'::regclass`).Row().Scan(&valid))
	require.False(t, valid)
	_, err = migrator.Up(context.Background())
	require.NoError(t, err)
	require.Equal(t, before, snapshot())
	require.NoError(t, app.Raw(`SELECT indisvalid FROM pg_index WHERE indexrelid='session_events_session_context_idx'::regclass`).Row().Scan(&valid))
	require.True(t, valid)
	var definition string
	require.NoError(t, app.Raw(`SELECT pg_get_indexdef('session_events_session_context_idx'::regclass)`).Row().Scan(&definition))
	for _, field := range []string{"framework", "app_name", "user_id", "session_id"} {
		require.Contains(t, definition, "md5("+field+")")
	}
	_, err = migrator.Up(context.Background())
	require.NoError(t, err)
	require.Equal(t, before, snapshot())
}
