//go:build integration

package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCleanupProfileState_DeletesAllPrefixes verifies that all three prefix patterns
// (cache, session, stream) are purged when PurgeTeamState is called.
func TestCleanupProfileState_DeletesAllPrefixes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := &mockConfig{
		redisAddr:     "localhost:6379",
		redisPassword: "",
		redisDB:       0,
	}

	client, err := NewClient(ctx, cfg)
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	defer client.Close()

	redisClient := client.GetClient()
	cleanup := NewCleanupRepository(redisClient)
	profileID := "test-cleanup-prefixes"

	// Create keys in all three categories
	cacheKey := fmt.Sprintf("profile:%s:cache:test-cache-key", profileID)
	sessionKey := fmt.Sprintf("profile:%s:session:test-session-key", profileID)
	streamKey := fmt.Sprintf("profile:%s:stream:test-stream-key", profileID)

	// Set keys
	require.NoError(t, redisClient.Set(ctx, cacheKey, "cache-value", 5*time.Minute).Err())
	require.NoError(t, redisClient.Set(ctx, sessionKey, `{"key_id":"abc"}`, 5*time.Minute).Err())
	require.NoError(t, redisClient.Set(ctx, streamKey, "stream-value", 5*time.Minute).Err())

	// Verify keys exist
	require.True(t, redisClient.Exists(ctx, cacheKey).Val() > 0)
	require.True(t, redisClient.Exists(ctx, sessionKey).Val() > 0)
	require.True(t, redisClient.Exists(ctx, streamKey).Val() > 0)

	// Purge
	err = cleanup.PurgeTeamState(ctx, profileID)
	require.NoError(t, err)

	// Verify keys are deleted
	assert.Equal(t, int64(0), redisClient.Exists(ctx, cacheKey).Val())
	assert.Equal(t, int64(0), redisClient.Exists(ctx, sessionKey).Val())
	assert.Equal(t, int64(0), redisClient.Exists(ctx, streamKey).Val())
}

// TestCleanupProfileState_LeavesOtherProfiles verifies that purging one profile
// does not affect keys from other profiles.
func TestCleanupProfileState_LeavesOtherProfiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := &mockConfig{
		redisAddr:     "localhost:6379",
		redisPassword: "",
		redisDB:       0,
	}

	client, err := NewClient(ctx, cfg)
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	defer client.Close()

	redisClient := client.GetClient()
	cleanup := NewCleanupRepository(redisClient)

	profileID1 := "test-cleanup-profile1"
	profileID2 := "test-cleanup-profile2"

	// Create keys for both profiles
	key1 := fmt.Sprintf("profile:%s:cache:key1", profileID1)
	key2 := fmt.Sprintf("profile:%s:cache:key2", profileID2)

	require.NoError(t, redisClient.Set(ctx, key1, "value1", 5*time.Minute).Err())
	require.NoError(t, redisClient.Set(ctx, key2, "value2", 5*time.Minute).Err())

	// Purge profile1
	err = cleanup.PurgeTeamState(ctx, profileID1)
	require.NoError(t, err)

	// Verify profile1 key is deleted but profile2 key remains
	assert.Equal(t, int64(0), redisClient.Exists(ctx, key1).Val())
	assert.Equal(t, int64(1), redisClient.Exists(ctx, key2).Val())

	// Cleanup profile2
	redisClient.Del(ctx, key2)
}

// TestCleanupProfileState_IteratesFullScan verifies that SCAN iterates through
// all pages of keys, not just the first page.
func TestCleanupProfileState_IteratesFullScan(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := &mockConfig{
		redisAddr:     "localhost:6379",
		redisPassword: "",
		redisDB:       0,
	}

	client, err := NewClient(ctx, cfg)
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	defer client.Close()

	redisClient := client.GetClient()
	cleanup := NewCleanupRepository(redisClient)

	profileID := "test-cleanup-fullscan"

	// Create more keys than fit in one SCAN page (SCAN uses small page sizes)
	// Create 500 keys to ensure multiple pages
	keyCount := 500
	keys := make([]string, keyCount)
	for i := 0; i < keyCount; i++ {
		keys[i] = fmt.Sprintf("profile:%s:cache:key-%d", profileID, i)
	}

	// Set all keys
	for _, key := range keys {
		require.NoError(t, redisClient.Set(ctx, key, "value", 5*time.Minute).Err())
	}

	// Purge
	err = cleanup.PurgeTeamState(ctx, profileID)
	require.NoError(t, err)

	// Verify all keys are deleted by checking a sample
	for i := 0; i < keyCount; i += 50 {
		assert.Equal(t, int64(0), redisClient.Exists(ctx, keys[i]).Val(),
			"Key %s should be deleted", keys[i])
	}
}

// TestInvalidateCredentialSessions_MatchingKeyID verifies that only sessions with
// matching key_id are deleted.
func TestInvalidateCredentialSessions_MatchingKeyID(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := &mockConfig{
		redisAddr:     "localhost:6379",
		redisPassword: "",
		redisDB:       0,
	}

	client, err := NewClient(ctx, cfg)
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	defer client.Close()

	redisClient := client.GetClient()
	cleanup := NewCleanupRepository(redisClient)

	profileID := "test-invalidate-matching"
	keyIDToInvalidate := "key-to-invalidate"
	keyIDToKeep := "key-to-keep"

	// Create sessions with different key_ids
	session1 := fmt.Sprintf("profile:%s:session:session1", profileID)
	session2 := fmt.Sprintf("profile:%s:session:session2", profileID)
	session3 := fmt.Sprintf("profile:%s:session:session3", profileID)

	payload1, _ := json.Marshal(map[string]string{"key_id": keyIDToInvalidate})
	payload2, _ := json.Marshal(map[string]string{"key_id": keyIDToKeep})
	payload3, _ := json.Marshal(map[string]string{"key_id": keyIDToInvalidate})

	require.NoError(t, redisClient.Set(ctx, session1, string(payload1), 5*time.Minute).Err())
	require.NoError(t, redisClient.Set(ctx, session2, string(payload2), 5*time.Minute).Err())
	require.NoError(t, redisClient.Set(ctx, session3, string(payload3), 5*time.Minute).Err())

	// Invalidate sessions for keyIDToInvalidate
	err = cleanup.InvalidateCredentialSessions(ctx, profileID, keyIDToInvalidate)
	require.NoError(t, err)

	// Verify sessions with matching key_id are deleted
	assert.Equal(t, int64(0), redisClient.Exists(ctx, session1).Val(), "session1 should be deleted")
	assert.Equal(t, int64(0), redisClient.Exists(ctx, session3).Val(), "session3 should be deleted")
	// Verify session with different key_id remains
	assert.Equal(t, int64(1), redisClient.Exists(ctx, session2).Val(), "session2 should remain")

	// Cleanup
	redisClient.Del(ctx, session2)
}

// TestInvalidateCredentialSessions_MalformedJSON_NocrashContinues verifies that
// malformed JSON in session payloads is logged and skipped without crashing.
func TestInvalidateCredentialSessions_MalformedJSON_NocrashContinues(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := &mockConfig{
		redisAddr:     "localhost:6379",
		redisPassword: "",
		redisDB:       0,
	}

	client, err := NewClient(ctx, cfg)
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	defer client.Close()

	redisClient := client.GetClient()
	cleanup := NewCleanupRepository(redisClient)

	profileID := "test-invalidate-malformed"
	keyID := "key-to-invalidate"

	// Create sessions with various payloads
	sessionValid := fmt.Sprintf("profile:%s:session:valid", profileID)
	sessionMalformed := fmt.Sprintf("profile:%s:session:malformed", profileID)
	sessionInvalidJSON := fmt.Sprintf("profile:%s:session:invalid-json", profileID)

	validPayload, _ := json.Marshal(map[string]string{"key_id": keyID})
	malformedPayload := "not-json-at-all"
	invalidJSONPayload := `{invalid: json, missing: quotes}`

	require.NoError(t, redisClient.Set(ctx, sessionValid, string(validPayload), 5*time.Minute).Err())
	require.NoError(t, redisClient.Set(ctx, sessionMalformed, malformedPayload, 5*time.Minute).Err())
	require.NoError(t, redisClient.Set(ctx, sessionInvalidJSON, invalidJSONPayload, 5*time.Minute).Err())

	// Invalidate should not crash on malformed JSON
	err = cleanup.InvalidateCredentialSessions(ctx, profileID, keyID)
	require.NoError(t, err, "InvalidateCredentialSessions should not return error on malformed JSON")

	// Valid session with matching key_id should be deleted
	assert.Equal(t, int64(0), redisClient.Exists(ctx, sessionValid).Val(), "valid session should be deleted")
	// Malformed sessions should be left as-is (or could be deleted depending on implementation)
	// The key point is: no crash and the valid matching key was deleted
}

// TestInvalidateCredentialSessions_LeavesOtherKeys verifies that invalidating sessions
// for one key does not affect sessions from other profiles.
func TestInvalidateCredentialSessions_LeavesOtherKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg := &mockConfig{
		redisAddr:     "localhost:6379",
		redisPassword: "",
		redisDB:       0,
	}

	client, err := NewClient(ctx, cfg)
	if err != nil {
		t.Skipf("Redis not available: %v", err)
	}
	defer client.Close()

	redisClient := client.GetClient()
	cleanup := NewCleanupRepository(redisClient)

	profileID1 := "test-invalidate-profile1"
	profileID2 := "test-invalidate-profile2"
	keyID := "shared-key-id"

	// Create sessions with same key_id in different profiles
	session1 := fmt.Sprintf("profile:%s:session:session1", profileID1)
	session2 := fmt.Sprintf("profile:%s:session:session2", profileID2)

	payload, _ := json.Marshal(map[string]string{"key_id": keyID})

	require.NoError(t, redisClient.Set(ctx, session1, string(payload), 5*time.Minute).Err())
	require.NoError(t, redisClient.Set(ctx, session2, string(payload), 5*time.Minute).Err())

	// Invalidate sessions for profileID1 only
	err = cleanup.InvalidateCredentialSessions(ctx, profileID1, keyID)
	require.NoError(t, err)

	// Verify profileID1 session is deleted
	assert.Equal(t, int64(0), redisClient.Exists(ctx, session1).Val(), "profile1 session should be deleted")
	// Verify profileID2 session is NOT deleted
	assert.Equal(t, int64(1), redisClient.Exists(ctx, session2).Val(), "profile2 session should remain")

	// Cleanup
	redisClient.Del(ctx, session2)
}
