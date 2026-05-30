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

// stubFactReader implements factReader for unit tests.
//
// Rows are keyed by profileID to model Neo4j's profile-scoped isolation:
// ScopedRead returns only the rows registered for the given profile, mirroring
// the {team_id: $profileId} filter in the Cypher queries.
//
// callCount tracks how many times ScopedRead has been called so list tests can
// provide different responses per call (page query vs count query).
type stubFactReader struct {
	// rowsByProfile maps profileID → rows returned for that profile.
	rowsByProfile map[string][]map[string]any
	// responsesByCall maps call index (0-based) → rows; takes precedence when set.
	responsesByCall map[int][]map[string]any
	callCount       int
	err             error
}

func (s *stubFactReader) ScopedRead(
	_ context.Context,
	profileID string,
	_ string,
	_ map[string]any,
) (neo4j.ResultSummary, []map[string]any, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	idx := s.callCount
	s.callCount++
	if s.responsesByCall != nil {
		if rows, ok := s.responsesByCall[idx]; ok {
			return nil, rows, nil
		}
	}
	return nil, s.rowsByProfile[profileID], nil
}

// Compile-time check: stubFactReader satisfies the package-internal interface.
var _ factReader = (*stubFactReader)(nil)

// makeFactRow builds a minimal Neo4j result row for a Fact node.
func makeFactRow(factID, subject, predicate, status string, recordedAt time.Time) map[string]any {
	return map[string]any{
		"fact_id":                        factID,
		"subject":                        subject,
		"predicate":                      predicate,
		"object":                         "object-value",
		"status":                         status,
		"truth_score":                    0.85,
		"valid_from":                     nil,
		"valid_to":                       nil,
		"recorded_at":                    recordedAt,
		"retracted_at":                   nil,
		"last_confirmed_at":              nil,
		"promoted_from_claim_id":         "claim-xyz",
		"classification":                 map[string]any{"domain": "test"},
		"classification_lattice_version": "v1",
		"source_quality":                 0.9,
		"labels":                         []any{"tag-a", "tag-b"},
		"metadata":                       nil,
	}
}

// TestGetFact covers AC-41: fact retrieval must enforce same-profile 404
// and hydrate all Fact domain fields correctly.
func TestGetFact(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	const factID = "fact-abc"
	now := time.Now().UTC()

	t.Run("returns fact when profile and ID match", func(t *testing.T) {
		row := makeFactRow(factID, "Alice", "knows", "active", now)
		reader := &stubFactReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {row},
			},
		}
		svc := NewGetFactService(reader)

		got, err := svc.Get(ctx, profileID, factID)

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, factID, got.FactID)
		require.Equal(t, profileID, got.ProfileID)
		require.Equal(t, "Alice", got.Subject)
		require.Equal(t, "knows", got.Predicate)
		require.Equal(t, "object-value", got.Object)
		require.Equal(t, domain.FactStatusActive, got.Status)
		require.InDelta(t, 0.85, got.TruthScore, 1e-9)
		require.InDelta(t, 0.9, got.SourceQuality, 1e-9)
		require.Equal(t, "claim-xyz", got.PromotedFromClaimID)
		require.Equal(t, "v1", got.ClassificationLatticeVersion)
		require.Equal(t, "test", got.Classification["domain"])
		require.ElementsMatch(t, []string{"tag-a", "tag-b"}, got.Labels)
		require.Equal(t, now, got.RecordedAt)
	})

	t.Run("returns ErrFactNotFound when fact does not exist", func(t *testing.T) {
		reader := &stubFactReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {}, // no rows — MATCH found nothing
			},
		}
		svc := NewGetFactService(reader)

		got, err := svc.Get(ctx, profileID, "nonexistent-fact")

		require.Nil(t, got)
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrFactNotFound),
			"missing fact must return ErrFactNotFound, got: %v", err)
	})

	t.Run("decodes JSON encoded classification", func(t *testing.T) {
		row := makeFactRow(factID, "Alice", "knows", "active", now)
		row["classification"] = nil
		row["classification_json"] = `{"domain":"test","confidentiality":"internal"}`
		reader := &stubFactReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {row},
			},
		}
		svc := NewGetFactService(reader)

		got, err := svc.Get(ctx, profileID, factID)

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "test", got.Classification["domain"])
		require.Equal(t, "internal", got.Classification["confidentiality"])
	})

	t.Run("propagates reader error", func(t *testing.T) {
		readerErr := errors.New("neo4j unavailable")
		reader := &stubFactReader{err: readerErr}
		svc := NewGetFactService(reader)

		got, err := svc.Get(ctx, profileID, factID)

		require.Nil(t, got)
		require.Error(t, err)
		require.Contains(t, err.Error(), "neo4j unavailable")
	})

	t.Run("temporal fields are propagated correctly", func(t *testing.T) {
		validFrom := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		validTo := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		recordedAt := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)
		retractedAt := time.Date(2024, 9, 1, 0, 0, 0, 0, time.UTC)
		lastConfirmedAt := time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)

		row := map[string]any{
			"fact_id":                        factID,
			"subject":                        "X",
			"predicate":                      "y",
			"object":                         "Z",
			"status":                         "retracted",
			"truth_score":                    0.6,
			"valid_from":                     validFrom,
			"valid_to":                       validTo,
			"recorded_at":                    recordedAt,
			"retracted_at":                   retractedAt,
			"last_confirmed_at":              lastConfirmedAt,
			"promoted_from_claim_id":         "claim-temporal",
			"classification":                 nil,
			"classification_lattice_version": "",
			"source_quality":                 0.5,
			"labels":                         []any{},
			"metadata":                       nil,
		}
		reader := &stubFactReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {row},
			},
		}
		svc := NewGetFactService(reader)

		got, err := svc.Get(ctx, profileID, factID)

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, domain.FactStatusRetracted, got.Status)
		require.NotNil(t, got.ValidFrom)
		require.Equal(t, validFrom, *got.ValidFrom)
		require.NotNil(t, got.ValidTo)
		require.Equal(t, validTo, *got.ValidTo)
		require.Equal(t, recordedAt, got.RecordedAt)
		require.NotNil(t, got.RetractedAt)
		require.Equal(t, retractedAt, *got.RetractedAt)
		require.NotNil(t, got.LastConfirmedAt)
		require.Equal(t, lastConfirmedAt, *got.LastConfirmedAt)
	})

	t.Run("nil optional fields map to nil pointers", func(t *testing.T) {
		row := makeFactRow(factID, "Sun", "is", "active", now)
		row["valid_from"] = nil
		row["valid_to"] = nil
		row["retracted_at"] = nil
		row["last_confirmed_at"] = nil
		row["classification"] = nil
		row["labels"] = []any{}
		reader := &stubFactReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {row},
			},
		}
		svc := NewGetFactService(reader)

		got, err := svc.Get(ctx, profileID, factID)

		require.NoError(t, err)
		require.Nil(t, got.ValidFrom)
		require.Nil(t, got.ValidTo)
		require.Nil(t, got.RetractedAt)
		require.Nil(t, got.LastConfirmedAt)
		require.Nil(t, got.Classification)
		require.Empty(t, got.Labels)
	})
}

// TestGetFact_CrossProfileIsolation verifies that a fact belonging to profile A
// is not returned when querying as profile B, and that existence under the
// other profile is not leaked. This is a mandatory security test per
// .claude/rules/profile-isolation.md.
func TestGetFact_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()
	const profileA = "00000000-0000-0000-0000-000000000001"
	const profileB = "00000000-0000-0000-0000-000000000002"
	const sharedFactID = "fact-shared-id"
	now := time.Now().UTC()

	// The stub models Neo4j's profile-scoped isolation: ScopedRead returns only
	// the rows registered for the given profileID, mirroring the
	// {team_id: $profileId} MATCH filter. Profile B gets no rows even though
	// the fact ID matches — exactly as production Neo4j would behave.
	row := makeFactRow(sharedFactID, "Alice", "knows", "active", now)
	reader := &stubFactReader{
		rowsByProfile: map[string][]map[string]any{
			profileA: {row}, // fact belongs to profile A
			profileB: {},    // profile B sees nothing for this fact ID
		},
	}
	svc := NewGetFactService(reader)

	// Profile B must not receive profile A's fact.
	gotB, errB := svc.Get(ctx, profileB, sharedFactID)
	require.Nil(t, gotB, "profile B must not receive profile A's fact")
	require.Error(t, errB)
	require.True(t, errors.Is(errB, ErrFactNotFound),
		"profile B query must return ErrFactNotFound (no existence leak), got: %v", errB)

	// Profile A can retrieve its own fact without error.
	gotA, errA := svc.Get(ctx, profileA, sharedFactID)
	require.NoError(t, errA, "profile A must be able to retrieve its own fact")
	require.NotNil(t, gotA)
	require.Equal(t, profileA, gotA.ProfileID,
		"returned fact must carry profile A's ID")
	require.Equal(t, sharedFactID, gotA.FactID)

	// The profile A result must not reference profile B.
	bResults := []string{gotA.ProfileID}
	require.NotContains(t, bResults, profileB,
		"profile A's fact must not reference profile B")
}

func TestGetFactByIDsAndEvidenceMapping(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	now := time.Now().UTC()

	row := makeFactRow("fact-1", "Alice", "likes", "active", now)
	row["created_by_profile_id"] = "creator-1"
	row["created_by_profile_name"] = "Creator"
	row["promoted_by_profile_id"] = "promoter-1"
	row["promoted_by_profile_name"] = "Promoter"
	row["metadata"] = `{"source":"unit"}`
	row["evidence"] = []any{
		map[string]any{
			"fragment_id":        "fragment-1",
			"speaker":            "Alice",
			"span_start":         int64(4),
			"span_end":           int(12),
			"extract_conf":       float32(0.75),
			"extraction_model":   "model-a",
			"extraction_version": "1",
			"pipeline_run_id":    "run-1",
			"authority":          "secondary",
		},
		map[string]any{"fragment_id": ""},
		"not-a-map",
	}

	reader := &stubFactReader{
		rowsByProfile: map[string][]map[string]any{
			profileID: {row, {"fact_id": "", "recorded_at": now}},
		},
	}
	svc := NewGetFactService(reader)
	batchSvc := svc.(interface {
		GetByIDs(ctx context.Context, profileID string, factIDs []string) (map[string]*domain.Fact, error)
	})

	empty, err := batchSvc.GetByIDs(ctx, profileID, nil)
	require.NoError(t, err)
	require.Empty(t, empty)

	got, err := batchSvc.GetByIDs(ctx, profileID, []string{"fact-1", "missing"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	fact := got["fact-1"]
	require.NotNil(t, fact)
	require.Equal(t, "creator-1", fact.CreatedByProfileID)
	require.Equal(t, "promoter-1", fact.PromotedByProfileID)
	require.Equal(t, "unit", fact.Metadata["source"])
	require.Len(t, fact.Evidence, 1)
	require.Equal(t, "fragment-1", fact.Evidence[0].FragmentID)
	require.Equal(t, 4, fact.Evidence[0].SpanStart)
	require.Equal(t, 12, fact.Evidence[0].SpanEnd)
	require.InDelta(t, 0.75, fact.Evidence[0].ExtractConf, 1e-6)
	require.Equal(t, domain.AuthoritySecondary, fact.Evidence[0].Authority)

	reader.err = errors.New("read failed")
	got, err = batchSvc.GetByIDs(ctx, profileID, []string{"fact-1"})
	require.Nil(t, got)
	require.ErrorContains(t, err, "fact batch get")
}
