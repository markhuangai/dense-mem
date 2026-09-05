package dreamservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/repository"
)

func TestScheduledEvidenceCycleBoundsTargetsContextsAndRelatedRecords(t *testing.T) {
	teamID := uuid.NewString()
	store := &dreamRepositoryStub{
		inputs:      make([]repository.DreamInput, evidenceDiscoveryRelatedLimit+2),
		listRecords: make([]repository.HypothesisRecord, evidenceDiscoveryRelatedLimit+2),
	}
	for index := range store.inputs {
		store.inputs[index] = repository.DreamInput{RelationshipID: uuid.NewString(), Status: "active"}
	}
	for index := range store.listRecords {
		store.listRecords[index] = repository.HypothesisRecord{HypothesisID: uuid.NewString(), Status: string(domain.DreamStatusProposed)}
	}
	targets := make([]repository.EvidenceDiscoveryTargetInput, evidenceDiscoveryTargetLimit+3)
	for index := range targets {
		id := uuid.NewString()
		targets[index] = repository.EvidenceDiscoveryTargetInput{
			Target:   repository.EvidenceTarget{EvidenceID: id, FragmentID: id, ContentHash: "hash-" + id, Content: "target evidence", Authority: "primary", SourceGroupKey: "source-a"},
			Contexts: make([]repository.EvidenceContext, evidenceDiscoveryContextLimit+2),
		}
		for contextIndex := range targets[index].Contexts {
			targets[index].Contexts[contextIndex] = repository.EvidenceContext{EvidenceID: uuid.NewString(), Content: "context evidence", Authority: "secondary"}
		}
	}
	evidenceStore := &evidenceRepositoryStub{targets: targets}
	generator := &evidenceGeneratorStub{model: "evidence-model", generated: []GeneratedDream{{
		Hypothesis: "A may relate to B.", SubjectEntityID: uuid.NewString(), PredicateKey: "relates", PredicateVersion: 1,
		ObjectEntityID: uuid.NewString(), EvidenceDerivations: []repository.EvidenceDerivationSource{{EvidenceID: targets[0].Target.EvidenceID, FragmentID: targets[0].Target.FragmentID, SourceGroupKey: "source-a", SpanStart: 0, SpanEnd: 6, Quote: "target", Authority: "primary"}},
	}}}
	svc := New(Dependencies{
		Store: store, ScheduledStore: store, EvidenceStore: evidenceStore, EvidenceGenerator: generator,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC", StartTimeLocal: "03:00"}},
		Now:       func() time.Time { return time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC) },
	}).(*service)

	result, err := svc.RunScheduledEvidenceCycle(context.Background(), teamID, time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, evidenceDiscoveryTargetLimit, result.EvidenceTargets)
	require.Len(t, generator.requests, evidenceDiscoveryTargetLimit*evidenceDiscoveryPassLimit)
	require.Len(t, evidenceStore.evaluations, evidenceDiscoveryTargetLimit*evidenceDiscoveryPassLimit)
	for _, request := range generator.requests {
		require.LessOrEqual(t, len(request.Contexts), evidenceDiscoveryContextLimit)
		require.LessOrEqual(t, len(request.RelatedRelationships)+len(request.RelatedHypotheses), evidenceDiscoveryRelatedLimit)
	}
	require.Equal(t, evidenceDiscoveryTargetLimit, evidenceStore.lastLimit)
	require.Equal(t, evidenceDiscoveryContextLimit, evidenceStore.lastMaxContexts)
	require.Zero(t, evidenceStore.validatedCalls, "evaluation persistence owns the validated attempt transition")
}

func TestScheduledEvidenceCycleSurfacesProviderFailureAndCompletesFailedRun(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	store := &dreamRepositoryStub{}
	evidenceStore := &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{{
		Target: repository.EvidenceTarget{EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary"},
	}}}
	providerErr := errors.New("provider unavailable")
	svc := New(Dependencies{
		Store: store, ScheduledStore: store, EvidenceStore: evidenceStore,
		EvidenceGenerator: &evidenceGeneratorStub{model: "evidence-model", err: providerErr},
		AppConfig:         cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC", StartTimeLocal: "03:00"}},
	}).(*service)

	result, err := svc.RunScheduledEvidenceCycle(context.Background(), teamID, time.Date(2026, 9, 4, 4, 0, 0, 0, time.UTC))
	require.ErrorIs(t, err, providerErr)
	require.Equal(t, "error", result.Status)
	require.Equal(t, "failed", store.completeInput.Status)
}

func TestScheduledEvidenceCycleSkipsInactiveTeamBeforeClaim(t *testing.T) {
	teamID := uuid.New()
	store := &dreamRepositoryStub{}
	svc := New(Dependencies{
		Store: store, ScheduledStore: store, Teams: &evidenceTeamServiceStub{team: &domain.Team{ID: teamID, Status: "archived"}},
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC", StartTimeLocal: "03:00"}},
	}).(*service)

	result, err := svc.RunScheduledEvidenceCycle(context.Background(), teamID.String(), time.Date(2026, 9, 4, 7, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, "skipped", result.Status)
	require.Empty(t, store.claimInput.TeamID, "inactive teams must not claim an evidence run")
	recovered, err := svc.RecoverScheduledEvidenceCycle(context.Background(), teamID.String())
	require.NoError(t, err)
	require.Nil(t, recovered, "inactive teams must not claim evidence recovery")
}

func TestScheduledEvidenceCycleAcceptsMidHourAndSkipsDisabledWindows(t *testing.T) {
	teamID := uuid.NewString()
	store := &dreamRepositoryStub{}
	service := New(Dependencies{
		Store: store, ScheduledStore: store, EvidenceStore: &evidenceRepositoryStub{},
		EvidenceGenerator: &evidenceGeneratorStub{model: "evidence-model"},
		AppConfig:         cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC", StartTimeLocal: "03:00"}},
	}).(*service)
	nonHourly, err := service.RunScheduledEvidenceCycle(context.Background(), teamID, time.Date(2026, 9, 4, 3, 1, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, "completed", nonHourly.Status)
	require.Equal(t, teamID, store.claimInput.TeamID)

	store.claimInput = repository.DreamCycleClaimInput{}
	service.deps.AppConfig = cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: false, MaxOutputs: 5, Timezone: "UTC", StartTimeLocal: "03:00"}}
	disabled, err := service.RunScheduledEvidenceCycle(context.Background(), teamID, time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, "skipped", disabled.Status)
	require.Empty(t, store.claimInput.TeamID)
}

func TestRecoverScheduledEvidenceCycleCompletesDisabledRun(t *testing.T) {
	teamID := uuid.NewString()
	leaseToken := uuid.NewString()
	store := &dreamRepositoryStub{recoveryRun: &repository.DreamCycleRun{
		TeamID: teamID, RunID: uuid.NewString(), LeaseToken: leaseToken, Status: "running", Claimed: true,
	}}
	service := New(Dependencies{
		Store: store, ScheduledStore: store,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: false, MaxOutputs: 5, Timezone: "UTC", StartTimeLocal: "03:00"}},
	}).(*service)
	result, err := service.RecoverScheduledEvidenceCycle(context.Background(), teamID)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "cancelled", result.Status)
	require.Equal(t, "cancelled", store.completeInput.Status)
}

func TestEvidenceTeamActivityDefaultsAndRejectsArchivedTeams(t *testing.T) {
	teamID := uuid.New()
	service := &service{}
	active, err := service.evidenceTeamIsActive(context.Background(), teamID.String())
	require.NoError(t, err)
	require.True(t, active)
	service.deps.Teams = &evidenceTeamServiceStub{}
	active, err = service.evidenceTeamIsActive(context.Background(), teamID.String())
	require.NoError(t, err)
	require.False(t, active)
	service.deps.Teams = &evidenceTeamServiceStub{team: &domain.Team{ID: teamID, Status: "ARCHIVED"}}
	active, err = service.evidenceTeamIsActive(context.Background(), teamID.String())
	require.NoError(t, err)
	require.False(t, active)
}

func TestClaimedEvidenceCycleSurfacesStoreAndProviderErrors(t *testing.T) {
	teamID := uuid.NewString()
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}
	baseConfig := EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5}}
	cases := []struct {
		name string
		deps Dependencies
	}{
		{name: "missing dependencies", deps: Dependencies{ScheduledStore: &dreamRepositoryStub{}}},
		{name: "target list", deps: Dependencies{Store: &dreamRepositoryStub{}, EvidenceStore: &evidenceRepositoryStub{targetsErr: errors.New("target list failed")}, EvidenceGenerator: &evidenceGeneratorStub{model: "model"}}},
		{name: "relationship list", deps: Dependencies{Store: &dreamRepositoryStub{listInputsErr: errors.New("relationship list failed")}, EvidenceStore: &evidenceRepositoryStub{}, EvidenceGenerator: &evidenceGeneratorStub{model: "model"}}},
		{name: "provider model", deps: Dependencies{Store: &dreamRepositoryStub{}, EvidenceStore: &evidenceRepositoryStub{}, EvidenceGenerator: &evidenceGeneratorStub{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, ok := tc.deps.Store.(*dreamRepositoryStub)
			if !ok {
				store = &dreamRepositoryStub{}
				tc.deps.Store = store
			}
			if tc.deps.ScheduledStore == nil {
				tc.deps.ScheduledStore = store
			}
			svc := &service{deps: tc.deps, now: time.Now}
			result, err := svc.runClaimedEvidenceCycle(context.Background(), teamID, baseConfig, &RunCycleResult{TeamID: teamID}, claimed)
			require.Error(t, err)
			require.Equal(t, "error", result.Status)
		})
	}
}

func TestFinishEvidenceCycleSurfacesRunAndCompletionErrors(t *testing.T) {
	teamID := uuid.NewString()
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}
	now := time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC)
	store := &dreamRepositoryStub{}
	service := &service{deps: Dependencies{ScheduledStore: store}, now: func() time.Time { return now }}
	result, err := service.finishEvidenceCycle(context.Background(), teamID, EffectiveConfig{}, &RunCycleResult{}, claimed, 0, 0, 0, 0, 0, errors.New("pq: password=secret cycle failed"))
	require.ErrorContains(t, err, "cycle failed")
	require.Equal(t, "error", result.Status)
	require.Equal(t, "evidence discovery cycle failed", result.Error)
	require.Equal(t, result.Error, store.completeInput.Error)
	require.NotContains(t, result.Error, "password")

	store.completeErr = errors.New("completion failed")
	result, err = service.finishEvidenceCycle(context.Background(), teamID, EffectiveConfig{}, &RunCycleResult{}, claimed, 0, 0, 0, 0, 0, nil)
	require.ErrorContains(t, err, "completion failed")
	require.Equal(t, "error", result.Status)
	require.Equal(t, "evidence discovery cycle failed", result.Error)
	_, err = service.finishEvidenceCycle(context.Background(), teamID, EffectiveConfig{}, &RunCycleResult{}, nil, 0, 0, 0, 0, 0, nil)
	require.Error(t, err)
}

func TestFinishEvidenceCycleMarksProviderFailureWithoutLeakingDetails(t *testing.T) {
	teamID := uuid.NewString()
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}
	store := &dreamRepositoryStub{}
	service := &service{deps: Dependencies{ScheduledStore: store}, now: time.Now}
	providerErr := &modelprovider.ProviderError{
		Provider: "fixture", Message: "connection reset", FailureClass: modelprovider.ProviderFailureClassTransport,
	}
	result, err := service.finishEvidenceCycle(context.Background(), teamID, EffectiveConfig{}, &RunCycleResult{}, claimed, 0, 0, 0, 0, 1, providerErr)
	require.ErrorIs(t, err, providerErr)
	require.Equal(t, "evidence discovery provider unavailable", result.Error)
	require.Equal(t, 1, result.OutcomeSummary["provider_failed"])
	require.Equal(t, result.Error, store.completeInput.Error)
	require.NotContains(t, result.Error, "connection reset")
}

func TestEvidenceRelatedSelectionUsesSharedFiveRecordCap(t *testing.T) {
	target := repository.EvidenceTarget{SourceGroupKey: "source-a"}
	relationships := make([]repository.DreamInput, 8)
	for index := range relationships {
		relationships[index] = repository.DreamInput{RelationshipID: string(rune('a' + index)), Status: "active", Evidence: []repository.DreamEvidence{{SourceGroupKey: "source-a"}}}
	}
	hypotheses := make([]repository.HypothesisRecord, 8)
	for index := range hypotheses {
		hypotheses[index] = repository.HypothesisRecord{HypothesisID: string(rune('a' + index)), Status: string(domain.DreamStatusProposed)}
	}
	relationships = selectEvidenceRelatedRelationships(target, relationships, evidenceDiscoveryRelatedLimit)
	hypotheses = selectEvidenceRelatedHypotheses(target, hypotheses, evidenceDiscoveryRelatedLimit-len(relationships))
	require.Len(t, relationships, evidenceDiscoveryRelatedLimit)
	require.Empty(t, hypotheses)
}

func TestEvidenceRelatedSelectionFiltersLifecycleAndBoundsHypotheses(t *testing.T) {
	target := repository.EvidenceTarget{SourceGroupKey: "source-a"}
	relationships := []repository.DreamInput{
		{RelationshipID: "same", Status: "active", Evidence: []repository.DreamEvidence{{SourceGroupKey: "source-a"}}},
		{RelationshipID: "other", Status: "active", Evidence: []repository.DreamEvidence{{SourceGroupKey: "source-b"}}},
		{RelationshipID: "inactive", Status: "inactive"},
		{RelationshipID: "", Status: "active"},
	}
	selected := selectEvidenceRelatedRelationships(target, relationships, 1)
	require.Equal(t, []string{"same"}, []string{selected[0].RelationshipID})
	require.Empty(t, selectEvidenceRelatedRelationships(target, relationships, 0))
	hypotheses := []repository.HypothesisRecord{
		{HypothesisID: "reinforced", Status: string(domain.DreamStatusReinforced)},
		{HypothesisID: "proposed", Status: string(domain.DreamStatusProposed)},
		{HypothesisID: "rejected", Status: string(domain.DreamStatusRejected)},
	}
	selectedHypotheses := selectEvidenceRelatedHypotheses(target, hypotheses, 1)
	require.Equal(t, "proposed", selectedHypotheses[0].HypothesisID)
	require.Empty(t, selectEvidenceRelatedHypotheses(target, hypotheses, 0))
}

func TestEvidenceProposalConversionRejectsInvalidAndKeepsDistinctEvidenceIDs(t *testing.T) {
	target := repository.EvidenceTarget{EvidenceID: "target", FragmentID: "target", Content: "target", Authority: "primary"}
	valid := GeneratedDream{Hypothesis: "A may use B.", SubjectEntityID: "subject", PredicateKey: "uses", ObjectEntityID: "object", EvidenceDerivations: []repository.EvidenceDerivationSource{
		{EvidenceID: "target", FragmentID: "target", SourceGroupKey: "source-a", SpanStart: 0, SpanEnd: 6, Quote: "target", Authority: "primary"},
		{EvidenceID: "context", FragmentID: "context", SourceGroupKey: "source-b", SpanStart: 0, SpanEnd: 7, Quote: "context", Authority: "secondary"},
		{EvidenceID: "context", FragmentID: "context", SourceGroupKey: "source-b", SpanStart: 8, SpanEnd: 12, Quote: "text", Authority: "secondary"},
	}}
	invalid := []GeneratedDream{
		{},
		{Hypothesis: "missing object", SubjectEntityID: "subject", PredicateKey: "uses"},
		{Hypothesis: "missing target citation", SubjectEntityID: "subject", PredicateKey: "uses", ObjectValueID: "value", EvidenceDerivations: []repository.EvidenceDerivationSource{{EvidenceID: "context", FragmentID: "context", SpanStart: 0, SpanEnd: 1, Quote: "c", Authority: "secondary"}}},
	}
	proposals, rejected := evidenceProposalsFromGenerated(append(invalid, valid), target, "model", 1)
	require.Len(t, proposals, 1)
	require.Equal(t, 3, rejected)
	require.Equal(t, []string{"target", "context"}, proposals[0].SourceEvidenceIDs)
	require.Equal(t, domain.DreamLaneEvidenceDiscovery, proposals[0].Lane)
	proposals, rejected = evidenceProposalsFromGenerated([]GeneratedDream{valid}, target, "model", 0)
	require.Len(t, proposals, 1)
	require.Zero(t, rejected)
}

func TestEvidenceProviderFailureAbandonmentIsConservative(t *testing.T) {
	require.True(t, evidenceProviderFailureCanAbandon(ErrDreamProviderUnavailable))
	require.True(t, evidenceProviderFailureCanAbandon(&modelprovider.MalformedResponseError{}))
	require.True(t, evidenceProviderFailureCanAbandon(&modelprovider.RateLimitError{}))
	require.True(t, evidenceProviderFailureCanAbandon(&modelprovider.ProviderError{FailureClass: modelprovider.ProviderFailureClassRequestInvalid}))
	require.True(t, evidenceProviderFailureCanAbandon(&modelprovider.ProviderError{FailureClass: modelprovider.ProviderFailureClassHTTPClient, StatusCode: 400}))
	require.True(t, evidenceProviderFailureCanAbandon(&modelprovider.ProviderError{FailureClass: modelprovider.ProviderFailureClassHTTPClient, StatusCode: 403}))
	require.False(t, evidenceProviderFailureCanAbandon(&modelprovider.ProviderError{FailureClass: modelprovider.ProviderFailureClassTimeout}))
	require.False(t, evidenceProviderFailureCanAbandon(&modelprovider.ProviderError{FailureClass: modelprovider.ProviderFailureClassHTTPServer, StatusCode: 503}))
	require.False(t, evidenceProviderFailureCanAbandon(&modelprovider.ProviderError{FailureClass: modelprovider.ProviderFailureClassHTTPClient}))
	require.False(t, evidenceProviderFailureCanAbandon(errors.New("ambiguous transport failure")))
}

func TestScheduledEvidenceCycleRejectsInvalidClaimsAndConfigurationFailures(t *testing.T) {
	teamID := uuid.NewString()
	window := time.Date(2026, 9, 4, 4, 0, 0, 0, time.UTC)
	baseConfig := cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC", StartTimeLocal: "03:00"}}

	svc := New(Dependencies{ScheduledStore: &dreamRepositoryStub{}, AppConfig: baseConfig}).(*service)
	_, err := svc.runScheduledEvidenceCycle(context.Background(), "not-a-uuid", window)
	require.Error(t, err)

	store := &dreamRepositoryStub{claimNil: true}
	svc = New(Dependencies{ScheduledStore: store, AppConfig: baseConfig}).(*service)
	_, err = svc.runScheduledEvidenceCycle(context.Background(), teamID, window)
	require.ErrorContains(t, err, "missing durable claim")

	store = &dreamRepositoryStub{run: repository.DreamCycleRun{RunID: uuid.NewString(), Claimed: false, Status: "running"}}
	svc = New(Dependencies{ScheduledStore: store, AppConfig: baseConfig}).(*service)
	result, err := svc.runScheduledEvidenceCycle(context.Background(), teamID, window)
	require.NoError(t, err)
	require.Equal(t, "skipped", result.Status)

	store = &dreamRepositoryStub{claimErr: errors.New("claim failed")}
	svc = New(Dependencies{ScheduledStore: store, AppConfig: baseConfig}).(*service)
	_, err = svc.runScheduledEvidenceCycle(context.Background(), teamID, window)
	require.ErrorContains(t, err, "claim failed")

	svc = New(Dependencies{ScheduledStore: &dreamRepositoryStub{}, AppConfig: errorAppConfigStub{err: errors.New("config failed")}}).(*service)
	_, err = svc.runScheduledEvidenceCycle(context.Background(), teamID, window)
	require.ErrorContains(t, err, "config failed")
}

func TestRecoverScheduledEvidenceCycleHandlesMissingStoreRecoveryAndTeamErrors(t *testing.T) {
	teamID := uuid.NewString()
	svc := &service{now: time.Now}
	_, err := svc.RecoverScheduledEvidenceCycle(context.Background(), teamID)
	require.ErrorContains(t, err, "scheduled dream repository is required")

	store := &dreamRepositoryStub{recoveryErr: errors.New("recovery failed")}
	svc = New(Dependencies{ScheduledStore: store, AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC", StartTimeLocal: "03:00"}}}).(*service)
	_, err = svc.RecoverScheduledEvidenceCycle(context.Background(), "not-a-uuid")
	require.Error(t, err)
	_, err = svc.RecoverScheduledEvidenceCycle(context.Background(), teamID)
	require.ErrorContains(t, err, "recovery failed")

	store = &dreamRepositoryStub{}
	svc = New(Dependencies{ScheduledStore: store, AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC", StartTimeLocal: "03:00"}}}).(*service)
	result, err := svc.RecoverScheduledEvidenceCycle(context.Background(), teamID)
	require.NoError(t, err)
	require.Nil(t, result)

	svc.deps.Teams = &errorTeamServiceStub{err: errors.New("team lookup failed")}
	_, err = svc.evidenceTeamIsActive(context.Background(), teamID)
	require.ErrorContains(t, err, "team lookup failed")
	_, err = svc.evidenceTeamIsActive(context.Background(), "not-a-uuid")
	require.Error(t, err)
}

func TestClaimedEvidenceCycleHandlesAttemptAndPersistenceFailures(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}
	target := repository.EvidenceDiscoveryTargetInput{Target: repository.EvidenceTarget{EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary"}}
	config := EffectiveConfig{DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 0}}
	newService := func(store *evidenceRepositoryStub, repo *dreamRepositoryStub, generator *evidenceGeneratorStub) *service {
		return &service{deps: Dependencies{Store: repo, ScheduledStore: repo, EvidenceStore: store, EvidenceGenerator: generator}, now: time.Now}
	}

	store := &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{target}, attemptPasses: map[string]int{targetID + ":hash": evidenceDiscoveryPassLimit}}
	repo := &dreamRepositoryStub{}
	generator := &evidenceGeneratorStub{model: "model"}
	result, err := newService(store, repo, generator).runClaimedEvidenceCycle(context.Background(), teamID, config, &RunCycleResult{}, claimed)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Empty(t, generator.requests)

	for _, tc := range []struct {
		name   string
		store  *evidenceRepositoryStub
		genErr error
	}{
		{name: "dispatch", store: &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{target}, dispatchedErr: errors.New("dispatch failed")}},
		{name: "persistence", store: &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{target}, persistErr: errors.New("persist failed")}},
		{name: "provider", store: &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{target}}, genErr: ErrDreamProviderUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &dreamRepositoryStub{}
			generator := &evidenceGeneratorStub{model: "model", err: tc.genErr}
			result, err := newService(tc.store, repo, generator).runClaimedEvidenceCycle(context.Background(), teamID, config, &RunCycleResult{}, claimed)
			require.Error(t, err)
			require.Equal(t, "error", result.Status)
		})
	}

	store = &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{target}}
	repo = &dreamRepositoryStub{}
	generator = &evidenceGeneratorStub{model: "model", generated: []GeneratedDream{{Hypothesis: "A may use B.", SubjectEntityID: "subject", PredicateKey: "uses", ObjectEntityID: "object", EvidenceDerivations: []repository.EvidenceDerivationSource{{EvidenceID: targetID, FragmentID: targetID, SpanStart: 0, SpanEnd: 6, Quote: "target", Authority: "primary"}}}}}
	_, err = newService(store, repo, generator).runClaimedEvidenceCycle(context.Background(), teamID, config, &RunCycleResult{}, claimed)
	require.NoError(t, err)
	require.Equal(t, 50, generator.requests[0].MaxOutputs)

	store = &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{target}, abandonErr: errors.New("abandon failed")}
	generator = &evidenceGeneratorStub{model: "model", err: ErrDreamProviderUnavailable}
	_, err = newService(store, &dreamRepositoryStub{}, generator).runClaimedEvidenceCycle(context.Background(), teamID, config, &RunCycleResult{}, claimed)
	require.ErrorContains(t, err, "abandon failed")

	repo = &dreamRepositoryStub{reinforcedErr: errors.New("reinforced list failed")}
	store = &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{target}}
	generator = &evidenceGeneratorStub{model: "model"}
	_, err = newService(store, repo, generator).runClaimedEvidenceCycle(context.Background(), teamID, config, &RunCycleResult{}, claimed)
	require.ErrorContains(t, err, "reinforced list failed")
}

func TestRecoveredEvidenceCyclePreservesPersistedEvaluationTotals(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	target := repository.EvidenceDiscoveryTargetInput{Target: repository.EvidenceTarget{
		EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary",
	}}
	store := &evidenceRepositoryStub{
		targets:       []repository.EvidenceDiscoveryTargetInput{target},
		attemptPasses: map[string]int{targetID + ":hash": 1},
		runTotals: repository.EvidenceDiscoveryRunTotals{
			TargetCount: 1, Evaluated: 1, Created: 4, Rejected: 2, ProviderProposals: 3,
			ProviderTurns: 2, ProviderInputTokens: 10, ProviderOutputTokens: 6,
		},
	}
	repo := &dreamRepositoryStub{}
	generator := &evidenceGeneratorStub{
		model: "model",
		generated: []GeneratedDream{{
			Hypothesis: "A may use B.", SubjectEntityID: "subject", PredicateKey: "uses", ObjectEntityID: "object",
			EvidenceDerivations: []repository.EvidenceDerivationSource{{EvidenceID: targetID, FragmentID: targetID, SpanStart: 0, SpanEnd: 6, Quote: "target", Authority: "primary"}},
		}},
	}
	claimed := &repository.DreamCycleRun{
		TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), AttemptCount: 2, Claimed: true,
	}
	service := &service{deps: Dependencies{
		Store: repo, ScheduledStore: repo, EvidenceStore: store, EvidenceGenerator: generator,
	}, now: time.Now}

	result, err := service.runClaimedEvidenceCycle(context.Background(), teamID, EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5},
	}, &RunCycleResult{}, claimed)
	require.NoError(t, err)
	require.Equal(t, 5, result.CreatedDreams)
	require.Equal(t, 2, result.RejectedDreams)
	require.Equal(t, 2, result.EvaluatedEvidenceTargets)
	require.Equal(t, 4, result.ProviderProposals)
	require.Equal(t, 3, result.ProviderTurns)
	require.Equal(t, 10, result.ProviderInputTokens)
	require.Equal(t, 6, result.ProviderOutputTokens)
	require.Equal(t, 5, repo.completeInput.CreatedHypotheses)
	require.Equal(t, 2, repo.completeInput.RejectedHypotheses)
	require.Equal(t, 2, repo.completeInput.EvaluatedEvidenceTargets)
}

func TestRecoveredEvidenceCycleCountsDistinctPersistedAndSelectedTargets(t *testing.T) {
	teamID := uuid.NewString()
	priorID := uuid.NewString()
	currentID := uuid.NewString()
	target := repository.EvidenceDiscoveryTargetInput{Target: repository.EvidenceTarget{
		EvidenceID: currentID, FragmentID: currentID, ContentHash: "current-hash", Content: "target", Authority: "primary",
	}}
	store := &evidenceRepositoryStub{
		targets: []repository.EvidenceDiscoveryTargetInput{target},
		runTotals: repository.EvidenceDiscoveryRunTotals{
			TargetCount: 1, TargetKeys: []string{priorID + ":prior-hash"},
		},
	}
	repo := &dreamRepositoryStub{}
	generator := &evidenceGeneratorStub{model: "model", generated: []GeneratedDream{{
		Hypothesis: "A may use B.", SubjectEntityID: "subject", PredicateKey: "uses", ObjectEntityID: "object",
		EvidenceDerivations: []repository.EvidenceDerivationSource{{EvidenceID: currentID, FragmentID: currentID, SpanStart: 0, SpanEnd: 6, Quote: "target", Authority: "primary"}},
	}}}
	claimed := &repository.DreamCycleRun{
		TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), AttemptCount: 2, Claimed: true,
	}
	service := &service{deps: Dependencies{
		Store: repo, ScheduledStore: repo, EvidenceStore: store, EvidenceGenerator: generator,
	}, now: time.Now}

	result, err := service.runClaimedEvidenceCycle(context.Background(), teamID, EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5},
	}, &RunCycleResult{}, claimed)
	require.NoError(t, err)
	require.Equal(t, 2, result.EvidenceTargets)
	require.Equal(t, 2, repo.completeInput.EvidenceTargets)
}

func TestRecoveredEvidenceCyclePreservesTotalsWhenTargetListingFails(t *testing.T) {
	teamID := uuid.NewString()
	store := &evidenceRepositoryStub{
		targetsErr: errors.New("target list failed"),
		runTotals: repository.EvidenceDiscoveryRunTotals{
			TargetCount: 3, Evaluated: 3, Created: 4, Rejected: 2, ProviderProposals: 5,
			ProviderTurns: 2, ProviderInputTokens: 10, ProviderOutputTokens: 6,
		},
	}
	repo := &dreamRepositoryStub{}
	claimed := &repository.DreamCycleRun{
		TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), AttemptCount: 2, Claimed: true,
	}
	service := &service{deps: Dependencies{
		Store: repo, ScheduledStore: repo, EvidenceStore: store, EvidenceGenerator: &evidenceGeneratorStub{model: "model"},
	}, now: time.Now}

	result, err := service.runClaimedEvidenceCycle(context.Background(), teamID, EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5},
	}, &RunCycleResult{}, claimed)
	require.Error(t, err)
	require.Equal(t, 3, result.EvidenceTargets)
	require.Equal(t, 4, repo.completeInput.CreatedHypotheses)
	require.Equal(t, 2, repo.completeInput.RejectedHypotheses)
	require.Equal(t, 3, repo.completeInput.EvaluatedEvidenceTargets)
	require.Equal(t, 5, repo.completeInput.ProviderProposals)
}

func TestScheduledEvidenceCycleSkipsSecondPassAfterNoProposal(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	store := &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{{
		Target: repository.EvidenceTarget{EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary"},
	}}}
	repo := &dreamRepositoryStub{}
	generator := &evidenceGeneratorStub{model: "model"}
	service := New(Dependencies{
		Store: repo, ScheduledStore: repo, EvidenceStore: store, EvidenceGenerator: generator,
		AppConfig: cycleAppConfigStub{cfg: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5, Timezone: "UTC", StartTimeLocal: "03:00"}},
	}).(*service)

	result, err := service.RunScheduledEvidenceCycle(context.Background(), teamID, time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	require.Equal(t, 1, len(generator.requests))
	require.Equal(t, 1, result.EvaluatedEvidenceTargets)
	require.Equal(t, 0, result.ProviderProposals)
	require.Equal(t, 1, repo.completeInput.OutcomeSummary["evaluated_evidence_targets"])
	require.Zero(t, repo.completeInput.OutcomeSummary["provider_proposals"])
}

func TestClaimedEvidenceCycleRegeneratesAfterDuplicatePersistence(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	target := repository.EvidenceDiscoveryTargetInput{Target: repository.EvidenceTarget{
		EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary",
	}}
	valid := GeneratedDream{
		Hypothesis: "A may use B.", SubjectEntityID: "subject", PredicateKey: "uses", ObjectEntityID: "object",
		EvidenceDerivations: []repository.EvidenceDerivationSource{{EvidenceID: targetID, FragmentID: targetID, SpanStart: 0, SpanEnd: 6, Quote: "target", Authority: "primary"}},
	}
	store := &evidenceRepositoryStub{
		targets:       []repository.EvidenceDiscoveryTargetInput{target},
		persistErrs:   []error{repository.ErrDreamExactHypothesisExists},
		attemptPasses: map[string]int{targetID + ":hash": 1},
	}
	repo := &dreamRepositoryStub{}
	generator := &evidenceGeneratorStub{model: "model", generatedResponses: [][]GeneratedDream{{valid}, {valid}}}
	service := &service{deps: Dependencies{Store: repo, ScheduledStore: repo, EvidenceStore: store, EvidenceGenerator: generator}, now: time.Now}
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}

	result, err := service.runClaimedEvidenceCycle(context.Background(), teamID, EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5},
	}, &RunCycleResult{}, claimed)
	require.NoError(t, err)
	require.Len(t, generator.requests, 2, "a duplicate response must receive one complete regeneration")
	require.Len(t, store.evaluations, 1, "the duplicate response must not create a partial evaluation")
	require.Len(t, generator.requests[1].RelatedHypotheses, 1, "regeneration must identify the rejected target")
	require.Equal(t, valid.SubjectEntityID, generator.requests[1].RelatedHypotheses[0].SubjectEntityID)
	require.Equal(t, valid.PredicateKey, generator.requests[1].RelatedHypotheses[0].PredicateKey)
	require.Equal(t, valid.ObjectEntityID, generator.requests[1].RelatedHypotheses[0].ObjectEntityID)
	require.Equal(t, 1, result.CreatedDreams)
	require.Equal(t, 1, result.EvaluatedEvidenceTargets)
}

func TestClaimedEvidenceCycleAbandonsAfterPersistenceFailure(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	target := repository.EvidenceDiscoveryTargetInput{Target: repository.EvidenceTarget{
		EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary",
	}}
	valid := GeneratedDream{
		Hypothesis: "A may use B.", SubjectEntityID: "subject", PredicateKey: "uses", ObjectEntityID: "object",
		EvidenceDerivations: []repository.EvidenceDerivationSource{{EvidenceID: targetID, FragmentID: targetID, SpanStart: 0, SpanEnd: 6, Quote: "target", Authority: "primary"}},
	}
	store := &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{target}, persistErr: errors.New("transient persistence failure")}
	repo := &dreamRepositoryStub{}
	service := &service{deps: Dependencies{
		Store: repo, ScheduledStore: repo, EvidenceStore: store,
		EvidenceGenerator: &evidenceGeneratorStub{model: "model", generated: []GeneratedDream{valid}},
	}, now: time.Now}
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}

	_, err := service.runClaimedEvidenceCycle(context.Background(), teamID, EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5},
	}, &RunCycleResult{}, claimed)
	require.ErrorContains(t, err, "transient persistence failure")
	require.Equal(t, 1, store.abandonCalls, "a dispatched attempt must be released after persistence failure")
}

func TestClaimedEvidenceCycleSkipsStaleInputsBeforeProviderDispatch(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	store := &evidenceRepositoryStub{
		targets: []repository.EvidenceDiscoveryTargetInput{{
			Target:   repository.EvidenceTarget{EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary"},
			Contexts: []repository.EvidenceContext{{EvidenceID: targetID, FragmentID: targetID, Content: "target", Authority: "primary"}},
		}},
		validateErr: repository.ErrDreamSourceStale,
	}
	repo := &dreamRepositoryStub{}
	generator := &evidenceGeneratorStub{model: "model", generated: []GeneratedDream{{
		Hypothesis: "A may use B.", SubjectEntityID: "subject", PredicateKey: "uses", ObjectEntityID: "object",
		EvidenceDerivations: []repository.EvidenceDerivationSource{{EvidenceID: targetID, FragmentID: targetID, SpanStart: 0, SpanEnd: 6, Quote: "target", Authority: "primary"}},
	}}}
	service := &service{deps: Dependencies{
		Store: repo, ScheduledStore: repo, EvidenceStore: store, EvidenceGenerator: generator,
	}, now: time.Now}
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}

	result, err := service.runClaimedEvidenceCycle(context.Background(), teamID, EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5},
	}, &RunCycleResult{}, claimed)
	require.NoError(t, err)
	require.Equal(t, "completed", result.Status)
	require.Equal(t, 1, store.validateCalls)
	require.Equal(t, 1, store.abandonCalls)
	require.Empty(t, generator.requests, "stale target/context snapshots must not reach the provider")
	require.Equal(t, 1, result.OutcomeSummary["stale_evidence_targets"])
	require.Zero(t, result.EvaluatedEvidenceTargets)
}

func TestClaimedEvidenceCycleKeepsAdmittedTimeoutReservation(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	store := &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{{
		Target: repository.EvidenceTarget{EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary"},
	}}}
	repo := &dreamRepositoryStub{}
	generator := &evidenceGeneratorStub{model: "model", err: &modelprovider.TimeoutError{Provider: "fixture", Message: "provider request timed out"}}
	service := &service{deps: Dependencies{
		Store: repo, ScheduledStore: repo, EvidenceStore: store, EvidenceGenerator: generator,
	}, now: time.Now}
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}

	_, err := service.runClaimedEvidenceCycle(context.Background(), teamID, EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5},
	}, &RunCycleResult{}, claimed)
	require.ErrorIs(t, err, modelprovider.ErrVerifierTimeout)
	require.Equal(t, 1, store.dispatchedCalls)
	require.Zero(t, store.abandonCalls, "an admitted timeout may have an uncertain provider outcome")
}

func TestClaimedEvidenceCycleAbandonsBeforeAdmissionTimeout(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	store := &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{{
		Target: repository.EvidenceTarget{EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary"},
	}}}
	repo := &dreamRepositoryStub{}
	generator := &evidenceGeneratorStub{
		model:         "model",
		skipAdmission: true,
		err:           &modelprovider.TimeoutError{Provider: "fixture", Message: "gate wait canceled"},
	}
	service := &service{deps: Dependencies{
		Store: repo, ScheduledStore: repo, EvidenceStore: store, EvidenceGenerator: generator,
	}, now: time.Now}
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}

	_, err := service.runClaimedEvidenceCycle(context.Background(), teamID, EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5},
	}, &RunCycleResult{}, claimed)
	require.ErrorIs(t, err, modelprovider.ErrVerifierTimeout)
	require.Zero(t, store.dispatchedCalls)
	require.Equal(t, 1, store.abandonCalls)
}

func TestClaimedEvidenceCycleRevalidatesAtProviderAdmission(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	store := &evidenceRepositoryStub{
		targets: []repository.EvidenceDiscoveryTargetInput{{
			Target: repository.EvidenceTarget{EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary"},
		}},
		validateErrs: []error{nil, repository.ErrDreamSourceStale},
	}
	repo := &dreamRepositoryStub{}
	generator := &evidenceGeneratorStub{model: "model", generated: []GeneratedDream{{
		Hypothesis: "A may use B.", SubjectEntityID: "subject", PredicateKey: "uses", ObjectEntityID: "object",
		EvidenceDerivations: []repository.EvidenceDerivationSource{{EvidenceID: targetID, FragmentID: targetID, SpanStart: 0, SpanEnd: 6, Quote: "target", Authority: "primary"}},
	}}}
	service := &service{deps: Dependencies{
		Store: repo, ScheduledStore: repo, EvidenceStore: store, EvidenceGenerator: generator,
	}, now: time.Now}
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}

	result, err := service.runClaimedEvidenceCycle(context.Background(), teamID, EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5},
	}, &RunCycleResult{}, claimed)
	require.NoError(t, err)
	require.Equal(t, 2, store.validateCalls)
	require.Zero(t, store.dispatchedCalls)
	require.Equal(t, 1, store.abandonCalls)
	require.Equal(t, 1, result.OutcomeSummary["stale_evidence_targets"])
}

func TestAbandonEvidenceDiscoveryAttemptUsesDetachedCleanupContext(t *testing.T) {
	store := &evidenceRepositoryStub{}
	service := &service{deps: Dependencies{EvidenceStore: store}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	require.NoError(t, service.abandonEvidenceDiscoveryAttempt(ctx, uuid.NewString(), uuid.NewString(), uuid.NewString()))
	require.False(t, store.abandonCanceled, "cleanup must not inherit cycle cancellation")
}

func TestValidateEvidenceDiscoveryInputsRequiresValidator(t *testing.T) {
	err := validateEvidenceDiscoveryInputs(context.Background(), nil, uuid.NewString(), repository.EvidenceTarget{}, nil)
	require.ErrorContains(t, err, "input validator is required")
}

func TestEvidenceDuplicateContextPrioritizesOffendingProposal(t *testing.T) {
	proposals := make([]repository.UpsertHypothesisInput, 6)
	for index := range proposals {
		proposals[index] = repository.UpsertHypothesisInput{
			SubjectEntityID: uuid.NewString(), PredicateKey: "uses", PredicateVersion: 1, ObjectEntityID: uuid.NewString(),
		}
	}
	relationships, hypotheses := evidenceDuplicateContext(nil, nil, proposals, 5)
	require.Empty(t, relationships)
	require.Len(t, hypotheses, evidenceDiscoveryRelatedLimit)
	require.Equal(t, proposals[5].SubjectEntityID, hypotheses[0].SubjectEntityID)
	require.Equal(t, proposals[5].ObjectEntityID, hypotheses[0].ObjectEntityID)
}

func TestClaimedEvidenceCycleRefreshesDuplicateContextForSecondPass(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	target := repository.EvidenceDiscoveryTargetInput{Target: repository.EvidenceTarget{
		EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary",
	}}
	valid := GeneratedDream{
		Hypothesis: "A may use B.", SubjectEntityID: "subject", PredicateKey: "uses", ObjectEntityID: "object",
		EvidenceDerivations: []repository.EvidenceDerivationSource{{EvidenceID: targetID, FragmentID: targetID, SpanStart: 0, SpanEnd: 6, Quote: "target", Authority: "primary"}},
	}
	store := &evidenceRepositoryStub{targets: []repository.EvidenceDiscoveryTargetInput{target}}
	repo := &dreamRepositoryStub{}
	generator := &evidenceGeneratorStub{model: "model", generatedResponses: [][]GeneratedDream{{valid}, nil}}
	service := &service{deps: Dependencies{Store: repo, ScheduledStore: repo, EvidenceStore: store, EvidenceGenerator: generator}, now: time.Now}
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}

	result, err := service.runClaimedEvidenceCycle(context.Background(), teamID, EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5},
	}, &RunCycleResult{}, claimed)
	require.NoError(t, err)
	require.Equal(t, 2, len(generator.requests))
	require.Len(t, generator.requests[1].RelatedHypotheses, 1, "pass two must receive pass-one duplicate context")
	require.Equal(t, valid.SubjectEntityID, generator.requests[1].RelatedHypotheses[0].SubjectEntityID)
	require.Equal(t, valid.PredicateKey, generator.requests[1].RelatedHypotheses[0].PredicateKey)
	require.Equal(t, valid.ObjectEntityID, generator.requests[1].RelatedHypotheses[0].ObjectEntityID)
	require.Len(t, store.evaluations, 2)
	require.Equal(t, 2, result.EvaluatedEvidenceTargets)
}

func TestClaimedEvidenceCycleAbandonsAfterDuplicateRegenerationExhaustion(t *testing.T) {
	teamID := uuid.NewString()
	targetID := uuid.NewString()
	target := repository.EvidenceDiscoveryTargetInput{Target: repository.EvidenceTarget{
		EvidenceID: targetID, FragmentID: targetID, ContentHash: "hash", Content: "target", Authority: "primary",
	}}
	valid := GeneratedDream{
		Hypothesis: "A may use B.", SubjectEntityID: "subject", PredicateKey: "uses", ObjectEntityID: "object",
		EvidenceDerivations: []repository.EvidenceDerivationSource{{EvidenceID: targetID, FragmentID: targetID, SpanStart: 0, SpanEnd: 6, Quote: "target", Authority: "primary"}},
	}
	store := &evidenceRepositoryStub{
		targets:     []repository.EvidenceDiscoveryTargetInput{target},
		persistErrs: []error{repository.ErrDreamExactHypothesisExists, repository.ErrDreamExactHypothesisExists},
	}
	repo := &dreamRepositoryStub{}
	generator := &evidenceGeneratorStub{model: "model", generatedResponses: [][]GeneratedDream{{valid}, {valid}}}
	service := &service{deps: Dependencies{Store: repo, ScheduledStore: repo, EvidenceStore: store, EvidenceGenerator: generator}, now: time.Now}
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}

	result, err := service.runClaimedEvidenceCycle(context.Background(), teamID, EffectiveConfig{
		DreamingRuntimeConfig: domain.DreamingRuntimeConfig{Enabled: true, MaxOutputs: 5},
	}, &RunCycleResult{}, claimed)
	require.ErrorIs(t, err, modelprovider.ErrVerifierMalformedResponse)
	require.Equal(t, "evidence discovery provider response invalid", result.Error)
	require.Len(t, generator.requests, 2)
	require.Empty(t, store.evaluations)
	require.Equal(t, 1, store.abandonCalls)
}

func TestFinishEvidenceCycleCombinesRunAndCompletionErrors(t *testing.T) {
	teamID := uuid.NewString()
	claimed := &repository.DreamCycleRun{TeamID: teamID, RunID: uuid.NewString(), LeaseToken: uuid.NewString(), Claimed: true}
	store := &dreamRepositoryStub{completeErr: errors.New("completion failed")}
	service := &service{deps: Dependencies{ScheduledStore: store}, now: time.Now}
	result, err := service.finishEvidenceCycle(context.Background(), teamID, EffectiveConfig{}, nil, claimed, 0, 0, 0, 0, 0, errors.New("cycle failed"))
	require.ErrorContains(t, err, "cycle failed")
	require.ErrorContains(t, err, "completion failed")
	require.Equal(t, "error", result.Status)
}

type evidenceRepositoryStub struct {
	targets         []repository.EvidenceDiscoveryTargetInput
	evaluations     []repository.EvidenceDiscoveryEvaluationInput
	runTotals       repository.EvidenceDiscoveryRunTotals
	runTotalsErr    error
	attemptPasses   map[string]int
	lastLimit       int
	lastMaxContexts int
	targetsErr      error
	validateErr     error
	validateErrs    []error
	lockErr         error
	dispatchedErr   error
	validatedErr    error
	persistErr      error
	persistErrs     []error
	abandonErr      error
	validatedCalls  int
	dispatchedCalls int
	abandonCalls    int
	abandonCanceled bool
	validateCalls   int
	validatedInputs []repository.EvidenceContext
}

type errorAppConfigStub struct{ err error }

func (s errorAppConfigStub) DreamingRuntimeConfig(context.Context) (domain.DreamingRuntimeConfig, error) {
	return domain.DreamingRuntimeConfig{}, s.err
}

type errorTeamServiceStub struct{ err error }

func (s *errorTeamServiceStub) GetByID(context.Context, uuid.UUID) (*domain.Team, error) {
	return nil, s.err
}

func (s *errorTeamServiceStub) List(context.Context, int, int) ([]*domain.Team, error) {
	return nil, s.err
}

type evidenceTeamServiceStub struct {
	team *domain.Team
}

func (s *evidenceTeamServiceStub) GetByID(context.Context, uuid.UUID) (*domain.Team, error) {
	return s.team, nil
}

func (s *evidenceTeamServiceStub) List(context.Context, int, int) ([]*domain.Team, error) {
	if s.team == nil {
		return nil, nil
	}
	return []*domain.Team{s.team}, nil
}

func (s *evidenceRepositoryStub) ListEvidenceDiscoveryTargets(_ context.Context, _ string, limit, maxContexts int) ([]repository.EvidenceDiscoveryTargetInput, error) {
	s.lastLimit, s.lastMaxContexts = limit, maxContexts
	if s.targetsErr != nil {
		return nil, s.targetsErr
	}
	return append([]repository.EvidenceDiscoveryTargetInput(nil), s.targets...), nil
}

func (s *evidenceRepositoryStub) ValidateEvidenceDiscoveryInputs(_ context.Context, _ string, _ repository.EvidenceTarget, contexts []repository.EvidenceContext) error {
	s.validateCalls++
	s.validatedInputs = append([]repository.EvidenceContext(nil), contexts...)
	if len(s.validateErrs) > 0 {
		err := s.validateErrs[0]
		s.validateErrs = s.validateErrs[1:]
		return err
	}
	return s.validateErr
}

func (s *evidenceRepositoryStub) LoadEvidenceDiscoveryRunTotals(_ context.Context, _, _ string) (repository.EvidenceDiscoveryRunTotals, error) {
	return s.runTotals, s.runTotalsErr
}

func (s *evidenceRepositoryStub) PersistEvidenceDiscoveryEvaluation(_ context.Context, input repository.EvidenceDiscoveryEvaluationInput) (repository.DreamGenerationPersistResult, error) {
	if len(s.persistErrs) > 0 {
		err := s.persistErrs[0]
		s.persistErrs = s.persistErrs[1:]
		if err != nil {
			return repository.DreamGenerationPersistResult{}, err
		}
	}
	if s.persistErr != nil {
		return repository.DreamGenerationPersistResult{}, s.persistErr
	}
	s.evaluations = append(s.evaluations, input)
	return repository.DreamGenerationPersistResult{Created: len(input.Proposals)}, nil
}

func (s *evidenceRepositoryStub) WithEvidenceDiscoveryTargetLock(_ context.Context, _ string, targetID, contentHash string, fn func(repository.EvidenceDiscoveryAttempt) error) error {
	if s.lockErr != nil {
		return s.lockErr
	}
	if s.attemptPasses == nil {
		s.attemptPasses = map[string]int{}
	}
	key := targetID + ":" + contentHash
	pass := s.attemptPasses[key] + 1
	s.attemptPasses[key] = pass
	if pass > evidenceDiscoveryPassLimit {
		return fn(repository.EvidenceDiscoveryAttempt{})
	}
	return fn(repository.EvidenceDiscoveryAttempt{AttemptID: uuid.NewString(), ReservationToken: uuid.NewString(), PassNumber: pass})
}

func (s *evidenceRepositoryStub) MarkEvidenceDiscoveryAttemptValidated(_ context.Context, _ repository.EvidenceDiscoveryAttemptValidationInput) error {
	s.validatedCalls++
	if s.validatedErr != nil {
		return s.validatedErr
	}
	return nil
}

func (s *evidenceRepositoryStub) MarkEvidenceDiscoveryAttemptDispatched(_ context.Context, _ repository.EvidenceDiscoveryAttemptValidationInput) error {
	s.dispatchedCalls++
	if s.dispatchedErr != nil {
		return s.dispatchedErr
	}
	return nil
}

func (s *evidenceRepositoryStub) AbandonEvidenceDiscoveryAttempt(ctx context.Context, _, _, _ string) error {
	s.abandonCalls++
	s.abandonCanceled = ctx.Err() != nil
	if s.abandonErr != nil {
		return s.abandonErr
	}
	return nil
}

type evidenceGeneratorStub struct {
	model              string
	generated          []GeneratedDream
	generatedResponses [][]GeneratedDream
	generatedCalls     int
	skipAdmission      bool
	err                error
	errorDiagnostics   GenerationDiagnostics
	requests           []EvidenceGenerationRequest
}

func (s *evidenceGeneratorStub) GenerateEvidence(ctx context.Context, _ string, request EvidenceGenerationRequest) ([]GeneratedDream, GenerationDiagnostics, error) {
	s.requests = append(s.requests, request)
	if !s.skipAdmission {
		if err := modelprovider.NotifyAdmission(ctx); err != nil {
			return nil, GenerationDiagnostics{}, err
		}
	}
	if s.err != nil {
		return nil, s.errorDiagnostics, s.err
	}
	generated := append([]GeneratedDream(nil), s.generated...)
	if s.generatedCalls < len(s.generatedResponses) {
		generated = append([]GeneratedDream(nil), s.generatedResponses[s.generatedCalls]...)
	}
	s.generatedCalls++
	for index := range generated {
		for derivationIndex := range generated[index].EvidenceDerivations {
			derivation := &generated[index].EvidenceDerivations[derivationIndex]
			if derivation.EvidenceID == "" || derivation.EvidenceID != request.Target.EvidenceID {
				derivation.EvidenceID = request.Target.EvidenceID
				derivation.FragmentID = request.Target.FragmentID
				derivation.SourceGroupKey = request.Target.SourceGroupKey
				derivation.Authority = request.Target.Authority
				derivation.Quote = "target"
			}
		}
	}
	return generated, GenerationDiagnostics{ProviderTurns: 1, ProviderProposals: len(generated)}, nil
}

func (s *evidenceGeneratorStub) Model() string { return s.model }
