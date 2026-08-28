package repository

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSynchronousRememberPreflightQuarantineLeavesCanonicalStateEmpty(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-preflight-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-preflight-owner")
	repo := NewLedgerRepository(appDB, rls)

	err := repo.RecordSynchronousRememberPreflightQuarantine(ctx, synchronousRememberAttemptFixture(teamID, ownerID, "sync-preflight", "sync-preflight-hash"))
	require.NoError(t, err)

	var canonicalRows, attempts int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT
				(SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM evidence_fragments WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM placement_runs WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM placement_assessments WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM search_documents WHERE team_id = ?::uuid)
		`, teamID, teamID, teamID, teamID, teamID).Scan(&canonicalRows).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid`, teamID, ownerID).Scan(&attempts).Error
	}))
	assert.Zero(t, canonicalRows)
	assert.Equal(t, int64(1), attempts)
}

func TestSynchronousRememberVectorFenceRollsBackCanonicalAndAttemptState(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-vector-fence", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-vector-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-vector-fence-owner")
	repo := NewLedgerRepository(appDB, rls)
	input := synchronousRememberAcceptedFixture(teamID, ownerID, "sync-vector-fence", "sync-vector-fence-hash", nil)
	previewCommit := synchronousRememberCommitInput(synchronousRememberPreviewCreate(input.CreateIngest), SubmissionAssessmentRunScope{TeamID: teamID, OwnerProfileID: ownerID})
	plan, err := repo.PlanSynchronousRememberEmbeddings(ctx, input.CreateIngest, previewCommit)
	require.NoError(t, err)
	inline := SynchronousRememberEmbeddingResult{EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions, EmbeddingModel: plan.EmbeddingModel, SearchGenerationID: plan.SearchGenerationID, SearchGenerationVersion: plan.SearchGenerationVersion}
	for index, document := range plan.Documents {
		hash := document.Hash
		if index == 0 {
			hash = "not-a-rendered-document"
		}
		inline.Embeddings = append(inline.Embeddings, SynchronousRememberEmbedding{DocumentHash: hash, Vector: []float32{1, 0, 0}})
	}
	input.InlineEmbeddings = &inline

	_, err = repo.CommitSynchronousRemember(ctx, input)
	require.ErrorIs(t, err, ErrSynchronousRememberEmbeddingFence)

	var canonicalRows, attempts int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT
				(SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM evidence_fragments WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM placement_runs WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM placement_assessments WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM entity_records WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM relationship_records WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM search_documents WHERE team_id = ?::uuid)
		`, teamID, teamID, teamID, teamID, teamID, teamID, teamID).Scan(&canonicalRows).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid`, teamID, ownerID).Scan(&attempts).Error
	}))
	assert.Zero(t, canonicalRows)
	assert.Zero(t, attempts)
}

func TestSynchronousRememberPublicResultFailureRollsBackCanonicalAndAttemptState(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-public-result-failure", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-public-result-failure-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-public-result-failure-owner")
	repo := NewLedgerRepository(appDB, rls)
	input := synchronousRememberAcceptedFixture(teamID, ownerID, "sync-public-result-failure", "sync-public-result-failure-hash", nil)
	prepareSynchronousRememberInline(t, repo, input.CreateIngest, &input)
	input.BuildPublicResult = func(*CreateIngestResult, *CommitSubmissionAssessmentResult) (map[string]any, error) {
		return nil, errors.New("public result construction failed")
	}

	_, err := repo.CommitSynchronousRemember(ctx, input)
	require.Error(t, err)
	require.Contains(t, err.Error(), "public_result")

	var canonicalRows, attempts int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT
				(SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM evidence_fragments WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM placement_runs WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM placement_assessments WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM entity_records WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM relationship_records WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM search_documents WHERE team_id = ?::uuid)
		`, teamID, teamID, teamID, teamID, teamID, teamID, teamID).Scan(&canonicalRows).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid`, teamID, ownerID).Scan(&attempts).Error
	}))
	assert.Zero(t, canonicalRows)
	assert.Zero(t, attempts)
}

func TestSynchronousRememberRejectedTerminalCommitsNoCanonicalMemoryOrSearch(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-rejected-terminal-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-rejected-terminal-owner")
	repo := NewLedgerRepository(appDB, rls)
	input := synchronousRememberAcceptedFixture(teamID, ownerID, "sync-rejected-terminal", "sync-rejected-terminal-hash", nil)
	input.Attempt.Outcome = "rejected"
	input.BuildCommit = nil
	terminalInput := SynchronousRememberTerminalInput{
		CreateIngest: input.CreateIngest,
		Attempt:      input.Attempt,
		BuildTerminal: func(_ *CreateIngestResult, scope SubmissionAssessmentRunScope) (*PersistSubmissionAssessmentInput, CompleteSubmissionAssessmentInput, error) {
			return nil, CompleteSubmissionAssessmentInput{
				SubmissionAssessmentRunScope: scope,
				OutcomeKind:                  "submission_assessment_rejected",
				Status:                       string(domain.SemanticReviewRejected),
				Category:                     "rejected",
			}, nil
		},
		BuildPublicResult: func(*CreateIngestResult, *CommitSubmissionAssessmentResult) (map[string]any, error) {
			return map[string]any{"processing_state": "rejected"}, nil
		},
	}

	committed, err := repo.CommitSynchronousRememberTerminal(ctx, terminalInput)
	require.NoError(t, err)
	require.NotNil(t, committed.Attempt)
	require.Equal(t, "rejected", committed.Attempt.Outcome)

	var canonicalRows, entities, relationships, documents int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT
				(SELECT count(*) FROM entity_records WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM relationship_records WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM search_documents WHERE team_id = ?::uuid),
				(SELECT count(*) FROM entity_records WHERE team_id = ?::uuid),
				(SELECT count(*) FROM relationship_records WHERE team_id = ?::uuid),
				(SELECT count(*) FROM search_documents WHERE team_id = ?::uuid)
		`, teamID, teamID, teamID, teamID, teamID, teamID).Row().Scan(&canonicalRows, &entities, &relationships, &documents)
	}))
	assert.Zero(t, canonicalRows)
	assert.Zero(t, entities)
	assert.Zero(t, relationships)
	assert.Zero(t, documents)
}

func TestSynchronousRememberRejectsSearchGenerationRotationAfterPlanning(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	contractID := insertSearchTestContract(t, adminDB, rls, "sync-remember-generation-rotation", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-generation-rotation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-generation-rotation-owner")
	repo := NewLedgerRepository(appDB, rls)
	input := synchronousRememberAcceptedFixture(teamID, ownerID, "sync-generation-rotation", "sync-generation-rotation-hash", nil)
	prepareSynchronousRememberInline(t, repo, input.CreateIngest, &input)
	oldGenerationID := input.InlineEmbeddings.SearchGenerationID

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE search_index_generations
			SET activation_state = 'deprecated', activated_at = NULL
			WHERE search_index_generation_id = ?::uuid AND embedding_contract_id = ?::uuid
		`, oldGenerationID, contractID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO search_index_generations (
			    search_index_generation_id, generation, embedding_contract_id,
			    embedding_dimensions, ann_strategy, operator_class,
			    indexed_expression, physical_index_name, exact_max_rows,
			    allow_exact_fallback, activation_state, activated_at
			)
			SELECT ?::uuid, generation + 1, embedding_contract_id,
			       embedding_dimensions, ann_strategy, operator_class,
			       indexed_expression, physical_index_name, exact_max_rows,
			       allow_exact_fallback, 'active', now()
			FROM search_index_generations
			WHERE search_index_generation_id = ?::uuid
		`, uuid.NewString(), oldGenerationID).Error
	}))

	_, err := repo.CommitSynchronousRemember(ctx, input)
	require.ErrorIs(t, err, ErrSynchronousRememberEmbeddingFence)

	var canonicalRows int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT
				(SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM evidence_fragments WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM placement_runs WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM placement_assessments WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM entity_records WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM relationship_records WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM search_documents WHERE team_id = ?::uuid) +
				(SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid)
		`, teamID, teamID, teamID, teamID, teamID, teamID, teamID, teamID).Scan(&canonicalRows).Error
	}))
	assert.Zero(t, canonicalRows)
}

func TestSynchronousRememberRejectsStaleSourceAfterPlanning(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-source-fence", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-source-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-source-fence-owner")
	repo := NewLedgerRepository(appDB, rls)
	const sourceKey = "doc://sync-remember-source-fence"
	content := "Orion links Vega."
	seed, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "sync-source-seed", RequestHash: "sync-source-seed-hash",
		Evidence: []EvidenceInput{{
			Content: content, ContentHash: sha256Hex(content), SourceType: "document", Authority: "primary",
			SourceKey: sourceKey, SourceRevisionToken: "rev-1", SourceRevisionContentHash: sha256Hex(content),
			SourceRevisionEnvelope: map[string]any{"source": "wiki", "section": "sync"},
		}},
	})
	require.NoError(t, err)
	require.Len(t, seed.Evidence, 1)

	input := synchronousRememberAcceptedFixture(teamID, ownerID, "sync-source-fence", "sync-source-fence-hash", nil)
	input.CreateIngest.Evidence[0] = EvidenceInput{
		Content: content, ContentHash: sha256Hex(content), SourceType: "document", Authority: "primary",
		SourceKey: sourceKey, SourceRevisionToken: "rev-1", SourceRevisionContentHash: sha256Hex(content),
		SourceRevisionEnvelope: map[string]any{"source": "wiki", "section": "sync"},
	}
	prepareSynchronousRememberInline(t, repo, input.CreateIngest, &input)

	_, err = repo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKey: sourceKey, SourceKind: sourceKindForEvidence("document"), Authority: "primary",
		RevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1", ContentHash: sha256Hex("Orion links Lyra."),
		Envelope: map[string]any{"source": "wiki", "section": "sync"},
	})
	require.NoError(t, err)

	_, err = repo.CommitSynchronousRemember(ctx, input)
	var preflight *RememberPreflightError
	require.ErrorAs(t, err, &preflight)
	require.Contains(t, preflight.Issues, RememberPreflightIssue{Path: "/evidence/0/previous_source_revision", Code: "stale", Message: "source revision is stale"})

	var syncIngests, syncAttempts int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = 'sync-source-fence'`, teamID, ownerID).Scan(&syncIngests).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = 'sync-source-fence'`, teamID, ownerID).Scan(&syncAttempts).Error
	}))
	assert.Zero(t, syncIngests)
	assert.Zero(t, syncAttempts)
}

func TestSynchronousRememberInlineCommitAddsNoEmbeddingJobs(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-inline", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-inline-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-inline-owner")
	repo := NewLedgerRepository(appDB, rls)

	input := synchronousRememberAcceptedFixture(teamID, ownerID, "sync-inline", "sync-inline-hash", nil)
	previewCreate := input.CreateIngest
	previewCommit := synchronousRememberCommitInput(synchronousRememberPreviewCreate(previewCreate), SubmissionAssessmentRunScope{TeamID: teamID, OwnerProfileID: ownerID})
	plan, err := repo.PlanSynchronousRememberEmbeddings(ctx, previewCreate, previewCommit)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Documents)
	inline := SynchronousRememberEmbeddingResult{EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions, EmbeddingModel: plan.EmbeddingModel, SearchGenerationID: plan.SearchGenerationID, SearchGenerationVersion: plan.SearchGenerationVersion}
	for _, document := range plan.Documents {
		inline.Embeddings = append(inline.Embeddings, SynchronousRememberEmbedding{DocumentHash: document.Hash, Vector: []float32{1, 0, 0}})
	}
	input.InlineEmbeddings = &inline

	committed, err := repo.CommitSynchronousRemember(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, committed.Attempt)

	var jobs, currentDocuments int64
	currentHashes := make(map[string]struct{})
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM embedding_jobs WHERE team_id = ?::uuid`, teamID).Scan(&jobs).Error; err != nil {
			return err
		}
		rows, err := tx.Raw(`SELECT document_hash FROM search_documents WHERE team_id = ?::uuid AND search_state = 'current'`, teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var hash string
			if err := rows.Scan(&hash); err != nil {
				return err
			}
			currentDocuments++
			currentHashes[hash] = struct{}{}
		}
		return rows.Err()
	}))
	assert.Zero(t, jobs)
	assert.GreaterOrEqual(t, currentDocuments, int64(len(plan.Documents)))
	plannedHashes := make(map[string]struct{}, len(plan.Documents))
	for _, document := range plan.Documents {
		plannedHashes[document.Hash] = struct{}{}
	}
	assert.Equal(t, plannedHashes, currentHashes)
}

func TestSynchronousRememberInlineCommitSupportsTypedValueProjection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-inline-value", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-inline-value-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-inline-value-owner")
	repo := NewLedgerRepository(appDB, rls)
	input := synchronousRememberAcceptedFixture(teamID, ownerID, "sync-inline-value", "sync-inline-value-hash", nil)
	originalBuildCommit := input.BuildCommit
	input.BuildCommit = func(created *CreateIngestResult, scope SubmissionAssessmentRunScope) (PersistSubmissionAssessmentInput, CommitSubmissionAssessmentInput, error) {
		persist, commit, err := originalBuildCommit(created, scope)
		if err != nil {
			return PersistSubmissionAssessmentInput{}, CommitSubmissionAssessmentInput{}, err
		}
		observation := &commit.RelationshipObservations[0].Observation
		observation.ObjectRef = ""
		observation.ObjectValue = &PlacementValueInput{Ref: "value:postgresql", ValueType: "string", CanonicalValue: "PostgreSQL", Display: "PostgreSQL"}
		for index := range commit.PredicateRegistrations {
			if commit.PredicateRegistrations[index].RelationshipRef == observation.Ref {
				commit.PredicateRegistrations[index].PredicateKey = "stores_memory_in"
				commit.PredicateRegistrations[index].ObjectKind = "string"
			}
		}
		return persist, commit, nil
	}
	prepareSynchronousRememberInline(t, repo, input.CreateIngest, &input)

	committed, err := repo.CommitSynchronousRemember(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, committed)

	var valueDocuments, relationshipDocuments int64
	var relationshipHasValue bool
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM search_documents WHERE team_id = ?::uuid AND source_kind = 'value'`, teamID).Scan(&valueDocuments).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*), EXISTS (SELECT 1 FROM search_documents WHERE team_id = ?::uuid AND source_kind = 'relationship' AND document_text LIKE '%PostgreSQL%') FROM search_documents WHERE team_id = ?::uuid AND source_kind = 'relationship'`, teamID, teamID).Row().Scan(&relationshipDocuments, &relationshipHasValue); err != nil {
			return err
		}
		return nil
	}))
	assert.Zero(t, valueDocuments)
	assert.GreaterOrEqual(t, relationshipDocuments, int64(1))
	assert.True(t, relationshipHasValue)
}

func TestSynchronousRememberAttemptRLSIsolatesTeamAndProfileABC(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "sync-remember-rls-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "sync-remember-rls-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "sync-remember-rls-owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "sync-remember-rls-team-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "sync-remember-rls-owner-c")
	repo := NewLedgerRepository(appDB, rls)
	require.NoError(t, repo.RecordSynchronousRememberPreflightQuarantine(ctx, synchronousRememberAttemptFixture(teamA, ownerA, "sync-rls-key", "sync-rls-hash")))

	for _, actor := range []struct {
		teamID, ownerID string
		expected        int64
	}{{teamA, ownerA, 1}, {teamA, ownerB, 1}, {teamC, ownerC, 0}} {
		var visible int64
		require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, actor.teamID, actor.ownerID, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND idempotency_key = 'sync-rls-key'`, teamA).Scan(&visible).Error
		}))
		assert.Equal(t, actor.expected, visible, "actor=%s/%s", actor.teamID, actor.ownerID)
	}
}

func TestSynchronousRememberConcurrentSameKeyHasOneTerminalWinner(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-concurrent-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-concurrent-owner")
	repo := NewLedgerRepository(appDB, rls)
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			attempt := synchronousRememberAttemptFixture(teamID, ownerID, "sync-concurrent-key", "sync-concurrent-hash")
			attempt.AttemptID = uuid.NewString()
			errs <- repo.RecordSynchronousRememberPreflightQuarantine(ctx, attempt)
		}(index)
	}
	close(start)
	wg.Wait()
	close(errs)

	var success, replay int
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrRememberReplay) {
			replay++
		} else {
			t.Fatalf("unexpected concurrent attempt error: %v", err)
		}
	}
	assert.Equal(t, 1, success)
	assert.Equal(t, 1, replay)
}

func TestSynchronousRememberFailedAttemptAllowsSameKeyTerminalRetry(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-retry-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-retry-owner")
	repo := NewLedgerRepository(appDB, rls)
	failed := synchronousRememberAttemptFixture(teamID, ownerID, "sync-retry-key", "sync-retry-hash")
	failed.Outcome, failed.FailedPhase, failed.ErrorCode = "failed", "assessment", "provider_unavailable"
	require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{Attempt: failed}))

	retried := synchronousRememberAttemptFixture(teamID, ownerID, "sync-retry-key", "sync-retry-hash")
	require.NoError(t, repo.RecordSynchronousRememberPreflightQuarantine(ctx, retried))
	loaded, err := repo.LoadRememberAttempt(ctx, RememberAttemptLookupInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "sync-retry-key"})
	require.NoError(t, err)
	require.Equal(t, "quarantined", loaded.Outcome)

	var count int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = 'sync-retry-key'`, teamID, ownerID).Scan(&count).Error
	}))
	assert.Equal(t, int64(2), count)
}

func TestSynchronousRememberFailedAttemptFencesDifferentHashAndAllowsMatchingCommit(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-failed-fence", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-failed-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-failed-fence-owner")
	repo := NewLedgerRepository(appDB, rls)
	const key = "sync-failed-fence-key"
	const hash = "sync-failed-fence-hash"
	failed := synchronousRememberAttemptFixture(teamID, ownerID, key, hash)
	failed.Outcome, failed.FailedPhase, failed.ErrorCode = "failed", "embedding", "embedding_unavailable"
	require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{Attempt: failed}))

	retryFailure := failed
	retryFailure.AttemptID = uuid.NewString()
	err := repo.RecordRememberFailure(ctx, RememberFailureRecordInput{Attempt: retryFailure})
	require.NoError(t, err)

	different := synchronousRememberAcceptedFixture(teamID, ownerID, key, "different-request-hash", nil)
	prepareSynchronousRememberInline(t, repo, different.CreateIngest, &different)
	_, err = repo.CommitSynchronousRemember(ctx, different)
	require.ErrorIs(t, err, ErrIdempotencyConflict)

	matching := synchronousRememberAcceptedFixture(teamID, ownerID, key, hash, nil)
	prepareSynchronousRememberInline(t, repo, matching.CreateIngest, &matching)
	_, err = repo.CommitSynchronousRemember(ctx, matching)
	require.NoError(t, err)

	loaded, err := repo.LoadRememberAttempt(ctx, RememberAttemptLookupInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key})
	require.NoError(t, err)
	require.Equal(t, "completed", loaded.Outcome)
	require.Equal(t, hash, loaded.RequestHash)

	var attemptCount, ingestCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?`, teamID, ownerID, key).Scan(&attemptCount).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?`, teamID, ownerID, key).Scan(&ingestCount).Error
	}))
	assert.Equal(t, int64(3), attemptCount)
	assert.Equal(t, int64(1), ingestCount)
}

func TestSynchronousRememberFailedAttemptFencesDifferentHashTerminalCommit(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-failed-terminal-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-failed-terminal-fence-owner")
	repo := NewLedgerRepository(appDB, rls)
	const key = "sync-failed-terminal-fence-key"
	failed := synchronousRememberAttemptFixture(teamID, ownerID, key, "sync-failed-terminal-fence-hash")
	failed.Outcome, failed.FailedPhase, failed.ErrorCode = "failed", "assessment", "provider_unavailable"
	require.NoError(t, repo.RecordRememberFailure(ctx, RememberFailureRecordInput{Attempt: failed}))

	terminalAttempt := synchronousRememberAttemptFixture(teamID, ownerID, key, "different-terminal-request-hash")
	terminalAttempt.Outcome = "rejected"
	_, err := repo.CommitSynchronousRememberTerminal(ctx, SynchronousRememberTerminalInput{
		CreateIngest: CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key, RequestHash: terminalAttempt.RequestHash,
			Status: string(domain.PlacementRunQueued), TelemetryRemember: true,
			Proposal: map[string]any{"relationship_hints": []any{}},
			Evidence: []EvidenceInput{{Content: "A mismatched terminal request must not commit.", ContentHash: sha256Hex("A mismatched terminal request must not commit.")}},
		},
		Attempt: terminalAttempt,
		BuildTerminal: func(_ *CreateIngestResult, scope SubmissionAssessmentRunScope) (*PersistSubmissionAssessmentInput, CompleteSubmissionAssessmentInput, error) {
			return nil, CompleteSubmissionAssessmentInput{
				SubmissionAssessmentRunScope: scope,
				OutcomeKind:                  "submission_assessment_rejected",
				Status:                       string(domain.SemanticReviewRejected),
				Category:                     "rejected",
			}, nil
		},
	})
	require.ErrorIs(t, err, ErrIdempotencyConflict)

	var ingests, attempts int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?`, teamID, ownerID, key).Scan(&ingests).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = ?`, teamID, ownerID, key).Scan(&attempts).Error
	}))
	assert.Zero(t, ingests)
	assert.Equal(t, int64(1), attempts)
}

func TestSynchronousRememberConcurrentAcceptedCommitHasOneCanonicalWinner(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-accepted-concurrent", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-accepted-concurrent-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-accepted-concurrent-owner")
	repo := NewLedgerRepository(appDB, rls)
	inputs := make([]SynchronousRememberCommitInput, 2)
	for index := range inputs {
		inputs[index] = synchronousRememberAcceptedFixture(teamID, ownerID, "sync-accepted-concurrent-key", "sync-accepted-concurrent-hash", nil)
		inputs[index].Attempt.AttemptID = uuid.NewString()
		prepareSynchronousRememberInline(t, repo, inputs[index].CreateIngest, &inputs[index])
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func(input SynchronousRememberCommitInput) {
			defer wg.Done()
			<-start
			_, err := repo.CommitSynchronousRemember(ctx, input)
			errs <- err
		}(inputs[index])
	}
	close(start)
	wg.Wait()
	close(errs)

	var success, replay int
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, ErrRememberReplay) {
			replay++
		} else {
			t.Fatalf("unexpected accepted concurrency error: %v", err)
		}
	}
	assert.Equal(t, 1, success)
	assert.Equal(t, 1, replay)
	var ingests, attempts, jobs, current int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT (SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid), (SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND idempotency_key = 'sync-accepted-concurrent-key'), (SELECT count(*) FROM embedding_jobs WHERE team_id = ?::uuid), (SELECT count(*) FROM search_documents WHERE team_id = ?::uuid AND search_state = 'current')`, teamID, teamID, teamID, teamID).Row().Scan(&ingests, &attempts, &jobs, &current)
	}))
	assert.Equal(t, int64(1), ingests)
	assert.Equal(t, int64(1), attempts)
	assert.Zero(t, jobs)
	assert.Greater(t, current, int64(0))
}

func prepareSynchronousRememberInline(t *testing.T, repo *LedgerRepositoryImpl, create CreateIngestInput, input *SynchronousRememberCommitInput) {
	t.Helper()
	previewCreate := synchronousRememberPreviewCreate(create)
	_, preview, err := input.BuildCommit(previewCreate, SubmissionAssessmentRunScope{
		TeamID: create.TeamID, OwnerProfileID: create.OwnerProfileID, IngestID: previewCreate.IngestID,
		PlacementRunID: previewCreate.PlacementRunID, WorkerID: synchronousRememberWorkerID, ExpectedAttempts: 1, MaxAttempts: 1,
	})
	require.NoError(t, err)
	plan, err := repo.PlanSynchronousRememberEmbeddings(context.Background(), create, preview)
	require.NoError(t, err)
	inline := SynchronousRememberEmbeddingResult{EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions, EmbeddingModel: plan.EmbeddingModel, SearchGenerationID: plan.SearchGenerationID, SearchGenerationVersion: plan.SearchGenerationVersion}
	for _, document := range plan.Documents {
		inline.Embeddings = append(inline.Embeddings, SynchronousRememberEmbedding{DocumentHash: document.Hash, Vector: []float32{1, 0, 0}})
	}
	input.InlineEmbeddings = &inline
}

func synchronousRememberPreviewCreate(create CreateIngestInput) *CreateIngestResult {
	evidence := make([]EvidenceFragment, len(create.Evidence))
	items := make([]PlacementItem, len(create.Evidence))
	for index, input := range create.Evidence {
		fragmentID := uuid.NewString()
		evidence[index] = EvidenceFragment{FragmentID: fragmentID, EvidenceIndex: index, Content: input.Content, ContentHash: input.ContentHash, Authority: input.Authority}
		items[index] = PlacementItem{PlacementItemID: uuid.NewString(), FragmentID: fragmentID, EvidenceIndex: index}
	}
	return &CreateIngestResult{TeamID: create.TeamID, OwnerProfileID: create.OwnerProfileID, IngestID: uuid.NewString(), PlacementRunID: uuid.NewString(), Evidence: evidence, Items: items}
}

func synchronousRememberAttemptFixture(teamID, ownerID, key, hash string) RememberAttemptRecordInput {
	return RememberAttemptRecordInput{TeamID: teamID, OwnerProfileID: ownerID, AttemptID: uuid.NewString(), IdempotencyKey: key, RequestHash: hash, ContractVersion: domain.ContractVersion, SubmissionKind: "remember", PublicResult: map[string]any{"processing_state": "terminal"}, AssessorTurns: 1, Duration: time.Millisecond}
}

func synchronousRememberAcceptedFixture(teamID, ownerID, key, hash string, inline *SynchronousRememberEmbeddingResult) SynchronousRememberCommitInput {
	evidence := []EvidenceInput{
		{Content: "Orion links Vega.", ContentHash: sha256Hex("Orion links Vega.")},
		{Content: "Vega links Lyra.", ContentHash: sha256Hex("Vega links Lyra.")},
	}
	create := CreateIngestInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: key, RequestHash: hash, Status: string(domain.PlacementRunQueued), TelemetryRemember: true, Proposal: map[string]any{"relationship_hints": []any{}}, Evidence: evidence}
	return SynchronousRememberCommitInput{
		CreateIngest: create, InlineEmbeddings: inline,
		Attempt: func() RememberAttemptRecordInput {
			attempt := synchronousRememberAttemptFixture(teamID, ownerID, key, hash)
			attempt.Outcome = "completed"
			return attempt
		}(),
		BuildCommit: func(created *CreateIngestResult, scope SubmissionAssessmentRunScope) (PersistSubmissionAssessmentInput, CommitSubmissionAssessmentInput, error) {
			persist := PersistSubmissionAssessmentInput{TeamID: scope.TeamID, OwnerProfileID: scope.OwnerProfileID, IngestID: scope.IngestID, PlacementRunID: scope.PlacementRunID, RequestID: "sync-remember:" + created.IngestID, AssessorContractVersion: domain.ContractVersion, Model: "sync-remember-test", Tokenizer: "o200k_base", InputTokens: 1, OutputTokens: 1, NormalizedResponse: []byte(`{"request_id":"sync-remember"}`), ResponseHash: sha256Hex("sync-remember-assessment"), ValidatedAt: time.Now().UTC()}
			return persist, synchronousRememberCommitInput(created, scope), nil
		},
	}
}

func synchronousRememberCommitInput(created *CreateIngestResult, scope SubmissionAssessmentRunScope) CommitSubmissionAssessmentInput {
	assessmentID := uuid.NewString()
	run := PlacementRun{TeamID: scope.TeamID, OwnerProfileID: scope.OwnerProfileID, IngestID: created.IngestID, PlacementRunID: created.PlacementRunID, Attempts: scope.ExpectedAttempts}
	input := submissionAssessmentCommitFixture(run, created, assessmentID, false)
	input.SubmissionAssessmentRunScope = scope
	for index := range input.RelationshipObservations {
		input.RelationshipObservations[index].Observation.PromoteToFact = true
	}
	return input
}
