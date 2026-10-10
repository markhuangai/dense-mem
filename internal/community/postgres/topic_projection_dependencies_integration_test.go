//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	community "github.com/markhuangai/dense-mem/internal/community/contract"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func (f *topicProjectionFixture) additionalSupport(t *testing.T, index int) *knowledge.RelationshipDecisionResult {
	t.Helper()
	owner := f.sources[index].OwnerProfileID
	semantic := knowledge.NewStore(f.appDB, f.rls, knowledge.ConflictRuntimeConfig{})
	object := createSemanticEntity(t, context.Background(), semantic, f.teamID, owner, "product", "New assigned-evidence member "+uuid.NewString())
	var ingest, content, subject string
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT evidence.ingest_id::text,evidence.content,relationship.subject_entity_id::text FROM evidence_fragments AS evidence
			JOIN relationship_records AS relationship ON relationship.team_id=evidence.team_id AND relationship.relationship_id=?::uuid
			WHERE evidence.team_id=?::uuid AND evidence.fragment_id=?::uuid`, f.sources[index].RelationshipID, f.teamID, f.evidenceIDs[index]).Row().Scan(&ingest, &content, &subject)
	}))
	return applySemanticDecision(t, f.actor(owner), semantic, ApplyRelationshipDecisionInput{TeamID: f.teamID, OwnerProfileID: owner, IngestID: ingest,
		SubjectEntityID: subject, PredicateKey: "uses", ObjectEntityID: object.EntityID,
		Support: &EvidenceSupportInput{FragmentID: f.evidenceIDs[index], SourceGroupKey: uuid.NewString(), SpanEnd: len(content), Authority: "primary"},
	})
}

func (f *topicProjectionFixture) sourceDependency(t *testing.T, handle ontology.SourceHandle) ontology.SourceDependency {
	t.Helper()
	sources, err := f.ontology.ReadSources(context.Background(), f.teamID, []ontology.SourceHandle{handle})
	require.NoError(t, err)
	require.Len(t, sources, 1)
	fingerprint, err := ontology.SourceFingerprint(sources[0])
	require.NoError(t, err)
	return ontology.SourceDependency{SourceHandle: handle, Fingerprint: fingerprint}
}

func TestCommunityTopicAssignedEvidenceTracksNewAndReinstatedSupport(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.topic(t, "fanout", []ontology.SourceHandle{{Kind: ontology.EvidenceSource, ID: f.evidenceIDs[1], Version: 1}})
	f.drain(t, 5)
	added := f.additionalSupport(t, 1)
	input := CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 20}
	records, err := f.store.RecallCommunities(f.actor(f.reader), input)
	require.NoError(t, err)
	require.Empty(t, records, "new membership left an incomplete projection readable")
	f.drain(t, 5)
	records, err = f.store.RecallCommunities(f.actor(f.reader), input)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, 2, records[0].RelationshipCount)
	semantic := knowledge.NewStore(f.appDB, f.rls, knowledge.ConflictRuntimeConfig{})
	decision := knowledge.ApplyRelationshipSupportDecisionInput{TeamID: f.teamID, OwnerProfileID: f.ownerB, RelationshipID: added.Relationship.RelationshipID, SupportID: added.SupportID, Decision: "revoke", Reason: "fanout withdrawal", IdempotencyKey: uuid.NewString()}
	_, err = semantic.ApplyRelationshipSupportDecision(f.actor(f.ownerB), decision)
	require.NoError(t, err)
	f.drain(t, 5)
	decision.Decision, decision.IdempotencyKey = "reinstate", uuid.NewString()
	_, err = semantic.ApplyRelationshipSupportDecision(f.actor(f.ownerB), decision)
	require.NoError(t, err)
	records, err = f.store.RecallCommunities(f.actor(f.reader), input)
	require.NoError(t, err)
	require.Empty(t, records, "reinstated membership was not fenced")
	f.drain(t, 5)
	records, err = f.store.RecallCommunities(f.actor(f.reader), input)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, 2, records[0].RelationshipCount)
}

func TestCommunityTopicNewMembershipBetweenPagesRejectsPublication(t *testing.T) {
	f := newTopicProjectionFixture(t)
	for range 100 {
		f.additionalSupport(t, 1)
	}
	f.topic(t, "paged-fanout", []ontology.SourceHandle{{Kind: ontology.EvidenceSource, ID: f.evidenceIDs[1], Version: 1}})
	progress, err := f.service.RunProjectionTurn(context.Background())
	require.NoError(t, err)
	require.True(t, progress)
	work, err := f.store.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.NotNil(t, work)
	f.additionalSupport(t, 1)
	require.ErrorIs(t, f.store.AppendTopicProjection(context.Background(), community.TopicProjectionBatch{Work: *work}, time.Now()), community.ErrCommunitySourceStale)
	require.NoError(t, f.store.FailTopicProjection(context.Background(), *work, "source_changed", time.Now()))
	f.drain(t, 10)
	records, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10, RelationshipLimit: 5})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, 102, records[0].RelationshipCount)
	require.True(t, records[0].RelationshipsTruncated)
}

func TestCommunityTopicDeclaredDefinitionSourceIsFenced(t *testing.T) {
	for _, kind := range []ontology.SourceKind{ontology.EvidenceSource, ontology.RelationshipSource} {
		for _, phase := range []string{"published", "publication"} {
			t.Run(string(kind)+"/"+phase, func(t *testing.T) {
				f := newTopicProjectionFixture(t)
				healthy := f.topic(t, "healthy", []ontology.SourceHandle{{Kind: ontology.RelationshipSource, ID: f.sources[18].RelationshipID, Version: int64(f.sources[18].RelationshipVersion)}})
				f.drain(t, 5)
				handle := ontology.SourceHandle{Kind: ontology.RelationshipSource, ID: f.sources[0].RelationshipID, Version: int64(f.sources[0].RelationshipVersion)}
				declared := ontology.SourceHandle{Kind: kind, ID: f.evidenceIDs[9], Version: 1}
				if kind == ontology.RelationshipSource {
					declared.ID, declared.Version = f.sources[9].RelationshipID, int64(f.sources[9].RelationshipVersion)
				}
				f.topic(t, "source-bound", []ontology.SourceHandle{handle}, f.sourceDependency(t, declared))
				var work *community.TopicProjectionWork
				if phase == "publication" {
					var err error
					work, err = f.store.ClaimTopicProjection(context.Background(), time.Now())
					require.NoError(t, err)
					require.NotNil(t, work)
				} else {
					f.drain(t, 5)
				}
				if kind == ontology.RelationshipSource {
					require.NoError(t, f.rls.WithTeamProfileTx(f.actor(f.ownerB), f.appDB, f.teamID, f.ownerB, func(tx *gorm.DB) error {
						return tx.Exec(`INSERT INTO evidence_quarantines(team_id,fragment_id,ingest_id,owner_profile_id,reason,space_id,space_generation)
							SELECT team_id,fragment_id,ingest_id,owner_profile_id,'declared dependency quarantine',space_id,space_generation FROM evidence_fragments
							WHERE team_id=?::uuid AND fragment_id=?::uuid`, f.teamID, f.evidenceIDs[9]).Error
					}))
				} else {
					f.retract(t, 9)
				}
				if work != nil {
					require.ErrorIs(t, f.store.AppendTopicProjection(context.Background(), community.TopicProjectionBatch{Work: *work}, time.Now()), community.ErrCommunitySourceStale)
					require.NoError(t, f.store.FailTopicProjection(context.Background(), *work, "source_changed", time.Now()))
				}
				records, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10})
				require.NoError(t, err)
				require.Len(t, records, 1, "stale definition leaked its descriptors or hid a healthy topic")
				require.Equal(t, healthy, records[0].LogicalCommunityID)
			})
		}
	}
}

func TestCommunityTopicNewClassificationOverrideInvalidatesOnlyAffectedTopic(t *testing.T) {
	f := newTopicProjectionFixture(t)
	f.seedCohort(t)
	f.drain(t, 10)
	before := f.currentIDs(t)
	alternate := f.topic(t, "alternate", nil)
	handle := ontology.SourceHandle{Kind: ontology.RelationshipSource, ID: f.sources[0].RelationshipID, Version: int64(f.sources[0].RelationshipVersion)}
	page, err := f.ontology.ListRecords(context.Background(), f.teamID, "", "", 1)
	require.NoError(t, err)
	actor, ok := requestctx.ActorFromContext(f.actor(f.reader))
	require.True(t, ok)
	actor.Role = "manager"
	_, err = f.ontology.PublishManager(requestctx.WithActor(context.Background(), actor), f.teamID, ontology.Publication{OperationKey: uuid.NewString(), ExpectedRevision: page.Revision, Reason: "new classification applicability", Changes: []ontology.Change{{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind,
		Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: alternate, Members: []ontology.SourceHandle{handle}}, Sources: []ontology.SourceDependency{f.sourceDependency(t, handle)}}}}})
	require.NoError(t, err)
	records, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10})
	require.NoError(t, err)
	require.Len(t, records, 2)
	for _, record := range records {
		require.Equal(t, before[record.LogicalCommunityID], record.CommunityID)
	}
}

func TestCommunityTopicNewOverrideDuringPublicationIsFenced(t *testing.T) {
	f := newTopicProjectionFixture(t)
	handle := ontology.SourceHandle{Kind: ontology.RelationshipSource, ID: f.sources[0].RelationshipID, Version: int64(f.sources[0].RelationshipVersion)}
	f.topic(t, "original", []ontology.SourceHandle{handle})
	work, err := f.store.ClaimTopicProjection(context.Background(), time.Now())
	require.NoError(t, err)
	require.NotNil(t, work)
	alternate := f.topic(t, "override-target", nil)
	page, err := f.ontology.ListRecords(context.Background(), f.teamID, "", "", 1)
	require.NoError(t, err)
	actor, _ := requestctx.ActorFromContext(f.actor(f.reader))
	actor.Role = "manager"
	_, err = f.ontology.PublishManager(requestctx.WithActor(context.Background(), actor), f.teamID, ontology.Publication{OperationKey: uuid.NewString(), ExpectedRevision: page.Revision, Reason: "classification changed during publication", Changes: []ontology.Change{{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind,
		Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: alternate, Members: []ontology.SourceHandle{handle}}, Sources: []ontology.SourceDependency{f.sourceDependency(t, handle)}}}}})
	require.NoError(t, err)
	require.ErrorIs(t, f.store.AppendTopicProjection(context.Background(), community.TopicProjectionBatch{Work: *work}, time.Now()), community.ErrCommunitySourceStale)
	require.NoError(t, f.store.FailTopicProjection(context.Background(), *work, "source_changed", time.Now()))
	require.Empty(t, f.currentIDs(t))
}

func TestCommunityTopicPredicateEligibilityDependencyIsFenced(t *testing.T) {
	for _, indirect := range []bool{false, true} {
		for _, phase := range []string{"published", "publication"} {
			t.Run(fmt.Sprintf("indirect_%t/%s", indirect, phase), func(t *testing.T) {
				f := newTopicProjectionFixture(t)
				ctx := context.Background()
				healthy := f.topic(t, "healthy", []ontology.SourceHandle{{Kind: ontology.RelationshipSource, ID: f.sources[18].RelationshipID, Version: int64(f.sources[18].RelationshipVersion)}})
				f.drain(t, 5)
				healthyID := f.currentIDs(t)[healthy]
				semantic := knowledge.NewStore(f.appDB, f.rls, knowledge.ConflictRuntimeConfig{})
				predicate, err := semantic.EnsureSemanticReviewPredicateCandidate(f.actor(f.ownerB), knowledge.EnsureSemanticPredicateCandidateInput{
					TeamID: f.teamID, OwnerProfileID: f.ownerB, Predicate: "qualifies_topic", RelationshipKind: "state", SubjectKind: "project", ObjectKind: "product"})
				require.NoError(t, err)
				var ingest, content, subject, object string
				require.NoError(t, f.rls.WithSystemTx(ctx, f.adminDB, func(tx *gorm.DB) error {
					return tx.Raw(`SELECT evidence.ingest_id::text,evidence.content,relationship.subject_entity_id::text,relationship.object_entity_id::text
						FROM evidence_fragments AS evidence JOIN relationship_records AS relationship ON relationship.team_id=evidence.team_id
						AND relationship.relationship_id=?::uuid WHERE evidence.team_id=?::uuid AND evidence.fragment_id=?::uuid`,
						f.sources[9].RelationshipID, f.teamID, f.evidenceIDs[9]).Row().Scan(&ingest, &content, &subject, &object)
				}))
				applySemanticDecision(t, f.actor(f.ownerB), semantic, ApplyRelationshipDecisionInput{
					TeamID: f.teamID, OwnerProfileID: f.ownerB, IngestID: ingest, SubjectEntityID: subject,
					PredicateKey: predicate.PredicateKey, PredicateVersion: predicate.Version, ObjectEntityID: object,
					Support: &EvidenceSupportInput{FragmentID: f.evidenceIDs[9], SourceGroupKey: uuid.NewString(), SpanEnd: len(content), Authority: "primary"},
				})
				dependency := f.sourceDependency(t, ontology.SourceHandle{Kind: ontology.PredicateSource, ID: predicate.PredicateKey, Version: int64(predicate.Version)})
				handles := []ontology.SourceHandle{{Kind: ontology.RelationshipSource, ID: f.sources[0].RelationshipID, Version: int64(f.sources[0].RelationshipVersion)}}
				var bound string
				if indirect {
					definitionID := uuid.NewString()
					f.publish(t, ontology.Change{Record: ontology.Record{ID: definitionID, Kind: ontology.PredicateConcept, Sources: []ontology.SourceDependency{dependency},
						Definition: &ontology.Definition{Key: "qualifying-concept", Label: "Qualifying concept"}}})
					bound = uuid.NewString()
					f.publish(t, ontology.Change{Record: ontology.Record{ID: bound, Kind: ontology.Topic,
						Dependencies: []ontology.RevisionRef{{ID: definitionID, Version: 1}},
						Definition:   &ontology.Definition{Key: "source-bound", Label: "source-bound tools", Description: "Community snapshot marker"}}})
					f.topics["source-bound"] = bound
					f.assignTopic(t, bound, handles)
				} else {
					bound = f.topic(t, "source-bound", handles, dependency)
				}
				var work *community.TopicProjectionWork
				if phase == "publication" {
					work, err = f.store.ClaimTopicProjection(ctx, time.Now())
					require.NoError(t, err)
					require.NotNil(t, work)
					require.True(t, work.Topic.Current)
					require.Len(t, work.Inputs, 1)
				} else {
					f.drain(t, 5)
					records, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10})
					require.NoError(t, err)
					require.Len(t, records, 2, "Predicate dependency setup was already stale")
				}
				require.NoError(t, f.rls.WithTeamProfileTx(f.actor(f.ownerB), f.appDB, f.teamID, f.ownerB, func(tx *gorm.DB) error {
					return tx.Exec(`INSERT INTO evidence_quarantines(team_id,fragment_id,ingest_id,owner_profile_id,reason,space_id,space_generation)
						SELECT team_id,fragment_id,ingest_id,owner_profile_id,'predicate dependency quarantine',space_id,space_generation
						FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid`, f.teamID, f.evidenceIDs[9]).Error
				}))
				view, err := f.ontology.GetRecord(ctx, f.teamID, bound, 0)
				require.NoError(t, err)
				require.False(t, view.Current)
				if work != nil {
					require.ErrorIs(t, f.store.AppendTopicProjection(ctx, community.TopicProjectionBatch{Work: *work}, time.Now()), community.ErrCommunitySourceStale)
					require.NoError(t, f.store.FailTopicProjection(ctx, *work, "source_changed", time.Now()))
				}
				records, err := f.store.RecallCommunities(f.actor(f.reader), CommunityRecallInput{TeamID: f.teamID, Query: "marker", Limit: 10})
				require.NoError(t, err)
				require.Len(t, records, 1, "stale Predicate dependency remained readable")
				require.Equal(t, healthy, records[0].LogicalCommunityID)
				require.Equal(t, healthyID, records[0].CommunityID)
				f.drain(t, 5)
				require.NoError(t, f.rls.WithTeamProfileTx(f.actor(f.ownerB), f.appDB, f.teamID, f.ownerB, func(tx *gorm.DB) error {
					return tx.Exec(`UPDATE evidence_quarantines SET status='released',released_at=clock_timestamp(),released_by_profile_id=?::uuid
						WHERE team_id=?::uuid AND fragment_id=?::uuid AND status='active'`, f.ownerB, f.teamID, f.evidenceIDs[9]).Error
				}))
				f.drain(t, 5)
				current := f.currentIDs(t)
				require.NotEmpty(t, current[bound], "restored Predicate eligibility was not refreshed")
				require.Equal(t, healthyID, current[healthy])
			})
		}
	}
}
