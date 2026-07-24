package migrationexecutor

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

func TestRunOnceInfersUniqueTeamOwnerAndResolvesPriorExclusion(t *testing.T) {
	runID := uuid.NewString()
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{RunID: runID, State: domain.V2MigrationStateRunning},
		validOwnerProfiles: map[string]bool{
			ownerProfileKey(teamID, ownerID): true,
		},
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			NextCursor: "sf-ownerless",
			Items: []neo4j.LegacyCorpusItem{{
				SourceID: "sf-ownerless",
				TeamID:   teamID,
				Content:  "legacy evidence without fragment ownership",
			}},
		},
		ownerResolution: neo4j.LegacyOwnerResolution{
			OwnerProfileID:   ownerID,
			OwnerProfileName: "legacy-owner",
			CandidateCount:   1,
		},
	}
	remember := &rememberStub{}
	svc := New(store, reader, remember, Config{})

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Submitted)
	require.Zero(t, result.Excluded)
	require.Len(t, store.upserts, 1)
	require.Equal(t, ownerID, store.upserts[0].OwnerProfileID)
	require.Equal(
		t,
		domain.V2MigrationOwnerResolutionUniqueTeamOwner,
		store.upserts[0].Metadata["legacy_owner_resolution"],
	)
	require.Len(t, remember.actors, 1)
	require.Equal(t, uuid.MustParse(ownerID), remember.actors[0].ProfileID)
	require.Equal(t, "legacy-owner", remember.actors[0].ProfileName)
	require.Len(t, store.resolvedExclusions, 1)
	require.Equal(t, "sf-ownerless", store.resolvedExclusions[0].SourceID)
	require.Equal(t, ownerID, store.resolvedExclusions[0].OwnerProfileID)
	require.Equal(
		t,
		domain.V2MigrationOwnerResolutionUniqueTeamOwner,
		store.resolvedExclusions[0].Resolution,
	)
}

func TestRunOnceDoesNotResubmitCompletedOwnerlessEvidenceDuringRepair(t *testing.T) {
	runID := uuid.NewString()
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{RunID: runID, State: domain.V2MigrationStateRunning},
		validOwnerProfiles: map[string]bool{
			ownerProfileKey(teamID, ownerID): true,
		},
		upsertOutcomes: map[string]string{
			"sf-ownerless-completed": domain.V2MigrationOutcomeAccepted,
		},
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			NextCursor: "sf-ownerless-completed",
			Items: []neo4j.LegacyCorpusItem{{
				SourceID: "sf-ownerless-completed",
				TeamID:   teamID,
				Content:  "already staged ownerless evidence",
			}},
		},
		ownerResolution: neo4j.LegacyOwnerResolution{
			OwnerProfileID: ownerID,
			CandidateCount: 1,
		},
	}
	remember := &rememberStub{}
	svc := New(store, reader, remember, Config{})

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Skipped)
	require.Zero(t, result.Submitted)
	require.Empty(t, remember.requests)
	require.Len(t, store.resolvedExclusions, 1)
	require.Equal(t, "sf-ownerless-completed", store.resolvedExclusions[0].SourceID)
}

func TestRunOnceKeepsAmbiguousTeamOwnerBlocked(t *testing.T) {
	runID := uuid.NewString()
	teamID := uuid.NewString()
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{RunID: runID, State: domain.V2MigrationStateRunning},
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			NextCursor: "sf-ambiguous-owner",
			Items: []neo4j.LegacyCorpusItem{{
				SourceID: "sf-ambiguous-owner",
				TeamID:   teamID,
				Content:  "legacy evidence with ambiguous ownership",
			}},
		},
		ownerResolution: neo4j.LegacyOwnerResolution{CandidateCount: 2},
	}
	remember := &rememberStub{}
	svc := New(store, reader, remember, Config{})

	result, err := svc.RunOnce(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, result.Excluded)
	require.Zero(t, result.Submitted)
	require.Empty(t, remember.requests)
	require.Empty(t, store.resolvedExclusions)
	require.Len(t, store.exclusions, 1)
	require.Equal(
		t,
		domain.V2MigrationExclusionAmbiguousOwnerProfile,
		store.exclusions[0].Metadata["exclusion_code"],
	)
	require.Equal(t, domain.V2MigrationOwnerResolutionAmbiguous, store.exclusions[0].Metadata["legacy_owner_resolution"])
}

func TestRunOnceReturnsOwnerResolutionFailureWithoutAdvancingCheckpoint(t *testing.T) {
	runID := uuid.NewString()
	resolutionErr := errors.New("neo4j owner lookup failed: credential=secret")
	store := &executorStoreStub{
		run: &domain.V2MigrationRun{RunID: runID, State: domain.V2MigrationStateRunning},
	}
	reader := &legacyReaderStub{
		page: neo4j.LegacyCorpusPage{
			NextCursor: "sf-owner-resolution-error",
			Items: []neo4j.LegacyCorpusItem{{
				SourceID: "sf-owner-resolution-error",
				TeamID:   uuid.NewString(),
				Content:  "ownerless evidence while Neo4j is unavailable",
			}},
		},
		ownerResolutionErr: resolutionErr,
	}
	svc := New(store, reader, &rememberStub{}, Config{})

	result, err := svc.RunOnce(context.Background())
	require.ErrorIs(t, err, resolutionErr)
	requirePartialRunOnceError(t, err, result)
	require.Equal(t, 1, result.Failed)
	require.Empty(t, store.upserts)
	require.Empty(t, store.checkpoints)
	require.Len(t, store.errors, 1)
	require.Equal(t, "resolve_legacy_owner", store.errors[0].Phase)
	require.Equal(t, "owner_resolution_failed", store.errors[0].ErrorCode)
	require.Equal(t, "legacy owner profile resolution failed", store.errors[0].Message)
	require.NotContains(t, store.errors[0].Message, "secret")
}

func TestOwnerRepairClassifiersFailClosed(t *testing.T) {
	teamID := uuid.NewString()
	ownerID := uuid.NewString()
	tests := []struct {
		name string
		item neo4j.LegacyCorpusItem
		want string
	}{
		{
			name: "invalid team",
			item: neo4j.LegacyCorpusItem{TeamID: "invalid"},
			want: domain.V2MigrationExclusionInvalidLegacyItem,
		},
		{
			name: "ambiguous owner",
			item: neo4j.LegacyCorpusItem{TeamID: teamID, OwnerCandidates: 2},
			want: domain.V2MigrationExclusionAmbiguousOwnerProfile,
		},
		{
			name: "no owner",
			item: neo4j.LegacyCorpusItem{TeamID: teamID},
			want: domain.V2MigrationExclusionUnresolvedOwnerProfile,
		},
		{
			name: "invalid owner",
			item: neo4j.LegacyCorpusItem{TeamID: teamID, OwnerProfileID: "invalid"},
			want: domain.V2MigrationExclusionInvalidOwnerProfile,
		},
		{
			name: "other invalid legacy field",
			item: neo4j.LegacyCorpusItem{TeamID: teamID, OwnerProfileID: ownerID},
			want: domain.V2MigrationExclusionInvalidLegacyItem,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, validationExclusionCode(tt.item))
		})
	}
}

func TestCorpusOutcomeHasStagedEvidence(t *testing.T) {
	for _, outcome := range []string{
		domain.V2MigrationOutcomeAccepted,
		domain.V2MigrationOutcomeNeedsReview,
		domain.V2MigrationOutcomeRejected,
		domain.V2MigrationOutcomeQuarantined,
	} {
		require.True(t, corpusOutcomeHasStagedEvidence(outcome), outcome)
	}
	for _, outcome := range []string{
		domain.V2MigrationOutcomePending,
		domain.V2MigrationOutcomeFailed,
		domain.V2MigrationOutcomeExcluded,
	} {
		require.False(t, corpusOutcomeHasStagedEvidence(outcome), outcome)
	}
}

func TestRunOncePropagatesRequiredOwnerRepairWrites(t *testing.T) {
	type fixture struct {
		store    *executorStoreStub
		reader   *legacyReaderStub
		remember *rememberStub
		item     *neo4j.LegacyCorpusItem
		ownerID  string
	}
	tests := []struct {
		name      string
		configure func(*fixture, error)
	}{
		{
			name: "resolution error audit",
			configure: func(f *fixture, sentinel error) {
				f.item.OwnerProfileID = ""
				f.reader.ownerResolutionErr = errors.New("neo4j unavailable")
				f.store.recordErrorErr = sentinel
			},
		},
		{
			name: "invalid item exclusion",
			configure: func(f *fixture, sentinel error) {
				f.item.TeamID = "invalid"
				f.store.recordExclusionErr = sentinel
			},
		},
		{
			name: "invalid item error audit",
			configure: func(f *fixture, sentinel error) {
				f.item.TeamID = "invalid"
				f.store.recordErrorErr = sentinel
			},
		},
		{
			name: "corpus upsert exclusion",
			configure: func(f *fixture, sentinel error) {
				f.store.upsertErr = errors.New("postgres unavailable")
				f.store.recordExclusionErr = sentinel
			},
		},
		{
			name: "corpus upsert error audit",
			configure: func(f *fixture, sentinel error) {
				f.store.upsertErr = errors.New("postgres unavailable")
				f.store.recordErrorErr = sentinel
			},
		},
		{
			name: "resolved exclusion",
			configure: func(f *fixture, sentinel error) {
				f.item.OwnerProfileID = ""
				f.reader.ownerResolution = neo4j.LegacyOwnerResolution{
					OwnerProfileID: f.ownerID,
					CandidateCount: 1,
				}
				f.store.upsertOutcomes = map[string]string{
					f.item.SourceID: domain.V2MigrationOutcomeAccepted,
				}
				f.store.resolveExclusionErr = sentinel
			},
		},
		{
			name: "remember failure outcome",
			configure: func(f *fixture, sentinel error) {
				f.remember.err = errors.New("provider unavailable")
				f.store.updateErr = sentinel
			},
		},
		{
			name: "remember failure audit",
			configure: func(f *fixture, sentinel error) {
				f.remember.err = errors.New("provider unavailable")
				f.store.recordErrorErr = sentinel
			},
		},
		{
			name: "remember success outcome",
			configure: func(f *fixture, sentinel error) {
				f.store.updateErr = sentinel
			},
		},
		{
			name: "owner validation audit",
			configure: func(f *fixture, sentinel error) {
				f.store.validateOwnerErr = errors.New("postgres unavailable")
				f.store.recordErrorErr = sentinel
			},
		},
		{
			name: "invalid owner exclusion",
			configure: func(f *fixture, sentinel error) {
				f.store.validOwnerProfiles = map[string]bool{}
				f.store.recordExclusionErr = sentinel
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runID := uuid.NewString()
			teamID := uuid.NewString()
			ownerID := uuid.NewString()
			item := neo4j.LegacyCorpusItem{
				SourceID:       "sf-required-write",
				TeamID:         teamID,
				OwnerProfileID: ownerID,
				Content:        "legacy evidence",
			}
			f := &fixture{
				store: &executorStoreStub{
					run: &domain.V2MigrationRun{RunID: runID, State: domain.V2MigrationStateRunning},
					validOwnerProfiles: map[string]bool{
						ownerProfileKey(teamID, ownerID): true,
					},
				},
				reader:   &legacyReaderStub{},
				remember: &rememberStub{},
				item:     &item,
				ownerID:  ownerID,
			}
			sentinel := errors.New(tt.name)
			tt.configure(f, sentinel)
			f.reader.page = neo4j.LegacyCorpusPage{
				NextCursor: f.item.SourceID,
				Items:      []neo4j.LegacyCorpusItem{*f.item},
			}

			result, err := New(f.store, f.reader, f.remember, Config{}).RunOnce(context.Background())
			require.ErrorIs(t, err, sentinel)
			requirePartialRunOnceError(t, err, result)
			require.Empty(t, f.store.checkpoints)
		})
	}
}
