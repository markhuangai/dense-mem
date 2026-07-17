package claimservice

import (
	"context"
	"errors"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

// stubScopedReader implements supportedFragmentsReader for unit tests.
// Rows are keyed by profileID so cross-profile isolation scenarios can be
// simulated without a real Neo4j instance.
type stubScopedReader struct {
	rowsByProfile map[string][]map[string]any
	err           error
}

func (s *stubScopedReader) ScopedRead(
	_ context.Context,
	profileID string,
	_ string,
	_ map[string]any,
) (neo4j.ResultSummary, []map[string]any, error) {
	if s.err != nil {
		return nil, nil, s.err
	}
	return nil, s.rowsByProfile[profileID], nil
}

// Compile-time check: stubScopedReader satisfies the local interface.
var _ supportedFragmentsReader = (*stubScopedReader)(nil)

// TestLoadSupportingFragments covers AC-10 and AC-15.
func TestLoadSupportingFragments(t *testing.T) {
	ctx := context.Background()
	const profileID = "profile-a"

	t.Run("returns fragments with content, source_quality, classification", func(t *testing.T) {
		reader := &stubScopedReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {
					{
						"fragment_id":    "frag-1",
						"content":        "the earth is round",
						"source_quality": 0.9,
						"classification": map[string]any{
							"confidentiality": "internal",
							"pii":             "none",
						},
					},
					{
						"fragment_id":    "frag-2",
						"content":        "water is H2O",
						"source_quality": 0.7,
						"classification": map[string]any{
							"confidentiality": "public",
							"pii":             "none",
						},
					},
				},
			},
		}

		got, err := loadSupportingFragments(ctx, reader, profileID, []string{"frag-1", "frag-2"})
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Len(t, got.Fragments, 2)

		// MaxSourceQuality must be max(0.9, 0.7) = 0.9
		require.InDelta(t, 0.9, got.MaxSourceQuality, 1e-9)

		// MergedClassification via lattice: confidentiality max(internal, public) = internal
		require.Equal(t, "internal", got.MergedClassification["confidentiality"])
		require.Equal(t, "none", got.MergedClassification["pii"])

		// ProfileID propagated to every fragment
		for _, f := range got.Fragments {
			require.Equal(t, profileID, f.ProfileID)
		}

		// Content is carried through
		contentSeen := make(map[string]bool)
		for _, f := range got.Fragments {
			contentSeen[f.Content] = true
		}
		require.True(t, contentSeen["the earth is round"])
		require.True(t, contentSeen["water is H2O"])
	})

	t.Run("returns ErrSupportingFragmentMissing when a fragment is absent", func(t *testing.T) {
		reader := &stubScopedReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {
					{
						"fragment_id":    "frag-1",
						"content":        "present",
						"source_quality": 0.5,
						"classification": nil,
					},
				},
			},
		}

		// frag-missing is not returned by the reader (not found / retracted)
		_, err := loadSupportingFragments(ctx, reader, profileID, []string{"frag-1", "frag-missing"})
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrSupportingFragmentMissing),
			"expected ErrSupportingFragmentMissing, got %v", err)
	})

	t.Run("retracted fragment treated as missing", func(t *testing.T) {
		// The Cypher coalesce guard filters retracted fragments server-side;
		// they simply do not appear in rows. The stub models this behaviour.
		reader := &stubScopedReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {}, // retracted fragment excluded by query
			},
		}

		_, err := loadSupportingFragments(ctx, reader, profileID, []string{"frag-retracted"})
		require.Error(t, err)
		require.True(t, errors.Is(err, ErrSupportingFragmentMissing),
			"retracted fragment must return ErrSupportingFragmentMissing")
	})

	t.Run("empty fragmentIDs returns empty result without error", func(t *testing.T) {
		reader := &stubScopedReader{rowsByProfile: map[string][]map[string]any{}}

		got, err := loadSupportingFragments(ctx, reader, profileID, []string{})
		require.NoError(t, err)
		require.Empty(t, got.Fragments)
		require.InDelta(t, 0.0, got.MaxSourceQuality, 1e-9)
		require.NotNil(t, got.MergedClassification)
	})

	t.Run("single fragment sets maxSourceQuality and classification correctly", func(t *testing.T) {
		reader := &stubScopedReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {
					{
						"fragment_id":    "frag-only",
						"content":        "solo",
						"source_quality": 0.42,
						"classification": map[string]any{
							"retention": "long_term",
						},
					},
				},
			},
		}

		got, err := loadSupportingFragments(ctx, reader, profileID, []string{"frag-only"})
		require.NoError(t, err)
		require.Len(t, got.Fragments, 1)
		require.InDelta(t, 0.42, got.MaxSourceQuality, 1e-9)
		require.Equal(t, "long_term", got.MergedClassification["retention"])
	})

	t.Run("decodes JSON encoded fragment classification", func(t *testing.T) {
		reader := &stubScopedReader{
			rowsByProfile: map[string][]map[string]any{
				profileID: {
					{
						"fragment_id":         "frag-json",
						"content":             "json encoded",
						"source_quality":      0.55,
						"classification":      nil,
						"classification_json": `{"confidentiality":"internal","pii":"none"}`,
						"authority":           "primary",
					},
				},
			},
		}

		got, err := loadSupportingFragments(ctx, reader, profileID, []string{"frag-json"})
		require.NoError(t, err)
		require.Len(t, got.Fragments, 1)
		require.Equal(t, "internal", got.MergedClassification["confidentiality"])
		require.Equal(t, "none", got.Fragments[0].Classification["pii"])
	})

	t.Run("propagates reader error", func(t *testing.T) {
		reader := &stubScopedReader{err: errors.New("neo4j unavailable")}

		_, err := loadSupportingFragments(ctx, reader, profileID, []string{"frag-1"})
		require.Error(t, err)
		require.Contains(t, err.Error(), "neo4j unavailable")
	})
}

// TestLoadSupportingFragments_CrossProfileIsolation verifies that data from
// profile A is not returned when querying as profile B, and vice versa.
// This is a mandatory security test per AGENTS.md.
func TestLoadSupportingFragments_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()
	const profileA = "profile-a"
	const profileB = "profile-b"

	// The stub models Neo4j's profile-scoped isolation: each profile key
	// returns only that profile's fragments. This simulates the $profileId
	// filter that ScopedRead enforces in production.
	reader := &stubScopedReader{
		rowsByProfile: map[string][]map[string]any{
			profileA: {
				{
					"fragment_id":    "frag-a1",
					"content":        "A's private data",
					"source_quality": 0.8,
					"classification": nil,
				},
			},
			profileB: {
				{
					"fragment_id":    "frag-b1",
					"content":        "B's private data",
					"source_quality": 0.6,
					"classification": nil,
				},
			},
		},
	}

	// Profile B querying a profile A fragment must fail.
	_, err := loadSupportingFragments(ctx, reader, profileB, []string{"frag-a1"})
	require.Error(t, err, "profile B must not access profile A's fragments")
	require.True(t, errors.Is(err, ErrSupportingFragmentMissing),
		"cross-profile read must return ErrSupportingFragmentMissing, got: %v", err)

	// Profile A querying a profile B fragment must also fail.
	_, err = loadSupportingFragments(ctx, reader, profileA, []string{"frag-b1"})
	require.Error(t, err, "profile A must not access profile B's fragments")
	require.True(t, errors.Is(err, ErrSupportingFragmentMissing),
		"cross-profile read must return ErrSupportingFragmentMissing, got: %v", err)

	// Profile A can access its own fragment.
	gotA, err := loadSupportingFragments(ctx, reader, profileA, []string{"frag-a1"})
	require.NoError(t, err)
	require.Len(t, gotA.Fragments, 1)
	aIDs := make([]string, len(gotA.Fragments))
	for i, f := range gotA.Fragments {
		aIDs[i] = f.FragmentID
	}
	require.NotContains(t, aIDs, "frag-b1",
		"profile B fragment must not appear in profile A's result")

	// Profile B can access its own fragment.
	gotB, err := loadSupportingFragments(ctx, reader, profileB, []string{"frag-b1"})
	require.NoError(t, err)
	require.Len(t, gotB.Fragments, 1)
	bIDs := make([]string, len(gotB.Fragments))
	for i, f := range gotB.Fragments {
		bIDs[i] = f.FragmentID
	}
	require.NotContains(t, bIDs, "frag-a1",
		"profile A fragment must not appear in profile B's result")
}
