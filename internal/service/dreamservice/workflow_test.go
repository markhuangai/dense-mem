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
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestRunCycleDoesNotTurnOneCandidateIntoAHeuristicDream(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	runID := uuid.NewString()
	subjectID := uuid.NewString()
	objectID := uuid.NewString()
	sourceID := uuid.NewString()
	repo := &dreamRepositoryStub{
		run: repository.DreamCycleRun{
			TeamID:               teamID.String(),
			RunID:                runID,
			InitiatedByProfileID: ownerID.String(),
			RunDate:              "2026-07-17",
			WindowKey:            "manual",
			Status:               "running",
			Claimed:              true,
		},
		inputs: []repository.DreamInput{{
			RelationshipID:   sourceID,
			OwnerProfileID:   ownerID.String(),
			Version:          3,
			Status:           "pending_evidence",
			SubjectEntityID:  subjectID,
			SubjectName:      "Dense-Mem",
			PredicateKey:     "uses",
			PredicateVersion: 1,
			ObjectEntityID:   objectID,
			ObjectName:       "PostgreSQL",
			SubjectKind:      "project",
			ObjectKind:       "product",
			Evidence:         []repository.DreamEvidence{{Content: "Dense-Mem may use PostgreSQL.", Authority: "primary"}},
		}},
	}
	svc := New(Dependencies{
		Store:     repo,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
		Now:       func() time.Time { return time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC) },
	})

	result, err := svc.RunCycle(dreamTestContext(teamID, ownerID), "ignored-profile", RunCycleRequest{
		Manual:     true,
		MaxOutputs: 2,
	})

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, runID, result.RunID)
	require.Equal(t, teamID.String(), result.TeamID)
	assert.Empty(t, repo.upserts)
	assert.Equal(t, teamID.String(), repo.listInput.TeamID)
	assert.Equal(t, teamID.String(), repo.claimInput.TeamID)
	assert.Equal(t, ownerID.String(), repo.claimInput.InitiatedByProfileID)
	assert.Equal(t, 0, repo.completeInput.CreatedHypotheses)
}

func TestRunCycleUsesProviderConversationLease(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	now := time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC)
	providerLease := 6*time.Minute + 30*time.Second
	repo := &dreamRepositoryStub{}
	svc := New(Dependencies{
		Store:              repo,
		ProviderCycleLease: providerLease,
		AppConfig:          cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
		Now:                func() time.Time { return now },
	})

	_, err := svc.RunCycle(dreamTestContext(teamID, ownerID), "ignored-profile", RunCycleRequest{Manual: true})
	require.NoError(t, err)
	assert.Equal(t, now.Add(providerLease), repo.claimInput.LeaseUntil)
}

func TestRunCycleReturnsInputSelectionOutcome(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	svc := New(Dependencies{
		Store:     &dreamRepositoryStub{listInputsErr: errors.New("list inputs failed")},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
	})

	result, err := svc.RunCycle(dreamTestContext(teamID, ownerID), "ignored-profile", RunCycleRequest{Manual: true})
	require.ErrorContains(t, err, "list inputs failed")
	require.NotNil(t, result)
	assert.Equal(t, map[string]int{"input_selection_error": 1}, result.OutcomeSummary)
}

func TestGenerateDreamProposalsRetainsFailedLookupDiagnostics(t *testing.T) {
	inputs := testDreamPathInputs()
	predicates := testDreamPathPredicates()

	t.Run("target lookup", func(t *testing.T) {
		svc := New(Dependencies{Store: &dreamRepositoryStub{predicates: predicates, targetsErr: errors.New("target lookup failed")}}).(*service)
		result, err := svc.generateDreamProposals(context.Background(), uuid.NewString(), inputs, 1)
		require.ErrorContains(t, err, "target lookup failed")
		assert.Equal(t, 1, result.candidatePaths)
		assert.Equal(t, 2, result.candidateTargets)
		assert.True(t, result.targetLookupFailed)
	})

	t.Run("path assessment lookup", func(t *testing.T) {
		svc := New(Dependencies{Store: &dreamRepositoryStub{predicates: predicates, pathAssessErr: errors.New("assessment lookup failed")}}).(*service)
		result, err := svc.generateDreamProposals(context.Background(), uuid.NewString(), inputs, 1)
		require.ErrorContains(t, err, "assessment lookup failed")
		assert.Equal(t, 1, result.candidatePaths)
		assert.Equal(t, 2, result.candidateTargets)
		assert.False(t, result.targetLookupFailed)
		assert.True(t, result.pathAssessmentLookupFailed)
	})
}

func TestRunCyclePersistsValidatedProviderHypothesis(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	runID := uuid.NewString()
	subjectID := uuid.NewString()
	middleID := uuid.NewString()
	objectID := uuid.NewString()
	activeSourceID := "relationship_a"
	candidateSourceID := "relationship_b"
	repo := &dreamRepositoryStub{
		run: repository.DreamCycleRun{
			TeamID:               teamID.String(),
			RunID:                runID,
			InitiatedByProfileID: ownerID.String(),
			RunDate:              "2026-07-17",
			Status:               "running",
			Claimed:              true,
		},
		inputs: []repository.DreamInput{
			{
				RelationshipID:   activeSourceID,
				OwnerProfileID:   ownerID.String(),
				Version:          2,
				Status:           "active",
				SubjectEntityID:  subjectID,
				SubjectName:      "Dense-Mem",
				PredicateKey:     "works_on",
				PredicateVersion: 1,
				ObjectEntityID:   middleID,
				ObjectName:       "PostgreSQL",
				SubjectKind:      "project",
				ObjectKind:       "product",
				Evidence:         []repository.DreamEvidence{{Content: "Dense-Mem works on PostgreSQL.", Authority: "primary"}},
			},
			{
				RelationshipID:   candidateSourceID,
				OwnerProfileID:   ownerID.String(),
				Version:          4,
				Status:           "pending_evidence",
				SubjectEntityID:  middleID,
				SubjectName:      "PostgreSQL",
				PredicateKey:     "informs",
				PredicateVersion: 1,
				ObjectEntityID:   objectID,
				ObjectName:       "Search freshness",
				SubjectKind:      "product",
				ObjectKind:       "concept",
				Evidence:         []repository.DreamEvidence{{Content: "PostgreSQL informs search freshness.", Authority: "primary"}},
			},
		},
		predicates: []repository.DreamTargetPredicate{{
			PredicateKey: "uses", Version: 1, AllowedSubjectKinds: []string{"project"}, AllowedObjectKinds: []string{"concept"}, RelationshipKind: "state", CurrentCardinality: "many",
		}},
	}
	generator := &dreamGeneratorStub{
		model: "provider-canonical",
		generated: []GeneratedDream{{
			PathRef:         "path_1",
			PredicateRef:    "predicate_1",
			EvidenceRefs:    []string{"evidence_1", "evidence_2"},
			Hypothesis:      "Dense-Mem may use search freshness.",
			Rationale:       "Active and candidate inputs point at a possible durable dependency.",
			WhatIf:          "What if the connection needs independent confirmation?",
			PossibleOutcome: "Collect independent evidence before accepting it.",
		}},
	}
	svc := New(Dependencies{
		Store:     repo,
		Generator: generator,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
		Now:       func() time.Time { return time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC) },
	})

	result, err := svc.RunCycle(dreamTestContext(teamID, ownerID), "ignored-profile", RunCycleRequest{Manual: true})

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 1, generator.calls)
	require.Len(t, generator.lastReq.Paths, 1)
	require.Len(t, repo.upserts, 1)
	assert.Equal(t, "provider", repo.upserts[0].GeneratorKind)
	assert.Equal(t, "provider-canonical", repo.upserts[0].GeneratorVersion)
	assert.Equal(t, subjectID, repo.upserts[0].SubjectEntityID)
	assert.Equal(t, "uses", repo.upserts[0].PredicateKey)
	assert.Equal(t, objectID, repo.upserts[0].ObjectEntityID)
	assert.Len(t, repo.upserts[0].SourceRefs, 2)
	assert.Equal(t, 2, repo.upserts[0].SourceVersions[activeSourceID])
	assert.Equal(t, 4, repo.upserts[0].SourceVersions[candidateSourceID])
	assert.NotEmpty(t, repo.upserts[0].ContentHash)
}

func TestRunCycleRejectsMalformedProviderOutputWithoutFallback(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	runID := uuid.NewString()
	subjectID := uuid.NewString()
	middleID := uuid.NewString()
	objectID := uuid.NewString()
	sourceID := "relationship_a"
	secondSourceID := "relationship_b"
	repo := &dreamRepositoryStub{
		run: repository.DreamCycleRun{
			TeamID:               teamID.String(),
			RunID:                runID,
			InitiatedByProfileID: ownerID.String(),
			RunDate:              "2026-07-17",
			Status:               "running",
			Claimed:              true,
		},
		inputs: []repository.DreamInput{
			{RelationshipID: sourceID, OwnerProfileID: ownerID.String(), Version: 1, Status: "active", SubjectEntityID: subjectID, SubjectName: "Dense-Mem", SubjectKind: "project", PredicateKey: "works_on", PredicateVersion: 1, ObjectEntityID: middleID, ObjectName: "PostgreSQL", ObjectKind: "product", Evidence: []repository.DreamEvidence{{Content: "Dense-Mem works on PostgreSQL.", Authority: "primary"}}},
			{RelationshipID: secondSourceID, OwnerProfileID: ownerID.String(), Version: 1, Status: "pending_evidence", SubjectEntityID: middleID, SubjectName: "PostgreSQL", SubjectKind: "product", PredicateKey: "informs", PredicateVersion: 1, ObjectEntityID: objectID, ObjectName: "Search freshness", ObjectKind: "concept", Evidence: []repository.DreamEvidence{{Content: "PostgreSQL informs search freshness.", Authority: "primary"}}},
		},
		predicates: []repository.DreamTargetPredicate{{PredicateKey: "uses", Version: 1, AllowedSubjectKinds: []string{"project"}, AllowedObjectKinds: []string{"concept"}, RelationshipKind: "state", CurrentCardinality: "many"}},
	}
	generator := &dreamGeneratorStub{
		model: "provider-canonical",
		generated: []GeneratedDream{
			{
				PathRef:      "unknown_path",
				PredicateRef: "predicate_1",
				EvidenceRefs: []string{"evidence_1", "evidence_2"},
				Hypothesis:   "Missing source should be rejected.",
			},
			{
				PathRef:      "path_1",
				PredicateRef: "unknown_predicate",
				EvidenceRefs: []string{"evidence_1", "evidence_2"},
				Hypothesis:   "Unknown predicate should be rejected.",
			},
		},
	}
	svc := New(Dependencies{
		Store:     repo,
		Generator: generator,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
		Now:       func() time.Time { return time.Date(2026, 7, 17, 3, 0, 0, 0, time.UTC) },
	})

	result, err := svc.RunCycle(dreamTestContext(teamID, ownerID), "ignored-profile", RunCycleRequest{Manual: true})

	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	assert.Empty(t, repo.upserts)
	assert.Equal(t, 0, repo.completeInput.CreatedHypotheses)
	assert.Equal(t, 2, repo.completeInput.RejectedHypotheses)
}

func TestGenerateDreamProposalsRetainsProviderFailureDiagnostics(t *testing.T) {
	teamID := uuid.New()
	subjectID := uuid.NewString()
	middleID := uuid.NewString()
	objectID := uuid.NewString()
	repo := &dreamRepositoryStub{
		inputs: []repository.DreamInput{
			{RelationshipID: "relationship_a", Version: 1, Status: "active", SubjectEntityID: subjectID, SubjectName: "Dense-Mem", SubjectKind: "project", PredicateKey: "works_on", PredicateVersion: 1, ObjectEntityID: middleID, ObjectName: "PostgreSQL", ObjectKind: "product", Evidence: []repository.DreamEvidence{{Content: "Dense-Mem works on PostgreSQL.", Authority: "primary"}}},
			{RelationshipID: "relationship_b", Version: 1, Status: "active", SubjectEntityID: middleID, SubjectName: "PostgreSQL", SubjectKind: "product", PredicateKey: "informs", PredicateVersion: 1, ObjectEntityID: objectID, ObjectName: "Search freshness", ObjectKind: "concept", Evidence: []repository.DreamEvidence{{Content: "PostgreSQL informs search freshness.", Authority: "primary"}}},
		},
		predicates: []repository.DreamTargetPredicate{{PredicateKey: "uses", Version: 1, AllowedSubjectKinds: []string{"project"}, AllowedObjectKinds: []string{"concept"}}},
	}
	svc := New(Dependencies{
		Store:     repo,
		Generator: &dreamGeneratorStub{model: "provider-canonical", err: errors.New("provider unavailable")},
	}).(*service)

	generation, err := svc.generateDreamProposals(context.Background(), teamID.String(), repo.inputs, 5)

	require.EqualError(t, err, "provider unavailable")
	assert.True(t, generation.providerFailed)
	assert.Equal(t, "provider-canonical", generation.model)
	require.Len(t, generation.paths, 1)
	assert.Equal(t, 1, generation.candidatePaths)
	result := &RunCycleResult{}
	applyDreamGenerationDiagnostics(result, generation, len(repo.inputs), 0, 0)
	assert.Equal(t, 1, result.AttemptedPaths)
	assert.Equal(t, 1, result.OutcomeSummary["provider_failed"])
}

func TestRunCycleMaterializesSeedHypothesesWithoutGenerator(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	runID := uuid.NewString()
	subjectID := uuid.NewString()
	objectID := uuid.NewString()
	sourceID := uuid.NewString()
	repo := &dreamRepositoryStub{
		run: repository.DreamCycleRun{
			TeamID:               teamID.String(),
			RunID:                runID,
			InitiatedByProfileID: ownerID.String(),
			RunDate:              "2026-07-17",
			Status:               "running",
			Claimed:              true,
		},
		inputs: []repository.DreamInput{{
			RelationshipID:   sourceID,
			OwnerProfileID:   ownerID.String(),
			Version:          7,
			Status:           "pending_evidence",
			SubjectEntityID:  subjectID,
			SubjectName:      "Dense-Mem",
			PredicateKey:     "uses",
			PredicateVersion: 1,
			ObjectEntityID:   objectID,
			ObjectName:       "PostgreSQL",
		}},
	}
	generator := &dreamGeneratorStub{err: errors.New("generator should not run for seeded dreams")}
	svc := New(Dependencies{
		Store:     repo,
		Generator: generator,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
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
	assert.Equal(t, "evaluation_seed", repo.upserts[0].GeneratorKind)
	assert.Equal(t, "What if PostgreSQL is the durable store?", repo.upserts[0].Payload["what_if"])
	assert.Equal(t, "Ask for independent evidence before acceptance.", repo.upserts[0].Payload["possible_outcome"])
	require.NotNil(t, repo.upserts[0].Likelihood)
	require.NotNil(t, repo.upserts[0].Confidence)
	assert.Equal(t, 0.8, *repo.upserts[0].Likelihood)
	assert.Equal(t, 0.6, *repo.upserts[0].Confidence)
}

func TestRunCycleRequiresAuthenticatedActor(t *testing.T) {
	svc := New(Dependencies{
		Store:     &dreamRepositoryStub{},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
	})

	_, err := svc.RunCycle(context.Background(), "profile-argument", RunCycleRequest{Manual: true})

	require.ErrorIs(t, err, ErrDreamAuthContext)
}

func TestReadPathsRefreshAndMapHypotheses(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	runID := uuid.NewString()
	hypothesisID := uuid.NewString()
	likelihood := 0.7
	confidence := 0.8
	now := time.Date(2026, 7, 17, 4, 0, 0, 0, time.UTC)
	repo := &dreamRepositoryStub{
		run: repository.DreamCycleRun{
			TeamID:               teamID.String(),
			RunID:                runID,
			InitiatedByProfileID: ownerID.String(),
			RunDate:              "2026-07-17",
			Status:               "completed",
			CreatedHypotheses:    1,
			RejectedHypotheses:   2,
			StartedAt:            now.Add(-time.Minute),
			CompletedAt:          &now,
		},
		getRecord: repository.HypothesisRecord{
			TeamID:             teamID.String(),
			HypothesisID:       hypothesisID,
			CreatedByProfileID: ownerID.String(),
			Status:             string(domain.DreamStatusReinforced),
			Statement:          "Dense-Mem may use PostgreSQL.",
			Rationale:          "Eligible sources point at a possible relation.",
			Likelihood:         &likelihood,
			Confidence:         &confidence,
			CycleRunID:         runID,
			GeneratorKind:      "provider",
			ContentHash:        "sha256:read-path",
			SourceRefs: []map[string]any{{
				"type": "candidate_relationship",
				"id":   "source-1",
			}},
			Derivations: []repository.DreamDerivationSource{{
				PremisePosition:     1,
				RelationshipID:      "source-1",
				RelationshipVersion: 2,
				SourceGroupKey:      "doc:architecture",
				Quote:               "Dense-Mem uses PostgreSQL for durable memory.",
				Authority:           "primary",
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
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, StartTimeLocal: "03:00", Timezone: "UTC"}},
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
	require.Len(t, listed[0].Derivations, 1)
	assert.Equal(t, "Dense-Mem uses PostgreSQL for durable memory.", listed[0].Derivations[0].Quote)

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
	assert.Equal(t, 0, status.PendingCount)
}

func TestResolveFeedbackSubmitsIndependentEvidence(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	ingestID := uuid.NewString()
	repo := &dreamRepositoryStub{
		getRecord: repository.HypothesisRecord{
			TeamID:             teamID.String(),
			HypothesisID:       hypothesisID,
			CreatedByProfileID: ownerID.String(),
			Status:             string(domain.DreamStatusProposed),
			Statement:          "Dense-Mem may use PostgreSQL.",
			Rationale:          "Candidate needs independent evidence.",
			CreatedAt:          time.Now().UTC(),
			UpdatedAt:          time.Now().UTC(),
		},
	}
	remember := &rememberServiceStub{result: &memoryservice.RememberResult{
		IngestID:        ingestID,
		ProcessingState: string(domain.PlacementRunQueued),
	}}
	svc := New(Dependencies{
		Store:     repo,
		Remember:  remember,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
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
		Evidence: []memoryservice.RememberEvidenceInput{{
			Content: "Dense-Mem may use PostgreSQL.",
		}},
	})
	require.ErrorContains(t, err, "hypothesis text cannot be submitted")

	evidenceContent := " The deployment note says Dense-Mem uses PostgreSQL. "
	relationshipHint := map[string]any{
		"ref": "uses-postgresql",
		"subject": map[string]any{
			"name":        "Dense-Mem",
			"entity_kind": "project",
			"span":        map[string]any{"evidence_index": 0, "start": 26, "end": 35},
		},
		"predicate": map[string]any{
			"proposed_key": "uses",
			"surface":      "uses",
			"span":         map[string]any{"evidence_index": 0, "start": 36, "end": 40},
		},
		"object": map[string]any{"entity": map[string]any{
			"name":        "PostgreSQL",
			"entity_kind": "product",
			"span":        map[string]any{"evidence_index": 0, "start": 41, "end": 51},
		}},
		"polarity": "+",
		"modality": "statement",
		"supports": []any{map[string]any{
			"evidence_index": 0,
			"start":          1,
			"end":            52,
		}},
	}
	res, err := svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_true",
		Feedback: "User confirmed this with a deployment note.",
		Evidence: []memoryservice.RememberEvidenceInput{{
			Content: evidenceContent,
		}},
		RelationshipHints: []map[string]any{relationshipHint},
		IdempotencyKey:    "dream-submit-1",
	})

	require.NoError(t, err)
	require.NotNil(t, res.Memory)
	require.Equal(t, ingestID, res.Memory.IngestID)
	require.NotNil(t, res.Dream)
	assert.Equal(t, domain.DreamStatusSubmitted, res.Dream.Status)
	require.Len(t, remember.requests, 1)
	assert.Equal(t, "dream-submit-1", remember.requests[0].IdempotencyKey)
	assert.Equal(t, evidenceContent, remember.requests[0].Evidence[0].Content)
	assert.Equal(t, []map[string]any{relationshipHint}, remember.requests[0].RelationshipHints)
	assert.Equal(t, hypothesisID, remember.requests[0].Evidence[0].Metadata["hypothesis_id"])
	assert.Equal(t, teamID.String(), repo.submitInput.TeamID)
	assert.Equal(t, ownerID.String(), repo.submitInput.ActorProfileID)
	assert.Equal(t, ingestID, repo.submitInput.SubmittedIngestID)
}

func TestResolveFeedbackLifecycleDecisions(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	baseRecord := repository.HypothesisRecord{
		TeamID:             teamID.String(),
		HypothesisID:       hypothesisID,
		CreatedByProfileID: ownerID.String(),
		Status:             string(domain.DreamStatusProposed),
		Statement:          "Dense-Mem may use PostgreSQL.",
		Rationale:          "Candidate needs independent evidence.",
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
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
				Evidence: []memoryservice.RememberEvidenceInput{{
					Content: "The deployment note says Dense-Mem does not use PostgreSQL.",
				}},
			},
			wantStatus: domain.DreamStatusSubmitted,
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
			remember := &rememberServiceStub{result: &memoryservice.RememberResult{
				IngestID:        uuid.NewString(),
				ProcessingState: string(domain.PlacementRunQueued),
			}}
			svc := New(Dependencies{
				Store:     repo,
				Remember:  remember,
				AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
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
				assert.Equal(t, "confirm_false", repo.submitInput.Decision)
				require.Len(t, remember.requests, 1)
			}
		})
	}
}

func TestRunCycleControlAndErrorBranches(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	subjectID := uuid.NewString()
	middleID := uuid.NewString()
	objectID := uuid.NewString()
	firstInput := repository.DreamInput{
		RelationshipID:   "relationship_a",
		OwnerProfileID:   ownerID.String(),
		Version:          1,
		Status:           "active",
		SubjectEntityID:  subjectID,
		SubjectName:      "Dense-Mem",
		SubjectKind:      "project",
		PredicateKey:     "works_on",
		PredicateVersion: 1,
		ObjectEntityID:   middleID,
		ObjectName:       "PostgreSQL",
		ObjectKind:       "product",
		Evidence:         []repository.DreamEvidence{{Content: "Dense-Mem works on PostgreSQL.", Authority: "primary"}},
	}
	secondInput := repository.DreamInput{
		RelationshipID:   "relationship_b",
		OwnerProfileID:   ownerID.String(),
		Version:          1,
		Status:           "pending_evidence",
		SubjectEntityID:  middleID,
		SubjectName:      "PostgreSQL",
		SubjectKind:      "product",
		PredicateKey:     "informs",
		PredicateVersion: 1,
		ObjectEntityID:   objectID,
		ObjectName:       "Search freshness",
		ObjectKind:       "concept",
		Evidence:         []repository.DreamEvidence{{Content: "PostgreSQL informs search freshness.", Authority: "primary"}},
	}
	predicates := []repository.DreamTargetPredicate{{PredicateKey: "uses", Version: 1, AllowedSubjectKinds: []string{"project"}, AllowedObjectKinds: []string{"concept"}, RelationshipKind: "state", CurrentCardinality: "many"}}
	generator := &dreamGeneratorStub{generated: []GeneratedDream{{PathRef: "path_1", PredicateRef: "predicate_1", EvidenceRefs: []string{"evidence_1", "evidence_2"}, Hypothesis: "Dense-Mem may use search freshness.", Rationale: "The two premises support a possibility.", WhatIf: "What if it needs independent confirmation?", PossibleOutcome: "Collect independent evidence."}}}

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
			cfg:        domain.DreamingRuntimeConfig{Enabled: false, MaxOutputs: 5, Timezone: "UTC"},
			req:        RunCycleRequest{},
			repo:       &dreamRepositoryStub{},
			wantStatus: "skipped",
		},
		{
			name: "unclaimed scheduled run skips without completion",
			cfg:  domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC"},
			repo: &dreamRepositoryStub{run: repository.DreamCycleRun{
				RunID:                uuid.NewString(),
				TeamID:               teamID.String(),
				InitiatedByProfileID: ownerID.String(),
				RunDate:              "2026-07-17",
				Status:               "running",
				Claimed:              false,
			}},
			wantStatus: "skipped",
		},
		{
			name:       "input query error is returned",
			cfg:        domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC"},
			repo:       &dreamRepositoryStub{listInputsErr: errors.New("list inputs failed")},
			wantStatus: "error",
			wantErr:    "list inputs failed",
		},
		{
			name:       "claim error is returned",
			cfg:        domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC"},
			repo:       &dreamRepositoryStub{claimErr: errors.New("claim failed")},
			wantStatus: "error",
			wantErr:    "claim failed",
		},
		{
			name:       "completion error is returned",
			cfg:        domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC"},
			repo:       &dreamRepositoryStub{completeErr: errors.New("complete failed")},
			wantStatus: "error",
			wantErr:    "complete failed",
		},
		{
			name:            "exact existing relationship is rejected without failing cycle",
			cfg:             domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC"},
			repo:            &dreamRepositoryStub{inputs: []repository.DreamInput{firstInput, secondInput}, predicates: predicates, upsertErr: repository.ErrDreamExactRelationshipExists},
			wantStatus:      "completed",
			wantComplete:    "completed",
			wantRejected:    1,
			wantUpsertCount: 1,
		},
		{
			name:            "unexpected upsert error fails cycle and records failed completion",
			cfg:             domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC"},
			repo:            &dreamRepositoryStub{inputs: []repository.DreamInput{firstInput, secondInput}, predicates: predicates, upsertErr: errors.New("write failed")},
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
				Generator: generator,
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

func TestResolveFeedbackErrorBranches(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	hypothesisID := uuid.NewString()
	record := repository.HypothesisRecord{
		TeamID:             teamID.String(),
		HypothesisID:       hypothesisID,
		CreatedByProfileID: ownerID.String(),
		Status:             string(domain.DreamStatusProposed),
		Statement:          "Dense-Mem may use PostgreSQL.",
		CreatedAt:          time.Now().UTC(),
		UpdatedAt:          time.Now().UTC(),
	}
	ctx := dreamTestContext(teamID, ownerID)

	svc := New(Dependencies{
		Store:     &dreamRepositoryStub{getErr: repository.ErrDreamHypothesisNotFound},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
	})
	_, err := svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{DreamID: hypothesisID, Decision: "reject"})
	require.ErrorIs(t, err, ErrDreamNotFound)

	svc = New(Dependencies{
		Store:     &dreamRepositoryStub{getRecord: record},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
	})
	_, err = svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_true",
		Evidence: []memoryservice.RememberEvidenceInput{{
			Content: "The deployment note says Dense-Mem uses PostgreSQL.",
		}},
	})
	require.ErrorContains(t, err, "remember service is required")

	svc = New(Dependencies{
		Store: &dreamRepositoryStub{
			getRecord: record,
			updateErr: repository.ErrDreamHypothesisNotFound,
		},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
	})
	_, err = svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{DreamID: hypothesisID, Decision: "reject"})
	require.ErrorIs(t, err, ErrDreamNotFound)

	svc = New(Dependencies{
		Store:     &dreamRepositoryStub{getRecord: record},
		Remember:  &rememberServiceStub{err: errors.New("remember failed")},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
	})
	_, err = svc.ResolveFeedback(ctx, "ignored-profile", ResolveFeedbackRequest{
		DreamID:  hypothesisID,
		Decision: "confirm_false",
		Evidence: []memoryservice.RememberEvidenceInput{{
			Content: "The deployment note says Dense-Mem does not use PostgreSQL.",
		}},
	})
	require.ErrorContains(t, err, "remember failed")
}

func TestStatusAndHelperEdgeCases(t *testing.T) {
	teamID := uuid.New()
	ownerID := uuid.New()
	repo := &dreamRepositoryStub{}
	svc := New(Dependencies{
		Store:     repo,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true}},
	})
	ctx := dreamTestContext(teamID, ownerID)

	runs, err := svc.ListRuns(ctx, "ignored-profile", 5)
	require.NoError(t, err)
	assert.Empty(t, runs)

	status, err := svc.Status(ctx, "ignored-profile")
	require.NoError(t, err)
	assert.Nil(t, status.LatestRun)
	assert.Equal(t, 0, status.PendingCount)

	assert.Equal(t, "relationship", dreamSourceType(repository.DreamInput{Status: "active"}))
	assert.Equal(t, "candidate_relationship", dreamSourceType(repository.DreamInput{Status: "pending_evidence"}))
	assert.Equal(t, "relationship", dreamSourceType(repository.DreamInput{}))
	assert.Equal(t, "from stringer", anyString(testStringer("from stringer")))
	require.Nil(t, optionalProbability(0))
	require.NotNil(t, optionalProbability(2))
	assert.Equal(t, 1.0, *optionalProbability(2))
}
