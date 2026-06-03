package factservice

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	defaultFactListLimit = 20
	maxFactListLimit     = 100
)

// FactListFilters holds optional filter criteria for List.
// A zero-value FactListFilters applies no extra filters beyond profile isolation.
type FactListFilters struct {
	Subject   string
	Predicate string
	Status    domain.FactStatus
	ValidAt   *time.Time
	KnownAt   *time.Time
}

// listFactServiceImpl implements ListFactsService.
type listFactServiceImpl struct {
	reader factReader
}

// Compile-time check that listFactServiceImpl satisfies ListFactsService.
var _ ListFactsService = (*listFactServiceImpl)(nil)

// NewListFactsService constructs a ready-to-use ListFactsService.
func NewListFactsService(reader factReader) ListFactsService {
	return &listFactServiceImpl{reader: reader}
}

// clampFactLimit applies the default and hard cap to a caller-supplied limit.
func clampFactLimit(requested int) int {
	if requested <= 0 {
		return defaultFactListLimit
	}
	if requested > maxFactListLimit {
		return maxFactListLimit
	}
	return requested
}

// factCursor holds the keyset values used for cursor-based pagination.
type factCursor struct {
	RecordedAt time.Time
	FactID     string
}

// encodeCursor produces an opaque base64 token from a keyset cursor.
// The internal format is "<unix-nano>:<fact_id>".
func encodeCursor(c factCursor) string {
	raw := fmt.Sprintf("%d:%s", c.RecordedAt.UnixNano(), c.FactID)
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// decodeCursor parses a cursor token back to its keyset values.
// Returns (cursor, true, nil) when the token is non-empty and valid.
// Returns (zero, false, nil) when the token is empty (first-page request).
// Returns (zero, false, err) when the token is non-empty but malformed.
func decodeCursor(token string) (factCursor, bool, error) {
	if token == "" {
		return factCursor{}, false, nil
	}
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return factCursor{}, false, fmt.Errorf("invalid cursor encoding: %w", err)
	}
	s := string(raw)
	idx := strings.IndexByte(s, ':')
	if idx < 0 {
		return factCursor{}, false, errors.New("malformed cursor: missing separator")
	}
	ns, err := strconv.ParseInt(s[:idx], 10, 64)
	if err != nil {
		return factCursor{}, false, fmt.Errorf("malformed cursor timestamp: %w", err)
	}
	factID := s[idx+1:]
	if factID == "" {
		return factCursor{}, false, errors.New("malformed cursor: empty fact_id")
	}
	return factCursor{
		RecordedAt: time.Unix(0, ns).UTC(),
		FactID:     factID,
	}, true, nil
}

// listFactsCypher retrieves a page of Facts for a profile ordered by
// (recorded_at DESC, fact_id DESC), supporting optional subject/predicate/
// status filters and keyset cursor pagination.
//
// Profile isolation: $profileId is injected by ScopedRead.
// Filters: empty-string parameters are treated as no-ops by the OR conditions.
// Cursor: when $hasCursor is false the cursor WHERE branch is skipped entirely.
const listFactsCypher = `
MATCH (f:Fact {team_id: $profileId})
WHERE ($subject = '' OR f.subject = $subject)
  AND ($predicate = '' OR f.predicate = $predicate)
  AND ($status = '' OR f.status = $status)
  AND ($validAt IS NULL OR ((f.valid_from IS NULL OR f.valid_from <= $validAt)
       AND (f.valid_to IS NULL OR f.valid_to > $validAt)))
  AND ($knownAt IS NULL OR (f.recorded_at <= $knownAt
       AND (f.recorded_to IS NULL OR f.recorded_to > $knownAt)))
  AND (
    NOT $hasCursor
    OR f.recorded_at < $cursorTime
    OR (f.recorded_at = $cursorTime AND f.fact_id < $cursorFactID)
  )
RETURN
    f.fact_id                        AS fact_id,
    f.owner_profile_id               AS owner_profile_id,
    f.owner_profile_name             AS owner_profile_name,
    f.created_by_profile_id          AS created_by_profile_id,
    f.created_by_profile_name        AS created_by_profile_name,
    f.promoted_by_profile_id         AS promoted_by_profile_id,
    f.promoted_by_profile_name       AS promoted_by_profile_name,
    f.subject                        AS subject,
    f.predicate                      AS predicate,
    f.object                         AS object,
    f.status                         AS status,
    f.authority_state                AS authority_state,
    f.truth_score                    AS truth_score,
    f.valid_from                     AS valid_from,
    f.valid_to                       AS valid_to,
    f.recorded_at                    AS recorded_at,
    f.recorded_to                    AS recorded_to,
    f.retracted_at                   AS retracted_at,
    f.last_confirmed_at              AS last_confirmed_at,
    f.promoted_from_claim_id         AS promoted_from_claim_id,
    f.classification                 AS classification,
    f.classification_json            AS classification_json,
    f.classification_lattice_version AS classification_lattice_version,
    f.source_quality                 AS source_quality,
    f.labels                         AS labels,
    f.metadata                       AS metadata
ORDER BY f.recorded_at DESC, f.fact_id DESC
LIMIT $limit`

// List returns up to limit Facts for profileID matching filters, starting after
// the cursor position (empty cursor = first page), ordered by
// (recorded_at DESC, fact_id DESC).
//
// Returns (facts, nextCursor, nil) on success. nextCursor is non-empty only
// when len(facts) == limit, indicating more pages may exist. An empty
// nextCursor means no further results exist.
//
// Profile isolation: the query is routed through ScopedRead, which injects
// $profileId and prevents cross-profile reads.
func (s *listFactServiceImpl) List(
	ctx context.Context,
	profileID string,
	filters FactListFilters,
	limit int,
	cursor string,
) ([]*domain.Fact, string, error) {
	limit = clampFactLimit(limit)

	cur, hasCursor, err := decodeCursor(cursor)
	if err != nil {
		return nil, "", fmt.Errorf("list facts: %w", err)
	}

	params := map[string]any{
		"subject":      filters.Subject,
		"predicate":    filters.Predicate,
		"status":       string(filters.Status),
		"validAt":      filters.ValidAt,
		"knownAt":      filters.KnownAt,
		"hasCursor":    hasCursor,
		"cursorTime":   cur.RecordedAt,
		"cursorFactID": cur.FactID,
		"limit":        int64(limit),
	}

	_, rows, err := s.reader.ScopedRead(ctx, profileID, listFactsCypher, params)
	if err != nil {
		return nil, "", fmt.Errorf("failed to list facts: %w", err)
	}

	facts := make([]*domain.Fact, 0, len(rows))
	for _, r := range rows {
		facts = append(facts, rowToFact(profileID, r))
	}

	// Emit a next-page cursor only when we received a full page. A short page
	// means the result set is exhausted.
	nextCursor := ""
	if len(facts) == limit {
		last := facts[len(facts)-1]
		nextCursor = encodeCursor(factCursor{
			RecordedAt: last.RecordedAt,
			FactID:     last.FactID,
		})
	}

	return facts, nextCursor, nil
}
