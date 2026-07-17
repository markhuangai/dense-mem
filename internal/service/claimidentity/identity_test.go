package claimidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// profileA and profileB are fixed UUIDs used across tests to verify
// cross-profile isolation.
const (
	profileA = "00000000-0000-0000-0000-000000000001"
	profileB = "00000000-0000-0000-0000-000000000002"
)

// ---------------------------------------------------------------------------
// ContentHash
// ---------------------------------------------------------------------------

// TestDeterministicClaimID verifies the core determinism contract for all
// three identity functions: same inputs → same output, always (AC-9).
func TestDeterministicClaimID(t *testing.T) {
	t.Run("ContentHash is deterministic", func(t *testing.T) {
		ts := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
		h1 := ContentHash("subject", "predicate", "object", &ts, nil)
		h2 := ContentHash("subject", "predicate", "object", &ts, nil)
		require.Equal(t, h1, h2, "same inputs must produce same content hash")
		require.NotEmpty(t, h1, "content hash must not be empty")
	})

	t.Run("ContentHash without validFrom is deterministic", func(t *testing.T) {
		h1 := ContentHash("s", "p", "o", nil, nil)
		h2 := ContentHash("s", "p", "o", nil, nil)
		require.Equal(t, h1, h2)
	})

	t.Run("ClaimID is deterministic", func(t *testing.T) {
		id1, err := ClaimID(profileA, "idem-key-abc")
		require.NoError(t, err)
		id2, err := ClaimID(profileA, "idem-key-abc")
		require.NoError(t, err)
		require.Equal(t, id1, id2, "same profileID + idempotencyKey must produce same ClaimID")
	})

	t.Run("ClaimIDFromHash is deterministic", func(t *testing.T) {
		hash := ContentHash("subject", "predicate", "object", nil, nil)
		id1, err := ClaimIDFromHash(profileA, hash)
		require.NoError(t, err)
		id2, err := ClaimIDFromHash(profileA, hash)
		require.NoError(t, err)
		require.Equal(t, id1, id2, "same profileID + contentHash must produce same ClaimIDFromHash")
	})

	t.Run("different inputs produce different IDs", func(t *testing.T) {
		id1, err := ClaimID(profileA, "key-1")
		require.NoError(t, err)
		id2, err := ClaimID(profileA, "key-2")
		require.NoError(t, err)
		require.NotEqual(t, id1, id2, "different idempotency keys must produce different IDs")
	})
}

// ---------------------------------------------------------------------------
// ContentHash internals
// ---------------------------------------------------------------------------

// TestContentHash_CanonicalForm verifies the canonical concatenation format.
func TestContentHash_CanonicalForm(t *testing.T) {
	ts := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	expected := sha256.Sum256([]byte("subj|pred|obj|2024-06-01T00:00:00Z|"))
	want := hex.EncodeToString(expected[:])
	got := ContentHash("subj", "pred", "obj", &ts, nil)
	assert.Equal(t, want, got, "canonical form must be subject|predicate|object|validFromUTC|validToUTC")
}

// TestContentHash_NilValidFrom verifies that nil validity bounds produce empty
// canonical segments.
func TestContentHash_NilValidFrom(t *testing.T) {
	// Canonical input must be "s|p|o||" (empty valid_from and valid_to, NOT "none").
	expected := sha256.Sum256([]byte("s|p|o||"))
	want := hex.EncodeToString(expected[:])
	got := ContentHash("s", "p", "o", nil, nil)
	assert.Equal(t, want, got, "nil validity bounds must produce empty canonical segments")

	// Regression guard: the new hash must differ from the old "none" sentinel.
	oldHash := hex.EncodeToString(func() []byte {
		h := sha256.Sum256([]byte("s|p|o|none"))
		return h[:]
	}())
	assert.NotEqual(t, oldHash, got,
		"nil-validFrom hash must differ from the deprecated 'none' sentinel hash (silent regression guard)")
}

func TestContentHash_PreservesSubsecondPrecision(t *testing.T) {
	a := time.Date(2024, 1, 15, 12, 0, 0, 1, time.UTC)
	b := time.Date(2024, 1, 15, 12, 0, 0, 2, time.UTC)

	assert.NotEqual(t,
		ContentHash("s", "p", "o", &a, nil),
		ContentHash("s", "p", "o", &b, nil),
		"validFrom nanoseconds must participate in the content hash",
	)
	assert.NotEqual(t,
		ContentHash("s", "p", "o", nil, &a),
		ContentHash("s", "p", "o", nil, &b),
		"validTo nanoseconds must participate in the content hash",
	)
}

func TestLegacyContentHashWithoutValidTo(t *testing.T) {
	ts := time.Date(2024, 6, 1, 0, 0, 0, 123, time.UTC)
	expected := sha256.Sum256([]byte("s|p|o|2024-06-01T00:00:00Z"))
	want := hex.EncodeToString(expected[:])

	got := LegacyContentHashWithoutValidTo("s", "p", "o", &ts)
	assert.Equal(t, want, got, "legacy compatibility hash must match the old four-field canonical form")
	assert.NotEqual(t, got, ContentHash("s", "p", "o", &ts, nil))
}

// TestContentHash_ValidFromNormalizedToUTC verifies timezone normalization.
func TestContentHash_ValidFromNormalizedToUTC(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	// 2024-01-15 07:00:00 EST == 2024-01-15 12:00:00 UTC
	eastern := time.Date(2024, 1, 15, 7, 0, 0, 0, loc)
	utc := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)

	hEast := ContentHash("s", "p", "o", &eastern, nil)
	hUTC := ContentHash("s", "p", "o", &utc, nil)
	assert.Equal(t, hEast, hUTC, "validFrom must be normalised to UTC before hashing")
}

func TestContentHash_ValidToNormalizedAndDistinct(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	eastern := time.Date(2024, 1, 16, 7, 0, 0, 0, loc)
	utc := time.Date(2024, 1, 16, 12, 0, 0, 0, time.UTC)
	other := time.Date(2024, 1, 17, 12, 0, 0, 0, time.UTC)

	hEast := ContentHash("s", "p", "o", nil, &eastern)
	hUTC := ContentHash("s", "p", "o", nil, &utc)
	hOther := ContentHash("s", "p", "o", nil, &other)
	assert.Equal(t, hEast, hUTC, "validTo must be normalised to UTC before hashing")
	assert.NotEqual(t, hUTC, hOther, "validTo must participate in the content hash")
}

// TestContentHash_LowercaseHex verifies output format.
func TestContentHash_LowercaseHex(t *testing.T) {
	h := ContentHash("test", "is", "running", nil, nil)
	assert.Equal(t, strings.ToLower(h), h, "hex output must be lowercase")
	assert.Len(t, h, 64, "SHA-256 hex must be 64 characters")
}

// TestContentHash_SensitiveToFieldChanges verifies that changing any field
// changes the hash.
func TestContentHash_SensitiveToFieldChanges(t *testing.T) {
	base := ContentHash("subject", "predicate", "object", nil, nil)

	cases := []struct {
		name string
		hash string
	}{
		{"changed subject", ContentHash("SUBJECT", "predicate", "object", nil, nil)},
		{"changed predicate", ContentHash("subject", "PREDICATE", "object", nil, nil)},
		{"changed object", ContentHash("subject", "predicate", "OBJECT", nil, nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.NotEqual(t, base, tc.hash, "changing %s must change hash", tc.name)
		})
	}
}

// ---------------------------------------------------------------------------
// ClaimID
// ---------------------------------------------------------------------------

// TestClaimID_InvalidProfileID returns an error for non-UUID profile IDs.
func TestClaimID_InvalidProfileID(t *testing.T) {
	_, err := ClaimID("not-a-uuid", "key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profileID is not a valid UUID")
}

// TestClaimID_EmptyIdempotencyKey is a valid degenerate case.
func TestClaimID_EmptyIdempotencyKey(t *testing.T) {
	id, err := ClaimID(profileA, "")
	require.NoError(t, err)
	require.Len(t, id, 36, "must return a valid UUID string")
}

// ---------------------------------------------------------------------------
// ClaimIDFromHash
// ---------------------------------------------------------------------------

// TestClaimIDFromHash_InvalidProfileID returns an error for non-UUID profile IDs.
func TestClaimIDFromHash_InvalidProfileID(t *testing.T) {
	_, err := ClaimIDFromHash("bad-id", "hashvalue")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profileID is not a valid UUID")
}

// TestClaimIDFromHash_ProducesValidUUID verifies the output format.
func TestClaimIDFromHash_ProducesValidUUID(t *testing.T) {
	hash := ContentHash("subject", "predicate", "object", nil, nil)
	id, err := ClaimIDFromHash(profileA, hash)
	require.NoError(t, err)
	assert.Len(t, id, 36, "ClaimIDFromHash must return a 36-character UUID string")

	parts := strings.Split(id, "-")
	require.Len(t, parts, 5, "UUID must have 5 hyphen-separated parts")
}

// ---------------------------------------------------------------------------
// ValidateClaimIdentityInputs — pre-hash field-length guard
// ---------------------------------------------------------------------------

// TestValidateClaimIdentityInputs_Valid passes on well-formed inputs.
func TestValidateClaimIdentityInputs_Valid(t *testing.T) {
	err := ValidateClaimIdentityInputs(profileA, "subject", "predicate", "object", "idem-key")
	require.NoError(t, err)
}

// TestValidateClaimIdentityInputs_InvalidProfileID rejects a non-UUID profileID.
func TestValidateClaimIdentityInputs_InvalidProfileID(t *testing.T) {
	err := ValidateClaimIdentityInputs("not-a-uuid", "s", "p", "o", "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "profileID must be a valid UUID")
}

// TestValidateClaimIdentityInputs_FieldTooLong rejects oversized fields.
func TestValidateClaimIdentityInputs_FieldTooLong(t *testing.T) {
	over := strings.Repeat("x", MaxSubjectBytes+1)
	cases := []struct {
		name          string
		subject       string
		predicate     string
		object        string
		idempotency   string
		wantSubstring string
	}{
		{"subject too long", over, "p", "o", "", "subject exceeds"},
		{"predicate too long", "s", over, "o", "", "predicate exceeds"},
		{"object too long", "s", "p", over, "", "object exceeds"},
		{"idempotency too long", "s", "p", "o", strings.Repeat("k", MaxIdempotencyBytes+1), "idempotencyKey exceeds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateClaimIdentityInputs(profileA, tc.subject, tc.predicate, tc.object, tc.idempotency)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSubstring)
		})
	}
}

// TestValidateClaimIdentityInputs_EmptyIdempotencyAllowed verifies that an
// empty idempotency key is a valid (content-hash path) input.
func TestValidateClaimIdentityInputs_EmptyIdempotencyAllowed(t *testing.T) {
	err := ValidateClaimIdentityInputs(profileA, "s", "p", "o", "")
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Cross-profile isolation — REQUIRED by AGENTS.md
// ---------------------------------------------------------------------------

// TestDeterministicClaimID_CrossProfileIsolation verifies that the same
// idempotency key or content hash generates different IDs for different
// profiles. Data from profile A must not bleed into profile B's identity
// space.
func TestDeterministicClaimID_CrossProfileIsolation(t *testing.T) {
	const sharedKey = "shared-idempotency-key"

	idA, err := ClaimID(profileA, sharedKey)
	require.NoError(t, err)
	idB, err := ClaimID(profileB, sharedKey)
	require.NoError(t, err)

	require.NotEqual(t, idA, idB,
		"same idempotency key must yield different ClaimIDs for different profiles")

	// Profile B results must not contain profile A's ID.
	bResults := []string{idB}
	require.NotContains(t, bResults, idA,
		"profile A ID must not appear in profile B result set")
}

// TestClaimIDFromHash_CrossProfileIsolation mirrors the above for the
// content-hash path.
func TestClaimIDFromHash_CrossProfileIsolation(t *testing.T) {
	hash := ContentHash("subject", "predicate", "object", nil, nil)

	idA, err := ClaimIDFromHash(profileA, hash)
	require.NoError(t, err)
	idB, err := ClaimIDFromHash(profileB, hash)
	require.NoError(t, err)

	require.NotEqual(t, idA, idB,
		"same content hash must yield different ClaimIDs for different profiles")

	bResults := []string{idB}
	require.NotContains(t, bResults, idA)
}
