package registry

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	recall "github.com/markhuangai/dense-mem/internal/recall"
	"github.com/stretchr/testify/require"
)

func TestRetiredDreamToolsAndHypothesisInputsAreAbsent(t *testing.T) {
	reg, err := BuildActive(Dependencies{})
	require.NoError(t, err)
	for _, name := range []string{"list_dreams", "get_dream", "resolve_dream_feedback", "eval_run_dream_cycle"} {
		_, present := reg.Get(name)
		require.False(t, present, name)
	}
	tool, present := reg.Get(ToolRecallMemory)
	require.True(t, present)
	require.NotContains(t, schemaProperties(tool.OutputSchema), "related_hypotheses")
	require.Error(t, ValidateContractInput(tool, map[string]any{"query": "memory", "include_hypotheses": true}, []string{"read"}))
	feedback, present := reg.Get(ToolSubmitRecallSessionFeedback)
	require.True(t, present)
	require.Error(t, ValidateContractInput(feedback, map[string]any{"recalls": []any{map[string]any{
		"recall_event_id": "rec-1", "quality": "medium", "used": false, "answer_supported": false,
		"hypothesis_feedback": []any{},
	}}}, []string{"write"}))
}

func TestRecallDoesNotPromptForOptionalFeedback(t *testing.T) {
	output := recallContractOutput(&recall.RecallResult{RecallID: "rec-1"})
	require.NotContains(t, output, "related_hypotheses")
	require.Empty(t, output["suggested_actions"])
	require.Equal(t, "dense-mem.v2.7.0", domain.ContractVersion)
}
