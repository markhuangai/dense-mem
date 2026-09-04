package memoryservice

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestSubmissionAssessmentConflictCanonicalizationKeepsForcedItemsDistinct(t *testing.T) {
	contentHash := "sha256:forced-conflict"
	tests := []struct {
		name        string
		forceInsert [2]bool
	}{
		{name: "forced_then_normal", forceInsert: [2]bool{true, false}},
		{name: "normal_then_forced", forceInsert: [2]bool{false, true}},
		{name: "both_forced", forceInsert: [2]bool{true, true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fragmentIDs := [2]string{uuid.NewString(), uuid.NewString()}
			evidenceIDs := [2]string{"evidence:0", "evidence:1"}
			plan := submissionAssessmentPlan{Items: []submissionAssessmentItem{
				{EvidenceID: evidenceIDs[0], Fragment: repository.EvidenceFragment{FragmentID: fragmentIDs[0], Content: "same forced content", ContentHash: contentHash}, DuplicateAssessmentRequired: !test.forceInsert[0], ExactReuseEligible: true},
				{EvidenceID: evidenceIDs[1], Fragment: repository.EvidenceFragment{FragmentID: fragmentIDs[1], Content: "same forced content", ContentHash: contentHash}, DuplicateAssessmentRequired: !test.forceInsert[1], ExactReuseEligible: true},
			}}
			response := assessor.SemanticAssessmentResponse{EvidenceConflictResults: []assessor.SemanticAssessmentEvidenceConflictResult{{
				Positions: []assessor.SemanticAssessmentEvidenceConflictPosition{
					{EvidenceID: evidenceIDs[0], Start: 0, End: 4},
					{EvidenceID: evidenceIDs[1], Start: 0, End: 4},
				},
			}}}

			require.Empty(t, validateSubmissionAssessmentEvidenceConflictCanonicalization(plan, response))
			canonical := submissionAssessmentConflictCanonicalEvidence(plan, response)
			for index, forced := range test.forceInsert {
				if forced {
					require.Equal(t, "submitted:"+fragmentIDs[index], canonical[evidenceIDs[index]])
				} else {
					require.Equal(t, "batch:"+contentHash+"\x00same forced content", canonical[evidenceIDs[index]])
				}
			}
		})
	}
}
