package postgres

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func ambiguousClassification(_ assessment.Request, response *assessment.Response) {
	response.Definitions = []assessment.Definition{}
	for i := range response.Items {
		response.Items[i].Status = "ambiguous"
		response.Items[i].DefinitionRef = ""
		response.Items[i].Reason = "classification_uncertain"
	}
}

func testOntologyOrganizationAmbiguousComparisonReceipt(t *testing.T) {
	f := newOrganizationFixture(t)
	a := f.organizationEvidence(t, 0, "Atlas stores data in PostgreSQL.", nil)
	b := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL for storage.", nil)
	before := f.canonicalSnapshot(t)
	service, calls := organizationFixtureService(t, f, nil, func(_ assessment.Request, response *assessment.Response) {
		response.Equivalence[0].Relation = "ambiguous"
	})
	input := ontology.OrganizationInput{OperationKey: "uncertain-pair", Sources: []ontology.SourceHandle{a, b}}
	result, err := service.Organize(context.Background(), f.team, input)
	require.NoError(t, err)
	require.True(t, result.Current)
	require.Len(t, result.AmbiguousComparisons, 1)
	comparison := result.AmbiguousComparisons[0]
	require.ElementsMatch(t, []ontology.SourceHandle{a, b}, []ontology.SourceHandle{comparison.Left, comparison.Right})
	require.Equal(t, "equivalence_ambiguous", comparison.Reason)
	groups, err := f.store.ListRecords(context.Background(), f.team, ontology.EvidenceGroup, "", 20)
	require.NoError(t, err)
	require.Empty(t, groups.Records)
	for _, key := range []string{input.OperationKey, "uncertain-pair-new-key"} {
		input.OperationKey = key
		replay, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, replay.Existing)
		require.Equal(t, result.AssessmentID, replay.AssessmentID)
		require.Equal(t, result.AmbiguousComparisons, replay.AmbiguousComparisons)
	}
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func testOntologyOrganizationCompletedReceiptInvalidation(t *testing.T) {
	t.Run("conflicting stale definition proposals publish no subset", func(t *testing.T) {
		f := newOrganizationFixture(t)
		oldSource := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		service, calls := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
			if len(request.Items) == 2 {
				require.Empty(t, request.Definitions)
				response.Definitions = []assessment.Definition{
					{Ref: "first", Kind: ontology.Topic, Key: "postgresql", Label: "First storage proposal", Aliases: []string{}},
					{Ref: "second", Kind: ontology.Topic, Key: "pg", Label: "Second storage proposal", Aliases: []string{}},
				}
				response.Items[0].DefinitionRef, response.Items[1].DefinitionRef = "first", "second"
			}
		})
		original, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "original-definition", Sources: []ontology.SourceHandle{oldSource}})
		require.NoError(t, err)
		id := ontology.OrganizationRecordID(f.team, ontology.Topic, "postgresql")
		old, err := f.store.GetRecord(context.Background(), f.team, id, 0)
		require.NoError(t, err)
		old.Definition.Aliases = []string{"pg"}
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("definition-alias", original.Publication.Revision, ontology.Change{ExpectedVersion: old.Version, Record: old.Record}))
		require.NoError(t, err)
		_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{oldSource.ID}, Reason: "withdraw definition support", IdempotencyKey: "withdraw-old-support", RequestHash: testHash("withdraw-old-support")})
		require.NoError(t, err)
		a := f.organizationEvidence(t, 0, "Atlas uses a relational store.", nil)
		b := f.organizationEvidence(t, 1, "Beacon relies on a SQL database.", nil)
		before := f.canonicalSnapshot(t)
		heads, err := f.store.ListRecords(context.Background(), f.team, "", "", 20)
		require.NoError(t, err)
		input := ontology.OrganizationInput{OperationKey: "conflicting-proposals", Sources: []ontology.SourceHandle{a, b}}
		result, err := service.Organize(context.Background(), f.team, input)
		require.ErrorIs(t, err, ontology.ErrConflict)
		require.Equal(t, "commit_conflict", result.FailureCode)
		require.Nil(t, result.Publication)
		require.Equal(t, int32(2), calls.Load())
		replay, err := service.Organize(context.Background(), f.team, input)
		require.Error(t, err)
		require.True(t, replay.Existing)
		require.Equal(t, result.AssessmentID, replay.AssessmentID)
		require.Equal(t, int32(2), calls.Load())
		after, err := f.store.ListRecords(context.Background(), f.team, "", "", 20)
		require.NoError(t, err)
		require.Equal(t, heads, after)
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	for _, match := range []string{"key", "alias"} {
		for _, pinned := range []bool{false, true} {
			name := "rebuild stale definition"
			if pinned {
				name = "stale definition pin still blocks rebuilding"
			}
			t.Run(name+"/"+match, func(t *testing.T) {
				f := newOrganizationFixture(t)
				oldSource := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
				service, calls := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
					if request.Items[0].Text == "Beacon uses PostgreSQL." && match == "alias" {
						response.Definitions[0].Key = "relational-store"
						response.Definitions[0].Label = "Relational storage"
						response.Definitions[0].Aliases = []string{" POSTGRESQL "}
					}
				})
				original, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "old-definition", Sources: []ontology.SourceHandle{oldSource}})
				require.NoError(t, err)
				id := ontology.OrganizationRecordID(f.team, ontology.Topic, "postgresql")
				oldDefinition, err := f.store.GetRecord(context.Background(), f.team, id, 0)
				require.NoError(t, err)
				require.True(t, oldDefinition.Current)
				var pin ontology.Record
				if pinned {
					pin = ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.PinDefinition, TargetID: id}}
					_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("pin-old-definition", original.Publication.Revision, ontology.Change{Record: pin}))
					require.NoError(t, err)
				}
				_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{oldSource.ID}, Reason: "replace definition support", IdempotencyKey: "withdraw-definition-source", RequestHash: testHash("withdraw-definition-source")})
				require.NoError(t, err)
				newSource := f.organizationEvidence(t, 1, "Beacon uses PostgreSQL.", nil)
				before := f.canonicalSnapshot(t)
				contextData, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{newSource})
				require.NoError(t, err)
				require.Empty(t, contextData.Candidates)
				input := ontology.OrganizationInput{OperationKey: "replacement-definition", Sources: []ontology.SourceHandle{newSource}}
				result, err := service.Organize(context.Background(), f.team, input)
				if pinned {
					require.ErrorIs(t, err, ontology.ErrOverride)
					require.Nil(t, result.Publication)
					retained, err := f.store.GetRecord(context.Background(), f.team, pin.ID, 0)
					require.NoError(t, err)
					require.False(t, retained.Retired)
					require.Equal(t, ontology.PinDefinition, retained.Override.Action)
				} else {
					require.NoError(t, err)
					require.NotNil(t, result.Publication)
					rebuilt, err := f.store.GetRecord(context.Background(), f.team, id, 0)
					require.NoError(t, err)
					require.True(t, rebuilt.Current)
					require.Equal(t, oldDefinition.Version+1, rebuilt.Version)
					require.Equal(t, oldDefinition.Definition.Key, rebuilt.Definition.Key)
					require.Len(t, rebuilt.Sources, 1)
					require.Equal(t, newSource, rebuilt.Sources[0].SourceHandle)
					input.OperationKey = "replacement-definition-replay"
					replay, err := service.Organize(context.Background(), f.team, input)
					require.NoError(t, err)
					require.True(t, replay.Existing)
					require.Equal(t, result.AssessmentID, replay.AssessmentID)
				}
				historical, err := f.store.GetRecord(context.Background(), f.team, id, oldDefinition.Version)
				require.NoError(t, err)
				require.Equal(t, oldDefinition.Record, historical.Record)
				require.Equal(t, int32(2), calls.Load())
				require.Equal(t, before, f.canonicalSnapshot(t))
			})
		}
	}
	t.Run("restored context reuses older completed receipt", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		before := f.canonicalSnapshot(t)
		service, calls := organizationFixtureService(t, f, nil, ambiguousClassification)
		input := ontology.OrganizationInput{OperationKey: "original-empty-context", Sources: []ontology.SourceHandle{source}}
		original, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		topic := testTopic("postgresql")
		publication, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("add-unused-topic", 0, ontology.Change{Record: topic}))
		require.NoError(t, err)
		input.OperationKey = "context-with-topic"
		newer, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.NotEqual(t, original.AssessmentID, newer.AssessmentID)
		require.Nil(t, newer.Publication)
		require.Equal(t, int32(2), calls.Load())
		view, err := f.store.GetRecord(context.Background(), f.team, topic.ID, 0)
		require.NoError(t, err)
		view.Record.Retired = true
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("retire-unused-topic", publication.Revision, ontology.Change{ExpectedVersion: view.Version, Record: view.Record}))
		require.NoError(t, err)
		stale, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, stale.Existing)
		require.False(t, stale.Current)
		require.Equal(t, newer.AssessmentID, stale.AssessmentID)
		input.OperationKey = "restored-empty-context"
		replay, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, replay.Existing)
		require.True(t, replay.Current)
		require.Equal(t, original.AssessmentID, replay.AssessmentID)
		require.Equal(t, int32(2), calls.Load())
		require.Equal(t, 3, receiptCount(t, f))
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("newer failure does not shadow completed receipt", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		before := f.canonicalSnapshot(t)
		service, calls := organizationFixtureService(t, f, nil, ambiguousClassification)
		input := ontology.OrganizationInput{OperationKey: "completed-before-failure", Sources: []ontology.SourceHandle{source}}
		original, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		var failed ontology.OrganizationReceipt
		require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
			var found bool
			failed, found, err = organizationByKey(tx, fence, input.OperationKey)
			require.True(t, found)
			return err
		}))
		failed.ID = uuid.NewString()
		failed.OperationKey = "newer-failure"
		failed.Result.AssessmentID = failed.ID
		failed.Result.Current = false
		failed.Result.FailureCode = "provider_unavailable"
		failed.Result.Outcomes[0].Status = "failed"
		failed.Result.Outcomes[0].Reason = failed.Result.FailureCode
		_, err = f.store.CommitOrganization(context.Background(), f.team, failed, ontology.Publication{})
		require.NoError(t, err)
		input.OperationKey = "reuse-older-success"
		replay, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, replay.Existing)
		require.True(t, replay.Current)
		require.Empty(t, replay.FailureCode)
		require.Equal(t, original.AssessmentID, replay.AssessmentID)
		input.OperationKey = failed.OperationKey
		replay, err = service.Organize(context.Background(), f.team, input)
		require.ErrorContains(t, err, "provider_unavailable")
		require.True(t, replay.Existing)
		require.Equal(t, failed.ID, replay.AssessmentID)
		require.Equal(t, int32(1), calls.Load())
		require.Equal(t, 3, receiptCount(t, f))
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("withdrawn source", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		service, calls := organizationFixtureService(t, f, nil, nil)
		input := ontology.OrganizationInput{OperationKey: "before-withdrawal", Sources: []ontology.SourceHandle{source}}
		original, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		_, err = f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{source.ID}, Reason: "withdraw completed source", IdempotencyKey: "withdraw-completed", RequestHash: testHash("withdraw-completed")})
		require.NoError(t, err)
		before := f.canonicalSnapshot(t)
		historical, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, historical.Existing)
		require.False(t, historical.Current)
		require.Equal(t, original.AssessmentID, historical.AssessmentID)
		input.OperationKey = "after-withdrawal"
		current, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.False(t, current.Existing)
		require.NotEqual(t, original.AssessmentID, current.AssessmentID)
		require.Equal(t, "unavailable", current.Outcomes[0].Status)
		require.Nil(t, current.Publication)
		require.Equal(t, int32(1), calls.Load())
		require.Equal(t, 2, receiptCount(t, f))
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("changed entity name", func(t *testing.T) {
		f := newOrganizationFixture(t)
		entity, err := f.knowledge.CreateEntity(context.Background(), knowledge.CreateEntityInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityKind: "project", CanonicalName: "Atlas"})
		require.NoError(t, err)
		source := ontology.SourceHandle{Kind: ontology.EntitySource, ID: entity.EntityID, Version: int64(entity.Version)}
		service, calls := organizationFixtureService(t, f, nil, nil)
		input := ontology.OrganizationInput{OperationKey: "before-occurrence", Sources: []ontology.SourceHandle{source}}
		original, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		_, err = f.knowledge.AddEntityName(context.Background(), knowledge.AddEntityNameInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityID: entity.EntityID, DisplayName: "Atlas Platform", NameKind: "alias"})
		require.NoError(t, err)
		before := f.canonicalSnapshot(t)
		historical, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.False(t, historical.Current)
		require.Equal(t, original.AssessmentID, historical.AssessmentID)
		input.OperationKey = "after-occurrence"
		current, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, current.Current)
		require.False(t, current.Existing)
		require.NotEqual(t, original.AssessmentID, current.AssessmentID)
		require.Equal(t, "organized", current.Outcomes[0].Status)
		require.Equal(t, int32(2), calls.Load())
		require.Equal(t, 2, receiptCount(t, f))
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("definition revision and unrelated edit", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		before := f.canonicalSnapshot(t)
		service, calls := organizationFixtureService(t, f, nil, nil)
		input := ontology.OrganizationInput{OperationKey: "before-definition", Sources: []ontology.SourceHandle{source}}
		original, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		unrelated, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("unrelated-definition", original.Publication.Revision, ontology.Change{Record: testTopic("gardening")}))
		require.NoError(t, err)
		input.OperationKey = "after-unrelated"
		reused, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, reused.Existing)
		require.True(t, reused.Current)
		require.Equal(t, original.AssessmentID, reused.AssessmentID)
		require.Equal(t, int32(1), calls.Load())
		page, err := f.store.ListRecords(context.Background(), f.team, ontology.Topic, "", 20)
		require.NoError(t, err)
		var topic ontology.Record
		for _, view := range page.Records {
			if view.Definition.Key == "postgresql" {
				topic = view.Record
			}
		}
		require.NotEmpty(t, topic.ID)
		topic.Definition.Description = "PostgreSQL durable database storage"
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("revised-definition", unrelated.Revision, ontology.Change{ExpectedVersion: topic.Version, Record: topic}))
		require.NoError(t, err)
		historical, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.False(t, historical.Current)
		require.Equal(t, original.AssessmentID, historical.AssessmentID)
		input.OperationKey = "after-definition"
		current, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, current.Current)
		require.False(t, current.Existing)
		require.NotEqual(t, original.AssessmentID, current.AssessmentID)
		require.Equal(t, int32(2), calls.Load())
		require.Equal(t, 3, receiptCount(t, f))
		assignment, err := f.store.GetRecord(context.Background(), f.team, current.Outcomes[0].RecordIDs[0], 0)
		require.NoError(t, err)
		require.True(t, assignment.Current)
		require.Equal(t, topic.ID, assignment.Assignment.DefinitionID)
		require.Contains(t, assignment.Dependencies, ontology.RevisionRef{ID: topic.ID, Version: 2})
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("new override after ambiguous receipt", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		before := f.canonicalSnapshot(t)
		service, calls := organizationFixtureService(t, f, nil, ambiguousClassification)
		input := ontology.OrganizationInput{OperationKey: "before-override", Sources: []ontology.SourceHandle{source}}
		original, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.Nil(t, original.Publication)
		require.Equal(t, "ambiguous", original.Outcomes[0].Status)
		topic := testTopic("manager-chosen")
		override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, Members: []ontology.SourceHandle{source}, DefinitionID: topic.ID}, Sources: []ontology.SourceDependency{f.source(t, source)}}
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("new-manager-pin", 0, ontology.Change{Record: topic}, ontology.Change{Record: override}))
		require.NoError(t, err)
		historical, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, historical.Existing)
		require.False(t, historical.Current)
		require.Equal(t, original.AssessmentID, historical.AssessmentID)
		input.OperationKey = "after-override"
		current, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.False(t, current.Existing)
		require.True(t, current.Current)
		require.NotEqual(t, original.AssessmentID, current.AssessmentID)
		require.Equal(t, "organized", current.Outcomes[0].Status)
		require.Equal(t, int32(1), calls.Load())
		require.Equal(t, 2, receiptCount(t, f))
		assignment, err := f.store.GetRecord(context.Background(), f.team, current.Outcomes[0].RecordIDs[0], 0)
		require.NoError(t, err)
		require.Equal(t, topic.ID, assignment.Assignment.DefinitionID)
		require.True(t, assignment.Current)
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("provider and policy identity", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		before := f.canonicalSnapshot(t)
		limits := assessor.DefaultSemanticAssessmentLimits()
		service, firstCalls := organizationFixtureService(t, f, nil, ambiguousClassification)
		input := ontology.OrganizationInput{OperationKey: "identity-original", Sources: []ontology.SourceHandle{source}}
		original, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		limits.MaxOutputTokens--
		changed, changedCalls := organizationFixtureServiceWithIdentity(t, f, nil, ambiguousClassification, "new-model", limits)
		_, err = changed.Organize(context.Background(), f.team, input)
		require.ErrorIs(t, err, ontology.ErrConflict)
		input.OperationKey = "identity-changed"
		result, err := changed.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, result.Current)
		require.False(t, result.Existing)
		require.NotEqual(t, original.AssessmentID, result.AssessmentID)
		require.Equal(t, "ambiguous", result.Outcomes[0].Status)
		replay, err := changed.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, replay.Existing)
		require.Equal(t, result.AssessmentID, replay.AssessmentID)
		require.Equal(t, int32(1), firstCalls.Load())
		require.Equal(t, int32(1), changedCalls.Load())
		require.Equal(t, 2, receiptCount(t, f))
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
}

func testOntologyOrganizationCancellationReceipt(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "cancelled"
		if timeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			f := newOrganizationFixture(t)
			source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
			before := f.canonicalSnapshot(t)
			started, disconnected := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
				close(disconnected)
			}))
			defer server.Close()
			cfg := &config.Config{AIVerifierAPIURL: server.URL, AIVerifierAPIKey: "synthetic-provider-key", AIVerifierModel: "fixture-model"}
			limits := assessorprovider.SemanticAssessmentLimitsForConfig(cfg)
			provider := assessment.NewProvider(assessorprovider.NewOpenAIAssessorWithAssessmentLimits(cfg, server.Client(), limits), cfg.AIVerifierModel, limits)
			service := organization.NewService(f.store, provider)
			ctx, cancel := context.WithCancel(context.Background())
			if timeout {
				ctx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
			}
			defer cancel()
			input := ontology.OrganizationInput{OperationKey: name, Sources: []ontology.SourceHandle{source}}
			type completion struct {
				result ontology.OrganizationResult
				err    error
			}
			done := make(chan completion, 1)
			go func() { result, err := service.Organize(ctx, f.team, input); done <- completion{result, err} }()
			select {
			case <-started:
			case <-time.After(10 * time.Second):
				t.Fatal("provider request did not start")
			}
			if !timeout {
				cancel()
			}
			var finished completion
			select {
			case finished = <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("cancellation did not finish")
			}
			code, cause := "request_cancelled", context.Canceled
			if timeout {
				code, cause = "request_timeout", context.DeadlineExceeded
			}
			require.ErrorIs(t, finished.err, cause)
			require.Equal(t, code, finished.result.FailureCode)
			require.Equal(t, "failed", finished.result.Outcomes[0].Status)
			require.Nil(t, finished.result.Publication)
			require.Len(t, finished.result.Attempts, 1)
			require.Equal(t, 1, receiptCount(t, f))
			select {
			case <-disconnected:
			case <-time.After(5 * time.Second):
				t.Fatal("provider request remained connected")
			}
			replay, err := service.Organize(context.Background(), f.team, input)
			require.Error(t, err)
			require.True(t, replay.Existing)
			require.Equal(t, finished.result.AssessmentID, replay.AssessmentID)
			require.Equal(t, code, replay.FailureCode)
			require.Equal(t, before, f.canonicalSnapshot(t))
			records, err := f.store.ListRecords(context.Background(), f.team, "", "", 20)
			require.NoError(t, err)
			require.Empty(t, records.Records)
		})
	}
}
