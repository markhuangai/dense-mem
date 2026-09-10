package serverapp

import (
	"context"

	accesscontract "github.com/markhuangai/dense-mem/internal/access/contract"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

type credentialLastUsedBatchRepository interface {
	TouchLastUsedBatch(context.Context, []accesscontract.LastUsedUpdate) error
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
	converted := make([]accesscontract.LastUsedUpdate, len(updates))
	for index, update := range updates {
		converted[index] = accesscontract.LastUsedUpdate(update)
	}
	return a.repo.TouchLastUsedBatch(ctx, converted)
}
