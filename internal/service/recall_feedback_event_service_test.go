package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestRecallFeedbackEventServiceRecordsSnapshotWithActorContext(t *testing.T) {
	ctx := context.Background()
	teamID := uuid.New()
	profileID := uuid.New()
	keyID := uuid.New()
	sharedSpaceID := uuid.New()
	spaceID := uuid.New()
	ctx = requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID, OwnerID: profileID, CredentialID: &keyID,
		AuthMethod: "api_key", Role: "manager",
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedSpaceID, Kind: domain.MemorySpaceTeamShared, Generation: 1},
			{ID: spaceID, Kind: domain.MemorySpaceCredentialPrivate, Generation: 3},
		},
	})

	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	repo := &recallFeedbackEventRepoStub{}
	svc := NewRecallFeedbackEventService(repo, nil, nil)
	svc.now = func() time.Time { return now }

	err := svc.RecordRecallSnapshot(ctx, domain.RecallFeedbackEvent{
		RecallID: "rec_1",
		Query:    "query",
		ToolArgs: map[string]any{"input": map[string]any{"query": "query"}},
		ResultRefs: []domain.RecallFeedbackResultRef{{
			Type: domain.RecallFeedbackResultTypeFragment,
			ID:   "fragment-1",
			Rank: 1,
		}},
	})
	require.NoError(t, err)
	require.Len(t, repo.snapshots, 1)
	got := repo.snapshots[0]
	assert.Equal(t, "rec_1", got.RecallID)
	assert.Equal(t, "query", got.Query)
	assert.Equal(t, domain.RecallFeedbackSnapshotCaptured, got.SnapshotState)
	assert.Equal(t, now, got.CreatedAt)
	assert.Equal(t, &teamID, got.TeamID)
	assert.Equal(t, &profileID, got.ProfileID)
	assert.Equal(t, &keyID, got.KeyID)
	assert.Equal(t, &spaceID, got.SpaceID)
	assert.EqualValues(t, 3, got.SpaceGeneration)
	assert.Equal(t, "api_key", got.AuthMethod)
}

func TestRecallFeedbackEventServiceRecordsFeedbackForCapturedSnapshotAndPrunesRetention(t *testing.T) {
	teamID, profileID, spaceID := uuid.New(), uuid.New(), uuid.New()
	ctx := recallFeedbackActorContext(teamID, profileID, spaceID, 2)
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	repo := &recallFeedbackEventRepoStub{
		event: &domain.RecallFeedbackEvent{
			RecallID:        "rec_1",
			TeamID:          &teamID,
			ProfileID:       &profileID,
			SpaceID:         &spaceID,
			SpaceGeneration: 2,
			SnapshotState:   domain.RecallFeedbackSnapshotCaptured,
			ResultRefs: []domain.RecallFeedbackResultRef{{
				Type: domain.RecallFeedbackResultTypeFragment,
				ID:   "fragment-1",
				Rank: 1,
			}},
		},
	}
	retention := recallFeedbackRetentionStub{cfg: domain.RecallFeedbackRuntimeConfig{RetentionDays: 7}}
	svc := NewRecallFeedbackEventService(repo, retention, nil)
	svc.now = func() time.Time { return now }

	err := svc.RecordRecallFeedback(ctx, domain.RecallFeedbackSubmission{
		RecallID:        "rec_1",
		Used:            true,
		AnswerSupported: false,
		Quality:         "low",
		MissingContext:  true,
		Irrelevant:      false,
		FeedbackComment: "knowledge explorer listbox pattern was missing",
		IrrelevantRefs: []domain.RecallFeedbackJudgedResultRef{{
			Type: domain.RecallFeedbackResultTypeFragment,
			ID:   "fragment-1",
			Rank: 1,
		}},
	})
	require.NoError(t, err)
	require.Len(t, repo.feedbacks, 1)
	got := repo.feedbacks[0]
	assert.Equal(t, domain.RecallFeedbackSnapshotCaptured, got.SnapshotState)
	assert.Equal(t, []domain.RecallFeedbackResultRef{{
		Type: domain.RecallFeedbackResultTypeFragment,
		ID:   "fragment-1",
		Rank: 1,
	}}, got.ResultRefs)
	assert.NotNil(t, got.Used)
	assert.True(t, *got.Used)
	assert.NotNil(t, got.AnswerSupported)
	assert.False(t, *got.AnswerSupported)
	assert.Equal(t, "low", got.Quality)
	assert.NotNil(t, got.MissingContext)
	assert.True(t, *got.MissingContext)
	assert.Equal(t, "knowledge explorer listbox pattern was missing", got.FeedbackComment)
	assert.Equal(t, []domain.RecallFeedbackJudgedResultRef{{
		Type: domain.RecallFeedbackResultTypeFragment,
		ID:   "fragment-1",
		Rank: 1,
	}}, got.IrrelevantRefs)

	require.NoError(t, svc.Prune(ctx))
	require.Len(t, repo.pruneCutoffs, 1)
	assert.Equal(t, now.AddDate(0, 0, -7), repo.pruneCutoffs[0])

	err = NewRecallFeedbackEventService(repo, recallFeedbackRetentionStub{err: errors.New("config failed")}, nil).Prune(ctx)
	require.ErrorContains(t, err, "failed to read recall feedback retention config")

	err = NewRecallFeedbackEventService(repo, recallFeedbackRetentionStub{cfg: domain.RecallFeedbackRuntimeConfig{RetentionDays: 0}}, nil).Prune(ctx)
	require.ErrorContains(t, err, "invalid recall feedback retention days")
}

func TestRecallFeedbackEventServiceRejectsUnsnapshottedOrUnreturnedFeedbackRefs(t *testing.T) {
	teamID, profileID, spaceID := uuid.New(), uuid.New(), uuid.New()
	ctx := recallFeedbackActorContext(teamID, profileID, spaceID, 1)
	repo := &recallFeedbackEventRepoStub{}
	svc := NewRecallFeedbackEventService(repo, nil, nil)

	err := svc.RecordRecallFeedback(ctx, domain.RecallFeedbackSubmission{
		RecallID:        "rec_missing",
		Used:            true,
		AnswerSupported: false,
		Quality:         "low",
		MissingContext:  true,
		FeedbackComment: "missing snapshot must fail closed",
	})
	require.ErrorIs(t, err, repository.ErrRecallFeedbackEventNotFound)

	repo.event = &domain.RecallFeedbackEvent{
		RecallID:        "rec_1",
		TeamID:          &teamID,
		SpaceID:         &spaceID,
		SpaceGeneration: 1,
		SnapshotState:   domain.RecallFeedbackSnapshotCaptured,
		ResultRefs: []domain.RecallFeedbackResultRef{{
			Type: domain.RecallFeedbackResultTypeEvidence,
			ID:   "evidence-1",
			Rank: 1,
		}},
	}
	err = svc.RecordRecallFeedback(ctx, domain.RecallFeedbackSubmission{
		RecallID:        "rec_1",
		Used:            true,
		AnswerSupported: false,
		Quality:         "low",
		Irrelevant:      true,
		FeedbackComment: "fabricated refs must fail closed",
		IrrelevantRefs: []domain.RecallFeedbackJudgedResultRef{{
			Type: domain.RecallFeedbackResultTypeEvidence,
			ID:   "other-evidence",
			Rank: 1,
		}},
	})
	require.ErrorContains(t, err, "was not returned")
	require.ErrorIs(t, err, ErrRecallFeedbackInvalidResultRef)
	require.Empty(t, repo.feedbacks)

	repo.event = &domain.RecallFeedbackEvent{
		RecallID:        "rec_stale",
		TeamID:          &teamID,
		SpaceID:         &spaceID,
		SpaceGeneration: 1,
		SnapshotState:   domain.RecallFeedbackSnapshotFeedbackOnly,
		ResultRefs: []domain.RecallFeedbackResultRef{{
			Type: domain.RecallFeedbackResultTypeEvidence,
			ID:   "evidence-1",
			Rank: 1,
		}},
	}
	err = svc.RecordRecallFeedback(ctx, domain.RecallFeedbackSubmission{
		RecallID:        "rec_stale",
		Used:            true,
		AnswerSupported: false,
		Quality:         "low",
		FeedbackComment: "feedback-only rows must not accept judgments",
	})
	require.ErrorContains(t, err, "snapshot is required")

	otherTeamID := uuid.New()
	repo.event = &domain.RecallFeedbackEvent{
		RecallID:      "rec_cross_team",
		TeamID:        &otherTeamID,
		SnapshotState: domain.RecallFeedbackSnapshotCaptured,
		ResultRefs: []domain.RecallFeedbackResultRef{{
			Type: domain.RecallFeedbackResultTypeEvidence,
			ID:   "evidence-1",
			Rank: 1,
		}},
	}
	teamCtx := recallFeedbackActorContext(teamID, profileID, spaceID, 1)
	err = svc.RecordRecallFeedback(teamCtx, domain.RecallFeedbackSubmission{
		RecallID:        "rec_cross_team",
		Used:            true,
		AnswerSupported: false,
		Quality:         "low",
		FeedbackComment: "cross-team feedback must fail closed",
	})
	require.ErrorIs(t, err, repository.ErrRecallFeedbackEventNotFound)
	require.Empty(t, repo.feedbacks)
}

func TestRecallFeedbackEventServiceRejectsSameTeamPeerAndReopenedGeneration(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	peerID := uuid.New()
	privateSpaceID := uuid.New()
	peerSpaceID := uuid.New()
	event := &domain.RecallFeedbackEvent{
		RecallID:        "rec_private",
		TeamID:          &teamID,
		ProfileID:       &ownerID,
		SpaceID:         &privateSpaceID,
		SpaceGeneration: 4,
		SnapshotState:   domain.RecallFeedbackSnapshotCaptured,
		ResultRefs:      []domain.RecallFeedbackResultRef{},
	}
	repo := &recallFeedbackEventRepoStub{event: event}
	svc := NewRecallFeedbackEventService(repo, nil, nil)

	peerCtx := recallFeedbackActorContext(teamID, peerID, peerSpaceID, 4)
	err := svc.RecordRecallFeedback(peerCtx, domain.RecallFeedbackSubmission{RecallID: event.RecallID, Quality: "low"})
	require.ErrorIs(t, err, repository.ErrRecallFeedbackEventNotFound)

	reopenedCtx := recallFeedbackActorContext(teamID, ownerID, privateSpaceID, 5)
	err = svc.RecordRecallFeedback(reopenedCtx, domain.RecallFeedbackSubmission{RecallID: event.RecallID, Quality: "low"})
	require.ErrorIs(t, err, repository.ErrRecallFeedbackEventNotFound)
	require.Empty(t, repo.feedbacks)
}

func TestRecallFeedbackEventServiceGetResolvesResults(t *testing.T) {
	teamID := uuid.New()
	profileID := uuid.New()
	repo := &recallFeedbackEventRepoStub{
		event: &domain.RecallFeedbackEvent{
			RecallID:  "rec_1",
			TeamID:    &teamID,
			ProfileID: &profileID,
			ResultRefs: []domain.RecallFeedbackResultRef{{
				Type: domain.RecallFeedbackResultTypeFact,
				ID:   "fact-1",
				Rank: 1,
			}},
		},
	}
	resolver := &recallFeedbackResolverStub{
		resolved: []domain.RecallFeedbackResolvedResult{{
			Type:             domain.RecallFeedbackResultTypeFact,
			ID:               "fact-1",
			Rank:             1,
			ResolutionStatus: "found",
		}},
	}
	svc := NewRecallFeedbackEventService(repo, nil, resolver)

	got, err := svc.GetRecallFeedbackEvent(context.Background(), "rec_1")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, teamID.String(), resolver.profileID)
	assert.Len(t, got.ResolvedResults, 1)
}

func TestRecallFeedbackEventServiceGetFallsBackToProfileScopeForLegacyRows(t *testing.T) {
	profileID := uuid.New()
	repo := &recallFeedbackEventRepoStub{
		event: &domain.RecallFeedbackEvent{
			RecallID:  "rec_legacy",
			ProfileID: &profileID,
			ResultRefs: []domain.RecallFeedbackResultRef{{
				Type: domain.RecallFeedbackResultTypeFragment,
				ID:   "fragment-1",
				Rank: 1,
			}},
		},
	}
	resolver := &recallFeedbackResolverStub{}
	svc := NewRecallFeedbackEventService(repo, nil, resolver)

	got, err := svc.GetRecallFeedbackEvent(context.Background(), "rec_legacy")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, profileID.String(), resolver.profileID)
}

func TestRecallFeedbackEventServiceStartShutdownLifecycle(t *testing.T) {
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	repo := &recallFeedbackEventRepoStub{pruneNotify: make(chan time.Time, 1)}
	retention := recallFeedbackRetentionStub{cfg: domain.RecallFeedbackRuntimeConfig{RetentionDays: 1}}
	svc := NewRecallFeedbackEventService(repo, retention, nil)
	svc.now = func() time.Time { return now }

	svc.Start(context.Background())
	svc.Start(context.Background())
	select {
	case cutoff := <-repo.pruneNotify:
		assert.Equal(t, now.AddDate(0, 0, -1), cutoff)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for startup prune")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	require.NoError(t, svc.Shutdown(shutdownCtx))
	require.NoError(t, svc.Shutdown(shutdownCtx))

	var nilSvc *RecallFeedbackEventServiceImpl
	nilSvc.Start(context.Background())
	require.NoError(t, nilSvc.Shutdown(context.Background()))
}

func TestRecallFeedbackEventServiceUnavailableAndErrors(t *testing.T) {
	ctx := context.Background()
	var nilSvc *RecallFeedbackEventServiceImpl
	_, err := nilSvc.ListRecallFeedbackEvents(ctx, domain.RecallFeedbackEventFilter{})
	require.ErrorContains(t, err, "unavailable")
	_, err = nilSvc.GetRecallFeedbackEvent(ctx, "rec_1")
	require.ErrorContains(t, err, "unavailable")
	require.NoError(t, nilSvc.RecordRecallSnapshot(ctx, domain.RecallFeedbackEvent{}))
	require.NoError(t, nilSvc.RecordRecallFeedback(ctx, domain.RecallFeedbackSubmission{}))
	require.NoError(t, nilSvc.Prune(ctx))

	repo := &recallFeedbackEventRepoStub{recordErr: errors.New("record failed")}
	svc := NewRecallFeedbackEventService(repo, nil, nil)
	teamID, spaceID := uuid.New(), uuid.New()
	err = svc.RecordRecallSnapshot(ctx, domain.RecallFeedbackEvent{RecallID: "rec_1", TeamID: &teamID, SpaceID: &spaceID, SpaceGeneration: 1})
	require.ErrorContains(t, err, "record failed")

	err = svc.RecordRecallSnapshot(ctx, domain.RecallFeedbackEvent{})
	require.ErrorContains(t, err, "recall_id is required")
	err = svc.RecordRecallFeedback(ctx, domain.RecallFeedbackSubmission{})
	require.ErrorContains(t, err, "recall_id is required")
}

func recallFeedbackActorContext(teamID, ownerID, spaceID uuid.UUID, generation int64) context.Context {
	return requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID:  teamID,
		OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{{
			ID: spaceID, Kind: domain.MemorySpaceCredentialPrivate, Generation: generation,
		}},
	})
}

type recallFeedbackEventRepoStub struct {
	mu           sync.Mutex
	snapshots    []domain.RecallFeedbackEvent
	feedbacks    []domain.RecallFeedbackEvent
	pruneCutoffs []time.Time
	pruneNotify  chan time.Time
	event        *domain.RecallFeedbackEvent
	recordErr    error
}

func (s *recallFeedbackEventRepoStub) RecordSnapshot(_ context.Context, event domain.RecallFeedbackEvent) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	s.snapshots = append(s.snapshots, event)
	return nil
}

func (s *recallFeedbackEventRepoStub) RecordFeedback(_ context.Context, event domain.RecallFeedbackEvent) error {
	if s.recordErr != nil {
		return s.recordErr
	}
	s.feedbacks = append(s.feedbacks, event)
	return nil
}

func (s *recallFeedbackEventRepoStub) List(_ context.Context, _ domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error) {
	return &domain.RecallFeedbackEventPage{}, nil
}

func (s *recallFeedbackEventRepoStub) Get(_ context.Context, _ string) (*domain.RecallFeedbackEvent, error) {
	return s.event, nil
}

func (s *recallFeedbackEventRepoStub) PruneBefore(_ context.Context, cutoff time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneCutoffs = append(s.pruneCutoffs, cutoff)
	if s.pruneNotify != nil {
		select {
		case s.pruneNotify <- cutoff:
		default:
		}
	}
	return nil
}

type recallFeedbackRetentionStub struct {
	cfg domain.RecallFeedbackRuntimeConfig
	err error
}

func (s recallFeedbackRetentionStub) RecallFeedbackRuntimeConfig(context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	return s.cfg, s.err
}

type recallFeedbackResolverStub struct {
	profileID string
	resolved  []domain.RecallFeedbackResolvedResult
	err       error
}

func (s *recallFeedbackResolverStub) ResolveRecallFeedbackResults(_ context.Context, profileID string, _ []domain.RecallFeedbackResultRef) ([]domain.RecallFeedbackResolvedResult, error) {
	s.profileID = profileID
	return s.resolved, s.err
}
