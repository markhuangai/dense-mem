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
	upsertRecallEvidenceSearchDocumentForTest(t, ctx, searchRepo, teamID, ownerID, canonical.Evidence[0])

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
	require.Len(t, trace.EvidenceSupports, 1)
	require.Equal(t, canonical.Evidence[0].FragmentID, trace.EvidenceSupports[0].FragmentID)
	require.Equal(t, alias.Evidence[0].FragmentID, trace.EvidenceSupports[0].OccurrenceID)
	require.Len(t, trace.EvidenceFragments, 1)
	require.Equal(t, canonical.Evidence[0].FragmentID, trace.EvidenceFragments[0].FragmentID)
	require.Equal(t, alias.Evidence[0].FragmentID, trace.EvidenceFragments[0].OccurrenceID)

	_, err = ledgerRepo.RetractEvidence(ctx, RetractEvidenceInput{
		TeamID: teamID, OwnerProfileID: ownerID, EvidenceIDs: []string{alias.Evidence[0].FragmentID},
		Reason: "retract historical alias support", IdempotencyKey: "recall-alias-support-retract",
		RequestHash: sha256Hex("recall-alias-support-retract"),
	})
	require.NoError(t, err)
	trace, err = semanticRepo.TraceRelationship(ctx, TraceRelationshipInput{
		TeamID: teamID, RelationshipID: decision.Relationship.RelationshipID,
	})
	require.NoError(t, err)
	require.Len(t, trace.EvidenceLifecycleEvents, 1)
	require.Equal(t, alias.Evidence[0].FragmentID, trace.EvidenceLifecycleEvents[0].TargetFragmentID)
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
