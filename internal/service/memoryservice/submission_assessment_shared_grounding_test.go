package memoryservice

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/assessor"
)

func TestSubmissionAssessmentWorkerSharesOneEntityResolutionForRepeatedGrounding(t *testing.T) {
	_, assessments, _, provider, worker := submissionAssessmentWorkerFixture(t)
	provider.response = func(req assessor.SemanticAssessmentRequest) (assessor.SemanticAssessmentResponse, error) {
		response := submissionAssessmentValidResponse(req, false)
		for index, entity := range req.SubmittedEntities {
			if entity.Name != "Alpha" {
				continue
			}
			groundingRef := entity.Groundings[0].GroundingRef
			response.EntityResults[index].GroundingRef = &groundingRef
		}
		return response, nil
	}

	processed, err := worker.ProcessNextSubmissionAssessmentPlacement(context.Background())

	require.NoError(t, err)
	require.True(t, processed)
	require.Len(t, assessments.commits, 1)
	assert.Len(t, assessments.commits[0].EntityResolutions, 5)
	assert.Empty(t, assessments.completions)
}
