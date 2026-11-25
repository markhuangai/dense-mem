//go:build integration

package postgres

import (
	"context"
	"testing"

	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecallEvidenceHydratesEvidenceProvenance(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-provenance-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-provenance-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-provenance", 3, "exact", "")
	ledgerRepo := knowledgepostgres.NewStore(appDB, rls)
	searchRepo := newSearchFixtureStore(appDB, rls)
	content := "Dense Mem records recall provenance for evidence hits."

	ingest, err := ledgerRepo.CreateIngestForTest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "recall-provenance",
		RequestHash:    sha256Hex(content),
		Evidence: []EvidenceInput{{
			Content:    content,
			SourceType: "document",
			SourceRef:  "wiki:recall-provenance",
		}},
	})
	require.NoError(t, err)
	require.Len(t, ingest.Evidence, 1)
	upsertRecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, ingest.Evidence[0])

	recall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID,
		Query:  "recall provenance",
		Limit:  5,
	})
	require.NoError(t, err)
	require.Len(t, recall.Results, 1)
	require.Equal(t, ingest.Evidence[0].FragmentID, recall.Results[0].EvidenceID)
	require.Equal(t, "wiki:recall-provenance", recall.Results[0].Source)
	require.Equal(t, "document", recall.Results[0].SourceType)
	require.False(t, recall.Results[0].CreatedAt.IsZero())
}

func TestRecallAndFullTextSuppressExactAliasesButKeepCanonicalEvidence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-alias-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-alias-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-alias", 3, "exact", "")
	ledgerRepo := knowledgepostgres.NewStore(appDB, rls)
	searchRepo := newSearchFixtureStore(appDB, rls)
	content := "canonical evidence remains visible while aliases are suppressed"
	canonical, err := ledgerRepo.CreateIngestForTest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "recall-alias-canonical",
		RequestHash: sha256Hex("recall-alias-canonical"), Evidence: []EvidenceInput{{Content: content}},
	})
	require.NoError(t, err)
	alias, err := ledgerRepo.CreateIngestForTest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "recall-alias-alias",
		RequestHash: sha256Hex("recall-alias-alias"), Evidence: []EvidenceInput{{Content: content}},
	})
	require.NoError(t, err)
	require.Len(t, canonical.Evidence, 1)
	require.Len(t, alias.Evidence, 1)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO evidence_exact_aliases (
				team_id, alias_fragment_id, alias_owner_profile_id,
				canonical_fragment_id, canonical_owner_profile_id
			) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid)
		`, teamID, alias.Evidence[0].FragmentID, ownerID, canonical.Evidence[0].FragmentID, ownerID).Error
	}))
	upsertRecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, canonical.Evidence[0])
	upsertRecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, alias.Evidence[0])

	fullText, err := searchRepo.SearchFullText(ctx, FullTextSearchInput{TeamID: teamID, Query: "canonical evidence visible", Limit: 10})
	require.NoError(t, err)
	require.Len(t, fullText, 1)
	require.Equal(t, canonical.Evidence[0].FragmentID, fullText[0].SourceID)
	recall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{TeamID: teamID, Query: "canonical evidence visible", Limit: 10})
	require.NoError(t, err)
	require.Len(t, recall.Results, 1)
	require.Equal(t, canonical.Evidence[0].FragmentID, recall.Results[0].EvidenceID)
}

func upsertRecallEvidenceSearchDocumentForTest(
	t *testing.T,
	ctx context.Context,
	repo *searchFixtureStore,
	teamID string,
	ownerID string,
	evidence EvidenceFragment,
) {
	t.Helper()
	doc, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKind:     "evidence",
		SourceID:       evidence.FragmentID,
		SourceVersion:  1,
		DocumentText:   evidence.Content,
	})
	require.NoError(t, err)
	require.Equal(t, evidence.FragmentID, doc.SourceID)
	var alias bool
	require.NoError(t, repo.rls.WithSystemTx(ctx, repo.db, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT EXISTS (
				SELECT 1 FROM evidence_exact_aliases
				WHERE team_id = ?::uuid AND alias_fragment_id = ?::uuid
			)
		`, teamID, evidence.FragmentID).Row().Scan(&alias)
	}))
	if alias {
		require.Equal(t, "not_required", doc.SearchState)
	} else {
		require.Equal(t, "pending", doc.SearchState)
	}
}
