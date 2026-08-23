package serverapp

import (
	"context"

	"github.com/markhuangai/dense-mem/internal/repository"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

type credentialLastUsedBatchRepository interface {
	TouchLastUsedBatch(context.Context, []repository.LastUsedUpdate) error
}

type credentialActivityBatchAdapter struct {
	repo credentialLastUsedBatchRepository
}

func newCredentialActivityBatchAdapter(repo credentialLastUsedBatchRepository) accessservice.CredentialLastUsedBatchStore {
	if repo == nil {
		return nil
	}
	return credentialActivityBatchAdapter{repo: repo}
}

func (a credentialActivityBatchAdapter) TouchLastUsedBatch(ctx context.Context, updates []accessservice.LastUsedUpdate) error {
	converted := make([]repository.LastUsedUpdate, len(updates))
	for index, update := range updates {
		converted[index] = repository.LastUsedUpdate{ID: update.ID, At: update.At}
	}
	return a.repo.TouchLastUsedBatch(ctx, converted)
}
