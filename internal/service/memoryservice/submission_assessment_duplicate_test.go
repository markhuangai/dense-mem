package memoryservice

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestSubmissionAssessmentDuplicateResolutionsKeepServerFences(t *testing.T) {
	exactID, semanticID, forceID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	canonicalID := uuid.NewString()
	plan := submissionAssessmentPlan{
		Items: []submissionAssessmentItem{
			{EvidenceID: "evidence:0", Fragment: repository.EvidenceFragment{FragmentID: exactID, EvidenceIndex: 0}},
			{EvidenceID: "evidence:1", Fragment: repository.EvidenceFragment{FragmentID: semanticID, EvidenceIndex: 1}, DuplicateAssessmentRequired: true},
			{EvidenceID: "evidence:2", Fragment: repository.EvidenceFragment{FragmentID: forceID, EvidenceIndex: 2}},
		},
		exactDuplicateByEvidenceID: map[string]repository.RememberDuplicateResolution{
			"evidence:0": {EvidenceIndex: 0, CandidateFragmentID: canonicalID, CandidateOwnerID: uuid.NewString(), Exact: true, Disposition: "reuse"},
		},
		duplicateCandidatesByEvidenceID: map[string]repository.RememberDuplicateCandidateGroup{
			"evidence:1": {EvidenceIndex: 1, Candidates: []repository.RememberDuplicateCandidate{{FragmentID: semanticID, OwnerProfileID: uuid.NewString(), Content: "equivalent"}}},
		},
	}
	response := assessor.SemanticAssessmentResponse{
		EvidenceEquivalenceResults: []assessor.SemanticAssessmentEvidenceEquivalenceResult{{
			EvidenceID: "evidence:1", Action: "reuse", CandidateEvidenceID: stringPointer(semanticID),
		}},
	}

	resolutions, err := submissionAssessmentDuplicateResolutions(plan, response)
	require.NoError(t, err)
	require.Len(t, resolutions, 3)
	require.Equal(t, "reuse", resolutions[0].Disposition)
	require.True(t, resolutions[0].Exact)
	require.Equal(t, "reuse", resolutions[1].Disposition)
	require.False(t, resolutions[1].Exact)
	require.Equal(t, "new", resolutions[2].Disposition)

	response.EvidenceEquivalenceResults[0].CandidateEvidenceID = stringPointer(uuid.NewString())
	_, err = submissionAssessmentDuplicateResolutions(plan, response)
	require.ErrorContains(t, err, "unknown candidate")
}
