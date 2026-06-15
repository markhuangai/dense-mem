package communityservice

import (
	"context"
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestCommunityReadServicesDelegateToStore(t *testing.T) {
	store := &communityReadStoreStub{
		community: &domain.Community{CommunityID: "c-1", ProfileID: "profile-1"},
		communities: []*domain.Community{
			{CommunityID: "c-1", ProfileID: "profile-1"},
			{CommunityID: "c-2", ProfileID: "profile-1"},
		},
	}

	getSvc := &getCommunitySummaryServiceImpl{store: store}
	got, err := getSvc.Get(context.Background(), "profile-1", "c-1")
	require.NoError(t, err)
	require.Equal(t, "c-1", got.CommunityID)
	require.Equal(t, "profile-1", store.getProfileID)
	require.Equal(t, "c-1", store.getCommunityID)

	listSvc := &listCommunitiesServiceImpl{store: store}
	list, err := listSvc.List(context.Background(), "profile-1", 2)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "profile-1", store.listProfileID)
	require.Equal(t, 2, store.listLimit)
}

type communityReadStoreStub struct {
	community      *domain.Community
	communities    []*domain.Community
	getProfileID   string
	getCommunityID string
	listProfileID  string
	listLimit      int
}

func (s *communityReadStoreStub) Get(ctx context.Context, profileID string, communityID string) (*domain.Community, error) {
	s.getProfileID = profileID
	s.getCommunityID = communityID
	return s.community, nil
}

func (s *communityReadStoreStub) List(ctx context.Context, profileID string, limit int) ([]*domain.Community, error) {
	s.listProfileID = profileID
	s.listLimit = limit
	return s.communities, nil
}
