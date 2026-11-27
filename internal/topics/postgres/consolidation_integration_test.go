//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	semantic "github.com/markhuangai/dense-mem/internal/semanticwrite/contract"
	"github.com/markhuangai/dense-mem/internal/topics/assignment"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestTopicConsolidationPreservesMembershipsAndHierarchy(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	_, err := searchpostgres.NewStore(f.admin, f.rls).EnsureActiveSearchContract(ctx, searchmaintenance.EnsureActiveSearchContractInput{Model: "fixture-embedding", Dimensions: 3})
	require.NoError(t, err)
	winner, loser, child, parent := testTopic("storage"), testTopic("database-storage"), testTopic("storage-backup"), testTopic("platform")
	winner.ID, loser.ID, child.ID, parent.ID = "10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002", "10000000-0000-4000-8000-000000000003", "10000000-0000-4000-8000-000000000004"
	manager := f.actor(0, "manager")
	initial, err := f.store.PublishManager(manager, f.team, testPublication("consolidation-winner", 0, topic.Change{Record: winner}))
	require.NoError(t, err)
	created, err := f.store.PublishAutomatic(ctx, f.team, testPublication("consolidation-loser", initial.Revision, topic.Change{Record: loser}))
	require.NoError(t, err)
	child.Definition.ParentID = loser.ID
	_, err = f.store.PublishManager(manager, f.team, testPublication("consolidation-child", created.Revision, topic.Change{Record: child}, topic.Change{Record: parent}))
	require.NoError(t, err)
	require.NoError(t, f.admin.Exec(`UPDATE topics SET placement='placed',parent_topic_id=?::uuid,version=version+1 WHERE team_id=?::uuid AND topic_id=?::uuid`, parent.ID, f.team, winner.ID).Error)
	first := f.organizationEvidence(t, 0, "First storage observation.", nil)
	second := f.organizationEvidence(t, 0, "Second storage observation.", nil)
	survivorOnly := f.organizationEvidence(t, 0, "Survivor-only storage observation.", nil)
	input := assignment.Scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: uuid.NewString()}
	items := []assignment.Publication{
		{EvidenceID: first.ID, Outcome: assignment.Outcome{Text: "First storage observation.", InputHash: testHash("first"), Method: "decision", Matches: []assignment.Match{{ID: winner.ID, Version: 1}, {ID: loser.ID, Version: 1}}}},
		{EvidenceID: second.ID, Outcome: assignment.Outcome{Text: "Second storage observation.", InputHash: testHash("second"), Method: "decision", Matches: []assignment.Match{{ID: loser.ID, Version: 1}}}},
		{EvidenceID: survivorOnly.ID, Outcome: assignment.Outcome{Text: "Survivor-only storage observation.", InputHash: testHash("survivor"), Method: "decision", Matches: []assignment.Match{{ID: winner.ID, Version: 1}}}},
	}
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error { return f.store.PublishAssignmentsTx(ctx, tx, input, items) }))
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
	revalidationDiscover(t, f, window)
	require.NoError(t, f.admin.Exec(`UPDATE topic_maintenance_sources SET status='organized' WHERE team_id=?::uuid AND source_id=?`, f.team, survivorOnly.ID).Error)
	priorQueue := revalidationQueue(t, f, survivorOnly)
	acceptedID := topic.OrganizationRecordID(f.team, topic.AssignmentKind, topic.SourceKey(first)+":"+winner.ID)
	before, err := f.store.GetRecord(manager, f.team, acceptedID, 0)
	require.NoError(t, err)
	require.True(t, before.Current)

	pairs, claim := consolidationFixtureClaim(t, f)
	var selected topic.TopicConsolidationPair
	for _, pair := range pairs {
		if pair.Left.ID == winner.ID && pair.Right.ID == loser.ID {
			selected = pair
		}
	}
	require.NotEmpty(t, selected.Left.ID)
	merged, err := f.store.CompleteTopicConsolidationPair(ctx, *claim, selected, true)
	require.NoError(t, err)
	require.Equal(t, winner.ID, merged.SurvivorID)
	require.Equal(t, loser.ID, merged.RetiredID)
	require.Equal(t, 1, merged.MovedMemberships)
	require.Equal(t, 2, merged.RetiredMemberships)
	require.Equal(t, 1, merged.ReparentedChildren)
	require.NoError(t, f.store.FinishTopicConsolidation(ctx, *claim, true))
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, topic.OrganizationResult{}, "", time.Now()))
	var survivorMarkers int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_maintenance_markers WHERE team_id=?::uuid AND anchor_kind='topic_record_heads' AND anchor_id=?`, f.team, winner.ID).Row().Scan(&survivorMarkers))
	require.Zero(t, survivorMarkers)
	revalidationDiscover(t, f, window)
	require.Equal(t, priorQueue, revalidationQueue(t, f, survivorOnly), "alias enrichment must not requeue survivor members after markers drain")
	after, err := f.store.GetRecord(manager, f.team, acceptedID, 0)
	require.NoError(t, err)
	require.True(t, after.Current)
	require.Equal(t, before.Version, after.Version)
	childView, err := f.store.GetRecord(manager, f.team, child.ID, 0)
	require.NoError(t, err)
	require.Equal(t, winner.ID, childView.Definition.ParentID)
	winnerView, err := f.store.GetRecord(manager, f.team, winner.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "placed", winnerView.CommunityPlacement)
	require.Equal(t, parent.ID, winnerView.CommunityParentID)
	var count int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_memberships WHERE team_id=?::uuid AND topic_id=?::uuid AND NOT retired`, f.team, winner.ID).Row().Scan(&count))
	require.Equal(t, 3, count)
}

func TestTopicConsolidationConcurrentPublishers(t *testing.T) {
	for _, writer := range []string{"remember", "manager"} {
		t.Run(writer, func(t *testing.T) {
			f := newOrganizationFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			managerCtx, cancelManager := context.WithTimeout(f.actor(0, "manager"), 20*time.Second)
			defer cancelManager()
			_, err := searchpostgres.NewStore(f.admin, f.rls).EnsureActiveSearchContract(ctx, searchmaintenance.EnsureActiveSearchContractInput{Model: "fixture-embedding", Dimensions: 3})
			require.NoError(t, err)
			winner, loser := testTopic("race-winner"), testTopic("race-loser")
			winner.ID, loser.ID = "10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002"
			initial, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("race-winner", 0, topic.Change{Record: winner}))
			require.NoError(t, err)
			_, err = f.store.PublishAutomatic(ctx, f.team, testPublication("race-loser", initial.Revision, topic.Change{Record: loser}))
			require.NoError(t, err)
			source := f.organizationEvidence(t, 0, "Concurrent storage observation.", nil)
			pairs, claim := consolidationFixtureClaim(t, f)
			require.Len(t, pairs, 1)
			start := make(chan struct{})
			merged := make(chan error, 1)
			published := make(chan error, 1)
			go func() {
				<-start
				_, err := f.store.CompleteTopicConsolidationPair(ctx, *claim, pairs[0], true)
				merged <- err
			}()
			go func() {
				<-start
				if writer == "manager" {
					definition := *loser.Definition
					definition.Description = "Manager changed the candidate during consolidation."
					_, err := managementService(f).SaveEdits(managerCtx, topic.SaveEditsInput{OperationKey: uuid.NewString(), Reason: "Race manager save with consolidation", Edits: []topic.ManagementEdit{{Action: "define", RecordID: loser.ID, ExpectedVersion: 1, Kind: topic.Topic, Definition: &definition}}})
					published <- err
					return
				}
				scope := assignment.Scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: uuid.NewString()}
				item := assignment.Publication{EvidenceID: source.ID, Outcome: assignment.Outcome{Text: "Concurrent storage observation.", Method: "decision", InputHash: testHash("race"), Matches: []assignment.Match{{ID: loser.ID, Version: 1}}}}
				published <- f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
					return f.store.PublishAssignmentsTx(ctx, tx, scope, []assignment.Publication{item})
				})
			}()
			close(start)
			mergeErr, publishErr := <-merged, <-published
			require.NoError(t, ctx.Err(), "concurrent production calls must not deadlock")
			require.NoError(t, managerCtx.Err(), "concurrent manager save must not deadlock")
			if writer == "manager" {
				require.True(t, (mergeErr == nil && errors.Is(publishErr, topic.ErrNotFound)) || (publishErr == nil && errors.Is(mergeErr, topic.ErrSourceStale)), "merge=%v save=%v", mergeErr, publishErr)
			} else {
				require.NoError(t, mergeErr)
				require.True(t, publishErr == nil || errors.Is(publishErr, assignment.ErrStale), "publication=%v", publishErr)
			}
			var phantom int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_memberships AS member JOIN topics AS target USING(team_id,shared_space_id,space_generation,topic_id) WHERE member.team_id=?::uuid AND NOT member.retired AND target.retired`, f.team).Row().Scan(&phantom))
			require.Zero(t, phantom)
			if writer == "remember" && publishErr == nil {
				var moved int
				require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_memberships WHERE team_id=?::uuid AND evidence_id=?::uuid AND topic_id=?::uuid AND NOT retired`, f.team, source.ID, winner.ID).Row().Scan(&moved))
				require.Equal(t, 1, moved)
			}
			require.NoError(t, f.store.FinishTopicConsolidation(ctx, *claim, mergeErr == nil))
			require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, topic.OrganizationResult{}, "", time.Now()))
		})
	}
}

func consolidationFixtureClaim(t *testing.T, f *ontologyFixture) ([]topic.TopicConsolidationPair, *topic.MaintenanceClaim) {
	t.Helper()
	ctx := context.Background()
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
	turn, err := f.store.ClaimMaintenanceTurn(topic.WithTopicEmbeddingStage(ctx), window.ID, time.Now(), 3*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	batch, err := f.store.ReadTopicEmbeddingBatch(ctx, *turn)
	require.NoError(t, err)
	require.NotEmpty(t, batch.Topics)
	vectors := semantic.Result{Fence: batch.Fence, Model: batch.Fence.Model}
	for _, candidate := range batch.Topics {
		vectors.Embeddings = append(vectors.Embeddings, semantic.Embedding{DocumentHash: fmt.Sprintf("%s@%d", candidate.ID, candidate.Version), Vector: []float32{1, 0, 0}})
	}
	_, err = f.store.CompleteTopicEmbeddingBatch(ctx, *turn, *batch, vectors)
	require.NoError(t, err)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
	turn, err = f.store.ClaimMaintenanceTurn(topic.WithConsolidationStage(ctx), window.ID, time.Now(), 3*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	pairs, claim, err := f.store.ClaimTopicConsolidation(ctx, *turn, window.ID, time.Now())
	require.NoError(t, err)
	require.NotNil(t, claim)
	return pairs, claim
}

func TestTopicConsolidationOverBoundRollsBack(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	_, err := searchpostgres.NewStore(f.admin, f.rls).EnsureActiveSearchContract(ctx, searchmaintenance.EnsureActiveSearchContractInput{Model: "fixture-embedding", Dimensions: 3})
	require.NoError(t, err)
	winner, loser := testTopic("bounded-winner"), testTopic("bounded-loser")
	winner.ID, loser.ID = "10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002"
	initial, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("bounded-winner", 0, topic.Change{Record: winner}))
	require.NoError(t, err)
	_, err = f.store.PublishAutomatic(ctx, f.team, testPublication("bounded-loser", initial.Revision, topic.Change{Record: loser}))
	require.NoError(t, err)
	input := assignment.Scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: uuid.NewString()}
	var items []assignment.Publication
	for i := 0; i < 32; i++ {
		text := fmt.Sprintf("Bounded source %d.", i)
		source := f.organizationEvidence(t, 0, text, nil)
		items = append(items, assignment.Publication{EvidenceID: source.ID, Outcome: assignment.Outcome{Text: text, InputHash: testHash(text), Method: "decision", Matches: []assignment.Match{{ID: loser.ID, Version: 1}}}})
	}
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error { return f.store.PublishAssignmentsTx(ctx, tx, input, items) }))
	pairs, claim := consolidationFixtureClaim(t, f)
	require.Len(t, pairs, 1)
	_, err = f.store.CompleteTopicConsolidationPair(ctx, *claim, pairs[0], true)
	require.ErrorIs(t, err, topic.ErrContextBound)
	var active, retired int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FILTER(WHERE NOT retired),count(*) FILTER(WHERE retired) FROM topic_memberships WHERE team_id=?::uuid AND topic_id=?::uuid`, f.team, loser.ID).Row().Scan(&active, &retired))
	require.Equal(t, 32, active)
	require.Zero(t, retired)
	view, err := f.store.GetRecord(f.actor(0, "manager"), f.team, loser.ID, 0)
	require.NoError(t, err)
	require.False(t, view.Retired)
	require.NoError(t, f.store.FinishTopicConsolidation(ctx, *claim, false))
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, topic.OrganizationResult{}, "context_bound", time.Now()))
}
