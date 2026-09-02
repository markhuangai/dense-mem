package remember

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestRememberServiceHelpersCoverTypedInputAndSpaceBranches(t *testing.T) {
	var nilService *service
	_, err := nilService.Remember(context.Background(), validRememberServiceRequest())
	require.ErrorContains(t, err, "service is required")

	status := &SubmissionStatusResult{SubmissionID: "submission"}
	processErr := &RememberProcessError{Status: status, Err: errors.New("cause")}
	mapped := preserveProcessStatus(processErr, ErrRememberRequestTimeout)
	var mappedProcessErr *RememberProcessError
	require.ErrorAs(t, mapped, &mappedProcessErr)
	require.Same(t, status, mappedProcessErr.Status)
	require.ErrorIs(t, mapped, ErrRememberRequestTimeout)
	require.Equal(t, ErrRememberRequestCancelled, preserveProcessStatus(errors.New("cause"), ErrRememberRequestCancelled))

	require.Empty(t, rememberSpaceID(domain.MemorySpaceAccess{}))
	spaceID := uuid.New()
	require.Equal(t, spaceID.String(), rememberSpaceID(domain.MemorySpaceAccess{ID: spaceID}))
	privateID := uuid.New()
	space := rememberSpace(requestctx.Actor{AllowedSpaces: []domain.MemorySpaceAccess{
		{Kind: domain.MemorySpaceTeamShared},
		{Kind: domain.MemorySpaceProfilePrivate, ID: privateID, Generation: 2},
	}})
	require.Equal(t, privateID, space.ID)
	shared := rememberSpace(requestctx.Actor{AllowedSpaces: []domain.MemorySpaceAccess{{Kind: domain.MemorySpaceTeamShared, ID: spaceID}}})
	require.Equal(t, spaceID, shared.ID)

}

func TestRememberServiceHelpersCoverRevisionMetadataAndSummaries(t *testing.T) {
	base := RememberEvidenceInput{Content: "first", SourceKey: "source", SourceRevision: "rev-1", PreviousSourceRevision: "rev-0", SourceGroup: " group ", SupersedesEvidenceIDs: []string{"old"}}
	second := base
	second.Content = "second"
	hashes := sourceRevisionContentHashes([]RememberEvidenceInput{base, second, {Content: "without revision"}})
	require.Len(t, hashes, 1)
	require.NotEmpty(t, hashes["source\x00rev-1\x00rev-0"])
	require.Empty(t, sourceRevisionBatchKey(RememberEvidenceInput{SourceKey: "source"}))

	metadata := evidenceProcessingIntentMetadata(map[string]any{}, base)
	require.Equal(t, []string{"old"}, metadata["supersedes_evidence_ids"])
	require.Equal(t, "group", metadata["contract_source_group"])
	primary, copied := ledgerAuthorityAndMetadata("", map[string]any{"existing": true})
	require.Equal(t, string(domain.AuthorityPrimary), primary)
	require.True(t, copied["existing"].(bool))
	secondary, copied := ledgerAuthorityAndMetadata(" secondary ", nil)
	require.Equal(t, "secondary", secondary)
	require.Equal(t, "secondary", copied["contract_authority"])
	require.Equal(t, "conversation", evidenceSourceType(" "))
	require.Equal(t, "document", evidenceSourceType(" document "))
	require.Equal(t, "source", sourceSummary([]RememberEvidenceInput{base}))
	require.Equal(t, "remember evidence_count=1", sourceSummary([]RememberEvidenceInput{{Content: "fact"}}))
	require.Equal(t, map[string]any{"source_type": base.SourceType, "source": base.Source, "source_group": base.SourceGroup, "metadata": base.Metadata}, sourceRevisionEnvelope(base))
}
