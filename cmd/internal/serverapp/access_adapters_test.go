package serverapp

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

type credentialLastUsedBatchRepositoryStub struct {
	updates []repository.LastUsedUpdate
}

func (s *credentialLastUsedBatchRepositoryStub) TouchLastUsedBatch(_ context.Context, updates []repository.LastUsedUpdate) error {
	s.updates = append(s.updates, updates...)
	return nil
}

func TestCredentialActivityBatchAdapterPreservesUpdates(t *testing.T) {
	repo := &credentialLastUsedBatchRepositoryStub{}
	adapter := newCredentialActivityBatchAdapter(repo)
	id := uuid.New()
	at := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)

	require.NoError(t, adapter.TouchLastUsedBatch(context.Background(), []accessservice.LastUsedUpdate{{ID: id, At: at}}))
	require.Equal(t, []repository.LastUsedUpdate{{ID: id, At: at}}, repo.updates)
}
