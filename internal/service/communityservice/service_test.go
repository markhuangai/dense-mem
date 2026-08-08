package communityservice

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/community"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestRunScheduledPublishesValidatedSnapshot(t *testing.T) {
	teamID := uuid.NewString()
	now := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	store := &communityServiceStoreStub{inputs: communityServiceInputs(), runID: uuid.NewString()}
	provider := &communitySummaryProviderStub{}
	svc := New(Dependencies{Store: store, Summary: provider, Now: func() time.Time { return now }})

	result, err := svc.RunScheduled(context.Background(), teamID, now)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "completed", result.Status)
	require.NotEmpty(t, result.RunID)
	require.NotEmpty(t, result.SourceFingerprint)
	require.GreaterOrEqual(t, result.CommunityCount, 1)
	require.Equal(t, result.CommunityCount, len(store.published.Communities))
	require.Equal(t, provider.calls, len(store.attempts))
	require.GreaterOrEqual(t, provider.calls, 1)
	for _, attempt := range store.attempts {
		require.True(t, attempt.Valid)
		require.NotEmpty(t, attempt.InputHash)
	}
	require.Empty(t, store.completed.Error)
	require.Equal(t, "completed", store.completed.Status)
}

func TestRunScheduledRetriesInvalidSummaryAndPublishesNothing(t *testing.T) {
	teamID := uuid.NewString()
	now := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	store := &communityServiceStoreStub{inputs: communityServiceInputs(), runID: uuid.NewString()}
	provider := &communitySummaryProviderStub{invalid: true}
	svc := New(Dependencies{Store: store, Summary: provider, Now: func() time.Time { return now }})

	result, err := svc.RunScheduled(context.Background(), teamID, now)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "failed", result.Status)
	require.Equal(t, "community summary generation failed", result.Error)
	require.Empty(t, store.published.Communities)
	require.GreaterOrEqual(t, provider.calls, maxSummaryRunAttempts)
	require.Len(t, store.attempts, provider.calls)
	for _, attempt := range store.attempts {
		require.False(t, attempt.Valid)
		require.NotEmpty(t, attempt.ErrorCode)
	}
	require.Equal(t, "failed", store.completed.Status)
}

func TestRunScheduledSkipsUnchangedWindow(t *testing.T) {
	teamID := uuid.NewString()
	now := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	inputs := communityServiceInputs()
	configurationHash := community.ConfigurationHash(community.DefaultSeed)
	fingerprint, err := fingerprintInputs(inputs, configurationHash, "model-v1")
	require.NoError(t, err)
	store := &communityServiceStoreStub{
		inputs: inputs,
		latest: &repository.CommunityRun{
			TeamID: teamID, RunID: uuid.NewString(), WindowKey: now.Format("2006-01-02"),
			Status: "completed", SourceFingerprint: fingerprint,
		},
	}
	provider := &communitySummaryProviderStub{}
	svc := New(Dependencies{Store: store, Summary: provider, Now: func() time.Time { return now }})

	result, err := svc.RunScheduled(context.Background(), teamID, now)

	require.NoError(t, err)
	require.Equal(t, "skipped", result.Status)
	require.Equal(t, 0, store.claims)
	require.Zero(t, provider.calls)
}

func TestRunScheduledRejectsInvalidSetupAndUnclaimedRuns(t *testing.T) {
	_, err := New(Dependencies{}).RunScheduled(context.Background(), uuid.NewString(), time.Now())
	require.ErrorContains(t, err, "repository is required")

	store := &communityServiceStoreStub{inputs: communityServiceInputs(), claimResult: &repository.CommunityRun{
		TeamID: uuid.NewString(), RunID: uuid.NewString(), Status: "running", Claimed: false,
	}}
	svc := New(Dependencies{Store: store, Summary: &communitySummaryProviderStub{}})
	_, err = svc.RunScheduled(context.Background(), "not-a-uuid", time.Now())
	require.ErrorContains(t, err, "invalid team id")

	result, err := svc.RunScheduled(context.Background(), uuid.NewString(), time.Now())
	require.NoError(t, err)
	require.Equal(t, "skipped", result.Status)
	_, err = svc.RunScheduled(context.Background(), uuid.NewString(), time.Time{})
	require.NoError(t, err)
}

func TestRunScheduledReportsProviderAndPublicationFailures(t *testing.T) {
	teamID := uuid.NewString()
	now := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	tests := []struct {
		name     string
		provider *communitySummaryProviderStub
		store    *communityServiceStoreStub
	}{
		{name: "provider error", provider: &communitySummaryProviderStub{providerErr: errors.New("provider unavailable")}, store: &communityServiceStoreStub{inputs: communityServiceInputs()}},
		{name: "attempt persistence error", provider: &communitySummaryProviderStub{invalid: true}, store: &communityServiceStoreStub{inputs: communityServiceInputs(), attemptErr: errors.New("attempt write failed")}},
		{name: "publication error", provider: &communitySummaryProviderStub{}, store: &communityServiceStoreStub{inputs: communityServiceInputs(), publishErr: errors.New("publish failed")}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			svc := New(Dependencies{Store: testCase.store, Summary: testCase.provider, Now: func() time.Time { return now }})
			result, err := svc.RunScheduled(context.Background(), teamID, now)
			require.NoError(t, err)
			require.Equal(t, "failed", result.Status)
			require.Equal(t, "failed", testCase.store.completed.Status)
		})
	}
}

func TestStatusReturnsExactCommunityCount(t *testing.T) {
	teamID := uuid.NewString()
	store := &communityServiceStoreStub{count: 37}
	svc := New(Dependencies{Store: store})

	result, err := svc.Status(context.Background(), teamID)

	require.NoError(t, err)
	require.Equal(t, 37, result.CurrentCommunityCount)
}

func TestStatusLoadsConfigAndPropagatesConfigErrors(t *testing.T) {
	teamID := uuid.NewString()
	latest := &repository.CommunityRun{TeamID: teamID, RunID: uuid.NewString(), Status: "failed"}
	store := &communityServiceStoreStub{latest: latest, count: 4}
	config := schedulerConfigStub{cfg: domain.CommunityDetectionRuntimeConfig{Enabled: true, StartTimeLocal: "03:00", Timezone: "UTC"}}
	svc := New(Dependencies{Store: store, AppConfig: config})
	result, err := svc.Status(context.Background(), teamID)
	require.NoError(t, err)
	require.True(t, result.EffectiveConfig.Enabled)
	require.Equal(t, latest.RunID, result.LatestRun.RunID)

	failed := New(Dependencies{Store: store, AppConfig: schedulerConfigStub{err: errors.New("config unavailable")}})
	_, err = failed.Status(context.Background(), teamID)
	require.ErrorContains(t, err, "config unavailable")
	countFailure := New(Dependencies{Store: &communityServiceStoreStub{countErr: errors.New("count unavailable")}})
	_, err = countFailure.Status(context.Background(), teamID)
	require.ErrorContains(t, err, "count unavailable")
}

func TestValidateCommunitySummaryResponseRejectsIncompleteResponses(t *testing.T) {
	relationships := []domain.CommunitySummaryRelationship{{
		RelationshipID: "relationship-1", EvidenceIDs: []string{"evidence-1"},
		SupportQuotes: []domain.CommunitySummarySupportQuote{{EvidenceID: "evidence-1", Quote: "exact quote"}},
		Subject:       "Dense-Mem", Predicate: "uses", Object: "PostgreSQL",
	}}
	base := domain.CommunitySummary{Summary: "A grounded summary.", InputHash: "input-hash"}
	cases := []struct {
		name   string
		mutate func(*domain.CommunitySummary)
		want   string
	}{
		{name: "empty summary", mutate: func(response *domain.CommunitySummary) { response.Summary = " " }, want: "summary_empty"},
		{name: "summary too long", mutate: func(response *domain.CommunitySummary) { response.Summary = strings.Repeat("x", 4001) }, want: "summary_too_long"},
		{name: "input hash mismatch", mutate: func(response *domain.CommunitySummary) { response.InputHash = "other" }, want: "input_hash_mismatch"},
		{name: "relationship not allowlisted", mutate: func(response *domain.CommunitySummary) { response.AdmittedRelationshipIDs = []string{"other"} }, want: "admitted_relationship_ids_not_allowlisted"},
		{name: "evidence not allowlisted", mutate: func(response *domain.CommunitySummary) { response.AdmittedEvidenceIDs = []string{"other"} }, want: "admitted_evidence_ids_not_allowlisted"},
		{name: "duplicate relationship", mutate: func(response *domain.CommunitySummary) {
			response.AdmittedRelationshipIDs = []string{"relationship-1", "relationship-1"}
		}, want: "admitted_relationship_ids_not_unique"},
		{name: "duplicate evidence", mutate: func(response *domain.CommunitySummary) {
			response.AdmittedEvidenceIDs = []string{"evidence-1", "evidence-1"}
		}, want: "admitted_evidence_ids_not_unique"},
		{name: "quote not exact", mutate: func(response *domain.CommunitySummary) {
			response.AdmittedSupportQuotes = []domain.CommunitySummarySupportQuote{{EvidenceID: "evidence-1", Quote: "rewritten"}}
		}, want: "admitted_support_quotes_not_exact"},
		{name: "top entity not allowlisted", mutate: func(response *domain.CommunitySummary) { response.TopEntities = []string{"Other"} }, want: "top_entities_not_allowlisted"},
		{name: "top predicate not allowlisted", mutate: func(response *domain.CommunitySummary) { response.TopPredicates = []string{"stores"} }, want: "top_predicates_not_allowlisted"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			response := base
			testCase.mutate(&response)
			require.Equal(t, testCase.want, validateCommunitySummaryResponse(response, "input-hash", relationships))
		})
	}

	valid := base
	valid.AdmittedRelationshipIDs = []string{"relationship-1"}
	valid.AdmittedEvidenceIDs = []string{"evidence-1"}
	valid.AdmittedSupportQuotes = append([]domain.CommunitySummarySupportQuote(nil), relationships[0].SupportQuotes...)
	valid.TopEntities = []string{"Dense-Mem", "PostgreSQL"}
	valid.TopPredicates = []string{"uses"}
	require.Empty(t, validateCommunitySummaryResponse(valid, "input-hash", relationships))
}

func TestBoundedSummaryRelationshipsDeduplicatesAndBoundsEvidence(t *testing.T) {
	longQuote := strings.Repeat("q", maxSummaryQuoteRunes+10)
	inputs := make([]repository.CommunityInput, 101)
	for index := range inputs {
		inputs[index] = repository.CommunityInput{
			RelationshipID: uuid.NewString(), SubjectName: "Dense-Mem", PredicateKey: "uses", ObjectName: "PostgreSQL",
			EvidenceIDs: []string{uuid.NewString(), uuid.NewString()},
		}
	}
	inputs[0].EvidenceIDs = []string{"evidence-1", "evidence-1", " "}
	inputs[0].EvidenceQuotes = []domain.CommunitySummarySupportQuote{
		{EvidenceID: "evidence-1", Quote: longQuote},
		{EvidenceID: "evidence-1", Quote: "duplicate"},
		{EvidenceID: "evidence-2", Quote: "second quote"},
		{EvidenceID: "evidence-3", Quote: "third quote"},
		{EvidenceID: "evidence-4", Quote: "fourth quote"},
	}

	relationships := boundedSummaryRelationships(inputs)
	require.Len(t, relationships, maxSummaryRelationships)
	require.Len(t, relationships[0].EvidenceIDs, 3)
	require.Len(t, relationships[0].SupportQuotes, maxSummaryQuotesPerRelationship)
	require.Len(t, []rune(relationships[0].SupportQuotes[0].Quote), maxSummaryQuoteRunes)
	evidenceCount := 0
	for _, relationship := range relationships {
		evidenceCount += len(relationship.EvidenceIDs)
	}
	require.Equal(t, maxSummaryEvidenceIDs, evidenceCount)
}

func TestCommunitySchedulerHelpersAndPublicRunErrors(t *testing.T) {
	now := time.Date(2026, 8, 8, 3, 0, 15, 0, time.UTC)
	require.Equal(t, 45*time.Second, nextMinuteDelay(now))
	require.Equal(t, time.Minute, nextMinuteDelay(now.Truncate(time.Minute)))
	require.Equal(t, deterministicJitter("team", now, 10), deterministicJitter("team", now, 10))
	require.LessOrEqual(t, deterministicJitter("team", now, 10), 10*time.Second)
	require.Zero(t, deterministicJitter("team", now, 0))
	require.LessOrEqual(t, deterministicJitter("team", now, int(^uint(0)>>1)), time.Duration(^uint64(0)>>1))
	require.True(t, dueAt(time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC), runtimeConfig{StartTimeLocal: "03:00", Timezone: "UTC"}))
	require.True(t, dueAt(time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC), runtimeConfig{StartTimeLocal: "invalid", Timezone: "not-a-timezone"}))

	completed := time.Date(2026, 8, 8, 3, 1, 0, 0, time.UTC)
	for _, status := range []string{"failed", "too_large", "cancelled", "skipped"} {
		result := runResultFromRecord(&repository.CommunityRun{Status: status, CompletedAt: &completed}, status)
		require.Equal(t, status, result.Status)
		if status == "skipped" {
			require.Empty(t, result.Error)
		} else {
			require.NotEmpty(t, result.Error)
		}
	}
	require.Nil(t, runResultFromRecord(nil, "skipped"))
	require.Len(t, boundedError(strings.Repeat("x", 1001)), 1000)
	require.Equal(t, "normal", boundedError("normal"))
	require.Equal(t, "fallback", firstNonEmpty(" ", "fallback"))
	require.Empty(t, firstNonEmpty(" ", ""))
	require.Equal(t, 2, summaryMinInt(2, 3))
	require.Equal(t, 3, summaryMinInt(4, 3))
	require.Equal(t, time.Time{}, derefTime(nil))
	require.Equal(t, 1.0, jaccard([]string{"a"}, []string{"a", "a"}))
	require.Equal(t, 1.0, jaccard(nil, nil))

	cluster := community.Cluster{CommunityID: uuid.New(), GroupKeys: []string{"g1", "g2"}}
	lineage := []repository.CommunityLineageRecord{{LogicalCommunityID: "logical-1", GroupKeys: []string{"g1", "g2"}}}
	require.Equal(t, "logical-1", matchLogicalIDs([]community.Cluster{cluster}, lineage)["g1\x00g2"])
	newLogical := matchLogicalIDs([]community.Cluster{{CommunityID: uuid.New(), GroupKeys: []string{"new"}}}, lineage)
	require.NotEmpty(t, newLogical["new"])
	require.Len(t, topPredicates([]repository.CommunityInput{
		{PredicateKey: "a"}, {PredicateKey: "b"}, {PredicateKey: "c"}, {PredicateKey: "d"}, {PredicateKey: "e"}, {PredicateKey: "f"},
	}), 5)
	require.Equal(t, uuid.Nil, uuidString("invalid"))
}

func TestCommunitySummarySupportQuoteHelpers(t *testing.T) {
	relationships := []domain.CommunitySummaryRelationship{{
		SupportQuotes: []domain.CommunitySummarySupportQuote{
			{EvidenceID: "e1", Quote: "same"},
			{EvidenceID: "e1", Quote: "different"},
			{EvidenceID: "e2", Quote: "second"},
		},
	}}
	quotes := supportQuotes(relationships)
	require.Len(t, quotes, 2)
	require.True(t, validSupportQuotes(quotes, quotes))
	require.False(t, validSupportQuotes(append(quotes, quotes[0]), quotes))
	require.False(t, validSupportQuotes([]domain.CommunitySummarySupportQuote{{EvidenceID: "unknown", Quote: "x"}}, quotes))
	require.False(t, uniqueStrings([]string{"duplicate", "duplicate"}))
	require.False(t, uniqueStrings([]string{""}))
	require.True(t, uniqueStrings(nil))
}

func TestCommunitySchedulerSkipsBeforeLaunchingRuns(t *testing.T) {
	ctx := context.Background()
	scheduler := NewScheduler(nil, nil, nil, nil)
	scheduler.Start(ctx)

	configErr := errors.New("config unavailable")
	for _, config := range []schedulerConfigStub{
		{err: configErr},
		{cfg: domain.CommunityDetectionRuntimeConfig{Enabled: false, StartTimeLocal: "03:00", Timezone: "UTC"}},
		{cfg: domain.CommunityDetectionRuntimeConfig{Enabled: true, StartTimeLocal: "03:00", Timezone: "UTC"}},
	} {
		candidate := NewScheduler(&schedulerServiceStub{started: make(chan struct{})}, schedulerProfilesStub{}, config, nil)
		candidate.now = func() time.Time { return time.Date(2026, 8, 8, 4, 0, 0, 0, time.UTC) }
		candidate.runDue(ctx)
	}

	profilesErr := &schedulerProfilesStub{err: errors.New("profiles unavailable")}
	failedListing := NewScheduler(&schedulerServiceStub{started: make(chan struct{})}, profilesErr, schedulerConfigStub{
		cfg: domain.CommunityDetectionRuntimeConfig{Enabled: true, StartTimeLocal: "03:00", Timezone: "UTC", MaxConcurrency: 9},
	}, nil)
	failedListing.now = func() time.Time { return time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC) }
	failedListing.runDue(ctx)

	boundary := make(chan time.Time, 1)
	boundary <- time.Now()
	boundaryScheduler := NewScheduler(&schedulerServiceStub{started: make(chan struct{})}, schedulerProfilesStub{}, schedulerConfigStub{}, nil)
	require.True(t, boundaryScheduler.waitForMinuteBoundary(ctx, boundary))

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	require.False(t, boundaryScheduler.waitForMinuteBoundary(canceled, make(chan time.Time)))
}

type communityServiceStoreStub struct {
	repository.CommunityRepository
	inputs      []repository.CommunityInput
	latest      *repository.CommunityRun
	runID       string
	claims      int
	attempts    []repository.CommunitySummaryAttemptInput
	published   repository.CommunitySnapshotPublishInput
	completed   repository.CommunityRunCompleteInput
	count       int
	countErr    error
	listErr     error
	attemptErr  error
	publishErr  error
	claimResult *repository.CommunityRun
	completeErr error
}

func (s *communityServiceStoreStub) ListCommunityInputs(_ context.Context, _ repository.CommunityInputListInput) ([]repository.CommunityInput, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return append([]repository.CommunityInput(nil), s.inputs...), nil
}

func (s *communityServiceStoreStub) LatestCommunityRun(_ context.Context, _ string) (*repository.CommunityRun, error) {
	return s.latest, nil
}

func (s *communityServiceStoreStub) ListCurrentCommunityLineage(context.Context, string) ([]repository.CommunityLineageRecord, error) {
	return nil, nil
}

func (s *communityServiceStoreStub) ClaimCommunityRun(_ context.Context, input repository.CommunityRunClaimInput) (*repository.CommunityRun, error) {
	s.claims++
	if s.claimResult != nil {
		return s.claimResult, nil
	}
	runID := s.runID
	if runID == "" {
		runID = uuid.NewString()
	}
	return &repository.CommunityRun{
		TeamID: input.TeamID, RunID: runID, WindowKey: input.WindowKey, Status: "running",
		AlgorithmKind: input.AlgorithmKind, AlgorithmVersion: input.AlgorithmVersion,
		ProfileVersion: input.ProfileVersion, ConfigurationHash: input.ConfigurationHash,
		SourceFingerprint: input.SourceFingerprint, StartedAt: time.Now().UTC(), Claimed: true,
	}, nil
}

func (s *communityServiceStoreStub) RenewCommunityRunLease(context.Context, repository.CommunityRunLeaseInput) error {
	return nil
}

func (s *communityServiceStoreStub) RecordCommunitySummaryAttempt(_ context.Context, input repository.CommunitySummaryAttemptInput) error {
	if s.attemptErr != nil {
		return s.attemptErr
	}
	s.attempts = append(s.attempts, input)
	return nil
}

func (s *communityServiceStoreStub) PublishCommunitySnapshot(_ context.Context, input repository.CommunitySnapshotPublishInput) error {
	if s.publishErr != nil {
		return s.publishErr
	}
	s.published = input
	s.completed = repository.CommunityRunCompleteInput{TeamID: input.TeamID, RunID: input.RunID, Status: "completed", NodeCount: input.NodeCount, EdgeCount: input.EdgeCount, CommunityCount: len(input.Communities)}
	return nil
}

func (s *communityServiceStoreStub) CompleteCommunityRun(_ context.Context, input repository.CommunityRunCompleteInput) error {
	s.completed = input
	return s.completeErr
}

func (s *communityServiceStoreStub) CountCurrentCommunities(context.Context, string) (int, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	return s.count, nil
}

type communitySummaryProviderStub struct {
	invalid     bool
	providerErr error
	calls       int
}

func (s *communitySummaryProviderStub) ModelName() string { return "model-v1" }

func (s *communitySummaryProviderStub) SummarizeCommunity(_ context.Context, input domain.CommunitySummaryInput) (domain.CommunitySummary, error) {
	s.calls++
	if s.providerErr != nil {
		return domain.CommunitySummary{}, s.providerErr
	}
	response := domain.CommunitySummary{Summary: "The community describes durable memory.", InputHash: input.SummaryInputHash, ProviderModel: "model-v1"}
	for _, relationship := range input.Relationships {
		response.AdmittedRelationshipIDs = append(response.AdmittedRelationshipIDs, relationship.RelationshipID)
		response.AdmittedEvidenceIDs = append(response.AdmittedEvidenceIDs, relationship.EvidenceIDs...)
	}
	if s.invalid {
		response.InputHash = "sha256:invalid"
	}
	return response, nil
}

func communityServiceInputs() []repository.CommunityInput {
	entity1, entity2, entity3, entity4 := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	sharedEvidence, otherEvidence := uuid.NewString(), uuid.NewString()
	return []repository.CommunityInput{
		{
			RelationshipID: uuid.NewString(), SemanticGroupKey: "g1", SubjectEntityID: entity1, ObjectEntityID: entity2,
			SubjectName: "Dense-Mem", PredicateKey: "uses", ObjectName: "PostgreSQL", EvidenceIDs: []string{sharedEvidence}, Version: 1,
		},
		{
			RelationshipID: uuid.NewString(), SemanticGroupKey: "g2", SubjectEntityID: entity1, ObjectEntityID: entity4,
			SubjectName: "Dense-Mem", PredicateKey: "stores", ObjectName: "memory", EvidenceIDs: []string{otherEvidence}, Version: 1,
		},
		{
			RelationshipID: uuid.NewString(), SemanticGroupKey: "g3", SubjectEntityID: entity2, ObjectEntityID: entity3,
			SubjectName: "PostgreSQL", PredicateKey: "supports", ObjectName: "Dense-Mem", EvidenceIDs: []string{sharedEvidence}, Version: 1,
		},
	}
}

var _ repository.CommunityRepository = (*communityServiceStoreStub)(nil)
var _ SummaryProvider = (*communitySummaryProviderStub)(nil)
