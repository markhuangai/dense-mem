package recallservice

import (
	"context"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
	"github.com/stretchr/testify/require"
)

func TestRecallService_CurrentnessRerankPrefersCurrentUpdate(t *testing.T) {
	query := "What is the current deployment window for service OBS-001?"
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:      "f-archived",
				Type:    "fragment",
				Content: "Archived 2025 calendar. service OBS-001 deployed at 02:01 UTC before the correction.",
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-current",
				Content:    "Current release calendar update dated 2026-06-28. service OBS-001 now deploys at 03:01 UTC.",
			},
			{
				FragmentID: "f-archived",
				Content:    "Archived 2025 calendar. service OBS-001 deployed at 02:01 UTC before the correction.",
			},
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-current", out[0].Fragment.FragmentID)
}

func TestCurrentnessAdjustmentRequiresQueryIdentifiersForBoosts(t *testing.T) {
	query := "What is the current deployment window for service OBS-001?"
	neighbor := "Current release calendar update dated 2026-06-28. service OBS-002 now deploys at 03:02 UTC."

	require.Zero(t, currentnessAdjustment(query, neighbor))
}

func TestRecallService_CurrentnessRerankPrefersNewerDatedFragment(t *testing.T) {
	query := "Who is the current owner for account TMP-001?"
	oldCreated := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	newCreated := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:        "f-old-current",
				Type:      "fragment",
				Content:   "Current owner record. account TMP-001 owner is alice.",
				CreatedAt: oldCreated,
				UpdatedAt: oldCreated,
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-new-dated",
				Content:    "Owner record dated 2026-06-26. account TMP-001 owner is bob.",
				CreatedAt:  newCreated,
				UpdatedAt:  newCreated,
			},
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-new-dated", out[0].Fragment.FragmentID)
}

func TestRecallService_ValidAtFiltersFutureFragmentMetadata(t *testing.T) {
	query := "As of 2026-06-22, what owner did service TIER-W-001 use?"
	validAt := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC)
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:       "f-future",
				Type:     "fragment",
				Content:  "Since 2026-06-27, service TIER-W-001 owner uses owner-cobalt.",
				Metadata: map[string]any{"valid_from": "2026-06-27T00:00:00Z"},
			},
			{
				ID:       "f-old",
				Type:     "fragment",
				Content:  "Since 2026-06-20, service TIER-W-001 owner uses owner-coral.",
				Metadata: map[string]any{"valid_from": "2026-06-20T00:00:00Z"},
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-future",
				Content:    "Since 2026-06-27, service TIER-W-001 owner uses owner-cobalt.",
				Metadata:   map[string]any{"valid_from": "2026-06-27T00:00:00Z"},
			},
			{
				FragmentID: "f-old",
				Content:    "Since 2026-06-20, service TIER-W-001 owner uses owner-coral.",
				Metadata:   map[string]any{"valid_from": "2026-06-20T00:00:00Z"},
			},
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5, ValidAt: &validAt})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-old", out[0].Fragment.FragmentID)
	for _, hit := range out {
		if hit.Fragment != nil {
			require.NotEqual(t, "f-future", hit.Fragment.FragmentID)
		}
	}
}

func TestFragmentMetadataKnownAtRequiresExplicitRecordedMetadata(t *testing.T) {
	knownAt := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)

	require.True(t, fragmentMetadataMatchesRecallWindow(nil, nil, &knownAt))
	require.False(t, fragmentMetadataMatchesRecallWindow(
		map[string]any{"recorded_at": "2026-06-29T00:00:00Z"},
		nil,
		&knownAt,
	))
}

func TestRecallService_CurrentnessRerankDatedFragmentBeatsUndatedCurrentCue(t *testing.T) {
	query := "Who is the current owner for account TMP-001?"
	oldCreated := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	newCreated := time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	oldCurrent := semanticsearch.SearchHit{
		ID:        "f-old-current",
		Type:      "fragment",
		Content:   "Current owner record. account TMP-001 owner is alice.",
		CreatedAt: oldCreated,
		UpdatedAt: oldCreated,
	}
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{oldCurrent},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-old-current",
				Content:    oldCurrent.Content,
				CreatedAt:  oldCreated,
				UpdatedAt:  oldCreated,
			},
			{
				FragmentID: "f-new-dated",
				Content:    "Owner record dated 2026-06-26. account TMP-001 owner is bob.",
				CreatedAt:  newCreated,
				UpdatedAt:  newCreated,
			},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-new-dated", out[0].Fragment.FragmentID)
}

func TestRecallService_CurrentnessRerankPrefersRelativeDatedFragment(t *testing.T) {
	query := "Who is the current owner for service TMP-002?"
	importedAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:        "f-undated-current",
				Type:      "fragment",
				Content:   "Current owner note. service TMP-002 owner uses owner-moon.",
				CreatedAt: importedAt,
				UpdatedAt: importedAt,
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-relative-dated",
				Content:    "Owner update from yesterday. service TMP-002 owner uses owner-sun.",
				CreatedAt:  importedAt,
				UpdatedAt:  importedAt,
			},
			{
				FragmentID: "f-undated-current",
				Content:    "Current owner note. service TMP-002 owner uses owner-moon.",
				CreatedAt:  importedAt,
				UpdatedAt:  importedAt,
			},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-relative-dated", out[0].Fragment.FragmentID)
}

func TestRecallService_CurrentnessRerankPrefersMonthNameDatedFragment(t *testing.T) {
	query := "Who is the current owner for service TMP-003?"
	importedAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:        "f-undated-current",
				Type:      "fragment",
				Content:   "Current owner note. service TMP-003 owner uses owner-moon.",
				CreatedAt: importedAt,
				UpdatedAt: importedAt,
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-month-dated",
				Content:    "Owner update dated June 27, 2026. service TMP-003 owner uses owner-sun.",
				CreatedAt:  importedAt,
				UpdatedAt:  importedAt,
			},
			{
				FragmentID: "f-undated-current",
				Content:    "Current owner note. service TMP-003 owner uses owner-moon.",
				CreatedAt:  importedAt,
				UpdatedAt:  importedAt,
			},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-month-dated", out[0].Fragment.FragmentID)
}

func TestRecallService_CurrentnessRerankDemotesExpiredValidityRange(t *testing.T) {
	query := "Who is the current owner for service TMP-004?"
	importedAt := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	expired := "Current owner note from the old rollout. service TMP-004 owner uses owner-cinder. This assignment is valid through 2026-06-20."
	current := "Current owner note. service TMP-004 owner uses owner-aurora."
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:        "f-expired-range",
				Type:      "fragment",
				Content:   expired,
				CreatedAt: importedAt,
				UpdatedAt: importedAt,
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-expired-range",
				Content:    expired,
				CreatedAt:  importedAt,
				UpdatedAt:  importedAt,
			},
			{
				FragmentID: "f-current",
				Content:    current,
				CreatedAt:  importedAt,
				UpdatedAt:  importedAt,
			},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-current", out[0].Fragment.FragmentID)
}

func TestRecallService_CurrentnessRerankHandlesActiveDecisionQueries(t *testing.T) {
	query := "What is the active launch decision for notice RET-046?"
	required := "Retraction update dated 2026-06-28. Earlier approval for notice RET-046 was withdrawn. The active decision is launch-paused-046."
	stale := "Original announcement said notice RET-046 was launch-approved-046 before the retraction."
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{ID: "f-stale", Type: "fragment", Content: stale},
			{ID: "f-required", Type: "fragment", Content: required},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{FragmentID: "f-required", Content: required},
			{FragmentID: "f-stale", Content: stale},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-required", out[0].Fragment.FragmentID)
}
