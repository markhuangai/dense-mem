package claimservice

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const (
	defaultClaimListLimit = 20
	maxClaimListLimit     = 100
)

// ErrInvalidClaimCursor is returned when a claim list cursor fails to decode.
var ErrInvalidClaimCursor = errors.New("invalid cursor")

// ListClaimOptions controls pagination and filtering for claim lists.
//
// Profile isolation: the profileID is always passed as a separate parameter to
// List; it is never embedded in ListClaimOptions so callers cannot accidentally
// bypass the isolation boundary.
type ListClaimOptions struct {
	// Limit is the maximum number of items to return.
	// 0 applies defaultClaimListLimit; values above maxClaimListLimit are clamped.
	Limit int
	// Cursor is the opaque keyset cursor over (recorded_at, claim_id).
	// An empty string starts from the beginning of the result set.
	Cursor string
	// Status filters results to claims with this status value.
	// Empty string means "no filter" — all statuses are returned.
	Status string
	// Predicate filters results to claims with this predicate value.
	// Empty string means "no filter".
	Predicate string
	// Subject filters results to claims with this subject value.
	// Empty string means "no filter".
	Subject string
	// Modality filters results to claims with this modality value.
	// Empty string means "no filter".
	Modality string
}

// ListClaimsResult is the paginated response from a claim list query.
type ListClaimsResult struct {
	Items      []*domain.Claim
	NextCursor string
	HasMore    bool
}

// ListClaimsFilteredService defines the interface for cursor-paginated, filtered claim listing (AC-15).
//
// Profile isolation invariant: every List call scopes all database access to
// profileID. Implementations MUST NOT return claims belonging to a different
// profile.
type ListClaimsFilteredService interface {
	List(ctx context.Context, profileID string, opts ListClaimOptions) (*ListClaimsResult, error)
}

type listClaimsFilteredServiceImpl struct {
	reader claimReader
}

// Compile-time check that listClaimsFilteredServiceImpl satisfies ListClaimsFilteredService.
var _ ListClaimsFilteredService = (*listClaimsFilteredServiceImpl)(nil)

// NewListClaimsFilteredService constructs a ListClaimsFilteredService.
func NewListClaimsFilteredService(reader claimReader) ListClaimsFilteredService {
	return &listClaimsFilteredServiceImpl{reader: reader}
}

// clampClaimLimit applies the default and hard cap to a caller-supplied limit.
func clampClaimLimit(requested int) int {
	if requested <= 0 {
		return defaultClaimListLimit
	}
	if requested > maxClaimListLimit {
		return maxClaimListLimit
	}
	return requested
}

// encodeClaimCursor encodes a keyset cursor for a claim row.
func encodeClaimCursor(recordedAt time.Time, claimID string) string {
	raw := fmt.Sprintf("%s|%s", recordedAt.UTC().Format(time.RFC3339Nano), claimID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeClaimCursor decodes a keyset cursor into (recordedAt, claimID, error).
// An empty cursor returns zero values and a nil error.
func decodeClaimCursor(cursor string) (time.Time, string, error) {
	if cursor == "" {
		return time.Time{}, "", nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: %v", ErrInvalidClaimCursor, err)
	}
	parts := strings.SplitN(string(raw), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, "", ErrInvalidClaimCursor
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return time.Time{}, "", fmt.Errorf("%w: %v", ErrInvalidClaimCursor, err)
	}
	return t, parts[1], nil
}

// listClaimsCypher is the Cypher template for claim list queries.
//
// Profile isolation: $profileId is injected automatically by ScopedRead and
// appears in the Claim node pattern, making cross-profile leakage impossible
// at the query level.
//
// The query overfetches by 1 (LIMIT $limit where limit = requested+1) so the
// service can detect whether more results exist without a separate COUNT query.
//
// Nullable filter parameters ($status, $predicate, $subject) use the IS NULL
// guard pattern: when the caller passes nil the filter is skipped entirely,
// matching all values for that dimension. This avoids generating multiple query
// variants while keeping parameterisation safe.
//
// Keyset pagination over (recorded_at DESC, claim_id DESC) is stable and index-
// friendly; the cursor covers the case where recorded_at ties (same timestamp,
// different claim IDs).
//
// supported_by is returned as an empty list — list operations do not fan out
// to SUPPORTED_BY edges to avoid N+1 reads. Callers that require edge data
// should call GetClaimService.Get for individual claims.
const listClaimsCypher = `
MATCH (c:Claim {team_id: $profileId})
	WHERE ($status IS NULL OR c.status = $status)
	  AND ($predicate IS NULL OR c.predicate = $predicate)
	  AND ($subject IS NULL OR c.subject = $subject)
	  AND ($modality IS NULL OR c.modality = $modality)
	  AND ($afterTs IS NULL OR c.recorded_at < $afterTs
	       OR (c.recorded_at = $afterTs AND c.claim_id < $afterId))
RETURN c.claim_id                        AS claim_id,
       c.created_by_profile_id           AS created_by_profile_id,
       c.created_by_profile_name         AS created_by_profile_name,
       c.subject                         AS subject,
       c.predicate                       AS predicate,
       c.object                          AS object,
       c.modality                        AS modality,
       c.polarity                        AS polarity,
       c.speaker                         AS speaker,
       c.span_start                      AS span_start,
       c.span_end                        AS span_end,
       c.valid_from                      AS valid_from,
       c.valid_to                        AS valid_to,
       c.recorded_at                     AS recorded_at,
       c.recorded_to                     AS recorded_to,
       c.extract_conf                    AS extract_conf,
       c.resolution_conf                 AS resolution_conf,
       c.source_quality                  AS source_quality,
       c.entailment_verdict              AS entailment_verdict,
       c.status                          AS status,
       c.last_verifier_response          AS last_verifier_response,
       c.verified_at                     AS verified_at,
       c.extraction_model                AS extraction_model,
       c.extraction_version              AS extraction_version,
       c.verifier_model                  AS verifier_model,
       c.pipeline_run_id                 AS pipeline_run_id,
       c.content_hash                    AS content_hash,
       c.idempotency_key                 AS idempotency_key,
       c.classification                  AS classification,
       c.classification_json             AS classification_json,
       c.classification_lattice_version  AS classification_lattice_version,
       c.owner_profile_id                AS owner_profile_id,
       c.owner_profile_name              AS owner_profile_name,
       [] AS supported_by
ORDER BY c.recorded_at DESC, c.claim_id DESC
LIMIT $limit
`

// List executes a keyset-paginated, filtered claim list scoped to profileID.
//
// Returns a ListClaimsResult containing the page of items, an opaque
// NextCursor (empty when there are no further pages), and HasMore indicating
// whether a subsequent page exists.
func (s *listClaimsFilteredServiceImpl) List(ctx context.Context, profileID string, opts ListClaimOptions) (*ListClaimsResult, error) {
	limit := clampClaimLimit(opts.Limit)

	afterTs, afterID, err := decodeClaimCursor(opts.Cursor)
	if err != nil {
		return nil, err
	}

	// Nil params instruct Neo4j to skip the corresponding WHERE clause guard.
	var statusParam, predicateParam, subjectParam any
	var modalityParam any
	if opts.Status != "" {
		statusParam = opts.Status
	}
	if opts.Predicate != "" {
		predicateParam = opts.Predicate
	}
	if opts.Subject != "" {
		subjectParam = opts.Subject
	}
	if opts.Modality != "" {
		modalityParam = opts.Modality
	}

	var afterTsParam, afterIDParam any
	if !afterTs.IsZero() {
		afterTsParam = afterTs
		afterIDParam = afterID
	}

	params := map[string]any{
		"status":    statusParam,
		"predicate": predicateParam,
		"subject":   subjectParam,
		"modality":  modalityParam,
		"afterTs":   afterTsParam,
		"afterId":   afterIDParam,
		// Overfetch by 1 to detect "more available" without a COUNT query.
		"limit": int64(limit + 1),
	}

	_, rows, err := s.reader.ScopedRead(ctx, profileID, listClaimsCypher, params)
	if err != nil {
		return nil, fmt.Errorf("failed to list claims: %w", err)
	}

	out := make([]*domain.Claim, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowToClaim(profileID, r))
	}

	result := &ListClaimsResult{Items: out}
	if len(out) > limit {
		// The page boundary is the last item within the requested limit.
		last := out[limit-1]
		result.NextCursor = encodeClaimCursor(last.RecordedAt, last.ClaimID)
		result.HasMore = true
		result.Items = out[:limit]
	}

	return result, nil
}
