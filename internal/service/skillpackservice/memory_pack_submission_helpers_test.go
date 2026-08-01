package skillpackservice

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestMemoryPackSubmissionReconciliationHelpers(t *testing.T) {
	for _, testCase := range []struct {
		state  string
		status string
		final  bool
	}{
		{state: string(domain.SubmissionQueued), status: domain.SkillPackImportStatusSubmitted},
		{state: string(domain.SubmissionProcessing), status: domain.SkillPackImportStatusSubmitted},
		{state: string(domain.SubmissionCompleted), status: domain.SkillPackImportStatusApplied, final: true},
		{state: string(domain.SubmissionRejected), status: domain.SkillPackImportStatusFailed, final: true},
		{state: string(domain.SubmissionQuarantined), status: domain.SkillPackImportStatusFailed, final: true},
		{state: string(domain.SubmissionFailed), status: domain.SkillPackImportStatusFailed, final: true},
	} {
		status, final, err := memoryPackImportStatusForSubmission(testCase.state)
		require.NoError(t, err)
		require.Equal(t, testCase.status, status)
		require.Equal(t, testCase.final, final)
	}
	_, _, err := memoryPackImportStatusForSubmission("unknown")
	require.ErrorContains(t, err, "unsupported submission processing state")

	applied, skipped := memoryPackSummaryCounts(map[string]any{}, 4, 1)
	require.Equal(t, 3, applied)
	require.Equal(t, 1, skipped)
	items := []ImportItemResult{{ItemID: "applied", Status: "submitted"}, {ItemID: "skipped", Status: "skipped"}}
	require.Nil(t, memoryPackSummaryItems(map[string]any{"items": make(chan int)}))
	applied, skipped = memoryPackSummaryCounts(map[string]any{"items": items}, 2, 0)
	require.Equal(t, 1, applied)
	require.Equal(t, 1, skipped)

	summary := map[string]any{"items": items}
	memoryPackSummarySetItemStatus(summary, "submitted", "applied")
	updated := memoryPackSummaryItems(summary)
	require.Equal(t, "applied", updated[0].Status)
	require.Equal(t, "skipped", updated[1].Status)

	cloned := cloneMemoryPackSummary(summary)
	cloned["submission_id"] = "submission-canonical"
	require.NotContains(t, summary, "submission_id")
}
