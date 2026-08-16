package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const (
	UserPortalSessionCookieName = "dense_mem_ui_session"
	UserPortalCSRFCookieName    = "dense_mem_ui_csrf"
	UserPortalSessionTTL        = 7 * 24 * time.Hour
)

var (
	ErrUserPortalSessionInvalid = errors.New("user portal session invalid")
	ErrUserPortalCSRFInvalid    = errors.New("user portal csrf invalid")
)

type UserPortalSessionResult struct {
	SessionToken string
	CSRFToken    string
	ExpiresAt    time.Time
}

// ActiveCredentialRepository is deliberately narrower than the general credential
// repository. Portal sessions need only the stable key identity and current
// authorization metadata on each request.
type ActiveCredentialRepository interface {
	GetActiveByID(ctx context.Context, id uuid.UUID) (*domain.Credential, error)
}

type UserPortalSessionManager interface {
	CreateSession(ctx context.Context, credentialID uuid.UUID) (*UserPortalSessionResult, error)
	AuthenticateSession(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.AuthenticatedActor, error)
	Logout(ctx context.Context, sessionToken string) error
}

type UserPortalSessionService struct {
	repo        repository.UserPortalSessionRepository
	credentials ActiveCredentialRepository
	now         func() time.Time
}

var _ UserPortalSessionManager = (*UserPortalSessionService)(nil)

func NewUserPortalSessionService(repo repository.UserPortalSessionRepository, credentials ActiveCredentialRepository, now func() time.Time) *UserPortalSessionService {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &UserPortalSessionService{repo: repo, credentials: credentials, now: now}
}

func (s *UserPortalSessionService) CreateSession(ctx context.Context, credentialID uuid.UUID) (*UserPortalSessionResult, error) {
	if s == nil || s.repo == nil || s.credentials == nil || credentialID == uuid.Nil {
		return nil, ErrUserPortalSessionInvalid
	}
	credential, err := s.credentials.GetActiveByID(ctx, credentialID)
	if err != nil {
		return nil, err
	}
	if credential == nil {
		return nil, ErrUserPortalSessionInvalid
	}

	now := s.now().UTC()
	if err := s.repo.DeleteExpiredSessions(ctx, now); err != nil {
		return nil, err
	}
	sessionToken, err := secureRandomToken(32)
	if err != nil {
		return nil, err
	}
	csrfToken, err := secureRandomToken(32)
	if err != nil {
		return nil, err
	}
	expiresAt := now.Add(UserPortalSessionTTL)
	if err := s.repo.CreateSession(ctx, &domain.UserPortalSession{
		SessionHash:  HashSSOToken(sessionToken),
		CredentialID: credential.ID,
		CSRFHash:     HashSSOToken(csrfToken),
		ExpiresAt:    expiresAt,
		CreatedAt:    now,
	}); err != nil {
		return nil, err
	}
	return &UserPortalSessionResult{
		SessionToken: sessionToken,
		CSRFToken:    csrfToken,
		ExpiresAt:    expiresAt,
	}, nil
}

func (s *UserPortalSessionService) AuthenticateSession(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.AuthenticatedActor, error) {
	if s == nil || s.repo == nil || s.credentials == nil || strings.TrimSpace(sessionToken) == "" {
		return nil, ErrUserPortalSessionInvalid
	}
	session, err := s.repo.GetSession(ctx, HashSSOToken(sessionToken))
	if err != nil {
		return nil, err
	}
	if session == nil || !session.ExpiresAt.After(s.now().UTC()) {
		return nil, ErrUserPortalSessionInvalid
	}
	if requireCSRF && !hashMatches(csrfToken, session.CSRFHash) {
		return nil, ErrUserPortalCSRFInvalid
	}
	credential, err := s.credentials.GetActiveByID(ctx, session.CredentialID)
	if err != nil {
		return nil, err
	}
	if credential == nil {
		return nil, ErrUserPortalSessionInvalid
	}
	return authenticatedActorFromCredential(credential), nil
}

func authenticatedActorFromCredential(credential *domain.Credential) *domain.AuthenticatedActor {
	if credential == nil {
		return nil
	}
	return &domain.AuthenticatedActor{
		Team: domain.Team{ID: credential.TeamID, Name: credential.TeamName},
		Identity: domain.ActorIdentity{
			ID:          credential.ActorIdentityID,
			Kind:        "api_client",
			DisplayName: credential.Name,
		},
		Membership: domain.Membership{
			ID:              credential.MembershipID,
			ActorIdentityID: credential.ActorIdentityID,
			TeamID:          credential.TeamID,
			OwnerID:         credential.OwnerID,
			Name:            credential.Name,
			Grants:          append([]string(nil), credential.Scopes...),
			Role:            credential.Role,
			Status:          "active",
		},
		OwnerID:    credential.OwnerID,
		Credential: credential,
	}
}

func (s *UserPortalSessionService) Logout(ctx context.Context, sessionToken string) error {
	if s == nil || s.repo == nil || strings.TrimSpace(sessionToken) == "" {
		return nil
	}
	return s.repo.DeleteSession(ctx, HashSSOToken(sessionToken))
}
