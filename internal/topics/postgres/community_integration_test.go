//go:build integration

package postgres

import (
	"testing"
	"time"

	"github.com/google/uuid"
	assignment "github.com/markhuangai/dense-mem/internal/topicassignment/contract"
	"github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCommunityPlacementChecksRevisionsAndAccountsForProviderWork(t *testing.T) {
	ctx := t.Context()
	f := newOntologyFixture(t)
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
	child := testTopic("postgresql-backup")
	child.ID = "10000000-0000-4000-8000-000000000001"
	child.Definition.Label = "PostgreSQL backup"
	parent := testTopic("postgresql")
	parent.ID = "10000000-0000-4000-8000-000000000002"
	parent.Definition.Label = "PostgreSQL"
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("community-seed", 0,
		contract.Change{Record: child}, contract.Change{Record: parent}))
	require.NoError(t, err)

	claim := func() (contract.CommunityInput, contract.MaintenanceClaim) {
		t.Helper()
		turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
		require.NoError(t, err)
		require.NotNil(t, turn)
		input, batch, err := f.store.ClaimCommunity(ctx, *turn, window.ID, time.Now())
		require.NoError(t, err)
		require.NotNil(t, input)
		require.NotNil(t, batch)
		return *input, *batch
	}

	input, batch := claim()
	require.Equal(t, child.ID, input.Topic.ID)
	require.Len(t, input.Candidates, 1)
	require.Equal(t, parent.ID, input.Candidates[0].ID)
	attempt := uuid.NewString()
	require.NoError(t, f.store.AdmitMaintenanceAttempt(ctx, batch, attempt, 1, 100, 200, time.Now()))
	require.NoError(t, f.store.ReconcileMaintenanceAttempt(ctx, batch, attempt, contract.AssessmentAttempt{Number: 1}))
	require.NoError(t, f.store.CompleteCommunity(ctx, batch, input, contract.CommunityOutcome{
		InputHash: "sha256:placement", Method: "generation", Status: "placed", ParentID: parent.ID,
	}, "", time.Now()))
	visible, err := f.store.GetRecord(f.actor(1, "member"), f.team, child.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "placed", visible.CommunityPlacement)
	require.Equal(t, parent.ID, visible.CommunityParentID)

	view, err := f.store.GetRecord(f.actor(0, "manager"), f.team, parent.ID, 0)
	require.NoError(t, err)
	view.Definition.Description = "Database administration and operations."
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("change-parent", 1,
		contract.Change{Record: view.Record, ExpectedVersion: 1}))
	require.NoError(t, err)
	input, batch = claim()
	require.Equal(t, child.ID, input.Topic.ID)
	require.Len(t, input.Candidates, 1)
	view, err = f.store.GetRecord(f.actor(0, "manager"), f.team, parent.ID, 0)
	require.NoError(t, err)
	view.Retired = true
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("retire-parent", 2,
		contract.Change{Record: view.Record, ExpectedVersion: 2}))
	require.NoError(t, err)
	err = f.store.CompleteCommunity(ctx, batch, input, contract.CommunityOutcome{
		InputHash: "sha256:stale-placement", Method: "decision", Status: "placed", ParentID: parent.ID,
	}, "", time.Now())
	require.ErrorIs(t, err, contract.ErrSourceStale)
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, batch, contract.OrganizationResult{}, "source_stale", time.Now()))
	input, batch = claim()
	require.Equal(t, child.ID, input.Topic.ID)
	require.Empty(t, input.Candidates)
	require.NoError(t, f.store.CompleteCommunity(ctx, batch, input, contract.CommunityOutcome{
		InputHash: "sha256:standalone", Method: "deterministic", Status: "standalone",
	}, "", time.Now()))
	status, err := f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Communities.Standalone)
	require.Zero(t, status.ActiveWork)
	require.Zero(t, status.Usage.UnreconciledAttempts)
	require.EqualValues(t, 1, status.Usage.UnavailableUsageAttempts)
}

func TestConcurrentTopicProposalsReuseScopedName(t *testing.T) {
	ctx := t.Context()
	f := newOntologyFixture(t)
	sources := []struct {
		id, content string
	}{
		{content: "PostgreSQL backups retain seven daily restore points."},
		{content: "Test PostgreSQL backups after changing storage providers."},
	}
	for i := range sources {
		sources[i].id = f.organizationEvidence(t, i, sources[i].content, nil).ID
	}
	start := make(chan struct{})
	result := make(chan error, len(sources))
	for _, source := range sources {
		go func() {
			<-start
			outcome := assignment.Outcome{
				Text: source.content, InputHash: "sha256:proposal", Method: "generation",
				Proposals: []assignment.Proposal{{Label: "PostgreSQL backups", Description: "Backing up a PostgreSQL database", Quote: "PostgreSQL backups"}},
			}
			result <- f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
				return f.store.PublishAssignmentsTx(ctx, tx, assignment.Scope{
					TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: uuid.NewString(),
				}, []assignment.Publication{{EvidenceID: source.id, Outcome: outcome}})
			})
		}()
	}
	close(start)
	for range sources {
		require.NoError(t, <-result)
	}
	var topics, members, distinct int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FILTER(WHERE kind='topic'),
		count(*) FILTER(WHERE kind='assignment'),count(DISTINCT topic_id)
		FROM topic_record_heads WHERE team_id=?::uuid AND NOT retired`, f.team).Row().Scan(&topics, &members, &distinct))
	require.Equal(t, 1, topics)
	require.Equal(t, 2, members)
	require.Equal(t, 1, distinct)
}
