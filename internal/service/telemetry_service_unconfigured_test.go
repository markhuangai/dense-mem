package service

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPrometheusTelemetryService_UnconfiguredPreservesProfileAndTeamSeriesStates(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()

	for _, tc := range []struct {
		name         string
		scope        string
		profileID    *uuid.UUID
		wantEmbed    string
		wantConflict string
	}{
		{name: "team", scope: "team", wantEmbed: TelemetryItemUnavailable, wantConflict: TelemetryItemUnavailable},
		{name: "profile", scope: "profile", profileID: &profileID, wantEmbed: TelemetryItemUnsupported, wantConflict: TelemetryItemUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewPrometheusTelemetryService("", time.Second)
			svc.SetFeatureResolver(TelemetryFeatureResolver{
				RecallFeedbackEnabled: func(context.Context) (bool, error) { return false, nil },
				DreamingEnabled:       func(context.Context, *uuid.UUID) (bool, error) { return false, nil },
			})

			snapshot, err := svc.Snapshot(context.Background(), TelemetryFilter{
				Window:    "15m",
				Scope:     tc.scope,
				TeamID:    &teamID,
				ProfileID: tc.profileID,
			})
			require.NoError(t, err)

			embed := telemetrySeriesByID(snapshot.ActivitySeries, "embedding_requests")
			require.NotNil(t, embed)
			require.Equal(t, tc.wantEmbed, embed.Status)
			if tc.scope == "team" {
				require.Equal(t, "source_unconfigured", embed.ReasonCode)
			} else {
				require.Equal(t, "scope_unsupported", embed.ReasonCode)
			}

			conflict := telemetrySeriesByID(snapshot.ActivitySeries, "conflict_review_duration")
			require.NotNil(t, conflict)
			require.Equal(t, tc.wantConflict, conflict.Status)
			if tc.scope == "team" {
				require.Equal(t, "source_unconfigured", conflict.ReasonCode)
			} else {
				require.Equal(t, "scope_unsupported", conflict.ReasonCode)
			}

			recall := telemetrySeriesByID(snapshot.ActivitySeries, "llm_recall_used_rate")
			require.NotNil(t, recall)
			require.Equal(t, TelemetryItemInactive, recall.Status)
			require.Equal(t, "feature_disabled", recall.ReasonCode)
			dream := telemetrySeriesByID(snapshot.ActivitySeries, "dream_feedbacks")
			require.NotNil(t, dream)
			require.Equal(t, TelemetryItemInactive, dream.Status)
			require.Equal(t, "feature_disabled", dream.ReasonCode)
		})
	}
}
