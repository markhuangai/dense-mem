package dreamservice

import (
	"context"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/stretchr/testify/require"
)

func TestDreamServiceResolveFeedbackConfirmFalseThroughRemember(t *testing.T) {
	now := time.Date(2026, 6, 11, 3, 0, 0, 0, time.UTC)
	graph := &cycleRunGraphStub{executeWrites: true, dreamRows: []map[string]any{{"d": dreamTestNode("dream-1", "profile-1", now)}}}
	memory := &dreamMemoryStub{rememberResult: &memoryservice.RememberResult{IngestID: "ingest-false", Status: "processing"}}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	svc := New(Dependencies{
		Graph:   graph,
		Memory:  memory,
		Metrics: metrics,
		Now:     func() time.Time { return now },
	})

	res, err := svc.ResolveFeedback(context.Background(), "profile-1", ResolveFeedbackRequest{
		DreamID:  "dream-1",
		Decision: "confirm_false",
		Feedback: "The user said this is not true.",
		Proposal: dreamFeedbackProposal("The user said this is not true."),
	})

	require.NoError(t, err)
	require.False(t, res.Deleted)
	require.Equal(t, domain.DreamStatusRejected, res.Dream.Status)
	require.Equal(t, "ingest-false", res.Memory.IngestID)
	require.True(t, memory.trustedAuthority)
	require.Contains(t, memory.lastRememberReq.Evidence[0].Labels, "dream_rejected")
	require.Equal(t, "confirm_false", memory.lastRememberReq.Evidence[0].Metadata["dream_decision"])
	require.Equal(t, "The user said this is not true.", memory.lastRememberReq.Evidence[0].Content)
	require.Equal(t, []observability.DreamFeedbackSample{
		{Decision: "confirm_false", Outcome: "ok", FromStatus: "proposed"},
	}, metrics.DreamFeedbackSamples())
}
