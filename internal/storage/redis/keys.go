package redis

import (
	"errors"
)

// KeyBuilderInterface is the companion interface for KeyBuilder.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type KeyBuilderInterface interface {
	RateLimit(profileID, identifier string) (string, error)
	Stream(profileID, identifier string) (string, error)
}

// validCategories contains the allowed key categories.
var validCategories = map[string]bool{
	"ratelimit": true,
	"stream":    true,
}

// Errors for key validation
var (
	ErrEmptyProfileID  = errors.New("profileID cannot be empty")
	ErrInvalidCategory = errors.New("category must be one of: ratelimit, stream")
	ErrEmptyIdentifier = errors.New("identifier cannot be empty")
)

// KeyBuilder constructs Redis keys with mandatory profile prefix.
// This ensures isolation between profiles and prevents cross-profile key collisions.
type KeyBuilder struct{}

// Ensure KeyBuilder implements KeyBuilderInterface
var _ KeyBuilderInterface = (*KeyBuilder)(nil)

// NewKeyBuilder creates a new KeyBuilder instance.
func NewKeyBuilder() *KeyBuilder {
	return &KeyBuilder{}
}

// buildKey constructs a key with the format: profile:{profileID}:{category}:{identifier}
// It validates that profileID is not empty and the category is valid.
func (kb *KeyBuilder) buildKey(profileID, category, identifier string) (string, error) {
	if profileID == "" {
		return "", ErrEmptyProfileID
	}
	if !validCategories[category] {
		return "", ErrInvalidCategory
	}
	if identifier == "" {
		return "", ErrEmptyIdentifier
	}
	return "profile:" + profileID + ":" + category + ":" + identifier, nil
}

// RateLimit constructs a key for rate limiting with format: profile:{profileID}:ratelimit:{identifier}
func (kb *KeyBuilder) RateLimit(profileID, identifier string) (string, error) {
	return kb.buildKey(profileID, "ratelimit", identifier)
}

// Stream constructs a key for streams with format: profile:{profileID}:stream:{identifier}
func (kb *KeyBuilder) Stream(profileID, identifier string) (string, error) {
	return kb.buildKey(profileID, "stream", identifier)
}
