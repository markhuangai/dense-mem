package repository

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAppendPlacementFirstDispositionRequiresTimestamps(t *testing.T) {
	timestamp := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		createdAt   time.Time
		completedAt time.Time
		want        string
	}{
		{
			name:        "created at",
			completedAt: timestamp,
			want:        "placement first disposition requires created_at",
		},
		{
			name:      "completed at",
			createdAt: timestamp,
			want:      "placement first disposition requires completed_at",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := appendPlacementFirstDisposition(
				context.Background(), nil, "team", "profile", "run", "completed", tt.createdAt, tt.completedAt,
			)

			require.Nil(t, result)
			require.EqualError(t, err, tt.want)
		})
	}
}
