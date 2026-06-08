package domain

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyGettersUseTeamAndNameCompatibilityFallbacks(t *testing.T) {
	profileID := uuid.New()
	teamID := uuid.New()
	keyID := uuid.New()
	lastUsed := time.Now().UTC().Add(-time.Hour)
	expires := time.Now().UTC().Add(time.Hour)
	created := time.Now().UTC().Add(-24 * time.Hour)
	revoked := time.Now().UTC()

	key := &APIKey{
		ID:         keyID,
		ProfileID:  profileID,
		TeamID:     teamID,
		Label:      "legacy label",
		Name:       "primary key",
		KeyHash:    "hash",
		KeyPrefix:  "dm_123",
		KeySuffix:  "abcd",
		Scopes:     []string{"memory:read"},
		Role:       "manager",
		RateLimit:  120,
		LastUsedAt: &lastUsed,
		ExpiresAt:  &expires,
		CreatedAt:  created,
		RevokedAt:  &revoked,
	}

	require.Equal(t, keyID, key.GetID())
	require.Equal(t, teamID, key.GetTeamID())
	require.Equal(t, teamID, key.GetProfileID())
	require.Equal(t, "primary key", key.GetProfileName())
	require.Equal(t, "primary key", key.GetLabel())
	require.Equal(t, "hash", key.GetKeyHash())
	require.Equal(t, "dm_123", key.GetKeyPrefix())
	require.Equal(t, "abcd", key.GetKeySuffix())
	require.Equal(t, []string{"memory:read"}, key.GetScopes())
	require.Equal(t, "manager", key.GetRole())
	require.Equal(t, 120, key.GetRateLimit())
	require.Equal(t, &lastUsed, key.GetLastUsedAt())
	require.Equal(t, &expires, key.GetExpiresAt())
	require.Equal(t, created, key.GetCreatedAt())
	require.Equal(t, &revoked, key.GetRevokedAt())

	legacy := &APIKey{ProfileID: profileID, Label: "legacy label"}
	require.Equal(t, profileID, legacy.GetTeamID())
	require.Equal(t, profileID, legacy.GetProfileID())
	require.Equal(t, "legacy label", legacy.GetProfileName())
	require.Equal(t, "legacy label", legacy.GetLabel())
	require.Equal(t, "member", legacy.GetRole())
}
