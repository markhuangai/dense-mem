//go:build integration

package postgres

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	traceapp "github.com/markhuangai/dense-mem/internal/trace"
)

func TestTraceOwnershipBoundsAndCompleteness(t *testing.T) {
	f := newTraceOwnershipFixture(t)
	no := false
	for _, test := range []struct {
		name                 string
		input                TraceRelationshipInput
		edges, events, runes int
		transitions          int
		reason               string
	}{
		{"default", TraceRelationshipInput{}, 24, 100, 2000, 1, "max_edges"},
		{"include", TraceRelationshipInput{IncludeEvidenceContent: new(true), IncludeVerification: new(true), IncludeTransitions: new(true)}, 24, 100, 2000, 1, "max_edges"},
		{"minimum", TraceRelationshipInput{MaxDepth: 1, MaxEdges: 1, MaxEvents: 1, MaxFragmentContentRunes: 1}, 1, 1, 1, 1, "max_edges"},
		{"below maximum", TraceRelationshipInput{MaxDepth: 3, MaxEdges: 99, MaxEvents: 499, MaxFragmentContentRunes: 7999}, 99, 499, 7999, 1, "max_edges"},
		{"maximum", TraceRelationshipInput{MaxDepth: 4, MaxEdges: 100, MaxEvents: 500, MaxFragmentContentRunes: 8000}, 100, 500, 8000, 1, "max_edges"},
		{"over maximum", TraceRelationshipInput{MaxDepth: 99, MaxEdges: 999, MaxEvents: 999, MaxFragmentContentRunes: 99999}, 100, 500, 8000, 1, "max_edges"},
		{"negative", TraceRelationshipInput{MaxDepth: -1, MaxEdges: -1, MaxEvents: -1, MaxFragmentContentRunes: -1}, 24, 100, 2000, 1, "max_edges"},
		{"event bound", TraceRelationshipInput{Topic: "no matching graph", MaxEvents: 1}, 0, 1, 2000, 1, "max_events"},
		{"exclude", TraceRelationshipInput{IncludeEvidenceContent: &no, IncludeVerification: &no, IncludeTransitions: &no}, 24, 0, 0, 0, "max_edges"},
		{"complete", TraceRelationshipInput{Topic: "no matching graph", IncludeVerification: &no, IncludeTransitions: &no}, 0, 0, 2000, 0, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := test.input
			input.TeamID, input.RelationshipID = " "+f.teamID+" ", " "+traceOwnershipID(2, 1)+" "
			result, err := f.store.TraceRelationship(f.ctx, input)
			require.NoError(t, err)
			require.Len(t, result.SemanticEdges, test.edges)
			require.Len(t, result.VerificationEvents, test.events)
			require.Len(t, result.Transitions, test.transitions)
			require.Len(t, result.EvidenceFragments, 1)
			require.Len(t, []rune(result.EvidenceFragments[0].Content), test.runes)
			require.Equal(t, string([]rune(f.content)[:test.runes]), result.EvidenceFragments[0].Content)
			require.Equal(t, test.runes > 0, result.EvidenceFragments[0].ContentTruncated)
			require.Equal(t, test.reason, result.StoppedReason)
			require.Equal(t, test.reason != "", result.Truncated)
			signature, err := f.signature(result)
			require.NoError(t, err)
			t.Logf("result_signature=%s", signature)
		})
	}

	_, err := f.store.TraceRelationship(f.ctx, TraceRelationshipInput{TeamID: f.teamID, RelationshipID: traceOwnershipID(2, 999)})
	require.ErrorIs(t, err, ErrTraceRelationshipNotFound)
}

func TestTraceOwnershipServiceAndAdapterEquivalent(t *testing.T) {
	f := newTraceOwnershipFixture(t)
	service := traceapp.NewSemantic(f.store)
	for _, req := range []traceapp.TraceRequest{
		{RelationshipID: " " + traceOwnershipID(2, 1) + " "},
		{RelationshipID: traceOwnershipID(2, 1), MaxDepth: 99, MaxEdges: 999, Topic: " uses ", MinRelevance: new(-1.0)},
		{RelationshipID: traceOwnershipID(2, 1), MaxDepth: -1, MaxEdges: -1,
			IncludeEvidenceContent: new(false), IncludeVerification: new(false), IncludeTransitions: new(false)},
	} {
		direct, err := f.store.TraceRelationship(f.ctx, TraceRelationshipInput{
			TeamID: f.teamID, RelationshipID: req.RelationshipID, MaxDepth: req.MaxDepth, MaxEdges: req.MaxEdges,
			Topic: req.Topic, MinRelevance: req.MinRelevance,
			IncludeEvidenceContent: req.IncludeEvidenceContent, IncludeVerification: req.IncludeVerification,
			IncludeTransitions: req.IncludeTransitions,
		})
		require.NoError(t, err)
		viaService, err := service.Trace(f.ctx, "", req)
		require.NoError(t, err)
		actual := viaService.Semantic
		require.Equal(t, direct.Relationship, actual.Relationship)
		require.Equal(t, direct.Observations, actual.Observations)
		require.Equal(t, direct.EvidenceSupports, actual.EvidenceSupports)
		require.Equal(t, direct.SupportDecisionEvents, actual.EvidenceSupportDecisionEvents)
		require.Equal(t, direct.EvidenceFragments, actual.Evidence)
		require.Equal(t, direct.EvidenceLifecycleEvents, actual.EvidenceLifecycleEvents)
		require.Equal(t, direct.VerificationEvents, actual.VerificationEvents)
		require.Equal(t, direct.Transitions, actual.Transitions)
		require.Equal(t, direct.Conflicts, actual.Conflicts)
		require.Equal(t, direct.CrossProfileReferences, actual.CrossProfileReferences)
		require.Equal(t, direct.IdentityCorrections, actual.IdentityCorrections)
		require.Equal(t, direct.SupersessionLineage, actual.SupersessionLineage)
		require.Equal(t, direct.SearchDocuments, actual.SearchDocuments)
		require.Equal(t, direct.SemanticNodes, actual.SemanticNodes)
		require.Equal(t, direct.SemanticEdges, actual.SemanticEdges)
		require.Equal(t, direct.VisitedEntityIDs, actual.VisitedEntityIDs)
		require.Equal(t, direct.StoppedReason, actual.StoppedReason)
		require.Equal(t, direct.Truncated, actual.Truncated)
		signature, err := f.signature(viaService)
		require.NoError(t, err)
		t.Logf("result_signature=%s", signature)
	}
	_, err := service.Trace(f.ctx, "", traceapp.TraceRequest{RelationshipID: traceOwnershipID(2, 999)})
	require.ErrorIs(t, err, traceapp.ErrTraceRelationshipNotFound)
}

func TestTraceOwnershipLegacyFragmentRuneBounds(t *testing.T) {
	f := newTraceOwnershipFixture(t)
	for _, limit := range []int{1, 2000, 8000} {
		exactContent := string([]rune(f.content)[:limit])
		exactID := traceOwnershipID(4, limit)
		require.NoError(t, f.rls.WithSystemTx(f.ctx, f.db, func(tx *gorm.DB) error {
			return tx.Exec(`INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
				content, content_hash, space_id)
				VALUES (?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?, ?, ?::uuid)`,
				f.teamID, exactID, traceOwnershipID(3, 0), f.ownerID, limit,
				exactContent, "trace-exact-content-"+exactID, f.spaceID).Error
		}))
		require.NoError(t, f.store.withTeamTx(f.ctx, f.teamID, func(tx *gorm.DB) error {
			result, err := loadTraceEvidenceFragments(f.ctx, tx, traceExecutionInput{
				Input: TraceRelationshipInput{TeamID: f.teamID, MaxFragmentContentRunes: limit}, spaceID: f.spaceID,
			}, []string{traceOwnershipID(4, 0), exactID})
			require.NoError(t, err)
			require.Len(t, result, 2)
			require.Equal(t, exactContent, result[0].Content)
			require.True(t, result[0].ContentTruncated)
			require.Equal(t, exactContent, result[1].Content)
			require.False(t, result[1].ContentTruncated)
			return nil
		}))
	}
}
