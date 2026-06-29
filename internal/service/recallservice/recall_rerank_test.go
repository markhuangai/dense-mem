package recallservice

import (
	"context"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
	"github.com/stretchr/testify/require"
)

func TestCurrentnessTemporalAdjustmentRequiresQueryIdentifiers(t *testing.T) {
	query := "Who is the current owner for account TMP-001?"
	newest := time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)
	frame := currentnessTemporalFrame{hasContentDate: true, newestContentDate: newest}
	neighbor := rrfEntry{
		Content:   "Owner record dated 2026-06-26. account TMP-002 owner is bob.",
		CreatedAt: newest,
		UpdatedAt: newest,
	}

	require.Zero(t, currentnessTemporalAdjustment(query, neighbor, frame))
}

func TestCurrentnessTemporalFramePrefersContentDatesOverBulkImportTimes(t *testing.T) {
	query := "What is the current deployment window for service OBS-001?"
	importedAt := time.Date(2026, 6, 28, 20, 0, 0, 0, time.UTC)
	entries := []rrfEntry{
		{
			Content:   "Current release calendar update dated 2026-06-28. service OBS-001 now deploys at 03:01 UTC.",
			CreatedAt: importedAt,
			UpdatedAt: importedAt,
		},
		{
			Content:   "Draft migration plan suggested 02:01 UTC for service OBS-001, but the plan was replaced.",
			CreatedAt: importedAt,
			UpdatedAt: importedAt,
		},
	}

	frame := currentnessTemporalFrameFor(query, entries)

	require.True(t, frame.hasContentDate)
	require.False(t, frame.useFragmentTimestamp)
	require.Positive(t, currentnessTemporalAdjustment(query, entries[0], frame))
	require.Negative(t, currentnessTemporalAdjustment(query, entries[1], frame))
}

func TestExpiredValidityAdjustmentUsesAsOfTime(t *testing.T) {
	query := "Who is the current owner for service TMP-004?"
	entry := rrfEntry{
		Content:   "Current owner note. service TMP-004 owner uses owner-cinder. This assignment is valid through June 30, 2026.",
		CreatedAt: time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC),
	}
	frame := currentnessTemporalFrame{newestFragmentTime: entry.CreatedAt}

	require.Zero(t, expiredValidityAdjustment(query, entry, frame))

	frame.newestFragmentTime = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	require.Negative(t, expiredValidityAdjustment(query, entry, frame))
}

func TestLatestTemporalDateInEntryParsesRelativeDatesFromFragmentTimestamp(t *testing.T) {
	anchor := time.Date(2026, 6, 28, 18, 30, 0, 0, time.UTC)
	cases := []struct {
		name    string
		content string
		want    time.Time
	}{
		{
			name:    "yesterday",
			content: "Owner update from yesterday. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "numeric days ago",
			content: "Owner update from 2 days ago. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "word days ago",
			content: "Owner update from two days ago. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "last week",
			content: "Owner update from last week. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "month name with year",
			content: "Owner update dated June 27, 2026. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "abbreviated month with anchored year",
			content: "Owner update dated Jun 27. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC),
		},
		{
			name:    "day before month",
			content: "Owner update dated 27 June 2026. service TMP-002 owner uses owner-sun.",
			want:    time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := latestTemporalDateInEntry(rrfEntry{Content: tc.content, CreatedAt: anchor, UpdatedAt: anchor})

			require.Equal(t, tc.want, got)
		})
	}
}

func TestLatestCurrentnessTemporalDateInEntryIgnoresValidityEndOnlyDate(t *testing.T) {
	anchor := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	entry := rrfEntry{
		Content:   "Current owner note. service TMP-004 owner uses owner-cinder. This assignment is valid through 2026-06-20.",
		CreatedAt: anchor,
		UpdatedAt: anchor,
	}

	require.Equal(t, time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC), latestValidityEndDateInEntry(entry))
	require.Zero(t, latestCurrentnessTemporalDateInEntry(entry))

	entry.Content = "Owner update dated 2026-06-27. service TMP-004 owner uses owner-aurora; the old assignment was valid through 2026-06-20."
	require.Equal(t, time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC), latestCurrentnessTemporalDateInEntry(entry))
}

func TestRerankAdjustmentsCoverCapsAndIdentifierGuards(t *testing.T) {
	currentQuery := "Who is the current owner for service TMP-201?"
	require.Zero(t, currentnessAdjustment("", "current service TMP-201"))
	require.Positive(t, currentnessAdjustment(currentQuery, "Current service TMP-201 owner uses owner-green."))
	require.InDelta(
		t,
		0.018,
		currentnessAdjustment(currentQuery, "Current archived service TMP-201 owner uses owner-green."),
		0.000001,
	)
	require.InDelta(
		t,
		-0.028,
		currentnessAdjustment(currentQuery, "Archived draft service TMP-201 owner uses owner-red."),
		0.000001,
	)

	selectionQuery := "Which enterprise job TMP-202 should use the timeout?"
	require.Zero(t, cueAdjustment(selectionQuery, "Enterprise job TMP-999 must use timeout 30s."))
	require.InDelta(
		t,
		0.026,
		cueAdjustment(selectionQuery, "Enterprise job TMP-202 must use the canonical current timeout policy."),
		0.000001,
	)
	require.InDelta(
		t,
		-0.026,
		cueAdjustment(selectionQuery, "Forbidden legacy draft note for job TMP-202 before timeout migration."),
		0.000001,
	)

	authorityQuery := "What is the authoritative required owner for service TMP-203?"
	require.Zero(t, authorityAdjustment(authorityQuery, "Authoritative source says service TMP-999 owner uses owner-red."))
	require.Positive(t, authorityAdjustment(authorityQuery, "Authoritative canonical policy requires service TMP-203 owner green."))
	require.InDelta(
		t,
		-0.034,
		authorityAdjustment(authorityQuery, "Informal chat suggested service TMP-203 owner red while testing."),
		0.000001,
	)
	require.True(t, identifiersMatchContent(nil, ""))
	require.Equal(t, []string{"tmp-204"}, rerankIdentifiers(rerankText("TMP-204 TMP-204 2026-06-29")))
}

func TestApplyRerankAdjustmentsClampNegativeScoresAndSkipOtherQueries(t *testing.T) {
	entries := []rrfEntry{{Content: "Informal chat suggested service TMP-204 owner red while testing.", FinalScore: 0.001}}
	applyAuthorityAdjustments("What is the authoritative required owner for service TMP-204?", entries)
	require.Zero(t, entries[0].FinalScore)

	entries = []rrfEntry{{Content: "Archived draft service TMP-205 owner uses owner-red.", FinalScore: 0.001}}
	applyCurrentnessAdjustments("Who is the current owner for service TMP-205?", entries)
	require.Zero(t, entries[0].FinalScore)

	entries = []rrfEntry{{Content: "Forbidden legacy draft note for job TMP-206 before timeout migration.", FinalScore: 0.001}}
	applyCueAdjustments("Which job TMP-206 should use timeout?", entries)
	require.Zero(t, entries[0].FinalScore)

	unchanged := []rrfEntry{{Content: "Informal chat suggested service TMP-207 owner red.", FinalScore: 0.001}}
	applyAuthorityAdjustments("Who owns service TMP-207?", unchanged)
	require.Equal(t, 0.001, unchanged[0].FinalScore)
}

func TestIdentifierSpecificityAdjustmentsCoverQueryShapes(t *testing.T) {
	entries := []rrfEntry{{Content: "job TMP-301 should use timeout 30s", FinalScore: 1}}
	applyIdentifierSpecificityAdjustments("Which job should use timeout?", entries)
	require.Equal(t, 1.0, entries[0].FinalScore)

	entries = []rrfEntry{{Content: "job TMP-301 should use timeout 30s", FinalScore: 1}}
	applyIdentifierSpecificityAdjustments("Which timeout should job use?", entries)
	require.Equal(t, 1.0, entries[0].FinalScore)

	entries = []rrfEntry{
		{Content: "job TMP-301 should use timeout 30s", FinalScore: 1},
		{Content: "job TMP-999 should use timeout 30s", FinalScore: 1},
		{Content: "", FinalScore: 1},
	}
	applyIdentifierSpecificityAdjustments("Which timeout should job TMP-301 use?", entries)
	require.InDelta(t, 1.004, entries[0].FinalScore, 0.000001)
	require.Equal(t, 1.0, entries[1].FinalScore)
	require.Equal(t, 1.0, entries[2].FinalScore)
}

func TestFragmentMetadataRecallWindowCoversMetadataFormats(t *testing.T) {
	base := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	before := base.Add(-time.Hour)
	after := base.Add(time.Hour)

	got, ok := metadataTime(map[string]any{"recordedAt": base.Format(time.RFC3339Nano)}, "recorded_at", "recordedAt")
	require.True(t, ok)
	require.Equal(t, base, got)
	_, ok = metadataTime(map[string]any{"recorded_at": "not-time"}, "recorded_at")
	require.False(t, ok)
	_, ok = metadataTime(map[string]any{"recorded_at": 123}, "recorded_at")
	require.False(t, ok)

	require.False(t, fragmentMetadataMatchesRecallWindow(map[string]any{"valid_from": after}, &base, nil))
	require.False(t, fragmentMetadataMatchesRecallWindow(map[string]any{"valid_to": base}, &base, nil))
	require.False(t, fragmentMetadataMatchesRecallWindow(map[string]any{"recorded_at": after}, nil, &base))
	require.False(t, fragmentMetadataMatchesRecallWindow(map[string]any{"recorded_to": base}, nil, &base))
	require.True(t, fragmentMetadataMatchesRecallWindow(map[string]any{
		"valid_from":  before,
		"valid_to":    after,
		"recorded_at": before,
		"recorded_to": after,
	}, &base, &base))
}

func TestFilterNonPositiveRRFEntriesDropsZeroScoresWhenPositiveExists(t *testing.T) {
	entries := []rrfEntry{
		{id: "positive", FinalScore: 0.01},
		{id: "zero", FinalScore: 0},
	}

	out := filterNonPositiveRRFEntries(entries)

	require.Len(t, out, 1)
	require.Equal(t, "positive", out[0].id)
}

func TestFilterNonPositiveRRFEntriesKeepsAllZeroScoresWhenNoPositiveExists(t *testing.T) {
	entries := []rrfEntry{
		{id: "zero-1", FinalScore: 0},
		{id: "zero-2", FinalScore: 0},
	}

	out := filterNonPositiveRRFEntries(entries)

	require.Len(t, out, 2)
	require.Equal(t, "zero-1", out[0].id)
	require.Equal(t, "zero-2", out[1].id)
}

func TestRecallService_IdentifierSpecificityPrefersExactJobID(t *testing.T) {
	query := "What timeout should job UNT-013 use?"
	neighbor := "Runtime configuration. job UNT-003 must use a timeout of 13 minutes."
	required := "Runtime configuration. job UNT-013 must use a timeout of 23 minutes."
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{ID: "f-neighbor", Type: "fragment", Content: neighbor},
			{ID: "f-filler-1", Type: "fragment", Content: "Runtime configuration. job UNT-001 must use a timeout of 11 minutes."},
			{ID: "f-filler-2", Type: "fragment", Content: "Runtime configuration. job UNT-041 must use a timeout of 11 minutes."},
			{ID: "f-required", Type: "fragment", Content: required},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{FragmentID: "f-required", Content: required},
			{FragmentID: "f-filler-1", Content: "Runtime configuration. job UNT-001 must use a timeout of 11 minutes."},
			{FragmentID: "f-neighbor", Content: neighbor},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-required", out[0].Fragment.FragmentID)
}

func TestIdentifierSpecificityAdjustmentRequiresExactIdentifier(t *testing.T) {
	queryText := rerankText("What timeout should job UNT-013 use?")

	require.Positive(t, identifierSpecificityAdjustment(queryText, "Runtime configuration. job UNT-013 must use a timeout of 23 minutes."))
	require.Zero(t, identifierSpecificityAdjustment(queryText, "Runtime configuration. job UNT-003 must use a timeout of 13 minutes."))
}

func TestRerankIdentifiersSkipsISODateTokens(t *testing.T) {
	queryText := rerankText("As of 2026-06-22, what owner did service TIER-W-001 use?")

	require.Equal(t, []string{"tier-w-001"}, rerankIdentifiers(queryText))
}

func TestApplyIdentifierSpecificityAdjustmentsRequiresUnitValueQuery(t *testing.T) {
	entries := []rrfEntry{
		{
			Content:    "Current release calendar update dated 2026-06-28. service OBS-001 now deploys at 03:01 UTC.",
			FinalScore: 0.02,
		},
	}

	applyIdentifierSpecificityAdjustments("What is the current deployment window for service OBS-001?", entries)

	require.Equal(t, 0.02, entries[0].FinalScore)
}

func TestRecallService_CueRerankPrefersDirectiveEvidence(t *testing.T) {
	query := "Which pager should alerts for queue NEG-001 use?"
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:      "f-stale",
				Type:    "fragment",
				Content: "Before 2026, queue NEG-001 used pager-001-red for alerts.",
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-required",
				Content:    "Routing policy dated 2026-06-28. Alerts for queue NEG-001 must use pager-001-green.",
			},
			{
				FragmentID: "f-stale",
				Content:    "Before 2026, queue NEG-001 used pager-001-red for alerts.",
			},
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-required", out[0].Fragment.FragmentID)
	for _, hit := range out {
		require.NotEqual(t, "f-stale", hit.Fragment.FragmentID)
	}
}

func TestRecallService_CueRerankSuppressesHistoricalSiblingWhenDirectiveExists(t *testing.T) {
	query := "Which pager should alerts for queue NEG-001 use?"
	stale := "Before 2026, queue NEG-001 used pager-001-red for alerts."
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{ID: "f-stale", Type: "fragment", Content: stale},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-required",
				Content:    "Routing policy dated 2026-06-28. Alerts for queue NEG-001 must use pager-001-green. Do not use pager-001-red for this queue.",
			},
			{FragmentID: "f-stale", Content: stale},
			{FragmentID: "f-filler-1", Content: "Queue NEG-001 inventory reference for alert routing review."},
			{FragmentID: "f-filler-2", Content: "Queue NEG-001 runbook index entry for pager ownership audits."},
			{FragmentID: "f-filler-3", Content: "Queue NEG-001 on-call calendar note for escalation metadata."},
			{FragmentID: "f-filler-4", Content: "Queue NEG-001 dashboard bookmark for support handoff."},
		},
	}
	svc := NewRecallService(&stubEmbedding{DimensionsResult: 4}, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.Len(t, out, 5)
	for _, hit := range out {
		require.NotEqual(t, "f-stale", hit.Fragment.FragmentID)
	}
}

func TestHistoricalSelectionAdjustmentRequiresDirectiveFrame(t *testing.T) {
	query := "Which pager should alerts for queue NEG-001 use?"
	stale := "Before 2026, queue NEG-001 used pager-001-red for alerts."

	require.Zero(t, historicalSelectionAdjustment(query, stale, selectionCueFrame{}))
	require.Negative(t, historicalSelectionAdjustment(query, stale, selectionCueFrame{hasDirectiveMatch: true}))
}

func TestRecallCueAdjustmentRequiresQueryIdentifiersForBoosts(t *testing.T) {
	query := "Which endpoint should the west-030 region use for billing sync?"
	neighborTemplate := "Export routing rule. Enterprise tenants such as tenant CND-030 enterprise must use endpoint-enterprise-030."

	require.Zero(t, cueAdjustment(query, neighborTemplate))
}

func TestRecallService_AuthorityRerankPrefersSignedRunbook(t *testing.T) {
	query := "Which procedure does runbook AUT-061 require?"
	sem := &fakeSemanticSearcher{
		hits: []semanticsearch.SearchHit{
			{
				ID:      "f-chat",
				Type:    "fragment",
				Content: "An informal chat suggested procedure-061-chat for runbook AUT-061, but it was not approved.",
			},
		},
	}
	kw := &fakeKeywordSearcher{
		hits: []keywordsearch.FragmentSearchResult{
			{
				FragmentID: "f-required",
				Content:    "Authoritative runbook signed by operations. runbook AUT-061 requires procedure-061-canonical.",
			},
			{
				FragmentID: "f-chat",
				Content:    "An informal chat suggested procedure-061-chat for runbook AUT-061, but it was not approved.",
			},
		},
	}
	emb := &stubEmbedding{DimensionsResult: 4}
	svc := NewRecallService(emb, sem, kw, &fakeHydrator{}, nil, nil)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: query, Limit: 5})

	require.NoError(t, err)
	require.NotEmpty(t, out)
	require.Equal(t, "f-required", out[0].Fragment.FragmentID)
}

func TestAuthorityAdjustmentRequiresQueryIdentifiersForBoosts(t *testing.T) {
	query := "Which procedure does runbook AUT-061 require?"
	neighbor := "Authoritative runbook signed by operations. runbook AUT-062 requires procedure-062-canonical."

	require.Zero(t, authorityAdjustment(query, neighbor))
}
