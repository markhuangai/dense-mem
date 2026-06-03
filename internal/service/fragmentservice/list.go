package fragmentservice

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

const (
	// DefaultListLimit is applied when the caller does not supply one.
	DefaultListLimit = 20
	// MaxListLimit is the hard cap; anything larger is clamped (AC-30).
	MaxListLimit = 100
)

// ErrInvalidCursor is returned when a cursor fails to decode.
var ErrInvalidCursor = errors.New("invalid cursor")

// ListOptions controls pagination and filtering for fragment lists.
type ListOptions struct {
	Limit      int    // 0 = DefaultListLimit; clamped to [1, MaxListLimit]
	Cursor     string // opaque keyset cursor over (created_at, fragment_id)
	SourceType string // optional domain.SourceType filter
}

// ListFragmentsService lists fragments in a profile with keyset pagination (AC-29, AC-30).
type ListFragmentsService interface {
	List(ctx context.Context, profileID string, opts ListOptions) ([]domain.Fragment, string, error)
}

type listFragmentsService struct {
	reader ScopedReader
}

var _ ListFragmentsService = (*listFragmentsService)(nil)

// NewListFragmentsService constructs a ListFragmentsService.
func NewListFragmentsService(reader ScopedReader) ListFragmentsService {
	return &listFragmentsService{reader: reader}
}

// clampLimit applies default and cap (AC-30).
func clampLimit(requested int) int {
	if requested <= 0 {
		return DefaultListLimit
	}
	if requested > MaxListLimit {
		return MaxListLimit
	}
	return requested
}

// encodeCursor encodes a cursor for a given fragment row.
func encodeCursor(createdAt time.Time, fragmentID string) string {
	raw := fmt.Sprintf("%s|%s", createdAt.UTC().Format(time.RFC3339Nano), fragmentID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeCursor returns (createdAt, fragmentID, error).
func decodeCursor(cursor string) (time.Time, string, error) {
	if cursor == "" {
		return time.Time{}, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", ErrInvalidCursor
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: %v", ErrInvalidCursor, err)
	}
	return t, parts[1], nil
}

// List executes a keyset-paginated fragment list scoped to the profile.
// Returns the page of fragments plus an opaque nextCursor; nextCursor is empty when no more pages exist.
func (s *listFragmentsService) List(ctx context.Context, profileID string, opts ListOptions) ([]domain.Fragment, string, error) {
	limit := clampLimit(opts.Limit)

	afterTs, afterID, err := decodeCursor(opts.Cursor)
	if err != nil {
		return nil, "", err
	}

	var srcType any
	if opts.SourceType != "" {
		srcType = opts.SourceType
	}
	var afterTsParam any
	var afterIDParam any
	if !afterTs.IsZero() {
		afterTsParam = afterTs
		afterIDParam = afterID
	}

	query := `
		MATCH (sf:SourceFragment {team_id: $profileId})
		WHERE ` + neo4j.FragmentActiveFilter + `
		  AND ($srcType IS NULL OR sf.source_type = $srcType)
		  AND ($afterTs IS NULL OR sf.created_at < $afterTs
		       OR (sf.created_at = $afterTs AND sf.fragment_id < $afterId))
		RETURN sf.fragment_id AS fragment_id,
		       sf.team_id AS team_id,
		       sf.content AS content,
		       sf.source AS source,
		       sf.source_type AS source_type,
		       sf.authority AS authority,
		       sf.labels AS labels,
		       sf.metadata AS metadata,
		       sf.metadata_json AS metadata_json,
		       sf.content_hash AS content_hash,
		       sf.idempotency_key AS idempotency_key,
		       sf.embedding_model AS embedding_model,
		       sf.embedding_dimensions AS embedding_dimensions,
		       sf.source_quality AS source_quality,
		       sf.classification AS classification,
		       sf.classification_json AS classification_json,
		       sf.recorded_to AS recorded_to,
		       sf.retracted_at AS retracted_at,
		       sf.owner_profile_id AS owner_profile_id,
		       sf.owner_profile_name AS owner_profile_name,
		       sf.created_by_profile_id AS created_by_profile_id,
		       sf.created_by_profile_name AS created_by_profile_name,
		       sf.created_at AS created_at,
		       sf.updated_at AS updated_at
		ORDER BY sf.created_at DESC, sf.fragment_id DESC
		LIMIT $limit
	`

	// Overfetch by 1 to detect "more available".
	params := map[string]any{
		"srcType": srcType,
		"afterTs": afterTsParam,
		"afterId": afterIDParam,
		"limit":   int64(limit + 1),
	}

	_, rows, err := s.reader.ScopedRead(ctx, profileID, query, params)
	if err != nil {
		return nil, "", fmt.Errorf("failed to list fragments: %w", err)
	}

	out := make([]domain.Fragment, 0, len(rows))
	for _, r := range rows {
		f := mapRowToFragment(r)
		out = append(out, *f)
	}

	var nextCursor string
	if len(out) > limit {
		last := out[limit-1] // page boundary = last item returned to caller
		nextCursor = encodeCursor(last.CreatedAt, last.FragmentID)
		out = out[:limit]
	}

	return out, nextCursor, nil
}
