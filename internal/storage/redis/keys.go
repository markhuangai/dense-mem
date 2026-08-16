package redis

import (
	"errors"
)

// KeyBuilderInterface is the companion interface for KeyBuilder.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type KeyBuilderInterface interface {
	RateLimit(namespaceID, identifier string) (string, error)
	Stream(namespaceID, identifier string) (string, error)
}

// validCategories contains the allowed key categories.
var validCategories = map[string]bool{
	"ratelimit": true,
	"stream":    true,
}

// Errors for key validation
var (
	ErrEmptyNamespaceID = errors.New("namespace ID cannot be empty")
	ErrInvalidCategory  = errors.New("category must be one of: ratelimit, stream")
	ErrEmptyIdentifier  = errors.New("identifier cannot be empty")
)

// KeyBuilder constructs Redis keys with the deployed profile prefix.
// The namespace value isolates teams or credential-scoped coordination state.
type KeyBuilder struct{}

// Ensure KeyBuilder implements KeyBuilderInterface
var _ KeyBuilderInterface = (*KeyBuilder)(nil)

// NewKeyBuilder creates a new KeyBuilder instance.
func NewKeyBuilder() *KeyBuilder {
	return &KeyBuilder{}
}

// buildKey preserves the deployed profile:{id}:{category}:{identifier} format.
func (kb *KeyBuilder) buildKey(namespaceID, category, identifier string) (string, error) {
	if namespaceID == "" {
		return "", ErrEmptyNamespaceID
	}
	if !validCategories[category] {
		return "", ErrInvalidCategory
	}
	if identifier == "" {
		return "", ErrEmptyIdentifier
	}
	return "profile:" + namespaceID + ":" + category + ":" + identifier, nil
}

// RateLimit constructs a key with the deployed profile:{namespaceID}:ratelimit:{identifier} format.
func (kb *KeyBuilder) RateLimit(namespaceID, identifier string) (string, error) {
	return kb.buildKey(namespaceID, "ratelimit", identifier)
}

// Stream constructs a key with the deployed profile:{namespaceID}:stream:{identifier} format.
func (kb *KeyBuilder) Stream(namespaceID, identifier string) (string, error) {
	return kb.buildKey(namespaceID, "stream", identifier)
}
