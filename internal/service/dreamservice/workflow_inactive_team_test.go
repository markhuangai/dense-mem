package dreamservice

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestRunCycleTranslatesInactiveTeamRepositoryErrors(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	ctx := dreamTestContext(teamID, ownerID)
	cfg := domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC"}

	for _, tc := range []struct {
		name string
		repo *dreamRepositoryStub
	}{
		{
			name: "list inputs",
			repo: &dreamRepositoryStub{listInputsErr: repository.ErrTeamInactive},
		},
		{
			name: "claim cycle",
			repo: &dreamRepositoryStub{claimErr: repository.ErrTeamInactive},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(Dependencies{
				Store:     tc.repo,
				AppConfig: cycleAppConfigStub{cfg: cfg},
				Now:       func() time.Time { return time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC) },
			})

			result, err := svc.RunCycle(ctx, "ignored-profile", RunCycleRequest{})

			var apiErr *httperr.APIError
			require.ErrorAs(t, err, &apiErr)
			require.Equal(t, httperr.NOT_FOUND, apiErr.Code)
			require.NotErrorIs(t, err, repository.ErrTeamInactive)
			require.NotNil(t, result)
			require.Equal(t, "error", result.Status)
		})
	}
}
