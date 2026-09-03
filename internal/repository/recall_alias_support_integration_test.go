package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestRecallExpansionFollowsHistoricalAliasSupportToCanonicalEvidence(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-alias-support-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-alias-support-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-alias-support", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)
	content := "historical alias support remains reachable through canonical evidence"
	canonical, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "recall-alias-support-canonical",
		RequestHash: sha256Hex("recall-alias-support-canonical"), Evidence: []EvidenceInput{{Content: content}},
	})
	require.NoError(t, err)
	alias, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "recall-alias-support-alias",
		RequestHash: sha256Hex("recall-alias-support-alias"), Evidence: []EvidenceInput{{Content: content}},
	})
	require.NoError(t, err)
	aliasTwo, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "recall-alias-support-alias-two",
		RequestHash: sha256Hex("recall-alias-support-alias-two"), Evidence: []EvidenceInput{{Content: content}},
	})
	require.NoError(t, err)
	require.Len(t, canonical.Evidence, 1)
	require.Len(t, alias.Evidence, 1)
	require.Len(t, aliasTwo.Evidence, 1)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO evidence_exact_aliases (
				team_id, alias_fragment_id, alias_owner_profile_id,
				canonical_fragment_id, canonical_owner_profile_id
			) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid)
		`, teamID, alias.Evidence[0].FragmentID, ownerID, canonical.Evidence[0].FragmentID, ownerID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO evidence_exact_aliases (
				team_id, alias_fragment_id, alias_owner_profile_id,
				canonical_fragment_id, canonical_owner_profile_id
			) VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?::uuid)
		`, teamID, aliasTwo.Evidence[0].FragmentID, ownerID, canonical.Evidence[0].FragmentID, ownerID).Error
	}))
	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Alias Support Subject")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Canonical Evidence")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: alias.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: alias.Evidence[0].FragmentID, SourceGroupKey: "recall:historical-alias",
			SpanStart: 0, SpanEnd: len(content), Quote: content, Authority: "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	secondDecision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: aliasTwo.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: aliasTwo.Evidence[0].FragmentID, SourceGroupKey: "recall:historical-alias-two",
			SpanStart: 0, SpanEnd: len(content), Quote: content, Authority: "primary",
		},
	})
	require.Equal(t, decision.Relationship.RelationshipID, secondDecision.Relationship.RelationshipID)
	upsertRecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, canonical.Evidence[0])
	relationshipDoc, err := searchRepo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship",
		SourceID: decision.Relationship.RelationshipID, SourceVersion: int64(decision.Relationship.Version),
		DocumentText: "relationship\nsubject: Alias Support Subject\npredicate: works on\nobject: Canonical Evidence\npolarity: positive",
	})
	require.NoError(t, err)
	require.Equal(t, "pending", relationshipDoc.SearchState)
	recalledRelationships, err := searchRepo.RecallRelationships(ctx, RecallRelationshipsInput{
		TeamID: teamID, ExpandFromEntityIDs: []string{subject.EntityID}, Limit: 5,
	})
	require.NoError(t, err)
	require.Len(t, recalledRelationships.Results, 1)
	require.Equal(t, []string{canonical.Evidence[0].FragmentID}, recalledRelationships.Results[0].EvidenceIDs)

	recall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID, ExpandFromEntityIDs: []string{subject.EntityID}, Limit: 5,
	})
	require.NoError(t, err)
	require.Len(t, recall.Results, 1)
	require.Equal(t, canonical.Evidence[0].FragmentID, recall.Results[0].EvidenceID)
	require.Contains(t, recall.Results[0].RelationshipIDs, decision.Relationship.RelationshipID)

	trace, err := semanticRepo.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID: teamID, RelationshipID: decision.Relationship.RelationshipID,
	})
	require.NoError(t, err)
	require.Len(t, trace.EvidenceSupports, 2)
	require.Len(t, trace.EvidenceFragments, 2)
	require.ElementsMatch(t, []string{alias.Evidence[0].FragmentID, aliasTwo.Evidence[0].FragmentID}, []string{
		trace.EvidenceSupports[0].OccurrenceID, trace.EvidenceSupports[1].OccurrenceID,
	})
	for _, fragment := range trace.EvidenceFragments {
		require.Equal(t, canonical.Evidence[0].FragmentID, fragment.FragmentID)
	}

	_, err = ledgerRepo.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID, EvidenceIDs: []string{canonical.Evidence[0].FragmentID},
		Reason: "retract canonical evidence with historical alias support", IdempotencyKey: "recall-alias-support-retract",
		RequestHash: sha256Hex("recall-alias-support-retract"),
	})
	require.NoError(t, err)
	trace, err = semanticRepo.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID: teamID, RelationshipID: decision.Relationship.RelationshipID,
	})
	require.NoError(t, err)
	require.Len(t, trace.EvidenceLifecycleEvents, 1)
	require.Equal(t, canonical.Evidence[0].FragmentID, trace.EvidenceLifecycleEvents[0].TargetFragmentID)
	require.Equal(t, "pending_evidence", trace.Relationship.Status)
}

func TestRecallQuarantinedHistoricalAliasSupportDoesNotExposeRelationship(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "recall-alias-support-quarantine-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "recall-alias-support-quarantine-owner")
	insertSearchTestContract(t, adminDB, rls, "recall-alias-support-quarantine", 3, "exact", "")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)
	content := "canonical evidence remains after its historical support alias is quarantined"
	canonical, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "recall-alias-support-quarantine-canonical",
		RequestHash: sha256Hex("recall-alias-support-quarantine-canonical"), Evidence: []EvidenceInput{{Content: content}},
	})
	require.NoError(t, err)
	alias, err := ledgerRepo.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "recall-alias-support-quarantine-alias",
		RequestHash: sha256Hex("recall-alias-support-quarantine-alias"), Evidence: []EvidenceInput{{Content: content}},
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
	subject := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "person", "Quarantined Alias Subject")
	object := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Quarantined Alias Evidence")
	decision := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: alias.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: alias.Evidence[0].FragmentID, SourceGroupKey: "recall:historical-alias-quarantine",
			SpanStart: 0, SpanEnd: len(content), Quote: content, Authority: "primary",
		},
	})
	require.NotNil(t, decision.Relationship)
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return insertEvidenceQuarantine(ctx, tx, CreateIngestInput{TeamID: teamID, OwnerProfileID: ownerID}, alias.IngestID, alias.Evidence[0].FragmentID, "quarantined historical support alias")
	}))
	upsertRecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, canonical.Evidence[0])

	recall, err := searchRepo.RecallEvidence(ctx, RecallEvidenceInput{
		TeamID: teamID, Query: content, ExpandFromEntityIDs: []string{subject.EntityID}, Limit: 5,
	})
	require.NoError(t, err)
	require.Len(t, recall.Results, 1)
	require.Equal(t, canonical.Evidence[0].FragmentID, recall.Results[0].EvidenceID)
	require.NotContains(t, recall.Results[0].RelationshipIDs, decision.Relationship.RelationshipID)
}
