package dreamservice

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
)

func TestV2RunCycleUsesAuthenticatedActorAndCandidateSafeInputs(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	runID := uuid.NewString()
	subjectID := uuid.NewString()
	objectID := uuid.NewString()
	sourceID := uuid.NewString()
	repo := &dreamRepositoryStub{
		run: repository.V2DreamCycleRun{
			TeamID:         teamID.String(),
			RunID:          runID,
			OwnerProfileID: ownerID.String(),
			RunDate:        "2026-07-17",
			WindowKey:      "manual",
			Status:         "running",
			Claimed:        true,
		},
		inputs: []repository.V2DreamInput{{
			RelationshipID:   sourceID,
			OwnerProfileID:   ownerID.String(),
			Version:          3,
			Tier:             "candidate",
			Status:           "pending_evidence",
			SubjectEntityID:  subjectID,
			SubjectName:      "Dense-Mem",
			PredicateKey:     "uses",
			PredicateVersion: 1,
			ObjectEntityID:   objectID,
			ObjectName:       "PostgreSQL",
		}},
	}
	svc := New(Dependencies{
		Store:     repo,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
		Now:       func() time.Time { return time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC) },
	})

	result, err := svc.RunCycle(dreamTestContext(teamID, ownerID), "ignored-profile", RunCycleRequest{
		Manual:     true,
		MaxOutputs: 2,
	})

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, runID, result.RunID)
	require.Equal(t, ownerID.String(), result.ProfileID)
	require.Len(t, repo.upserts, 1)
	assert.Equal(t, teamID.String(), repo.listInput.TeamID)
	assert.Equal(t, teamID.String(), repo.claimInput.TeamID)
	assert.Equal(t, ownerID.String(), repo.claimInput.OwnerProfileID)
	assert.Equal(t, teamID.String(), repo.upserts[0].TeamID)
	assert.Equal(t, ownerID.String(), repo.upserts[0].OwnerProfileID)
	assert.Equal(t, runID, repo.upserts[0].RunID)
	assert.Equal(t, sourceID, repo.upserts[0].SourceRefs[0]["id"])
	assert.Equal(t, 3, repo.upserts[0].SourceVersions[sourceID])
	assert.Equal(t, 1, repo.completeInput.CreatedHypotheses)
}

func TestV2RunCyclePersistsValidatedProviderHypothesis(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	runID := uuid.NewString()
	subjectID := uuid.NewString()
	objectID := uuid.NewString()
	activeSourceID := uuid.NewString()
	candidateSourceID := uuid.NewString()
	repo := &dreamRepositoryStub{
		run: repository.V2DreamCycleRun{
			TeamID:         teamID.String(),
			RunID:          runID,
			OwnerProfileID: ownerID.String(),
			RunDate:        "2026-07-17",
			Status:         "running",
			Claimed:        true,
		},
		inputs: []repository.V2DreamInput{
			{
				RelationshipID:   activeSourceID,
				OwnerProfileID:   ownerID.String(),
				Version:          2,
				Tier:             "validated_claim",
				Status:           "active",
				SubjectEntityID:  subjectID,
				SubjectName:      "Dense-Mem",
				PredicateKey:     "works_on",
				PredicateVersion: 1,
				ObjectEntityID:   objectID,
				ObjectName:       "PostgreSQL",
			},
			{
				RelationshipID:   candidateSourceID,
				OwnerProfileID:   ownerID.String(),
				Version:          4,
				Tier:             "candidate",
				Status:           "pending_evidence",
				SubjectEntityID:  subjectID,
				SubjectName:      "Dense-Mem",
				PredicateKey:     "uses",
				PredicateVersion: 1,
				ObjectEntityID:   objectID,
				ObjectName:       "PostgreSQL",
			},
		},
	}
	generator := &dreamGeneratorStub{
		model: "provider-v2",
		generated: []GeneratedDream{{
			Hypothesis:       "Dense-Mem may use PostgreSQL.",
			Rationale:        "Active and candidate inputs point at a possible durable dependency.",
			SubjectEntityID:  subjectID,
			PredicateKey:     "uses",
			PredicateVersion: 1,
			ObjectEntityID:   objectID,
			SourceRefs: []domain.DreamSourceRef{
				{Type: "claim", ID: activeSourceID},
				{Type: "candidate_relationship", ID: candidateSourceID},
			},
		}},
	}
	svc := New(Dependencies{
		Store:     repo,
		Generator: generator,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
		Now:       func() time.Time { return time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC) },
	})

	result, err := svc.RunCycle(dreamTestContext(teamID, ownerID), "ignored-profile", RunCycleRequest{Manual: true})

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 1, generator.calls)
	require.Len(t, generator.lastReq.Inputs, 2)
	require.Len(t, repo.upserts, 1)
	assert.Equal(t, "provider", repo.upserts[0].GeneratorKind)
	assert.Equal(t, "provider-v2", repo.upserts[0].GeneratorVersion)
	assert.Equal(t, subjectID, repo.upserts[0].SubjectEntityID)
	assert.Equal(t, "uses", repo.upserts[0].PredicateKey)
	assert.Equal(t, objectID, repo.upserts[0].ObjectEntityID)
	assert.Len(t, repo.upserts[0].SourceRefs, 2)
	assert.Equal(t, 2, repo.upserts[0].SourceVersions[activeSourceID])
	assert.Equal(t, 4, repo.upserts[0].SourceVersions[candidateSourceID])
	assert.NotEmpty(t, repo.upserts[0].ContentHash)
}

func TestV2RunCycleRejectsMalformedProviderOutputWithoutFallback(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	runID := uuid.NewString()
	subjectID := uuid.NewString()
	objectID := uuid.NewString()
	sourceID := uuid.NewString()
	repo := &dreamRepositoryStub{
		run: repository.V2DreamCycleRun{
			TeamID:         teamID.String(),
			RunID:          runID,
			OwnerProfileID: ownerID.String(),
			RunDate:        "2026-07-17",
			Status:         "running",
			Claimed:        true,
		},
		inputs: []repository.V2DreamInput{{
			RelationshipID:   sourceID,
			OwnerProfileID:   ownerID.String(),
			Version:          1,
			Tier:             "candidate",
			Status:           "pending_evidence",
			SubjectEntityID:  subjectID,
			SubjectName:      "Dense-Mem",
			PredicateKey:     "uses",
			PredicateVersion: 1,
			ObjectEntityID:   objectID,
			ObjectName:       "PostgreSQL",
		}},
	}
	generator := &dreamGeneratorStub{
		model: "provider-v2",
		generated: []GeneratedDream{
			{
				Hypothesis: "Missing source should be rejected.",
				SourceRefs: []domain.DreamSourceRef{
					{Type: "candidate_relationship", ID: "missing-source"},
				},
			},
			{
				Hypothesis:      "Unknown endpoint should be rejected.",
				SubjectEntityID: uuid.NewString(),
				PredicateKey:    "uses",
				ObjectEntityID:  objectID,
				SourceRefs: []domain.DreamSourceRef{
					{Type: "candidate_relationship", ID: sourceID},
				},
			},
		},
	}
	svc := New(Dependencies{
		Store:     repo,
		Generator: generator,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
		Now:       func() time.Time { return time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC) },
	})

	result, err := svc.RunCycle(dreamTestContext(teamID, ownerID), "ignored-profile", RunCycleRequest{Manual: true})

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	assert.Empty(t, repo.upserts)
	assert.Equal(t, 0, repo.completeInput.CreatedHypotheses)
	assert.Equal(t, 2, repo.completeInput.RejectedHypotheses)
}

func TestV2RunCycleMaterializesSeedHypothesesWithoutGenerator(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	runID := uuid.NewString()
	subjectID := uuid.NewString()
	objectID := uuid.NewString()
	sourceID := uuid.NewString()
	repo := &dreamRepositoryStub{
		run: repository.V2DreamCycleRun{
			TeamID:         teamID.String(),
			RunID:          runID,
			OwnerProfileID: ownerID.String(),
			RunDate:        "2026-07-17",
			Status:         "running",
			Claimed:        true,
		},
		inputs: []repository.V2DreamInput{{
			RelationshipID:   sourceID,
			OwnerProfileID:   ownerID.String(),
			Version:          7,
			Tier:             "candidate",
			Status:           "pending_evidence",
			SubjectEntityID:  subjectID,
			SubjectName:      "Dense-Mem",
			PredicateKey:     "uses",
			PredicateVersion: 1,
			ObjectEntityID:   objectID,
			ObjectName:       "PostgreSQL",
		}},
	}
	generator := &dreamGeneratorStub{err: errors.New("generator should not run for seeded V2 dreams")}
	svc := New(Dependencies{
		Store:     repo,
		Generator: generator,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
		Now:       func() time.Time { return time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC) },
	})

	result, err := svc.RunCycle(dreamTestContext(teamID, ownerID), "ignored-profile", RunCycleRequest{
		Manual: true,
		SeedDreams: []SeedDream{{
			Hypothesis:      "Dense-Mem may use PostgreSQL.",
			WhatIf:          "What if PostgreSQL is the durable store?",
			PossibleOutcome: "Ask for independent evidence before acceptance.",
			Rationale:       "Seeded eval hypothesis.",
			Likelihood:      0.8,
			Confidence:      0.6,
			SourceRefs:      []domain.DreamSourceRef{{Type: "candidate_relationship", ID: sourceID}},
		}},
	})

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	assert.Equal(t, 0, generator.calls)
	require.Len(t, repo.upserts, 1)
	assert.Equal(t, "Dense-Mem may use PostgreSQL.", repo.upserts[0].Statement)
	assert.Equal(t, "What if PostgreSQL is the durable store?", repo.upserts[0].Payload["what_if"])
	assert.Equal(t, "Ask for independent evidence before acceptance.", repo.upserts[0].Payload["possible_outcome"])
	require.NotNil(t, repo.upserts[0].Likelihood)
	require.NotNil(t, repo.upserts[0].Confidence)
	assert.Equal(t, 0.8, *repo.upserts[0].Likelihood)
	assert.Equal(t, 0.6, *repo.upserts[0].Confidence)
}

func TestV2RunCycleRequiresAuthenticatedActor(t *testing.T) {
	svc := New(Dependencies{
		Store:     &dreamRepositoryStub{},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true}},
	})

	_, err := svc.RunCycle(context.Background(), "profile-argument", RunCycleRequest{Manual: true})

	require.ErrorIs(t, err, ErrDreamAuthContext)
}

func TestV2ReadPathsRefreshAndMapHypotheses(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	runID := uuid.NewString()
	hypothesisID := uuid.NewString()
	likelihood := 0.7
	confidence := 0.8
	now := time.Date(2026, 7, 17, 4, 0, 0, 0, time.UTC)
	repo := &dreamRepositoryStub{
		run: repository.V2DreamCycleRun{
			TeamID:             teamID.String(),
			RunID:              runID,
			OwnerProfileID:     ownerID.String(),
			RunDate:            "2026-07-17",
			Status:             "completed",
			CreatedHypotheses:  1,
			RejectedHypotheses: 2,
			StartedAt:          now.Add(-time.Minute),
			CompletedAt:        &now,
		},
		getRecord: repository.V2HypothesisRecord{
			TeamID:         teamID.String(),
			HypothesisID:   hypothesisID,
			OwnerProfileID: ownerID.String(),
			Status:         string(domain.DreamStatusReinforced),
			Statement:      "Dense-Mem may use PostgreSQL.",
			Rationale:      "Eligible sources point at a possible relation.",
			Likelihood:     &likelihood,
			Confidence:     &confidence,
			CycleRunID:     runID,
			GeneratorKind:  "provider",
			ContentHash:    "sha256:read-path",
			SourceRefs: []map[string]any{{
				"type": "candidate_relationship",
				"id":   "source-1",
			}},
			Payload: map[string]any{
				"what_if":          "What if PostgreSQL is durable memory?",
				"possible_outcome": "Request confirmation.",
			},
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now,
		},
	}
	svc := New(Dependencies{
		Store:     repo,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
		Now:       func() time.Time { return now },
	})
	ctx := dreamTestContext(teamID, ownerID)

	listed, next, err := svc.List(ctx, "ignored-profile", ListOptions{Limit: 3, Status: string(domain.DreamStatusReinforced)})
	require.NoError(t, err)
	require.Empty(t, next)
	require.Len(t, listed, 1)
	assert.Equal(t, hypothesisID, listed[0].DreamID)
	assert.Equal(t, "What if PostgreSQL is durable memory?", listed[0].WhatIf)
	assert.Equal(t, []domain.DreamSourceRef{{Type: "candidate_relationship", ID: "source-1"}}, listed[0].SourceRefs)
	assert.Equal(t, teamID.String(), repo.refreshInput.TeamID)
	assert.Equal(t, ownerID.String(), repo.refreshInput.OwnerProfileID)

	got, err := svc.Get(ctx, "ignored-profile", hypothesisID)
	require.NoError(t, err)
	assert.Equal(t, domain.DreamStatusReinforced, got.Status)

	recalled, err := svc.Recall(ctx, "ignored-profile", "PostgreSQL", 5)
	require.NoError(t, err)
	require.Len(t, recalled, 1)

	runs, err := svc.ListRuns(ctx, "ignored-profile", 5)
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, runID, runs[0].RunID)
	assert.Equal(t, 1, runs[0].CreatedDreams)

	status, err := svc.Status(ctx, "ignored-profile")
	require.NoError(t, err)
	require.NotNil(t, status.LatestRun)
	assert.Equal(t, 1, status.PendingCount)
}

func TestV2ResolveFeedbackSubmitsIndependentEvidence(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	ingestID := uuid.NewString()
	repo := &dreamRepositoryStub{
		getRecord: repository.V2HypothesisRecord{
			TeamID:         teamID.String(),
			HypothesisID:   hypothesisID,
			OwnerProfileID: ownerID.String(),
			Status:         string(domain.DreamStatusProposed),
			Statement:      "Dense-Mem may use PostgreSQL.",
			Rationale:      "Candidate needs independent evidence.",
			CreatedAt:      time.Now().UTC(),
			UpdatedAt:      time.Now().UTC(),
		},
	}
	remember := &rememberServiceStub{result: &memoryservice.V2RememberResult{
		IngestID:        ingestID,
		ProcessingState: string(domain.V2PlacementRunQueued),
	}}
	svc := New(Dependencies{
		Store:     repo,
		Remember:  remember,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true}},
	})
	ctx := dreamTestContext(teamID, ownerID)

	_, err := svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_true",
	})
	require.ErrorContains(t, err, "independent evidence is required")

	_, err = svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_true",
		Evidence: []memoryservice.V2RememberEvidenceInput{{
			Content: "Dense-Mem may use PostgreSQL.",
		}},
	})
	require.ErrorContains(t, err, "hypothesis text cannot be submitted")

	res, err := svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_true",
		Feedback: "User confirmed this with a deployment note.",
		Evidence: []memoryservice.V2RememberEvidenceInput{{
			Content: "The deployment note says Dense-Mem uses PostgreSQL.",
		}},
		IdempotencyKey: "dream-submit-1",
	})

	require.NoError(t, err)
	require.NotNil(t, res.Memory)
	require.Equal(t, ingestID, res.Memory.IngestID)
	require.NotNil(t, res.Dream)
	assert.Equal(t, domain.DreamStatusSubmitted, res.Dream.Status)
	require.Len(t, remember.requests, 1)
	assert.Equal(t, domain.V2ContractVersion, remember.requests[0].ContractVersion)
	assert.Equal(t, "dream-submit-1", remember.requests[0].IdempotencyKey)
	assert.Equal(t, hypothesisID, remember.requests[0].Evidence[0].Metadata["hypothesis_id"])
	assert.Equal(t, teamID.String(), repo.submitInput.TeamID)
	assert.Equal(t, ownerID.String(), repo.submitInput.OwnerProfileID)
	assert.Equal(t, ingestID, repo.submitInput.SubmittedIngestID)
}

func TestV2ResolveFeedbackLifecycleDecisions(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	baseRecord := repository.V2HypothesisRecord{
		TeamID:         teamID.String(),
		HypothesisID:   hypothesisID,
		OwnerProfileID: ownerID.String(),
		Status:         string(domain.DreamStatusProposed),
		Statement:      "Dense-Mem may use PostgreSQL.",
		Rationale:      "Candidate needs independent evidence.",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}

	tests := []struct {
		name       string
		req        ResolveFeedbackRequest
		wantStatus domain.DreamStatus
		wantErr    string
		wantMemory bool
	}{
		{
			name:       "reject",
			req:        ResolveFeedbackRequest{DreamID: hypothesisID, Decision: "reject", Feedback: "not useful"},
			wantStatus: domain.DreamStatusRejected,
		},
		{
			name:       "stale",
			req:        ResolveFeedbackRequest{DreamID: hypothesisID, Decision: "stale", Feedback: "source changed"},
			wantStatus: domain.DreamStatusStale,
		},
		{
			name:       "reinforce",
			req:        ResolveFeedbackRequest{DreamID: hypothesisID, Decision: "reinforce", Feedback: "still useful"},
			wantStatus: domain.DreamStatusReinforced,
		},
		{
			name:       "ignore",
			req:        ResolveFeedbackRequest{DreamID: hypothesisID, Decision: "ignore"},
			wantStatus: domain.DreamStatusProposed,
		},
		{
			name: "confirm_false",
			req: ResolveFeedbackRequest{
				DreamID:  hypothesisID,
				Decision: "confirm_false",
				Evidence: []memoryservice.V2RememberEvidenceInput{{
					Content: "The deployment note says Dense-Mem does not use PostgreSQL.",
				}},
			},
			wantStatus: domain.DreamStatusRejected,
			wantMemory: true,
		},
		{
			name:    "invalid",
			req:     ResolveFeedbackRequest{DreamID: hypothesisID, Decision: "unknown"},
			wantErr: "invalid dream status",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &dreamRepositoryStub{getRecord: baseRecord}
			remember := &rememberServiceStub{result: &memoryservice.V2RememberResult{
				IngestID:        uuid.NewString(),
				ProcessingState: string(domain.V2PlacementRunQueued),
			}}
			svc := New(Dependencies{
				Store:     repo,
				Remember:  remember,
				AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true}},
			})
			res, err := svc.ResolveFeedback(dreamTestContext(teamID, ownerID), "ignored-profile", tc.req)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, res.Dream)
			assert.Equal(t, tc.wantStatus, res.Dream.Status)
			if tc.wantMemory {
				require.NotNil(t, res.Memory)
				assert.Equal(t, string(domain.DreamStatusRejected), repo.updateInput.Status)
				require.Len(t, remember.requests, 1)
			}
		})
	}
}

func TestV2RunCycleControlAndErrorBranches(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	sourceID := uuid.NewString()
	candidateInput := repository.V2DreamInput{
		RelationshipID:   sourceID,
		OwnerProfileID:   ownerID.String(),
		Version:          1,
		Tier:             "candidate",
		Status:           "pending_evidence",
		SubjectEntityID:  uuid.NewString(),
		SubjectName:      "Dense-Mem",
		PredicateKey:     "uses",
		PredicateVersion: 1,
		ObjectEntityID:   uuid.NewString(),
		ObjectName:       "PostgreSQL",
	}

	tests := []struct {
		name            string
		cfg             domain.DreamingRuntimeConfig
		req             RunCycleRequest
		repo            *dreamRepositoryStub
		wantStatus      string
		wantErr         string
		wantComplete    string
		wantRejected    int
		wantUpsertCount int
	}{
		{
			name:       "global disabled skips scheduled cycle",
			cfg:        domain.DreamingRuntimeConfig{Enabled: false, DreamEnabled: true, MaxOutputs: 5, Timezone: "UTC"},
			req:        RunCycleRequest{},
			repo:       &dreamRepositoryStub{},
			wantStatus: "skipped",
		},
		{
			name:       "dream phase disabled completes without repository reads",
			cfg:        domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: false, MaxOutputs: 5, Timezone: "UTC"},
			req:        RunCycleRequest{},
			repo:       &dreamRepositoryStub{},
			wantStatus: "completed",
		},
		{
			name: "unclaimed scheduled run skips without completion",
			cfg:  domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, MaxOutputs: 5, Timezone: "UTC"},
			repo: &dreamRepositoryStub{run: repository.V2DreamCycleRun{
				RunID:          uuid.NewString(),
				TeamID:         teamID.String(),
				OwnerProfileID: ownerID.String(),
				RunDate:        "2026-07-17",
				Status:         "running",
				Claimed:        false,
			}},
			wantStatus: "skipped",
		},
		{
			name:       "input query error is returned",
			cfg:        domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, MaxOutputs: 5, Timezone: "UTC"},
			repo:       &dreamRepositoryStub{listInputsErr: errors.New("list inputs failed")},
			wantStatus: "error",
			wantErr:    "list inputs failed",
		},
		{
			name:       "claim error is returned",
			cfg:        domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, MaxOutputs: 5, Timezone: "UTC"},
			repo:       &dreamRepositoryStub{claimErr: errors.New("claim failed")},
			wantStatus: "error",
			wantErr:    "claim failed",
		},
		{
			name:       "completion error is returned",
			cfg:        domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, MaxOutputs: 5, Timezone: "UTC"},
			repo:       &dreamRepositoryStub{completeErr: errors.New("complete failed")},
			wantStatus: "error",
			wantErr:    "complete failed",
		},
		{
			name:            "exact existing relationship is rejected without failing cycle",
			cfg:             domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, MaxOutputs: 5, Timezone: "UTC"},
			repo:            &dreamRepositoryStub{inputs: []repository.V2DreamInput{candidateInput}, upsertErr: repository.ErrV2DreamExactRelationshipExists},
			wantStatus:      "completed",
			wantComplete:    "completed",
			wantRejected:    1,
			wantUpsertCount: 1,
		},
		{
			name:            "unexpected upsert error fails cycle and records failed completion",
			cfg:             domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true, MaxOutputs: 5, Timezone: "UTC"},
			repo:            &dreamRepositoryStub{inputs: []repository.V2DreamInput{candidateInput}, upsertErr: errors.New("write failed")},
			wantStatus:      "error",
			wantErr:         "write failed",
			wantComplete:    "failed",
			wantUpsertCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := New(Dependencies{
				Store:     tc.repo,
				AppConfig: cycleAppConfigStub{cfg: tc.cfg},
				Now:       func() time.Time { return time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC) },
			})
			result, err := svc.RunCycle(dreamTestContext(teamID, ownerID), "ignored-profile", tc.req)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.NotNil(t, result)
			assert.Equal(t, tc.wantStatus, result.Status)
			if tc.wantComplete != "" {
				assert.Equal(t, tc.wantComplete, tc.repo.completeInput.Status)
			}
			assert.Equal(t, tc.wantRejected, tc.repo.completeInput.RejectedHypotheses)
			assert.Len(t, tc.repo.upserts, tc.wantUpsertCount)
		})
	}
}

func TestV2ResolveFeedbackErrorBranches(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	record := repository.V2HypothesisRecord{
		TeamID:         teamID.String(),
		HypothesisID:   hypothesisID,
		OwnerProfileID: ownerID.String(),
		Status:         string(domain.DreamStatusProposed),
		Statement:      "Dense-Mem may use PostgreSQL.",
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	ctx := dreamTestContext(teamID, ownerID)

	svc := New(Dependencies{
		Store:     &dreamRepositoryStub{getErr: repository.ErrV2DreamHypothesisNotFound},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true}},
	})
	_, err := svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{DreamID: hypothesisID, Decision: "reject"})
	require.ErrorIs(t, err, ErrDreamNotFound)

	svc = New(Dependencies{
		Store:     &dreamRepositoryStub{getRecord: record},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true}},
	})
	_, err = svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_true",
		Evidence: []memoryservice.V2RememberEvidenceInput{{
			Content: "The deployment note says Dense-Mem uses PostgreSQL.",
		}},
	})
	require.ErrorContains(t, err, "remember service is required")

	svc = New(Dependencies{
		Store: &dreamRepositoryStub{
			getRecord: record,
			updateErr: repository.ErrV2DreamHypothesisNotFound,
		},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true}},
	})
	_, err = svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{DreamID: hypothesisID, Decision: "reject"})
	require.ErrorIs(t, err, ErrDreamNotFound)

	svc = New(Dependencies{
		Store:     &dreamRepositoryStub{getRecord: record},
		Remember:  &rememberServiceStub{err: errors.New("remember failed")},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true}},
	})
	_, err = svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_false",
		Evidence: []memoryservice.V2RememberEvidenceInput{{
			Content: "The deployment note says Dense-Mem does not use PostgreSQL.",
		}},
	})
	require.ErrorContains(t, err, "remember failed")
}

func TestV2StatusAndHelperEdgeCases(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	repo := &dreamRepositoryStub{}
	svc := New(Dependencies{
		Store:     repo,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true}},
	})
	ctx := dreamTestContext(teamID, ownerID)

	runs, err := svc.ListRuns(ctx, "ignored-profile", 5)
	require.NoError(t, err)
	assert.Nil(t, runs)

	status, err := svc.Status(ctx, "ignored-profile")
	require.NoError(t, err)
	assert.Nil(t, status.LatestRun)
	assert.Equal(t, 0, status.PendingCount)

	_, err = New(Dependencies{
		Store:     &dreamRepositoryStub{refreshErr: errors.New("refresh failed")},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, DreamEnabled: true}},
	}).Status(ctx, "ignored-profile")
	require.ErrorContains(t, err, "refresh failed")

	assert.Equal(t, "fact", dreamSourceType(repository.V2DreamInput{Tier: "fact"}))
	assert.Equal(t, "claim", dreamSourceType(repository.V2DreamInput{Tier: "validated_claim"}))
	assert.Equal(t, "relationship", dreamSourceType(repository.V2DreamInput{Tier: "other"}))
	assert.Equal(t, "from stringer", anyString(testStringer("from stringer")))
	require.Nil(t, optionalProbability(0))
	require.NotNil(t, optionalProbability(2))
	assert.Equal(t, 1.0, *optionalProbability(2))
}

func dreamTestContext(teamID uuid.UUID, ownerID uuid.UUID) context.Context {
	return requestctx.WithActorCredential(
		requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
			TeamID:    teamID,
			ProfileID: ownerID,
		}),
		requestctx.ActorCredential{
			KeyID:      uuid.New(),
			AuthMethod: "api_key",
			Role:       "member",
		},
	)
}

type dreamRepositoryStub struct {
	inputs        []repository.V2DreamInput
	run           repository.V2DreamCycleRun
	getRecord     repository.V2HypothesisRecord
	listRecords   []repository.V2HypothesisRecord
	recallRecords []repository.V2HypothesisRecord
	listInput     repository.V2DreamInputListInput
	claimInput    repository.V2DreamCycleClaimInput
	completeInput repository.V2DreamCycleCompleteInput
	refreshInput  repository.V2RefreshHypothesisStalenessInput
	upserts       []repository.V2UpsertHypothesisInput
	submitInput   repository.V2SubmitHypothesisInput
	updateInput   repository.V2UpdateHypothesisStatusInput
	err           error
	claimErr      error
	completeErr   error
	listInputsErr error
	upsertErr     error
	listErr       error
	getErr        error
	recallErr     error
	refreshErr    error
	updateErr     error
	submitErr     error
	latestErr     error
}

func (s *dreamRepositoryStub) ClaimV2DreamCycle(_ context.Context, input repository.V2DreamCycleClaimInput) (*repository.V2DreamCycleRun, error) {
	s.claimInput = input
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	if s.err != nil {
		return nil, s.err
	}
	run := s.run
	if run.RunID == "" {
		run.RunID = uuid.NewString()
		run.TeamID = input.TeamID
		run.OwnerProfileID = input.OwnerProfileID
		run.RunDate = input.RunDate
		run.WindowKey = input.WindowKey
		run.Status = "running"
		run.Claimed = true
	}
	return &run, nil
}

func (s *dreamRepositoryStub) CompleteV2DreamCycle(_ context.Context, input repository.V2DreamCycleCompleteInput) error {
	s.completeInput = input
	if s.completeErr != nil {
		return s.completeErr
	}
	return s.err
}

func (s *dreamRepositoryStub) ListV2DreamInputs(_ context.Context, input repository.V2DreamInputListInput) ([]repository.V2DreamInput, error) {
	s.listInput = input
	if s.listInputsErr != nil {
		return nil, s.listInputsErr
	}
	return append([]repository.V2DreamInput(nil), s.inputs...), s.err
}

func (s *dreamRepositoryStub) UpsertV2Hypothesis(_ context.Context, input repository.V2UpsertHypothesisInput) (*repository.V2HypothesisRecord, bool, error) {
	s.upserts = append(s.upserts, input)
	if s.upsertErr != nil {
		return nil, false, s.upsertErr
	}
	if s.err != nil {
		return nil, false, s.err
	}
	return &repository.V2HypothesisRecord{
		TeamID:         input.TeamID,
		HypothesisID:   uuid.NewString(),
		OwnerProfileID: input.OwnerProfileID,
		Status:         string(domain.DreamStatusProposed),
		Statement:      input.Statement,
		CycleRunID:     input.RunID,
		ContentHash:    input.ContentHash,
		SourceRefs:     input.SourceRefs,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}, true, nil
}

func (s *dreamRepositoryStub) ListV2Hypotheses(context.Context, repository.V2ListHypothesesInput) ([]repository.V2HypothesisRecord, string, error) {
	if s.listErr != nil {
		return nil, "", s.listErr
	}
	if s.err != nil {
		return nil, "", s.err
	}
	if len(s.listRecords) > 0 {
		return append([]repository.V2HypothesisRecord(nil), s.listRecords...), "", nil
	}
	if s.getRecord.HypothesisID == "" {
		return nil, "", nil
	}
	return []repository.V2HypothesisRecord{s.getRecord}, "", nil
}

func (s *dreamRepositoryStub) GetV2Hypothesis(context.Context, repository.V2GetHypothesisInput) (*repository.V2HypothesisRecord, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.getRecord.HypothesisID == "" {
		return nil, repository.ErrV2DreamHypothesisNotFound
	}
	record := s.getRecord
	return &record, nil
}

func (s *dreamRepositoryStub) RecallV2Hypotheses(context.Context, repository.V2RecallHypothesesInput) ([]repository.V2HypothesisRecord, error) {
	if s.recallErr != nil {
		return nil, s.recallErr
	}
	if s.err != nil {
		return nil, s.err
	}
	if len(s.recallRecords) > 0 {
		return append([]repository.V2HypothesisRecord(nil), s.recallRecords...), nil
	}
	if s.getRecord.HypothesisID == "" {
		return nil, nil
	}
	return []repository.V2HypothesisRecord{s.getRecord}, nil
}

func (s *dreamRepositoryStub) RefreshV2HypothesisStaleness(_ context.Context, input repository.V2RefreshHypothesisStalenessInput) (int, error) {
	s.refreshInput = input
	if s.refreshErr != nil {
		return 0, s.refreshErr
	}
	return 0, s.err
}

func (s *dreamRepositoryStub) UpdateV2HypothesisStatus(_ context.Context, input repository.V2UpdateHypothesisStatusInput) (*repository.V2HypothesisRecord, error) {
	s.updateInput = input
	if s.updateErr != nil {
		return nil, s.updateErr
	}
	if s.err != nil {
		return nil, s.err
	}
	record := s.getRecord
	record.Status = input.Status
	record.InvalidatedReason = input.InvalidatedReason
	return &record, nil
}

func (s *dreamRepositoryStub) SubmitV2Hypothesis(_ context.Context, input repository.V2SubmitHypothesisInput) (*repository.V2HypothesisRecord, error) {
	s.submitInput = input
	if s.submitErr != nil {
		return nil, s.submitErr
	}
	if s.err != nil {
		return nil, s.err
	}
	record := s.getRecord
	record.Status = string(domain.DreamStatusSubmitted)
	record.SubmittedIngestID = input.SubmittedIngestID
	record.InvalidatedReason = input.InvalidatedReason
	return &record, nil
}

func (s *dreamRepositoryStub) LatestV2DreamCycle(context.Context, string, string) (*repository.V2DreamCycleRun, error) {
	if s.latestErr != nil {
		return nil, s.latestErr
	}
	if s.err != nil {
		return nil, s.err
	}
	if s.run.RunID == "" {
		return nil, nil
	}
	run := s.run
	return &run, nil
}

type rememberServiceStub struct {
	requests []memoryservice.V2RememberRequest
	result   *memoryservice.V2RememberResult
	err      error
}

func (s *rememberServiceStub) Remember(_ context.Context, req memoryservice.V2RememberRequest) (*memoryservice.V2RememberResult, error) {
	s.requests = append(s.requests, req)
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

func (s *rememberServiceStub) GetMemoryPlacement(context.Context, memoryservice.V2GetMemoryPlacementRequest) (*memoryservice.V2PlacementRunResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	return nil, errors.New("not implemented")
}
