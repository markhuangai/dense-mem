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

// ActiveAPIKeyRepository is deliberately narrower than the general API-key
// repository. Portal sessions need only the stable key identity and current
// authorization metadata on each request.
type ActiveAPIKeyRepository interface {
	GetActiveByID(ctx context.Context, id uuid.UUID) (*domain.APIKey, error)
}

type UserPortalSessionManager interface {
	CreateSession(ctx context.Context, keyID uuid.UUID) (*UserPortalSessionResult, error)
	AuthenticateSession(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.APIKey, error)
	Logout(ctx context.Context, sessionToken string) error
}

type UserPortalSessionService struct {
	repo repository.UserPortalSessionRepository
	keys ActiveAPIKeyRepository
	now  func() time.Time
}

var _ UserPortalSessionManager = (*UserPortalSessionService)(nil)

func NewUserPortalSessionService(repo repository.UserPortalSessionRepository, keys ActiveAPIKeyRepository, now func() time.Time) *UserPortalSessionService {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &UserPortalSessionService{repo: repo, keys: keys, now: now}
}

func (s *UserPortalSessionService) CreateSession(ctx context.Context, keyID uuid.UUID) (*UserPortalSessionResult, error) {
	if s == nil || s.repo == nil || s.keys == nil || keyID == uuid.Nil {
		return nil, ErrUserPortalSessionInvalid
	}
	key, err := s.keys.GetActiveByID(ctx, keyID)
	if err != nil {
		return nil, err
	}
	if key == nil {
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
		SessionHash: HashSSOToken(sessionToken),
		KeyID:       key.ID,
		CSRFHash:    HashSSOToken(csrfToken),
		ExpiresAt:   expiresAt,
		CreatedAt:   now,
	}); err != nil {
		return nil, err
	}
	return &UserPortalSessionResult{
		SessionToken: sessionToken,
		CSRFToken:    csrfToken,
		ExpiresAt:    expiresAt,
	}, nil
}

func (s *UserPortalSessionService) AuthenticateSession(ctx context.Context, sessionToken, csrfToken string, requireCSRF bool) (*domain.APIKey, error) {
	if s == nil || s.repo == nil || s.keys == nil || strings.TrimSpace(sessionToken) == "" {
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
	key, err := s.keys.GetActiveByID(ctx, session.KeyID)
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, ErrUserPortalSessionInvalid
	}
	return key, nil
}

func (s *UserPortalSessionService) Logout(ctx context.Context, sessionToken string) error {
	if s == nil || s.repo == nil || strings.TrimSpace(sessionToken) == "" {
		return nil
	}
	return s.repo.DeleteSession(ctx, HashSSOToken(sessionToken))
}
