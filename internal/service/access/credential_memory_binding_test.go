package access

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestNormalizeCredentialMemoryBindingDefaults(t *testing.T) {
	identity := uuid.New()
	binding, err := normalizeCredentialMemoryBinding("", &identity, []string{CredentialScopeRead})
	require.NoError(t, err)
	require.Equal(t, domain.CredentialBindingProfilePrivate, binding)

	binding, err = normalizeCredentialMemoryBinding("", nil, StandardCredentialScopes())
	require.NoError(t, err)
	require.Equal(t, domain.CredentialBindingCredentialPrivate, binding)

	binding, err = normalizeCredentialMemoryBinding("", nil, []string{CredentialScopeRead})
	require.NoError(t, err)
	require.Equal(t, domain.CredentialBindingSharedOnly, binding)

	_, err = normalizeCredentialMemoryBinding(string(domain.CredentialBindingProfilePrivate), nil, StandardCredentialScopes())
	require.Error(t, err)

	_, err = normalizeCredentialMemoryBinding("unknown", nil, StandardCredentialScopes())
	require.Error(t, err)

	binding, err = normalizeCredentialMemoryBinding(string(domain.CredentialBindingSharedOnly), nil, StandardCredentialScopes())
	require.NoError(t, err)
	require.Equal(t, domain.CredentialBindingSharedOnly, binding)
}
