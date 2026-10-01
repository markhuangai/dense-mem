//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestLifecycleOwnershipSupportRecomputePreservesTerminalAndAliasState(t *testing.T) {
	f := newLifecycleOwnershipFixture(t)
	ctx := context.Background()
	for _, status := range []string{"needs_review", "rejected", "retracted", "superseded", "quarantined", "disputed"} {
		t.Run(status, func(t *testing.T) {
			f.reset(t)
			require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
				return tx.Exec("UPDATE relationship_records SET status = ? WHERE team_id = ?::uuid AND relationship_id = ?::uuid",
					status, f.teamID, f.decision.Relationship.RelationshipID).Error
			}))
			before := f.state(t)
			input := f.supportInput()
			revoked, err := f.store.ApplyRelationshipSupportDecision(ctx, input)
			require.NoError(t, err)
			require.Equal(t, status, revoked.FromStatus)
			require.Equal(t, status, revoked.ToStatus)
			require.Zero(t, revoked.SupportCount)
			input.Decision, input.IdempotencyKey = "reinstate", "lifecycle-support-reinstate"
			reinstated, err := f.store.ApplyRelationshipSupportDecision(ctx, input)
			require.NoError(t, err)
			require.Equal(t, status, reinstated.ToStatus)
			require.Equal(t, 1, reinstated.SupportCount)
			after := f.state(t)
			require.Equal(t, status, after.Status)
			require.Equal(t, before.Version+2, after.Version)
			require.Equal(t, before.SupportDecisions+2, after.SupportDecisions)
			require.Equal(t, before.Transitions, after.Transitions)
		})
	}
	f.reset(t)
	canonical, err := f.store.ApplyRelationshipDecision(ctx, ApplyRelationshipDecisionInput{
		TeamID: f.teamID, OwnerProfileID: f.ownerID, IngestID: f.ingest.IngestID,
		SubjectEntityID: f.decision.Relationship.SubjectEntityID, PredicateKey: "works_on", ObjectEntityID: f.correctObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: f.ingest.Evidence[0].FragmentID, SourceGroupKey: "lifecycle-source", SpanEnd: len(lifecycleOwnershipContent), Authority: "primary"},
	})
	require.NoError(t, err)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec("UPDATE relationship_records SET identity_alias_of_relationship_id = ?::uuid WHERE team_id = ?::uuid AND relationship_id = ?::uuid",
			canonical.Relationship.RelationshipID, f.teamID, f.decision.Relationship.RelationshipID).Error
	}))
	before := f.state(t)
	_, err = f.store.ApplyRelationshipSupportDecision(ctx, f.supportInput())
	require.ErrorIs(t, err, ErrSemanticIdentityAlias)
	require.Equal(t, before, f.state(t), "the attempted support decision must roll back for an alias")
	plan, err := f.store.PlanRelationshipCorrectionEmbeddings(ctx, f.correctionInput())
	require.NoError(t, err)
	require.Empty(t, plan.Documents)
	rejected, err := f.store.CorrectRelationship(ctx, f.correctionInput())
	require.NoError(t, err)
	require.Equal(t, "relationship_not_active", rejected.ErrorCode)
	require.Equal(t, before, f.state(t))
}

func TestLifecycleOwnershipCorrectionPreviewEligibility(t *testing.T) {
	f := newLifecycleOwnershipFixture(t)
	ctx := context.Background()
	for _, test := range []struct {
		status   string
		count    int
		eligible bool
	}{
		{"active", 1, true}, {"active", 0, false}, {"pending_evidence", 1, false}, {"retracted", 1, false},
	} {
		t.Run(test.status+string(rune('0'+test.count)), func(t *testing.T) {
			f.reset(t)
			require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
				return tx.Exec("UPDATE relationship_records SET status = ?, support_count = ? WHERE team_id = ?::uuid AND relationship_id = ?::uuid",
					test.status, test.count, f.teamID, f.decision.Relationship.RelationshipID).Error
			}))
			before := f.state(t)
			input := f.correctionInput()
			plan, err := f.store.PlanRelationshipCorrectionEmbeddings(ctx, input)
			require.NoError(t, err)
			require.Equal(t, before, f.state(t), "preview must be read-only")
			result, err := f.store.CorrectRelationshipWithEmbeddings(ctx, input, relationshipCorrectionTestEmbeddings(plan))
			require.NoError(t, err)
			if !test.eligible {
				require.Empty(t, plan.Documents)
				require.Equal(t, "rejected", result.ProcessingState)
				require.Equal(t, "relationship_not_active", result.ErrorCode)
				require.Equal(t, before, f.state(t))
				return
			}
			require.Len(t, plan.Documents, 2)
			require.Equal(t, "completed", result.ProcessingState)
			require.Equal(t, "current", result.SearchState)
			after := f.state(t)
			require.Equal(t, "superseded", after.Status)
			require.Equal(t, before.Version+1, after.Version)
			require.Equal(t, before.Transitions+1, after.Transitions)
		})
	}
}

func TestLifecycleOwnershipConcurrentRetractionRetiresDocumentsOnce(t *testing.T) {
	f := newLifecycleOwnershipFixture(t)
	ctx := context.Background()
	search := newSearchFixtureStore(f.appDB, f.rls)
	documents := make(map[string][]float32)
	for _, source := range []struct {
		kind, id string
		version  int
	}{
		{"evidence", f.ingest.Evidence[0].FragmentID, 1},
		{"relationship", f.decision.Relationship.RelationshipID, f.decision.Relationship.Version},
	} {
		document, err := search.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
			TeamID: f.teamID, OwnerProfileID: f.ownerID, SourceKind: source.kind, SourceID: source.id,
			SourceVersion: int64(source.version), ProjectionFormat: 2, DocumentText: "Lifecycle " + source.kind,
			SpaceID: f.decision.Relationship.SpaceID, SpaceGeneration: f.decision.Relationship.SpaceGeneration,
		})
		require.NoError(t, err)
		documents[document.SearchDocumentID] = []float32{1, 0, 0}
	}
	completeSearchDocumentsForTest(t, search, f.teamID, documents)
	type documentState struct {
		Version      int
		State        string
		HasEmbedding bool
	}
	readDocuments := func() []documentState {
		var states []documentState
		require.NoError(t, f.rls.WithTeamProfileTx(ctx, f.appDB, f.teamID, f.ownerID, func(tx *gorm.DB) error {
			return tx.Raw("SELECT document_version AS version, search_state AS state, embedding IS NOT NULL AS has_embedding FROM search_documents WHERE team_id = ?::uuid ORDER BY source_kind", f.teamID).Scan(&states).Error
		}))
		return states
	}
	beforeDocuments, before := readDocuments(), f.state(t)
	require.Len(t, beforeDocuments, 2)
	for _, document := range beforeDocuments {
		require.Equal(t, "current", document.State)
		require.True(t, document.HasEmbedding)
	}
	type outcome struct {
		result *EvidenceLifecycleResult
		err    error
	}
	results, start := make(chan outcome, 2), make(chan struct{})
	input := f.retractInput()
	for range 2 {
		go func() {
			<-start
			result, err := f.store.RetractEvidence(ctx, input)
			results <- outcome{result, err}
		}()
	}
	close(start)
	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.NotEqual(t, first.result.Existing, second.result.Existing)
	require.Equal(t, first.result.DecisionID, second.result.DecisionID)
	require.Equal(t, 1, first.result.PendingRelationshipCount)
	first.result.Existing, second.result.Existing = false, false
	require.Equal(t, first.result, second.result)
	after := f.state(t)
	require.Equal(t, "pending_evidence", after.Status)
	require.Zero(t, after.SupportCount)
	require.Equal(t, before.Version+1, after.Version)
	require.Equal(t, before.SupportDecisions+1, after.SupportDecisions)
	require.Equal(t, before.Transitions+1, after.Transitions)
	require.Equal(t, before.LifecycleEvents+1, after.LifecycleEvents)
	afterDocuments := readDocuments()
	require.Len(t, afterDocuments, len(beforeDocuments))
	for index, document := range afterDocuments {
		require.Equal(t, beforeDocuments[index].Version+1, document.Version)
		require.Equal(t, "not_required", document.State)
		require.False(t, document.HasEmbedding)
	}
}
