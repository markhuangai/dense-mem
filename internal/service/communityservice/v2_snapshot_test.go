package communityservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestV2SnapshotServicePublishesDeterministicPostgresSnapshot(t *testing.T) {
	teamID := uuid.NewString()
	runID := uuid.NewString()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	inputs := []repository.V2CommunityInput{
		v2CommunityInputForTest("00000000-0000-0000-0000-000000000003", "00000000-0000-0000-0000-000000000103", "00000000-0000-0000-0000-000000000203", "Fact C", "works_on", "Fact D", "fact"),
		v2CommunityInputForTest("00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000101", "00000000-0000-0000-0000-000000000201", "Mark Huang", "works_on", "Dense-Mem", "fact"),
		v2CommunityInputForTest("00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000201", "00000000-0000-0000-0000-000000000202", "Dense-Mem", "uses", "PostgreSQL", "validated_claim"),
	}
	repo := &v2SnapshotRepoStub{
		run: &repository.V2CommunityRun{
			TeamID:    teamID,
			RunID:     runID,
			WindowKey: "window-1",
			Status:    "running",
			Claimed:   true,
		},
		inputs: inputs,
	}
	svc := NewV2SnapshotService(repo, V2SnapshotOptions{
		MaxNodes: 10,
		MaxEdges: 10,
		Now:      func() time.Time { return now },
	})

	result, err := svc.Detect(context.Background(), V2SnapshotRequest{
		TeamID:            teamID,
		WindowKey:         "window-1",
		ConfigurationHash: "cfg",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 5, result.NodeCount)
	require.Equal(t, 3, result.EdgeCount)
	require.Equal(t, 2, result.CommunityCount)
	require.Equal(t, 1, repo.refreshCalls)
	require.NotNil(t, repo.published)
	require.Equal(t, repository.V2CommunityAlgorithmKind, repo.published.AlgorithmKind)
	require.Len(t, repo.published.Communities, 2)
	require.Equal(t, 3, repo.published.Communities[0].MemberCount)
	require.Contains(t, repo.published.Communities[0].TopEntities, "Dense-Mem")

	reversed := []repository.V2CommunityInput{inputs[2], inputs[1], inputs[0]}
	first := buildV2CommunitySnapshot(teamID, runID, inputs, now)
	second := buildV2CommunitySnapshot(teamID, runID, reversed, now)
	require.Equal(t, first.SourceFingerprint, second.SourceFingerprint)
	require.Equal(t, first.Communities[0].CommunityID, second.Communities[0].CommunityID)
	require.Equal(t, first.Communities[0].Sources, second.Communities[0].Sources)
}

func TestV2SnapshotServiceCompletesTooLargeRun(t *testing.T) {
	teamID := uuid.NewString()
	runID := uuid.NewString()
	repo := &v2SnapshotRepoStub{
		run: &repository.V2CommunityRun{
			TeamID:    teamID,
			RunID:     runID,
			WindowKey: "window-large",
			Status:    "running",
			Claimed:   true,
		},
		inputs: []repository.V2CommunityInput{
			v2CommunityInputForTest("00000000-0000-0000-0000-000000000011", "00000000-0000-0000-0000-000000000111", "00000000-0000-0000-0000-000000000211", "A", "works_on", "B", "fact"),
			v2CommunityInputForTest("00000000-0000-0000-0000-000000000012", "00000000-0000-0000-0000-000000000211", "00000000-0000-0000-0000-000000000212", "B", "uses", "C", "fact"),
		},
	}
	svc := NewV2SnapshotService(repo, V2SnapshotOptions{
		MaxNodes: 2,
		MaxEdges: 10,
		Now:      func() time.Time { return time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC) },
	})

	result, err := svc.Detect(context.Background(), V2SnapshotRequest{
		TeamID:    teamID,
		WindowKey: "window-large",
	})
	require.ErrorIs(t, err, ErrCommunityGraphTooLarge)
	require.Equal(t, "too_large", result.Status)
	require.Equal(t, "too_large", repo.completed.Status)
	require.Equal(t, 3, repo.completed.NodeCount)
	require.Nil(t, repo.published)
}

func TestV2SnapshotServiceHandlesUnclaimedSkippedAndUnavailable(t *testing.T) {
	teamID := uuid.NewString()
	unclaimedRepo := &v2SnapshotRepoStub{
		run: &repository.V2CommunityRun{
			TeamID:    teamID,
			RunID:     uuid.NewString(),
			WindowKey: "window-existing",
			Status:    "completed",
			Claimed:   false,
		},
	}
	svc := NewV2SnapshotService(unclaimedRepo, V2SnapshotOptions{})
	result, err := svc.Detect(context.Background(), V2SnapshotRequest{
		TeamID:    teamID,
		WindowKey: "window-existing",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 0, unclaimedRepo.refreshCalls)

	skippedRepo := &v2SnapshotRepoStub{
		run: &repository.V2CommunityRun{
			TeamID:    teamID,
			RunID:     uuid.NewString(),
			WindowKey: "window-empty",
			Status:    "running",
			Claimed:   true,
		},
	}
	svc = NewV2SnapshotService(skippedRepo, V2SnapshotOptions{})
	result, err = svc.Detect(context.Background(), V2SnapshotRequest{
		TeamID:    teamID,
		WindowKey: "window-empty",
	})
	require.NoError(t, err)
	require.Equal(t, "skipped", result.Status)
	require.Equal(t, "skipped", skippedRepo.completed.Status)

	_, err = NewV2SnapshotService(nil, V2SnapshotOptions{}).Detect(context.Background(), V2SnapshotRequest{
		TeamID: teamID,
	})
	require.ErrorIs(t, err, ErrCommunityUnavailable)
}

func TestV2SnapshotServiceMarksRunFailedWhenPublishFails(t *testing.T) {
	teamID := uuid.NewString()
	publishErr := errors.New("source version changed")
	repo := &v2SnapshotRepoStub{
		run: &repository.V2CommunityRun{
			TeamID:    teamID,
			RunID:     uuid.NewString(),
			WindowKey: "window-publish-fail",
			Status:    "running",
			Claimed:   true,
		},
		inputs: []repository.V2CommunityInput{
			v2CommunityInputForTest("00000000-0000-0000-0000-000000000021", "00000000-0000-0000-0000-000000000121", "00000000-0000-0000-0000-000000000221", "A", "works_on", "B", "fact"),
		},
		publishErr: publishErr,
	}
	svc := NewV2SnapshotService(repo, V2SnapshotOptions{
		MaxNodes: 10,
		MaxEdges: 10,
	})

	result, err := svc.Detect(context.Background(), V2SnapshotRequest{
		TeamID:    teamID,
		WindowKey: "window-publish-fail",
	})
	require.ErrorIs(t, err, publishErr)
	require.Equal(t, "failed", result.Status)
	require.Equal(t, "failed", repo.completed.Status)
	require.Contains(t, repo.completed.Error, "source version changed")
}

func v2CommunityInputForTest(relationshipID, subjectID, objectID, subjectName, predicate, objectName, tier string) repository.V2CommunityInput {
	return repository.V2CommunityInput{
		RelationshipID:   relationshipID,
		OwnerProfileID:   "00000000-0000-0000-0000-00000000aaaa",
		Version:          1,
		Tier:             tier,
		SubjectEntityID:  subjectID,
		SubjectName:      subjectName,
		PredicateKey:     predicate,
		PredicateVersion: 1,
		ObjectEntityID:   objectID,
		ObjectName:       objectName,
	}
}

type v2SnapshotRepoStub struct {
	run          *repository.V2CommunityRun
	inputs       []repository.V2CommunityInput
	refreshCalls int
	published    *repository.V2CommunitySnapshotPublishInput
	completed    repository.V2CommunityRunCompleteInput
	err          error
	publishErr   error
}

func (s *v2SnapshotRepoStub) ClaimV2CommunityRun(context.Context, repository.V2CommunityRunClaimInput) (*repository.V2CommunityRun, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.run, nil
}

func (s *v2SnapshotRepoStub) CompleteV2CommunityRun(_ context.Context, input repository.V2CommunityRunCompleteInput) error {
	s.completed = input
	return nil
}

func (s *v2SnapshotRepoStub) ListV2CommunityInputs(context.Context, repository.V2CommunityInputListInput) ([]repository.V2CommunityInput, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.inputs, nil
}

func (s *v2SnapshotRepoStub) PublishV2CommunitySnapshot(_ context.Context, input repository.V2CommunitySnapshotPublishInput) error {
	s.published = &input
	if s.publishErr != nil {
		return s.publishErr
	}
	return nil
}

func (s *v2SnapshotRepoStub) RefreshV2CommunityStaleness(context.Context, repository.V2CommunityStalenessInput) (int, error) {
	s.refreshCalls++
	return 0, nil
}

func (s *v2SnapshotRepoStub) ListV2Communities(context.Context, repository.V2CommunityListInput) ([]repository.V2CommunityRecord, error) {
	return nil, errors.New("unused")
}

func (s *v2SnapshotRepoStub) GetV2Community(context.Context, repository.V2CommunityGetInput) (*repository.V2CommunityRecord, error) {
	return nil, errors.New("unused")
}

func (s *v2SnapshotRepoStub) RecallV2CommunityDiscovery(context.Context, repository.V2CommunityDiscoveryInput) ([]repository.V2CommunityDiscoveryPath, error) {
	return nil, errors.New("unused")
}

func (s *v2SnapshotRepoStub) LatestV2CommunityRun(context.Context, string) (*repository.V2CommunityRun, error) {
	return nil, errors.New("unused")
}
