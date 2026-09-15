package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

type provisionCounterStub struct {
	value int64
	err   error
}

func (s *provisionCounterStub) IncrWithExpire(context.Context, string, int64) (int64, error) {
	if s.err != nil {
		return 0, s.err
	}
	s.value++
	return s.value, nil
}

func (*provisionCounterStub) AddWithExpire(context.Context, string, int64, int64) (int64, error) {
	return 0, nil
}

type provisionTeamStub struct {
	team      *domain.Team
	createErr error
	deletedID uuid.UUID
}

func (s *provisionTeamStub) Create(context.Context, accessservice.CreateTeamRequest, *string, string, string, string) (*domain.Team, error) {
	if s.createErr != nil {
		return nil, s.createErr
	}
	if s.team == nil {
		s.team = &domain.Team{ID: uuid.New(), Name: "demo-team"}
	}
	return s.team, nil
}
func (s *provisionTeamStub) Get(context.Context, uuid.UUID) (*domain.Team, error) { return s.team, nil }
func (s *provisionTeamStub) GetByID(context.Context, uuid.UUID) (*domain.Team, error) {
	return s.team, nil
}
func (s *provisionTeamStub) List(context.Context, int, int) ([]*domain.Team, error) {
	return []*domain.Team{s.team}, nil
}
func (s *provisionTeamStub) Count(context.Context) (int64, error) { return 1, nil }
func (s *provisionTeamStub) Update(context.Context, uuid.UUID, accessservice.UpdateTeamRequest, *string, string, string, string) (*domain.Team, error) {
	return s.team, nil
}
func (s *provisionTeamStub) Delete(_ context.Context, id uuid.UUID, _ *string, _, _, _ string) error {
	s.deletedID = id
	return nil
}

type provisionCredentialStub struct {
	credential *domain.Credential
	raw        string
	err        error
}

func (s *provisionCredentialStub) CreateCredential(context.Context, uuid.UUID, accessservice.CreateCredentialRequest, *string, string, string, string) (*domain.Credential, string, error) {
	if s.err != nil {
		return nil, "", s.err
	}
	return s.credential, s.raw, nil
}
func (s *provisionCredentialStub) ListByTeam(context.Context, uuid.UUID, int, int) ([]*domain.Credential, error) {
	return nil, nil
}
func (s *provisionCredentialStub) CountByTeam(context.Context, uuid.UUID) (int64, error) {
	return 0, nil
}
func (s *provisionCredentialStub) GetByIDForTeam(context.Context, uuid.UUID, uuid.UUID) (*domain.Credential, error) {
	return nil, nil
}
func (s *provisionCredentialStub) ListSSOOwnedCredentials(context.Context, uuid.UUID, uuid.UUID) ([]*domain.Credential, error) {
	return nil, nil
}
func (s *provisionCredentialStub) GetSSOOwnedCredentialByID(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.Credential, error) {
	return nil, nil
}
func (s *provisionCredentialStub) GetSSOOwnedCredential(context.Context, uuid.UUID, uuid.UUID) (*domain.Credential, error) {
	return nil, nil
}
func (s *provisionCredentialStub) RevokeForTeam(context.Context, uuid.UUID, uuid.UUID, *string, string, string, string) error {
	return nil
}
func (s *provisionCredentialStub) DeleteForTeam(context.Context, uuid.UUID, uuid.UUID, *string, string, string, string) error {
	return nil
}
func (s *provisionCredentialStub) UpdateNameForTeam(context.Context, uuid.UUID, uuid.UUID, string, *string, string, string, string) (*domain.Credential, error) {
	return nil, nil
}
func (s *provisionCredentialStub) UpdateRoleForTeam(context.Context, uuid.UUID, uuid.UUID, string, *string, string, string, string) (*domain.Credential, error) {
	return nil, nil
}
func (s *provisionCredentialStub) UpdateScopesForTeam(context.Context, uuid.UUID, uuid.UUID, []string, *string, string, string, string) (*domain.Credential, error) {
	return nil, nil
}
func (s *provisionCredentialStub) RotateForTeam(context.Context, uuid.UUID, uuid.UUID, accessservice.CreateCredentialRequest, *string, string, string, string) (*domain.Credential, string, error) {
	return nil, "", nil
}

func TestProvisionerValidationQuotaAndSuccess(t *testing.T) {
	if _, err := (*Provisioner)(nil).Provision(context.Background(), ProvisionOptions{}); err == nil {
		t.Fatal("nil provisioner was accepted")
	}
	if _, err := NewProvisioner(nil, nil, nil, Quotas{}).Provision(context.Background(), ProvisionOptions{}); err == nil {
		t.Fatal("missing provisioner dependencies were accepted")
	}
	if _, err := NewProvisioner(&provisionTeamStub{}, &provisionCredentialStub{}, &provisionCounterStub{err: errors.New("counter")}, Quotas{}).Provision(context.Background(), ProvisionOptions{ClientIP: "127.0.0.1"}); err == nil {
		t.Fatal("counter failure was swallowed")
	}
	quotas := DefaultQuotas()
	quotas.IssuePerIPDay = 1
	team := &provisionTeamStub{team: &domain.Team{ID: uuid.New(), Name: "demo-team"}}
	credential := &provisionCredentialStub{credential: &domain.Credential{ID: uuid.New(), Name: "demo-credential"}, raw: "dm_demo_key"}
	response, err := NewProvisioner(team, credential, &provisionCounterStub{}, quotas).Provision(context.Background(), ProvisionOptions{ClientIP: "127.0.0.1", BaseURL: " https://demo.example/ "})
	require.NoError(t, err)
	require.Equal(t, "https://demo.example/mcp", response.MCPURL)
	require.Equal(t, "https://demo.example/ui", response.UIURL)
	require.Equal(t, "dm_demo_key", response.APIKey)
	if response.ExpiresAt.Before(time.Now().UTC()) {
		t.Fatal("provisioned credential already expired")
	}
}

func TestProvisionerRollsBackTeamWhenCredentialCreationFails(t *testing.T) {
	team := &provisionTeamStub{team: &domain.Team{ID: uuid.New(), Name: "demo-team"}}
	provisioner := NewProvisioner(team, &provisionCredentialStub{err: errors.New("credential failed")}, &provisionCounterStub{}, DefaultQuotas())
	if _, err := provisioner.Provision(context.Background(), ProvisionOptions{ClientIP: "127.0.0.1"}); err == nil {
		t.Fatal("credential failure was swallowed")
	}
	if team.deletedID != team.team.ID {
		t.Fatalf("rollback deleted team = %s, want %s", team.deletedID, team.team.ID)
	}
}
