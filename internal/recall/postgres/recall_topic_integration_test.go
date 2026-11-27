//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpostgres "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	recallservice "github.com/markhuangai/dense-mem/internal/recall"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspostgres "github.com/markhuangai/dense-mem/internal/settings/postgres"
	organization "github.com/markhuangai/dense-mem/internal/topics"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	"github.com/markhuangai/dense-mem/internal/topics/assignment"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type equivalenceRetryDecision struct{}

func (*equivalenceRetryDecision) DecisionRuntimeConfig(context.Context) (domain.DecisionRuntimeConfig, error) {
	return domain.DecisionRuntimeConfig{Revision: "equivalence-retry", Effective: domain.DecisionTasks{TopicAssignment: true}}, nil
}

func (d *equivalenceRetryDecision) Decide(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
	answers := make(map[string]modelprovider.DecisionAnswer)
	for _, group := range request.Groups {
		for _, question := range group.Questions {
			probability := 0.99
			answers[question.Name] = modelprovider.DecisionAnswer{Type: modelprovider.DecisionPredicate, Probability: &probability}
		}
	}
	return modelprovider.DecisionResult{Answers: answers}, nil
}

func TestRecallEquivalenceMaintenanceFailureRetryAndCoverage(t *testing.T) {
	f := newRecallOntologyFixture(t)
	ctx := context.Background()
	texts := []string{"Atlas stores data in PostgreSQL.", "PostgreSQL is Atlas's data store."}
	handles := []topic.SourceHandle{f.evidence(t, 0, texts[0], nil), f.evidence(t, 1, texts[1], nil)}
	config := settings.NewAppConfigService(settingspostgres.NewAppConfigRepository(f.app, f.rls), nil)
	_, err := config.UpdateTopicMaintenanceSettings(ctx, map[string]string{
		domain.AppConfigTopicMaintenanceEnabled: "true", domain.AppConfigTopicMaintenanceModel: "fixture-model",
		domain.AppConfigTopicMaintenanceContextWindow: "300000",
	}, "control", "", "")
	require.NoError(t, err)
	_, err = config.UpdateGeneralSettings(ctx, map[string]string{domain.AppConfigTimezone: "UTC"}, "control", "", "")
	require.NoError(t, err)
	policy, err := config.TopicMaintenanceRuntimeConfig(ctx)
	require.NoError(t, err)
	window, err := f.topicStore.EnsureMaintenanceWindow(ctx, policy, time.Now().UTC())
	require.NoError(t, err)
	turn, err := f.topicStore.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	_, err = f.topicStore.DiscoverMaintenance(ctx, *turn, topic.MaintenancePageSize)
	require.NoError(t, err)
	claim, err := f.topicStore.ClaimMaintenanceBatch(ctx, *turn, window.ID, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.ElementsMatch(t, handles, claim.Sources)
	result := topic.OrganizationResult{Current: true, IsolatedOutcomes: true}
	for _, source := range claim.Sources {
		snapshots, err := f.topicStore.ReadSources(ctx, f.team, []topic.SourceHandle{source})
		require.NoError(t, err)
		fingerprint, err := topic.SourceFingerprint(snapshots[0])
		require.NoError(t, err)
		outcome, err := f.topicStore.CommitMaintenanceItemAssignment(topic.WithMaintenanceClaim(ctx, *claim), *claim, assignment.Publication{
			EvidenceID: source.ID, ExpectedFingerprint: fingerprint,
			Outcome: assignment.Outcome{Text: topic.SourceDisplayText(snapshots[0]), Method: "decision", InputHash: sha256Hex(source.ID)},
		})
		require.NoError(t, err)
		result.Outcomes = append(result.Outcomes, outcome)
	}
	require.NoError(t, f.topicStore.CompleteMaintenanceBatch(ctx, *claim, result, "", time.Now().UTC()))
	var missingDocument string
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		return tx.Raw(`UPDATE search_documents SET embedding=NULL,search_state='pending'
			WHERE team_id=?::uuid AND source_id=?::uuid RETURNING search_document_id::text`, f.team, handles[0].ID).Row().Scan(&missingDocument)
	}))
	decisions := &equivalenceRetryDecision{}
	service := organization.NewMaintenanceService(organization.MaintenanceDependencies{
		Repository: f.topicStore, Config: config, DefaultModel: "fixture-model", ProviderTimeout: time.Minute,
		Equivalence: f.topicStore, Decisions: decisions, DecisionSettings: decisions,
		Audit: auditapp.New(auditpostgres.NewStore(f.app, f.rls)),
		Organizer: func(_ string, _ int64, accounting assessment.AttemptAccounting) *organization.Service {
			organizer, _ := f.organizerWithAccounting(t, nil, false, accounting)
			return organizer
		},
	})
	failed, err := service.Command(ctx, domain.TopicMaintenanceCommand{Action: "run", OperationKey: uuid.NewString(), MaxBatches: 1}, "", "")
	require.NoError(t, err)
	progress, err := service.RunTurn(ctx)
	require.True(t, progress)
	require.Error(t, err)
	status, err := service.Status(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Equivalence.Failed)
	require.EqualValues(t, 1, status.Equivalence.Completed, "a missing vector cannot fail a healthy sibling")
	require.False(t, status.CoverageComplete)
	retryable, err := f.topicStore.MaintenanceRunRetryable(ctx, failed.ID)
	require.NoError(t, err)
	require.True(t, retryable)
	completeSearchDocumentsForTest(t, f.search, f.team, map[string][]float32{missingDocument: {1, 0, 0}})
	retry, err := service.Command(ctx, domain.TopicMaintenanceCommand{Action: "retry", OperationKey: uuid.NewString(), RetryRunID: failed.ID, MaxBatches: 1}, "", "")
	require.NoError(t, err)
	progress, err = service.RunTurn(ctx)
	require.True(t, progress)
	require.NoError(t, err)
	var selected []byte
	require.NoError(t, f.admin.Raw(`SELECT sources FROM topic_maintenance_batches WHERE run_id=?::uuid AND batch_kind='equivalence'`, retry.ID).Row().Scan(&selected))
	var retried []topic.SourceHandle
	require.NoError(t, json.Unmarshal(selected, &retried))
	require.Equal(t, []topic.SourceHandle{handles[0]}, retried)
	status, err = service.Status(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 2, status.Equivalence.Completed)
	require.True(t, status.CoverageComplete)
	grouped, err := f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "PostgreSQL", Limit: 3, OrganizationEnabled: true})
	require.NoError(t, err)
	require.Len(t, grouped.Results, 1)
	require.Len(t, grouped.Results[0].EquivalentEvidenceIDs, 1)
	require.ElementsMatch(t, []string{handles[0].ID, handles[1].ID}, []string{grouped.Results[0].EvidenceID, grouped.Results[0].EquivalentEvidenceIDs[0]})
}

func TestRecallOntologyGroupingKnownIDsAndStaleSources(t *testing.T) {
	f := newRecallOntologyFixture(t)
	texts := []string{"Atlas stores data in PostgreSQL.", "PostgreSQL is Atlas's datastore.", "Atlas stores data in PostgreSQL and Redis."}
	handles := []topic.SourceHandle{}
	meanings := map[string]string{}
	for i, text := range texts {
		handles = append(handles, f.evidence(t, i, text, nil))
		meanings[text] = "storage"
	}
	meanings[texts[2]] = "storage-and-cache"
	service, calls := f.organizer(t, meanings)
	_, err := service.Organize(context.Background(), f.team, topic.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	beforeCalls := calls.Load()
	input := RecallEvidenceInput{TeamID: f.team, Query: "PostgreSQL", Limit: 3, SpaceID: f.space, OrganizationEnabled: true}
	grouped, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, grouped.Results, 2)
	var duplicateID string
	for _, hit := range grouped.Results {
		if len(hit.EquivalentEvidenceIDs) > 0 {
			require.Len(t, hit.EquivalentEvidenceIDs, 1)
			duplicateID = hit.EquivalentEvidenceIDs[0]
		}
	}
	require.NotEmpty(t, duplicateID)
	for _, knownID := range []string{duplicateID, strings.ToUpper(duplicateID), strings.ReplaceAll(duplicateID, "-", ""), "{" + duplicateID + "}", "urn:uuid:" + duplicateID} {
		input.KnownEvidenceIDs = []string{knownID}
		known, err := f.search.RecallEvidence(f.actor(2, "member"), input)
		require.NoError(t, err)
		require.Empty(t, known.Degradations)
		require.Len(t, known.Results, 1)
		require.Equal(t, handles[2].ID, known.Results[0].EvidenceID)
	}
	input.KnownEvidenceIDs = []string{uuid.NewString()}
	unknown, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, unknown.Results, 2)
	_, err = f.knowledge.RetractEvidence(f.actor(2, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[2], EvidenceIDs: []string{handles[0].ID}, Reason: "wrong owner regression", IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex("wrong owner")})
	require.Error(t, err)
	_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{handles[0].ID}, Reason: "withdraw source", IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex("withdraw source")})
	require.NoError(t, err)
	input.KnownEvidenceIDs = []string{handles[0].ID}
	stale, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, stale.Results, 2)
	for _, hit := range stale.Results {
		require.Empty(t, hit.EquivalentEvidenceIDs)
		require.NotEqual(t, handles[0].ID, hit.EvidenceID)
	}
	require.Contains(t, degradationCodes(stale.Degradations), "ontology_stale")
	require.Equal(t, beforeCalls, calls.Load(), "Recall cannot invoke organization assessment")
}

func TestRecallOntologyDiscoveryAliasesAndTemporalFallback(t *testing.T) {
	f := newRecallOntologyFixture(t)
	handles := []topic.SourceHandle{f.evidence(t, 0, "Atlas stores data in PostgreSQL.", nil), f.evidence(t, 1, "PostgreSQL is Atlas's datastore.", nil)}
	service, _ := f.organizer(t, map[string]string{"Atlas stores data in PostgreSQL.": "same", "PostgreSQL is Atlas's datastore.": "same"})
	_, err := service.Organize(context.Background(), f.team, topic.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	input := RecallEvidenceInput{TeamID: f.team, Query: "database", Limit: 3, SpaceID: f.space}
	ordinary, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Empty(t, ordinary.Results)
	input.OrganizationEnabled = true
	discovered, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, discovered.Results, 1)
	require.Len(t, discovered.Results[0].EquivalentEvidenceIDs, 1)
	page, err := f.topicStore.ListRecords(context.Background(), f.team, "", "", 1)
	require.NoError(t, err)
	duplicate := topic.Record{ID: uuid.NewString(), Kind: topic.Topic, Definition: &topic.Definition{Key: "database-alias-conflict", Label: "Database alias conflict", Aliases: []string{"database"}}}
	_, err = f.topicStore.PublishManager(f.actor(0, "manager"), f.team, topic.Publication{OperationKey: uuid.NewString(), ExpectedRevision: page.Revision, Reason: "reject duplicate topic alias", Changes: []topic.Change{{ExpectedVersion: duplicate.Version, Record: duplicate}}})
	require.ErrorIs(t, err, topic.ErrInvalid)
	unchanged, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, unchanged.Results, 1, "rejected aliases must not change current discovery")
	input.Query = "PostgreSQL"
	for _, knownTime := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid_at", true: "known_at"}[knownTime], func(t *testing.T) {
			now := time.Now().UTC()
			temporal := input
			if knownTime {
				temporal.KnownAt = &now
			} else {
				temporal.ValidAt = &now
			}
			result, err := f.search.RecallEvidence(f.actor(2, "member"), temporal)
			require.NoError(t, err)
			require.Len(t, result.Results, 2)
			require.Contains(t, degradationCodes(result.Degradations), "ontology_temporal_not_supported")
			for _, hit := range result.Results {
				require.Empty(t, hit.EquivalentEvidenceIDs)
			}
		})
	}
	f.rejectHistoricalOverride(t, topic.KeepSeparate, handles)
	retained, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, retained.Results, 1)
	hit := retained.Results[0]
	require.ElementsMatch(t, []string{handles[0].ID, handles[1].ID}, append([]string{hit.EvidenceID}, hit.EquivalentEvidenceIDs...))

}

func TestRecallOntologyOptionalFailureAndRequiredFailureRemainDistinct(t *testing.T) {
	f := newRecallOntologyFixture(t)
	f.evidence(t, 0, "Atlas uses PostgreSQL.", nil)
	input := RecallEvidenceInput{TeamID: f.team, Query: "PostgreSQL", Limit: 1, SpaceID: f.space, OrganizationEnabled: true}
	reader := f.search.recall.organizationReader
	f.search.recall.WithOrganizationReader(func() OrganizationReader {
		return func(ctx context.Context, tx *gorm.DB, team string, input topic.RecallReadInput) (topic.RecallOrganization, error) {
			return topic.RecallOrganization{}, tx.Exec("SELECT deliberately_missing_ontology_function()").Error
		}
	})
	result, err := f.search.RecallEvidence(f.actor(2, "member"), input)
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	require.Contains(t, degradationCodes(result.Degradations), "ontology_unavailable")
	f.search.recall.WithOrganizationReader(reader)
	ctx, cancel := context.WithCancel(f.actor(2, "member"))
	cancel()
	result, err = f.search.RecallEvidence(ctx, input)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, result)
	_, err = reader()(f.actor(2, "member"), nil, f.team, topic.RecallReadInput{Limit: 1})
	require.Error(t, err)
	foreignTeam := createLedgerTeam(t, f.admin, f.rls, "foreign ontology recall")
	result, err = f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: foreignTeam, Query: "PostgreSQL", OrganizationEnabled: true})
	require.True(t, result == nil || len(result.Results) == 0)
	if err != nil {
		require.False(t, errors.Is(err, context.Canceled))
	}
}

func degradationCodes(values []recallcontract.RecallDegradationResult) []string {
	codes := []string{}
	for _, value := range values {
		codes = append(codes, value.Code)
	}
	return codes
}

func TestRecallOntologyPunctuatedKeysAndAliases(t *testing.T) {
	f := newRecallOntologyFixture(t)
	handle := f.evidence(t, 0, "Atlas retains durable records.", nil)
	for _, name := range []string{"C++", "Node.js", "C#"} {
		definition := topic.Record{ID: uuid.NewString(), Kind: topic.Topic, Definition: &topic.Definition{Key: name, Label: name, Aliases: []string{name + " platform"}}}
		assignment := f.assignment(t, definition.ID, handle)
		f.publication(t, definition, assignment)
		accepted, err := f.topicStore.GetRecord(context.Background(), f.team, assignment.ID, 0)
		require.NoError(t, err)
		require.True(t, accepted.Current)
		for _, query := range []string{name, "What is " + name + "?", "Where is (" + name + " platform)?"} {
			input := RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: query, Limit: 3}
			ordinary, err := f.search.RecallEvidence(f.actor(2, "member"), input)
			require.NoError(t, err)
			require.Empty(t, ordinary.Results)
			input.OrganizationEnabled = true
			discovered, err := f.search.RecallEvidence(f.actor(2, "member"), input)
			require.NoError(t, err)
			require.Len(t, discovered.Results, 1)
			require.Equal(t, handle.ID, discovered.Results[0].EvidenceID)
		}
		definition.Version = 1
		definition.Definition.Key = name + " language"
		definition.Definition.Label = name + " language"
		definition.Definition.Aliases = []string{name}
		f.publication(t, definition)
		retained, err := f.topicStore.GetRecord(context.Background(), f.team, assignment.ID, 0)
		require.NoError(t, err)
		require.True(t, retained.Current)
		require.Equal(t, accepted.Record, retained.Record)
		alias, err := f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "What is " + name + "?", Limit: 3, OrganizationEnabled: true})
		require.NoError(t, err)
		require.Len(t, alias.Results, 1)
		require.Equal(t, handle.ID, alias.Results[0].EvidenceID)
	}
}

func TestRecallOntologyFeedbackAlternatesDoNotInflateResults(t *testing.T) {
	f := newRecallOntologyFixture(t)
	handles := []topic.SourceHandle{f.evidence(t, 0, "Atlas stores data in PostgreSQL.", nil), f.evidence(t, 1, "PostgreSQL is Atlas's datastore.", nil)}
	producer, _ := f.organizer(t, map[string]string{"Atlas stores data in PostgreSQL.": "same", "PostgreSQL is Atlas's datastore.": "same"})
	_, err := producer.Organize(context.Background(), f.team, topic.OrganizationInput{OperationKey: uuid.NewString(), Sources: handles})
	require.NoError(t, err)
	result, err := f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "PostgreSQL", Limit: 1, OrganizationEnabled: true})
	require.NoError(t, err)
	require.Len(t, result.Results, 1)
	hit := result.Results[0]
	refs := recallservice.FeedbackResultRefs(&recallcontract.RecallResult{Results: []recallcontract.RecallResultItem{{EvidenceID: hit.EvidenceID, EquivalentEvidenceIDs: hit.EquivalentEvidenceIDs, Rank: hit.Rank}}})
	team, owner, space := uuid.MustParse(f.team), uuid.MustParse(f.owners[2]), uuid.MustParse(f.space)
	recallID := "rec_" + uuid.NewString()
	store := NewFeedbackStore(f.app, f.rls)
	require.NoError(t, store.RecordSnapshot(f.actor(2, "member"), domain.RecallFeedbackEvent{RecallID: recallID, TeamID: &team, ProfileID: &owner, SpaceID: &space, SpaceGeneration: f.generation, ResultRefs: refs}))
	saved, err := store.Get(f.actor(2, "member"), recallID)
	require.NoError(t, err)
	require.NotNil(t, saved)
	require.Equal(t, 1, saved.ResultCount)
	require.Len(t, saved.ResultRefs, 2)
	require.Equal(t, hit.EvidenceID, saved.ResultRefs[1].EquivalentTo)
	feedback := recallservice.NewRecallFeedbackEventService(store, nil, nil)
	require.NoError(t, feedback.RecordRecallFeedback(f.actor(2, "member"), domain.RecallFeedbackSubmission{RecallID: recallID, Quality: "high", Used: true, AnswerSupported: true, IrrelevantRefs: []domain.RecallFeedbackJudgedResultRef{{Type: domain.RecallFeedbackResultTypeEvidence, ID: hit.EquivalentEvidenceIDs[0], Rank: 1}}}))
	saved, err = store.Get(f.actor(2, "member"), recallID)
	require.NoError(t, err)
	require.Equal(t, 1, saved.ResultCount)
	require.Equal(t, hit.EquivalentEvidenceIDs[0], saved.IrrelevantRefs[0].ID)
}
