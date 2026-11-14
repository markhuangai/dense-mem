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

	"github.com/markhuangai/dense-mem/internal/domain"
	settings "github.com/markhuangai/dense-mem/internal/settings"
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
	assertSecurityBanOrdering(t, db)
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
			errors <- repo.UpsertBan(context.Background(), &domain.SecurityIPBan{IP: ip, Source: domain.SecurityBanSourceAuto, Reason: "AUTH_INVALID", FailureCount: i + 1, BannedAt: at, LastFailedAt: &at, CreatedAt: at, UpdatedAt: at, Metadata: map[string]any{"last_request": domain.SecurityAuthFailureRequest{ID: string(rune('a' + i)), CapturedAt: at, Body: "bounded diagnostic"}}})
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
	require.NoError(t, repo.UpsertBan(context.Background(), manual))
	require.NoError(t, repo.UpsertBan(context.Background(), ban))
	actual, err := repo.GetActiveBan(context.Background(), ip, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, domain.SecurityBanSourceManual, actual.Source)
	require.NoError(t, repo.DeleteBan(context.Background(), ip, manualAt.Add(time.Second)))
	require.NoError(t, repo.UpsertBan(context.Background(), ban))
	actual, err = repo.GetActiveBan(context.Background(), ip, time.Now().UTC())
	require.NoError(t, err)
	require.Nil(t, actual)
}
