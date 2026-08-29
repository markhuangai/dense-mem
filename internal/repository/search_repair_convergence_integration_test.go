package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSearchRepairConvergenceUsesDocumentUpdateForExistingDrift(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "search-repair-drift-age-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "search-repair-drift-age-owner")
	insertSearchTestContract(t, adminDB, rls, "search-repair-drift-age", 2, "exact", "")
	repo := NewSearchRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Drift Age Subject")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Drift Age Object")
	content := "Drift Age Subject works on Drift Age Object."
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "search-repair-drift-age-evidence", content)
	decision := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "search-repair-drift-age",
			SpanStart: 0, SpanEnd: len(content), Authority: "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	_, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: decision.Relationship.RelationshipID,
		SourceVersion: int64(decision.Relationship.Version), ProjectionFormat: 2,
		DocumentText: "relationship\nsubject: Drift Age Subject\npredicate: works on\nobject: Drift Age Object\npolarity: positive",
	})
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE relationship_records
			SET created_at = clock_timestamp() - interval '1 day'
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, decision.Relationship.RelationshipID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE search_documents
			SET search_state = 'failed', updated_at = clock_timestamp() - interval '10 seconds'
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
		`, teamID, decision.Relationship.RelationshipID).Error
	}))

	projection, err := repo.GetSearchConvergence(ctx, SearchConvergenceInput{})
	require.NoError(t, err)
	require.EqualValues(t, 1, projection.DriftedDocuments)
	require.GreaterOrEqual(t, projection.OldestDriftAge, 8*time.Second)
	require.Less(t, projection.OldestDriftAge, time.Minute)
}
