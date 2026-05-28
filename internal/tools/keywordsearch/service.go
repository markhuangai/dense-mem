package keywordsearch

import (
	"context"
	"errors"
	"sort"
	"time"
)

const (
	// DefaultLimit is the default limit for search results.
	DefaultLimit = 20
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
}

// KeywordSearchRequest represents the request for keyword search.
type KeywordSearchRequest struct {
	Query   string     `json:"query"`
	Limit   int        `json:"limit"`
	Labels  []string   `json:"labels,omitempty"`
	ValidAt *time.Time `json:"valid_at,omitempty"`
	KnownAt *time.Time `json:"known_at,omitempty"`
}

// KeywordSearchRequestInterface is the companion interface for KeywordSearchRequest.
type KeywordSearchRequestInterface interface {
	GetQuery() string
	GetLimit() int
	GetLabels() []string
}

// Ensure KeywordSearchRequest implements KeywordSearchRequestInterface.
var _ KeywordSearchRequestInterface = (*KeywordSearchRequest)(nil)

// GetQuery returns the query string.
func (r *KeywordSearchRequest) GetQuery() string {
	return r.Query
}

// GetLimit returns the limit.
func (r *KeywordSearchRequest) GetLimit() int {
	return r.Limit
}

// GetLabels returns the labels filter.
func (r *KeywordSearchRequest) GetLabels() []string {
	return r.Labels
}

// KeywordSearchMeta represents metadata about the search.
type KeywordSearchMeta struct {
	LimitApplied int `json:"limit_applied"`
}

// KeywordSearchMetaInterface is the companion interface for KeywordSearchMeta.
type KeywordSearchMetaInterface interface {
	GetLimitApplied() int
}

// Ensure KeywordSearchMeta implements KeywordSearchMetaInterface.
var _ KeywordSearchMetaInterface = (*KeywordSearchMeta)(nil)

// GetLimitApplied returns the applied limit.
func (m *KeywordSearchMeta) GetLimitApplied() int {
	return m.LimitApplied
}

// KeywordSearchResult represents the result of a keyword search.
type KeywordSearchResult struct {
	Data []SearchHit       `json:"data"`
	Meta KeywordSearchMeta `json:"meta"`
}

// KeywordSearchResultInterface is the companion interface for KeywordSearchResult.
type KeywordSearchResultInterface interface {
	GetData() []SearchHit
	GetMeta() KeywordSearchMeta
}

// Ensure KeywordSearchResult implements KeywordSearchResultInterface.
var _ KeywordSearchResultInterface = (*KeywordSearchResult)(nil)

// GetData returns the search hits.
func (r *KeywordSearchResult) GetData() []SearchHit {
	return r.Data
}

// GetMeta returns the metadata.
func (r *KeywordSearchResult) GetMeta() KeywordSearchMeta {
	return r.Meta
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

// FragmentSearchResult represents a result from SourceFragment full-text search.
type FragmentSearchResult struct {
	FragmentID string         `json:"fragment_id"`
	Content    string         `json:"content"`
	Score      float64        `json:"score"`
	Labels     []string       `json:"labels"`
	Metadata   map[string]any `json:"metadata"`
	ProfileID  string         `json:"team_id"`
}

// FactSearchResult represents a result from Fact predicate full-text search.
type FactSearchResult struct {
	FactID     string         `json:"fact_id"`
	Predicate  string         `json:"predicate"`
	Score      float64        `json:"score"`
	Labels     []string       `json:"labels"`
	Metadata   map[string]any `json:"metadata"`
	ProfileID  string         `json:"team_id"`
	ValidFrom  *time.Time     `json:"valid_from,omitempty"`
	ValidTo    *time.Time     `json:"valid_to,omitempty"`
	RecordedAt time.Time      `json:"recorded_at"`
	RecordedTo *time.Time     `json:"recorded_to,omitempty"`
}

// FragmentSearcherInterface defines the interface for fragment full-text search.
type FragmentSearcherInterface interface {
	SearchContent(ctx context.Context, profileID string, query string, labels []string, limit int) ([]FragmentSearchResult, error)
}

// FactSearcherInterface defines the interface for fact full-text search.
type FactSearcherInterface interface {
	SearchPredicate(ctx context.Context, profileID string, query string, labels []string, limit int) ([]FactSearchResult, error)
}

// KeywordSearchService defines the interface for keyword search operations.
type KeywordSearchService interface {
	Search(ctx context.Context, profileID string, req *KeywordSearchRequest) (*KeywordSearchResult, error)
}

// KeywordSearchServiceInterface is the companion interface for keyword search service.
type KeywordSearchServiceInterface interface {
	Search(ctx context.Context, profileID string, req *KeywordSearchRequest) (*KeywordSearchResult, error)
}

// keywordSearchService implements KeywordSearchService.
type keywordSearchService struct {
	fragmentSearcher FragmentSearcherInterface
	factSearcher     FactSearcherInterface
}

// Ensure keywordSearchService implements KeywordSearchService.
var _ KeywordSearchService = (*keywordSearchService)(nil)

// NewKeywordSearchService creates a new keyword search service.
func NewKeywordSearchService(fragmentSearcher FragmentSearcherInterface, factSearcher FactSearcherInterface) KeywordSearchService {
	return &keywordSearchService{
		fragmentSearcher: fragmentSearcher,
		factSearcher:     factSearcher,
	}
}

// ValidationError represents a validation error for keyword search.
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

// Search performs a keyword search across SourceFragment content and Fact predicates.
// It merges and ranks results from both indexes, applies defense-in-depth profile filtering,
// and returns the top results capped to the limit.
func (s *keywordSearchService) Search(ctx context.Context, profileID string, req *KeywordSearchRequest) (*KeywordSearchResult, error) {
	// Validate and apply limit
	limitApplied, err := validateAndApplyLimit(req.Limit)
	if err != nil {
		return nil, err
	}

	// Validate query is not empty
	if req.Query == "" {
		return nil, NewValidationError("query is required")
	}

	// Validate profile ID is not empty
	if profileID == "" {
		return nil, NewValidationError("profile ID is required")
	}

	// Search SourceFragment content (filtered by team_id in Cypher)
	fragmentResults, err := s.fragmentSearcher.SearchContent(ctx, profileID, req.Query, req.Labels, limitApplied)
	if err != nil {
		return nil, err
	}

	// Search Fact predicates (filtered by team_id in Cypher)
	factResults, err := s.factSearcher.SearchPredicate(ctx, profileID, req.Query, req.Labels, limitApplied)
	if err != nil {
		return nil, err
	}

	// Convert results to SearchHit and apply defense-in-depth post-filter
	hits := make([]SearchHit, 0, len(fragmentResults)+len(factResults))

	// Convert fragment results with post-filter
	for _, fr := range fragmentResults {
		// Defense-in-depth: post-filter by team_id in Go
		if fr.ProfileID != profileID {
			continue // Skip results from other profiles
		}
		// Optional labels filter - defense-in-depth
		if !matchesLabels(fr.Labels, req.Labels) {
			continue
		}
		hits = append(hits, SearchHit{
			ID:        fr.FragmentID,
			Type:      "fragment",
			Content:   fr.Content,
			Score:     fr.Score,
			Labels:    fr.Labels,
			Metadata:  fr.Metadata,
			ProfileID: fr.ProfileID,
		})
	}

	// Convert fact results with post-filter
	for _, fr := range factResults {
		// Defense-in-depth: post-filter by team_id in Go
		if fr.ProfileID != profileID {
			continue // Skip results from other profiles
		}
		if !factResultMatchesWindow(fr, req.ValidAt, req.KnownAt) {
			continue
		}
		// Optional labels filter - defense-in-depth
		if !matchesLabels(fr.Labels, req.Labels) {
			continue
		}
		hits = append(hits, SearchHit{
			ID:        fr.FactID,
			Type:      "fact",
			Content:   fr.Predicate,
			Score:     fr.Score,
			Labels:    fr.Labels,
			Metadata:  fr.Metadata,
			ProfileID: fr.ProfileID,
		})
	}

	// Sort by score descending (deterministic for testing)
	sort.Slice(hits, func(i, j int) bool {
		// Primary: score descending
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		// Secondary: ID ascending for deterministic ordering
		return hits[i].ID < hits[j].ID
	})

	// Apply limit cap
	if len(hits) > limitApplied {
		hits = hits[:limitApplied]
	}

	return &KeywordSearchResult{
		Data: hits,
		Meta: KeywordSearchMeta{
			LimitApplied: limitApplied,
		},
	}, nil
}

func factResultMatchesWindow(f FactSearchResult, validAt, knownAt *time.Time) bool {
	if validAt != nil {
		if f.ValidFrom != nil && f.ValidFrom.After(*validAt) {
			return false
		}
		if f.ValidTo != nil && !f.ValidTo.After(*validAt) {
			return false
		}
	}
	if knownAt != nil {
		if f.RecordedAt.After(*knownAt) {
			return false
		}
		if f.RecordedTo != nil && !f.RecordedTo.After(*knownAt) {
			return false
		}
	}
	return true
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

// matchesLabels checks if the result labels match the requested labels filter.
// If no labels filter is specified, all results match.
// If labels filter is specified, result must have at least one matching label.
func matchesLabels(resultLabels []string, filterLabels []string) bool {
	// No filter means all match
	if len(filterLabels) == 0 {
		return true
	}

	// Result must have at least one of the requested labels
	for _, fl := range filterLabels {
		for _, rl := range resultLabels {
			if rl == fl {
				return true
			}
		}
	}

	return false
}
