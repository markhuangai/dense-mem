package remember

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestContextForPhaseUsesEntryAnchoredSequentialCaps(t *testing.T) {
	started := time.Now()
	ctx := WithRememberDeadlines(context.Background(), started)

	assessmentCtx, assessmentCancel := ContextForPhase(ctx, RememberPhaseAssessment)
	defer assessmentCancel()
	assessmentDeadline, ok := assessmentCtx.Deadline()
	require.True(t, ok)
	require.Equal(t, started.Add(RememberAssessmentBudget), assessmentDeadline)

	embeddingCtx, embeddingCancel := ContextForPhase(ctx, RememberPhaseEmbedding)
	defer embeddingCancel()
	embeddingDeadline, ok := embeddingCtx.Deadline()
	require.True(t, ok)
	require.False(t, embeddingDeadline.After(started.Add(RememberAssessmentBudget+RememberEmbeddingBudget)))
	require.False(t, embeddingDeadline.After(time.Now().Add(RememberEmbeddingBudget)))

	commitCtx, commitCancel := ContextForPhase(ctx, RememberPhaseCommit)
	defer commitCancel()
	commitDeadline, ok := commitCtx.Deadline()
	require.True(t, ok)
	require.False(t, commitDeadline.After(started.Add(RememberTotalBudget)))
	require.False(t, commitDeadline.After(time.Now().Add(RememberCommitBudget)))
}

func TestContextForPhaseHonorsEarlierCallerDeadline(t *testing.T) {
	started := time.Now()
	base := WithRememberDeadlines(context.Background(), started)
	callerDeadline := started.Add(3 * time.Second)
	callerCtx, callerCancel := context.WithDeadline(base, callerDeadline)
	defer callerCancel()

	phaseCtx, phaseCancel := ContextForPhase(callerCtx, RememberPhaseCommit)
	defer phaseCancel()
	deadline, ok := phaseCtx.Deadline()
	require.True(t, ok)
	require.Equal(t, callerDeadline, deadline)
}

func TestContextForPhaseUsesDefaultsWithoutAnchoredDeadlines(t *testing.T) {
	ctx, cancel := ContextForPhase(context.Background(), RememberPhase("unknown"))
	defer cancel()
	deadline, ok := ctx.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(RememberTotalBudget), deadline, time.Second)

	anchored := WithRememberDeadlines(context.Background(), time.Time{})
	unknown, unknownCancel := ContextForPhase(anchored, RememberPhase("unknown"))
	defer unknownCancel()
	unknownDeadline, ok := unknown.Deadline()
	require.True(t, ok)
	anchoredDeadlines, ok := rememberDeadlinesFromContext(anchored)
	require.True(t, ok)
	require.Equal(t, anchoredDeadlines.TotalDeadline, unknownDeadline)

	assessment, assessmentCancel := ContextForPhase(anchored, RememberPhaseAssessment)
	defer assessmentCancel()
	assessmentDeadline, ok := assessment.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(RememberAssessmentBudget), assessmentDeadline, time.Second)
}
