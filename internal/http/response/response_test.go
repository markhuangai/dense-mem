package response

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestCommunityResponses(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	require.Nil(t, ToCommunityResponse(nil))

	community := &domain.Community{
		CommunityID:      "community-1",
		ProfileID:        "profile-1",
		Level:            2,
		Summary:          "summary",
		SummaryVersion:   "v1",
		MemberCount:      3,
		TopEntities:      []string{"alice"},
		TopPredicates:    []string{"knows"},
		LastSummarizedAt: now,
	}
	got := ToCommunityResponse(community)
	require.Equal(t, "community-1", got.CommunityID)
	require.Equal(t, now, got.LastSummarizedAt)

	list := ToListCommunitiesResponse([]*domain.Community{nil, community})
	require.Equal(t, 1, list.Total)
	require.Len(t, list.Items, 1)

	detect := ToCommunityDetectResponse([]*domain.Community{nil, community})
	require.True(t, detect.Detected)
	require.Equal(t, 1, detect.CommunityCount)
	require.Equal(t, 3, detect.NodeCount)
}

func TestFactResponses(t *testing.T) {
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	require.Nil(t, ToFactResponse(nil))
	require.Nil(t, toEvidenceResponses(nil))

	fact := &domain.Fact{
		FactID:                       "fact-1",
		ProfileID:                    "profile-1",
		CreatedByProfileID:           "creator",
		CreatedByProfileName:         "Creator",
		PromotedByProfileID:          "promoter",
		PromotedByProfileName:        "Promoter",
		Subject:                      "Alice",
		Predicate:                    "knows",
		Object:                       "Bob",
		Status:                       domain.FactStatusActive,
		TruthScore:                   0.91,
		RecordedAt:                   now,
		LastConfirmedAt:              &now,
		PromotedFromClaimID:          "claim-1",
		Classification:               map[string]any{"domain": "people"},
		ClassificationLatticeVersion: "v1",
		SourceQuality:                0.8,
		Labels:                       []string{"people"},
		Metadata:                     map[string]any{"source": "unit"},
		Evidence:                     []domain.Evidence{{FragmentID: "fragment-1", Speaker: "Alice", SpanStart: 1, SpanEnd: 2, ExtractConf: 0.9, ExtractionModel: "model", ExtractionVersion: "v1", PipelineRunID: "run", Authority: domain.AuthorityPrimary}},
	}

	got := ToFactResponse(fact)
	require.Equal(t, "fact-1", got.FactID)
	require.Equal(t, "active", got.Status)
	require.Len(t, got.Evidence, 1)
	require.Equal(t, "primary", got.Evidence[0].Authority)

	list := ToListFactsResponse([]*domain.Fact{nil, fact}, "next")
	require.True(t, list.HasMore)
	require.Equal(t, "next", list.NextCursor)
	require.Len(t, list.Items, 1)
}
