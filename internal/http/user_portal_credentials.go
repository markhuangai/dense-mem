package http

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func toUserPortalCredential(credential *domain.Credential) userPortalCredentialResponse {
	return userPortalCredentialResponse{
		ID:         credential.ID,
		TeamID:     credential.GetTeamID(),
		Name:       credential.GetName(),
		KeySuffix:  credential.KeySuffix,
		Scopes:     append([]string{}, credential.Scopes...),
		Role:       credential.GetRole(),
		RateLimit:  credential.RateLimit,
		LastUsedAt: controlTimePtr(credential.LastUsedAt),
		ExpiresAt:  controlTimePtr(credential.ExpiresAt),
		CreatedAt:  credential.CreatedAt.Format(time.RFC3339),
		MemoryBinding: func() string {
			binding := credential.MemoryBinding
			if !binding.Valid() {
				binding = domain.CredentialBindingSharedOnly
			}
			return string(binding)
		}(),
		MemorySpaceKind: func() string {
			binding := credential.MemoryBinding
			if !binding.Valid() {
				binding = domain.CredentialBindingSharedOnly
			}
			return string(binding.SpaceKind())
		}(),
	}
}
