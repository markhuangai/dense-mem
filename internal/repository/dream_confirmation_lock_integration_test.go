package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHypothesisConfirmationLockSerializesCallbacks(t *testing.T) {
	_, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	repo := NewSemanticRepository(appDB, rls)
	teamID := uuid.NewString()
	hypothesisID := uuid.NewString()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondEntered := make(chan struct{})
	firstErr := make(chan error, 1)
	secondErr := make(chan error, 1)

	go func() {
		firstErr <- repo.WithHypothesisConfirmationLock(ctx, teamID, hypothesisID, func() error {
			close(firstEntered)
			<-releaseFirst
			return nil
		})
	}()
	select {
	case <-firstEntered:
	case <-ctx.Done():
		t.Fatal("first confirmation lock callback did not start")
	}

	go func() {
		secondErr <- repo.WithHypothesisConfirmationLock(ctx, teamID, hypothesisID, func() error {
			close(secondEntered)
			return nil
		})
	}()
	select {
	case <-secondEntered:
		t.Fatal("second confirmation callback ran before the first released the lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseFirst)

	require.NoError(t, <-firstErr)
	select {
	case <-secondEntered:
	case <-ctx.Done():
		t.Fatal("second confirmation lock callback did not run after release")
	}
	require.NoError(t, <-secondErr)
}
