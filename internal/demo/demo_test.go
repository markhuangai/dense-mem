package demo

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service"
)

type fakeCounterStore struct {
	values map[string]int64
}

func (s *fakeCounterStore) IncrWithExpire(ctx context.Context, key string, expireSeconds int64) (int64, error) {
	return s.AddWithExpire(ctx, key, 1, expireSeconds)
}

func (s *fakeCounterStore) AddWithExpire(_ context.Context, key string, delta int64, _ int64) (int64, error) {
	if s.values == nil {
		s.values = make(map[string]int64)
	}
	s.values[key] += delta
	return s.values[key], nil
}

func TestQuotaManagerEnforcesRequestAndByteCaps(t *testing.T) {
	ctx := context.Background()
	quotas := DefaultQuotas()
	quotas.TotalRequests = 2
	quotas.FragmentBytes = 5
	manager := NewQuotaManager(&fakeCounterStore{}, quotas)

	require.NoError(t, manager.ConsumeRequest(ctx, "team-1"))
	require.NoError(t, manager.ConsumeRequest(ctx, "team-1"))
	err := manager.ConsumeRequest(ctx, "team-1")
	requireAPIErrorCode(t, err, httperr.RATE_LIMITED)

	require.NoError(t, manager.ConsumeFragment(ctx, "team-1", "abc"))
	err = manager.ConsumeFragment(ctx, "team-1", "def")
	requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
}

func TestProvisionCreatesDemoTeamAndExpiringKey(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	profiles := &fakeProfileService{}
	keys := &fakeAPIKeyService{}
	provisioner := NewProvisioner(profiles, keys, &fakeCounterStore{}, DefaultQuotas())
	provisioner.now = func() time.Time { return now }

	resp, err := provisioner.Provision(ctx, ProvisionOptions{
		ClientIP: "203.0.113.10",
		BaseURL:  "https://demo-dense-mem.markhuang.ai/",
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.APIKey)
	require.Equal(t, "https://demo-dense-mem.markhuang.ai/mcp", resp.MCPURL)
	require.Equal(t, "https://demo-dense-mem.markhuang.ai/ui", resp.UIURL)
	require.Equal(t, now.Add(24*time.Hour), resp.ExpiresAt)

	require.Len(t, profiles.created, 1)
	created := profiles.created[0]
	require.Equal(t, true, created.Metadata["demo"])
	require.Equal(t, now.Add(24*time.Hour).Format(time.RFC3339), created.Metadata["demo_expires_at"])

	require.Equal(t, created.ID, keys.profileID)
	require.Equal(t, 20, keys.req.RateLimit)
	require.NotNil(t, keys.req.ExpiresAt)
	require.Equal(t, now.Add(24*time.Hour), *keys.req.ExpiresAt)
	require.Equal(t, []string{"read", "write"}, keys.req.Scopes)
}

func TestProvisionEnforcesIssueQuota(t *testing.T) {
	ctx := context.Background()
	quotas := DefaultQuotas()
	quotas.IssuePerIPDay = 1
	provisioner := NewProvisioner(&fakeProfileService{}, &fakeAPIKeyService{}, &fakeCounterStore{}, quotas)
	provisioner.now = func() time.Time { return time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC) }

	_, err := provisioner.Provision(ctx, ProvisionOptions{ClientIP: "203.0.113.10", BaseURL: "https://demo.example"})
	require.NoError(t, err)

	_, err = provisioner.Provision(ctx, ProvisionOptions{ClientIP: "203.0.113.10", BaseURL: "https://demo.example"})
	requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
}

func TestProvisionReturnsUnavailableWhenIssueQuotaStoreFails(t *testing.T) {
	ctx := context.Background()
	provisioner := NewProvisioner(&fakeProfileService{}, &fakeAPIKeyService{}, failingCounterStore{}, DefaultQuotas())

	_, err := provisioner.Provision(ctx, ProvisionOptions{ClientIP: "203.0.113.10", BaseURL: "https://demo.example"})

	requireAPIErrorCode(t, err, httperr.SERVICE_UNAVAILABLE)
}

func TestProvisionRollbackWhenKeyCreateFails(t *testing.T) {
	ctx := context.Background()
	profiles := &fakeProfileService{}
	keys := &fakeAPIKeyService{createErr: errors.New("key create failed")}
	provisioner := NewProvisioner(profiles, keys, &fakeCounterStore{}, DefaultQuotas())

	_, err := provisioner.Provision(ctx, ProvisionOptions{ClientIP: "203.0.113.10", BaseURL: "https://demo.example"})

	require.Error(t, err)
	require.Len(t, profiles.created, 1)
	require.Equal(t, []uuid.UUID{profiles.created[0].ID}, profiles.deleted)
}

func TestProvisionRejectsUnavailableDependencies(t *testing.T) {
	ctx := context.Background()

	_, err := (*Provisioner)(nil).Provision(ctx, ProvisionOptions{})
	requireAPIErrorCode(t, err, httperr.SERVICE_UNAVAILABLE)

	_, err = NewProvisioner(nil, &fakeAPIKeyService{}, &fakeCounterStore{}, DefaultQuotas()).Provision(ctx, ProvisionOptions{})
	requireAPIErrorCode(t, err, httperr.SERVICE_UNAVAILABLE)
}

func TestRequestBaseURLUsesRequestHostWhenNotConfigured(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/demo/api/session", nil)
	req.Host = "127.0.0.1:19090"
	rec := httptest.NewRecorder()

	got := requestBaseURL(e.NewContext(req, rec), "")

	require.Equal(t, "http://127.0.0.1:19090", got)
}

func TestRequestBaseURLUsesForwardedHeaders(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/demo/api/session", nil)
	req.Host = "internal:8080"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "demo-dense-mem.markhuang.ai")
	rec := httptest.NewRecorder()

	got := requestBaseURL(e.NewContext(req, rec), "")

	require.Equal(t, "https://demo-dense-mem.markhuang.ai", got)
}

func TestRequestBaseURLConfiguredOverrideWins(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/demo/api/session", nil)
	req.Host = "127.0.0.1:19090"
	rec := httptest.NewRecorder()

	got := requestBaseURL(e.NewContext(req, rec), "https://demo.example/")

	require.Equal(t, "https://demo.example", got)
}

func TestRequestBaseURLFallsBackToLocalhost(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/demo/api/session", nil)
	req.Host = ""
	rec := httptest.NewRecorder()

	got := requestBaseURL(e.NewContext(req, rec), "")

	require.Equal(t, "http://localhost", got)
	require.Equal(t, "https", firstHeader(" https, http "))
}

func TestProvisionHelpersUseFallbacks(t *testing.T) {
	emptyHash := hashClientIP("")
	unknownHash := hashClientIP("unknown")
	require.Equal(t, unknownHash, emptyHash)

	token, err := randomHex(0)
	require.NoError(t, err)
	require.Len(t, token, 8)
}

func TestRepositoryRejectsUnavailableStorage(t *testing.T) {
	_, err := NewRepository(nil, nil).ExpiredTeamIDs(context.Background(), time.Now(), 0)
	require.ErrorContains(t, err, "demo cleanup repository unavailable")
}

func TestRepositoryExpiredTeamIDsQueriesExpiredDemoTeams(t *testing.T) {
	db, mock, cleanup := newDemoMockDB(t)
	defer cleanup()

	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	id1 := uuid.New()
	id2 := uuid.New()
	mock.ExpectQuery(`(?s)SELECT id\s+FROM teams\s+WHERE deleted_at IS NULL.*metadata @>.*metadata->>'demo_expires_at' IS NOT NULL.*LIMIT \$2`).
		WithArgs(now.UTC(), defaultCleanupBatchSize).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(id1.String()).AddRow(id2.String()))

	ids, err := NewRepository(db, directSystemRLS{}).ExpiredTeamIDs(context.Background(), now, 0)

	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{id1, id2}, ids)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCleanerDeletesExpiredTeamsThroughProfileService(t *testing.T) {
	ctx := context.Background()
	id1 := uuid.New()
	id2 := uuid.New()
	repo := &fakeExpiredRepo{ids: []uuid.UUID{id1, id2}}
	profiles := &fakeProfileService{
		deleteErr: map[uuid.UUID]error{
			id2: httperr.New(httperr.NOT_FOUND, "already deleted"),
		},
	}
	cleaner := NewCleaner(repo, profiles, time.Minute)
	cleaner.now = func() time.Time { return time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC) }

	err := cleaner.PurgeExpired(ctx)
	require.NoError(t, err)
	require.Equal(t, []uuid.UUID{id1, id2}, profiles.deleted)
	require.Equal(t, cleaner.now(), repo.now)
	require.Equal(t, defaultCleanupBatchSize, repo.limit)
}

func TestCleanerSkipsPostgresDeleteWhenPrePurgeFails(t *testing.T) {
	ctx := context.Background()
	id1 := uuid.New()
	id2 := uuid.New()
	repo := &fakeExpiredRepo{ids: []uuid.UUID{id1, id2}}
	profiles := &fakeProfileService{}
	purger := &fakeDataPurger{
		err: map[string]error{
			id1.String(): errBoom{},
		},
	}
	cleaner := NewCleanerWithDataPurger(repo, profiles, purger, time.Minute)

	err := cleaner.PurgeExpired(ctx)
	require.Error(t, err)
	require.Equal(t, []string{id1.String(), id2.String()}, purger.purged)
	require.Equal(t, []uuid.UUID{id2}, profiles.deleted)
}

func TestCleanerReturnsRepositoryError(t *testing.T) {
	err := NewCleaner(
		&fakeExpiredRepo{err: errors.New("repository failed")},
		&fakeProfileService{},
		time.Minute,
	).PurgeExpired(context.Background())

	require.ErrorContains(t, err, "repository failed")
}

func TestCleanerReturnsDeleteError(t *testing.T) {
	id := uuid.New()
	err := NewCleaner(
		&fakeExpiredRepo{ids: []uuid.UUID{id}},
		&fakeProfileService{deleteErr: map[uuid.UUID]error{id: errors.New("delete failed")}},
		time.Minute,
	).PurgeExpired(context.Background())

	require.ErrorContains(t, err, "delete expired demo team")
	require.ErrorContains(t, err, "delete failed")
}

func TestCleanerDefaultsAndUnavailable(t *testing.T) {
	cleaner := NewCleaner(nil, nil, 0)
	require.Equal(t, 10*time.Minute, cleaner.interval)

	err := cleaner.PurgeExpired(context.Background())
	require.ErrorContains(t, err, "demo cleaner unavailable")
}

func requireAPIErrorCode(t *testing.T, err error, code httperr.ErrorCode) {
	t.Helper()
	require.Error(t, err)
	var apiErr *httperr.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, code, apiErr.Code)
}

type fakeProfileService struct {
	created   []*domain.Profile
	deleted   []uuid.UUID
	deleteErr map[uuid.UUID]error
}

func (s *fakeProfileService) Create(ctx context.Context, req service.CreateProfileRequest, actorKeyID *string, actorRole, clientIP, correlationID string) (*domain.Profile, error) {
	profile := &domain.Profile{
		ID:          uuid.New(),
		Name:        req.Name,
		Description: req.Description,
		Metadata:    req.Metadata,
		Config:      req.Config,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}
	s.created = append(s.created, profile)
	return profile, nil
}

func (s *fakeProfileService) Get(ctx context.Context, id uuid.UUID) (*domain.Profile, error) {
	return s.GetByID(ctx, id)
}

func (s *fakeProfileService) GetByID(_ context.Context, id uuid.UUID) (*domain.Profile, error) {
	for _, profile := range s.created {
		if profile.ID == id {
			return profile, nil
		}
	}
	return &domain.Profile{ID: id, Name: "existing"}, nil
}

func (s *fakeProfileService) List(context.Context, int, int) ([]*domain.Profile, error) {
	return s.created, nil
}

func (s *fakeProfileService) Count(context.Context) (int64, error) {
	return int64(len(s.created)), nil
}

func (s *fakeProfileService) Update(context.Context, uuid.UUID, service.UpdateProfileRequest, *string, string, string, string) (*domain.Profile, error) {
	return nil, nil
}

func (s *fakeProfileService) Delete(_ context.Context, id uuid.UUID, _ *string, _, _, _ string) error {
	s.deleted = append(s.deleted, id)
	if s.deleteErr != nil {
		return s.deleteErr[id]
	}
	return nil
}

type fakeAPIKeyService struct {
	profileID uuid.UUID
	req       service.CreateAPIKeyRequest
	createErr error
}

func (s *fakeAPIKeyService) CreateStandardKey(_ context.Context, profileID uuid.UUID, req service.CreateAPIKeyRequest, _ *string, _, _, _ string) (*domain.APIKey, string, error) {
	if s.createErr != nil {
		return nil, "", s.createErr
	}
	s.profileID = profileID
	s.req = req
	key := &domain.APIKey{
		ID:        uuid.New(),
		ProfileID: profileID,
		TeamID:    profileID,
		Name:      req.Name,
		Label:     req.Name,
		Scopes:    req.Scopes,
		RateLimit: req.RateLimit,
		ExpiresAt: req.ExpiresAt,
	}
	return key, "dm_demo_raw_key", nil
}

func (s *fakeAPIKeyService) ListByProfile(context.Context, uuid.UUID, int, int) ([]*domain.APIKey, error) {
	return nil, nil
}

func (s *fakeAPIKeyService) CountByProfile(context.Context, uuid.UUID) (int64, error) {
	return 0, nil
}

func (s *fakeAPIKeyService) GetByIDForProfile(context.Context, uuid.UUID, uuid.UUID) (*domain.APIKey, error) {
	return nil, nil
}

func (s *fakeAPIKeyService) RevokeForProfile(context.Context, uuid.UUID, uuid.UUID, *string, string, string, string) error {
	return nil
}

func (s *fakeAPIKeyService) DeleteForProfile(context.Context, uuid.UUID, uuid.UUID, *string, string, string, string) error {
	return nil
}

func (s *fakeAPIKeyService) UpdateNameForProfile(context.Context, uuid.UUID, uuid.UUID, string, *string, string, string, string) (*domain.APIKey, error) {
	return nil, nil
}

func (s *fakeAPIKeyService) UpdateRoleForProfile(context.Context, uuid.UUID, uuid.UUID, string, *string, string, string, string) (*domain.APIKey, error) {
	return nil, nil
}

func (s *fakeAPIKeyService) RotateForProfile(context.Context, uuid.UUID, uuid.UUID, service.CreateAPIKeyRequest, *string, string, string, string) (*domain.APIKey, string, error) {
	return nil, "", nil
}

type fakeExpiredRepo struct {
	ids   []uuid.UUID
	now   time.Time
	limit int
	err   error
}

func (r *fakeExpiredRepo) ExpiredTeamIDs(_ context.Context, now time.Time, limit int) ([]uuid.UUID, error) {
	r.now = now
	r.limit = limit
	if r.err != nil {
		return nil, r.err
	}
	return r.ids, nil
}

type fakeDataPurger struct {
	purged []string
	err    map[string]error
}

func (p *fakeDataPurger) PurgeProfileData(_ context.Context, profileID string) error {
	p.purged = append(p.purged, profileID)
	return p.err[profileID]
}

type errBoom struct{}

func (errBoom) Error() string { return "boom" }

func newDemoMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)

	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{
		Conn:                 sqlDB,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: true,
	})
	require.NoError(t, err)

	return db, mock, func() {
		_ = sqlDB.Close()
	}
}

type directSystemRLS struct{}

func (directSystemRLS) WithProfileTx(ctx context.Context, db *gorm.DB, _ string, fn func(tx *gorm.DB) error) error {
	return fn(db.WithContext(ctx))
}

func (directSystemRLS) WithTeamTx(ctx context.Context, db *gorm.DB, _ string, fn func(tx *gorm.DB) error) error {
	return fn(db.WithContext(ctx))
}

func (directSystemRLS) WithSystemTx(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	return fn(db.WithContext(ctx))
}
