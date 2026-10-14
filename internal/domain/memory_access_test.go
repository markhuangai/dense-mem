package domain

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMemoryAccessSeparatesPrivateAndSharedWrites(t *testing.T) {
	private := MemorySpaceAccess{ID: uuid.New(), Kind: MemorySpacePrivate, Generation: 3}
	shared := MemorySpaceAccess{ID: uuid.New(), Kind: MemorySpaceTeamShared, Generation: 1}
	spaces := []MemorySpaceAccess{private, shared}
	for _, tc := range []struct {
		name   string
		grants []string
		want   MemoryAccess
	}{
		{"team read only", []string{"read"}, MemoryAccess{Private: MemoryPermission{Read: true, Write: true}, TeamShared: MemoryPermission{Read: true}}},
		{"team writable", []string{"read", "write"}, MemoryAccess{Private: MemoryPermission{Read: true, Write: true}, TeamShared: MemoryPermission{Read: true, Write: true}}},
		{"no memory grant", []string{"feedback:read"}, MemoryAccess{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, MemoryCapabilities(tc.grants, spaces))
			for _, destination := range []MemorySpaceAccess{private, shared} {
				selected, err := ResolveMemoryWriteSpace(destination.Kind, tc.grants, spaces)
				allowed := tc.want.Private.Write
				if destination.Kind == MemorySpaceTeamShared {
					allowed = tc.want.TeamShared.Write
				}
				if allowed {
					require.NoError(t, err)
					require.Equal(t, destination, selected)
				} else {
					require.ErrorIs(t, err, ErrMemoryWriteDenied)
					require.Zero(t, selected)
				}
			}
		})
	}
	require.ErrorIs(t, AuthorizeMemoryMutation(uuid.New(), []string{"read", "write"}, spaces), ErrMemoryWriteDenied)
	require.NoError(t, AuthorizeMemoryMutation(private.ID, []string{"read"}, spaces))
	require.ErrorIs(t, AuthorizeMemoryMutation(shared.ID, []string{"read"}, spaces), ErrMemoryWriteDenied)
}

func TestMemoryWriteRequiresAnExplicitUnambiguousLiveDestination(t *testing.T) {
	private := MemorySpaceAccess{ID: uuid.New(), Kind: MemorySpacePrivate, Generation: 1}
	for _, tc := range []struct {
		name   string
		target MemorySpaceKind
		spaces []MemorySpaceAccess
		want   error
	}{
		{"missing target", "", []MemorySpaceAccess{private}, ErrMemoryTargetRequired},
		{"old profile target", "profile_private", []MemorySpaceAccess{private}, ErrMemoryTargetInvalid},
		{"old credential target", "credential_private", []MemorySpaceAccess{private}, ErrMemoryTargetInvalid},
		{"arbitrary target", "other", []MemorySpaceAccess{private}, ErrMemoryTargetInvalid},
		{"missing private", MemorySpacePrivate, nil, ErrMemoryWriteDenied},
		{"duplicate private", MemorySpacePrivate, []MemorySpaceAccess{private, private}, ErrMemoryWriteDenied},
		{"invalid generation", MemorySpacePrivate, []MemorySpaceAccess{{ID: private.ID, Kind: MemorySpacePrivate}}, ErrMemoryWriteDenied},
		{"shared denial is not redirected", MemorySpaceTeamShared, []MemorySpaceAccess{private}, ErrMemoryWriteDenied},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selected, err := ResolveMemoryWriteSpace(tc.target, []string{"read", "write"}, tc.spaces)
			require.ErrorIs(t, err, tc.want)
			require.Zero(t, selected)
		})
	}
}
