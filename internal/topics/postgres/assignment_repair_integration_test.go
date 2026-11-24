//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	assignment "github.com/markhuangai/dense-mem/internal/topicassignment/contract"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
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
	first, err := f.store.PublishAutomatic(ctx, f.team, testPublication("original-topics", 0, ontology.Change{Record: modified}, ontology.Change{Record: retired}, ontology.Change{Record: stable}))
	require.NoError(t, err)
	input := assignment.Scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: uuid.NewString()}
	write := func(outcome assignment.Outcome, fingerprint string) error {
		return f.rls.WithTeamTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
			return f.store.PublishAssignmentsTx(ctx, tx, input, []assignment.Publication{{EvidenceID: source.ID, ExpectedFingerprint: fingerprint, Outcome: outcome}})
		})
	}
	foregroundHash := testHash("unchanged foreground shortlist")
	accepted := assignment.Outcome{Text: text, Method: "generation", InputHash: foregroundHash}
	for _, topic := range []ontology.Record{modified, retired, stable} {
		accepted.Matches = append(accepted.Matches, assignment.Match{ID: topic.ID, Version: 1, Quote: text})
	}
	require.NoError(t, write(accepted, ""))
	_, claim := maintenanceClaim(t, f, maintenanceWindow(t, f, maintenanceSettings(t, f)))
	require.Equal(t, foregroundHash, claim.PriorAssignmentHashes[ontology.SourceKey(source)])
	modified.Definition.Key, modified.Definition.Label = "peppercorn", "Peppercorn"
	retired.Retired = true
	_, err = f.store.PublishAutomatic(ctx, f.team, testPublication("changed-topics", first.Revision+1, ontology.Change{Record: modified, ExpectedVersion: 1}, ontology.Change{Record: retired, ExpectedVersion: 1}))
	require.NoError(t, err)
	foreground, err := f.store.ReadAssignmentCandidates(ctx, input, assignment.Slice{Text: text})
	require.NoError(t, err)
	for _, topic := range foreground.Topics {
		require.NotEqual(t, modified.ID, topic.ID)
	}
	catalog, err := f.store.ReadAssignmentCandidates(ctx, input, assignment.Slice{EvidenceID: source.ID, SourceFingerprint: dep.Fingerprint, Text: text})
	require.NoError(t, err)
	require.True(t, catalog.NeedsRepair)
	require.NotEmpty(t, catalog.Topics)
	require.Equal(t, modified.ID, catalog.Topics[0].ID)
	for _, topic := range catalog.Topics {
		require.NotEqual(t, retired.ID, topic.ID)
	}
	input.OperationID = uuid.NewString()
	require.NoError(t, write(assignment.Outcome{Text: text, Method: "generation", InputHash: testHash("reassessed"), Reviewed: catalog.Topics}, dep.Fingerprint))
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
}
