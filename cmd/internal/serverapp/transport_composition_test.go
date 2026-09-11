package serverapp

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

type transportSearchConvergenceStub struct {
	value *repository.SearchConvergence
	err   error
}

func (s transportSearchConvergenceStub) GetSearchConvergence(context.Context) (*repository.SearchConvergence, error) {
	return s.value, s.err
}

func TestNativeSearchConvergenceReaderAdaptsCompatibilityProjection(t *testing.T) {
	now := time.Date(2026, time.August, 26, 1, 0, 0, 0, time.UTC)
	start, finish := now.Add(-time.Minute), now.Add(-time.Second)
	reader := nativeSearchConvergenceReader(transportSearchConvergenceStub{value: &repository.SearchConvergence{
		ObservedAt: now, Status: "attention_required", ExpectedDocuments: 10, CurrentDocuments: 7,
		DriftedDocuments: 3, AffectedTeamCount: 2, OldestDriftAge: 2 * time.Minute,
		Contract:     &repository.ActiveSearchContract{EmbeddingProvider: "openai", EmbeddingModel: "model", EmbeddingDimensions: 3},
		DriftClasses: []repository.SearchDocumentDriftCount{{Class: "missing_vector", Count: 3}},
		LatestRun:    &repository.SearchReconciliationRun{RunID: "run", LocalRunDate: now, Status: "failed", StartedAt: &start, CompletedAt: &finish, UpdatedAt: now},
	}})

	got, err := reader.GetSearchConvergence(context.Background())
	if err != nil {
		t.Fatalf("adapted reader returned error: %v", err)
	}
	if got == nil || got.Status != "attention_required" || got.ExpectedDocuments != 10 {
		t.Fatalf("adapted projection = %#v", got)
	}
	if len(got.DriftClasses) != 1 || got.DriftClasses[0].Class != "missing_vector" {
		t.Fatalf("adapted drift classes = %#v", got.DriftClasses)
	}
	if got.LatestRun == nil || got.LatestRun.RunID != "run" {
		t.Fatalf("adapted latest run = %#v", got.LatestRun)
	}

	if nativeSearchConvergenceReader(nil) != nil {
		t.Fatal("nil compatibility reader should remain nil")
	}
	failed := errors.New("backend failed")
	failedReader := nativeSearchConvergenceReader(transportSearchConvergenceStub{err: failed})
	_, err = failedReader.GetSearchConvergence(context.Background())
	if !errors.Is(err, failed) {
		t.Fatalf("adapted error = %v, want %v", err, failed)
	}
}

func TestTransportCompositionPreservesRouteAndUserHookOrder(t *testing.T) {
	backend, err := buildInMemoryBackend(config.Config{SSEMaxConcurrentStreams: 2})
	if err != nil {
		t.Fatalf("build in-memory backend: %v", err)
	}
	var routeRegistered bool
	var mcpPresentDuringRegister bool
	var userHooks []string
	var postAuthHooks []string
	markHook := func(marker string) echo.MiddlewareFunc {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				userHooks = append(userHooks, marker)
				return next(c)
			}
		}
	}
	markPostAuthHook := func(marker string) echo.MiddlewareFunc {
		return func(next echo.HandlerFunc) echo.HandlerFunc {
			return func(c echo.Context) error {
				postAuthHooks = append(postAuthHooks, marker)
				return next(c)
			}
		}
	}
	teamID := uuid.New()
	credential := &domain.Credential{
		ID:              uuid.New(),
		ActorIdentityID: uuid.New(),
		MembershipID:    uuid.New(),
		OwnerID:         uuid.New(),
		TeamID:          teamID,
		KeyPrefix:       "dm_live_transport-test",
		Scopes:          []string{"read", "write"},
		Role:            "manager",
	}
	composition, err := buildTransportComposition(transportCompositionInputs{
		cfg:                config.Config{},
		pgDB:               &postgres.DB{},
		backend:            backend,
		logger:             observability.New(slog.LevelInfo),
		toolRegistry:       registry.New(),
		directoryIdentity:  service.NewDirectoryIdentityService(nil, service.DirectoryIdentityConfig{}),
		credentialRepo:     transportCredentialRepository{credential: credential},
		credentialVerifier: transportCredentialVerifier{},
		teamService:        transportTeamService{team: &domain.Team{ID: teamID, Name: "transport-test"}},
		options: RuntimeOptions{
			DisableControlPortal: true,
			RegisterRoutes: func(runtime RuntimeContext) error {
				routeRegistered = true
				for _, route := range runtime.Echo.Routes() {
					if route.Path == "/mcp" {
						mcpPresentDuringRegister = true
					}
				}
				runtime.Echo.GET("/registered-before-protected", func(echo.Context) error { return nil })
				return nil
			},
			PostAuthMiddleware:   []echo.MiddlewareFunc{markPostAuthHook("post-auth-1"), markPostAuthHook("post-auth-2")},
			UserPortalMiddleware: []echo.MiddlewareFunc{markHook("user-1"), markHook("user-2")},
		},
	})
	if err != nil {
		t.Fatalf("build transport composition: %v", err)
	}
	if !routeRegistered {
		t.Fatal("RegisterRoutes hook was not called")
	}

	routes := composition.e.Routes()
	mcpRegistered := false
	var sessionRoute *echo.Route
	for _, route := range routes {
		if route.Path == "/mcp" && route.Method == http.MethodPost {
			mcpRegistered = true
		}
		if route.Path == "/ui/api/session" && route.Method == http.MethodGet {
			sessionRoute = route
		}
	}
	if mcpPresentDuringRegister || !mcpRegistered {
		t.Fatalf("protected route registration order: present during callback=%t registered afterward=%t", mcpPresentDuringRegister, mcpRegistered)
	}
	if sessionRoute == nil {
		t.Fatal("user portal session route was not registered")
	}

	postAuthRequest := httptest.NewRequest(http.MethodPost, "/mcp", bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	postAuthRequest.Header.Set("Authorization", "Bearer dm_live_transport-test-key-1234567890")
	postAuthRequest.Header.Set("MCP-Protocol-Version", "2025-06-18")
	postAuthRequest.Header.Set("Accept", "application/json")
	composition.e.ServeHTTP(httptest.NewRecorder(), postAuthRequest)
	if got, want := postAuthHooks, []string{"post-auth-1", "post-auth-2"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("post-auth middleware order = %v; want %v", got, want)
	}

	request := httptest.NewRequest(http.MethodGet, "/ui/api/session", nil)
	composition.e.ServeHTTP(httptest.NewRecorder(), request)
	if got, want := userHooks, []string{"user-1", "user-2"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("user middleware order = %v; want %v", got, want)
	}
}

type transportCredentialRepository struct {
	repository.CredentialRepository
	credential *domain.Credential
}

func (r transportCredentialRepository) GetActiveByPrefix(context.Context, string) (*domain.Credential, error) {
	return r.credential, nil
}

type transportCredentialVerifier struct{}

func (transportCredentialVerifier) Verify(context.Context, string, string) (bool, error) {
	return true, nil
}

type transportTeamService struct {
	service.TeamService
	team *domain.Team
}

func (s transportTeamService) GetByID(context.Context, uuid.UUID) (*domain.Team, error) {
	return s.team, nil
}

var _ crypto.CredentialVerifier = transportCredentialVerifier{}
