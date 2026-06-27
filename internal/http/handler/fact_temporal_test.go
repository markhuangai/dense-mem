package handler

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestFactMatchesTemporalWindow(t *testing.T) {
	base := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	before := base.Add(-time.Hour)
	after := base.Add(time.Hour)

	tests := []struct {
		name    string
		fact    *domain.Fact
		validAt *time.Time
		knownAt *time.Time
		want    bool
	}{
		{
			name: "nil fact",
			want: false,
		},
		{
			name: "within open temporal window",
			fact: &domain.Fact{RecordedAt: before},
			want: true,
		},
		{
			name:    "valid before valid_from",
			fact:    &domain.Fact{ValidFrom: &after, RecordedAt: before},
			validAt: &base,
			want:    false,
		},
		{
			name:    "valid at closed valid_to boundary",
			fact:    &domain.Fact{ValidTo: &base, RecordedAt: before},
			validAt: &base,
			want:    false,
		},
		{
			name:    "known before recorded_at",
			fact:    &domain.Fact{RecordedAt: after},
			knownAt: &base,
			want:    false,
		},
		{
			name:    "known at closed recorded_to boundary",
			fact:    &domain.Fact{RecordedAt: before, RecordedTo: &base},
			knownAt: &base,
			want:    false,
		},
		{
			name:    "inside validity and knowledge windows",
			fact:    &domain.Fact{ValidFrom: &before, ValidTo: &after, RecordedAt: before, RecordedTo: &after},
			validAt: &base,
			knownAt: &base,
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, factMatchesTemporalWindow(tt.fact, tt.validAt, tt.knownAt))
		})
	}
}
