// Package claimidentity provides deterministic claim identity generation and
// pre-hash field-length guards for the knowledge-pipeline claim stage.
//
// Determinism invariant: given the same inputs, every exported function MUST
// return the same output — no randomness, no clock reads inside hash paths.
//
// Security invariant: ValidateClaimIdentityInputs MUST be called before any
// hashing function to bound input size and prevent DoS via unbounded hashing.
package claimidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Maximum byte lengths for claim identity inputs.
// These caps are the pre-hash field-length guard flagged by the security
// executor. Callers MUST invoke ValidateClaimIdentityInputs before hashing.
const (
	MaxSubjectBytes     = 8192
	MaxPredicateBytes   = 8192
	MaxObjectBytes      = 8192
	MaxIdempotencyBytes = 512
)

// ContentHash returns the lowercase SHA-256 hex of the canonical form:
//
//	subject + "|" + predicate + "|" + object + "|" + normalized_valid_from + "|" + normalized_valid_to
//
// normalized validity values are RFC 3339 Nano UTC representations when
// non-nil, or empty strings when nil.
//
// No trimming or case-folding is applied to the input fields — the caller is
// responsible for normalizing before invoking this function.
func ContentHash(subject, predicate, object string, validFrom, validTo *time.Time) string {
	normalizedValidFrom := normalizedContentHashTime(validFrom, time.RFC3339Nano)
	normalizedValidTo := normalizedContentHashTime(validTo, time.RFC3339Nano)
	input := subject + "|" + predicate + "|" + object + "|" + normalizedValidFrom + "|" + normalizedValidTo
	return hashInput(input)
}

// LegacyContentHashWithoutValidTo returns the pre-valid_to content hash shape.
//
// This exists only for compatibility lookup against claims written before
// valid_to participated in content hashing.
func LegacyContentHashWithoutValidTo(subject, predicate, object string, validFrom *time.Time) string {
	normalizedValidFrom := normalizedContentHashTime(validFrom, time.RFC3339)
	input := subject + "|" + predicate + "|" + object + "|" + normalizedValidFrom
	return hashInput(input)
}

func normalizedContentHashTime(value *time.Time, layout string) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(layout)
}

func hashInput(input string) string {
	hash := sha256.Sum256([]byte(input))
	return hex.EncodeToString(hash[:])
}

// ClaimID returns a UUIDv5 string derived from the given profileID and
// idempotencyKey.
//
// Namespace: the profileID parsed as a UUID (tenant-scoped).
// Name:      idempotencyKey bytes.
//
// Two calls with the same profileID and idempotencyKey ALWAYS return the same
// value. Two calls with different profileIDs but the same key return different
// values — cross-profile isolation is enforced by the namespace.
func ClaimID(profileID string, idempotencyKey string) (string, error) {
	ns, err := uuid.Parse(profileID)
	if err != nil {
		return "", fmt.Errorf("claimidentity: profileID is not a valid UUID: %w", err)
	}
	// uuid.NewSHA1 is UUIDv5 (SHA-1-based, RFC 4122 §4.3).
	id := uuid.NewSHA1(ns, []byte(idempotencyKey))
	return id.String(), nil
}

// ClaimIDFromHash returns a UUIDv5 string derived from the given profileID and
// contentHash (as returned by ContentHash).
//
// Namespace: the profileID parsed as a UUID (tenant-scoped).
// Name:      contentHash bytes.
//
// This is used when no idempotency key is supplied; the content hash drives
// determinism instead.
func ClaimIDFromHash(profileID string, contentHash string) (string, error) {
	ns, err := uuid.Parse(profileID)
	if err != nil {
		return "", fmt.Errorf("claimidentity: profileID is not a valid UUID: %w", err)
	}
	id := uuid.NewSHA1(ns, []byte(contentHash))
	return id.String(), nil
}

// ValidateClaimIdentityInputs is the pre-hash field-length guard.
//
// It must be called before ContentHash, ClaimID, or ClaimIDFromHash to ensure
// that no unbounded input reaches the hash functions. Pass an empty string for
// idempotencyKey when no key is present.
//
// Invariant: a non-nil return value means no hashing should proceed.
func ValidateClaimIdentityInputs(profileID, subject, predicate, object, idempotencyKey string) error {
	if _, err := uuid.Parse(profileID); err != nil {
		return errors.New("claimidentity: profileID must be a valid UUID")
	}
	if len([]byte(subject)) > MaxSubjectBytes {
		return fmt.Errorf("claimidentity: subject exceeds maximum of %d bytes", MaxSubjectBytes)
	}
	if len([]byte(predicate)) > MaxPredicateBytes {
		return fmt.Errorf("claimidentity: predicate exceeds maximum of %d bytes", MaxPredicateBytes)
	}
	if len([]byte(object)) > MaxObjectBytes {
		return fmt.Errorf("claimidentity: object exceeds maximum of %d bytes", MaxObjectBytes)
	}
	if len([]byte(idempotencyKey)) > MaxIdempotencyBytes {
		return fmt.Errorf("claimidentity: idempotencyKey exceeds maximum of %d bytes", MaxIdempotencyBytes)
	}
	return nil
}
