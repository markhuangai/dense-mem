//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	community "github.com/markhuangai/dense-mem/internal/community/contract"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCommunityTopicLargeFanoutResumesAndRotates(t *testing.T) {
	f := newTopicProjectionFixture(t)
	ctx := context.Background()
	semantic := knowledge.NewStore(f.appDB, f.rls, knowledge.ConflictRuntimeConfig{})
	subject := createSemanticEntity(t, ctx, semantic, f.teamID, f.ownerA, "project", "Large shared hub")
	content := "Large shared hub uses the enumerated synthetic products."
	ingest := createSemanticIngest(t, ctx, semantic, f.teamID, f.ownerA, "large-topic-fanout", content)
	const groups = 5001
	for i := range groups {
		object := createSemanticEntity(t, ctx, semantic, f.teamID, f.ownerA, "product", fmt.Sprintf("Large product %04d", i))
		applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
			TeamID: f.teamID, OwnerProfileID: f.ownerA, IngestID: ingest.IngestID,
			SubjectEntityID: subject.EntityID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
			Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "large-topic-fanout", SpanEnd: len(content), Authority: "primary"},
		})
	}
	large := f.topic(t, "large", []ontology.SourceHandle{{Kind: ontology.EvidenceSource, ID: ingest.Evidence[0].FragmentID, Version: 1}})
	progress, err := f.service.RunProjectionTurn(ctx)
	require.NoError(t, err)
	require.True(t, progress)
	var storedID, assignmentCursor, relationshipCursor string
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT community_id::text,assignment_cursor,relationship_cursor FROM community_topic_work WHERE topic_id=?::uuid`, large).Row().Scan(&storedID, &assignmentCursor, &relationshipCursor)
	}))
	require.NotEmpty(t, assignmentCursor)
	require.NotEmpty(t, relationshipCursor)
	_, err = f.store.GetCommunity(f.actor(f.reader), CommunityGetInput{TeamID: f.teamID, CommunityID: storedID})
	require.ErrorIs(t, err, community.ErrCommunityNotFound)
	work, err := f.store.ClaimTopicProjection(ctx, time.Now())
	require.NoError(t, err)
	require.NotNil(t, work)
	require.Equal(t, storedID, work.CommunityID)
	require.True(t, work.More)
	require.LessOrEqual(t, len(work.Inputs), 100)
	small := f.topic(t, "small", []ontology.SourceHandle{{Kind: ontology.RelationshipSource, ID: f.sources[0].RelationshipID, Version: int64(f.sources[0].RelationshipVersion)}})
	require.NoError(t, f.store.FailTopicProjection(ctx, *work, "interrupted", time.Now()))
	f.store = NewStore(f.appDB.Session(&gorm.Session{NewDB: true}), f.rls).WithTopics(f.ontology.NewTopicCatalogReader(), f.ontology.NewTopicMembershipReader(), f.ontology.NewTopicProjectionAdmission(), f.ontology.NewTopicProjectionRelease())
	f.service = communityapp.New(communityapp.Dependencies{Store: f.store, AppConfig: topicFixtureConfig{}})
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		var id, assignment, relationship string
		if err := tx.Raw(`SELECT community_id::text,assignment_cursor,relationship_cursor FROM community_topic_work WHERE topic_id=?::uuid`, large).Row().Scan(&id, &assignment, &relationship); err != nil {
			return err
		}
		require.Equal(t, storedID, id)
		require.Equal(t, assignmentCursor, assignment)
		require.Equal(t, relationshipCursor, relationship)
		var count int
		if err := tx.Raw(`SELECT count(*) FROM community_sources WHERE community_id=?::uuid`, storedID).Row().Scan(&count); err != nil {
			return err
		}
		require.Equal(t, 100, count)
		return nil
	}))
	for turn := 0; turn < 4 && f.currentIDs(t)[small] == ""; turn++ {
		progress, err := f.service.RunProjectionTurn(ctx)
		require.NoError(t, err)
		require.True(t, progress)
	}
	require.NotEmpty(t, f.currentIDs(t)[small], "large topic monopolized maintenance")
	require.Empty(t, f.currentIDs(t)[large], "incomplete topic became visible")
	turns := f.drain(t, 100)
	require.Greater(t, turns, 40)
	ids := f.currentIDs(t)
	require.Len(t, ids, 2)
	record, err := f.store.GetCommunity(f.actor(f.reader), CommunityGetInput{TeamID: f.teamID, CommunityID: ids[large]})
	require.NoError(t, err)
	require.Equal(t, groups, record.SourceCount)
	require.Equal(t, groups+1, record.MemberCount)
	records, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 5})
	require.NoError(t, err)
	for _, record := range records {
		if record.LogicalCommunityID == large {
			require.Equal(t, groups, record.RelationshipCount)
			require.Len(t, record.Relationships, 5)
			require.True(t, record.RelationshipsTruncated)
		}
	}
	require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
		var stored, distinct, failed int
		if err := tx.Raw(`SELECT count(*),count(DISTINCT semantic_group_key) FROM community_sources WHERE team_id=?::uuid AND community_id=?::uuid`, f.teamID, ids[large]).Row().Scan(&stored, &distinct); err != nil {
			return err
		}
		require.Equal(t, groups, stored)
		require.Equal(t, groups, distinct)
		if err := tx.Raw(`SELECT count(*) FROM community_snapshot_runs WHERE team_id=?::uuid AND algorithm_kind='ontology_topic' AND status='too_large'`, f.teamID).Row().Scan(&failed); err != nil {
			return err
		}
		require.Zero(t, failed)
		return nil
	}))
	t.Logf("groups=%d high_degree=%d page_bound=100 complete=true resumable=true fair=true", groups, groups)
}

func TestCommunityTopicDefinitionRaceAndModeAdmission(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	work, err := f.store.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	changedTopic := work.TopicID
	handles := []ontology.SourceHandle{}
	for _, input := range work.Inputs {
		handles = append(handles, ontology.SourceHandle{Kind: ontology.RelationshipSource, ID: input.RelationshipID, Version: int64(input.Version)})
	}
	definition, err := f.ontology.GetRecord(context.Background(), f.teamID, work.TopicID, 0)
	require.NoError(t, err)
	definition.Definition.Description = "Changed during publication"
	f.publish(t, ontology.Change{ExpectedVersion: definition.Version, Record: definition.Record})
	require.ErrorIs(t, f.store.AppendTopicProjection(context.Background(), community.TopicProjectionBatch{Work: *work}, time.Now()), community.ErrCommunitySourceStale)
	require.NoError(t, f.store.FailTopicProjection(context.Background(), *work, "source_changed", time.Now()))
	work, err = f.store.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	f.setMode(t, false)
	require.ErrorIs(t, f.store.AppendTopicProjection(context.Background(), community.TopicProjectionBatch{Work: *work}, time.Now()), ontology.ErrMaintenanceDisabled)
	require.Empty(t, f.currentIDs(t))
	require.NoError(t, f.store.FailTopicProjection(context.Background(), *work, "interrupted", time.Now()))
	// The legacy publisher was admitted before ontology enablement but must fence at publication.
	run, err := f.store.ClaimCommunityRun(context.Background(), CommunityRunClaimInput{TeamID: f.teamID, WindowKey: uuid.NewString(), SourceFingerprint: "fixture-sources"})
	require.NoError(t, err)
	f.setMode(t, true)
	publication := f.publication
	publication.RunID = run.RunID
	require.ErrorIs(t, f.store.PublishCommunitySnapshot(context.Background(), publication), ontology.ErrMaintenanceDisabled)
	f.drain(t, 10)
	require.Len(t, f.currentIDs(t), 2)
	f.assignTopic(t, changedTopic, handles)
	f.drain(t, 10)
	require.Len(t, f.currentIDs(t), 3)
}
