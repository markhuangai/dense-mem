package serverapp

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
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
	repo repository.PrivateMemoryRepository,
	runtimeConfig service.PrivateMemoryRuntimeConfigProvider,
	invalidator service.CredentialSessionInvalidator,
	auditService service.AuditService,
	logger observability.LogProvider,
) (*service.PrivateMemoryService, error) {
	privateMemoryService := service.NewPrivateMemoryService(service.PrivateMemoryServiceConfig{
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
