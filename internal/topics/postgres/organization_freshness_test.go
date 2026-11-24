package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	topiccontract "github.com/markhuangai/dense-mem/internal/topics/contract"
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
				_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("concurrent-definition", 0, topiccontract.Change{Record: topic}))
				require.NoError(t, err)
			})
			input := topiccontract.OrganizationInput{OperationKey: "freshness-publication", Sources: []topiccontract.SourceHandle{source}}
			result, err := service.Organize(context.Background(), f.team, input)
			require.Equal(t, int32(1), calls.Load())
			if !relevant {
				require.NoError(t, err)
				require.True(t, result.Current)
				require.Nil(t, result.FreshnessRejection)
				return
			}
			require.ErrorIs(t, err, topiccontract.ErrSourceStale)
			require.Nil(t, result.Publication)
			rejection := result.FreshnessRejection
			require.NotNil(t, rejection)
			require.True(t, rejection.Valid())
			require.Equal(t, "context_hash", rejection.Check)
			require.NotEqual(t, rejection.Expected, rejection.Observed)
			require.NotNil(t, rejection.ExpectedContext)
			require.NotNil(t, rejection.ObservedContext)
			require.NotContains(t, rejection.ExpectedContext.Candidates, topiccontract.OrganizationContextRevision{ID: topic.ID, Version: 1, Current: true})
			require.Contains(t, rejection.ObservedContext.Candidates, topiccontract.OrganizationContextRevision{ID: topic.ID, Version: 1, Current: true})
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
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("definition", 0, topiccontract.Change{Record: topic}))
		require.NoError(t, err)
		service, _ := organizationFixtureService(t, f, nil, nil)
		_, err = service.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: "classified", Sources: []topiccontract.SourceHandle{source}})
		require.NoError(t, err)
		var receipt topiccontract.OrganizationReceipt
		require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
			var err error
			receipt, _, err = organizationByKey(tx, fence, "classified", false)
			return err
		}))
		check := func(receipt topiccontract.OrganizationReceipt, expected string) {
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
		missing.Dependencies = []topiccontract.RevisionRef{{ID: uuid.NewString(), Version: 1}}
		check(missing, "dependency_missing")
		head, err := f.store.GetRecord(context.Background(), f.team, topic.ID, 0)
		require.NoError(t, err)
		page, err := f.store.ListRecords(context.Background(), f.team, topiccontract.Topic, "", 20)
		require.NoError(t, err)
		head.Record.Definition.Description = "Revised PostgreSQL definition"
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("revise-definition", page.Revision, topiccontract.Change{ExpectedVersion: head.Version, Record: head.Record}))
		require.NoError(t, err)
		check(receipt, "dependency_version")
		head, err = f.store.GetRecord(context.Background(), f.team, topic.ID, 0)
		require.NoError(t, err)
		page, err = f.store.ListRecords(context.Background(), f.team, topiccontract.Topic, "", 20)
		require.NoError(t, err)
		head.Record.Retired = true
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("retire-definition", page.Revision, topiccontract.Change{ExpectedVersion: head.Version, Record: head.Record}))
		require.NoError(t, err)
		retired := receipt
		retired.Dependencies = []topiccontract.RevisionRef{{ID: topic.ID, Version: head.Version + 1}}
		check(retired, "dependency_retired")
	})
	t.Run("dependency source changes during provider exchange", func(t *testing.T) {
		f := newOrganizationFixture(t)
		support := f.organizationEvidence(t, 0, "PostgreSQL supports the organization definition.", nil)
		source := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
		topic := testTopic("postgresql")
		topic.Sources = []topiccontract.SourceDependency{f.source(t, support)}
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("supported-definition", 0, topiccontract.Change{Record: topic}))
		require.NoError(t, err)
		service, _ := organizationFixtureService(t, f, nil, func(assessment.Request, *assessment.Response) {
			_, err := f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{support.ID}, Reason: "withdrawn during assessment", IdempotencyKey: "withdraw-support", RequestHash: testHash("withdraw-support")})
			require.NoError(t, err)
		})
		result, err := service.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: "stale-dependency", Sources: []topiccontract.SourceHandle{source}})
		require.ErrorIs(t, err, topiccontract.ErrSourceStale)
		require.Nil(t, result.Publication)
		require.NotNil(t, result.FreshnessRejection)
		require.Equal(t, "dependency_not_current", result.FreshnessRejection.Check)
		require.Equal(t, topic.ID, result.FreshnessRejection.RecordID)
		require.Equal(t, "current", result.FreshnessRejection.Expected)
		require.NotEmpty(t, result.FreshnessRejection.Observed)
	})
}

func testOrganizationFreshnessBounds(t *testing.T) {
	f := newOrganizationFixture(t)
	source := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
	dependency := f.source(t, source)
	before := f.canonicalSnapshot(t)
	service, calls := organizationFixtureService(t, f, nil, func(_ assessment.Request, response *assessment.Response) {
		response.Definitions = []assessment.Definition{}
		response.Items[0].Status, response.Items[0].DefinitionRef, response.Items[0].Reason = "ambiguous", "", "The topic is uncertain."
		var revision int64
		const topics = topiccontract.MaxDependencyRecords + 1
		for offset := 0; offset < topics; offset += topiccontract.MaxChanges {
			var changes []topiccontract.Change
			for i := offset; i < min(offset+topiccontract.MaxChanges, topics); i++ {
				topic := testTopic(fmt.Sprintf("context-%d", i))
				topic.Sources = []topiccontract.SourceDependency{dependency}
				changes = append(changes, topiccontract.Change{Record: topic})
			}
			published, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(fmt.Sprintf("concurrent-topics-%d", offset), revision, changes...))
			require.NoError(t, err)
			revision = published.Revision
		}
	})
	input := topiccontract.OrganizationInput{OperationKey: "bounded-publication", Sources: []topiccontract.SourceHandle{source}}
	result, err := service.Organize(context.Background(), f.team, input)
	require.ErrorIs(t, err, topiccontract.ErrSourceStale)
	require.Equal(t, int32(1), calls.Load())
	require.Nil(t, result.Publication)
	require.NotNil(t, result.FreshnessRejection)
	require.Equal(t, "context_bound", result.FreshnessRejection.Check)
	require.True(t, result.FreshnessRejection.Valid())
	replay, err := service.Organize(context.Background(), f.team, input)
	require.Error(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, result.FreshnessRejection, replay.FreshnessRejection)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, before, f.canonicalSnapshot(t))
}
