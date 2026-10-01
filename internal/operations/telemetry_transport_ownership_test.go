package operations

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTelemetryTransportOwnershipFixedResults(t *testing.T) {
	team := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	profile := uuid.MustParse("22222222-2222-4222-8222-222222222222")
	defaultFilter := TelemetryFilter{Window: "15m", Scope: "system", Audience: TelemetryAudienceOperator}
	cases := []struct {
		name   string
		mode   string
		filter TelemetryFilter
	}{}
	for _, mode := range []string{"success", "partial_failure", "empty", "sparse", "short", "negative", "nan", "positive_inf", "negative_inf", "invalid_numeric", "wrong_timestamp", "wrong_value_type", "invalid_json", "truncated_json", "api_failure", "http_failure", "pricing_missing", "canceled", "deadline"} {
		cases = append(cases, struct {
			name   string
			mode   string
			filter TelemetryFilter
		}{mode, mode, defaultFilter})
	}
	for _, scope := range []string{"team", "profile", "self"} {
		cases = append(cases, struct {
			name   string
			mode   string
			filter TelemetryFilter
		}{scope, "success", TelemetryFilter{Window: "15m", Scope: scope, TeamID: &team, ProfileID: &profile, Audience: TelemetryAudienceOperator}})
	}
	for _, window := range []string{"30m", "1h", "12h", "1d", "7d", "30d"} {
		filter := defaultFilter
		filter.Window = window
		cases = append(cases, struct {
			name   string
			mode   string
			filter TelemetryFilter
		}{"window_" + window, "success", filter})
	}
	userFilter := defaultFilter
	userFilter.Audience = TelemetryAudienceUser
	cases = append(cases, struct {
		name   string
		mode   string
		filter TelemetryFilter
	}{"user", "success", userFilter})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newTelemetryTransportOwnershipFixture(t, tc.mode)
			ctx := context.Background()
			if tc.mode == "canceled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if tc.mode == "deadline" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
			}
			snapshot, err := f.snapshot(ctx, tc.filter)
			require.NoError(t, err)
			require.NotNil(t, snapshot)
			result := f.snapshotSignature(t, snapshot, err)
			contract, calls := f.requestContract(t, 1)
			if tc.mode == "canceled" || tc.mode == "deadline" {
				require.Zero(t, calls)
			} else {
				require.Positive(t, calls)
			}
			if tc.mode == "success" || tc.mode == "partial_failure" {
				card := telemetrySpecByID(snapshot.WindowedCards, "http_requests")
				require.NotNil(t, card)
				require.Equal(t, TelemetryItemReady, card.Status)
				require.Equal(t, 2.5, card.Value)
			}
			if tc.mode == "partial_failure" {
				require.Equal(t, TelemetrySnapshotDegraded, snapshot.Status)
				require.Equal(t, "query_failed", telemetrySpecByID(snapshot.WindowedCards, "http_errors").ReasonCode)
			}
			t.Logf("fixed_case=%s result_signature_sha256=%s request_contract_sha256=%s provider_calls=%d", tc.name, result, contract, calls)
		})
	}
}
