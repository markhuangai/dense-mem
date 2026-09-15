package recall

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestRecallFeedbackAdditionalValidationBranches(t *testing.T) {
	recordRecallFeedbackMetric(context.Background(), nil, recallcontract.FeedbackObservation{})
	nilRecorder := SubmitRecallFeedbackBatch(context.Background(), nil, nil, []domain.RecallFeedbackSubmission{{RecallID: "unavailable"}})
	require.NotNil(t, nilRecorder.Failure)
	deadline, cancel := context.WithCancel(context.Background())
	cancel()
	result := SubmitRecallFeedbackBatch(deadline, &recallFeedbackBatchRecorder{failAt: 0, err: context.Canceled}, nil, []domain.RecallFeedbackSubmission{{RecallID: "cancelled"}})
	require.NotNil(t, result.Failure)
	require.Equal(t, "request_cancelled", result.Failure.ReasonCode)
	guidance := recallFeedbackFailureGuidance(context.DeadlineExceeded)
	require.Equal(t, "request_timeout", guidance.reasonCode)

	refs := FeedbackResultRefs(&RecallResult{
		Results:              []RecallResultItem{{EvidenceID: "", RelationshipIDs: []string{"relationship"}, Rank: 0}},
		RelatedRelationships: []RelatedRelationshipSummary{{RelationshipID: "related", SearchState: ""}},
		RelatedCommunities:   []RecallDiscoveryPath{{CommunityID: "", CommunityRelationships: []RelatedRelationshipSummary{{RelationshipID: ""}}}},
	})
	require.Len(t, refs, 2)
	require.Equal(t, 1, refs[0].Rank)
	require.Equal(t, "", recallFeedbackFirstNonEmpty(" ", ""))

	returned := []domain.RecallFeedbackResultRef{{Type: domain.RecallFeedbackResultTypeDream, ID: "dream-1"}}
	for _, feedback := range []domain.RecallFeedbackSubmission{
		{IrrelevantRefs: []domain.RecallFeedbackJudgedResultRef{{Type: "", ID: "id"}}},
		{IrrelevantRefs: []domain.RecallFeedbackJudgedResultRef{{Type: domain.RecallFeedbackResultTypeHypothesis, ID: "dream-1"}, {Type: domain.RecallFeedbackResultTypeHypothesis, ID: "dream-1"}}},
		{DreamFeedback: []domain.RecallFeedbackDreamFeedback{{DreamID: ""}}},
		{DreamFeedback: []domain.RecallFeedbackDreamFeedback{{DreamID: "dream-1"}, {DreamID: "dream-1"}}},
		{DreamFeedback: []domain.RecallFeedbackDreamFeedback{{DreamID: "missing"}}},
	} {
		require.Error(t, validateRecallFeedbackSubmissionRefs(feedback, returned))
	}
	lookup := recallFeedbackReturnedRefSet([]domain.RecallFeedbackResultRef{{Type: "unknown", ID: "x"}, {Type: domain.RecallFeedbackResultTypeEvidence, ID: "e", Rank: 2}})
	require.False(t, lookup.contains("unknown", "x", 0))
	require.True(t, lookup.contains(domain.RecallFeedbackResultTypeEvidence, "e", 0))
	require.False(t, lookup.contains(domain.RecallFeedbackResultTypeEvidence, "e", 1))
	require.Empty(t, recallFeedbackRefKey("unknown", "id"))
	require.Equal(t, domain.RecallFeedbackResultTypeHypothesis, recallFeedbackCanonicalResultType(domain.RecallFeedbackResultTypeDream))

	teamID, profileID, spaceID := uuid.New(), uuid.New(), uuid.New()
	actor := requestctx.Actor{TeamID: teamID, OwnerID: profileID, AllowedSpaces: []domain.MemorySpaceAccess{
		{ID: spaceID, Kind: domain.MemorySpaceProfilePrivate, Generation: 1},
		{ID: spaceID, Kind: domain.MemorySpaceProfilePrivate, Generation: 2},
	}}
	ctx := requestctx.WithActor(context.Background(), actor)
	if err := NewRecallFeedbackEventService(&recallFeedbackEventRepoStub{}, nil, nil).RecordRecallSnapshot(context.Background(), domain.RecallFeedbackEvent{RecallID: "no-space"}); err == nil {
		t.Fatal("snapshot without memory-space context was accepted")
	}
	if _, err := bindRecallFeedbackEventSpace(ctx, domain.RecallFeedbackEvent{RecallID: "ambiguous"}); err == nil {
		t.Fatal("ambiguous private space was accepted")
	}
	if _, err := bindRecallFeedbackEventSpace(ctx, domain.RecallFeedbackEvent{RecallID: "unauthorized", TeamID: ptrUUID(uuid.New())}); err == nil {
		t.Fatal("cross-team space was accepted")
	}
	if _, err := bindRecallFeedbackEventSpace(context.Background(), domain.RecallFeedbackEvent{RecallID: "missing"}); err == nil {
		t.Fatal("space-less unauthenticated event was accepted")
	}
	if _, err := bindRecallFeedbackEventSpace(requestctx.WithActor(context.Background(), requestctx.Actor{}), domain.RecallFeedbackEvent{RecallID: "no-team"}); err == nil {
		t.Fatal("actor without team was accepted")
	}
	sharedCtx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: profileID, AllowedSpaces: []domain.MemorySpaceAccess{
		{ID: uuid.New(), Kind: domain.MemorySpaceTeamShared, Generation: 1},
		{ID: uuid.New(), Kind: domain.MemorySpaceTeamShared, Generation: 1},
	}})
	if _, err := bindRecallFeedbackEventSpace(sharedCtx, domain.RecallFeedbackEvent{RecallID: "shared-ambiguous"}); err == nil {
		t.Fatal("multiple shared spaces were accepted")
	}
	if _, err := bindRecallFeedbackEventSpace(context.Background(), domain.RecallFeedbackEvent{RecallID: "legacy", TeamID: ptrUUID(teamID), SpaceID: ptrUUID(spaceID), SpaceGeneration: 1}); err != nil {
		t.Fatalf("legacy event binding: %v", err)
	}
	explicit := domain.RecallFeedbackEvent{RecallID: "explicit", TeamID: ptrUUID(teamID), SpaceID: ptrUUID(spaceID), SpaceGeneration: 1}
	if _, err := bindRecallFeedbackEventSpace(ctx, explicit); err == nil {
		t.Fatal("ambiguous explicit space was accepted")
	}
	uniqueSpace := uuid.New()
	uniqueCtx := requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: teamID, OwnerID: profileID, AllowedSpaces: []domain.MemorySpaceAccess{{ID: uniqueSpace, Kind: domain.MemorySpaceTeamShared, Generation: 4}}})
	if _, err := bindRecallFeedbackEventSpace(uniqueCtx, domain.RecallFeedbackEvent{RecallID: "unique", SpaceID: ptrUUID(uniqueSpace), SpaceGeneration: 3}); err == nil {
		t.Fatal("stale explicit space generation was accepted")
	}
	bound, err := bindRecallFeedbackEventSpace(uniqueCtx, domain.RecallFeedbackEvent{RecallID: "unique", SpaceID: ptrUUID(uniqueSpace)})
	if err != nil || bound.SpaceGeneration != 4 {
		t.Fatalf("unique space binding = %+v, %v", bound, err)
	}
	if _, err := NewRecallFeedbackEventService(&recallFeedbackEventRepoStub{}, nil, nil).ListRecallFeedbackEvents(ctx, domain.RecallFeedbackEventFilter{}); err != nil {
		t.Fatalf("feedback list: %v", err)
	}
	if _, err := NewRecallFeedbackEventService(&recallFeedbackEventRepoStub{}, nil, nil).GetRecallFeedbackEvent(ctx, "missing"); err != nil {
		t.Fatalf("missing feedback event: %v", err)
	}
	if !recallFeedbackEventAuthorizedForSubmission(ctx, &domain.RecallFeedbackEvent{TeamID: ptrUUID(teamID), SpaceID: ptrUUID(spaceID), SpaceGeneration: 1}) {
		t.Fatal("authorized feedback event was rejected")
	}
	if recallFeedbackEventAuthorizedForSubmission(ctx, &domain.RecallFeedbackEvent{TeamID: ptrUUID(uuid.New()), SpaceID: ptrUUID(spaceID), SpaceGeneration: 1}) {
		t.Fatal("cross-team feedback event was authorized")
	}
	resolverRepo := &recallFeedbackEventRepoStub{event: &domain.RecallFeedbackEvent{RecallID: "resolve", TeamID: &teamID, ResultRefs: []domain.RecallFeedbackResultRef{{Type: domain.RecallFeedbackResultTypeEvidence, ID: "e"}}}}
	if _, err := NewRecallFeedbackEventService(resolverRepo, nil, &recallFeedbackResolverStub{err: errors.New("resolve failed")}).GetRecallFeedbackEvent(ctx, "resolve"); err == nil {
		t.Fatal("resolver error was ignored")
	}
	if errors.Is(nil, ErrRecallFeedbackInvalidInput) {
		t.Fatal("nil unexpectedly matched feedback error")
	}
}

func ptrUUID(value uuid.UUID) *uuid.UUID { return &value }
