//go:build integration

package settings_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	settings "github.com/markhuangai/dense-mem/internal/settings"
	settingscontract "github.com/markhuangai/dense-mem/internal/settings/contract"
	settingspostgres "github.com/markhuangai/dense-mem/internal/settings/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func assertSecurityBanRequestSnapshots(t *testing.T, db *gorm.DB, service *settings.SecurityServiceImpl, handler http.Handler) {
	t.Helper()
	server := httptest.NewServer(handler)
	defer server.Close()
	ip := uniqueSecurityTestIP()
	defer func() { _ = service.DeleteSecurityBan(context.Background(), ip, "control", "", "") }()
	const body = "{\n\"diagnostic\":\"request-body-canary\",\"auth\":\"wrong-token\",\"configured\":\"settings-token\"\n}"
	send := func(method, path, token, payload, clientIP string) (int, string) {
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(payload))
		require.NoError(t, err)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Forwarded-For", clientIP)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Add("X-Diagnostic", "first")
		req.Header.Add("X-Diagnostic", "second")
		response, err := server.Client().Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		responseBody, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return response.StatusCode, string(responseBody)
	}
	for range 2 {
		status, public := send(http.MethodPatch, "/control/api/config/general", "wrong-token", body, ip)
		require.Equal(t, http.StatusUnauthorized, status)
		require.NotContains(t, public, "request-body-canary")
	}
	ban, err := service.CheckBan(context.Background(), ip)
	require.NoError(t, err)
	require.NotNil(t, ban)
	encoded, err := json.Marshal(ban.Metadata["last_request"])
	require.NoError(t, err)
	var snapshot domain.SecurityAuthFailureRequest
	require.NoError(t, json.Unmarshal(encoded, &snapshot))
	require.Contains(t, snapshot.Body, "request-body-canary")
	require.NotContains(t, string(encoded), "wrong-token")
	require.NotContains(t, string(encoded), "settings-token")
	require.Equal(t, []string{"first", "second"}, snapshot.Headers["X-Diagnostic"])
	status, list := send(http.MethodGet, "/control/api/security/bans", "settings-token", "", "192.0.2.45")
	require.Equal(t, http.StatusOK, status)
	require.Contains(t, list, snapshot.ID)
	deniedIP := uniqueSecurityTestIP()
	defer func() { _ = service.DeleteSecurityBan(context.Background(), deniedIP, "control", "", "") }()
	status, denied := send(http.MethodGet, "/control/api/security/bans", "wrong-token", "", deniedIP)
	require.Equal(t, http.StatusUnauthorized, status)
	require.NotContains(t, denied, snapshot.ID)
	status, _ = send(http.MethodPatch, "/control/api/config/general", "different-wrong-token", "{}", ip)
	require.Equal(t, http.StatusForbidden, status)
	unchanged, err := service.CheckBan(context.Background(), ip)
	require.NoError(t, err)
	require.Equal(t, ban.Metadata, unchanged.Metadata)
	var auditMetadata string
	require.NoError(t, db.Raw("SELECT metadata::text FROM audit_log WHERE operation = 'SECURITY_AUTO_BAN' AND entity_id = $1 ORDER BY timestamp DESC LIMIT 1", ip).Scan(&auditMetadata).Error)
	require.NotEmpty(t, auditMetadata)
	require.NotContains(t, auditMetadata, "request-body-canary")
	require.NotContains(t, auditMetadata, "last_request")
	for _, payload := range []string{"before\x00after", `{"value":"\u0000"}`} {
		require.NoError(t, service.DeleteSecurityBan(context.Background(), ip, "control", "", ""))
		for range 2 {
			status, _ := send(http.MethodPatch, "/control/api/config/general", "wrong-token", payload, ip)
			require.Equal(t, http.StatusUnauthorized, status)
		}
		persisted, err := service.CheckBan(context.Background(), ip)
		require.NoError(t, err)
		require.NotNil(t, persisted)
		encoded, err := json.Marshal(persisted.Metadata["last_request"])
		require.NoError(t, err)
		var captured domain.SecurityAuthFailureRequest
		require.NoError(t, json.Unmarshal(encoded, &captured))
		if strings.IndexByte(payload, 0) >= 0 {
			require.Empty(t, captured.Body)
			require.Equal(t, "unavailable", captured.BodyCapture.State)
			require.Equal(t, "binary_body", captured.BodyCapture.Reason)
		} else {
			require.Equal(t, payload, captured.Body)
			require.Equal(t, "captured", captured.BodyCapture.State)
		}
		status, _ := send(http.MethodPatch, "/control/api/config/general", "wrong-token", "{}", ip)
		require.Equal(t, http.StatusForbidden, status)
	}
	assertSecurityBanOrdering(t, db)
	assertSecurityBanAuditAfterCommittedCancellation(t, db)
}

func assertSecurityBanOrdering(t *testing.T, db *gorm.DB) {
	t.Helper()
	repo := settingspostgres.NewSecurityRepository(db, storagepostgres.NewRLS())
	ip := uniqueSecurityTestIP()
	now := time.Now().UTC().Add(-time.Minute)
	defer func() { _ = repo.DeleteBan(context.Background(), ip, time.Now().UTC()) }()
	var group sync.WaitGroup
	errors := make(chan error, 8)
	for i := range 8 {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			at := now.Add(time.Duration(i) * time.Second)
			_, err := repo.UpsertBan(context.Background(), &domain.SecurityIPBan{IP: ip, Source: domain.SecurityBanSourceAuto, Reason: "AUTH_INVALID", FailureCount: i + 1, BannedAt: at, LastFailedAt: &at, CreatedAt: at, UpdatedAt: at, Metadata: map[string]any{"last_request": domain.SecurityAuthFailureRequest{ID: string(rune('a' + i)), CapturedAt: at, Body: "bounded diagnostic"}}})
			errors <- err
		}(i)
	}
	group.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	ban, err := repo.GetActiveBan(context.Background(), ip, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, 8, ban.FailureCount)
	require.Equal(t, "h", ban.Metadata["last_request"].(map[string]any)["id"])
	manualAt := time.Now().UTC()
	manual := &domain.SecurityIPBan{IP: ip, Source: domain.SecurityBanSourceManual, Reason: "manual", BannedAt: manualAt, CreatedAt: manualAt, UpdatedAt: manualAt}
	applied, err := repo.UpsertBan(context.Background(), manual)
	require.NoError(t, err)
	require.True(t, applied)
	applied, err = repo.UpsertBan(context.Background(), ban)
	require.NoError(t, err)
	require.False(t, applied)
	actual, err := repo.GetActiveBan(context.Background(), ip, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, domain.SecurityBanSourceManual, actual.Source)
	require.NoError(t, repo.DeleteBan(context.Background(), ip, manualAt.Add(time.Second)))
	applied, err = repo.UpsertBan(context.Background(), ban)
	require.NoError(t, err)
	require.False(t, applied)
	actual, err = repo.GetActiveBan(context.Background(), ip, time.Now().UTC())
	require.NoError(t, err)
	require.Nil(t, actual)
}

type cancelAfterBanWriteRepository struct {
	settingscontract.SecurityRepository
	cancel context.CancelFunc
}

func (r *cancelAfterBanWriteRepository) UpsertBan(ctx context.Context, ban *domain.SecurityIPBan) (bool, error) {
	applied, err := r.SecurityRepository.UpsertBan(ctx, ban)
	if applied && err == nil {
		r.cancel()
	}
	return applied, err
}

func assertSecurityBanAuditAfterCommittedCancellation(t *testing.T, db *gorm.DB) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rls := storagepostgres.NewRLS()
	repo := settingspostgres.NewSecurityRepository(db, rls)
	audit := auditapp.New(auditpostgres.NewStore(db, rls))
	service := settings.NewSecurityServiceWithDiagnostics(&cancelAfterBanWriteRepository{SecurityRepository: repo, cancel: cancel}, audit, settings.SecurityDiagnosticsOptions{Protector: observability.NewCredentialProtector()})
	ip := uniqueSecurityTestIP()
	defer func() { _ = repo.DeleteBan(context.Background(), ip, time.Now().UTC()) }()
	var ban *domain.SecurityIPBan
	for range 2 {
		var err error
		ban, err = service.RecordAuthFailure(ctx, ip, "mcp", "AUTH_INVALID", nil)
		require.NoError(t, err)
	}
	require.NotNil(t, ban)
	require.ErrorIs(t, ctx.Err(), context.Canceled)
	persisted, err := repo.GetActiveBan(context.Background(), ip, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, persisted)
	var count int64
	require.NoError(t, db.Raw("SELECT count(*) FROM audit_log WHERE operation = 'SECURITY_AUTO_BAN' AND entity_id = $1", ip).Scan(&count).Error)
	require.Equal(t, int64(1), count)
}
