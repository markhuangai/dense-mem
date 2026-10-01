package contract

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
)

func TestCompletenessPreservesInclusiveEventBounds(t *testing.T) {
	input := NormalizeInput(Input{MaxEvents: 3})
	for _, collection := range []struct {
		name string
		fill func(*RelationshipTraceResult, int)
	}{
		{"observations", func(r *RelationshipTraceResult, n int) { r.Observations = make([]RelationshipObservationRecord, n) }},
		{"supports", func(r *RelationshipTraceResult, n int) {
			r.EvidenceSupports = make([]RelationshipEvidenceSupportRecord, n)
		}},
		{"decisions", func(r *RelationshipTraceResult, n int) {
			r.SupportDecisionEvents = make([]RelationshipSupportDecisionEvent, n)
		}},
		{"verification", func(r *RelationshipTraceResult, n int) {
			r.VerificationEvents = make([]RelationshipVerificationEvent, n)
		}},
		{"transitions", func(r *RelationshipTraceResult, n int) { r.Transitions = make([]RelationshipTransitionEvent, n) }},
		{"lifecycle", func(r *RelationshipTraceResult, n int) {
			r.EvidenceLifecycleEvents = make([]TraceEvidenceLifecycleEvent, n)
		}},
	} {
		for _, count := range []int{0, 2, 3, 4} {
			t.Run(fmt.Sprintf("%s/%d", collection.name, count), func(t *testing.T) {
				result := &RelationshipTraceResult{}
				collection.fill(result, count)
				truncated, reason := Completeness(input, result)
				require.Equal(t, count >= 3, truncated)
				if count >= 3 {
					require.Equal(t, "max_events", reason)
				} else {
					require.Empty(t, reason)
				}
				require.False(t, result.Truncated)
				require.Empty(t, result.StoppedReason)
			})
		}
	}
}

func TestCompletenessPreservesEdgeBoundsAndPrecedence(t *testing.T) {
	input := NormalizeInput(Input{MaxEdges: 2, MaxEvents: 3})
	for _, test := range []struct {
		edges, events int
		want          string
	}{
		{0, 0, ""}, {1, 2, ""}, {2, 0, "max_edges"}, {3, 0, "max_edges"},
		{1, 3, "max_events"}, {2, 3, "max_edges"}, {3, 4, "max_edges"},
	} {
		t.Run(fmt.Sprintf("edges-%d/events-%d", test.edges, test.events), func(t *testing.T) {
			result := &RelationshipTraceResult{
				SemanticEdges: make([]graphcontract.Edge, test.edges),
				Observations:  make([]RelationshipObservationRecord, test.events),
			}
			truncated, reason := Completeness(input, result)
			require.Equal(t, test.want, reason)
			require.Equal(t, test.want != "", truncated)
		})
	}
}

func TestCompletenessDoesNotCountOtherCollections(t *testing.T) {
	result := &RelationshipTraceResult{
		EvidenceFragments:      make([]TraceEvidenceFragment, 2),
		Conflicts:              make([]RelationshipConflictCaseRecord, 2),
		CrossProfileReferences: make([]RelationshipCrossReferenceRecord, 2),
		IdentityCorrections:    make([]EntityCorrectionEventRecord, 2),
		SupersessionLineage:    make([]RelationshipTraceRecord, 2),
		SearchDocuments:        make([]TraceSearchDocument, 2),
		SemanticNodes:          make([]graphcontract.Node, 2),
		VisitedEntityIDs:       []string{"one", "two"},
	}
	truncated, reason := Completeness(NormalizeInput(Input{MaxEdges: 1, MaxEvents: 1}), result)
	require.False(t, truncated)
	require.Empty(t, reason)
}
