package audit

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/audit/contract"
	"github.com/stretchr/testify/require"
)

func TestExportProjectionExcludesUntrustedFields(t *testing.T) {
	canary := "prompt-token-cookie-correlation-canary"
	team, actor := uuid.NewString(), uuid.NewString()
	entry := contract.ExportEntry{Position: contract.ExportPosition{ID: uuid.NewString(), Timestamp: time.Now().UTC(), XID: "1"}, TeamID: &team, ActorID: &actor, Operation: canary, EntityType: canary, EntityID: canary, ActorRole: canary}
	raw, err := json.Marshal(projectExportEntry(entry))
	require.NoError(t, err)
	require.NotContains(t, string(raw), canary)
	require.Contains(t, string(raw), team)
	require.Contains(t, string(raw), actor)
	require.NotContains(t, string(raw), "xid")
	require.NotContains(t, string(raw), "payload")
	require.NotContains(t, string(raw), "metadata")
	entry.Operation = "SECURITY_REJECTED"
	entry.EntityType = "memory_intake_attempt"
	entry.ActorRole = "control"
	entry.EntityID = uuid.NewString()
	event := projectExportEntry(entry)
	require.Equal(t, entry.Operation, event.Operation)
	require.Equal(t, entry.EntityType, event.EntityType)
	require.Equal(t, entry.EntityID, event.EntityID)
}

func TestExportCursorScopeAndClosedShape(t *testing.T) {
	team := uuid.New()
	cursor := exportCursor{Version: 1, Scope: team.String(), Position: &contract.ExportPosition{XID: "125", Timestamp: time.Now().UTC(), ID: uuid.NewString()}}
	encoded, err := encodeExportCursor(cursor)
	require.NoError(t, err)
	parsed, limit, err := parseExportRequest(ExportRequest{TeamID: &team, Cursor: encoded})
	require.NoError(t, err)
	require.Equal(t, 100, limit)
	require.Equal(t, cursor, parsed)
	other := uuid.New()
	for _, request := range []ExportRequest{{Cursor: encoded}, {TeamID: &other, Cursor: encoded}, {Cursor: "!"}, {Limit: -1}, {Limit: 1001}, {Cursor: strings.Repeat("a", 1025)}} {
		_, _, err := parseExportRequest(request)
		require.ErrorIs(t, err, ErrInvalidExport)
	}
	for _, raw := range []string{`{"version":1,"scope":"instance","position":null,"extra":true}`, `{"version":1,"scope":"instance","position":null} {}`, `null`, `{"version":2,"scope":"instance"}`} {
		_, _, err := parseExportRequest(ExportRequest{Cursor: base64.RawURLEncoding.EncodeToString([]byte(raw))})
		require.ErrorIs(t, err, ErrInvalidExport)
	}
	for _, position := range []contract.ExportPosition{
		{XID: "0125", Timestamp: time.Now().UTC(), ID: uuid.NewString()},
		{XID: "125", ID: uuid.NewString()},
		{XID: "125", Timestamp: time.Now().UTC(), ID: "not-a-uuid"},
	} {
		encoded, err := encodeExportCursor(exportCursor{Version: 1, Scope: "instance", Position: &position})
		require.NoError(t, err)
		_, _, err = parseExportRequest(ExportRequest{Cursor: encoded})
		require.ErrorIs(t, err, ErrInvalidExport)
	}
	nilTeam := uuid.Nil
	_, _, err = parseExportRequest(ExportRequest{TeamID: &nilTeam})
	require.ErrorIs(t, err, ErrInvalidExport)
}

func TestExportProjectionPreservesSettingsAndSecurityClasses(t *testing.T) {
	for _, entry := range []contract.ExportEntry{
		{Operation: "APP_CONFIG_UPDATE", EntityType: "app_config", EntityID: "general"},
		{Operation: "SECURITY_SETTINGS_UPDATE", EntityType: "security_settings", EntityID: "global"},
		{Operation: "SECURITY_AUTO_BAN", EntityType: "security_ip_ban", EntityID: "192.0.2.10"},
		{Operation: "SECURITY_MANUAL_BAN", EntityType: "security_ip_ban", EntityID: "192.0.2.10"},
		{Operation: "SECURITY_UNBAN", EntityType: "security_ip_ban", EntityID: "192.0.2.10"},
	} {
		t.Run(entry.Operation, func(t *testing.T) {
			event := projectExportEntry(entry)
			require.Equal(t, entry.Operation, event.Operation)
			require.Equal(t, entry.EntityType, event.EntityType)
			require.Empty(t, event.EntityID)
		})
	}
}

func TestExportWithoutServiceIsUnavailable(t *testing.T) {
	var service *Service
	_, err := service.ExportPage(context.Background(), ExportRequest{})
	require.ErrorIs(t, err, ErrExportUnavailable)
}
