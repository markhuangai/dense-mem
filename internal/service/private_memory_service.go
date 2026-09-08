package service

import (
	privacyservice "github.com/markhuangai/dense-mem/internal/privacy/service"
)

// Legacy aliases keep the HTTP and composition packages source-compatible
// while privacy owns the application workflow and worker lifecycle.
type PrivateMemoryRuntimeConfigProvider = privacyservice.PrivateMemoryRuntimeConfigProvider
type PrivateMemoryCommand = privacyservice.PrivateMemoryCommand
type PrivateMemoryAuditContext = privacyservice.PrivateMemoryAuditContext
type PrivateMemoryServiceConfig = privacyservice.PrivateMemoryServiceConfig
type PrivateMemoryService = privacyservice.PrivateMemoryService

var (
	ErrPrivateMemoryAcknowledgementRequired  = privacyservice.ErrPrivateMemoryAcknowledgementRequired
	ErrPrivateMemoryIdempotencyKeyRequired   = privacyservice.ErrPrivateMemoryIdempotencyKeyRequired
	ErrPrivateMemoryInvalidReason            = privacyservice.ErrPrivateMemoryInvalidReason
	ErrPrivateMemoryAuditUnavailable         = privacyservice.ErrPrivateMemoryAuditUnavailable
	ErrPrivateMemoryRuntimeConfigUnavailable = privacyservice.ErrPrivateMemoryRuntimeConfigUnavailable
)

func NewPrivateMemoryService(config PrivateMemoryServiceConfig) *PrivateMemoryService {
	return privacyservice.NewPrivateMemoryService(config)
}
