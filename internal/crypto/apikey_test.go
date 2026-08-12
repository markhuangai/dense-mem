package crypto

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArgon2VerifierRejectsNonCanonicalParameters(t *testing.T) {
	rawKey, err := GenerateRawKey()
	assert.NoError(t, err)
	hash, err := HashKey(rawKey)
	assert.NoError(t, err)
	verifier := NewArgon2Verifier(2)

	valid, err := verifier.Verify(context.Background(), rawKey, hash)
	assert.NoError(t, err)
	assert.True(t, valid)

	for _, tampered := range []string{
		strings.Replace(hash, "m=65536", "m=1", 1),
		strings.Replace(hash, "t=3", "t=1", 1),
		strings.Replace(hash, "p=4", "p=1", 1),
		strings.Replace(hash, "v=19", "v=18", 1),
	} {
		valid, err = verifier.Verify(context.Background(), rawKey, tampered)
		assert.NoError(t, err)
		assert.False(t, valid)
	}
}

// TestGenerateAPIKeyFormat verifies the raw key format
func TestGenerateAPIKeyFormat(t *testing.T) {
	// Generate multiple keys to ensure consistency
	for i := 0; i < 10; i++ {
		key, err := GenerateRawKey()
		require.NoError(t, err, "GenerateRawKey should not return an error")

		// Check prefix
		assert.True(t, strings.HasPrefix(key, "dm_live_"), "Key should start with 'dm_live_'")

		// Check prefix extraction (first 24 chars)
		prefix := GetKeyPrefix(key)
		assert.Equal(t, 24, len(prefix), "Prefix should be 24 characters")
		assert.Equal(t, key[:24], prefix, "Prefix should be first 24 characters of key")

		suffix := GetKeySuffix(key)
		assert.Equal(t, 6, len(suffix), "Suffix should be 6 characters")
		assert.Equal(t, key[len(key)-6:], suffix, "Suffix should be last 6 characters of key")

		// Check that it's valid base64url after the prefix
		encodedPart := strings.TrimPrefix(key, "dm_live_")
		assert.NotEmpty(t, encodedPart, "Encoded part should not be empty")

		// Verify no padding characters (base64url uses no padding)
		assert.NotContains(t, encodedPart, "=", "Base64url should not contain padding")
	}
}

func TestGetLookupPrefixesIncludesLegacyPrefix(t *testing.T) {
	rawKey := "dm_live_12345678901234567890"

	prefixes := GetLookupPrefixes(rawKey)

	require.Len(t, prefixes, 2)
	assert.Equal(t, rawKey[:24], prefixes[0])
	assert.Equal(t, rawKey[:12], prefixes[1])
}

func TestGetKeySuffixShortKey(t *testing.T) {
	assert.Equal(t, "short", GetKeySuffix("short"))
}

func TestProfileKeySlugAndPrefixBranches(t *testing.T) {
	assert.Equal(t, "my-team-name", KeySlug("  My_Team!!Name  "))
	assert.Equal(t, "very-long-te", KeySlug("very-long-team-name-that-is-capped"))
	assert.Equal(t, "", KeySlug("!!!"))
	assert.Equal(t, "short", GetKeyPrefix("short"))

	key, err := GenerateRawKeyForProfile("Research Team")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(key, "dm_research-tea_"))

	key, err = GenerateRawKeyForProfile("!!!")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(key, "dm_profile_"))

	assert.Empty(t, GetLookupPrefixes("short"))
}

// TestVerifyAPIKeyCorrect verifies that a valid key passes verification
func TestVerifyAPIKeyCorrect(t *testing.T) {
	rawKey, err := GenerateRawKey()
	require.NoError(t, err)

	hash, err := HashKey(rawKey)
	require.NoError(t, err)

	// Verify the correct key passes
	assert.True(t, VerifyKey(rawKey, hash), "Correct key should verify")
}

// TestVerifyAPIKeyWrongKey verifies that a wrong key fails verification
func TestVerifyAPIKeyWrongKey(t *testing.T) {
	rawKey1, err := GenerateRawKey()
	require.NoError(t, err)

	rawKey2, err := GenerateRawKey()
	require.NoError(t, err)

	hash, err := HashKey(rawKey1)
	require.NoError(t, err)

	// Verify wrong key fails
	assert.False(t, VerifyKey(rawKey2, hash), "Wrong key should not verify")
}

// TestVerifyAPIKeyTampered verifies that a tampered hash fails verification
func TestVerifyAPIKeyTampered(t *testing.T) {
	rawKey, err := GenerateRawKey()
	require.NoError(t, err)

	hash, err := HashKey(rawKey)
	require.NoError(t, err)

	tamperedHash := strings.Replace(hash, "$argon2id$", "$argon2i$", 1)

	// Verify tampered hash fails
	assert.False(t, VerifyKey(rawKey, tamperedHash), "Tampered hash should not verify")
}

// TestHashKeyFormat verifies PHC string format
func TestHashKeyFormat(t *testing.T) {
	rawKey, err := GenerateRawKey()
	require.NoError(t, err)

	hash, err := HashKey(rawKey)
	require.NoError(t, err)

	// Verify PHC format: $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
	assert.True(t, strings.HasPrefix(hash, "$argon2id$v=19$"), "Hash should have correct algorithm and version prefix")
	assert.Contains(t, hash, "m=65536", "Hash should contain memory parameter")
	assert.Contains(t, hash, "t=3", "Hash should contain time parameter")
	assert.Contains(t, hash, "p=4", "Hash should contain threads parameter")

	// Count $ separators (should be 6 for PHC format)
	parts := strings.Split(hash, "$")
	assert.Equal(t, 6, len(parts), "PHC hash should have 6 parts separated by $")
}

// TestVerifyKeyInvalidHash verifies various invalid hash formats
func TestVerifyKeyInvalidHash(t *testing.T) {
	rawKey, err := GenerateRawKey()
	require.NoError(t, err)

	// Test empty hash
	assert.False(t, VerifyKey(rawKey, ""), "Empty hash should not verify")

	// Test invalid format
	assert.False(t, VerifyKey(rawKey, "invalid"), "Invalid hash format should not verify")

	// Test wrong algorithm
	assert.False(t, VerifyKey(rawKey, "$argon2i$v=19$m=65536,t=3,p=4$abc$def"), "Wrong algorithm should not verify")

	// Test malformed PHC
	assert.False(t, VerifyKey(rawKey, "$argon2id$v=19$m=65536"), "Malformed PHC should not verify")

	// Test malformed base64 salt/hash with otherwise correct PHC shape
	assert.False(t, VerifyKey(rawKey, "$argon2id$v=19$m=65536,t=3,p=4$not base64$def"))
	assert.False(t, VerifyKey(rawKey, "$argon2id$v=19$m=65536,t=3,p=4$YWJj$not base64"))
}
