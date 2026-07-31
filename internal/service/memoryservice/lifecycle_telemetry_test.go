package memoryservice

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestLifecycleRecordsFirstDispositionFromResolution(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	createdAt := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	metrics := observability.NewPrometheusMetrics()
	placement := &lifecyclePlacementStub{
		result: &repository.ResolvePlacementReviewResult{
			DecisionID: "resolution-first-disposition",
			IngestID:   uuid.NewString(),
			Status:     string(domain.PlacementRunCompleted),
			FirstDisposition: &repository.PlacementFirstDisposition{
				Status:      string(domain.PlacementRunCompleted),
				CreatedAt:   createdAt,
				CompletedAt: createdAt.Add(2 * time.Second),
			},
		},
	}
	svc := NewLifecycleService(LifecycleDependencies{Placement: placement, Metrics: metrics})

	_, err := svc.ResolveMemoryPlacement(authenticatedRememberContext(teamID, profileID, keyID), ResolveMemoryPlacementRequest{
		ContractVersion:      domain.ContractVersion,
		Action:               domain.ResolveReject,
		IngestID:             placement.result.IngestID,
		PlacementItemID:      uuid.NewString(),
		PlacementItemVersion: 1,
		Message:              "not durable memory",
		IdempotencyKey:       "resolution-first-disposition",
	})
	require.NoError(t, err)

	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		if strings.HasPrefix(line, "densemem_remember_first_disposition_total{") &&
			strings.Contains(line, "team_id=\""+teamID.String()+"\"") &&
			strings.Contains(line, "profile_id=\""+profileID.String()+"\"") &&
			strings.Contains(line, "status=\"completed\"") {
			require.True(t, strings.HasSuffix(line, " 1"), "first disposition line = %q", line)
			return
		}
	}
	t.Fatal("resolution did not record a remember first-disposition metric")
}
