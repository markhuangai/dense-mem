package serverapp

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	accesscontract "github.com/markhuangai/dense-mem/internal/access/contract"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

type credentialLastUsedBatchRepositoryStub struct {
	updates []accesscontract.LastUsedUpdate
}

func (s *credentialLastUsedBatchRepositoryStub) TouchLastUsedBatch(_ context.Context, updates []accesscontract.LastUsedUpdate) error {
	s.updates = append(s.updates, updates...)
	return nil
}

func TestCredentialActivityBatchAdapterPreservesUpdates(t *testing.T) {
	repo := &credentialLastUsedBatchRepositoryStub{}
	adapter := newCredentialActivityBatchAdapter(repo)
	id := uuid.New()
	at := time.Date(2026, time.August, 22, 12, 0, 0, 0, time.UTC)

	require.NoError(t, adapter.TouchLastUsedBatch(context.Background(), []accessservice.LastUsedUpdate{{ID: id, At: at}}))
	require.Equal(t, []accesscontract.LastUsedUpdate{{ID: id, At: at}}, repo.updates)
}
