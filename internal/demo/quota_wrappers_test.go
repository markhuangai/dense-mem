package demo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/dto"
	httpmw "github.com/markhuangai/dense-mem/internal/http/middleware"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

func TestQuotaManagerConsumesNamedCaps(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name    string
		quotas  func() Quotas
		consume func(*QuotaManager, string) error
	}{
		{
			name: "writes",
			quotas: func() Quotas {
				q := DefaultQuotas()
				q.WriteAttempts = 1
				return q
			},
			consume: func(m *QuotaManager, teamID string) error {
				return m.ConsumeWrite(ctx, teamID)
			},
		},
		{
			name: "claims",
			quotas: func() Quotas {
				q := DefaultQuotas()
				q.CreatedClaims = 1
				return q
			},
			consume: func(m *QuotaManager, teamID string) error {
				return m.ConsumeClaim(ctx, teamID)
			},
		},
		{
			name: "verifier",
			quotas: func() Quotas {
				q := DefaultQuotas()
				q.VerifierAttempts = 1
				return q
			},
			consume: func(m *QuotaManager, teamID string) error {
				return m.ConsumeVerifier(ctx, teamID)
			},
		},
		{
			name: "facts",
			quotas: func() Quotas {
				q := DefaultQuotas()
				q.PromotedFacts = 1
				return q
			},
			consume: func(m *QuotaManager, teamID string) error {
				return m.ConsumeFact(ctx, teamID)
			},
		},
		{
			name: "recall",
			quotas: func() Quotas {
				q := DefaultQuotas()
				q.RecallCalls = 1
				return q
			},
			consume: func(m *QuotaManager, teamID string) error {
				return m.ConsumeRecall(ctx, teamID)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewQuotaManager(&fakeCounterStore{}, tc.quotas())
			teamID := uuid.NewString()

			require.NoError(t, tc.consume(manager, teamID))
			requireAPIErrorCode(t, tc.consume(manager, teamID), httperr.RATE_LIMITED)
		})
	}
}

func TestQuotaManagerRejectsMissingStoreAndTeam(t *testing.T) {
	ctx := context.Background()

	requireAPIErrorCode(t, NewQuotaManager(nil, DefaultQuotas()).ConsumeRequest(ctx, "team-1"), httperr.SERVICE_UNAVAILABLE)
	requireAPIErrorCode(t, NewQuotaManager(&fakeCounterStore{}, DefaultQuotas()).ConsumeRequest(ctx, ""), httperr.FORBIDDEN)
	requireAPIErrorCode(t, NewQuotaManager(failingCounterStore{}, DefaultQuotas()).ConsumeRequest(ctx, "team-1"), httperr.SERVICE_UNAVAILABLE)
	require.Equal(t, int64(300), (*QuotaManager)(nil).Quotas().TotalRequests)
	require.Equal(t, int64(300), (*QuotaManager)(nil).Limits().TotalRequests)
}

func TestQuotaManagerNormalizesZeroQuotas(t *testing.T) {
	quotas := NewQuotaManager(&fakeCounterStore{}, Quotas{}).Quotas()

	require.Equal(t, 24*time.Hour, quotas.SessionTTL)
	require.Equal(t, 25*time.Hour, quotas.CounterTTL)
	require.Equal(t, int64(300), quotas.TotalRequests)
	require.Equal(t, int64(75), quotas.WriteAttempts)
	require.Equal(t, int64(30), quotas.FragmentAttempts)
	require.Equal(t, int64(128*1024), quotas.FragmentBytes)
	require.Equal(t, int64(30), quotas.CreatedClaims)
	require.Equal(t, int64(10), quotas.VerifierAttempts)
	require.Equal(t, int64(5), quotas.PromotedFacts)
	require.Equal(t, int64(50), quotas.RecallCalls)
	require.Equal(t, 20, quotas.PerMinuteRequests)
	require.Equal(t, int64(10), quotas.IssuePerIPDay)
}

func TestRequestQuotaMiddlewareConsumesResolvedTeamOrPrincipalTeam(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func(uuid.UUID) context.Context
	}{
		{
			name: "resolved team",
			ctx: func(teamID uuid.UUID) context.Context {
				return httpmw.SetResolvedProfileIDForTest(context.Background(), teamID)
			},
		},
		{
			name: "principal fallback",
			ctx: func(teamID uuid.UUID) context.Context {
				return httpmw.SetPrincipalForTest(context.Background(), &httpmw.Principal{TeamID: teamID})
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := DefaultQuotas()
			q.TotalRequests = 1
			manager := NewQuotaManager(&fakeCounterStore{}, q)
			e := echo.New()
			e.HTTPErrorHandler = httperr.ErrorHandler
			e.Use(RequestQuotaMiddleware(manager))
			e.GET("/", func(c echo.Context) error {
				return c.NoContent(http.StatusNoContent)
			})
			teamID := uuid.New()

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(tc.ctx(teamID))
			e.ServeHTTP(rec, req)
			require.Equal(t, http.StatusNoContent, rec.Code)

			rec = httptest.NewRecorder()
			req = httptest.NewRequest(http.MethodGet, "/", nil).WithContext(tc.ctx(teamID))
			e.ServeHTTP(rec, req)
			require.Equal(t, http.StatusTooManyRequests, rec.Code)
		})
	}
}

func TestRequestQuotaMiddlewarePassesThroughWithoutTeam(t *testing.T) {
	q := DefaultQuotas()
	q.TotalRequests = 0
	manager := NewQuotaManager(&fakeCounterStore{}, q)
	e := echo.New()
	e.Use(RequestQuotaMiddleware(manager))
	e.GET("/", func(c echo.Context) error {
		return c.NoContent(http.StatusNoContent)
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
}

func TestRegisterRoutesServesLandingAndSession(t *testing.T) {
	e := echo.New()
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	provisioner := NewProvisioner(&fakeProfileService{}, &fakeAPIKeyService{}, &fakeCounterStore{}, DefaultQuotas())
	provisioner.now = func() time.Time { return now }
	RegisterRoutes(e, provisioner, "")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "<title>Dense-Mem Demo</title>")

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/demo/api/session", nil)
	req.Host = "127.0.0.1:19090"
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	var body ProvisionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.NotEmpty(t, body.APIKey)
	require.Equal(t, "http://127.0.0.1:19090/mcp", body.MCPURL)
	require.Equal(t, "http://127.0.0.1:19090/ui", body.UIURL)
}

func TestRegisterRoutesUsesConfiguredBaseURL(t *testing.T) {
	e := echo.New()
	provisioner := NewProvisioner(&fakeProfileService{}, &fakeAPIKeyService{}, &fakeCounterStore{}, DefaultQuotas())
	RegisterRoutes(e, provisioner, "https://configured.example/")

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/demo/api/session", nil)
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusCreated, rec.Code)

	var body ProvisionResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "https://configured.example/mcp", body.MCPURL)
}

func TestCleanerStartRunsInitialPurgeAndStops(t *testing.T) {
	ctx := context.Background()
	id := uuid.New()
	repo := &fakeExpiredRepo{ids: []uuid.UUID{id}}
	profiles := &fakeProfileService{}
	cleaner := NewCleaner(repo, profiles, time.Hour)

	stop := cleaner.Start(ctx)
	require.Eventually(t, func() bool {
		return len(profiles.deleted) == 1
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, stop(ctx))
	require.NoError(t, stop(ctx))
}

func TestQuotaManagerConsumeFragmentEmptyContentSkipsByteCounter(t *testing.T) {
	store := &fakeCounterStore{}
	q := DefaultQuotas()
	q.FragmentAttempts = 2
	q.FragmentBytes = 1
	manager := NewQuotaManager(store, q)

	require.NoError(t, manager.ConsumeFragment(context.Background(), "team-1", ""))

	require.Equal(t, int64(1), store.values[quotaKey("team-1", "fragments")])
	require.Zero(t, store.values[quotaKey("team-1", "fragment_bytes")])
}

func TestQuotaWrappersConsumeAndDelegate(t *testing.T) {
	ctx := context.Background()
	teamID := "team-1"

	t.Run("fragment create", func(t *testing.T) {
		q := DefaultQuotas()
		q.WriteAttempts = 1
		q.FragmentAttempts = 1
		q.FragmentBytes = 4
		next := &fakeFragmentCreate{}
		wrapped := WrapFragmentCreate(next, NewQuotaManager(&fakeCounterStore{}, q))

		result, err := wrapped.Create(ctx, teamID, &dto.CreateFragmentRequest{Content: "demo"})
		require.NoError(t, err)
		require.Equal(t, "fragment-1", result.Fragment.FragmentID)
		require.Equal(t, 1, next.calls)

		_, err = wrapped.Create(ctx, teamID, &dto.CreateFragmentRequest{Content: "x"})
		requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
		require.Equal(t, 1, next.calls)
	})

	t.Run("claim create", func(t *testing.T) {
		q := DefaultQuotas()
		q.WriteAttempts = 1
		q.CreatedClaims = 1
		next := &fakeClaimCreate{}
		wrapped := WrapClaimCreate(next, NewQuotaManager(&fakeCounterStore{}, q))

		result, err := wrapped.Create(ctx, teamID, &domain.Claim{ClaimID: "claim-1"})
		require.NoError(t, err)
		require.Equal(t, "claim-1", result.Claim.ClaimID)
		require.Equal(t, 1, next.calls)

		_, err = wrapped.Create(ctx, teamID, &domain.Claim{ClaimID: "claim-2"})
		requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
		require.Equal(t, 1, next.calls)
	})

	t.Run("claim verify", func(t *testing.T) {
		q := DefaultQuotas()
		q.WriteAttempts = 1
		q.VerifierAttempts = 1
		next := &fakeClaimVerify{}
		wrapped := WrapClaimVerify(next, NewQuotaManager(&fakeCounterStore{}, q))

		claim, err := wrapped.Verify(ctx, teamID, "claim-1")
		require.NoError(t, err)
		require.Equal(t, "claim-1", claim.ClaimID)
		require.Equal(t, 1, next.calls)

		_, err = wrapped.Verify(ctx, teamID, "claim-2")
		requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
		require.Equal(t, 1, next.calls)
	})

	t.Run("fact promote", func(t *testing.T) {
		q := DefaultQuotas()
		q.WriteAttempts = 1
		q.PromotedFacts = 1
		next := &fakeFactPromote{}
		wrapped := WrapFactPromote(next, NewQuotaManager(&fakeCounterStore{}, q))

		fact, err := wrapped.Promote(ctx, teamID, "claim-1")
		require.NoError(t, err)
		require.Equal(t, "fact-1", fact.FactID)
		require.Equal(t, 1, next.calls)

		_, err = wrapped.Promote(ctx, teamID, "claim-2")
		requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
		require.Equal(t, 1, next.calls)
	})

	t.Run("fact confirm", func(t *testing.T) {
		q := DefaultQuotas()
		q.WriteAttempts = 1
		q.PromotedFacts = 1
		next := &fakeFactConfirm{}
		wrapped := WrapFactConfirm(next, NewQuotaManager(&fakeCounterStore{}, q))

		result, err := wrapped.ConfirmMemory(ctx, teamID, factservice.ConfirmMemoryRequest{ClaimID: "claim-1"})
		require.NoError(t, err)
		require.Equal(t, "accepted", result.Decision)
		require.Equal(t, 1, next.calls)

		_, err = wrapped.ConfirmMemory(ctx, teamID, factservice.ConfirmMemoryRequest{ClaimID: "claim-2"})
		requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
		require.Equal(t, 1, next.calls)
	})

	t.Run("recall", func(t *testing.T) {
		q := DefaultQuotas()
		q.RecallCalls = 1
		next := &fakeRecall{}
		wrapped := WrapRecall(next, NewQuotaManager(&fakeCounterStore{}, q))

		hits, err := wrapped.Recall(ctx, teamID, recallservice.RecallRequest{Query: "demo"})
		require.NoError(t, err)
		require.Len(t, hits, 1)
		require.Equal(t, 1, next.calls)

		_, err = wrapped.Recall(ctx, teamID, recallservice.RecallRequest{Query: "demo"})
		requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
		require.Equal(t, 1, next.calls)
	})
}

func TestQuotaWrappersStopOnSpecificQuotaCaps(t *testing.T) {
	ctx := context.Background()
	teamID := "team-1"

	t.Run("claim cap", func(t *testing.T) {
		q := DefaultQuotas()
		q.WriteAttempts = 10
		q.CreatedClaims = 1
		next := &fakeClaimCreate{}
		wrapped := WrapClaimCreate(next, NewQuotaManager(&fakeCounterStore{}, q))

		_, err := wrapped.Create(ctx, teamID, &domain.Claim{ClaimID: "claim-1"})
		require.NoError(t, err)
		_, err = wrapped.Create(ctx, teamID, &domain.Claim{ClaimID: "claim-2"})
		requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
		require.Equal(t, 1, next.calls)
	})

	t.Run("verifier cap", func(t *testing.T) {
		q := DefaultQuotas()
		q.WriteAttempts = 10
		q.VerifierAttempts = 1
		next := &fakeClaimVerify{}
		wrapped := WrapClaimVerify(next, NewQuotaManager(&fakeCounterStore{}, q))

		_, err := wrapped.Verify(ctx, teamID, "claim-1")
		require.NoError(t, err)
		_, err = wrapped.Verify(ctx, teamID, "claim-2")
		requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
		require.Equal(t, 1, next.calls)
	})

	t.Run("fact cap", func(t *testing.T) {
		q := DefaultQuotas()
		q.WriteAttempts = 10
		q.PromotedFacts = 1
		next := &fakeFactPromote{}
		wrapped := WrapFactPromote(next, NewQuotaManager(&fakeCounterStore{}, q))

		_, err := wrapped.Promote(ctx, teamID, "claim-1")
		require.NoError(t, err)
		_, err = wrapped.Promote(ctx, teamID, "claim-2")
		requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
		require.Equal(t, 1, next.calls)
	})

	t.Run("confirm cap", func(t *testing.T) {
		q := DefaultQuotas()
		q.WriteAttempts = 10
		q.PromotedFacts = 1
		next := &fakeFactConfirm{}
		wrapped := WrapFactConfirm(next, NewQuotaManager(&fakeCounterStore{}, q))

		_, err := wrapped.ConfirmMemory(ctx, teamID, factservice.ConfirmMemoryRequest{ClaimID: "claim-1"})
		require.NoError(t, err)
		_, err = wrapped.ConfirmMemory(ctx, teamID, factservice.ConfirmMemoryRequest{ClaimID: "claim-2"})
		requireAPIErrorCode(t, err, httperr.RATE_LIMITED)
		require.Equal(t, 1, next.calls)
	})
}

func TestDisabledCommunityDetectService(t *testing.T) {
	err := DisabledCommunityDetectService{}.Detect(context.Background(), "team-1", communityservice.DetectOptions{})
	require.ErrorIs(t, err, communityservice.ErrCommunityUnavailable)
}

type fakeFragmentCreate struct {
	calls int
}

func (s *fakeFragmentCreate) Create(_ context.Context, profileID string, req *dto.CreateFragmentRequest) (*fragmentservice.CreateResult, error) {
	s.calls++
	return &fragmentservice.CreateResult{Fragment: &domain.Fragment{FragmentID: "fragment-1", ProfileID: profileID, Content: req.Content}}, nil
}

type fakeClaimCreate struct {
	calls int
}

func (s *fakeClaimCreate) Create(_ context.Context, profileID string, claim *domain.Claim) (*claimservice.CreateResult, error) {
	s.calls++
	claim.ProfileID = profileID
	return &claimservice.CreateResult{Claim: claim}, nil
}

type fakeClaimVerify struct {
	calls int
}

func (s *fakeClaimVerify) Verify(_ context.Context, profileID string, claimID string) (*domain.Claim, error) {
	s.calls++
	return &domain.Claim{ClaimID: claimID, ProfileID: profileID}, nil
}

type fakeFactPromote struct {
	calls int
}

func (s *fakeFactPromote) Promote(_ context.Context, profileID string, _ string) (*domain.Fact, error) {
	s.calls++
	return &domain.Fact{FactID: "fact-1", ProfileID: profileID}, nil
}

type fakeFactConfirm struct {
	calls int
}

func (s *fakeFactConfirm) ConfirmMemory(_ context.Context, _ string, req factservice.ConfirmMemoryRequest) (*factservice.ConfirmMemoryResult, error) {
	s.calls++
	return &factservice.ConfirmMemoryResult{ClaimID: req.ClaimID, Decision: "accepted"}, nil
}

type fakeRecall struct {
	calls int
}

func (s *fakeRecall) Recall(_ context.Context, _ string, _ recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	s.calls++
	return []recallservice.RecallHit{{FinalScore: 1}}, nil
}

type failingCounterStore struct{}

func (failingCounterStore) IncrWithExpire(context.Context, string, int64) (int64, error) {
	return 0, errors.New("counter failed")
}

func (failingCounterStore) AddWithExpire(context.Context, string, int64, int64) (int64, error) {
	return 0, errors.New("counter failed")
}
