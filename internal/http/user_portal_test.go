package http

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/graphview"
	"github.com/markhuangai/dense-mem/internal/storage/inmem"
)

type userPortalAuthRepo struct {
	key *domain.APIKey
}

type failingPublicRateLimitService struct {
	err error
}

func (s failingPublicRateLimitService) Check(context.Context, string, string, int) (bool, int, time.Time, error) {
	return false, 0, time.Time{}, s.err
}

func (r *userPortalAuthRepo) CreateStandardKey(context.Context, *domain.APIKey) error { return nil }
func (r *userPortalAuthRepo) ListByProfile(context.Context, uuid.UUID, int, int) ([]*domain.APIKey, error) {
	return nil, nil
}
func (r *userPortalAuthRepo) CountByProfile(context.Context, uuid.UUID) (int64, error) { return 0, nil }
func (r *userPortalAuthRepo) GetByIDForProfile(context.Context, uuid.UUID, uuid.UUID) (*domain.APIKey, error) {
	return nil, nil
}
func (r *userPortalAuthRepo) GetSSOOwnedKey(context.Context, uuid.UUID, uuid.UUID) (*domain.APIKey, error) {
	return nil, nil
}
func (r *userPortalAuthRepo) GetActiveByPrefix(_ context.Context, prefix string) (*domain.APIKey, error) {
	if r.key != nil && r.key.KeyPrefix == prefix {
		key := *r.key
		return &key, nil
	}
	return nil, nil
}
func (r *userPortalAuthRepo) RevokeForProfile(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return 0, nil
}
func (r *userPortalAuthRepo) DeleteForProfile(context.Context, uuid.UUID, uuid.UUID) (int64, error) {
	return 0, nil
}
func (r *userPortalAuthRepo) UpdateNameForProfile(context.Context, uuid.UUID, uuid.UUID, string) (int64, error) {
	return 0, nil
}
func (r *userPortalAuthRepo) UpdateRoleForProfile(context.Context, uuid.UUID, uuid.UUID, string, []string) (int64, error) {
	return 0, nil
}
func (r *userPortalAuthRepo) UpdateScopesForProfile(context.Context, uuid.UUID, uuid.UUID, []string) (int64, error) {
	return 0, nil
}
func (r *userPortalAuthRepo) RotateForProfile(context.Context, uuid.UUID, uuid.UUID, string, string, string, *time.Time) (int64, error) {
	return 0, nil
}
func (r *userPortalAuthRepo) TouchLastUsed(context.Context, uuid.UUID) error { return nil }

type userPortalKeySvc struct {
	keys            []*domain.APIKey
	rotateProfileID uuid.UUID
	rotateKeyID     uuid.UUID
	deleteProfileID uuid.UUID
	deleteKeyID     uuid.UUID
	lastRotateReq   service.CreateAPIKeyRequest
	lastCreateReq   service.CreateAPIKeyRequest
	rawRotatedKey   string
	rawCreatedKey   string
}

func (s *userPortalKeySvc) CreateStandardKey(_ context.Context, profileID uuid.UUID, req service.CreateAPIKeyRequest, _ *string, _ string, _ string, _ string) (*domain.APIKey, string, error) {
	scopes, err := service.NormalizeAPIKeyScopes(req.Scopes)
	if err != nil {
		return nil, "", err
	}
	if s.rawCreatedKey == "" {
		s.rawCreatedKey = "dm_created_plaintext"
	}
	s.lastCreateReq = req
	key := &domain.APIKey{
		ID:                 uuid.New(),
		TeamID:             profileID,
		ProfileID:          profileID,
		Name:               req.Name,
		Label:              req.Name,
		KeySuffix:          "own123",
		Scopes:             scopes,
		Role:               service.APIKeyRoleMember,
		RateLimit:          req.RateLimit,
		CreatedAt:          time.Now().UTC().Truncate(time.Second),
		ExpiresAt:          req.ExpiresAt,
		SSOOwnerIdentityID: req.SSOOwnerIdentityID,
	}
	s.keys = append(s.keys, key)
	return key, s.rawCreatedKey, nil
}
func (s *userPortalKeySvc) UpdateNameForProfile(_ context.Context, profileID, id uuid.UUID, name string, _ *string, _ string, _ string, _ string) (*domain.APIKey, error) {
	key, _ := s.GetByIDForProfile(context.Background(), profileID, id)
	if key == nil {
		return nil, httperr.New(httperr.NOT_FOUND, "key not found")
	}
	key.Name = name
	key.Label = name
	return key, nil
}
func (s *userPortalKeySvc) UpdateRoleForProfile(context.Context, uuid.UUID, uuid.UUID, string, *string, string, string, string) (*domain.APIKey, error) {
	return nil, nil
}
func (s *userPortalKeySvc) UpdateScopesForProfile(_ context.Context, profileID, id uuid.UUID, scopes []string, _ *string, _ string, _ string, _ string) (*domain.APIKey, error) {
	key, _ := s.GetByIDForProfile(context.Background(), profileID, id)
	if key == nil {
		return nil, httperr.New(httperr.NOT_FOUND, "key not found")
	}
	normalized, err := service.NormalizeAPIKeyScopes(scopes)
	if err != nil {
		return nil, err
	}
	key.Scopes = normalized
	return key, nil
}
func (s *userPortalKeySvc) RotateForProfile(_ context.Context, profileID, id uuid.UUID, req service.CreateAPIKeyRequest, _ *string, _ string, _ string, _ string) (*domain.APIKey, string, error) {
	key, _ := s.GetByIDForProfile(context.Background(), profileID, id)
	if key == nil {
		return nil, "", httperr.New(httperr.NOT_FOUND, "key not found")
	}
	s.rotateProfileID = profileID
	s.rotateKeyID = id
	s.lastRotateReq = req
	key.KeySuffix = "rot8ed"
	key.LastUsedAt = nil
	if s.rawRotatedKey == "" {
		s.rawRotatedKey = "dm_rotated_plaintext"
	}
	return key, s.rawRotatedKey, nil
}
func (s *userPortalKeySvc) ListByProfile(_ context.Context, profileID uuid.UUID, _, _ int) ([]*domain.APIKey, error) {
	out := make([]*domain.APIKey, 0, len(s.keys))
	for _, key := range s.keys {
		if key.GetTeamID() == profileID {
			out = append(out, key)
		}
	}
	return out, nil
}
func (s *userPortalKeySvc) CountByProfile(_ context.Context, profileID uuid.UUID) (int64, error) {
	var count int64
	for _, key := range s.keys {
		if key.GetTeamID() == profileID {
			count++
		}
	}
	return count, nil
}
func (s *userPortalKeySvc) GetByIDForProfile(_ context.Context, profileID, id uuid.UUID) (*domain.APIKey, error) {
	for _, key := range s.keys {
		if key.GetTeamID() == profileID && key.ID == id {
			return key, nil
		}
	}
	return nil, httperr.New(httperr.NOT_FOUND, "key not found")
}
func (s *userPortalKeySvc) GetSSOOwnedKey(_ context.Context, profileID, identityID uuid.UUID) (*domain.APIKey, error) {
	for _, key := range s.keys {
		if key.GetTeamID() == profileID && key.SSOOwnerIdentityID != nil && *key.SSOOwnerIdentityID == identityID && key.RevokedAt == nil {
			return key, nil
		}
	}
	return nil, nil
}
func (s *userPortalKeySvc) DeleteForProfile(_ context.Context, profileID, id uuid.UUID, _ *string, _ string, _ string, _ string) error {
	s.deleteProfileID = profileID
	s.deleteKeyID = id
	return nil
}

type userPortalNilKeySvc struct {
	userPortalKeySvc
	err error
}

func (s *userPortalNilKeySvc) GetByIDForProfile(context.Context, uuid.UUID, uuid.UUID) (*domain.APIKey, error) {
	return nil, s.err
}

type userPortalRotateErrorKeySvc struct {
	userPortalKeySvc
	err error
}

func (s *userPortalRotateErrorKeySvc) RotateForProfile(context.Context, uuid.UUID, uuid.UUID, service.CreateAPIKeyRequest, *string, string, string, string) (*domain.APIKey, string, error) {
	return nil, "", s.err
}

type userPortalGraphSvc struct {
	profileID        string
	query            graphview.Query
	calls            int
	detailProfileID  string
	detailType       string
	detailID         string
	detailCalls      int
	err              error
	nodeDetailErr    error
	nodeDetailResult *graphview.Node
}

func (s *userPortalGraphSvc) Graph(_ context.Context, profileID string, query graphview.Query) (*graphview.Snapshot, error) {
	s.profileID = profileID
	s.query = query
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &graphview.Snapshot{
		Scope: query.Scope,
		Nodes: []graphview.Node{{
			Key:   "entity:entity-1",
			ID:    "entity-1",
			Type:  "entity",
			Title: "entity title",
		}},
	}, nil
}

func (s *userPortalGraphSvc) NodeDetail(_ context.Context, profileID string, nodeType string, nodeID string) (*graphview.Node, error) {
	s.detailProfileID = profileID
	s.detailType = nodeType
	s.detailID = nodeID
	s.detailCalls++
	if s.nodeDetailErr != nil {
		return nil, s.nodeDetailErr
	}
	if s.nodeDetailResult != nil {
		return s.nodeDetailResult, nil
	}
	return &graphview.Node{
		Key:         "entity:entity-1",
		ID:          "entity-1",
		Type:        "entity",
		Title:       "entity title",
		Body:        "entity detail",
		Status:      "active",
		CommunityID: "community-1",
		Score:       0.9,
	}, nil
}

func TestUserPortalSessionShowsOnlyAuthenticatedKey(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Mine", []string{"read"})
	otherKey := &domain.APIKey{
		ID:        uuid.New(),
		TeamID:    teamID,
		ProfileID: teamID,
		Name:      "Other",
		Label:     "Other",
		KeySuffix: "other1",
		Scopes:    []string{"read", "write"},
		RateLimit: 120,
		CreatedAt: time.Now().UTC(),
	}
	server := userPortalTestServer(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey, otherKey}}, "")

	req := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), authKey.ID.String())
	require.Contains(t, rec.Body.String(), `"config":{"dreaming":{"enabled":true},"retention":"standard"}`)
	require.Contains(t, rec.Body.String(), `"can_rotate":false`)
	require.NotContains(t, rec.Body.String(), otherKey.ID.String())
	require.NotContains(t, rec.Body.String(), "Other")
}

func TestUserPortalTelemetryMemberUsesSelfScope(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, keyID, "Mine", []string{"read", "write"})
	telemetry := &controlTelemetrySvc{snapshot: &service.TelemetrySnapshot{
		Available: true,
		Window:    service.TelemetryWindow{Key: "15m"},
		Cards:     []service.TelemetryCard{{ID: "http_requests", Label: "HTTP requests", Unit: "requests", Value: 4}},
	}}
	server := userPortalTestServerWithTelemetry(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "", telemetry)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/telemetry?window=15m", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"available":true`)
	require.Equal(t, "15m", telemetry.filter.Window)
	require.Equal(t, "self", telemetry.filter.Scope)
	require.Equal(t, service.TelemetryAudienceUser, telemetry.filter.Audience)
	require.Equal(t, teamID, *telemetry.filter.TeamID)
	require.Equal(t, keyID, *telemetry.filter.ProfileID)
	require.Equal(t, 1, telemetry.calls)

	req = httptest.NewRequest(http.MethodGet, "/ui/api/telemetry?window=30m&scope=team", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, "15m", telemetry.filter.Window)
	require.Equal(t, "self", telemetry.filter.Scope)
	require.Equal(t, keyID, *telemetry.filter.ProfileID)
	require.Equal(t, 1, telemetry.calls)

	req = httptest.NewRequest(http.MethodGet, "/ui/api/telemetry?scope=profile", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, 1, telemetry.calls)
}

func TestUserPortalTelemetryManagerUsesTeamScope(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, keyID, "Manager", []string{"read", "write"})
	authKey.Role = service.APIKeyRoleManager
	telemetry := &controlTelemetrySvc{snapshot: &service.TelemetrySnapshot{
		Available: true,
		Window:    service.TelemetryWindow{Key: "30m"},
		Cards:     []service.TelemetryCard{{ID: "http_requests", Label: "HTTP requests", Unit: "requests", Value: 9}},
	}}
	server := userPortalTestServerWithTelemetry(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "", telemetry)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/telemetry?window=30m", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"available":true`)
	require.Equal(t, "30m", telemetry.filter.Window)
	require.Equal(t, "team", telemetry.filter.Scope)
	require.Equal(t, service.TelemetryAudienceUser, telemetry.filter.Audience)
	require.Equal(t, teamID, *telemetry.filter.TeamID)
	require.Nil(t, telemetry.filter.ProfileID)
	require.Equal(t, 1, telemetry.calls)

	req = httptest.NewRequest(http.MethodGet, "/ui/api/telemetry?window=15m&scope=team", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "15m", telemetry.filter.Window)
	require.Equal(t, "team", telemetry.filter.Scope)
	require.Nil(t, telemetry.filter.ProfileID)
	require.Equal(t, 2, telemetry.calls)

	req = httptest.NewRequest(http.MethodGet, "/ui/api/telemetry?scope=self", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, 2, telemetry.calls)
}

func TestUserPortalGraphUsesAuthenticatedTeamScope(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Reader", []string{"read"})
	graph := &userPortalGraphSvc{}
	server := userPortalTestServerWithGraph(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "", nil, graph)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/graph?scope=local&anchor_type=entity&anchor_id=entity-1&depth=2&limit=50&types=entity,value&q=memory", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"entity:entity-1"`)
	require.Equal(t, 1, graph.calls)
	require.Equal(t, teamID.String(), graph.profileID)
	require.Equal(t, "local", graph.query.Scope)
	require.Equal(t, "entity", graph.query.AnchorType)
	require.Equal(t, "entity-1", graph.query.AnchorID)
	require.Equal(t, 2, graph.query.Depth)
	require.Equal(t, 50, graph.query.Limit)
	require.Equal(t, []string{"entity", "value"}, graph.query.Types)
	require.Equal(t, "memory", graph.query.Query)
}

func TestPublicIPRateLimitFailureDoesNotLogRawError(t *testing.T) {
	e := echo.New()
	var logs bytes.Buffer
	e.Logger.SetOutput(&logs)
	e.GET("/rate-limited", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	}, publicIPRateLimitMiddleware("test", failingPublicRateLimitService{err: errors.New("postgres password=not-safe-to-log")}, &config.Config{RateLimitPerMinute: 1}))

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/rate-limited", nil))

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Contains(t, logs.String(), "public ip rate limit check failed")
	require.NotContains(t, logs.String(), "postgres password=not-safe-to-log")
}

func TestUserPortalGraphRequiresReadScope(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Writer", []string{"write"})
	graph := &userPortalGraphSvc{}
	server := userPortalTestServerWithGraph(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "", nil, graph)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/graph", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, 0, graph.calls)
}

func TestUserPortalGraphNodeDetailUsesAuthenticatedTeamScope(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Reader", []string{"read"})
	graph := &userPortalGraphSvc{}
	server := userPortalTestServerWithGraph(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "", nil, graph)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/node-detail?type=entity&id=entity-1", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"body":"entity detail"`)
	require.Equal(t, 1, graph.detailCalls)
	require.Equal(t, teamID.String(), graph.detailProfileID)
	require.Equal(t, "entity", graph.detailType)
	require.Equal(t, "entity-1", graph.detailID)
}

func TestUserPortalGraphNodeDetailMapsValidationAndNotFound(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Reader", []string{"read"})
	graph := &userPortalGraphSvc{nodeDetailErr: graphview.ErrMissingNode}
	server := userPortalTestServerWithGraph(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "", nil, graph)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/node-detail", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)

	graph.nodeDetailErr = graphview.ErrNodeNotFound
	req = httptest.NewRequest(http.MethodGet, "/ui/api/node-detail?type=entity&id=missing", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec = httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestUserPortalTelemetryReadOnlyForbidden(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Read only", []string{"read"})
	telemetry := &controlTelemetrySvc{snapshot: &service.TelemetrySnapshot{Available: true}}
	server := userPortalTestServerWithTelemetry(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "", telemetry)

	req := httptest.NewRequest(http.MethodGet, "/ui/api/telemetry", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Equal(t, 0, telemetry.calls)
}

func TestUserPortalTelemetryUnavailable(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Mine", []string{"read", "write"})

	noTelemetry := userPortalTestServer(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "")
	req := httptest.NewRequest(http.MethodGet, "/ui/api/telemetry", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec := httptest.NewRecorder()
	noTelemetry.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestUserPortalRotateRequiresWriteScope(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Read only", []string{"read"})
	server := userPortalTestServer(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "")

	req := httptest.NewRequest(http.MethodPost, "/ui/api/key/rotate", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusForbidden, rec.Code)
	require.Contains(t, rec.Body.String(), "insufficient permissions")
}

func TestUserPortalRotateSelfOnlyPreservesMetadata(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, keyID, "Current profile", []string{"read", "write"})
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	authKey.ExpiresAt = &expiresAt
	authKey.RateLimit = 77
	keySvc := &userPortalKeySvc{keys: []*domain.APIKey{authKey}}
	server := userPortalTestServer(t, teamID, authKey, keySvc, "")

	req := httptest.NewRequest(http.MethodPost, "/ui/api/key/rotate", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"api_key":"dm_rotated_plaintext"`)
	require.Equal(t, teamID, keySvc.rotateProfileID)
	require.Equal(t, keyID, keySvc.rotateKeyID)
	require.Equal(t, "Current profile", keySvc.lastRotateReq.Name)
	require.Equal(t, 77, keySvc.lastRotateReq.RateLimit)
	require.Equal(t, expiresAt, *keySvc.lastRotateReq.ExpiresAt)
}

func TestUserPortalRotateRejectsEditableFields(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Current profile", []string{"read", "write"})
	server := userPortalTestServer(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, "")

	req := httptest.NewRequest(http.MethodPost, "/ui/api/key/rotate", strings.NewReader(`{"name":"changed"}`))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Contains(t, rec.Body.String(), "does not accept editable fields")
}

func TestUserPortalCurrentSessionErrors(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	authKey, _ := userPortalTestKey(t, teamID, keyID, "Current profile", []string{"read", "write"})

	h := &userPortalHandler{
		profiles: &controlProfileSvc{},
		keys:     &userPortalKeySvc{keys: []*domain.APIKey{authKey}},
	}
	err := h.session(userPortalEchoContext(t, http.MethodGet, "/ui/api/session", "", nil))
	require.ErrorContains(t, err, "authentication required")

	_, err = h.currentSession(userPortalEchoContext(t, http.MethodGet, "/ui/api/session", "", &httpmw.Principal{
		KeyID:  keyID,
		TeamID: teamID,
		Role:   service.APIKeyRoleMember,
		Scopes: []string{"read"},
	}))
	require.ErrorContains(t, err, "team not found")

	h.profiles = &controlProfileSvc{profiles: []*domain.Profile{{
		ID:        teamID,
		Name:      "Team",
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}}
	h.keys = &userPortalNilKeySvc{err: errors.New("lookup failed")}
	_, err = h.currentSession(userPortalEchoContext(t, http.MethodGet, "/ui/api/session", "", &httpmw.Principal{
		KeyID:  keyID,
		TeamID: teamID,
		Role:   service.APIKeyRoleMember,
		Scopes: []string{"read"},
	}))
	require.ErrorContains(t, err, "lookup failed")

	h.keys = &userPortalNilKeySvc{}
	_, err = h.currentSession(userPortalEchoContext(t, http.MethodGet, "/ui/api/session", "", &httpmw.Principal{
		KeyID:  keyID,
		TeamID: teamID,
		Role:   service.APIKeyRoleMember,
		Scopes: []string{"read"},
	}))
	require.ErrorContains(t, err, "key not found")

	h.keys = &userPortalKeySvc{keys: []*domain.APIKey{authKey}}
	h.appConfig = &controlAppConfigSvc{dreamingRuntimeErr: errors.New("config failed")}
	_, err = h.currentSession(userPortalEchoContext(t, http.MethodGet, "/ui/api/session", "", &httpmw.Principal{
		KeyID:  keyID,
		TeamID: teamID,
		Role:   service.APIKeyRoleMember,
		Scopes: []string{"read"},
	}))
	require.ErrorContains(t, err, "load dreaming runtime config")
}

func TestUserPortalRotateCurrentKeyErrors(t *testing.T) {
	teamID := uuid.New()
	keyID := uuid.New()
	authKey, _ := userPortalTestKey(t, teamID, keyID, "Current profile", []string{"read", "write"})
	h := &userPortalHandler{
		profiles: &controlProfileSvc{},
		keys:     &userPortalKeySvc{keys: []*domain.APIKey{authKey}},
	}

	err := h.rotateCurrentKey(userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "{}", nil))
	require.ErrorContains(t, err, "authentication required")

	err = h.rotateCurrentKey(userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "{}", &httpmw.Principal{
		KeyID:      keyID,
		TeamID:     teamID,
		Role:       service.APIKeyRoleMember,
		Scopes:     []string{"read", "write"},
		AuthMethod: "sso_session",
	}))
	require.ErrorContains(t, err, "sso sessions cannot rotate api keys")

	principal := &httpmw.Principal{
		KeyID:  keyID,
		TeamID: teamID,
		Role:   service.APIKeyRoleMember,
		Scopes: []string{"read", "write"},
	}
	h.keys = &userPortalNilKeySvc{err: errors.New("lookup failed")}
	err = h.rotateCurrentKey(userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "{}", principal))
	require.ErrorContains(t, err, "lookup failed")

	h.keys = &userPortalNilKeySvc{}
	err = h.rotateCurrentKey(userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "{}", principal))
	require.ErrorContains(t, err, "key not found")

	h.keys = &userPortalRotateErrorKeySvc{
		userPortalKeySvc: userPortalKeySvc{keys: []*domain.APIKey{authKey}},
		err:              errors.New("rotate failed"),
	}
	err = h.rotateCurrentKey(userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "{}", principal))
	require.ErrorContains(t, err, "rotate failed")
}

func TestRejectEditableRotateBodyBranches(t *testing.T) {
	c := userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "", nil)
	c.Request().Body = nil
	require.NoError(t, rejectEditableRotateBody(c))

	c = userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", "   ", nil)
	require.NoError(t, rejectEditableRotateBody(c))

	c = userPortalEchoContext(t, http.MethodPost, "/ui/api/key/rotate", `{"name"`, nil)
	require.ErrorContains(t, rejectEditableRotateBody(c), "malformed JSON body")
}

func TestUserPortalStaticDoesNotShadowAPI(t *testing.T) {
	teamID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, uuid.New(), "Mine", []string{"read", "write"})
	staticDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(staticDir, "index.html"), []byte("<!doctype html><title>ui</title>"), 0o644))
	server := userPortalTestServer(t, teamID, authKey, &userPortalKeySvc{keys: []*domain.APIKey{authKey}}, staticDir)

	uiReq := httptest.NewRequest(http.MethodGet, "/ui", nil)
	uiRec := httptest.NewRecorder()
	server.ServeHTTP(uiRec, uiReq)
	require.Equal(t, http.StatusOK, uiRec.Code)
	require.Contains(t, uiRec.Body.String(), "<title>ui</title>")

	apiReq := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	apiReq.Header.Set("Authorization", "Bearer "+rawKey)
	apiRec := httptest.NewRecorder()
	server.ServeHTTP(apiRec, apiReq)
	require.Equal(t, http.StatusOK, apiRec.Code)
	require.Contains(t, apiRec.Body.String(), `"can_rotate":true`)
	require.NotContains(t, apiRec.Body.String(), "<title>ui</title>")
}

func TestRegisterUserPortalRegistersCurrentSurface(t *testing.T) {
	e := echo.New()
	RegisterUserPortal(e, UserPortalDeps{})

	routes := registeredRoutes(e)
	for _, route := range []string{
		"GET /ui/api/team",
		"PATCH /ui/api/team",
		"DELETE /ui/api/team",
		"GET /ui/api/team/profiles",
		"POST /ui/api/team/profiles",
		"GET /ui/api/team/profiles/:keyId",
		"PATCH /ui/api/team/profiles/:keyId",
		"POST /ui/api/team/profiles/:keyId/rotate",
		"DELETE /ui/api/team/profiles/:keyId",
		"GET /ui/api/dreaming/status",
		"GET /ui/api/dreaming/runs",
		"GET /ui/api/dreams",
		"GET /ui/api/dreams/:dreamId",
	} {
		require.True(t, routes[route], "route %q not registered", route)
	}
}

func TestUserPortalSessionAloneAllowsOnlyMissingCredentialsThroughAuth(t *testing.T) {
	e := NewServer(config.Config{}, nil, HealthConfig{})
	RegisterUserPortal(e, UserPortalDeps{})

	req := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"AUTH_MISSING"`)
	require.Contains(t, rec.Body.String(), `"message":"authentication required"`)

	req = httptest.NewRequest(http.MethodGet, "/ui/api/team", nil)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), `"message":"missing authorization header"`)

	req = httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	req.Header.Set("Authorization", "Bearer invalid")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"AUTH_INVALID"`)
}

func TestUserPortalMountedRoutesUseAuthenticatedTeam(t *testing.T) {
	teamID := uuid.New()
	managerID := uuid.New()
	memberID := uuid.New()
	managerKey, rawKey := userPortalTestKey(t, teamID, managerID, "Manager", []string{"read", "write"})
	managerKey.Role = service.APIKeyRoleManager
	memberKey := &domain.APIKey{
		ID:        memberID,
		TeamID:    teamID,
		ProfileID: teamID,
		Name:      "Member",
		Label:     "Member",
		KeySuffix: "member",
		Scopes:    []string{"read"},
		Role:      service.APIKeyRoleMember,
		RateLimit: 120,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}
	profiles := &controlProfileSvc{profiles: []*domain.Profile{{
		ID:          teamID,
		Name:        "Team",
		Description: "Current team",
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
		UpdatedAt:   time.Now().UTC().Truncate(time.Second),
	}}}
	keys := &userPortalKeySvc{keys: []*domain.APIKey{managerKey, memberKey}}
	dreams := &controlDreamServiceStub{dream: &domain.Dream{TeamID: teamID.String(), Status: domain.DreamStatusProposed}}
	e := NewServer(config.Config{
		HTTPMaxBodyBytes:   1048576,
		RateLimitPerMinute: 100,
	}, nil, HealthConfig{})
	RegisterUserPortal(e, UserPortalDeps{
		APIKeyRepo:   &userPortalAuthRepo{key: managerKey},
		ProfileSvc:   profiles,
		APIKeySvc:    keys,
		RateLimitSvc: service.NewRateLimitService(inmem.NewInMemoryRateLimitStore()),
		DreamSvc:     dreams,
		Config:       &config.Config{RateLimitPerMinute: 100},
	})

	for _, tt := range []struct {
		method string
		path   string
		want   string
	}{
		{method: http.MethodGet, path: "/ui/api/team", want: `"name":"Team"`},
		{method: http.MethodGet, path: "/ui/api/team/profiles", want: `"name":"Member"`},
		{method: http.MethodGet, path: "/ui/api/team/profiles/" + memberID.String(), want: `"name":"Member"`},
		{method: http.MethodGet, path: "/ui/api/dreams/dream-1", want: `"dream_id":"dream-1"`},
	} {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		req.Header.Set("Authorization", "Bearer "+rawKey)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, tt.path)
		require.Contains(t, rec.Body.String(), tt.want)
	}

	req := httptest.NewRequest(http.MethodPost, "/ui/api/team/profiles", strings.NewReader(`{"name":"Created","scopes":["read"],"rate_limit":60}`))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)
	require.Contains(t, rec.Body.String(), `"api_key":"dm_created_plaintext"`)
	require.Equal(t, service.APIKeyRoleMember, keys.lastCreateReq.Role)

	req = httptest.NewRequest(http.MethodPatch, "/ui/api/team/profiles/"+memberID.String(), strings.NewReader(`{"scopes":["read","write"]}`))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"scopes":["read","write"]`)

	req = httptest.NewRequest(http.MethodPost, "/ui/api/team/profiles/"+memberID.String()+"/rotate", strings.NewReader(`{"name":"Member rotated","rate_limit":90}`))
	req.Header.Set("Authorization", "Bearer "+rawKey)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"api_key":"dm_rotated_plaintext"`)
	require.Equal(t, teamID, keys.rotateProfileID)
	require.Equal(t, memberID, keys.rotateKeyID)

	req = httptest.NewRequest(http.MethodDelete, "/ui/api/team/profiles/"+memberID.String(), nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, teamID, keys.deleteProfileID)
	require.Equal(t, memberID, keys.deleteKeyID)

	req = httptest.NewRequest(http.MethodDelete, "/ui/api/team", nil)
	req.Header.Set("Authorization", "Bearer "+rawKey)
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, teamID, profiles.deleted)
	require.Equal(t, teamID.String(), dreams.profileID)
}

func TestUserPortalMissingRouteServicesReturnUnavailable(t *testing.T) {
	teamID := uuid.New()
	managerID := uuid.New()
	authKey, rawKey := userPortalTestKey(t, teamID, managerID, "Manager", []string{"read", "write"})
	authKey.Role = service.APIKeyRoleManager
	cfg := &config.Config{
		HTTPMaxBodyBytes:   1048576,
		RateLimitPerMinute: 100,
	}
	e := NewServer(*cfg, nil, HealthConfig{})
	RegisterUserPortal(e, UserPortalDeps{
		APIKeyRepo:   &userPortalAuthRepo{key: authKey},
		RateLimitSvc: service.NewRateLimitService(inmem.NewInMemoryRateLimitStore()),
		Config:       cfg,
	})

	for _, tt := range []struct {
		path string
		want string
	}{
		{path: "/ui/api/session", want: "profile service unavailable"},
		{path: "/ui/api/team", want: "profile service unavailable"},
		{path: "/ui/api/team/profiles/" + uuid.NewString(), want: "api key service unavailable"},
		{path: "/ui/api/dreams/dream-1", want: "dream service unavailable"},
	} {
		req := httptest.NewRequest(http.MethodGet, tt.path, nil)
		req.Header.Set("Authorization", "Bearer "+rawKey)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code, tt.path)
		require.Contains(t, rec.Body.String(), tt.want)
	}
}

func userPortalTestKey(t *testing.T, teamID, keyID uuid.UUID, name string, scopes []string) (*domain.APIKey, string) {
	t.Helper()
	rawKey, err := crypto.GenerateRawKeyForProfile(name)
	require.NoError(t, err)
	keyHash, err := crypto.HashKey(rawKey)
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	return &domain.APIKey{
		ID:        keyID,
		TeamID:    teamID,
		ProfileID: teamID,
		Name:      name,
		Label:     name,
		KeyHash:   keyHash,
		KeyPrefix: crypto.GetKeyPrefix(rawKey),
		KeySuffix: crypto.GetKeySuffix(rawKey),
		Scopes:    scopes,
		RateLimit: 120,
		CreatedAt: now,
	}, rawKey
}

func userPortalTestServer(t *testing.T, teamID uuid.UUID, authKey *domain.APIKey, keySvc *userPortalKeySvc, staticDir string) http.Handler {
	return userPortalTestServerWithTelemetry(t, teamID, authKey, keySvc, staticDir, nil)
}

func userPortalTestServerWithTelemetry(t *testing.T, teamID uuid.UUID, authKey *domain.APIKey, keySvc *userPortalKeySvc, staticDir string, telemetry service.TelemetryReader) http.Handler {
	return userPortalTestServerWithGraph(t, teamID, authKey, keySvc, staticDir, telemetry, nil)
}

func userPortalTestServerWithGraph(t *testing.T, teamID uuid.UUID, authKey *domain.APIKey, keySvc *userPortalKeySvc, staticDir string, telemetry service.TelemetryReader, graph graphview.Service) http.Handler {
	t.Helper()
	profiles := &controlProfileSvc{profiles: []*domain.Profile{{
		ID:        teamID,
		Name:      "Team",
		Config:    map[string]any{"dreaming": map[string]any{"enabled": true}, "retention": "standard"},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}}
	e := NewServer(config.Config{
		HTTPMaxBodyBytes:   1048576,
		RateLimitPerMinute: 100,
	}, nil, HealthConfig{})
	RegisterUserPortal(e, UserPortalDeps{
		APIKeyRepo:    &userPortalAuthRepo{key: authKey},
		ProfileSvc:    profiles,
		APIKeySvc:     keySvc,
		RateLimitSvc:  service.NewRateLimitService(inmem.NewInMemoryRateLimitStore()),
		Telemetry:     telemetry,
		GraphView:     graph,
		Config:        &config.Config{RateLimitPerMinute: 100},
		UserStaticDir: staticDir,
	})
	return e
}

func userPortalEchoContext(t *testing.T, method, target, body string, principal *httpmw.Principal) echo.Context {
	t.Helper()

	e := echo.New()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if principal != nil {
		req = req.WithContext(httpmw.SetPrincipalForTest(req.Context(), principal))
	}
	rec := httptest.NewRecorder()
	return e.NewContext(req, rec)
}
