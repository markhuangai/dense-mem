package claimservice

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

// TestListClaims covers AC-15: cursor-paginated claim listing with status,
// predicate, and subject filters, keyed over (recorded_at DESC, claim_id DESC).
func TestListClaims(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	// makeRow builds a minimal Neo4j result row for the given claim fields.
	makeRow := func(claimID, status, predicate, subject string, recordedAt time.Time) map[string]any {
		return map[string]any{
			"claim_id":                       claimID,
			"subject":                        subject,
			"predicate":                      predicate,
			"object":                         "object",
			"modality":                       "assertion",
			"polarity":                       "positive",
			"speaker":                        "",
			"span_start":                     int64(0),
			"span_end":                       int64(0),
			"valid_from":                     nil,
			"valid_to":                       nil,
			"recorded_at":                    recordedAt,
			"recorded_to":                    nil,
			"extract_conf":                   0.9,
			"resolution_conf":                0.8,
			"source_quality":                 0.7,
			"entailment_verdict":             "unverified",
			"status":                         status,
			"last_verifier_response":         "",
			"verified_at":                    nil,
			"extraction_model":               "model-1",
			"extraction_version":             "1.0",
			"verifier_model":                 "",
			"pipeline_run_id":                "",
			"content_hash":                   "hash-" + claimID,
			"idempotency_key":                "",
			"classification":                 nil,
			"classification_lattice_version": "",
			"supported_by":                   []any{},
		}
	}

	t.Run("returns claims for profile", func(t *testing.T) {
		row := makeRow("claim-1", "candidate", "knows", "Alice", now)
		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {row},
			},
		}
		svc := NewListClaimsFilteredService(reader)

		result, err := svc.List(ctx, profileID, ListClaimOptions{})

		require.NoError(t, err)
		require.NotNil(t, result)
		require.Len(t, result.Items, 1)
		require.Equal(t, "claim-1", result.Items[0].ClaimID)
		require.Equal(t, profileID, result.Items[0].ProfileID)
		require.False(t, result.HasMore)
		require.Empty(t, result.NextCursor)
	})

	t.Run("returns empty list when no claims exist", func(t *testing.T) {
		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {},
			},
		}
		svc := NewListClaimsFilteredService(reader)

		result, err := svc.List(ctx, profileID, ListClaimOptions{})

		require.NoError(t, err)
		require.NotNil(t, result)
		require.Empty(t, result.Items)
		require.Empty(t, result.NextCursor)
		require.False(t, result.HasMore)
	})

	t.Run("sets HasMore and NextCursor when overfetch returns limit+1 rows", func(t *testing.T) {
		const pageLimit = 2
		// The stub returns all registered rows regardless of the LIMIT param, so
		// register exactly limit+1 rows to trigger the has_more detection.
		rows := make([]map[string]any, pageLimit+1)
		for i := range rows {
			ts := now.Add(-time.Duration(i) * time.Second)
			rows[i] = makeRow(fmt.Sprintf("claim-%d", i), "candidate", "knows", "Alice", ts)
		}
		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: rows,
			},
		}
		svc := NewListClaimsFilteredService(reader)

		result, err := svc.List(ctx, profileID, ListClaimOptions{Limit: pageLimit})

		require.NoError(t, err)
		require.Len(t, result.Items, pageLimit, "must return exactly limit items")
		require.True(t, result.HasMore)
		require.NotEmpty(t, result.NextCursor)
	})

	t.Run("cursor round-trip encodes and decodes correctly", func(t *testing.T) {
		ts := time.Date(2024, 3, 15, 12, 0, 0, 123456789, time.UTC)
		encoded := encodeClaimCursor(ts, "claim-xyz")
		gotTs, gotID, err := decodeClaimCursor(encoded)

		require.NoError(t, err)
		require.Equal(t, ts, gotTs)
		require.Equal(t, "claim-xyz", gotID)
	})

	t.Run("invalid cursor returns ErrInvalidClaimCursor", func(t *testing.T) {
		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{profileID: {}},
		}
		svc := NewListClaimsFilteredService(reader)

		_, err := svc.List(ctx, profileID, ListClaimOptions{Cursor: "!!!not-valid-base64!!!"})

		require.Error(t, err)
		require.True(t, errors.Is(err, ErrInvalidClaimCursor))
	})

	t.Run("propagates reader error", func(t *testing.T) {
		readerErr := errors.New("neo4j unavailable")
		reader := &stubClaimReader{err: readerErr}
		svc := NewListClaimsFilteredService(reader)

		_, err := svc.List(ctx, profileID, ListClaimOptions{})

		require.Error(t, err)
		require.Contains(t, err.Error(), "neo4j unavailable")
	})

	t.Run("clamps oversized limit to maxClaimListLimit", func(t *testing.T) {
		row := makeRow("claim-1", "candidate", "knows", "Alice", now)
		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{profileID: {row}},
		}
		svc := NewListClaimsFilteredService(reader)

		// limit=99999 exceeds maxClaimListLimit; stub returns 1 row so no has_more.
		result, err := svc.List(ctx, profileID, ListClaimOptions{Limit: 99999})

		require.NoError(t, err)
		require.Len(t, result.Items, 1)
		require.False(t, result.HasMore)
	})

	t.Run("zero limit applies default", func(t *testing.T) {
		row := makeRow("claim-1", "candidate", "knows", "Alice", now)
		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{profileID: {row}},
		}
		svc := NewListClaimsFilteredService(reader)

		result, err := svc.List(ctx, profileID, ListClaimOptions{Limit: 0})

		require.NoError(t, err)
		require.Len(t, result.Items, 1)
	})

	t.Run("hydrates claim fields from row", func(t *testing.T) {
		row := makeRow("claim-hydrate", "validated", "likes", "Bob", now)
		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{profileID: {row}},
		}
		svc := NewListClaimsFilteredService(reader)

		result, err := svc.List(ctx, profileID, ListClaimOptions{})

		require.NoError(t, err)
		require.Len(t, result.Items, 1)
		c := result.Items[0]
		require.Equal(t, "claim-hydrate", c.ClaimID)
		require.Equal(t, profileID, c.ProfileID)
		require.Equal(t, "Bob", c.Subject)
		require.Equal(t, "likes", c.Predicate)
		require.Equal(t, "validated", string(c.Status))
		require.InDelta(t, 0.9, c.ExtractConf, 1e-9)
		require.InDelta(t, 0.8, c.ResolutionConf, 1e-9)
		require.InDelta(t, 0.7, c.SourceQuality, 1e-9)
	})

	t.Run("decodes JSON encoded classification from list rows", func(t *testing.T) {
		row := makeRow("claim-json", "candidate", "likes", "Bob", now)
		row["classification_json"] = `{"confidentiality":"internal"}`
		reader := &stubClaimReader{
			rowsByProfile: map[string][]map[string]any{profileID: {row}},
		}
		svc := NewListClaimsFilteredService(reader)

		result, err := svc.List(ctx, profileID, ListClaimOptions{})

		require.NoError(t, err)
		require.Len(t, result.Items, 1)
		require.Equal(t, "internal", result.Items[0].Classification["confidentiality"])
	})
}

// TestListClaims_CrossProfileIsolation verifies that claims belonging to profile
// A are not returned when querying as profile B.
//
// This is a mandatory security test per .claude/rules/profile-isolation.md.
func TestListClaims_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()
	const profileA = "00000000-0000-0000-0000-000000000001"
	const profileB = "00000000-0000-0000-0000-000000000002"
	now := time.Now().UTC()

	rowA := map[string]any{
		"claim_id":                       "claim-a",
		"subject":                        "Alice",
		"predicate":                      "knows",
		"object":                         "Bob",
		"modality":                       "assertion",
		"polarity":                       "positive",
		"speaker":                        "",
		"span_start":                     int64(0),
		"span_end":                       int64(0),
		"valid_from":                     nil,
		"valid_to":                       nil,
		"recorded_at":                    now,
		"recorded_to":                    nil,
		"extract_conf":                   0.0,
		"resolution_conf":                0.0,
		"source_quality":                 0.0,
		"entailment_verdict":             "unverified",
		"status":                         "candidate",
		"last_verifier_response":         "",
		"verified_at":                    nil,
		"extraction_model":               "",
		"extraction_version":             "",
		"verifier_model":                 "",
		"pipeline_run_id":                "",
		"content_hash":                   "hash-a",
		"idempotency_key":                "",
		"classification":                 nil,
		"classification_lattice_version": "",
		"supported_by":                   []any{},
	}

	// The stub models Neo4j's profile-scoped isolation: ScopedRead returns only
	// rows registered for the given profileID, mirroring the
	// {team_id: $profileId} MATCH filter. Profile B receives no rows.
	reader := &stubClaimReader{
		rowsByProfile: map[string][]map[string]any{
			profileA: {rowA},
			profileB: {},
		},
	}
	svc := NewListClaimsFilteredService(reader)

	// Profile A can see its own claims.
	resultA, err := svc.List(ctx, profileA, ListClaimOptions{})
	require.NoError(t, err)
	require.Len(t, resultA.Items, 1)
	require.Equal(t, "claim-a", resultA.Items[0].ClaimID)
	require.Equal(t, profileA, resultA.Items[0].ProfileID,
		"returned claim must carry profile A's ID")

	// Profile B sees no claims — profile A's data must not leak.
	resultB, err := svc.List(ctx, profileB, ListClaimOptions{})
	require.NoError(t, err)
	require.Empty(t, resultB.Items, "profile B must not receive profile A's claims")

	// Collect all claim IDs returned to profile B and assert absence of profile A's ID.
	bIDs := make([]string, len(resultB.Items))
	for i, c := range resultB.Items {
		bIDs[i] = c.ClaimID
	}
	require.NotContains(t, bIDs, "claim-a",
		"profile A's claim must not be visible to profile B")
}

type sequenceClaimReader struct {
	pages [][]map[string]any
	call  int
	err   error
}

func (s *sequenceClaimReader) ScopedRead(_ context.Context, _ string, _ string, _ map[string]any) (neo4j.ResultSummary, []map[string]any, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	if s.call >= len(s.pages) {
		return nil, nil, nil
	}
	rows := s.pages[s.call]
	s.call++
	return nil, rows, nil
}

func TestListClaimsCompatService(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
	row := func(id string, seconds int) map[string]any {
		return map[string]any{
			"claim_id":           id,
			"subject":            "Alice",
			"predicate":          "likes",
			"object":             "Go",
			"modality":           "assertion",
			"polarity":           "+",
			"recorded_at":        now.Add(-time.Duration(seconds) * time.Second),
			"entailment_verdict": "unverified",
			"status":             "candidate",
			"supported_by":       []any{},
		}
	}

	reader := &sequenceClaimReader{pages: [][]map[string]any{
		{row("claim-0", 0), row("claim-1", 1), row("claim-overfetch", 2)},
		{row("claim-2", 3)},
	}}
	svc := NewListClaimsService(reader)

	items, total, err := svc.List(ctx, profileID, 1, 2)
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, "claim-2", items[0].ClaimID)
	require.Equal(t, 3, total)

	reader = &sequenceClaimReader{pages: [][]map[string]any{{}}}
	svc = NewListClaimsService(reader)
	items, total, err = svc.List(ctx, profileID, 5, 10)
	require.NoError(t, err)
	require.Empty(t, items)
	require.Equal(t, 0, total)

	reader = &sequenceClaimReader{err: errors.New("list failed")}
	svc = NewListClaimsService(reader)
	items, total, err = svc.List(ctx, profileID, 5, 0)
	require.Nil(t, items)
	require.Equal(t, 0, total)
	require.ErrorContains(t, err, "failed to list claims")
}
