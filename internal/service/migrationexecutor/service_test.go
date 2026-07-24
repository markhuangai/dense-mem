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
			IngestID:        "11111111-1111-1111-1111-111111111111",
			ProcessingState: string(domain.V2PlacementRunQueued),
		},
	}
	svc := New(store, reader, remember, Config{
		PageSize: 1000,
		WorkerID: "worker-a",
		Now:      func() time.Time { return now },
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
	require.Len(t, remember.migrationActors, 1)
	assert.Equal(t, uuid.MustParse(runID), remember.migrationActors[0].RunID)
	require.Equal(t, []bool{false}, remember.credentialContexts)

	require.Len(t, store.upserts, 1)
	assert.Equal(t, ownerID.String(), store.upserts[0].OwnerProfileID)
	assert.Equal(t, "sha256:source", store.upserts[0].SourceHash)
	require.Len(t, store.updates, 1)
	assert.Equal(t, domain.V2MigrationOutcomePending, store.updates[0].Outcome)
	assert.Equal(t, "11111111-1111-1111-1111-111111111111", store.updates[0].IngestID)
	assert.Empty(t, store.updates[0].PlacementItemID)
	assert.Equal(t, string(domain.V2PlacementRunQueued), store.updates[0].Metadata["remember_processing_state"])
	require.Len(t, store.sourceMaps, 1)
	assert.Equal(t, domain.V2MigrationTargetIngest, store.sourceMaps[0].TargetType)
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

func TestRunOnceDerivesMissingLegacySourceHashBeforeStaging(t *testing.T) {
	runID := uuid.NewString()
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	content := "legacy evidence without stored hash"
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: runID,
			State: domain.V2MigrationStateRunning,
		},
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			NextCursor: "sf-derived-hash",
			Items: []neo4j.LegacyCorpusItem{{
				SourceID:       "sf-derived-hash",
				TeamID:         teamID,
				OwnerProfileID: ownerID,
				Content:        content,
			}},
		},
	}
	svc := New(store, reader, &rememberStub{}, Config{})

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Submitted)
	require.Len(t, store.upserts, 1)
	assert.Equal(t, neo4j.LegacyContentHash(content), store.upserts[0].SourceHash)
}

func TestRunOnceExcludesLegacyOwnerProfileOutsideTeamBeforeRemember(t *testing.T) {
	runID := uuid.NewString()
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: runID,
			State: domain.V2MigrationStateRunning,
		},
		validOwnerProfiles: map[string]bool{
			ownerProfileKey(teamID, uuid.NewString()): true,
		},
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			NextCursor: "sf-cross",
			Items: []neo4j.LegacyCorpusItem{{
				SourceID:         "sf-cross",
				TeamID:           teamID,
				OwnerProfileID:   ownerID,
				OwnerProfileName: "wrong-team-owner",
				Content:          "legacy evidence with invalid owner pair",
			}},
		},
	}
	remember := &rememberStub{}
	svc := New(store, reader, remember, Config{})

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, &RunOnceResult{
		RunID:      runID,
		Fetched:    1,
		Excluded:   1,
		NextCursor: "sf-cross",
	}, result)
	assert.Empty(t, store.upserts)
	assert.Empty(t, remember.requests)
	require.Len(t, store.exclusions, 1)
	assert.Contains(t, store.exclusions[0].Reason, "legacy owner profile does not belong to team")
	assert.True(t, store.exclusions[0].BlocksCutover)
	require.Len(t, store.errors, 1)
	assert.Equal(t, "validate_legacy_owner_profile", store.errors[0].Phase)
	assert.Equal(t, "invalid_legacy_owner_profile", store.errors[0].ErrorCode)
	assert.False(t, store.errors[0].Retryable)
}

func TestRunOnceReturnsOwnerProfileValidationFailureWithoutAdvancingCheckpoint(t *testing.T) {
	runID := uuid.NewString()
	validateErr := errors.New("postgres unavailable: postgres://operator:secret@db.internal/dense_mem")
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: runID,
			State: domain.V2MigrationStateRunning,
		},
		validateOwnerErr: validateErr,
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			NextCursor: "sf-validation",
			Items: []neo4j.LegacyCorpusItem{{
				SourceID:       "sf-validation",
				TeamID:         uuid.NewString(),
				OwnerProfileID: uuid.NewString(),
				Content:        "legacy evidence while postgres is down",
			}},
		},
	}
	svc := New(store, reader, &rememberStub{}, Config{})

	result, err := svc.RunOnce(context.Background())
	require.ErrorIs(t, err, validateErr)
	require.NotNil(t, result)
	requirePartialRunOnceError(t, err, result)
	assert.Equal(t, 1, result.Failed)
	assert.Empty(t, store.upserts)
	assert.Empty(t, store.checkpoints)
	require.Len(t, store.errors, 1)
	assert.Equal(t, "owner_profile_validation_failed", store.errors[0].ErrorCode)
	assert.Equal(t, "legacy owner profile validation failed", store.errors[0].Message)
	assert.NotContains(t, store.errors[0].Message, "secret")
	assert.True(t, store.errors[0].Retryable)
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
	svc := New(store, reader, remember, Config{})

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
	remember := &rememberStub{err: errors.New("provider down: Authorization: Bearer secret-token")}
	svc := New(store, reader, remember, Config{})

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	require.Len(t, store.updates, 1)
	assert.Equal(t, domain.V2MigrationOutcomeFailed, store.updates[0].Outcome)
	require.Len(t, store.errors, 1)
	assert.Equal(t, "remember_failed", store.errors[0].ErrorCode)
	assert.Equal(t, "remember v2 submission failed", store.errors[0].Message)
	assert.Equal(t, "remember v2 submission failed", store.updates[0].Metadata["error"])
	assert.NotContains(t, store.errors[0].Message, "secret-token")
}

func TestRunOnceRequiresRunningMigration(t *testing.T) {
	svc := New(&executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: uuid.NewString(),
			State: domain.V2MigrationStateReady,
		},
	}, &legacyReaderStub{}, &rememberStub{}, Config{})

	_, err := svc.RunOnce(context.Background())
	require.ErrorIs(t, err, ErrMigrationNotRunning)
}

func TestRunOnceRequiresDependenciesAndValidRunID(t *testing.T) {
	_, err := New(nil, nil, nil, Config{}).RunOnce(context.Background())
	require.ErrorIs(t, err, ErrMissingDependency)

	svc := New(&executorStoreStub{run: &domain.V2MigrationRun{
		RunID: "not-a-uuid",
		State: domain.V2MigrationStateRunning,
	}}, &legacyReaderStub{}, &rememberStub{}, Config{})
	_, err = svc.RunOnce(context.Background())
	require.ErrorIs(t, err, ErrInvalidRunID)
}

func TestRunOnceUsesRunCheckpointFallbackAndMarksDone(t *testing.T) {
	runID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID:           runID,
			State:           domain.V2MigrationStateRunning,
			CheckpointKey:   domain.V2MigrationCheckpointLegacyNeo4jCursor,
			CheckpointValue: map[string]any{"after_source_id": "sf-run"},
		},
	}
	reader := &legacyReaderStub{page: neo4j.LegacyCorpusPage{}}
	svc := New(store, reader, &rememberStub{}, Config{
		PageSize: 1000,
		WorkerID: "worker-b",
	})

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, &RunOnceResult{
		RunID:      runID,
		Fetched:    0,
		NextCursor: "",
		Done:       true,
		FinalRun:   store.run,
	}, result)
	assert.Equal(t, "sf-run", reader.req.AfterSourceID)
	assert.Equal(t, 500, reader.req.Limit)
	require.Len(t, store.checkpoints, 1)
	assert.Equal(t, "", store.checkpoints[0].CheckpointValue["after_source_id"])
	assert.Equal(t, true, store.checkpoints[0].CheckpointValue["done"])
	assert.Equal(t, "worker-b", store.checkpoints[0].LeaseOwner)
	assert.Equal(t, 1, store.refreshCalls)
	assert.Equal(t, 1, store.finalizeCalls)
}

func TestRunOnceDoneCheckpointFinalizesWithoutReadingLegacyAgain(t *testing.T) {
	runID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: runID,
			State: domain.V2MigrationStateRunning,
		},
		checkpoint: map[string]any{
			"after_source_id": "",
			"done":            true,
		},
	}
	reader := &legacyReaderStub{page: neo4j.LegacyCorpusPage{
		Items: []neo4j.LegacyCorpusItem{{
			SourceID:       "sf-should-not-read",
			TeamID:         uuid.NewString(),
			OwnerProfileID: uuid.NewString(),
			Content:        "would be duplicated if the done checkpoint were ignored",
		}},
	}}
	remember := &rememberStub{}
	svc := New(store, reader, remember, Config{})

	result, err := svc.RunOnce(context.Background())

	require.NoError(t, err)
	require.Equal(t, &RunOnceResult{RunID: runID, Done: true, FinalRun: store.run}, result)
	assert.Equal(t, 0, reader.calls)
	assert.Empty(t, remember.requests)
	assert.Empty(t, store.checkpoints)
	assert.Equal(t, 1, store.refreshCalls)
	assert.Equal(t, 1, store.finalizeCalls)
}

func TestRunOnceDoesNotFinalizeBeforeCursorDone(t *testing.T) {
	runID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: runID,
			State: domain.V2MigrationStateRunning,
		},
	}
	reader := &legacyReaderStub{page: neo4j.LegacyCorpusPage{
		NextCursor: "sf-next",
		Items: []neo4j.LegacyCorpusItem{{
			SourceID:       "sf-current",
			TeamID:         uuid.NewString(),
			OwnerProfileID: uuid.NewString(),
			Content:        "legacy evidence",
		}},
	}}

	result, err := New(store, reader, &rememberStub{}, Config{}).RunOnce(context.Background())

	require.NoError(t, err)
	require.False(t, result.Done)
	assert.Equal(t, 1, store.refreshCalls)
	assert.Zero(t, store.finalizeCalls)
}

func TestRunOnceRecordsReadFailure(t *testing.T) {
	runID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: runID,
			State: domain.V2MigrationStateRunning,
		},
	}
	readErr := errors.New("neo4j unavailable at bolt://user:secret@legacy")
	svc := New(store, &legacyReaderStub{err: readErr}, &rememberStub{}, Config{})

	result, err := svc.RunOnce(context.Background())
	require.ErrorIs(t, err, readErr)
	assert.Nil(t, result)
	require.Len(t, store.errors, 1)
	assert.Equal(t, "read_legacy_corpus", store.errors[0].Phase)
	assert.Equal(t, "read_failed", store.errors[0].ErrorCode)
	assert.Equal(t, "legacy corpus read failed", store.errors[0].Message)
	assert.NotContains(t, store.errors[0].Message, "secret")
	assert.True(t, store.errors[0].Retryable)
}

func TestRunOnceMapsQuarantinedRememberResult(t *testing.T) {
	runID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: runID,
			State: domain.V2MigrationStateRunning,
		},
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			NextCursor: "sf-030",
			Items: []neo4j.LegacyCorpusItem{{
				SourceID:       "sf-030",
				TeamID:         uuid.NewString(),
				OwnerProfileID: uuid.NewString(),
				Content:        "quarantine me",
			}},
		},
	}
	remember := &rememberStub{
		result: &memoryservice.V2RememberResult{
			IngestID:        "33333333-3333-3333-3333-333333333333",
			ProcessingState: string(domain.V2PlacementRunQuarantined),
		},
	}
	svc := New(store, reader, remember, Config{})

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Submitted)
	require.Len(t, store.updates, 1)
	assert.Equal(t, domain.V2MigrationOutcomeQuarantined, store.updates[0].Outcome)
}

func TestRunOnceReturnsRequiredSourceMapWriteFailure(t *testing.T) {
	runID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: runID,
			State: domain.V2MigrationStateRunning,
		},
		sourceMapErr: errors.New("source map write failed"),
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			Items: []neo4j.LegacyCorpusItem{{
				SourceID:       "sf-040",
				TeamID:         uuid.NewString(),
				OwnerProfileID: uuid.NewString(),
				Content:        "source map failure",
			}},
		},
	}
	svc := New(store, reader, &rememberStub{}, Config{})

	result, err := svc.RunOnce(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "source map write failed")
	require.NotNil(t, result)
	requirePartialRunOnceError(t, err, result)
	assert.Equal(t, 1, result.Submitted)
	assert.Empty(t, store.updates)
}

func TestRunOnceRecordsCorpusUpsertFailureAsCutoverBlocker(t *testing.T) {
	runID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{
			RunID: runID,
			State: domain.V2MigrationStateRunning,
		},
		upsertErr: errors.New("postgres unavailable: api_key=secret"),
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			Items: []neo4j.LegacyCorpusItem{{
				SourceID:       "sf-045",
				TeamID:         uuid.NewString(),
				OwnerProfileID: uuid.NewString(),
				Content:        "upsert failure",
			}},
		},
	}
	svc := New(store, reader, &rememberStub{}, Config{})

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, result.Failed)
	assert.Empty(t, store.updates)
	require.Len(t, store.exclusions, 1)
	assert.True(t, store.exclusions[0].BlocksCutover)
	assert.Equal(t, "postgres corpus item upsert failed", store.exclusions[0].Reason)
	assert.Equal(t, "migration repository write failed", store.exclusions[0].Metadata["error"])
	require.Len(t, store.errors, 1)
	assert.Equal(t, "upsert_corpus_item", store.errors[0].Phase)
	assert.Equal(t, "postgres_write_failed", store.errors[0].ErrorCode)
	assert.Equal(t, "migration repository write failed", store.errors[0].Message)
	assert.NotContains(t, store.errors[0].Message, "secret")
	assert.True(t, store.errors[0].Retryable)
}

func TestRunOnceReturnsCheckpointAndStatsRefreshFailures(t *testing.T) {
	t.Run("checkpoint", func(t *testing.T) {
		runID := uuid.NewString()
		checkpointErr := errors.New("checkpoint failed")
		store := &executorStoreStub{
			run: &domain.V2MigrationRun{
				RunID: runID,
				State: domain.V2MigrationStateRunning,
			},
			checkpointErr: checkpointErr,
		}
		svc := New(store, &legacyReaderStub{}, &rememberStub{}, Config{})

		result, err := svc.RunOnce(context.Background())
		require.ErrorIs(t, err, checkpointErr)
		require.NotNil(t, result)
		requirePartialRunOnceError(t, err, result)
		assert.Equal(t, runID, result.RunID)
		assert.Equal(t, 0, store.refreshCalls)
	})

	t.Run("stats refresh", func(t *testing.T) {
		runID := uuid.NewString()
		refreshErr := errors.New("refresh failed")
		store := &executorStoreStub{
			run: &domain.V2MigrationRun{
				RunID: runID,
				State: domain.V2MigrationStateRunning,
			},
			refreshErr: refreshErr,
		}
		svc := New(store, &legacyReaderStub{}, &rememberStub{}, Config{})

		result, err := svc.RunOnce(context.Background())
		require.ErrorIs(t, err, refreshErr)
		require.NotNil(t, result)
		requirePartialRunOnceError(t, err, result)
		assert.Equal(t, runID, result.RunID)
		require.Len(t, store.checkpoints, 1)
		assert.Equal(t, 1, store.refreshCalls)
	})
}

func TestPartialRunOnceErrorWrapsResultAndCause(t *testing.T) {
	cause := errors.New("checkpoint failed")
	result := &RunOnceResult{RunID: uuid.NewString(), Fetched: 1}

	err := partialRunOnceError(result, cause)

	var partial *PartialRunOnceError
	require.ErrorAs(t, err, &partial)
	require.Same(t, result, partial.Result)
	require.ErrorIs(t, err, cause)
	assert.Equal(t, cause.Error(), err.Error())
	assert.Nil(t, partialRunOnceError(result, nil))
}

func TestMigrationErrorMessageUsesBoundedSafeText(t *testing.T) {
	assert.Equal(t, "legacy corpus item is invalid", migrationErrorMessage("validate_legacy_item", "invalid_legacy_item", errors.New("content contains sk-secret")))
	assert.Equal(t, "migration phase failed", migrationErrorMessage("custom_phase", "custom_code", errors.New("dsn postgres://user:secret@db")))
	assert.Empty(t, migrationErrorMessage("", "", nil))
}

func TestMigrationExecutorHelpersCoverFallbackBranches(t *testing.T) {
	var nilPartial *PartialRunOnceError
	assert.Equal(t, "v2 migration executor: partial run failed", nilPartial.Error())
	assert.Nil(t, nilPartial.Unwrap())

	svc := &service{cfg: Config{PageSize: 42}, store: &executorStoreStub{}}
	assert.Equal(t, 42, svc.pageSize())

	require.NoError(t, svc.recordSourceMaps(context.Background(), uuid.NewString(), neo4j.LegacyCorpusItem{}, nil))
	require.NoError(t, svc.recordSourceMaps(context.Background(), uuid.NewString(), neo4j.LegacyCorpusItem{}, &memoryservice.V2RememberResult{}))
	assert.Empty(t, svc.store.(*executorStoreStub).sourceMaps)

	assert.ErrorContains(t, validateLegacyCorpusItem(neo4j.LegacyCorpusItem{
		SourceID:       "sf-no-hash",
		TeamID:         uuid.NewString(),
		OwnerProfileID: uuid.NewString(),
		Content:        "content without hash",
	}), "source_hash")
	assert.Empty(t, legacyEntityHints(neo4j.LegacyCorpusItem{}))
}

func TestLegacyRememberRequestPreservesSupportedSourceAuthorityAndHints(t *testing.T) {
	item := neo4j.LegacyCorpusItem{
		SourceID:   "sf-050",
		Source:     "https://example.test/source",
		SourceType: "conversation",
		Authority:  "derived",
		Labels:     []string{"legacy_neo4j_migration", " note ", "note"},
		Facts: []neo4j.LegacyFactHint{{
			FactID:    "fact-1",
			Subject:   "Grace",
			Predicate: "worked_on",
			Object:    "compiler",
		}},
	}

	req := legacyRememberRequest("run-1", item)

	require.Len(t, req.Evidence, 1)
	assert.Equal(t, "conversation", req.Evidence[0].SourceType)
	assert.Equal(t, "derived", req.Evidence[0].Authority)
	assert.Equal(t, "https://example.test/source", req.Evidence[0].Source)
	assert.Equal(t, "neo4j://source-fragment/sf-050", req.Evidence[0].SourceKey)
	assert.Equal(t, []string{"legacy_neo4j_migration", "note"}, req.Evidence[0].Labels)
	require.Len(t, req.EntityHints, 2)
	require.Len(t, req.RelationshipHints, 1)
	assert.Equal(t, "fact", req.RelationshipHints[0]["legacy_kind"])
}

func TestLegacyHelpersCoverOptionalAndInvalidBranches(t *testing.T) {
	createdAt := time.Date(2026, 7, 17, 16, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)
	metadata := legacyCorpusMetadata(neo4j.LegacyCorpusItem{
		SourceID:       "sf-060",
		SourceKind:     "custom-source",
		CreatedAt:      &createdAt,
		UpdatedAt:      &updatedAt,
		Metadata:       map[string]any{"legacy": true},
		Classification: map[string]any{"tier": "semantic"},
	})

	assert.Equal(t, "custom-source", metadata["migration_source_kind"])
	assert.Equal(t, createdAt.Format(time.RFC3339Nano), metadata["legacy_created_at"])
	assert.Equal(t, updatedAt.Format(time.RFC3339Nano), metadata["legacy_updated_at"])
	assert.Equal(t, map[string]any{"legacy": true}, metadata["legacy_metadata"])
	assert.Equal(t, map[string]any{"tier": "semantic"}, metadata["legacy_classification"])

	assert.ErrorContains(t, validateLegacyCorpusItem(neo4j.LegacyCorpusItem{}), "source_id")
	assert.ErrorContains(t, validateLegacyCorpusItem(neo4j.LegacyCorpusItem{
		SourceID:       "sf-061",
		TeamID:         "not-a-uuid",
		OwnerProfileID: uuid.NewString(),
		Content:        "content",
	}), "team_id")
	assert.ErrorContains(t, validateLegacyCorpusItem(neo4j.LegacyCorpusItem{
		SourceID:       "sf-062",
		TeamID:         uuid.NewString(),
		OwnerProfileID: "not-a-uuid",
		Content:        "content",
	}), "owner_profile_id")
	assert.ErrorContains(t, validateLegacyCorpusItem(neo4j.LegacyCorpusItem{
		SourceID:       "sf-063",
		TeamID:         uuid.NewString(),
		OwnerProfileID: uuid.NewString(),
	}), "content")

	assert.Equal(t, domain.V2MigrationOutcomeFailed, rememberOutcome(nil))
	assert.Equal(t, domain.V2MigrationOutcomeQuarantined, rememberOutcome(&memoryservice.V2RememberResult{
		ProcessingState: string(domain.V2PlacementRunQuarantined),
	}))
	assert.Equal(t, domain.V2MigrationOutcomeFailed, rememberOutcome(&memoryservice.V2RememberResult{
		ProcessingState: string(domain.V2PlacementRunFailed),
	}))
	assert.Equal(t, domain.V2MigrationOutcomeAccepted, rememberOutcome(&memoryservice.V2RememberResult{
		ProcessingState: string(domain.V2PlacementRunCompleted),
	}))
	assert.Equal(t, domain.V2MigrationOutcomePending, rememberOutcome(&memoryservice.V2RememberResult{
		ProcessingState: string(domain.V2PlacementRunQueued),
	}))
	assert.Equal(t, "", stringFromMap(nil, "missing"))
	assert.Equal(t, "", stringFromMap(map[string]any{"value": nil}, "value"))
	assert.Nil(t, relationshipHint("subject", "", "object", "id", "claim"))
	assert.Equal(t, "derived", legacyAuthority("untrusted"))
}

type executorStoreStub struct {
	run                *domain.V2MigrationRun
	checkpoint         map[string]any
	upsertOutcomes     map[string]string
	upsertErr          error
	validateOwnerErr   error
	validOwnerProfiles map[string]bool
	sourceMapErr       error
	checkpointErr      error
	refreshErr         error
	upserts            []repository.V2UpsertMigrationCorpusItemInput
	updates            []repository.V2UpdateMigrationCorpusOutcomeInput
	sourceMaps         []repository.V2UpsertMigrationSourceMapInput
	checkpoints        []repository.V2UpsertMigrationCheckpointInput
	errors             []repository.V2RecordMigrationErrorInput
	exclusions         []repository.V2RecordMigrationExclusionInput
	refreshCalls       int
	finalizeCalls      int
	finalizeErr        error
}

func (s *executorStoreStub) GetLatestRun(context.Context) (*domain.V2MigrationRun, error) {
	return s.run, nil
}

func (s *executorStoreStub) ValidateMigrationOwnerProfile(_ context.Context, input repository.V2ValidateMigrationOwnerProfileInput) (bool, error) {
	if s.validateOwnerErr != nil {
		return false, s.validateOwnerErr
	}
	if s.validOwnerProfiles == nil {
		return true, nil
	}
	return s.validOwnerProfiles[ownerProfileKey(input.TeamID, input.OwnerProfileID)], nil
}

func ownerProfileKey(teamID, ownerProfileID string) string {
	return teamID + "/" + ownerProfileID
}

func requirePartialRunOnceError(t *testing.T, err error, result *RunOnceResult) {
	t.Helper()
	var partial *PartialRunOnceError
	require.ErrorAs(t, err, &partial)
	require.Same(t, result, partial.Result)
}

func (s *executorStoreStub) UpsertMigrationCorpusItem(_ context.Context, input repository.V2UpsertMigrationCorpusItemInput) (*domain.V2MigrationCorpusItem, error) {
	s.upserts = append(s.upserts, input)
	if s.upsertErr != nil {
		return nil, s.upsertErr
	}
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
	return s.sourceMapErr
}

func (s *executorStoreStub) UpsertMigrationCheckpoint(_ context.Context, input repository.V2UpsertMigrationCheckpointInput) error {
	s.checkpoints = append(s.checkpoints, input)
	return s.checkpointErr
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
	return s.run, s.refreshErr
}

func (s *executorStoreStub) FinalizeMigrationRun(context.Context, string, time.Time) (*domain.V2MigrationRun, error) {
	s.finalizeCalls++
	return s.run, s.finalizeErr
}

type legacyReaderStub struct {
	calls int
	req   neo4j.LegacyCorpusPageRequest
	page  neo4j.LegacyCorpusPage
	err   error
}

func (r *legacyReaderStub) ReadCorpusPage(_ context.Context, req neo4j.LegacyCorpusPageRequest) (neo4j.LegacyCorpusPage, error) {
	r.calls++
	r.req = req
	return r.page, r.err
}

type rememberStub struct {
	requests           []memoryservice.V2RememberRequest
	actors             []requestctx.ActorProfile
	migrationActors    []requestctx.MigrationActor
	credentialContexts []bool
	result             *memoryservice.V2RememberResult
	err                error
}

func (s *rememberStub) RememberV2(ctx context.Context, req memoryservice.V2RememberRequest) (*memoryservice.V2RememberResult, error) {
	s.requests = append(s.requests, req)
	actor, _ := requestctx.ActorProfileFromContext(ctx)
	s.actors = append(s.actors, actor)
	migrationActor, _ := requestctx.MigrationActorFromContext(ctx)
	s.migrationActors = append(s.migrationActors, migrationActor)
	_, credentialOK := requestctx.ActorCredentialFromContext(ctx)
	s.credentialContexts = append(s.credentialContexts, credentialOK)
	if s.err != nil {
		return nil, s.err
	}
	if s.result != nil {
		return s.result, nil
	}
	return &memoryservice.V2RememberResult{
		IngestID:        uuid.NewString(),
		ProcessingState: string(domain.V2PlacementRunQueued),
	}, nil
}
