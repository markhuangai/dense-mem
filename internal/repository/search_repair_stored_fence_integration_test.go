package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSearchRepairUpdatesExistingDocumentUsingStoredFenceValues(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-stored-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-stored-fence-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-stored-fence", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	content := "canonical stored fence evidence"
	ingest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "search-repair-stored-fence-evidence",
		RequestHash: sha256Hex(content), Evidence: []EvidenceInput{{Content: content}},
	})
	document, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: ingest.Evidence[0].FragmentID,
		SourceVersion: 1, DocumentText: "stale stored fence evidence",
	})
	require.NoError(t, err)
	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "search-repair-stored-fence-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, more, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, selected, 1)
	require.Equal(t, document.SearchDocumentID, selected[0].SearchDocumentID)
	require.Equal(t, content, selected[0].DocumentText)

	apply, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.5, 0.5}}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, apply.UpdatedCount)
	require.Zero(t, apply.RemainingDriftedCount)

	var repairedText string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT document_text
			FROM search_documents
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		`, teamID, document.SearchDocumentID).Row().Scan(&repairedText)
	}))
	require.Equal(t, content, repairedText)
}

func TestSearchRepairRetiresJobsFromStoredSpaceAfterCanonicalRelationshipSpaceChange(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-space-job-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-space-job-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-space-job", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	spaceRepo := NewMemorySpaceRepository(appDB, rls)
	shared, err := spaceRepo.GetTeamShared(ctx, uuid.MustParse(teamID))
	require.NoError(t, err)
	require.NotNil(t, shared)
	private, err := spaceRepo.EnsureProfilePrivate(ctx, uuid.MustParse(teamID), uuid.MustParse(ownerID))
	require.NoError(t, err)
	var sharedGeneration, privateGeneration int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, shared.ID).Row().Scan(&sharedGeneration); err != nil {
			return err
		}
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?::uuid`, private.ID).Row().Scan(&privateGeneration)
	}))

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Space Moved Subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Space Moved Object")
	content := "Space Moved Subject uses Space Moved Object."
	ingest := createSearchRepairAcceptedIngest(t, ctx, ledger, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, SpaceID: shared.ID.String(), SpaceGeneration: sharedGeneration,
		IdempotencyKey: "search-repair-space-job-evidence", RequestHash: sha256Hex(content), Evidence: []EvidenceInput{{Content: content}},
	})
	decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "search-repair-space-job",
			SpanStart: 0, SpanEnd: len(content), Authority: "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	document, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: decision.Relationship.RelationshipID,
		SourceVersion: int64(decision.Relationship.Version), ProjectionFormat: 2, DocumentText: "stale relationship space",
		SpaceID: shared.ID.String(), SpaceGeneration: sharedGeneration,
	})
	require.NoError(t, err)
	var queuedJobID string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT embedding_job_id::text
			FROM embedding_jobs
			WHERE team_id = ?::uuid AND search_document_id = ?::uuid
			ORDER BY created_at
			LIMIT 1
		`, teamID, document.SearchDocumentID).Row().Scan(&queuedJobID)
	}))
	require.NotEmpty(t, queuedJobID)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE embedding_jobs
			SET status = 'failed', attempts = max_attempts, total_attempts = max_attempts,
			    failure_class = 'transient', failure_code = 'provider_timeout',
			    first_failed_at = now(), last_failed_at = now(), completed_at = now()
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, queuedJobID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			UPDATE relationship_records
			SET space_id = ?::uuid, space_generation = ?
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, private.ID, privateGeneration, teamID, decision.Relationship.RelationshipID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'current', embedding = '[0.5,0.5]'::vector,
			    embedding_updated_at = clock_timestamp(), embedding_error = '', updated_at = clock_timestamp()
			WHERE team_id = ?::uuid AND source_kind = 'evidence' AND source_id = ?::uuid
		`, teamID, ingest.Evidence[0].FragmentID).Error
	}))

	contract, err := repo.GetActiveSearchContract(ctx)
	require.NoError(t, err)
	run, claimed, err := repo.ReserveSearchRepairRun(ctx, SearchRepairRunInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		LocalRunDate: time.Now().UTC(), CreateIfMissing: true, WorkerID: "search-repair-space-job-worker", Lease: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, claimed)
	selected, more, err := repo.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions, Limit: 1,
	})
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, selected, 1)
	require.Equal(t, private.ID.String(), selected[0].SpaceID)

	apply, err := repo.ApplySearchRepair(ctx, ApplySearchRepairInput{
		RunID: run.RunID, LeaseToken: run.LeaseToken,
		EmbeddingContractID: contract.EmbeddingContractID, EmbeddingDimensions: contract.EmbeddingDimensions,
		SearchIndexGenerationID: contract.SearchIndexGenerationID, IndexGeneration: contract.IndexGeneration,
		Documents: []SearchRepairEmbedding{{SearchRepairDocument: selected[0], Embedding: []float32{0.5, 0.5}}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, apply.UpdatedCount)

	var jobStatus string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM embedding_jobs
			WHERE team_id = ?::uuid AND embedding_job_id = ?::uuid
		`, teamID, queuedJobID).Row().Scan(&jobStatus)
	}))
	require.Equal(t, string(domain.EmbeddingJobStale), jobStatus)
}
