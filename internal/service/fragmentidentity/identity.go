// Package fragmentidentity provides fragment identity generation and content hashing.
package fragmentidentity

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/google/uuid"
)

// HashResult contains the result of a content hash computation.
type HashResult struct {
	Hex string // lowercase SHA-256 hex
}

// NewFragmentID returns a new UUIDv7 (time-ordered) string.
// UUIDv7 is time-ordered so created_at queries line up nicely with id-lex order (AC-20).
func NewFragmentID() string {
	// UUIDv7 is available in google/uuid v1.6.0+
	id, err := uuid.NewV7()
	if err != nil {
		// Fallback to v4 if v7 fails (should not happen in practice)
		return uuid.New().String()
	}
	return id.String()
}

// ContentHash returns the SHA-256 hex of the raw UTF-8 bytes of content
// (no trimming, no case folding — AC-20, AC-43).
// The hash is computed over []byte(content) directly, without any normalization.
func ContentHash(content string) HashResult {
	hash := sha256.Sum256([]byte(content))
	return HashResult{
		Hex: hex.EncodeToString(hash[:]),
	}
}
