package middleware

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestActorAllowedSpacesUsesSSOMembershipPrivateSpace(t *testing.T) {
	teamID := uuid.New()
	privateID := uuid.New()
	actor := testSSOActor(teamID, uuid.New(), uuid.New(), uuid.New(), nil, []string{"read"})
	actor.Membership.MemorySpaceID = privateID

	spaces := actorAllowedSpaces(actor)
	require.Len(t, spaces, 2)
	assert.Equal(t, domain.MemorySpaceTeamShared, spaces[0].Kind)
	assert.Equal(t, domain.MemorySpaceProfilePrivate, spaces[1].Kind)
	assert.Equal(t, privateID, spaces[1].ID)
}

func TestActorAllowedSpacesSkipsNilCredentialPrivateSpace(t *testing.T) {
	actor := testSSOActor(uuid.New(), uuid.New(), uuid.New(), uuid.New(), nil, []string{"read"})
	actor.Credential = &domain.Credential{
		MemoryBinding:     domain.CredentialBindingProfilePrivate,
		TeamSharedSpaceID: uuid.New(),
	}

	spaces := actorAllowedSpaces(actor)
	require.Len(t, spaces, 1)
	assert.Equal(t, domain.MemorySpaceTeamShared, spaces[0].Kind)
}
