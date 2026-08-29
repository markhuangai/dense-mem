package remember

import (
	"context"
	"time"
)

// RememberPhase identifies one of the bounded synchronous Remember phases.
type RememberPhase string

const (
	RememberPhaseAssessment RememberPhase = "assessment"
	RememberPhaseEmbedding  RememberPhase = "embedding"
	RememberPhaseCommit     RememberPhase = "commit"
)

const (
	RememberTotalBudget              = 180 * time.Second
	RememberAssessmentBudget         = 160 * time.Second
	RememberEmbeddingBudget          = 10 * time.Second
	RememberCommitBudget             = 10 * time.Second
	RememberFailurePersistenceBudget = 2 * time.Second
)

type rememberDeadlineContextKey struct{}

// RememberDeadlines stores absolute phase caps anchored at application-service entry.
type RememberDeadlines struct {
	StartedAt          time.Time
	TotalDeadline      time.Time
	AssessmentDeadline time.Time
	EmbeddingDeadline  time.Time
	CommitDeadline     time.Time
}

// WithRememberDeadlines anchors the synchronous Remember budgets at service entry.
// It does not replace an earlier caller deadline.
func WithRememberDeadlines(ctx context.Context, startedAt time.Time) context.Context {
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	return context.WithValue(ctx, rememberDeadlineContextKey{}, RememberDeadlines{
		StartedAt:          startedAt,
		TotalDeadline:      startedAt.Add(RememberTotalBudget),
		AssessmentDeadline: startedAt.Add(RememberAssessmentBudget),
		EmbeddingDeadline:  startedAt.Add(RememberAssessmentBudget + RememberEmbeddingBudget),
		CommitDeadline:     startedAt.Add(RememberTotalBudget),
	})
}

func rememberDeadlinesFromContext(ctx context.Context) (RememberDeadlines, bool) {
	deadlines, ok := ctx.Value(rememberDeadlineContextKey{}).(RememberDeadlines)
	return deadlines, ok
}

// ContextForPhase applies the entry-anchored cap and the phase maximum.
func ContextForPhase(ctx context.Context, phase RememberPhase) (context.Context, context.CancelFunc) {
	if deadlines, ok := rememberDeadlinesFromContext(ctx); ok {
		phaseStart := time.Now()
		deadline := phaseStart.Add(phaseBudget(phase))
		switch phase {
		case RememberPhaseAssessment:
			deadline = minDeadline(deadline, deadlines.AssessmentDeadline)
		case RememberPhaseEmbedding:
			deadline = minDeadline(deadline, deadlines.EmbeddingDeadline)
		case RememberPhaseCommit:
			deadline = minDeadline(deadline, deadlines.CommitDeadline)
		default:
			deadline = minDeadline(deadline, deadlines.TotalDeadline)
		}
		return context.WithDeadline(ctx, deadline)
	}
	return context.WithTimeout(ctx, phaseBudget(phase))
}

func phaseBudget(phase RememberPhase) time.Duration {
	switch phase {
	case RememberPhaseAssessment:
		return RememberAssessmentBudget
	case RememberPhaseEmbedding:
		return RememberEmbeddingBudget
	case RememberPhaseCommit:
		return RememberCommitBudget
	default:
		return RememberTotalBudget
	}
}

func minDeadline(left, right time.Time) time.Time {
	if right.Before(left) {
		return right
	}
	return left
}
