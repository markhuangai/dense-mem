//go:build integration

package serverapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepg "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	processor "github.com/markhuangai/dense-mem/internal/remember/service/processor"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/semanticwrite"
	trace "github.com/markhuangai/dense-mem/internal/trace/contract"
	tracepg "github.com/markhuangai/dense-mem/internal/trace/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type partitionPostgresAssessor struct {
	rememberProcessorIntegrationAssessor
	calls            atomic.Int32
	beforeAssessment func(context.Context)
}

func (p *partitionPostgresAssessor) Complete(_ context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	p.calls.Add(1)
	var input assessor.PartitionRequest
	if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
		return modelprovider.StructuredResult{}, err
	}
	response := assessor.PartitionResponse{RequestID: input.RequestID, Items: make([]assessor.PartitionResult, len(input.Items))}
	for i, item := range input.Items {
		refs := regexp.MustCompile(`⟦([^⟧]+)⟧`).FindAllStringSubmatch(item.BoundaryText, -1)
		ends := []string{refs[len(refs)-1][1]}
		if newline := strings.Index(item.BoundaryText, "\n"); newline >= 0 {
			next := regexp.MustCompile(`⟦([^⟧]+)⟧`).FindStringSubmatch(item.BoundaryText[newline:])
			if len(next) > 1 && next[1] != ends[0] {
				ends = append([]string{next[1]}, ends...)
			}
		}
		response.Items[i] = assessor.PartitionResult{ItemID: item.ItemID, EndRefs: ends}
	}
	body, err := json.Marshal(response)
	return modelprovider.StructuredResult{Content: string(body)}, err
}

func (p *partitionPostgresAssessor) Assess(ctx context.Context, r assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentSession, assessor.SemanticAssessmentTurn, error) {
	if p.beforeAssessment != nil {
		p.beforeAssessment(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, assessor.SemanticAssessmentTurn{}, err
	}
	return p.rememberProcessorIntegrationAssessor.Assess(ctx, r)
}

func TestRememberPartitionPostgres(t *testing.T) {
	admin, app, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	t.Cleanup(cleanup)
	model, dimensions := ensureRememberProcessorIntegrationSearchContract(t, admin, rls)
	provider := &partitionPostgresAssessor{}
	ledger := knowledgepg.NewStore(app, rls)
	service := remember.NewService(remember.Dependencies{Synchronous: processor.NewSynchronousProcessor(processor.ProcessorDependencies{
		Ledger: ledger, Assessor: provider, Limits: assessor.DefaultSemanticAssessmentLimits(), CommitFailureStage: knowledgepg.RememberCommitFailureStage,
		EmbeddingExecutor: semanticwrite.NewProviderExecutor(rememberProcessorIntegrationSearchEmbedder{model: model, dimensions: dimensions}),
	})})
	newActor := func(team uuid.UUID) context.Context {
		if team == uuid.Nil {
			team = uuid.New()
			require.NoError(t, access.NewTeamRepository(admin, rls).Create(t.Context(), &domain.Team{ID: team, Name: "partition-" + team.String()}))
		}
		id := uuid.New()
		require.NoError(t, access.NewCredentialRepository(admin, rls, nil).CreateCredential(t.Context(), &domain.Credential{ID: id, TeamID: team, Name: "partition actor", Role: "member", KeyHash: id.String(), KeyPrefix: strings.ReplaceAll(id.String(), "-", "")[:24], KeySuffix: "test", Scopes: []string{"read", "write"}}))
		return rememberProcessorActorContext(t, t.Context(), admin, rls, team, id)
	}
	a := newActor(uuid.Nil)
	actorA, _ := requestctx.ActorFromContext(a)
	b, c := newActor(actorA.TeamID), newActor(uuid.Nil)
	count := func(table string, ingest string) int64 {
		var n int64
		require.NoError(t, rls.WithSystemTx(t.Context(), admin, func(tx *gorm.DB) error {
			query := tx.Table(table).Where("team_id = ?", actorA.TeamID)
			if ingest != "" {
				query = query.Where("ingest_id = ?", ingest)
			}
			return query.Count(&n).Error
		}))
		return n
	}
	failedResult := func(err error) *remember.SubmissionStatusResult {
		var failure *remember.RememberProcessError
		require.True(t, errors.As(err, &failure))
		require.NotNil(t, failure.Status)
		return failure.Status
	}
	request := func(content string, kind domain.MemorySpaceKind) remember.RememberRequest {
		return remember.RememberRequest{TargetSpace: kind, IdempotencyKey: uuid.NewString(), Evidence: []remember.RememberEvidenceInput{{Content: content, SourceType: "manual", ForceInsert: true}}}
	}
	traceStore := tracepg.New(app, rls)
	content := "  Atlas uses PostgreSQL.\r\n\nBoreal uses Redis. 界😀  "
	req := request(content, domain.MemorySpaceTeamShared)
	result, err := service.Remember(a, req)
	require.NoError(t, err)
	require.Len(t, result.Evidence, 2)
	require.EqualValues(t, 1, count("remember_submitted_items", result.SubmissionID))
	require.NoError(t, remember.ValidateTerminalRememberResult(result.Terminal, len(req.Evidence)))
	reconstructed := ""
	for i, item := range result.Evidence {
		require.Equal(t, 0, item.EvidenceIndex)
		require.Equal(t, i, *item.PartitionIndex)
		traced, err := traceStore.TraceEvidence(a, trace.NormalizeInput(trace.Input{TeamID: actorA.TeamID.String(), EvidenceID: item.EvidenceID, MaxFragmentContentRunes: 16384}))
		require.NoError(t, err)
		require.NotNil(t, traced.Evidence.Partition)
		require.Equal(t, knowledge.EvidenceContentHash(content), traced.Evidence.Partition.OriginalContentHash)
		require.Equal(t, item.OccurrenceID, traced.Evidence.OccurrenceID)
		reconstructed += traced.Evidence.Content
	}
	require.Equal(t, content, reconstructed)
	calls := provider.calls.Load()
	replay, err := service.Remember(a, req)
	require.NoError(t, err)
	require.Equal(t, result, replay)
	require.Equal(t, calls, provider.calls.Load())

	t.Run("one lifecycle event links the complete replacement set", func(t *testing.T) {
		replacement := request("Atlas uses pgvector.\nBoreal uses Valkey.", domain.MemorySpaceTeamShared)
		replacement.Evidence[0].SupersedesEvidenceIDs = []string{result.Evidence[0].EvidenceID, result.Evidence[1].EvidenceID}
		updated, err := service.Remember(a, replacement)
		require.NoError(t, err)
		require.Len(t, updated.Evidence, 2)
		require.Len(t, updated.Evidence[0].SupersededEvidenceIDs, 2)
		require.Empty(t, updated.Evidence[1].SupersededEvidenceIDs)
		traced, err := traceStore.TraceEvidence(a, trace.NormalizeInput(trace.Input{TeamID: actorA.TeamID.String(), EvidenceID: result.Evidence[0].EvidenceID}))
		require.NoError(t, err)
		require.Len(t, traced.Lifecycle, 1)
		require.ElementsMatch(t, []string{updated.Evidence[0].EvidenceID, updated.Evidence[1].EvidenceID}, traced.Lifecycle[0].ReplacementEvidenceIDs)
		for _, other := range []context.Context{b, c} {
			unauthorized := request("Unauthorized first.\nUnauthorized second.", domain.MemorySpaceTeamShared)
			unauthorized.Evidence[0].SupersedesEvidenceIDs = []string{updated.Evidence[0].EvidenceID}
			denied, err := service.Remember(other, unauthorized)
			require.Error(t, err)
			if denied != nil {
				require.Equal(t, "failed", denied.ProcessingState)
			}
		}
	})

	t.Run("original hash and revision fencing survive partitioning", func(t *testing.T) {
		source := request("Source revision one.\nSource second meaning.", domain.MemorySpaceTeamShared)
		source.Evidence[0].SourceKey = "partition-source-" + uuid.NewString()
		source.Evidence[0].SourceRevision = "r1"
		first, err := service.Remember(a, source)
		require.NoError(t, err)
		second := source
		second.IdempotencyKey = uuid.NewString()
		second.Evidence = append([]remember.RememberEvidenceInput(nil), source.Evidence...)
		second.Evidence[0].Content = "Changed source revision.\nChanged independent meaning."
		second.Evidence[0].SourceRevision = "r2"
		second.Evidence[0].PreviousSourceRevision = "r1"
		newer, err := service.Remember(a, second)
		require.NoError(t, err)
		require.Len(t, newer.Evidence, 2)
		traced, err := traceStore.TraceEvidence(a, trace.NormalizeInput(trace.Input{TeamID: actorA.TeamID.String(), EvidenceID: first.Evidence[0].EvidenceID}))
		require.NoError(t, err)
		require.Equal(t, "r1", traced.Evidence.RevisionToken)
		require.NotEqual(t, traced.Evidence.SourceRevisionID, traced.Evidence.CurrentRevisionID)
		source.IdempotencyKey = uuid.NewString()
		source.Evidence[0].SourceRevision = "r3"
		source.Evidence[0].PreviousSourceRevision = "r1"
		_, err = service.Remember(a, source)
		require.Error(t, err)
		failed := failedResult(err)
		require.EqualValues(t, 0, count("remember_submitted_items", failed.SubmissionID))
		reached, release := make(chan struct{}), make(chan struct{})
		provider.beforeAssessment = func(ctx context.Context) {
			close(reached)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		losing := second
		losing.IdempotencyKey = uuid.NewString()
		losing.Evidence = append([]remember.RememberEvidenceInput(nil), second.Evidence...)
		losing.Evidence[0].Content = "Losing concurrent revision.\nLosing tail."
		losing.Evidence[0].SourceRevision = "r3-loser"
		losing.Evidence[0].PreviousSourceRevision = "r2"
		done := make(chan error, 1)
		go func() { _, err := service.Remember(a, losing); done <- err }()
		select {
		case <-reached:
		case <-time.After(5 * time.Second):
			t.Fatal("source racer did not reach assessment")
		}
		provider.beforeAssessment = nil
		winner := losing
		winner.IdempotencyKey = uuid.NewString()
		winner.Evidence = append([]remember.RememberEvidenceInput(nil), losing.Evidence...)
		winner.Evidence[0].Content = "Winning concurrent revision.\nWinning tail."
		winner.Evidence[0].SourceRevision = "r3-winner"
		winning, err := service.Remember(a, winner)
		require.NoError(t, err)
		require.Len(t, winning.Evidence, 2)
		close(release)
		err = <-done
		require.Error(t, err)
		failed = failedResult(err)
		require.Equal(t, "stale_input", failed.Errors[0].Code)
		for _, table := range []string{"knowledge_ingests", "evidence_fragments", "evidence_occurrences", "remember_submitted_items", "semantic_assessments"} {
			require.EqualValues(t, 0, count(table, failed.SubmissionID), table)
		}
	})

	t.Run("late failure rolls back provenance and all semantic state", func(t *testing.T) {
		function := "partition_rollback_" + strings.ReplaceAll(uuid.NewString(), "-", "")
		require.NoError(t, admin.Exec(fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.idempotency_key LIKE 'partition-rollback-%%' AND NEW.outcome='completed' THEN RAISE EXCEPTION 'partition late rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER %s BEFORE INSERT ON remember_attempts FOR EACH ROW EXECUTE FUNCTION %s()`, function, function, function)).Error)
		defer func() {
			require.NoError(t, admin.Exec(fmt.Sprintf("DROP TRIGGER %s ON remember_attempts; DROP FUNCTION %s()", function, function)).Error)
		}()
		adverse := request("Rollback original.\nRollback suffix.", domain.MemorySpaceTeamShared)
		adverse.IdempotencyKey = "partition-rollback-" + uuid.NewString()
		_, err := service.Remember(a, adverse)
		require.Error(t, err)
		failed := failedResult(err)
		for _, table := range []string{"knowledge_ingests", "evidence_fragments", "evidence_occurrences", "remember_submitted_items", "semantic_assessments"} {
			require.EqualValues(t, 0, count(table, failed.SubmissionID), table)
		}
	})

	t.Run("database rejects incomplete provenance and mutation", func(t *testing.T) {
		err := rls.WithSystemTx(a, admin, func(tx *gorm.DB) error {
			return tx.Exec(`INSERT INTO remember_submitted_items(team_id,source_item_id,ingest_id,owner_profile_id,space_id,space_generation,submitted_index,content,content_hash,policy_version)
    SELECT team_id,?::uuid,ingest_id,owner_profile_id,space_id,space_generation,1,'missing span','sha256:invalid',policy_version FROM remember_submitted_items WHERE team_id=?::uuid AND ingest_id=?::uuid`, uuid.NewString(), actorA.TeamID, result.SubmissionID).Error
		})
		require.ErrorContains(t, err, "coverage is incomplete")
		require.Error(t, rls.WithSystemTx(a, admin, func(tx *gorm.DB) error {
			return tx.Exec("UPDATE remember_submitted_items SET content = 'rewritten' WHERE team_id = ? AND ingest_id = ?", actorA.TeamID, result.SubmissionID).Error
		}))
	})

	t.Run("private isolation legal hold erasure and stale generation", func(t *testing.T) {
		private := request("Private original.\nPrivate suffix 界😀.", domain.MemorySpacePrivate)
		saved, err := service.Remember(a, private)
		require.NoError(t, err)
		for _, other := range []context.Context{b, c} {
			actor, _ := requestctx.ActorFromContext(other)
			_, err := traceStore.TraceEvidence(other, trace.NormalizeInput(trace.Input{TeamID: actor.TeamID.String(), EvidenceID: saved.Evidence[1].EvidenceID}))
			require.Error(t, err)
		}
		var space uuid.UUID
		for _, allowed := range actorA.AllowedSpaces {
			if allowed.Kind == domain.MemorySpacePrivate {
				space = allowed.ID
			}
		}
		require.NotEqual(t, uuid.Nil, space)
		repo := privacy.NewPrivateMemoryRepository(app, rls)
		require.NoError(t, repo.Prepare(t.Context()))
		erase := privacy.PrivateMemoryErasureRequest{TeamID: actorA.TeamID, OwnerID: actorA.OwnerID, CredentialID: *actorA.CredentialID, IdempotencyScopeHash: strings.Repeat("e", 64), RequestHash: strings.Repeat("f", 64), ReasonCode: "owner_requested"}
		_, _, err = repo.PlaceLegalHold(t.Context(), space, "partition provenance")
		require.NoError(t, err)
		_, _, err = repo.RequestCredentialErasure(a, erase)
		require.ErrorIs(t, err, privacy.ErrPrivateMemoryLegalHold)
		_, _, err = repo.ReleaseLegalHold(t.Context(), space)
		require.NoError(t, err)
		reached, release := make(chan struct{}), make(chan struct{})
		provider.beforeAssessment = func(ctx context.Context) {
			close(reached)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		late := request("Stale generation original.\nStale suffix.", domain.MemorySpacePrivate)
		done := make(chan error, 1)
		go func() { _, err := service.Remember(a, late); done <- err }()
		select {
		case <-reached:
		case <-time.After(5 * time.Second):
			t.Fatal("assessment was not reached")
		}
		_, _, err = repo.RequestCredentialErasure(a, erase)
		require.NoError(t, err)
		close(release)
		require.Error(t, <-done)
		provider.beforeAssessment = nil
		claim, err := repo.ClaimNext(t.Context(), "partition-worker", time.Minute)
		require.NoError(t, err)
		require.NotNil(t, claim)
		erased, err := repo.ExecuteClaim(t.Context(), claim.ID, "partition-worker", claim.Fence)
		require.NoError(t, err)
		require.Equal(t, domain.PrivateMemoryErasureCompleted, erased.Status)
		for _, table := range []string{"remember_submitted_items", "remember_item_partitions"} {
			var remaining int64
			require.NoError(t, rls.WithSystemTx(t.Context(), admin, func(tx *gorm.DB) error {
				return tx.Table(table).Where("team_id = ? AND space_id = ?", actorA.TeamID, space).Count(&remaining).Error
			}))
			require.Zero(t, remaining)
		}
		_, err = service.Remember(a, private)
		require.Error(t, err)
	})

	t.Run("cancellation before publication leaves no partition authority", func(t *testing.T) {
		ctx, cancel := context.WithCancel(b)
		provider.beforeAssessment = func(context.Context) { cancel() }
		defer func() { provider.beforeAssessment = nil }()
		cancelled := request("Cancelled original.\nCancelled tail.", domain.MemorySpaceTeamShared)
		_, err := service.Remember(ctx, cancelled)
		require.Error(t, err)
		require.NoError(t, rls.WithSystemTx(t.Context(), admin, func(tx *gorm.DB) error {
			var rows int64
			err := tx.Table("knowledge_ingests").Where("idempotency_key = ?", cancelled.IdempotencyKey).Count(&rows).Error
			require.Zero(t, rows)
			return err
		}))
	})
}
