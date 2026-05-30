package graphquery

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type captureGraphQueryService struct {
	hadDeadline bool
	calls       int
}

func (s *captureGraphQueryService) Execute(ctx context.Context, profileID string, query string, params map[string]any) (*GraphQueryResult, error) {
	_, s.hadDeadline = ctx.Deadline()
	s.calls++
	return &GraphQueryResult{Rows: []map[string]any{{"ok": true}}, RowCount: 1}, nil
}

func TestTimeoutServiceAppliesDefaultOnlyWhenNeeded(t *testing.T) {
	t.Run("no default timeout passes context through", func(t *testing.T) {
		inner := &captureGraphQueryService{}
		svc := NewTimeoutService(inner, 0)

		got, err := svc.Execute(context.Background(), "profile-1", "RETURN 1", nil)

		require.NoError(t, err)
		require.Equal(t, 1, got.RowCount)
		require.Equal(t, 1, inner.calls)
		require.False(t, inner.hadDeadline)
	})

	t.Run("default timeout adds deadline", func(t *testing.T) {
		inner := &captureGraphQueryService{}
		svc := NewTimeoutService(inner, time.Second)

		_, err := svc.Execute(context.Background(), "profile-1", "RETURN 1", nil)

		require.NoError(t, err)
		require.True(t, inner.hadDeadline)
	})

	t.Run("existing deadline is preserved", func(t *testing.T) {
		inner := &captureGraphQueryService{}
		svc := NewTimeoutService(inner, time.Second)
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Minute))
		defer cancel()

		_, err := svc.Execute(ctx, "profile-1", "RETURN 1", nil)

		require.NoError(t, err)
		require.True(t, inner.hadDeadline)
	})
}
