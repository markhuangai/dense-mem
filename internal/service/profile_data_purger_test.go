package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfileDataPurgerIsNoopAfterCutover(t *testing.T) {
	t.Parallel()

	purger := NewProfileDataPurger()

	err := purger.PurgeProfileData(context.Background(), "profile-123")
	require.NoError(t, err)
}
