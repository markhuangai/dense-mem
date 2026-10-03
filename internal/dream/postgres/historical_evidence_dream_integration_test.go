//go:build integration

package postgres

import (
	"context"
	"github.com/google/uuid"
	"github.com/lib/pq"
	dreamcontract "github.com/markhuangai/dense-mem/internal/dream/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"testing"
)

func TestHistoricalEvidenceDreamPreservesReadsAndOwnerConfirmation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "evidence-dream-derivation-readback", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "evidence-dream-derivation-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-derivation-owner")
	semantic := newDreamFixtureStore(appDB, rls)
	ledger := knowledgepostgres.NewStore(appDB, rls, knowledgepostgres.ConflictRuntimeConfig{})
	search := newDreamSearchFixtureStore(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "PostgreSQL")
	const content = "Dense-Mem uses PostgreSQL for durable memory."
	ingest, err := ledger.CreateIngestForTest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "evidence-dream-derivation-ingest",
		RequestHash: sha256Hex(content), Evidence: []EvidenceInput{{
			Content: content, InitialEvent: &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"},
		}},
	})
	require.NoError(t, err)
	target := requireTestEvidenceFragment(t, ingest)
	document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence", SourceID: target.FragmentID,
		SourceVersion: 1, DocumentText: content,
	})
	require.NoError(t, err)
	completeSearchDocumentsForTest(t, search, teamID, map[string][]float32{document.SearchDocumentID: {1, 0}})
	sourceGroupKey := "ingest:" + ingest.IngestID
	hypothesisID := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO hypotheses (
				team_id, hypothesis_id, space_id, space_generation, created_by_profile_id,
				lane, status, statement, subject_entity_id, predicate_key, predicate_version,
				object_entity_id, source_refs, source_versions, source_owner_profile_ids,
				content_hash, target_identity, generator_kind, generator_version
			) VALUES (?, ?, dense_mem_team_shared_space(?), dense_mem_team_shared_generation(?), ?,
				'evidence_discovery', 'proposed', 'Dense-Mem may use PostgreSQL for durable memory.',
				?, 'uses', 1, ?, '[]'::jsonb, '{}'::jsonb, ?::uuid[], ?, ?, 'provider', 'legacy-v2.6.3')
		`, teamID, hypothesisID, teamID, teamID, ownerID, subject.EntityID, object.EntityID,
			pq.Array([]string{ownerID}), sha256Hex("historical-hypothesis"),
			dreamcontract.HypothesisTargetIdentity(teamID, subject.EntityID, "uses", object.EntityID, "")).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO hypothesis_evidence_derivation_sources (
				team_id, hypothesis_id, space_id, space_generation, evidence_id, fragment_id,
				source_group_key, span_start, span_end, quote, authority
			) VALUES (?, ?, dense_mem_team_shared_space(?), dense_mem_team_shared_generation(?), ?, ?, ?, 0, ?, ?, ?)
		`, teamID, hypothesisID, teamID, teamID, target.FragmentID, target.FragmentID,
			sourceGroupKey, len([]rune(content)), content, target.Authority).Error
	}))
	records, _, err := semantic.ListHypotheses(ctx, ListHypothesesInput{TeamID: teamID, Status: "proposed", Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "evidence_discovery", string(records[0].Lane))
	require.Equal(t, []string{target.FragmentID}, records[0].SourceEvidenceIDs)
	require.Len(t, records[0].EvidenceDerivations, 1)
	require.Equal(t, content, records[0].EvidenceDerivations[0].Quote)
	require.Equal(t, sourceGroupKey, records[0].EvidenceDerivations[0].SourceGroupKey)
	require.Equal(t, []string{ownerID}, records[0].SourceOwnerProfileIDs)
	loaded, err := semantic.GetHypothesis(ctx, GetHypothesisInput{TeamID: teamID, HypothesisID: records[0].HypothesisID})
	require.NoError(t, err)
	require.Len(t, loaded.EvidenceDerivations, 1)
	require.Equal(t, target.FragmentID, loaded.EvidenceDerivations[0].EvidenceID)
	otherTeamID := createLedgerTeam(t, adminDB, rls, "historical-evidence-other-team")
	_, err = semantic.GetHypothesis(ctx, GetHypothesisInput{TeamID: otherTeamID, HypothesisID: hypothesisID})
	require.ErrorIs(t, err, ErrDreamHypothesisNotFound)
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "evidence-dream-other-owner")
	_, err = semantic.UpdateHypothesisStatus(ctx, UpdateHypothesisStatusInput{
		TeamID: teamID, ActorProfileID: otherOwnerID, HypothesisID: records[0].HypothesisID,
		Status: "reinforced", Decision: "reinforce",
	})
	require.ErrorIs(t, err, ErrDreamHypothesisNotFound)
	updated, err := semantic.UpdateHypothesisStatus(ctx, UpdateHypothesisStatusInput{
		TeamID: teamID, ActorProfileID: ownerID, HypothesisID: records[0].HypothesisID,
		Status: "reinforced", Decision: "reinforce",
	})
	require.NoError(t, err)
	require.Equal(t, "reinforced", updated.Status)
	submission := createSemanticIngest(t, ctx, ledger, teamID, ownerID,
		"evidence-dream-owner-submit", "Independent owner evidence for the hypothesis.")
	_, err = semantic.SubmitHypothesis(ctx, SubmitHypothesisInput{
		TeamID: teamID, ActorProfileID: otherOwnerID, HypothesisID: records[0].HypothesisID,
		Decision: "confirm_true", SubmittedIngestID: submission.IngestID,
	})
	require.ErrorIs(t, err, ErrDreamHypothesisNotFound)
	ownerSubmitted, err := semantic.SubmitHypothesis(ctx, SubmitHypothesisInput{
		TeamID: teamID, ActorProfileID: ownerID, HypothesisID: records[0].HypothesisID,
		Decision: "confirm_true", SubmittedIngestID: submission.IngestID,
	})
	require.NoError(t, err)
	require.Equal(t, submission.IngestID, ownerSubmitted.SubmittedIngestID)
	_, err = semantic.SubmitHypothesis(ctx, SubmitHypothesisInput{
		TeamID: teamID, ActorProfileID: otherOwnerID, HypothesisID: records[0].HypothesisID,
		Decision: "confirm_true", SubmittedIngestID: submission.IngestID,
	})
	require.ErrorIs(t, err, ErrDreamHypothesisNotFound, "an evidence-lane idempotency replay remains owner-bound")
}
