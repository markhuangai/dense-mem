package serverapp

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/observability"
	privacycontract "github.com/markhuangai/dense-mem/internal/privacy/contract"
	privacyservice "github.com/markhuangai/dense-mem/internal/privacy/service"
)

var errPrivateMemoryPrepareFailed = errors.New("private-memory erasure preparation failed")

func privateMemoryPrepareBootError(err error) error {
	if err == nil {
		return nil
	}
	return errPrivateMemoryPrepareFailed
}

func preparePrivateMemoryService(
	ctx context.Context,
	repo privacycontract.PrivateMemoryRepository,
	runtimeConfig privacyservice.PrivateMemoryRuntimeConfigProvider,
	invalidator privacyservice.CredentialSessionInvalidator,
	auditService privacyservice.AuditService,
	logger observability.LogProvider,
) (*privacyservice.PrivateMemoryService, error) {
	privateMemoryService := privacyservice.NewPrivateMemoryService(privacyservice.PrivateMemoryServiceConfig{
		Repository:         repo,
		RuntimeConfig:      runtimeConfig,
		SessionInvalidator: invalidator,
		AuditService:       auditService,
		Logger:             logger,
	})
	if err := privateMemoryPrepareBootError(privateMemoryService.Prepare(ctx)); err != nil {
		return nil, err
	}
	return privateMemoryService, nil
}
