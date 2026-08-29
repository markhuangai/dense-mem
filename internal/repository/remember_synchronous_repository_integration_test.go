package repository

import (
	"context"
	"errors"
	"fmt"
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

	var phase, eventKind string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT phase, event_kind
			FROM remember_attempt_events
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid
			ORDER BY sequence_no ASC
			LIMIT 1
		`, teamID, ownerID).Row().Scan(&phase, &eventKind)
	}))
	assert.Equal(t, "preflight", phase)
	assert.Equal(t, "preflight_quarantined", eventKind)
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
	require.NotEmpty(t, plan.Documents)
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

func TestSynchronousRememberEmbeddingPlanRejectsOverBudgetDocuments(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-embedding-budget", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-embedding-budget-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-embedding-budget-owner")
	repo := NewLedgerRepository(appDB, rls)

	evidence := make([]EvidenceInput, 100)
	for index := range evidence {
		content := fmt.Sprintf("budget evidence %03d", index)
		evidence[index] = EvidenceInput{Content: content, ContentHash: sha256Hex(content)}
	}
	observations := make([]SubmissionAssessmentRelationshipObservationInput, 157)
	for index := range observations {
		observations[index] = SubmissionAssessmentRelationshipObservationInput{
			RelationshipRef: fmt.Sprintf("budget-relationship-%03d", index),
			Observation: PlacementRelationshipDecisionInput{
				Ref: fmt.Sprintf("budget-relationship-%03d", index), SubjectRef: fmt.Sprintf("subject-%03d", index), ObjectRef: fmt.Sprintf("object-%03d", index),
				PredicateKey: "stores_memory_in", PromoteToFact: true, Polarity: "+",
			},
		}
	}
	plan, err := repo.PlanSynchronousRememberEmbeddings(ctx,
		CreateIngestInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "sync-embedding-budget", RequestHash: "sync-embedding-budget-hash", Status: string(domain.PlacementRunQueued), TelemetryRemember: true, Evidence: evidence},
		CommitSubmissionAssessmentInput{SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{TeamID: teamID, OwnerProfileID: ownerID}, RelationshipObservations: observations},
	)

	require.Error(t, err)
	require.Nil(t, plan)
	require.ErrorIs(t, err, ErrSynchronousRememberEmbeddingFence)
	require.ErrorIs(t, err, ErrSynchronousRememberEmbeddingInputBudget)
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
	input.Attempt.ErrorCode = "no_supported_memory"
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
	assert.Equal(t, []int64{0, 0, 0, 0}, []int64{canonicalRows, entities, relationships, documents})

	var attemptCode, eventCode string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT attempt.error_code, event.metadata ->> 'error_code' FROM remember_attempts AS attempt JOIN remember_attempt_events AS event ON event.team_id = attempt.team_id AND event.attempt_id = attempt.attempt_id WHERE attempt.team_id = ?::uuid AND attempt.owner_profile_id = ?::uuid`, teamID, ownerID).Row().Scan(&attemptCode, &eventCode)
	}))
	assert.Equal(t, "no_supported_memory", attemptCode)
	assert.Equal(t, attemptCode, eventCode)
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

func TestSynchronousRememberRejectsStaleSupersessionTargetAfterPlanning(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-supersession-fence", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-supersession-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-supersession-fence-owner")
	repo := NewLedgerRepository(appDB, rls)
	target, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "sync-supersession-target", RequestHash: "sync-supersession-target-hash",
		Evidence: []EvidenceInput{{Content: "Original supersession target."}},
	})
	require.NoError(t, err)
	require.Len(t, target.Evidence, 1)

	input := synchronousRememberAcceptedFixture(teamID, ownerID, "sync-supersession-stale", "sync-supersession-stale-hash", nil)
	input.CreateIngest.Evidence[0].SupersedesEvidenceIDs = []string{target.Evidence[0].FragmentID}
	prepareSynchronousRememberInline(t, repo, input.CreateIngest, &input)

	_, err = repo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "sync-supersession-mutator", RequestHash: "sync-supersession-mutator-hash",
		Evidence: []EvidenceInput{{Content: "A competing replacement wins first.", IdempotencyKey: "sync-supersession-mutator-evidence", SupersedesEvidenceIDs: []string{target.Evidence[0].FragmentID}}},
	})
	require.NoError(t, err)

	_, err = repo.CommitSynchronousRemember(ctx, input)
	var preflight *RememberPreflightError
	require.ErrorAs(t, err, &preflight)
	require.Contains(t, preflight.Issues, RememberPreflightIssue{Path: "/evidence/0/supersedes_evidence_ids/0", Code: "stale", Message: "supersession target is stale"})

	var candidateIngests, candidateAttempts int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = 'sync-supersession-stale'`, teamID, ownerID).Scan(&candidateIngests).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*) FROM remember_attempts WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = 'sync-supersession-stale'`, teamID, ownerID).Scan(&candidateAttempts).Error
	}))
	assert.Zero(t, candidateIngests)
	assert.Zero(t, candidateAttempts)
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
	input.CreateIngest.Evidence[0].InitialEvent = &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass", Reason: "deterministic intake scan passed"}
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

	var jobs, currentDocuments, passEvents int64
	currentHashes := make(map[string]struct{})
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM embedding_jobs WHERE team_id = ?::uuid`, teamID).Scan(&jobs).Error; err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM evidence_security_events WHERE team_id = ?::uuid AND ingest_id = ?::uuid AND event_kind = 'deterministic_scan' AND decision = 'pass'`, teamID, committed.Ingest.IngestID).Scan(&passEvents).Error; err != nil {
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
	assert.Equal(t, int64(1), passEvents)
	assert.GreaterOrEqual(t, currentDocuments, int64(len(plan.Documents)))
	plannedHashes := make(map[string]struct{}, len(plan.Documents))
	for _, document := range plan.Documents {
		plannedHashes[document.Hash] = struct{}{}
	}
	assert.Equal(t, plannedHashes, currentHashes)
}

func TestSynchronousRememberInlineCommitRetiresExistingEmbeddingJob(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-inline-retire", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-inline-retire-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-inline-retire-owner")
	semantic := NewSemanticRepository(appDB, rls)
	entity := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "concept", "Authoritative Dense-Mem")
	search := NewSearchRepository(appDB, rls)
	target, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "entity", SourceID: entity.EntityID,
		SourceVersion: int64(entity.Version), ProjectionFormat: 1,
		DocumentText: entitySearchProjectionText(entity.CanonicalName, entity.EntityKind),
	})
	require.NoError(t, err)
	require.NotEmpty(t, target.QueuedJobID)
	unrelated, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: uuid.NewString(),
		SourceVersion: 1, ProjectionFormat: 1, DocumentText: "unrelated asynchronous embedding document",
	})
	require.NoError(t, err)
	require.NotEmpty(t, unrelated.QueuedJobID)

	repo := NewLedgerRepository(appDB, rls)
	input := synchronousRememberAcceptedFixture(teamID, ownerID, "sync-inline-retire", "sync-inline-retire-hash", nil)
	configureSynchronousRememberEntityReuse(t, repo, &input, entity)

	committed, err := repo.CommitSynchronousRemember(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, committed)

	var targetState, targetJobStatus, unrelatedJobStatus string
	var targetHasEmbedding bool
	var targetRunnableJobs int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT search_state, embedding IS NOT NULL
			FROM search_documents
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, target.SearchDocumentID).Row().Scan(&targetState, &targetHasEmbedding); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, target.QueuedJobID).Row().Scan(&targetJobStatus); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT count(*) FROM embedding_jobs
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
			  AND status IN ('queued', 'processing', 'failed')
		`, teamID, target.SearchDocumentID).Scan(&targetRunnableJobs).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT status FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, unrelated.QueuedJobID).Row().Scan(&unrelatedJobStatus)
	}))
	assert.Equal(t, string(domain.SearchProjectionCurrent), targetState)
	assert.True(t, targetHasEmbedding)
	assert.Equal(t, string(domain.EmbeddingJobStale), targetJobStatus)
	assert.Zero(t, targetRunnableJobs)
	assert.Equal(t, string(domain.EmbeddingJobQueued), unrelatedJobStatus)
}

func TestSynchronousRememberInlineCommitRollsBackWhenEmbeddingJobRetirementFails(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-inline-retire-failure", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-inline-retire-failure-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-inline-retire-failure-owner")
	semantic := NewSemanticRepository(appDB, rls)
	entity := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "concept", "Authoritative Dense-Mem")
	search := NewSearchRepository(appDB, rls)
	target, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "entity", SourceID: entity.EntityID,
		SourceVersion: int64(entity.Version), ProjectionFormat: 1,
		DocumentText: entitySearchProjectionText(entity.CanonicalName, entity.EntityKind),
	})
	require.NoError(t, err)
	require.NotEmpty(t, target.QueuedJobID)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			CREATE FUNCTION test_synchronous_remember_embedding_job_retirement_failure()
			RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				RAISE EXCEPTION 'intentional synchronous Remember embedding job retirement failure';
			END;
			$$;
			CREATE TRIGGER test_synchronous_remember_embedding_job_retirement_failure
			BEFORE UPDATE OF status ON embedding_jobs
			FOR EACH ROW WHEN (NEW.status = 'stale')
			EXECUTE FUNCTION test_synchronous_remember_embedding_job_retirement_failure()
		`).Error
	}))
	defer func() {
		err := rls.WithSystemTx(context.Background(), adminDB, func(tx *gorm.DB) error {
			if err := tx.Exec(`DROP TRIGGER IF EXISTS test_synchronous_remember_embedding_job_retirement_failure ON embedding_jobs`).Error; err != nil {
				return err
			}
			return tx.Exec(`DROP FUNCTION IF EXISTS test_synchronous_remember_embedding_job_retirement_failure()`).Error
		})
		if err != nil {
			t.Errorf("clean up embedding job retirement failure trigger: %v", err)
		}
	}()

	repo := NewLedgerRepository(appDB, rls)
	input := synchronousRememberAcceptedFixture(teamID, ownerID, "sync-inline-retire-failure", "sync-inline-retire-failure-hash", nil)
	configureSynchronousRememberEntityReuse(t, repo, &input, entity)

	_, err = repo.CommitSynchronousRemember(ctx, input)
	require.Error(t, err)
	var stageErr *synchronousRememberEmbeddingStageError
	require.ErrorAs(t, err, &stageErr)
	assert.Equal(t, "job_retirement", stageErr.SynchronousRememberEmbeddingStage())

	var targetState, targetJobStatus string
	var targetHasEmbedding bool
	var attempts int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT search_state, embedding IS NOT NULL
			FROM search_documents
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, target.SearchDocumentID).Row().Scan(&targetState, &targetHasEmbedding); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, target.QueuedJobID).Row().Scan(&targetJobStatus); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*) FROM remember_attempts
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = 'sync-inline-retire-failure'
		`, teamID, ownerID).Scan(&attempts).Error
	}))
	assert.Equal(t, string(domain.SearchProjectionPending), targetState)
	assert.False(t, targetHasEmbedding)
	assert.Equal(t, string(domain.EmbeddingJobQueued), targetJobStatus)
	assert.Zero(t, attempts)
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

func TestSynchronousRememberPreservesReusedEntitySearchDocumentOwner(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "sync-remember-reused-entity-owner", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "sync-remember-reused-entity-owner-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-reused-entity-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "sync-remember-reused-entity-owner-b")
	semantic := NewSemanticRepository(appDB, rls)
	entity := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "concept", "Authoritative Dense-Mem")
	repo := NewLedgerRepository(appDB, rls)

	commitReusingEntity := func(key string) SynchronousRememberCommitInput {
		input := synchronousRememberAcceptedFixture(teamID, ownerB, key, key+"-hash", nil)
		originalBuildCommit := input.BuildCommit
		input.BuildCommit = func(created *CreateIngestResult, scope SubmissionAssessmentRunScope) (PersistSubmissionAssessmentInput, CommitSubmissionAssessmentInput, error) {
			persist, commit, err := originalBuildCommit(created, scope)
			if err != nil {
				return PersistSubmissionAssessmentInput{}, CommitSubmissionAssessmentInput{}, err
			}
			for index := range commit.EntityResolutions {
				if commit.EntityResolutions[index].Resolution.MentionRef != "orion" {
					continue
				}
				commit.EntityResolutions[index].Resolution.Action = string(domain.EntityResolutionReuse)
				commit.EntityResolutions[index].Resolution.EntityID = entity.EntityID
				commit.EntityResolutions[index].Resolution.ExactEntityID = entity.EntityID
				commit.EntityResolutions[index].Resolution.EntityKind = entity.EntityKind
				commit.EntityResolutions[index].Resolution.CanonicalName = entity.CanonicalName
			}
			return persist, commit, nil
		}
		prepareSynchronousRememberInline(t, repo, input.CreateIngest, &input)
		return input
	}

	first := commitReusingEntity("sync-reused-entity-owner-first")
	_, err := repo.CommitSynchronousRemember(ctx, first)
	require.NoError(t, err)
	second := commitReusingEntity("sync-reused-entity-owner-second")
	_, err = repo.CommitSynchronousRemember(ctx, second)
	require.NoError(t, err)

	var documentCount int64
	var documentOwner string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*) FROM search_documents WHERE team_id = ?::uuid AND source_kind = 'entity' AND source_id = ?::uuid`, teamID, entity.EntityID).Scan(&documentCount).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT owner_profile_id::text FROM search_documents WHERE team_id = ?::uuid AND source_kind = 'entity' AND source_id = ?::uuid`, teamID, entity.EntityID).Row().Scan(&documentOwner)
	}))
	assert.Equal(t, int64(1), documentCount)
	assert.Equal(t, ownerA, documentOwner)
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
