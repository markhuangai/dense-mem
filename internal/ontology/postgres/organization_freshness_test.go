package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func testOrganizationFreshnessComparisons(t *testing.T) {
	t.Run("freshness bounds", testOrganizationFreshnessBounds)
	for _, relevant := range []bool{false, true} {
		name := "unrelated catalog change remains current"
		if relevant {
			name = "relevant candidate introduction rejects atomically"
		}
		t.Run(name, func(t *testing.T) {
			f := newOrganizationFixture(t)
			source := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
			before := f.canonicalSnapshot(t)
			key := "zzunrelatedvocabulary"
			if relevant {
				key = "postgresql"
			}
			topic := testTopic(key)
			service, calls := organizationFixtureService(t, f, nil, func(assessment.Request, *assessment.Response) {
				_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("concurrent-definition", 0, ontology.Change{Record: topic}))
				require.NoError(t, err)
			})
			input := ontology.OrganizationInput{OperationKey: "freshness-publication", Sources: []ontology.SourceHandle{source}}
			result, err := service.Organize(context.Background(), f.team, input)
			require.Equal(t, int32(1), calls.Load())
			if !relevant {
				require.NoError(t, err)
				require.True(t, result.Current)
				require.Nil(t, result.FreshnessRejection)
				return
			}
			require.ErrorIs(t, err, ontology.ErrSourceStale)
			require.Nil(t, result.Publication)
			rejection := result.FreshnessRejection
			require.NotNil(t, rejection)
			require.True(t, rejection.Valid())
			require.Equal(t, "context_hash", rejection.Check)
			require.NotEqual(t, rejection.Expected, rejection.Observed)
			require.NotNil(t, rejection.ExpectedContext)
			require.NotNil(t, rejection.ObservedContext)
			require.NotContains(t, rejection.ExpectedContext.Candidates, ontology.OrganizationContextRevision{ID: topic.ID, Version: 1, Current: true})
			require.Contains(t, rejection.ObservedContext.Candidates, ontology.OrganizationContextRevision{ID: topic.ID, Version: 1, Current: true})
			replay, err := service.Organize(context.Background(), f.team, input)
			require.ErrorContains(t, err, "stale_input")
			require.True(t, replay.Existing)
			require.Equal(t, rejection, replay.FreshnessRejection)
			require.Equal(t, int32(1), calls.Load())
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
	t.Run("current dependency revision and retired state", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
		topic := testTopic("postgresql")
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("definition", 0, ontology.Change{Record: topic}))
		require.NoError(t, err)
		service, _ := organizationFixtureService(t, f, nil, nil)
		_, err = service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "classified", Sources: []ontology.SourceHandle{source}})
		require.NoError(t, err)
		var receipt ontology.OrganizationReceipt
		require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
			var err error
			receipt, _, err = organizationByKey(tx, fence, "classified")
			return err
		}))
		check := func(receipt ontology.OrganizationReceipt, expected string) {
			t.Helper()
			require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
				rejection, err := f.store.organizationInputRejection(tx, fence, receipt)
				require.NoError(t, err)
				require.NotNil(t, rejection)
				require.Equal(t, expected, rejection.Check)
				require.True(t, rejection.Valid())
				current, err := f.store.organizationInputsCurrent(tx, fence, receipt)
				require.NoError(t, err)
				require.False(t, current)
				return nil
			}))
		}
		missing := receipt
		missing.Dependencies = []ontology.RevisionRef{{ID: uuid.NewString(), Version: 1}}
		check(missing, "dependency_missing")
		head, err := f.store.GetRecord(context.Background(), f.team, topic.ID, 0)
		require.NoError(t, err)
		page, err := f.store.ListRecords(context.Background(), f.team, ontology.Topic, "", 20)
		require.NoError(t, err)
		head.Record.Definition.Description = "Revised PostgreSQL definition"
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("revise-definition", page.Revision, ontology.Change{ExpectedVersion: head.Version, Record: head.Record}))
		require.NoError(t, err)
		check(receipt, "dependency_version")
		head, err = f.store.GetRecord(context.Background(), f.team, topic.ID, 0)
		require.NoError(t, err)
		page, err = f.store.ListRecords(context.Background(), f.team, ontology.Topic, "", 20)
		require.NoError(t, err)
		head.Record.Retired = true
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("retire-definition", page.Revision, ontology.Change{ExpectedVersion: head.Version, Record: head.Record}))
		require.NoError(t, err)
		retired := receipt
		retired.Dependencies = []ontology.RevisionRef{{ID: topic.ID, Version: head.Version + 1}}
		check(retired, "dependency_retired")
	})
	t.Run("dependency source changes during provider exchange", func(t *testing.T) {
		f := newOrganizationFixture(t)
		support := f.organizationEvidence(t, 0, "PostgreSQL supports the organization definition.", nil)
		source := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
		topic := testTopic("postgresql")
		topic.Sources = []ontology.SourceDependency{f.source(t, support)}
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("supported-definition", 0, ontology.Change{Record: topic}))
		require.NoError(t, err)
		service, _ := organizationFixtureService(t, f, nil, func(assessment.Request, *assessment.Response) {
			_, err := f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{support.ID}, Reason: "withdrawn during assessment", IdempotencyKey: "withdraw-support", RequestHash: testHash("withdraw-support")})
			require.NoError(t, err)
		})
		result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "stale-dependency", Sources: []ontology.SourceHandle{source}})
		require.ErrorIs(t, err, ontology.ErrSourceStale)
		require.Nil(t, result.Publication)
		require.NotNil(t, result.FreshnessRejection)
		require.Equal(t, "dependency_not_current", result.FreshnessRejection.Check)
		require.Equal(t, topic.ID, result.FreshnessRejection.RecordID)
		require.Equal(t, "current", result.FreshnessRejection.Expected)
		require.NotEmpty(t, result.FreshnessRejection.Observed)
	})
}

func testOrganizationFreshnessBounds(t *testing.T) {
	for _, check := range []string{"dependency_catalog_bound", "dependency_context_bound", "context_bound"} {
		t.Run(check, func(t *testing.T) {
			f := newOrganizationFixture(t)
			source := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
			topic, parent := testTopic("postgresql"), testTopic("unrelated-root")
			var revision int64
			if check != "context_bound" {
				changes := []ontology.Change{{Record: topic}}
				if check == "dependency_context_bound" {
					topic.Definition.ParentID = parent.ID
					changes = []ontology.Change{{Record: parent}, {Record: topic}}
				}
				published, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("initial-definitions", revision, changes...))
				require.NoError(t, err)
				revision = published.Revision
			}
			before := f.canonicalSnapshot(t)
			service, calls := organizationFixtureService(t, f, nil, func(_ assessment.Request, response *assessment.Response) {
				target := topic.ID
				if check == "dependency_context_bound" {
					target = parent.ID
				}
				if check == "context_bound" {
					response.Definitions = []assessment.Definition{}
					response.Items[0].Status, response.Items[0].DefinitionRef, response.Items[0].Reason = "ambiguous", "", "The topic is uncertain."
					topic.Sources = []ontology.SourceDependency{f.source(t, source)}
					published, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("new-source-context", revision, ontology.Change{Record: topic}))
					require.NoError(t, err)
					revision = published.Revision
				}
				for offset := 0; offset < ontology.MaxDependencyRecords; offset += ontology.MaxChanges {
					var changes []ontology.Change
					for range min(ontology.MaxChanges, ontology.MaxDependencyRecords-offset) {
						changes = append(changes, ontology.Change{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind,
							Override: &ontology.Override{Action: ontology.PinDefinition, TargetID: target}}})
					}
					published, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(fmt.Sprintf("concurrent-pins-%d", offset), revision, changes...))
					require.NoError(t, err)
					revision = published.Revision
				}
			})
			input := ontology.OrganizationInput{OperationKey: "bounded-publication", Sources: []ontology.SourceHandle{source}}
			result, err := service.Organize(context.Background(), f.team, input)
			require.ErrorIs(t, err, ontology.ErrSourceStale)
			require.Equal(t, int32(1), calls.Load())
			require.Nil(t, result.Publication)
			require.NotNil(t, result.FreshnessRejection)
			require.Equal(t, check, result.FreshnessRejection.Check)
			require.True(t, result.FreshnessRejection.Valid())
			replay, err := service.Organize(context.Background(), f.team, input)
			require.Error(t, err)
			require.True(t, replay.Existing)
			require.Equal(t, result.FreshnessRejection, replay.FreshnessRejection)
			require.Equal(t, int32(1), calls.Load())
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
}
