package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRecallFeedbackEventRepositoryRecordsSnapshotBeforeFeedback(t *testing.T) {
	_, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	repo := NewRecallFeedbackEventRepository(appDB, rls)
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	recallID := "rec_" + uuid.NewString()

	used := true
	answerSupported := false
	missingContext := true
	irrelevant := true
	err := repo.RecordFeedback(ctx, domain.RecallFeedbackEvent{
		RecallID:        recallID,
		TeamID:          &teamID,
		ProfileID:       &profileID,
		KeyID:           &keyID,
		AuthMethod:      "api_key",
		Used:            &used,
		AnswerSupported: &answerSupported,
		Quality:         "low",
		MissingContext:  &missingContext,
		Irrelevant:      &irrelevant,
		FeedbackComment: "feedback must not create a snapshot row",
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrRecallFeedbackEventNotFound), "err = %v", err)

	createdAt := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)
	err = repo.RecordSnapshot(ctx, domain.RecallFeedbackEvent{
		RecallID:                  recallID,
		CreatedAt:                 createdAt,
		UpdatedAt:                 createdAt,
		TeamID:                    &teamID,
		ProfileID:                 &profileID,
		KeyID:                     &keyID,
		AuthMethod:                "api_key",
		ToolName:                  "recall_memory",
		Query:                     "postgres memory",
		ToolArgs:                  map[string]any{"input": map[string]any{"query": "postgres memory"}},
		ContractVersion:           domain.V2ContractVersion,
		RankingProfileVersion:     "ranking-v1",
		EmbeddingContractVersion:  "embedding-v1",
		SearchIndexProfileVersion: "search-v1",
		SearchState:               string(domain.V2SearchProjectionCurrent),
		Degradation:               map[string]any{"vector": "unavailable"},
		SnapshotMetadata:          map[string]any{"result_schema": "v2.evidence_relationship_refs.v1"},
		ResultRefs: []domain.RecallFeedbackResultRef{{
			Type:           domain.RecallFeedbackResultTypeEvidence,
			ID:             "00000000-0000-0000-0000-00000000e001",
			Rank:           1,
			Tier:           "evidence",
			StatusAtRecall: string(domain.V2SearchProjectionCurrent),
		}},
	})
	require.NoError(t, err)

	err = repo.RecordFeedback(ctx, domain.RecallFeedbackEvent{
		RecallID:        recallID,
		KeyID:           &keyID,
		AuthMethod:      "api_key",
		Used:            &used,
		AnswerSupported: &answerSupported,
		Quality:         "low",
		MissingContext:  &missingContext,
		Irrelevant:      &irrelevant,
		FeedbackComment: "result was unrelated to the query",
		IrrelevantRefs: []domain.RecallFeedbackJudgedResultRef{{
			Type: domain.RecallFeedbackResultTypeEvidence,
			ID:   "00000000-0000-0000-0000-00000000e001",
			Rank: 1,
		}},
	})
	require.NoError(t, err)

	got, err := repo.Get(ctx, recallID)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, domain.V2ContractVersion, got.ContractVersion)
	require.Equal(t, string(domain.V2SearchProjectionCurrent), got.SearchState)
	require.Equal(t, map[string]any{"vector": "unavailable"}, got.Degradation)
	require.Equal(t, map[string]any{"result_schema": "v2.evidence_relationship_refs.v1"}, got.SnapshotMetadata)
	require.Len(t, got.ResultRefs, 1)
	require.Equal(t, domain.RecallFeedbackResultTypeEvidence, got.ResultRefs[0].Type)
	require.Equal(t, "low", got.Quality)
	require.NotNil(t, got.FeedbackAt)
	require.Len(t, got.IrrelevantRefs, 1)

	page, err := repo.List(ctx, domain.RecallFeedbackEventFilter{TeamID: &teamID})
	require.NoError(t, err)
	require.Equal(t, int64(1), page.Total)
	require.Len(t, page.Items, 1)
}
