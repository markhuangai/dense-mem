package factservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

// filteringStubFactReader simulates the profile-scoped, filtered Neo4j query
// used by listFactServiceImpl. It applies subject/predicate/status filters and
// cursor-based pagination to pre-loaded rows, mirroring the WHERE clauses in
// listFactsCypher.
//
// Rows must be pre-sorted by (recorded_at DESC, fact_id DESC) — the stub
// preserves insertion order and applies limit, exactly as a correctly-ordered
// Cypher query would.
//
// Profile isolation is not modelled here; use stubFactReader (from read_test.go)
// when cross-profile behaviour needs to be verified.
type filteringStubFactReader struct {
	rows       []map[string]any
	lastParams map[string]any
}

func (r *filteringStubFactReader) ScopedRead(
	_ context.Context,
	_ string,
	_ string,
	params map[string]any,
) (neo4j.ResultSummary, []map[string]any, error) {
	r.lastParams = params

	subject, _ := params["subject"].(string)
	predicate, _ := params["predicate"].(string)
	status, _ := params["status"].(string)
	hasCursor, _ := params["hasCursor"].(bool)
	var cursorTime time.Time
	var cursorFactID string
	if hasCursor {
		cursorTime, _ = params["cursorTime"].(time.Time)
		cursorFactID, _ = params["cursorFactID"].(string)
	}
	limit := int64(1000)
	if l, ok := params["limit"].(int64); ok {
		limit = l
	}

	var result []map[string]any
	for _, row := range r.rows {
		// Subject filter.
		if subject != "" {
			if s, _ := row["subject"].(string); s != subject {
				continue
			}
		}
		// Predicate filter.
		if predicate != "" {
			if p, _ := row["predicate"].(string); p != predicate {
				continue
			}
		}
		// Status filter.
		if status != "" {
			if st, _ := row["status"].(string); st != status {
				continue
			}
		}
		// Cursor filter: keep rows that come "after" the cursor in DESC order.
		// Mirrors: f.recorded_at < $cursorTime
		//          OR (f.recorded_at = $cursorTime AND f.fact_id < $cursorFactID)
		if hasCursor {
			rowTime, _ := row["recorded_at"].(time.Time)
			rowFactID, _ := row["fact_id"].(string)
			if rowTime.After(cursorTime) {
				continue // row is "before" cursor position in DESC order
			}
			if rowTime.Equal(cursorTime) && rowFactID >= cursorFactID {
				continue // at cursor position or "before" it in DESC order
			}
		}
		result = append(result, row)
	}

	if int64(len(result)) > limit {
		result = result[:limit]
	}
	return nil, result, nil
}

// Compile-time check: filteringStubFactReader satisfies factReader.
var _ factReader = (*filteringStubFactReader)(nil)

// TestListFacts covers AC-41: paginated fact listing scoped to a profile.
func TestListFacts(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	t.Run("returns facts for profile", func(t *testing.T) {
		row := makeFactRow("fact-1", "Alice", "knows", "active", now)
		reader := &stubFactReader{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := NewListFactsService(reader)

		facts, nextCursor, err := svc.List(ctx, profileID, FactListFilters{}, 10, "")

		require.NoError(t, err)
		require.Len(t, facts, 1)
		require.Equal(t, "fact-1", facts[0].FactID)
		require.Equal(t, profileID, facts[0].ProfileID)
		// 1 result with limit 10 → short page → no cursor.
		require.Empty(t, nextCursor)
	})

	t.Run("returns empty list when no facts exist", func(t *testing.T) {
		reader := &stubFactReader{
			responsesByCall: map[int][]map[string]any{
				0: {},
			},
		}
		svc := NewListFactsService(reader)

		facts, nextCursor, err := svc.List(ctx, profileID, FactListFilters{}, 10, "")

		require.NoError(t, err)
		require.Empty(t, facts)
		require.Empty(t, nextCursor)
	})

	t.Run("returns cursor when full page is returned", func(t *testing.T) {
		rows := make([]map[string]any, 2)
		for i := range rows {
			ts := now.Add(-time.Duration(i) * time.Second)
			rows[i] = makeFactRow("fact-"+string(rune('A'+i)), "Alice", "knows", "active", ts)
		}
		reader := &stubFactReader{
			responsesByCall: map[int][]map[string]any{
				0: rows,
			},
		}
		svc := NewListFactsService(reader)

		facts, nextCursor, err := svc.List(ctx, profileID, FactListFilters{}, 2, "")

		require.NoError(t, err)
		require.Len(t, facts, 2)
		require.NotEmpty(t, nextCursor, "cursor must be present when full page is returned")
	})

	t.Run("no cursor when page is shorter than limit", func(t *testing.T) {
		row := makeFactRow("fact-1", "Alice", "knows", "active", now)
		reader := &stubFactReader{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := NewListFactsService(reader)

		_, nextCursor, err := svc.List(ctx, profileID, FactListFilters{}, 10, "")

		require.NoError(t, err)
		require.Empty(t, nextCursor, "no cursor when result count < limit")
	})

	t.Run("zero limit applies defaultFactListLimit", func(t *testing.T) {
		row := makeFactRow("fact-1", "Alice", "knows", "active", now)
		reader := &stubFactReader{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := NewListFactsService(reader)

		facts, _, err := svc.List(ctx, profileID, FactListFilters{}, 0, "")

		require.NoError(t, err)
		require.Len(t, facts, 1)
	})

	t.Run("oversized limit is clamped to maxFactListLimit", func(t *testing.T) {
		row := makeFactRow("fact-1", "Alice", "knows", "active", now)
		reader := &stubFactReader{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := NewListFactsService(reader)

		facts, _, err := svc.List(ctx, profileID, FactListFilters{}, 99999, "")

		require.NoError(t, err)
		require.Len(t, facts, 1)
	})

	t.Run("hydrates fact fields from row", func(t *testing.T) {
		row := makeFactRow("fact-hydrate", "Bob", "has_skill", "active", now)
		reader := &stubFactReader{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := NewListFactsService(reader)

		facts, _, err := svc.List(ctx, profileID, FactListFilters{}, 10, "")

		require.NoError(t, err)
		require.Len(t, facts, 1)
		f := facts[0]
		require.Equal(t, "fact-hydrate", f.FactID)
		require.Equal(t, profileID, f.ProfileID)
		require.Equal(t, "Bob", f.Subject)
		require.Equal(t, "has_skill", f.Predicate)
		require.InDelta(t, 0.85, f.TruthScore, 1e-9)
		require.ElementsMatch(t, []string{"tag-a", "tag-b"}, f.Labels)
	})

	t.Run("decodes JSON encoded classification from list rows", func(t *testing.T) {
		row := makeFactRow("fact-json", "Bob", "has_skill", "active", now)
		row["classification"] = nil
		row["classification_json"] = `{"domain":"test","confidentiality":"internal"}`
		reader := &stubFactReader{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := NewListFactsService(reader)

		facts, _, err := svc.List(ctx, profileID, FactListFilters{}, 10, "")

		require.NoError(t, err)
		require.Len(t, facts, 1)
		require.Equal(t, "internal", facts[0].Classification["confidentiality"])
	})

	t.Run("propagates reader error", func(t *testing.T) {
		readerErr := errors.New("neo4j unavailable")
		reader := &stubFactReader{err: readerErr}
		svc := NewListFactsService(reader)

		_, _, err := svc.List(ctx, profileID, FactListFilters{}, 10, "")

		require.Error(t, err)
		require.Contains(t, err.Error(), "neo4j unavailable")
	})

	t.Run("returns error on invalid cursor token", func(t *testing.T) {
		reader := &stubFactReader{}
		svc := NewListFactsService(reader)

		_, _, err := svc.List(ctx, profileID, FactListFilters{}, 10, "!!!not-base64!!!")

		require.Error(t, err)
		require.Contains(t, err.Error(), "invalid cursor")
	})
}

// TestListFacts_FilterBySubject verifies that only facts with the specified
// subject are returned when a subject filter is applied (AC-41).
func TestListFacts_FilterBySubject(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	rows := []map[string]any{
		makeFactRow("fact-alice-1", "Alice", "knows", "active", now),
		makeFactRow("fact-bob", "Bob", "knows", "active", now.Add(-time.Second)),
		makeFactRow("fact-alice-2", "Alice", "likes", "active", now.Add(-2*time.Second)),
	}
	reader := &filteringStubFactReader{rows: rows}
	svc := NewListFactsService(reader)

	facts, _, err := svc.List(ctx, profileID, FactListFilters{Subject: "Alice"}, 10, "")

	require.NoError(t, err)
	require.Len(t, facts, 2, "only Alice's facts must be returned")
	for _, f := range facts {
		require.Equal(t, "Alice", f.Subject, "all returned facts must have Subject=Alice")
	}
	ids := make([]string, len(facts))
	for i, f := range facts {
		ids[i] = f.FactID
	}
	require.NotContains(t, ids, "fact-bob", "Bob's fact must not appear in Alice-filtered results")
}

// TestListFacts_FilterByPredicate verifies that only facts with the specified
// predicate are returned when a predicate filter is applied (AC-41).
func TestListFacts_FilterByPredicate(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	rows := []map[string]any{
		makeFactRow("fact-knows-1", "Alice", "knows", "active", now),
		makeFactRow("fact-likes", "Alice", "likes", "active", now.Add(-time.Second)),
		makeFactRow("fact-knows-2", "Bob", "knows", "active", now.Add(-2*time.Second)),
	}
	reader := &filteringStubFactReader{rows: rows}
	svc := NewListFactsService(reader)

	facts, _, err := svc.List(ctx, profileID, FactListFilters{Predicate: "knows"}, 10, "")

	require.NoError(t, err)
	require.Len(t, facts, 2, "only knows-predicate facts must be returned")
	for _, f := range facts {
		require.Equal(t, "knows", f.Predicate, "all returned facts must have Predicate=knows")
	}
	ids := make([]string, len(facts))
	for i, f := range facts {
		ids[i] = f.FactID
	}
	require.NotContains(t, ids, "fact-likes", "likes fact must not appear in knows-filtered results")
}

// TestListFacts_FilterByStatus verifies that only facts with the specified
// status are returned when a status filter is applied (AC-41).
func TestListFacts_FilterByStatus(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	rows := []map[string]any{
		makeFactRow("fact-active-1", "Alice", "knows", "active", now),
		makeFactRow("fact-retracted", "Alice", "knows", "retracted", now.Add(-time.Second)),
		makeFactRow("fact-active-2", "Bob", "knows", "active", now.Add(-2*time.Second)),
	}
	reader := &filteringStubFactReader{rows: rows}
	svc := NewListFactsService(reader)

	facts, _, err := svc.List(ctx, profileID, FactListFilters{Status: domain.FactStatusActive}, 10, "")

	require.NoError(t, err)
	require.Len(t, facts, 2, "only active facts must be returned")
	for _, f := range facts {
		require.Equal(t, domain.FactStatusActive, f.Status, "all returned facts must have Status=active")
	}
	ids := make([]string, len(facts))
	for i, f := range facts {
		ids[i] = f.FactID
	}
	require.NotContains(t, ids, "fact-retracted", "retracted fact must not appear in active-filtered results")
}

// TestListFacts_CombinedFilters verifies that subject, predicate, and status
// filters compose with AND semantics — only facts matching all criteria return.
func TestListFacts_CombinedFilters(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	rows := []map[string]any{
		makeFactRow("match", "Alice", "knows", "active", now),
		makeFactRow("wrong-subject", "Bob", "knows", "active", now.Add(-time.Second)),
		makeFactRow("wrong-predicate", "Alice", "likes", "active", now.Add(-2*time.Second)),
		makeFactRow("wrong-status", "Alice", "knows", "retracted", now.Add(-3*time.Second)),
	}
	reader := &filteringStubFactReader{rows: rows}
	svc := NewListFactsService(reader)

	facts, _, err := svc.List(ctx, profileID, FactListFilters{
		Subject:   "Alice",
		Predicate: "knows",
		Status:    domain.FactStatusActive,
	}, 10, "")

	require.NoError(t, err)
	require.Len(t, facts, 1)
	require.Equal(t, "match", facts[0].FactID)
}

// TestListFacts_CursorPagination verifies keyset cursor pagination (AC-41):
//   - page 1 returns a cursor when a full page is received
//   - page 2 uses that cursor to retrieve the next page without overlap
//   - a short final page carries no cursor
//   - ordering is (recorded_at DESC, fact_id DESC) across all pages
func TestListFacts_CursorPagination(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	base := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	// Five facts pre-sorted in DESC order (most recent first), ready for the
	// filteringStubFactReader which preserves insertion order.
	rows := []map[string]any{
		makeFactRow("fact-5", "Alice", "knows", "active", base.Add(4*time.Second)), // newest
		makeFactRow("fact-4", "Alice", "knows", "active", base.Add(3*time.Second)),
		makeFactRow("fact-3", "Alice", "knows", "active", base.Add(2*time.Second)),
		makeFactRow("fact-2", "Alice", "knows", "active", base.Add(time.Second)),
		makeFactRow("fact-1", "Alice", "knows", "active", base), // oldest
	}
	reader := &filteringStubFactReader{rows: rows}
	svc := NewListFactsService(reader)

	// Page 1: first two facts.
	page1, cursor1, err := svc.List(ctx, profileID, FactListFilters{}, 2, "")
	require.NoError(t, err)
	require.Len(t, page1, 2)
	require.NotEmpty(t, cursor1, "cursor must be present after a full first page")
	require.Equal(t, "fact-5", page1[0].FactID)
	require.Equal(t, "fact-4", page1[1].FactID)

	// Page 2: use cursor from page 1.
	page2, cursor2, err := svc.List(ctx, profileID, FactListFilters{}, 2, cursor1)
	require.NoError(t, err)
	require.Len(t, page2, 2)
	require.NotEmpty(t, cursor2, "cursor must be present after a full second page")
	require.Equal(t, "fact-3", page2[0].FactID)
	require.Equal(t, "fact-2", page2[1].FactID)

	// No overlap between page 1 and page 2.
	page1IDs := map[string]bool{page1[0].FactID: true, page1[1].FactID: true}
	for _, f := range page2 {
		require.False(t, page1IDs[f.FactID], "page 2 must not overlap with page 1: %s", f.FactID)
	}

	// Page 3: short page → no cursor.
	page3, cursor3, err := svc.List(ctx, profileID, FactListFilters{}, 2, cursor2)
	require.NoError(t, err)
	require.Len(t, page3, 1)
	require.Empty(t, cursor3, "no cursor when final page is short")
	require.Equal(t, "fact-1", page3[0].FactID)
}

// TestListFacts_CrossProfileIsolation verifies that facts belonging to profile A
// are not returned when querying as profile B.
//
// This is a mandatory security test per .claude/rules/profile-isolation.md.
func TestListFacts_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()
	const profileA = "00000000-0000-0000-0000-000000000001"
	const profileB = "00000000-0000-0000-0000-000000000002"
	now := time.Now().UTC()

	rowA := makeFactRow("fact-a", "Alice", "knows", "active", now)

	// The stub models Neo4j's profile-scoped isolation: ScopedRead returns only
	// rows registered for the given profileID, mirroring the
	// {team_id: $profileId} MATCH filter. Profile B receives no rows.
	reader := &stubFactReader{
		rowsByProfile: map[string][]map[string]any{
			profileA: {rowA},
			profileB: {},
		},
	}
	svcA := NewListFactsService(reader)
	reader.callCount = 0

	// Profile A can see its own facts.
	factsA, _, errA := svcA.List(ctx, profileA, FactListFilters{}, 10, "")
	require.NoError(t, errA)
	require.Len(t, factsA, 1)
	require.Equal(t, "fact-a", factsA[0].FactID)
	require.Equal(t, profileA, factsA[0].ProfileID)

	// Reset for profile B query.
	readerB := &stubFactReader{
		rowsByProfile: map[string][]map[string]any{
			profileA: {rowA},
			profileB: {},
		},
	}
	svcB := NewListFactsService(readerB)

	// Profile B sees no facts — profile A's data must not leak.
	factsB, nextCursorB, errB := svcB.List(ctx, profileB, FactListFilters{}, 10, "")
	require.NoError(t, errB)
	require.Empty(t, factsB, "profile B must not receive profile A's facts")
	require.Empty(t, nextCursorB)

	// Collect all fact IDs visible to profile B and assert absence of profile A's IDs.
	bIDs := make([]string, len(factsB))
	for i, f := range factsB {
		bIDs[i] = f.FactID
	}
	require.NotContains(t, bIDs, "fact-a",
		"profile A's fact must not be visible to profile B")
}
