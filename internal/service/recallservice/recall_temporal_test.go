package recallservice

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestRecallService_CurrentnessRanksFactByTripleContentDate(t *testing.T) {
	requiredRecordedAt := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	negativeRecordedAt := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	factSearcher := &fakeFactSearcher{
		results: []FactRecallResult{
			{FactID: "fact-required", ProfileID: "pA", RecordedAt: requiredRecordedAt},
			{FactID: "fact-negative", ProfileID: "pA", RecordedAt: negativeRecordedAt},
		},
	}
	factGetter := &fakeFactGetter{
		facts: map[string]*domain.Fact{
			"fact-required": {
				FactID:     "fact-required",
				ProfileID:  "pA",
				Subject:    "service TIER-D-001 owner as of June 27, 2026",
				Predicate:  "uses",
				Object:     "owner-jade",
				Status:     domain.FactStatusActive,
				TruthScore: 0.7,
				RecordedAt: requiredRecordedAt,
			},
			"fact-negative": {
				FactID:     "fact-negative",
				ProfileID:  "pA",
				Subject:    "service TIER-D-001 owner as of June 20, 2026",
				Predicate:  "uses",
				Object:     "owner-onyx",
				Status:     domain.FactStatusActive,
				TruthScore: 0.99,
				RecordedAt: negativeRecordedAt,
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{},
		&fakeKeywordSearcher{},
		&fakeHydrator{},
		factSearcher,
		factGetter,
		nil,
		nil,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "Who is the current owner for service TIER-D-001?", Limit: 1})

	require.NoError(t, err)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].Fact)
	require.Equal(t, "fact-required", out[0].Fact.FactID)
}

func TestRecallService_CurrentnessRanksClaimByTripleContentDate(t *testing.T) {
	requiredRecordedAt := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	negativeRecordedAt := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	claimSearcher := &fakeClaimSearcher{
		results: []ClaimRecallResult{
			{ClaimID: "claim-required", ProfileID: "pA", RecordedAt: requiredRecordedAt},
			{ClaimID: "claim-negative", ProfileID: "pA", RecordedAt: negativeRecordedAt},
		},
	}
	claimGetter := &fakeClaimGetter{
		claims: map[string]*domain.Claim{
			"claim-required": {
				ClaimID:     "claim-required",
				ProfileID:   "pA",
				Subject:     "service TIER-D-002 pager as of June 27, 2026",
				Predicate:   "uses",
				Object:      "pager-jade",
				Status:      domain.StatusValidated,
				ExtractConf: 0.2,
				RecordedAt:  requiredRecordedAt,
			},
			"claim-negative": {
				ClaimID:     "claim-negative",
				ProfileID:   "pA",
				Subject:     "service TIER-D-002 pager as of June 20, 2026",
				Predicate:   "uses",
				Object:      "pager-onyx",
				Status:      domain.StatusValidated,
				ExtractConf: 0.99,
				RecordedAt:  negativeRecordedAt,
			},
		},
	}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{},
		&fakeKeywordSearcher{},
		&fakeHydrator{},
		nil,
		nil,
		claimSearcher,
		claimGetter,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "What is the latest pager for service TIER-D-002?", Limit: 1})

	require.NoError(t, err)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].Claim)
	require.Equal(t, "claim-required", out[0].Claim.ClaimID)
}

func TestRecallService_CurrentnessRanksFactByEvidenceFragmentDate(t *testing.T) {
	requiredRecordedAt := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	negativeRecordedAt := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	factSearcher := &fakeFactSearcher{
		results: []FactRecallResult{
			{FactID: "fact-required", ProfileID: "pA", RecordedAt: requiredRecordedAt},
			{FactID: "fact-negative", ProfileID: "pA", RecordedAt: negativeRecordedAt},
		},
	}
	factGetter := &fakeFactGetter{
		facts: map[string]*domain.Fact{
			"fact-required": {
				FactID:     "fact-required",
				ProfileID:  "pA",
				Subject:    "service TIER-S-001 owner",
				Predicate:  "uses",
				Object:     "owner-jade",
				Status:     domain.FactStatusActive,
				TruthScore: 0.7,
				RecordedAt: requiredRecordedAt,
				Evidence:   []domain.Evidence{{FragmentID: "fragment-required"}},
			},
			"fact-negative": {
				FactID:     "fact-negative",
				ProfileID:  "pA",
				Subject:    "service TIER-S-001 owner",
				Predicate:  "uses",
				Object:     "owner-onyx",
				Status:     domain.FactStatusActive,
				TruthScore: 0.99,
				RecordedAt: negativeRecordedAt,
				Evidence:   []domain.Evidence{{FragmentID: "fragment-negative"}},
			},
		},
	}
	hydrator := &fakeHydrator{frags: map[string]*domain.Fragment{
		"fragment-required": {
			FragmentID: "fragment-required",
			ProfileID:  "pA",
			Content:    "Source update dated June 27, 2026. service TIER-S-001 owner uses owner-jade.",
			CreatedAt:  requiredRecordedAt,
			UpdatedAt:  requiredRecordedAt,
		},
		"fragment-negative": {
			FragmentID: "fragment-negative",
			ProfileID:  "pA",
			Content:    "Source update dated June 20, 2026. service TIER-S-001 owner uses owner-onyx.",
			CreatedAt:  negativeRecordedAt,
			UpdatedAt:  negativeRecordedAt,
		},
	}}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{},
		&fakeKeywordSearcher{},
		hydrator,
		factSearcher,
		factGetter,
		nil,
		nil,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "Who is the current owner for service TIER-S-001?", Limit: 1})

	require.NoError(t, err)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].Fact)
	require.Equal(t, "fact-required", out[0].Fact.FactID)
	require.Nil(t, out[0].Fact.Evidence)
	require.Equal(t, int32(1), atomic.LoadInt32(&hydrator.batchCallCount))
}

func TestRecallService_CurrentnessRanksClaimByEvidenceFragmentDate(t *testing.T) {
	requiredRecordedAt := time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC)
	negativeRecordedAt := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	claimSearcher := &fakeClaimSearcher{
		results: []ClaimRecallResult{
			{ClaimID: "claim-required", ProfileID: "pA", RecordedAt: requiredRecordedAt},
			{ClaimID: "claim-negative", ProfileID: "pA", RecordedAt: negativeRecordedAt},
		},
	}
	claimGetter := &fakeClaimGetter{
		claims: map[string]*domain.Claim{
			"claim-required": {
				ClaimID:     "claim-required",
				ProfileID:   "pA",
				Subject:     "service TIER-S-002 pager",
				Predicate:   "uses",
				Object:      "pager-jade",
				Status:      domain.StatusValidated,
				ExtractConf: 0.2,
				RecordedAt:  requiredRecordedAt,
				Evidence:    []domain.Evidence{{FragmentID: "fragment-required"}},
			},
			"claim-negative": {
				ClaimID:     "claim-negative",
				ProfileID:   "pA",
				Subject:     "service TIER-S-002 pager",
				Predicate:   "uses",
				Object:      "pager-onyx",
				Status:      domain.StatusValidated,
				ExtractConf: 0.99,
				RecordedAt:  negativeRecordedAt,
				Evidence:    []domain.Evidence{{FragmentID: "fragment-negative"}},
			},
		},
	}
	hydrator := &fakeHydrator{frags: map[string]*domain.Fragment{
		"fragment-required": {
			FragmentID: "fragment-required",
			ProfileID:  "pA",
			Content:    "Source update dated June 27, 2026. service TIER-S-002 pager uses pager-jade.",
			CreatedAt:  requiredRecordedAt,
			UpdatedAt:  requiredRecordedAt,
		},
		"fragment-negative": {
			FragmentID: "fragment-negative",
			ProfileID:  "pA",
			Content:    "Source update dated June 20, 2026. service TIER-S-002 pager uses pager-onyx.",
			CreatedAt:  negativeRecordedAt,
			UpdatedAt:  negativeRecordedAt,
		},
	}}
	svc := NewRecallServiceWithTiers(
		&stubEmbedding{DimensionsResult: 4},
		&fakeSemanticSearcher{},
		&fakeKeywordSearcher{},
		hydrator,
		nil,
		nil,
		claimSearcher,
		claimGetter,
		0,
		nil,
		nil,
	)

	out, err := svc.Recall(context.Background(), "pA", RecallRequest{Query: "What is the latest pager for service TIER-S-002?", Limit: 1})

	require.NoError(t, err)
	require.Len(t, out, 1)
	require.NotNil(t, out[0].Claim)
	require.Equal(t, "claim-required", out[0].Claim.ClaimID)
	require.Nil(t, out[0].Claim.Evidence)
	require.Equal(t, int32(1), atomic.LoadInt32(&hydrator.batchCallCount))
}

func TestTemporalRankTimeForRecallPrefersValidFromOverTripleContentDate(t *testing.T) {
	validFrom := time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)
	recordedAt := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)

	got := temporalRankTimeForRecall(
		"Who is the current owner for service TIER-D-003?",
		&validFrom,
		recordedAt,
		"service TIER-D-003 owner as of June 27, 2026",
		"uses",
		"owner-jade",
	)

	require.Equal(t, validFrom, got)
}

func TestTypedTemporalRankTimeForRecallDemotesExpiredValidTo(t *testing.T) {
	validFrom := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	validTo := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	recordedAt := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	frame := typedCurrentnessTemporalFrame{asOf: time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)}

	got := typedTemporalRankTimeForRecallWithEvidence(
		"Who is the current owner for service TIER-D-004?",
		&validFrom,
		&validTo,
		recordedAt,
		nil,
		nil,
		frame,
		"service TIER-D-004 owner",
		"uses",
		"owner-ember",
	)

	require.Zero(t, got)

	historical := typedTemporalRankTimeForRecallWithEvidence(
		"As of 2026-06-27, who owned service TIER-D-004?",
		&validFrom,
		&validTo,
		recordedAt,
		nil,
		nil,
		frame,
		"service TIER-D-004 owner",
		"uses",
		"owner-ember",
	)

	require.Equal(t, validFrom, historical)
}

func TestLatestTemporalDateInTextParsesWeekdayNames(t *testing.T) {
	anchor := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)

	require.Equal(
		t,
		time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
		latestTemporalDateInText("Owner update from Friday.", anchor),
	)
	require.Equal(
		t,
		time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC),
		latestTemporalDateInText("Owner update from last Friday.", anchor),
	)
	require.Equal(
		t,
		time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC),
		latestTemporalDateInText("Owner update from last Monday.", anchor),
	)
	require.Zero(t, latestTemporalDateInText("Owner update expected next Friday.", anchor))
}

func TestLatestTemporalDateInTextParsesUnambiguousNumericDates(t *testing.T) {
	anchor := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	expected := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)

	require.Equal(t, expected, latestTemporalDateInText("Owner update dated 6/27/2026.", anchor))
	require.Equal(t, expected, latestTemporalDateInText("Owner update dated 27/6/2026.", anchor))
	require.Equal(t, expected, latestTemporalDateInText("Owner update dated 2026/6/27.", anchor))
	require.Equal(t, expected, latestTemporalDateInText("Owner update dated 6/27.", anchor))
	require.Zero(t, latestTemporalDateInText("Owner update dated 6/7/2026.", anchor))
	require.Zero(t, latestTemporalDateInText("Owner update dated 2/31/2026.", anchor))
	require.Zero(t, latestTemporalDateInText("Owner update dated 6/27.", time.Time{}))
}

func TestTemporalScoringDeltasCoverAgeBands(t *testing.T) {
	cases := []struct {
		name     string
		age      time.Duration
		content  float64
		fragment float64
	}{
		{name: "negative", age: -time.Hour, content: 0.028, fragment: 0.014},
		{name: "same day", age: 12 * time.Hour, content: 0.014, fragment: 0.007},
		{name: "middle age", age: 96 * time.Hour, content: -0.018, fragment: -0.010},
		{name: "stale", age: 8 * 24 * time.Hour, content: -0.026, fragment: -0.014},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.InDelta(t, tc.content, contentDateTemporalDelta(tc.age), 0.000001)
			require.InDelta(t, tc.fragment, fragmentTimestampTemporalDelta(tc.age), 0.000001)
		})
	}
}

func TestCurrentnessTemporalAdjustmentCoversFallbacks(t *testing.T) {
	query := "Who is the current owner for service TMP-101?"
	latest := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	entry := rrfEntry{
		Content:   "service TMP-101 owner uses owner-green.",
		CreatedAt: latest.AddDate(0, 0, -4),
		UpdatedAt: latest.AddDate(0, 0, -4),
	}

	require.Zero(t, currentnessTemporalAdjustment(query, entry, currentnessTemporalFrame{}))
	require.InDelta(t, -0.006, currentnessTemporalAdjustment(query, entry, currentnessTemporalFrame{
		hasContentDate:    true,
		newestContentDate: latest,
	}), 0.000001)
	require.Zero(t, currentnessTemporalAdjustment(query, rrfEntry{Content: entry.Content}, currentnessTemporalFrame{
		useFragmentTimestamp: true,
		newestFragmentTime:   latest,
	}))
	require.InDelta(t, -0.010, currentnessTemporalAdjustment(query, entry, currentnessTemporalFrame{
		useFragmentTimestamp: true,
		newestFragmentTime:   latest,
	}), 0.000001)
}

func TestCurrentnessAsOfTimeFallbacks(t *testing.T) {
	created := time.Date(2026, 6, 20, 8, 0, 0, 0, time.UTC)
	updated := time.Date(2026, 6, 21, 8, 0, 0, 0, time.UTC)
	contentDate := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	fragmentTime := time.Date(2026, 6, 28, 8, 0, 0, 0, time.UTC)
	entry := rrfEntry{CreatedAt: created, UpdatedAt: updated}

	require.Equal(t, contentDate, currentnessAsOfTime("current owner as of 2026-06-27", entry, currentnessTemporalFrame{}))
	require.Equal(t, contentDate, currentnessAsOfTime("current owner", entry, currentnessTemporalFrame{newestContentDate: contentDate}))
	require.Equal(t, fragmentTime, currentnessAsOfTime("current owner", entry, currentnessTemporalFrame{newestFragmentTime: fragmentTime}))
	require.Equal(t, updated, currentnessAsOfTime("current owner", entry, currentnessTemporalFrame{}))
}

func TestCurrentnessAsOfTimeUsesSharedRelativeQueryAnchor(t *testing.T) {
	query := "Who was the current owner yesterday for service TMP-104?"
	newest := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	older := newest.AddDate(0, 0, -7)
	entries := []rrfEntry{
		{Content: "service TMP-104 owner uses owner-blue.", CreatedAt: older, UpdatedAt: older},
		{Content: "service TMP-104 owner uses owner-green.", CreatedAt: newest, UpdatedAt: newest},
	}

	frame := currentnessTemporalFrameFor(query, entries)

	want := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	require.Equal(t, want, frame.queryDate)
	require.Equal(t, want, currentnessAsOfTime(query, entries[0], frame))
	require.Equal(t, want, currentnessAsOfTime(query, entries[1], frame))
}

func TestTypedCurrentnessAsOfAndExpiryFallbacks(t *testing.T) {
	recordedAt := time.Date(2026, 6, 21, 8, 0, 0, 0, time.UTC)
	frameAsOf := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	validTo := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	futureValidTo := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	require.Equal(
		t,
		time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC),
		typedCurrentnessAsOfTime("current owner as of 2026-06-27", recordedAt, typedCurrentnessTemporalFrame{asOf: frameAsOf}),
	)
	require.Equal(t, frameAsOf, typedCurrentnessAsOfTime("current owner", recordedAt, typedCurrentnessTemporalFrame{asOf: frameAsOf}))
	require.Equal(t, recordedAt, typedCurrentnessAsOfTime("current owner", recordedAt, typedCurrentnessTemporalFrame{}))
	require.Zero(t, typedCurrentnessAsOfTime("current owner", time.Time{}, typedCurrentnessTemporalFrame{}))

	require.False(t, typedValidityExpiredForRecall("current owner", nil, recordedAt, typedCurrentnessTemporalFrame{asOf: frameAsOf}))
	require.False(t, typedValidityExpiredForRecall("current owner", &time.Time{}, recordedAt, typedCurrentnessTemporalFrame{asOf: frameAsOf}))
	require.False(t, typedValidityExpiredForRecall("historical owner", &validTo, recordedAt, typedCurrentnessTemporalFrame{asOf: frameAsOf}))
	require.False(t, typedValidityExpiredForRecall("current owner", &validTo, time.Time{}, typedCurrentnessTemporalFrame{}))
	require.False(t, typedValidityExpiredForRecall("current owner", &futureValidTo, recordedAt, typedCurrentnessTemporalFrame{asOf: frameAsOf}))
	require.True(t, typedValidityExpiredForRecall("current owner", &validTo, recordedAt, typedCurrentnessTemporalFrame{asOf: frameAsOf}))
}

func TestTypedCurrentnessAsOfTimeUsesSharedRelativeQueryAnchor(t *testing.T) {
	query := "Who was the current owner yesterday for service TMP-105?"
	newest := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	older := newest.AddDate(0, 0, -7)
	frame := typedCurrentnessTemporalFrameForFacts(query, []hydratedFactRecallCandidate{
		{Fact: &domain.Fact{Subject: "service TMP-105 owner", Predicate: "uses", Object: "owner-blue", RecordedAt: older}},
		{Fact: &domain.Fact{Subject: "service TMP-105 owner", Predicate: "uses", Object: "owner-green", RecordedAt: newest}},
	}, nil)

	want := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	require.Equal(t, want, frame.queryDate)
	require.Equal(t, want, typedCurrentnessAsOfTime(query, older, frame))
	require.Equal(t, want, typedCurrentnessAsOfTime(query, newest, frame))
}

func TestTypedCurrentnessTemporalFramesSkipNilAndIdentifierMismatches(t *testing.T) {
	query := "Who is the current owner for service TMP-102?"
	contentDate := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	recordedAt := time.Date(2026, 6, 21, 8, 0, 0, 0, time.UTC)

	updateTypedCurrentnessTemporalFrame(nil, query, nil, recordedAt, nil, nil, "service TMP-102 owner as of June 27, 2026")

	factFrame := typedCurrentnessTemporalFrameForFacts(query, []hydratedFactRecallCandidate{
		{},
		{Fact: &domain.Fact{Subject: "service TMP-999 owner as of June 29, 2026", RecordedAt: recordedAt}},
		{Fact: &domain.Fact{
			Subject:    "service TMP-102 owner as of June 27, 2026",
			Predicate:  "uses",
			Object:     "owner-green",
			RecordedAt: recordedAt,
		}},
	}, nil)
	require.Equal(t, contentDate, factFrame.asOf)

	claimFrame := typedCurrentnessTemporalFrameForClaims(query, []*domain.Claim{
		nil,
		{Subject: "service TMP-999 owner as of June 29, 2026", RecordedAt: recordedAt},
		{
			Subject:    "service TMP-102 owner as of June 27, 2026",
			Predicate:  "uses",
			Object:     "owner-green",
			RecordedAt: recordedAt,
		},
	}, nil)
	require.Equal(t, contentDate, claimFrame.asOf)
}

func TestLatestTemporalDateInEvidenceSkipsInvalidEvidence(t *testing.T) {
	anchor := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	query := "Who is the current owner for service TMP-103?"
	fragments := map[string]*domain.Fragment{
		"mismatch": {
			Content:   "Source dated June 29, 2026 says service TMP-999 owner uses owner-red.",
			CreatedAt: anchor,
			UpdatedAt: anchor,
		},
		"older": {
			Content:   "Source dated June 26, 2026 says service TMP-103 owner uses owner-blue.",
			CreatedAt: anchor,
			UpdatedAt: anchor,
		},
		"newer": {
			Content:   "Source dated June 27, 2026 says service TMP-103 owner uses owner-green.",
			CreatedAt: anchor,
			UpdatedAt: anchor,
		},
	}

	require.Zero(t, latestTemporalDateInEvidence(query, nil, fragments))
	require.Zero(t, latestTemporalDateInEvidence(query, []domain.Evidence{{FragmentID: "newer"}}, nil))
	require.Equal(t, time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC), latestTemporalDateInEvidence(query, []domain.Evidence{
		{},
		{FragmentID: "missing"},
		{FragmentID: "mismatch"},
		{FragmentID: "older"},
		{FragmentID: "newer"},
	}, fragments))
}

func TestTemporalTokenHelpersCoverRecognizedValues(t *testing.T) {
	months := map[string]time.Month{
		"jan": time.January, "feb": time.February, "mar": time.March, "apr": time.April,
		"may": time.May, "jun": time.June, "jul": time.July, "aug": time.August,
		"sep": time.September, "oct": time.October, "nov": time.November, "dec": time.December,
	}
	for token, want := range months {
		got, ok := monthNameNumber(token)
		require.True(t, ok)
		require.Equal(t, want, got)
	}
	_, ok := monthNameNumber("never")
	require.False(t, ok)

	weekdays := map[string]time.Weekday{
		"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
		"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday,
	}
	for token, want := range weekdays {
		got, ok := weekdayNumber(token)
		require.True(t, ok)
		require.Equal(t, want, got)
	}
	_, ok = weekdayNumber("never")
	require.False(t, ok)

	days := map[string]int{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7}
	for token, want := range days {
		got, ok := relativeDayCount(token)
		require.True(t, ok)
		require.Equal(t, want, got)
	}
	_, ok = relativeDayCount("eight")
	require.False(t, ok)
}

func TestTemporalDateAtFieldsCoversSupportedForms(t *testing.T) {
	anchor := time.Date(2026, 6, 29, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		fields []string
		index  int
		want   time.Time
		ok     bool
	}{
		{name: "iso", fields: []string{"2026-06-27"}, want: time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC), ok: true},
		{name: "numeric", fields: []string{"6/27"}, want: time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC), ok: true},
		{name: "month day year", fields: []string{"june", "27", "2026"}, want: time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC), ok: true},
		{name: "day month year", fields: []string{"27", "june", "2026"}, want: time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC), ok: true},
		{name: "out of range", fields: []string{"june"}, index: 2},
		{name: "invalid", fields: []string{"soon"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := temporalDateAtFields(tc.fields, tc.index, anchor)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestIdentifierMatchingHelpersCoverNilAndMismatches(t *testing.T) {
	require.False(t, factMatchesQueryIdentifiers("current owner for service TMP-104", nil))
	require.False(t, claimMatchesQueryIdentifiers("current owner for service TMP-104", nil))
	require.False(t, factMatchesQueryIdentifiers(
		"current owner for service TMP-104",
		&domain.Fact{Subject: "service TMP-999 owner", Predicate: "uses", Object: "owner-red"},
	))
	require.False(t, claimMatchesQueryIdentifiers(
		"current owner for service TMP-104",
		&domain.Claim{Subject: "service TMP-999 owner", Predicate: "uses", Object: "owner-red"},
	))
	require.True(t, factMatchesQueryIdentifiers(
		"current owner for service TMP-104",
		&domain.Fact{Subject: "service TMP-104 owner", Predicate: "uses", Object: "owner-green"},
	))
	require.True(t, claimMatchesQueryIdentifiers(
		"current owner for service TMP-104",
		&domain.Claim{Subject: "service TMP-104 owner", Predicate: "uses", Object: "owner-green"},
	))
	require.True(t, knowledgeTripleMatchesQueryIdentifiers("current owner", nil, "service TMP-999 owner"))
	require.False(t, knowledgeTripleMatchesQueryIdentifiers(
		"Radioiodine treatment of non-toxic multinodular goitre reduces thyroid volume.",
		nil,
		"issue DM-412",
		"profile_fact",
		"low recall ratings require deterministic identifier extraction at write and recall time",
	))
	require.False(t, knowledgeTripleMatchesQueryIdentifiers(
		"how do we fix recall accuracy",
		nil,
		"issue DM-412",
		"profile_fact",
		"low recall ratings require deterministic identifier extraction at write and recall time",
	))
	require.True(t, knowledgeTripleMatchesQueryIdentifiers(
		"low recall ratings",
		nil,
		"issue DM-412",
		"profile_fact",
		"low recall ratings require deterministic identifier extraction at write and recall time",
	))
}
