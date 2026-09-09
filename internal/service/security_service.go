package service

// This compatibility facade keeps the historical security service import path
// stable while policy lives in internal/settings.

import (
	settings "github.com/markhuangai/dense-mem/internal/settings"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
)

type SecurityService = settings.SecurityService
type SecurityServiceImpl = settings.SecurityServiceImpl

var (
	ErrInvalidSecurityIP       = settings.ErrInvalidSecurityIP
	ErrInvalidSecuritySettings = settings.ErrInvalidSecuritySettings
)

func NewSecurityService(repo settingscontract.SecurityRepository, audit AuditService) *SecurityServiceImpl {
	return settings.NewSecurityService(repo, audit)
}
