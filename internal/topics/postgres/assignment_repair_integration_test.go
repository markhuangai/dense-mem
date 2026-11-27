//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	assignment "github.com/markhuangai/dense-mem/internal/topics/assignment"
	topiccontract "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func testMaintenanceAssignmentRepair(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	const text = "The log buffers requests."
	source := f.organizationEvidence(t, 0, text, nil)
	dep := f.source(t, source)
	modified, retired, stable := testTopic("logs"), testTopic("buffering"), testTopic("requests")
	first, err := f.store.PublishAutomatic(ctx, f.team, testPublication("original-topics", 0, topiccontract.Change{Record: modified}, topiccontract.Change{Record: retired}, topiccontract.Change{Record: stable}))
	require.NoError(t, err)
	input := assignment.Scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: uuid.NewString()}
	foregroundCtx := f.actor(0, "member")
	foregroundHash := testHash("unchanged foreground shortlist")
	accepted := assignment.Outcome{Text: text, Method: "generation", InputHash: foregroundHash}
	for _, topic := range []topiccontract.Record{modified, retired, stable} {
		accepted.Matches = append(accepted.Matches, assignment.Match{ID: topic.ID, Version: 1, Quote: text})
	}
	require.NoError(t, f.rls.WithTeamProfileTx(foregroundCtx, f.app, f.team, f.owners[0], func(tx *gorm.DB) error {
		return f.store.PublishAssignmentsTx(foregroundCtx, tx, input, []assignment.Publication{{EvidenceID: source.ID, Outcome: accepted}})
	}))
	_, claim := maintenanceClaim(t, f, maintenanceWindow(t, f, maintenanceSettings(t, f)))
	require.Equal(t, foregroundHash, claim.PriorAssignmentHashes[topiccontract.SourceKey(source)])
	modified.Definition.Key, modified.Definition.Label = "peppercorn", "Peppercorn"
	retired.Retired = true
	_, err = f.store.PublishAutomatic(ctx, f.team, testPublication("changed-topics", first.Revision+1, topiccontract.Change{Record: modified, ExpectedVersion: 1}, topiccontract.Change{Record: retired, ExpectedVersion: 1}))
	require.NoError(t, err)
	foreground, err := f.store.ReadAssignmentCandidates(foregroundCtx, input, assignment.Slice{Text: text})
	require.NoError(t, err)
	require.Len(t, foreground.Topics, 1)
	require.Equal(t, stable.ID, foreground.Topics[0].ID)
	catalog, err := f.store.ReadAssignmentCandidates(ctx, input, assignment.Slice{EvidenceID: source.ID, SourceFingerprint: dep.Fingerprint, Text: text})
	require.NoError(t, err)
	require.True(t, catalog.NeedsRepair)
	require.NotEmpty(t, catalog.Topics)
	require.Equal(t, modified.ID, catalog.Topics[0].ID)
	for _, topic := range catalog.Topics {
		require.NotEqual(t, retired.ID, topic.ID)
	}
	input.OperationID = uuid.NewString()
	reassessed := assignment.Outcome{Text: text, Method: "generation", InputHash: testHash("reassessed"), Reviewed: catalog.Topics}
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		return f.store.PublishAssignmentsTx(ctx, tx, input, []assignment.Publication{{EvidenceID: source.ID, ExpectedFingerprint: dep.Fingerprint, Outcome: reassessed}})
	}))
	require.NoError(t, f.store.withScope(ctx, f.team, true, func(tx *gorm.DB, fence scope) error {
		memberships, err := assignmentMemberships(tx, fence, source.ID)
		if err != nil {
			return err
		}
		require.True(t, memberships[modified.ID].Retired)
		require.True(t, memberships[retired.ID].Retired)
		require.False(t, memberships[stable.ID].Retired)
		require.EqualValues(t, 1, memberships[stable.ID].Version)
		return nil
	}))

	t.Run("new matching topic leaves a dispatched shortlist valid", func(t *testing.T) {
		f := newOrganizationFixture(t)
		const text = "PostgreSQL stores records."
		source := f.organizationEvidence(t, 0, text, nil)
		dep := f.source(t, source)
		window := maintenanceWindow(t, f, concurrencyConfig(t, f, 2, 2))
		_, claim := maintenanceClaim(t, f, window)
		require.Equal(t, []topiccontract.SourceHandle{source}, claim.Sources)
		ctx := topiccontract.WithMaintenanceClaim(t.Context(), *claim)
		input := assignment.Scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: claim.ID}
		candidates, err := f.store.ReadAssignmentCandidates(ctx, input, assignment.Slice{EvidenceID: source.ID, SourceFingerprint: dep.Fingerprint, Text: text})
		require.NoError(t, err)
		require.Empty(t, candidates.Topics)

		topic := testTopic("postgresql")
		revalidationPublish(t, f, topiccontract.Change{Record: topic})
		revalidationDiscover(t, f, window)
		queued := revalidationQueue(t, f, source)
		require.Equal(t, queued.Revision, claim.Revisions[topiccontract.SourceKey(source)])

		outcome := assignment.Outcome{Text: text, Method: "generation", InputHash: testHash("old shortlist")}
		committed, err := f.store.CommitMaintenanceItemAssignment(ctx, *claim, assignment.Publication{EvidenceID: source.ID, ExpectedFingerprint: dep.Fingerprint, Outcome: outcome})
		require.NoError(t, err)
		require.Equal(t, "uncategorized", committed.Status)
		var outcomeCount int64
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_assignment_outcomes WHERE team_id=?::uuid AND operation_id=?::uuid`, f.team, claim.ID).Row().Scan(&outcomeCount))
		require.EqualValues(t, 1, outcomeCount)
		require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, topiccontract.OrganizationResult{Current: true, IsolatedOutcomes: true, Outcomes: []topiccontract.OrganizationOutcome{committed}}, "", time.Now()))
		require.Equal(t, "uncategorized", revalidationQueue(t, f, source).Status)
	})
}
