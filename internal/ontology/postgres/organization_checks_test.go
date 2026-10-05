package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func testOntologyOrganizationAtomicReplayAndIsolation(t *testing.T) {
	f := newOrganizationFixture(t)
	a := f.organizationEvidence(t, 0, "Atlas stores data in PostgreSQL.", nil)
	b := f.organizationEvidence(t, 1, "PostgreSQL is Atlas's data store.", nil)
	before := f.canonicalSnapshot(t)
	service, calls := organizationFixtureService(t, f, func(assessment.Item, assessment.Item) bool { return true }, nil)
	input := ontology.OrganizationInput{OperationKey: "organize", Sources: []ontology.SourceHandle{b, a}}
	result, err := service.Organize(context.Background(), f.team, input)
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
	require.NotNil(t, result.Publication)
	require.True(t, result.Current)
	groups, err := f.store.ListRecords(context.Background(), f.team, ontology.EvidenceGroup, "", 20)
	require.NoError(t, err)
	require.Len(t, groups.Records, 1)
	require.True(t, groups.Records[0].Current)
	require.Len(t, groups.Records[0].Group.Members, 2)
	require.Equal(t, result.AssessmentID, groups.Records[0].Group.AssessmentID)
	replay, err := service.Organize(context.Background(), f.team, input)
	require.NoError(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, result.AssessmentID, replay.AssessmentID)
	require.Equal(t, int32(1), calls.Load())
	input.OperationKey = "another-key"
	input.Sources = []ontology.SourceHandle{a, b}
	replay, err = service.Organize(context.Background(), f.team, input)
	require.NoError(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, result.AssessmentID, replay.AssessmentID)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, 2, receiptCount(t, f))
	input.Sources = input.Sources[:1]
	_, err = service.Organize(context.Background(), f.team, input)
	require.ErrorIs(t, err, ontology.ErrConflict)
	_, err = service.Organize(f.actor(1, "member"), f.team, ontology.OrganizationInput{OperationKey: "request-mode", Sources: []ontology.SourceHandle{a}})
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	other := &domain.Team{Name: "organization-c-" + uuid.NewString()}
	require.NoError(t, access.NewTeamRepository(f.admin, f.rls).Create(context.Background(), other))
	var count int
	require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, other.ID.String(), func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM ontology_assessments WHERE team_id=?::uuid`, f.team).Row().Scan(&count)
	}))
	require.Zero(t, count)
	_, err = service.Organize(context.Background(), other.ID.String(), ontology.OrganizationInput{OperationKey: "foreign-source", Sources: []ontology.SourceHandle{a}})
	require.NoError(t, err)
	require.Equal(t, int32(1), calls.Load())
	require.Equal(t, before, f.canonicalSnapshot(t))
	require.Error(t, f.admin.Exec(`UPDATE ontology_assessments SET body='{}'::jsonb WHERE team_id=?::uuid`, f.team).Error)
	require.Error(t, f.admin.Exec(`DELETE FROM ontology_assessments WHERE team_id=?::uuid`, f.team).Error)
	migrator, err := storage.NewMigrator(f.admin)
	require.NoError(t, err)
	require.ErrorContains(t, migrator.RunDown(context.Background()), "cannot roll back populated ontology assessments")
}

func testOntologyOrganizationRejectsStaleAndMalformedPublication(t *testing.T) {
	t.Run("stale source", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		service, calls := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
			_, err := f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{source.ID}, Reason: "withdrawn during assessment", IdempotencyKey: "withdraw", RequestHash: testHash("withdraw")})
			require.NoError(t, err)
		})
		result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "stale", Sources: []ontology.SourceHandle{source}})
		require.Error(t, err)
		require.Equal(t, "stale_input", result.FailureCode)
		require.Equal(t, int32(1), calls.Load())
		require.Equal(t, 1, receiptCount(t, f))
		page, err := f.store.ListRecords(context.Background(), f.team, "", "", 20)
		require.NoError(t, err)
		require.Empty(t, page.Records)
	})
	t.Run("invalid complete response", func(t *testing.T) {
		f := newOrganizationFixture(t)
		a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		b := f.organizationEvidence(t, 1, "Atlas uses Redis.", nil)
		before := f.canonicalSnapshot(t)
		service, calls := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) { response.Items = response.Items[:1] })
		input := ontology.OrganizationInput{OperationKey: "invalid", Sources: []ontology.SourceHandle{a, b}}
		result, err := service.Organize(context.Background(), f.team, input)
		require.Error(t, err)
		require.Equal(t, "provider_response_invalid", result.FailureCode)
		require.Equal(t, int32(3), calls.Load())
		require.Len(t, result.Attempts, 3)
		page, err := f.store.ListRecords(context.Background(), f.team, "", "", 20)
		require.NoError(t, err)
		require.Empty(t, page.Records)
		require.Equal(t, before, f.canonicalSnapshot(t))
		require.Equal(t, 1, receiptCount(t, f))
		_, err = service.Organize(context.Background(), f.team, input)
		require.Error(t, err)
		require.Equal(t, int32(3), calls.Load())
	})
	t.Run("revision race", func(t *testing.T) {
		f := newOrganizationFixture(t)
		a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		b := f.organizationEvidence(t, 1, "Beacon uses Redis.", nil)
		barrier := make(chan struct{})
		var arrived sync.WaitGroup
		arrived.Add(2)
		service, _ := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) { arrived.Done(); <-barrier })
		results := make(chan error, 2)
		var completed sync.WaitGroup
		for _, source := range []ontology.SourceHandle{a, b} {
			completed.Add(1)
			go func(source ontology.SourceHandle) {
				defer completed.Done()
				_, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: source.ID, Sources: []ontology.SourceHandle{source}})
				results <- err
			}(source)
		}
		arrived.Wait()
		close(barrier)
		completed.Wait()
		close(results)
		passed, conflicts := 0, 0
		for err := range results {
			if err == nil {
				passed++
			} else if errors.Is(err, ontology.ErrConflict) || errors.Is(err, ontology.ErrSourceStale) {
				conflicts++
			} else {
				t.Fatal(err)
			}
		}
		require.Equal(t, 1, passed)
		require.Equal(t, 1, conflicts)
		require.Equal(t, 2, receiptCount(t, f))
	})
}

func testOntologyOrganizationOverridesAndIncompleteGroups(t *testing.T) {
	f := newOrganizationFixture(t)
	a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
	b := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
	separation := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.KeepSeparate, Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("separate", 0, ontology.Change{Record: separation}))
	require.NoError(t, err)
	service, calls := organizationFixtureService(t, f, func(assessment.Item, assessment.Item) bool { return true }, nil)
	input := ontology.OrganizationInput{OperationKey: "honor-separation", Sources: []ontology.SourceHandle{a, b}}
	_, err = service.Organize(context.Background(), f.team, input)
	require.NoError(t, err)
	groups, err := f.store.ListRecords(context.Background(), f.team, ontology.EvidenceGroup, "", 20)
	require.NoError(t, err)
	require.Empty(t, groups.Records)
	beforeCalls := calls.Load()
	view, err := f.store.GetRecord(context.Background(), f.team, separation.ID, 0)
	require.NoError(t, err)
	require.False(t, view.Retired)
	input.OperationKey = "repeated"
	_, err = service.Organize(context.Background(), f.team, input)
	require.NoError(t, err)
	require.Equal(t, beforeCalls, calls.Load())
	c := f.organizationEvidence(t, 0, "Atlas stores data in PostgreSQL.", nil)
	d := f.organizationEvidence(t, 1, "PostgreSQL is Atlas's data store.", nil)
	result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "group", Sources: []ontology.SourceHandle{c, d}})
	require.NoError(t, err)
	require.NotNil(t, result.Publication)
	beforeCalls = calls.Load()
	result, err = service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "incomplete", Sources: []ontology.SourceHandle{c}})
	require.NoError(t, err)
	require.Equal(t, "ambiguous", result.Outcomes[0].Status)
	require.Equal(t, "resubmit_complete_group", result.Outcomes[0].Reason)
	require.Equal(t, beforeCalls, calls.Load())
	_, err = service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "unavailable", Sources: []ontology.SourceHandle{{Kind: ontology.EvidenceSource, ID: uuid.NewString(), Version: 1}}})
	require.NoError(t, err)
	_, err = service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "too-many", Sources: append(make([]ontology.SourceHandle, 20), a)})
	require.ErrorIs(t, err, ontology.ErrInvalid)
}

func testOntologyOrganizationRejectsSplitGroupIdentityConflict(t *testing.T) {
	f := newOrganizationFixture(t)
	handles := []ontology.SourceHandle{}
	for i := 0; i < 4; i++ {
		handles = append(handles, f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil))
	}
	service, calls := organizationFixtureService(t, f, nil, nil)
	input := ontology.OrganizationInput{OperationKey: "original-four-member-group", Sources: handles}
	original, err := service.Organize(context.Background(), f.team, input)
	require.NoError(t, err)
	groups, err := f.store.ListRecords(context.Background(), f.team, ontology.EvidenceGroup, "", 20)
	require.NoError(t, err)
	require.Len(t, groups.Records, 1)
	group := groups.Records[0].Record
	require.Len(t, group.Group.Members, 4)
	changes := []ontology.Change{}
	for _, pair := range [][2]int{{0, 1}, {0, 3}, {2, 1}, {2, 3}} {
		a, b := handles[pair[0]], handles[pair[1]]
		record := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.KeepSeparate, Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
		changes = append(changes, ontology.Change{Record: record})
	}
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("split-four-member-group", original.Publication.Revision, changes...))
	require.NoError(t, err)
	before := f.canonicalSnapshot(t)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	input.OperationKey = "reject-conflicting-group-reuse"
	result, err := service.Organize(context.Background(), f.team, input)
	require.ErrorIs(t, err, ontology.ErrConflict)
	require.Equal(t, "commit_conflict", result.FailureCode)
	require.Nil(t, result.Publication)
	for _, outcome := range result.Outcomes {
		require.Equal(t, "failed", outcome.Status)
		require.Empty(t, outcome.RecordIDs)
	}
	afterHistory, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Equal(t, history, afterHistory)
	stored, err := f.store.GetRecord(context.Background(), f.team, group.ID, 0)
	require.NoError(t, err)
	require.Equal(t, group.Group, stored.Group)
	require.Equal(t, group.Version, stored.Version)
	require.False(t, stored.Current)
	require.Equal(t, 2, receiptCount(t, f))
	priorCalls := calls.Load()
	replay, err := service.Organize(context.Background(), f.team, input)
	require.Error(t, err)
	require.True(t, replay.Existing)
	require.Equal(t, result.AssessmentID, replay.AssessmentID)
	require.Equal(t, priorCalls, calls.Load())
	require.Equal(t, before, f.canonicalSnapshot(t))
}
