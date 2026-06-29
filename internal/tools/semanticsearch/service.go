package semanticsearch

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"
)

const (
	// DefaultLimit is the default limit for search results.
	DefaultLimit = 10
	// MaxLimit is the maximum allowed limit for search results.
	MaxLimit = 100
)

// SearchHit represents a single search result hit.
type SearchHit struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"` // "fragment" or "fact"
	Content   string         `json:"content"`
	Score     float64        `json:"score"`
	Labels    []string       `json:"labels"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	ProfileID string         `json:"team_id"` // For defense-in-depth post-filter
	CreatedAt *time.Time     `json:"created_at,omitempty"`
	UpdatedAt *time.Time     `json:"updated_at,omitempty"`
}

// SearchHitInterface is the companion interface for SearchHit.
type SearchHitInterface interface {
	GetID() string
	GetType() string
	GetContent() string
	GetScore() float64
	GetLabels() []string
	GetMetadata() map[string]any
	GetProfileID() string
}

// Ensure SearchHit implements SearchHitInterface.
var _ SearchHitInterface = (*SearchHit)(nil)

// GetID returns the hit ID.
func (h *SearchHit) GetID() string {
	return h.ID
}

// GetType returns the hit type.
func (h *SearchHit) GetType() string {
	return h.Type
}

// GetContent returns the hit content.
func (h *SearchHit) GetContent() string {
	return h.Content
}

// GetScore returns the hit score.
func (h *SearchHit) GetScore() float64 {
	return h.Score
}

// GetLabels returns the hit labels.
func (h *SearchHit) GetLabels() []string {
	return h.Labels
}

// GetMetadata returns the hit metadata.
func (h *SearchHit) GetMetadata() map[string]any {
	return h.Metadata
}

// GetProfileID returns the hit profile ID.
func (h *SearchHit) GetProfileID() string {
	return h.ProfileID
}

// SemanticSearchRequest represents the request for semantic search.
type SemanticSearchRequest struct {
	Embedding []float32 `json:"embedding"`
	Query     string    `json:"query,omitempty"` // Optional, for logging/debugging
	Limit     int       `json:"limit"`
	Threshold float64   `json:"threshold,omitempty"` // Optional similarity threshold
}

// SemanticSearchRequestInterface is the companion interface for SemanticSearchRequest.
type SemanticSearchRequestInterface interface {
	GetEmbedding() []float32
	GetQuery() string
	GetLimit() int
	GetThreshold() float64
}

// Ensure SemanticSearchRequest implements SemanticSearchRequestInterface.
var _ SemanticSearchRequestInterface = (*SemanticSearchRequest)(nil)

// GetEmbedding returns the embedding vector.
func (r *SemanticSearchRequest) GetEmbedding() []float32 {
	return r.Embedding
}

// GetQuery returns the query string.
func (r *SemanticSearchRequest) GetQuery() string {
	return r.Query
}

// GetLimit returns the limit.
func (r *SemanticSearchRequest) GetLimit() int {
	return r.Limit
}

// GetThreshold returns the similarity threshold.
func (r *SemanticSearchRequest) GetThreshold() float64 {
	return r.Threshold
}

// SemanticSearchMeta represents metadata about the search.
type SemanticSearchMeta struct {
	LimitApplied int `json:"limit_applied"`
}

// SemanticSearchMetaInterface is the companion interface for SemanticSearchMeta.
type SemanticSearchMetaInterface interface {
	GetLimitApplied() int
}

// Ensure SemanticSearchMeta implements SemanticSearchMetaInterface.
var _ SemanticSearchMetaInterface = (*SemanticSearchMeta)(nil)

// GetLimitApplied returns the applied limit.
func (m *SemanticSearchMeta) GetLimitApplied() int {
	return m.LimitApplied
}

// SemanticSearchResult represents the result of a semantic search.
type SemanticSearchResult struct {
	Data []SearchHit        `json:"data"`
	Meta SemanticSearchMeta `json:"meta"`
}

// SemanticSearchResultInterface is the companion interface for SemanticSearchResult.
type SemanticSearchResultInterface interface {
	GetData() []SearchHit
	GetMeta() SemanticSearchMeta
}

// Ensure SemanticSearchResult implements SemanticSearchResultInterface.
var _ SemanticSearchResultInterface = (*SemanticSearchResult)(nil)

// GetData returns the search hits.
func (r *SemanticSearchResult) GetData() []SearchHit {
	return r.Data
}

// GetMeta returns the metadata.
func (r *SemanticSearchResult) GetMeta() SemanticSearchMeta {
	return r.Meta
}

// EmbeddingSearcherInterface defines the interface for vector similarity search.
type EmbeddingSearcherInterface interface {
	QueryVectorIndex(ctx context.Context, profileID string, embedding []float32, limit int) ([]SearchHit, error)
}

// SemanticSearchService defines the interface for semantic search operations.
type SemanticSearchService interface {
	Search(ctx context.Context, profileID string, req *SemanticSearchRequest) (*SemanticSearchResult, error)
}

// SemanticSearchServiceInterface is the companion interface for semantic search service.
type SemanticSearchServiceInterface interface {
	Search(ctx context.Context, profileID string, req *SemanticSearchRequest) (*SemanticSearchResult, error)
}

// semanticSearchService implements SemanticSearchService.
type semanticSearchService struct {
	embeddingSearcher   EmbeddingSearcherInterface
	embeddingDimensions int
}

// Ensure semanticSearchService implements SemanticSearchService.
var _ SemanticSearchService = (*semanticSearchService)(nil)

// NewSemanticSearchService creates a new semantic search service.
func NewSemanticSearchService(embeddingSearcher EmbeddingSearcherInterface, embeddingDimensions int) SemanticSearchService {
	return &semanticSearchService{
		embeddingSearcher:   embeddingSearcher,
		embeddingDimensions: embeddingDimensions,
	}
}

// ValidationError represents a validation error for semantic search.
type ValidationError struct {
	Message string
}

func (e *ValidationError) Error() string {
	return e.Message
}

// NewValidationError creates a new ValidationError.
func NewValidationError(message string) *ValidationError {
	return &ValidationError{Message: message}
}

// IsValidationError checks if an error is a ValidationError.
func IsValidationError(err error) bool {
	var validationErr *ValidationError
	return errors.As(err, &validationErr)
}

// EmbeddingGenerationNotConfiguredError represents the 501 error for missing embedding generation.
type EmbeddingGenerationNotConfiguredError struct {
	Message string
}

func (e *EmbeddingGenerationNotConfiguredError) Error() string {
	return e.Message
}

// NewEmbeddingGenerationNotConfiguredError creates a new EmbeddingGenerationNotConfiguredError.
func NewEmbeddingGenerationNotConfiguredError(message string) *EmbeddingGenerationNotConfiguredError {
	return &EmbeddingGenerationNotConfiguredError{Message: message}
}

// IsEmbeddingGenerationNotConfiguredError checks if an error is an EmbeddingGenerationNotConfiguredError.
func IsEmbeddingGenerationNotConfiguredError(err error) bool {
	var embErr *EmbeddingGenerationNotConfiguredError
	return errors.As(err, &embErr)
}

// DimensionMismatchError represents an error when embedding dimensions don't match config.
type DimensionMismatchError struct {
	Expected int
	Actual   int
}

func (e *DimensionMismatchError) Error() string {
	return "embedding dimension mismatch: expected " + strconv.Itoa(e.Expected) + ", got " + strconv.Itoa(e.Actual)
}

// NewDimensionMismatchError creates a new DimensionMismatchError.
func NewDimensionMismatchError(expected, actual int) *DimensionMismatchError {
	return &DimensionMismatchError{Expected: expected, Actual: actual}
}

// IsDimensionMismatchError checks if an error is a DimensionMismatchError.
func IsDimensionMismatchError(err error) bool {
	var dimErr *DimensionMismatchError
	return errors.As(err, &dimErr)
}

// Search performs a semantic search using vector similarity.
// It validates the embedding, applies limit rules, and returns results
// with defense-in-depth profile filtering.
func (s *semanticSearchService) Search(ctx context.Context, profileID string, req *SemanticSearchRequest) (*SemanticSearchResult, error) {
	// Phase 1: if embedding missing => 501 EMBEDDING_GENERATION_NOT_CONFIGURED
	if len(req.Embedding) == 0 {
		return nil, NewEmbeddingGenerationNotConfiguredError("embedding generation not configured")
	}

	// Validate embedding length equals EmbeddingDimensions from config
	if len(req.Embedding) != s.embeddingDimensions {
		return nil, NewDimensionMismatchError(s.embeddingDimensions, len(req.Embedding))
	}

	// Validate profile ID is not empty
	if profileID == "" {
		return nil, NewValidationError("profile ID is required")
	}

	// Validate and apply limit
	limitApplied, err := validateAndApplyLimit(req.Limit)
	if err != nil {
		return nil, err
	}
	if req.Threshold < 0 || req.Threshold > 1 {
		return nil, NewValidationError("threshold must be between 0 and 1")
	}

	// Query vector index on SourceFragment.embedding
	hits, err := s.embeddingSearcher.QueryVectorIndex(ctx, profileID, req.Embedding, limitApplied)
	if err != nil {
		return nil, err
	}

	// Defense-in-depth: post-filter by team_id in Go
	// profile A must never receive profile B rows even if B contains the nearest global vector candidate
	filteredHits := make([]SearchHit, 0, len(hits))
	for _, hit := range hits {
		if hit.ProfileID != profileID {
			continue
		}
		if req.Threshold > 0 && hit.Score < req.Threshold {
			continue
		}
		filteredHits = append(filteredHits, hit)
	}

	// Sort by score descending (deterministic for testing)
	sort.Slice(filteredHits, func(i, j int) bool {
		// Primary: score descending
		if filteredHits[i].Score != filteredHits[j].Score {
			return filteredHits[i].Score > filteredHits[j].Score
		}
		// Secondary: ID ascending for deterministic ordering
		return filteredHits[i].ID < filteredHits[j].ID
	})

	// Apply limit cap
	if len(filteredHits) > limitApplied {
		filteredHits = filteredHits[:limitApplied]
	}

	return &SemanticSearchResult{
		Data: filteredHits,
		Meta: SemanticSearchMeta{
			LimitApplied: limitApplied,
		},
	}, nil
}

// validateAndApplyLimit validates and applies limit rules.
// Returns the applied limit or an error if limit is invalid.
func validateAndApplyLimit(limit int) (int, error) {
	// limit=0 is rejected with 422
	if limit == 0 {
		return 0, NewValidationError("limit must be greater than 0")
	}

	// Default limit if not specified (negative means use default)
	if limit < 0 {
		return DefaultLimit, nil
	}

	// Cap to max limit
	if limit > MaxLimit {
		return MaxLimit, nil
	}

	return limit, nil
}
