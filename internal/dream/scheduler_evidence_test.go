package dream

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestSchedulerPreservesEarlierDailyDispatchWhenLaterTeamPageFails(t *testing.T) {
	teams := make([]*domain.Team, schedulerTeamPageSize+1)
	for index := range teams {
		teams[index] = &domain.Team{ID: uuid.New()}
	}
	base := &schedulerDreamStub{cfg: dueSchedulerConfig()}
	dreams := base
	profiles := &schedulerProfileStub{profiles: teams, offsetErrs: map[int]error{schedulerTeamPageSize: errors.New("second page failed")}}
	scheduler := NewScheduler(dreams, profiles, discardSchedulerLogger())
	scheduler.now = func() time.Time { return time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC) }

	scheduler.runDue(context.Background())

	require.Len(t, base.scheduledTeams, schedulerTeamPageSize)
}
