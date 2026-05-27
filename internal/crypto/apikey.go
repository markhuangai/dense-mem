package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters
const (
	argon2Memory          = 65536 // 64 MB
	argon2Time            = 3
	argon2Threads         = 4
	argon2SaltLength      = 16
	argon2KeyLength       = 32
	keyPrefix             = "dm_live_"
	keyPrefixLength       = 24 // first 24 chars of the raw key (including prefix)
	legacyKeyPrefixLength = 12
	keySuffixLength       = 6
)

// GenerateRawKey generates a new raw API key.
// Format: dm_live_ + base64url(32 random bytes)
// The key_prefix is the first 24 characters of the full raw key string.
func GenerateRawKey() (string, error) {
	// Generate 32 random bytes
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Encode with base64url (no padding)
	encoded := base64.RawURLEncoding.EncodeToString(randomBytes)

	// Prefix with dm_live_
	fullKey := keyPrefix + encoded

	return fullKey, nil
}

// GetKeyPrefix extracts the first 24 characters of the raw key.
func GetKeyPrefix(rawKey string) string {
	if len(rawKey) < keyPrefixLength {
		return rawKey
	}
	return rawKey[:keyPrefixLength]
}

// GetLookupPrefixes returns current and legacy key-prefix candidates.
func GetLookupPrefixes(rawKey string) []string {
	prefixes := []string{}
	if len(rawKey) >= keyPrefixLength {
		prefixes = append(prefixes, rawKey[:keyPrefixLength])
	}
	if len(rawKey) >= legacyKeyPrefixLength {
		legacy := rawKey[:legacyKeyPrefixLength]
		if len(prefixes) == 0 || prefixes[0] != legacy {
			prefixes = append(prefixes, legacy)
		}
	}
	return prefixes
}

// GetKeySuffix extracts the last 6 characters of the raw key for display.
func GetKeySuffix(rawKey string) string {
	if len(rawKey) < keySuffixLength {
		return rawKey
	}
	return rawKey[len(rawKey)-keySuffixLength:]
}

// HashKey hashes a raw API key using Argon2id and returns a PHC string.
// Format: $argon2id$v=19$m=65536,t=3,p=4$<salt>$<hash>
func HashKey(rawKey string) (string, error) {
	// Generate random salt
	salt := make([]byte, argon2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("failed to generate salt: %w", err)
	}

	// Hash the raw key (without the prefix)
	hash := argon2.IDKey([]byte(rawKey), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLength)

	// Encode as PHC string
	phc := fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		argon2Memory,
		argon2Time,
		argon2Threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	)

	return phc, nil
}

// VerifyKey verifies a raw key against a PHC-encoded hash.
// Uses constant-time comparison to prevent timing attacks.
func VerifyKey(rawKey, phcHash string) bool {
	// Parse the PHC string
	parts := strings.Split(phcHash, "$")
	if len(parts) != 6 {
		return false
	}

	// parts[0] is empty (leading $)
	// parts[1] should be "argon2id"
	// parts[2] should be "v=19"
	// parts[3] should be "m=65536,t=3,p=4"
	// parts[4] is the salt (base64)
	// parts[5] is the hash (base64)

	if parts[1] != "argon2id" {
		return false
	}

	// Decode salt
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}

	// Decode expected hash
	expectedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}

	// Recompute the hash with the same parameters
	computedHash := argon2.IDKey([]byte(rawKey), salt, argon2Time, argon2Memory, argon2Threads, uint32(len(expectedHash)))

	// Constant-time comparison
	return subtle.ConstantTimeCompare(computedHash, expectedHash) == 1
}
