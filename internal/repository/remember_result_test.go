package repository

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestRememberPublicResultUsesCanonicalCompletedProjection(t *testing.T) {
	input := SynchronousRememberCommitInput{
		IngestID: uuid.NewString(),
		Metadata: map[string]any{"correlation_id": "terminal-correlation"},
		Commit: CommitSubmissionAssessmentInput{
			RelationshipResults: []SubmissionRelationshipResultInput{{RelationshipRef: "relationship-1", Disposition: "not_stored", Reason: "not_supported_by_evidence"}},
		},
	}
	evidence := []EvidenceFragment{{FragmentID: uuid.NewString(), EvidenceIndex: 0, ContentHash: "sha256:result", SupersededEvidenceIDs: []string{}}}
	result := rememberPublicResult(input, evidence, &submissionSemanticCommitState{
		SearchDocuments: []SearchDocumentResult{{SearchState: "current"}},
	}, nil)
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var terminal remember.TerminalRememberResult
	require.NoError(t, json.Unmarshal(raw, &terminal))
	terminal.Kind = remember.ResultKindTerminal
	require.NoError(t, remember.ValidateTerminalRememberResult(&terminal, 1, []string{"relationship-1"}))
	require.Equal(t, "completed", terminal.ProcessingState)
	require.Empty(t, terminal.Errors)
	require.Equal(t, "sha256:result", terminal.Evidence[0].ContentHash)
}

func TestRememberPublicResultReportsCurrentProjectionForReusedEvidence(t *testing.T) {
	input := SynchronousRememberCommitInput{IngestID: uuid.NewString()}
	canonicalID := uuid.NewString()
	result := rememberPublicResult(input, []EvidenceFragment{{
		FragmentID: canonicalID, SubmittedFragmentID: uuid.NewString(), EvidenceIndex: 0,
		ContentHash: "sha256:reused",
	}}, &submissionSemanticCommitState{}, nil)

	require.Equal(t, "current", result["search_state"])
	evidence, ok := result["evidence"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, evidence, 1)
	require.Equal(t, "current", evidence[0]["search_state"])
}
