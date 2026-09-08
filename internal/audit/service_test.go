package audit

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/audit/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

type storeStub struct {
	entry       contract.Entry
	appendErr   error
	entries     []contract.Entry
	listErr     error
	count       int
	countErr    error
	appendCalls int
}

func (s *storeStub) Append(_ context.Context, entry contract.Entry) error {
	s.entry = entry
	s.appendCalls++
	return s.appendErr
}

func (s *storeStub) List(context.Context, string, int, int) ([]contract.Entry, error) {
	return s.entries, s.listErr
}

func (s *storeStub) Count(context.Context, string) (int, error) {
	return s.count, s.countErr
}

func TestAppendRedactsPayloadsWithoutMutatingTheCaller(t *testing.T) {
	store := &storeStub{}
	svc := New(store)
	ctx := requestctx.WithClientIP(context.Background(), "192.0.2.10")
	entry := accessservice.AuditLogEntry{
		ID:           "audit-1",
		ProfileID:    stringPointer("team-1"),
		Timestamp:    time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		Operation:    "CREATE",
		EntityType:   "profile",
		EntityID:     "team-1",
		AfterPayload: map[string]interface{}{"name": "safe", "api_key": "remove", "nested": map[string]interface{}{"token": "remove", "label": "keep"}},
		Metadata:     map[string]interface{}{"source": "test", "password": "remove"},
	}

	require.NoError(t, svc.Append(ctx, entry))
	require.Equal(t, 1, store.appendCalls)
	require.Equal(t, "audit-1", store.entry.ID)
	require.Equal(t, "192.0.2.10", store.entry.ClientIP)
	require.JSONEq(t, `{"name":"safe","nested":{"label":"keep"}}`, string(store.entry.AfterPayload))
	require.JSONEq(t, `{"source":"test"}`, string(store.entry.Metadata))
	require.Contains(t, entry.AfterPayload, "api_key")
	require.Contains(t, entry.Metadata, "password")
}

func TestAppendReportsJSONErrorsBeforeStoreAccess(t *testing.T) {
	store := &storeStub{}
	svc := New(store)
	err := svc.Append(context.Background(), accessservice.AuditLogEntry{
		BeforePayload: map[string]interface{}{"invalid": func() {}},
	})
	require.ErrorContains(t, err, "failed to marshal before_payload")
	require.Zero(t, store.appendCalls)

	err = svc.Append(context.Background(), accessservice.AuditLogEntry{
		AfterPayload: map[string]interface{}{"invalid": func() {}},
	})
	require.ErrorContains(t, err, "failed to marshal after_payload")
	require.Zero(t, store.appendCalls)

	err = svc.Append(context.Background(), accessservice.AuditLogEntry{
		Metadata: map[string]interface{}{"invalid": func() {}},
	})
	require.ErrorContains(t, err, "failed to marshal metadata")
	require.Zero(t, store.appendCalls)
}

func TestAppendOwnsCredentialSpaceLookupEligibility(t *testing.T) {
	store := &storeStub{}
	teamID := "adc56b94-9853-45d6-b970-aafadf2d1c5d"
	credentialID := "b5ca9fc3-60a6-4e65-9227-c22a91f85d5c"
	require.NoError(t, New(store).Append(context.Background(), accessservice.AuditLogEntry{
		ProfileID: &teamID, EntityType: "api_key", EntityID: credentialID, Metadata: map[string]interface{}{},
	}))
	require.NotNil(t, store.entry.CredentialMemorySpaceLookup)
	require.Equal(t, teamID, store.entry.CredentialMemorySpaceLookup.TeamID.String())
	require.Equal(t, credentialID, store.entry.CredentialMemorySpaceLookup.CredentialID.String())

	store = &storeStub{}
	require.NoError(t, New(store).Append(context.Background(), accessservice.AuditLogEntry{
		ProfileID: stringPointer("not-a-uuid"), EntityType: "api_key", EntityID: credentialID, Metadata: map[string]interface{}{},
	}))
	require.Nil(t, store.entry.CredentialMemorySpaceLookup)
}

func TestEventHelpersPreserveOperationsAndMetadata(t *testing.T) {
	store := &storeStub{}
	svc := New(store)
	ctx := context.Background()

	require.NoError(t, svc.TeamDeleteBlocked(ctx, "team-1", nil, nil, "admin", "", "corr", "active keys"))
	require.Equal(t, "DELETE_BLOCKED", store.entry.Operation)
	metadata := decodePayload(store.entry.Metadata)
	require.Equal(t, "active keys", metadata["reason"])

	require.NoError(t, svc.CrossTeamDenied(ctx, "actor", "target", "read", nil, "", "corr"))
	require.Equal(t, "CROSS_PROFILE_DENIED", store.entry.Operation)
	metadata = decodePayload(store.entry.Metadata)
	require.Equal(t, "actor", metadata["actor_profile_id"])
	require.Equal(t, "target", metadata["target_team_id"])
	require.Equal(t, "read", metadata["denied_operation"])
}

func TestEventHelpersCoverTheRemainingAuditOperations(t *testing.T) {
	store := &storeStub{}
	svc := New(store)
	ctx := context.Background()
	teamID := "team-1"
	credentialID := "credential-1"
	actorID := "actor-1"

	require.NoError(t, svc.TeamCreated(ctx, teamID, nil, &actorID, "admin", "", "created"))
	require.Equal(t, "CREATE", store.entry.Operation)
	require.Equal(t, teamID, store.entry.EntityID)

	require.NoError(t, svc.TeamUpdated(ctx, teamID, nil, nil, &actorID, "admin", "", "updated"))
	require.Equal(t, "UPDATE", store.entry.Operation)

	require.NoError(t, svc.TeamDeleted(ctx, teamID, nil, &actorID, "admin", "", "deleted"))
	require.Equal(t, "DELETE", store.entry.Operation)

	require.NoError(t, svc.CredentialCreated(ctx, &teamID, credentialID, nil, &actorID, "admin", "", "credential-created"))
	require.Equal(t, "CREATE", store.entry.Operation)
	require.Equal(t, "api_key", store.entry.EntityType)

	require.NoError(t, svc.CredentialRevoked(ctx, &teamID, credentialID, nil, &actorID, "admin", "", "credential-revoked"))
	require.Equal(t, "REVOKE", store.entry.Operation)

	require.NoError(t, svc.AuthFailure(ctx, &teamID, "api_key", credentialID, nil, "", "auth-failed"))
	require.Equal(t, "AUTH_FAILURE", store.entry.Operation)

	require.NoError(t, svc.RateLimited(ctx, &teamID, "read", nil, "", "rate-limited"))
	require.Equal(t, "RATE_LIMITED", store.entry.Operation)
	require.Equal(t, "read", decodePayload(store.entry.Metadata)["limited_operation"])

	require.NoError(t, svc.SystemQuery(ctx, "list_profiles", nil, &actorID, "system", "", "system-query"))
	require.Equal(t, "SYSTEM_QUERY", store.entry.Operation)
	require.Equal(t, "list_profiles", decodePayload(store.entry.Metadata)["query_type"])

	require.NoError(t, svc.InvariantViolation(ctx, "profile", teamID, "missing owner", nil, "", "invariant"))
	require.Equal(t, "INVARIANT_VIOLATION", store.entry.Operation)
	require.Equal(t, "missing owner", decodePayload(store.entry.Metadata)["violation_description"])
}

func TestListConvertsEntriesAndSeparatesCountFailure(t *testing.T) {
	store := &storeStub{
		entries: []contract.Entry{{
			ID:            "audit-1",
			ProfileID:     stringPointer("team-1"),
			MemorySpaceID: stringPointer("space-1"),
			Timestamp:     time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
			Operation:     "UPDATE",
			EntityType:    "profile",
			EntityID:      "team-1",
			BeforePayload: []byte(`{"name":"before"}`),
			AfterPayload:  []byte(`{"name":"after"}`),
			ActorKeyID:    stringPointer("key-1"),
			ActorRole:     "admin",
			ClientIP:      "203.0.113.10",
			CorrelationID: "corr-1",
			Metadata:      []byte(`{"source":"test"}`),
		}},
		count: 3,
	}
	entries, total, err := New(store).List(context.Background(), "team-1", 20, 0)
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Len(t, entries, 1)
	require.Equal(t, "space-1", *entries[0].MemorySpaceID)
	require.Equal(t, "before", entries[0].BeforePayload["name"])
	require.Equal(t, "after", entries[0].AfterPayload["name"])

	store.countErr = errors.New("count failed")
	_, _, err = New(store).List(context.Background(), "team-1", 20, 0)
	require.ErrorContains(t, err, "failed to count audit log entries")
}

func TestListBoundsMalformedPayloadsAndNilClientIP(t *testing.T) {
	store := &storeStub{entries: []contract.Entry{{
		ID:            "audit-invalid",
		BeforePayload: []byte("not-json"),
		AfterPayload:  []byte("not-json"),
		Metadata:      []byte("not-json"),
	}}, count: 1}
	entries, total, err := New(store).List(context.Background(), "team-1", 1, 0)
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, entries, 1)
	require.Nil(t, entries[0].BeforePayload)
	require.Nil(t, entries[0].AfterPayload)
	require.Nil(t, entries[0].Metadata)
	require.Empty(t, entries[0].ClientIP)
}

func TestRedactPayloadHandlesTypedNestedValues(t *testing.T) {
	redacted := RedactPayload(map[string]interface{}{
		"safe":   "keep",
		"nested": map[string]string{"clientSecret": "remove", "value": "keep"},
		"items":  []map[string]interface{}{{"refresh_token": "remove", "value": "keep"}},
	})
	raw, err := json.Marshal(redacted)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "remove")
	require.Contains(t, string(raw), "keep")
}

func stringPointer(value string) *string {
	return &value
}
