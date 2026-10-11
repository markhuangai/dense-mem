//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	sessionservice "github.com/markhuangai/dense-mem/internal/session/service"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSessionReceiptFailureRollsBackKnowledgeAndRecoversOriginalIntake(t *testing.T) {
	admin, app, rls, cleanup := setupKnowledgeOwnerDB(t)
	defer cleanup()
	team := createOwnerTeam(t, admin, rls, "session-atomic")
	owner := createOwnerProfile(t, admin, rls, team, "session-owner")
	installOwnerSearchContract(t, admin, rls, "session-atomic", 3, 1)
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	input := ownerRememberInput(team, owner, "session-atomic")
	intake.Request.Events[0].Text = input.Evidence[0].Content
	var err error
	intake.Windows, err = sessionservice.BuildWindows(intake.Request, "o200k_base")
	require.NoError(t, err)
	intake.RequestHash, err = sessionservice.RequestHash(intake.Request)
	require.NoError(t, err)
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	submission, err := store.StageSession(ctx, intake)
	require.NoError(t, err)
	input.IngestID, input.Commit.IngestID = submission.ID, submission.ID
	input.IdempotencyKey, input.RequestHash = "session:"+submission.ID, intake.RequestHash
	input.SpaceID, input.SpaceGeneration = intake.Scope.SpaceID, intake.Scope.SpaceGeneration
	input.Evidence[0].Metadata = map[string]any{"session": map[string]any{"event_index": 0, "span_start": 0, "span_end": len([]rune(input.Evidence[0].Content)), "event_id": "one", "session_id": intake.Request.SessionID}}
	checkpoint := json.RawMessage(`{"request_id":"fixture","coverage":[],"entities":[],"relationships":[],"security_signals":[],"overflow":false}`)
	require.NoError(t, store.SaveSessionExtraction(ctx, intake.Scope, submission.ID, 0, checkpoint))
	require.NoError(t, store.SaveSessionLinking(ctx, intake.Scope, submission.ID, json.RawMessage(`{"request_id":"fixture","groups":[]}`)))
	plan, err := store.PlanRememberEmbeddings(ctx, input)
	require.NoError(t, err)
	prepared := &session.Prepared{Commit: input, Embeddings: ownerEmbeddings(plan, false)}
	result := session.Result{ContractVersion: domain.ContractVersion, SubmissionID: submission.ID, SubmissionKind: "session_ingest", CorrelationID: "fixture", AcceptedEventCount: 1, ProcessingState: "completed", SearchState: "not_required", Events: []session.EventResult{{EventID: "one", Disposition: "accepted", ProcessingState: "completed", EvidenceIDs: []string{}}}, RelationshipResults: []SubmissionRelationshipResult{}, Errors: []session.Error{}}
	originalResult, err := json.Marshal(result)
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Exec(`
		CREATE FUNCTION session_test_receipt_failure() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.body->>'processing_state' = 'completed' THEN RAISE EXCEPTION 'forced receipt failure'; END IF; RETURN NEW; END $$;
		CREATE TRIGGER session_test_receipt_failure BEFORE INSERT ON session_submission_receipts FOR EACH ROW EXECUTE FUNCTION session_test_receipt_failure();
	`).Error
	}))
	_, err = store.CommitSession(ctx, intake.Scope, submission.ID, prepared, result)
	require.ErrorContains(t, err, "forced receipt failure")
	afterFailure, err := json.Marshal(result)
	require.NoError(t, err)
	require.JSONEq(t, string(originalResult), string(afterFailure))
	for _, table := range []string{"knowledge_ingests", "evidence_fragments", "search_documents", "remember_attempts"} {
		require.Zero(t, ownerCount(t, admin, rls, table, team, submission.ID), table)
	}
	failure := result
	failure.ProcessingState = "failed"
	failure.Events = append([]session.EventResult(nil), result.Events...)
	failure.Events[0].ProcessingState = "failed"
	failure.Errors = []session.Error{{Code: "database_failure", Retryable: true, NextAction: "retry_same_request"}}
	require.NoError(t, store.RecordSessionFailure(ctx, intake.Scope, submission.ID, failure))
	replayed, err := store.StageSession(ctx, intake)
	require.NoError(t, err)
	require.Equal(t, submission.ID, replayed.ID)
	require.Len(t, replayed.Extractions, 1)
	require.Equal(t, "failed", replayed.Result.ProcessingState)
	require.Empty(t, replayed.Result.Events[0].EvidenceIDs)
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Exec(`DROP TRIGGER session_test_receipt_failure ON session_submission_receipts; DROP FUNCTION session_test_receipt_failure();`).Error
	}))
	completed, err := store.CommitSession(ctx, intake.Scope, submission.ID, prepared, result)
	require.NoError(t, err)
	require.Equal(t, "completed", completed.ProcessingState)
	require.Equal(t, "current", completed.SearchState)
	require.Len(t, completed.Events[0].EvidenceIDs, 1)
	require.Empty(t, result.Events[0].EvidenceIDs)
	for _, table := range []string{"knowledge_ingests", "evidence_fragments", "search_documents", "remember_attempts"} {
		require.EqualValues(t, 1, ownerCount(t, admin, rls, table, team, submission.ID), table)
	}
	replayed, err = store.StageSession(ctx, intake)
	require.NoError(t, err)
	require.Equal(t, *completed, *replayed.Result)
}

func TestSessionCheckpointAndSuccessfulReceiptAreImmutable(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	team := createLedgerTeam(t, admin, rls, "session-checkpoint")
	owner := createLedgerProfile(t, admin, rls, team, "session-owner")
	ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, domain.MemorySpaceProfilePrivate)
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	submission, err := store.StageSession(ctx, intake)
	require.NoError(t, err)
	body := json.RawMessage(`{"request_id":"one"}`)
	require.NoError(t, store.SaveSessionExtraction(ctx, intake.Scope, submission.ID, 0, body))
	require.NoError(t, store.SaveSessionExtraction(ctx, intake.Scope, submission.ID, 0, body))
	require.ErrorIs(t, store.SaveSessionExtraction(ctx, intake.Scope, submission.ID, 0, json.RawMessage(`{"request_id":"changed"}`)), session.ErrRequestConflict)
	result := session.Result{ContractVersion: domain.ContractVersion, SubmissionID: submission.ID, SubmissionKind: "session_ingest", CorrelationID: "fixture", AcceptedEventCount: 1, ProcessingState: "completed", SearchState: "not_required", Events: []session.EventResult{{EventID: "one", Disposition: "accepted", ProcessingState: "completed", EvidenceIDs: []string{}}}, RelationshipResults: []SubmissionRelationshipResult{}, Errors: []session.Error{}}
	completed, err := store.CommitSession(ctx, intake.Scope, submission.ID, nil, result)
	require.NoError(t, err)
	require.Equal(t, "not_required", completed.SearchState)
	result.ProcessingState = "failed"
	require.ErrorIs(t, store.RecordSessionFailure(ctx, intake.Scope, submission.ID, result), session.ErrStale)
	replayed, err := store.StageSession(ctx, intake)
	require.NoError(t, err)
	require.Equal(t, "completed", replayed.Result.ProcessingState)
	var count int64
	require.NoError(t, rls.WithSystemTx(context.Background(), admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid`, team).Row().Scan(&count)
	}))
	require.Zero(t, count)
}

func TestSessionPreparedCommitCannotReviveChangedOrErasedPrivateGeneration(t *testing.T) {
	admin, app, rls, cleanup := setupKnowledgeOwnerDB(t)
	defer cleanup()
	installOwnerSearchContract(t, admin, rls, "session-generation", 3, 1)
	for _, kind := range []domain.MemorySpaceKind{domain.MemorySpaceProfilePrivate, domain.MemorySpaceCredentialPrivate} {
		for _, erase := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/erase=%t", kind, erase), func(t *testing.T) {
				team := createOwnerTeam(t, admin, rls, fmt.Sprintf("session-generation-%s-%t", kind, erase))
				owner := createOwnerProfile(t, admin, rls, team, "owner")
				ctx, intake := sessionIntakeFixture(t, admin, app, rls, team, owner, kind)
				input := ownerRememberInput(team, owner, "session-generation")
				intake.Request.Events[0].Text = input.Evidence[0].Content
				var err error
				intake.Windows, err = sessionservice.BuildWindows(intake.Request, "o200k_base")
				require.NoError(t, err)
				intake.RequestHash, err = sessionservice.RequestHash(intake.Request)
				require.NoError(t, err)
				store := NewStore(app, rls, ConflictRuntimeConfig{})
				staged, err := store.StageSession(ctx, intake)
				require.NoError(t, err)
				input.IngestID, input.Commit.IngestID = staged.ID, staged.ID
				input.SpaceID, input.SpaceGeneration = intake.Scope.SpaceID, intake.Scope.SpaceGeneration
				input.IdempotencyKey, input.RequestHash = "session:"+staged.ID, intake.RequestHash
				input.Evidence[0].Metadata = map[string]any{"session": map[string]any{"event_index": 0, "span_start": 0, "span_end": len([]rune(input.Evidence[0].Content)), "event_id": "one", "session_id": intake.Request.SessionID}}
				require.NoError(t, store.SaveSessionExtraction(ctx, intake.Scope, staged.ID, 0, json.RawMessage(`{}`)))
				require.NoError(t, store.SaveSessionLinking(ctx, intake.Scope, staged.ID, json.RawMessage(`{}`)))
				plan, err := store.PlanRememberEmbeddings(ctx, input)
				require.NoError(t, err)
				prepared := &session.Prepared{Commit: input, Embeddings: ownerEmbeddings(plan, false)}
				if erase {
					eraser := privacy.NewPrivateMemoryRepository(app, rls)
					require.NoError(t, eraser.Prepare(ctx))
					operation, _, err := eraser.RequestControlErasure(ctx, uuid.MustParse(intake.Scope.SpaceID), privacy.Hash("session", staged.ID), privacy.Hash("session-erasure", staged.ID), "operator_request")
					require.NoError(t, err)
					claim, err := eraser.ClaimNext(ctx, "session-test", time.Minute)
					require.NoError(t, err)
					require.NotNil(t, claim)
					require.Equal(t, operation.ID, claim.ID)
					completed, err := eraser.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
					require.NoError(t, err)
					require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)
				} else {
					require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
						return tx.Exec(`UPDATE memory_spaces SET lifecycle_state='sealed',generation=generation+1,sealed_at=now() WHERE id=?::uuid`, intake.Scope.SpaceID).Error
					}))
				}
				result := session.Result{SubmissionID: staged.ID, ProcessingState: "completed", AcceptedEventCount: 1, Events: []session.EventResult{{EventID: "one", ProcessingState: "completed", Disposition: "accepted", EvidenceIDs: []string{}}}}
				_, err = store.CommitSession(ctx, intake.Scope, staged.ID, prepared, result)
				require.ErrorIs(t, err, session.ErrStale)
				for _, table := range []string{"knowledge_ingests", "evidence_fragments", "search_documents", "remember_attempts"} {
					require.Zero(t, ownerCount(t, admin, rls, table, team, staged.ID), table)
				}
			})
		}
	}
}
