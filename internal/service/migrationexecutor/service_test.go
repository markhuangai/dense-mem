package migrationexecutor

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

func TestRunOnceSubmitsLegacyItemsUnderOriginalOwnerAndRecordsProgress(t *testing.T) {
	runID := uuid.NewString()
	teamID := uuid.New()
	ownerID := uuid.New()
	credentialID := uuid.New()
	now := time.Date(2026, 7, 17, 15, 0, 0, 0, time.UTC)
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: runID,
			State: domain.V2MigrationStateRunning,
		},
		checkpoint: map[string]any{"after_source_id": "sf-001"},
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			NextCursor: "sf-003",
			Items: []neo4j.LegacyCorpusItem{
				{
					SourceKind:       neo4j.LegacyCorpusSourceKind,
					SourceID:         "sf-002",
					SourceHash:       "sha256:source",
					TeamID:           teamID.String(),
					OwnerProfileID:   ownerID.String(),
					OwnerProfileName: "source-owner",
					Content:          "  exact legacy evidence\n",
					Authority:        "user",
					Labels:           []string{"legacy"},
					Claims: []neo4j.LegacyClaimHint{{
						ClaimID:   "claim-1",
						Subject:   "Ada",
						Predicate: "likes",
						Object:    "Postgres",
					}},
				},
				{
					SourceID: "sf-003",
					TeamID:   teamID.String(),
					Content:  "missing owner",
				},
			},
		},
	}
	remember := &rememberStub{
		result: &memoryservice.V2RememberResult{
			IngestID: "11111111-1111-1111-1111-111111111111",
			Status:   string(domain.V2PlacementRunQueued),
			Items: []memoryservice.V2RememberItemResult{{
				ItemID:   "22222222-2222-2222-2222-222222222222",
				Category: string(domain.V2EvidenceNeedsReview),
			}},
		},
	}
	svc := New(store, reader, remember, Config{
		PageSize:              1000,
		WorkerID:              "worker-a",
		MigrationCredentialID: credentialID,
		Now:                   func() time.Time { return now },
	})
	callerCtx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		TeamID:    uuid.New(),
		ProfileID: uuid.New(),
	})

	result, err := svc.RunOnce(callerCtx)
	require.NoError(t, err)
	require.Equal(t, &RunOnceResult{
		RunID:      runID,
		Fetched:    2,
		Submitted:  1,
		Excluded:   1,
		NextCursor: "sf-003",
	}, result)
	require.Equal(t, "sf-001", reader.req.AfterSourceID)
	require.Equal(t, 500, reader.req.Limit)

	require.Len(t, remember.requests, 1)
	req := remember.requests[0]
	require.Equal(t, domain.V2ContractVersion, req.ContractVersion)
	require.Equal(t, "v2-migration:"+runID+":neo4j:sf-002", req.IdempotencyKey)
	require.Len(t, req.Evidence, 1)
	assert.Equal(t, "  exact legacy evidence\n", req.Evidence[0].Content)
	assert.Equal(t, "document", req.Evidence[0].SourceType)
	assert.Equal(t, "primary", req.Evidence[0].Authority)
	assert.Equal(t, "neo4j://source-fragment/sf-002", req.Evidence[0].Source)
	assert.Equal(t, "neo4j://source-fragment/sf-002", req.Evidence[0].SourceKey)
	assert.Equal(t, []string{"legacy", "legacy_neo4j_migration"}, req.Evidence[0].Labels)
	assert.Equal(t, "user", req.Evidence[0].Metadata["legacy_authority"])
	require.Len(t, req.EntityHints, 2)
	require.Len(t, req.RelationshipHints, 1)

	require.Len(t, remember.actors, 1)
	assert.Equal(t, teamID, remember.actors[0].TeamID)
	assert.Equal(t, ownerID, remember.actors[0].ProfileID)
	assert.Equal(t, "source-owner", remember.actors[0].ProfileName)
	require.Len(t, remember.credentials, 1)
	assert.Equal(t, credentialID, remember.credentials[0].KeyID)
	assert.Equal(t, "migration", remember.credentials[0].AuthMethod)

	require.Len(t, store.upserts, 1)
	assert.Equal(t, ownerID.String(), store.upserts[0].OwnerProfileID)
	require.Len(t, store.updates, 1)
	assert.Equal(t, domain.V2MigrationOutcomeNeedsReview, store.updates[0].Outcome)
	assert.Equal(t, "11111111-1111-1111-1111-111111111111", store.updates[0].IngestID)
	assert.Equal(t, "22222222-2222-2222-2222-222222222222", store.updates[0].PlacementItemID)
	require.Len(t, store.sourceMaps, 2)
	require.Len(t, store.checkpoints, 1)
	assert.Equal(t, domain.V2MigrationCheckpointLegacyNeo4jCursor, store.checkpoints[0].CheckpointKey)
	assert.Equal(t, "sf-003", store.checkpoints[0].CheckpointValue["after_source_id"])
	assert.Equal(t, "worker-a", store.checkpoints[0].LeaseOwner)
	require.Len(t, store.exclusions, 1)
	assert.Contains(t, store.exclusions[0].Reason, "owner_profile_id")
	require.Len(t, store.errors, 1)
	assert.Equal(t, "invalid_legacy_item", store.errors[0].ErrorCode)
	assert.Equal(t, 1, store.refreshCalls)
}

func TestRunOnceSkipsAlreadyCompletedItemsOnResume(t *testing.T) {
	runID := uuid.NewString()
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: runID,
			State: domain.V2MigrationStateRunning,
		},
		upsertOutcomes: map[string]string{"sf-010": domain.V2MigrationOutcomeNeedsReview},
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			NextCursor: "sf-010",
			Items: []neo4j.LegacyCorpusItem{{
				SourceID:       "sf-010",
				TeamID:         teamID,
				OwnerProfileID: ownerID,
				Content:        "already submitted",
			}},
		},
	}
	remember := &rememberStub{err: errors.New("remember should not be called")}
	svc := New(store, reader, remember, Config{MigrationCredentialID: uuid.New()})

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Skipped)
	assert.Equal(t, 0, result.Submitted)
	assert.Empty(t, remember.requests)
	assert.Empty(t, store.updates)
}

func TestRunOnceRecordsFailedOutcomeWhenRememberFails(t *testing.T) {
	runID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: runID,
			State: domain.V2MigrationStateRunning,
		},
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			NextCursor: "sf-020",
			Items: []neo4j.LegacyCorpusItem{{
				SourceID:       "sf-020",
				TeamID:         uuid.NewString(),
				OwnerProfileID: uuid.NewString(),
				Content:        "remember failure",
			}},
		},
	}
	remember := &rememberStub{err: errors.New("provider down")}
	svc := New(store, reader, remember, Config{MigrationCredentialID: uuid.New()})

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	require.Len(t, store.updates, 1)
	assert.Equal(t, domain.V2MigrationOutcomeFailed, store.updates[0].Outcome)
	require.Len(t, store.errors, 1)
	assert.Equal(t, "remember_failed", store.errors[0].ErrorCode)
}

func TestRunOnceRequiresRunningMigration(t *testing.T) {
	svc := New(&executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: uuid.NewString(),
			State: domain.V2MigrationStateReady,
		},
	}, &legacyReaderStub{}, &rememberStub{}, Config{MigrationCredentialID: uuid.New()})

	_, err := svc.RunOnce(context.Background())
	require.ErrorIs(t, err, ErrMigrationNotRunning)
}

type executorStoreStub struct {
	run            *domain.V2MigrationRun
	checkpoint     map[string]any
	upsertOutcomes map[string]string
	upserts        []repository.V2UpsertMigrationCorpusItemInput
	updates        []repository.V2UpdateMigrationCorpusOutcomeInput
	sourceMaps     []repository.V2UpsertMigrationSourceMapInput
	checkpoints    []repository.V2UpsertMigrationCheckpointInput
	errors         []repository.V2RecordMigrationErrorInput
	exclusions     []repository.V2RecordMigrationExclusionInput
	refreshCalls   int
}

func (s *executorStoreStub) GetLatestRun(context.Context) (*domain.V2MigrationRun, error) {
	return s.run, nil
}

func (s *executorStoreStub) UpsertMigrationCorpusItem(_ context.Context, input repository.V2UpsertMigrationCorpusItemInput) (*domain.V2MigrationCorpusItem, error) {
	s.upserts = append(s.upserts, input)
	outcome := domain.V2MigrationOutcomePending
	if s.upsertOutcomes != nil && s.upsertOutcomes[input.SourceID] != "" {
		outcome = s.upsertOutcomes[input.SourceID]
	}
	return &domain.V2MigrationCorpusItem{
		RunID:          input.RunID,
		TeamID:         input.TeamID,
		OwnerProfileID: input.OwnerProfileID,
		SourceKind:     input.SourceKind,
		SourceID:       input.SourceID,
		Outcome:        outcome,
	}, nil
}

func (s *executorStoreStub) UpdateMigrationCorpusOutcome(_ context.Context, input repository.V2UpdateMigrationCorpusOutcomeInput) (*domain.V2MigrationCorpusItem, error) {
	s.updates = append(s.updates, input)
	return &domain.V2MigrationCorpusItem{
		RunID:           input.RunID,
		SourceKind:      input.SourceKind,
		SourceID:        input.SourceID,
		Outcome:         input.Outcome,
		IngestID:        input.IngestID,
		PlacementItemID: input.PlacementItemID,
	}, nil
}

func (s *executorStoreStub) UpsertMigrationSourceMap(_ context.Context, input repository.V2UpsertMigrationSourceMapInput) error {
	s.sourceMaps = append(s.sourceMaps, input)
	return nil
}

func (s *executorStoreStub) UpsertMigrationCheckpoint(_ context.Context, input repository.V2UpsertMigrationCheckpointInput) error {
	s.checkpoints = append(s.checkpoints, input)
	return nil
}

func (s *executorStoreStub) GetMigrationCheckpoint(context.Context, string, string) (map[string]any, error) {
	return s.checkpoint, nil
}

func (s *executorStoreStub) RecordMigrationError(_ context.Context, input repository.V2RecordMigrationErrorInput) error {
	s.errors = append(s.errors, input)
	return nil
}

func (s *executorStoreStub) RecordMigrationExclusion(_ context.Context, input repository.V2RecordMigrationExclusionInput) error {
	s.exclusions = append(s.exclusions, input)
	return nil
}

func (s *executorStoreStub) RefreshMigrationRunStats(context.Context, string, time.Time) (*domain.V2MigrationRun, error) {
	s.refreshCalls++
	return s.run, nil
}

type legacyReaderStub struct {
	req  neo4j.LegacyCorpusPageRequest
	page neo4j.LegacyCorpusPage
	err  error
}

func (r *legacyReaderStub) ReadCorpusPage(_ context.Context, req neo4j.LegacyCorpusPageRequest) (neo4j.LegacyCorpusPage, error) {
	r.req = req
	return r.page, r.err
}

type rememberStub struct {
	requests    []memoryservice.V2RememberRequest
	actors      []requestctx.ActorProfile
	credentials []requestctx.ActorCredential
	result      *memoryservice.V2RememberResult
	err         error
}

func (s *rememberStub) RememberV2(ctx context.Context, req memoryservice.V2RememberRequest) (*memoryservice.V2RememberResult, error) {
	s.requests = append(s.requests, req)
	actor, _ := requestctx.ActorProfileFromContext(ctx)
	s.actors = append(s.actors, actor)
	credential, _ := requestctx.ActorCredentialFromContext(ctx)
	s.credentials = append(s.credentials, credential)
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &memoryservice.V2RememberResult{
		IngestID: uuid.NewString(),
		Status:   string(domain.V2PlacementRunQueued),
		Items: []memoryservice.V2RememberItemResult{{
			ItemID:   uuid.NewString(),
			Category: string(domain.V2EvidenceNeedsReview),
		}},
	}, nil
}
