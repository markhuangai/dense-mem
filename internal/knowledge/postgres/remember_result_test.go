package postgres

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	remember "github.com/markhuangai/dense-mem/internal/remember/service"
)

func TestRememberPublicResultUsesCanonicalCompletedProjection(t *testing.T) {
	input := SynchronousRememberCommitInput{
		IngestID: uuid.NewString(),
		Metadata: map[string]any{"correlation_id": "terminal-correlation"},
	}
	evidence := []EvidenceFragment{{FragmentID: uuid.NewString(), EvidenceIndex: 0, ContentHash: "sha256:result", SupersededEvidenceIDs: []string{}}}
	result := rememberPublicResult(input, evidence, &submissionSemanticCommitState{
		SearchDocuments: []SearchDocumentResult{{SearchState: "current"}},
	})
	raw, err := json.Marshal(result)
	require.NoError(t, err)
	var terminal remember.TerminalRememberResult
	require.NoError(t, json.Unmarshal(raw, &terminal))
	terminal.Kind = remember.ResultKindTerminal
	require.NoError(t, remember.ValidateTerminalRememberResult(&terminal, 1))
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
	}}, &submissionSemanticCommitState{})

	require.Equal(t, "current", result["search_state"])
	evidence, ok := result["evidence"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, evidence, 1)
	require.Equal(t, "current", evidence[0]["search_state"])
}

func TestRememberPublicResultProjectsAssessorContextWarnings(t *testing.T) {
	input := SynchronousRememberCommitInput{
		IngestID:                          uuid.NewString(),
		CandidateContextOmittedCandidates: 2,
	}
	result := rememberPublicResult(input, nil, &submissionSemanticCommitState{})
	warnings, ok := result["warnings"].([]string)
	require.True(t, ok)
	require.Len(t, warnings, 1)
	require.Contains(t, warnings[0], "duplicate candidates")
}
