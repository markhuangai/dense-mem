package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAuthorityIsValid(t *testing.T) {
	require.True(t, AuthorityAuthoritative.IsValid())
	require.True(t, AuthorityPrimary.IsValid())
	require.True(t, AuthoritySecondary.IsValid())
	require.True(t, AuthorityInferred.IsValid())
	require.True(t, AuthorityUnknown.IsValid())
	require.False(t, Authority("rumor").IsValid())
}

func TestTeamCompatibilityGetters(t *testing.T) {
	id := uuid.New()
	now := time.Now().UTC()
	deletedAt := now.Add(time.Hour)
	team := &Team{
		ID:          id,
		Name:        "Core",
		Description: "primary workspace",
		Metadata:    map[string]any{"tier": "prod"},
		Config:      map[string]any{"retention": "standard"},
		CreatedAt:   now,
		UpdatedAt:   now.Add(time.Minute),
		DeletedAt:   &deletedAt,
	}

	require.Equal(t, id, team.GetID())
	require.Equal(t, "Core", team.GetName())
	require.Equal(t, "primary workspace", team.GetDescription())
	require.Equal(t, map[string]any{"tier": "prod"}, team.GetMetadata())
	require.Equal(t, map[string]any{"retention": "standard"}, team.GetConfig())
	require.Equal(t, now, team.GetCreatedAt())
	require.Equal(t, now.Add(time.Minute), team.GetUpdatedAt())
	require.Equal(t, &deletedAt, team.GetDeletedAt())
}
