//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func maintenanceRelationship(t *testing.T, f *ontologyFixture) ontology.SourceHandle {
	t.Helper()
	subject := maintenanceEntity(t, f, "Atlas")
	object := maintenanceEntity(t, f, "PostgreSQL")
	predicate, err := f.knowledge.EnsureSemanticReviewPredicateCandidate(context.Background(), knowledge.EnsureSemanticPredicateCandidateInput{TeamID: f.team, OwnerProfileID: f.owners[0], Predicate: "uses", RelationshipKind: "state", SubjectKind: "project", ObjectKind: "project"})
	require.NoError(t, err)
	evidence := f.evidence(t, 0, "Atlas uses PostgreSQL.")
	decision, err := f.knowledge.ApplyRelationshipDecision(context.Background(), knowledge.ApplyRelationshipDecisionInput{TeamID: f.team, OwnerProfileID: f.owners[0], IngestID: evidence.IngestID, SubjectEntityID: subject.ID, PredicateKey: predicate.PredicateKey, PredicateVersion: predicate.Version, ObjectEntityID: object.ID, Polarity: "+", Support: &knowledge.EvidenceSupportInput{FragmentID: evidence.Evidence[0].FragmentID, SourceGroupKey: uuid.NewString(), SpanEnd: len("Atlas uses PostgreSQL."), Authority: "primary"}})
	require.NoError(t, err)
	require.NotNil(t, decision.Relationship)
	return ontology.SourceHandle{Kind: ontology.RelationshipSource, ID: decision.Relationship.RelationshipID, Version: int64(decision.Relationship.Version)}
}

func TestOntologyMaintenanceLargeBacklogAndFairTurns(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	relationship := maintenanceRelationship(t, f)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO entity_records SELECT (jsonb_populate_record(NULL::entity_records,to_jsonb(template)||jsonb_build_object('entity_id',gen_random_uuid(),'identity_context',jsonb_build_object('backlog',index)))).*
		 FROM (SELECT * FROM entity_records WHERE team_id=?::uuid LIMIT 1) AS template CROSS JOIN generate_series(1,5001) AS index`, f.team).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO relationship_records SELECT (jsonb_populate_record(NULL::relationship_records,to_jsonb(template)||jsonb_build_object('relationship_id',gen_random_uuid(),'scope_key','backlog:'||index,'semantic_group_key','backlog:'||index))).*
		 FROM relationship_records AS template CROSS JOIN generate_series(1,20001) AS index WHERE template.team_id=?::uuid AND template.relationship_id=?::uuid`, f.team, relationship.ID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO relationship_evidence_supports SELECT (jsonb_populate_record(NULL::relationship_evidence_supports,to_jsonb(template)||jsonb_build_object('support_id',gen_random_uuid(),'relationship_id',relationship.relationship_id))).*
		 FROM relationship_evidence_supports AS template JOIN relationship_records AS relationship ON relationship.team_id=template.team_id AND relationship.scope_key LIKE 'backlog:%'
		 WHERE template.team_id=?::uuid AND template.relationship_id=?::uuid`, f.team, relationship.ID).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO relationship_support_decision_events SELECT (jsonb_populate_record(NULL::relationship_support_decision_events,to_jsonb(template)||jsonb_build_object('support_decision_id',gen_random_uuid(),'support_id',support.support_id,'relationship_id',support.relationship_id,'idempotency_key',gen_random_uuid()::text))).*
		 FROM relationship_support_decision_events AS template JOIN relationship_evidence_supports AS support ON support.team_id=template.team_id AND support.relationship_id<>template.relationship_id
		 WHERE template.team_id=?::uuid AND template.relationship_id=?::uuid AND template.decision='grant'`, f.team, relationship.ID).Error
	}))
	require.NoError(t, f.rls.WithTeamTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
		var key string
		var version int64
		if err := tx.Raw(`SELECT predicate_key,predicate_version FROM relationship_records WHERE team_id=?::uuid AND relationship_id=?::uuid`, f.team, relationship.ID).Row().Scan(&key, &version); err != nil {
			return err
		}
		var plan []byte
		if err := tx.Raw(`EXPLAIN (ANALYZE,FORMAT JSON) `+predicateSourceSQL, f.team, f.space, f.generation, pq.Array([]string{key}), pq.Array([]int64{version})).Row().Scan(&plan); err != nil {
			return err
		}
		type node struct {
			Relation string  `json:"Relation Name"`
			Loops    float64 `json:"Actual Loops"`
			Rows     float64 `json:"Actual Rows"`
			Plans    []node  `json:"Plans"`
		}
		var reports []struct{ Plan node }
		if err := json.Unmarshal(plan, &reports); err != nil {
			return err
		}
		require.Len(t, reports, 1)
		require.Equal(t, float64(1), reports[0].Plan.Rows)
		loops := float64(0)
		var visit func(node)
		visit = func(current node) {
			if current.Relation == "relationship_support_decision_events" {
				loops += current.Loops
			}
			for _, child := range current.Plans {
				visit(child)
			}
		}
		visit(reports[0].Plan)
		require.Equal(t, float64(1), loops, "one eligible predicate needs one supporting decision, regardless of corpus size")
		return nil
	}))
	other := maintenanceOtherTeam(t, f)
	other.organizationEvidence(t, 0, "Small team's pending work.", nil)
	config := maintenanceSettings(t, f)
	window := maintenanceWindow(t, f, config)
	first, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Hour)
	require.NoError(t, err)
	require.NotNil(t, first)
	_, err = f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NoError(t, f.store.DiscoverMaintenance(ctx, *first, ontology.MaintenancePageSize))
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *first))
	second, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Hour)
	require.NoError(t, err)
	require.NotNil(t, second)
	require.NotEqual(t, first.TeamID, second.TeamID, "one team turn cannot consume another team's position")
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *second))
	var big *ontology.MaintenanceTurn
	for range 4 {
		turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Hour)
		require.NoError(t, err)
		require.NotNil(t, turn)
		if turn.TeamID == f.team {
			big = turn
			break
		}
		require.NoError(t, f.store.DiscoverMaintenance(ctx, *turn, ontology.MaintenancePageSize))
		require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
	}
	require.NotNil(t, big)
	var discovered bool
	for range 800 {
		require.NoError(t, f.store.DiscoverMaintenance(ctx, *big, ontology.MaintenancePageSize))
		require.NoError(t, f.admin.Raw(`SELECT discovery_kind=4 FROM ontology_maintenance_teams WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, f.team, f.space, f.generation).Row().Scan(&discovered))
		if discovered {
			break
		}
	}
	require.True(t, discovered, "bounded backfill must discover the entire large corpus")
	status, err := f.store.MaintenanceStatus(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.False(t, status.CoverageComplete)
	var nodes, edges int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FILTER(WHERE source_kind='entity'),count(*) FILTER(WHERE source_kind='relationship') FROM ontology_maintenance_sources WHERE team_id=?::uuid AND eligible`, f.team).Row().Scan(&nodes, &edges))
	require.Greater(t, nodes, 5000)
	require.Greater(t, edges, 20000)
	unrelated := testTopic("unrelated-large-marker")
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("sparse-large-marker", 0, ontology.Change{Record: unrelated}))
	require.NoError(t, err)
	require.NoError(t, f.store.withScope(ctx, f.team, true, func(tx *gorm.DB, fence scope) error {
		handles, err := maintenanceMarkerSources(tx, fence, maintenanceMarker{AnchorKind: "ontology_record_heads", TargetKind: "definition", TargetID: unrelated.ID}, 100)
		if err != nil {
			return err
		}
		require.Empty(t, handles, "an unrelated definition must not visit the 25k-source backlog")
		return nil
	}))
	var cursor string
	require.NoError(t, f.admin.Raw(`SELECT entity_id::text FROM entity_records WHERE team_id=?::uuid ORDER BY entity_id OFFSET 4900 LIMIT 1`, f.team).Row().Scan(&cursor))
	assertNativeMaintenanceSeek(t, f, cursor)
	var relationshipCursor string
	require.NoError(t, f.admin.Raw(`SELECT relationship_id::text FROM relationship_records WHERE team_id=?::uuid ORDER BY relationship_id OFFSET 19900 LIMIT 1`, f.team).Row().Scan(&relationshipCursor))
	var predicate string
	require.NoError(t, f.admin.Raw(`SELECT predicate_key FROM relationship_records WHERE team_id=?::uuid AND relationship_id=?::uuid`, f.team, relationship.ID).Row().Scan(&predicate))
	require.NoError(t, f.rls.WithTeamTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
		fence := scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation}
		marker := maintenanceMarker{TargetKind: "predicate", TargetID: predicate, Cursor: "relationship:" + relationshipCursor}
		for range 3 {
			page, err := maintenanceMarkerSources(tx, fence, marker, 20)
			if err != nil {
				return err
			}
			require.Len(t, page, 20)
			for _, handle := range page {
				require.Greater(t, ontology.SourceKey(handle), marker.Cursor)
				marker.Cursor = ontology.SourceKey(handle)
			}
		}
		return nil
	}))
	claim, err := f.store.ClaimMaintenanceBatch(ctx, *big, window.ID, time.Now().UTC())
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.LessOrEqual(t, len(claim.Sources), 20)
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, ontology.OrganizationResult{}, "budget_deferred", time.Now().UTC()))
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *big))
	restarted := NewStore(f.app, f.rls)
	next, err := restarted.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, next)
	require.NotEqual(t, f.team, next.TeamID)
	require.NoError(t, restarted.ReleaseMaintenanceTurn(ctx, *next))
	service, calls := maintenanceServiceFixture(t, f, config, nil)
	for range 8 {
		_, err := service.RunTurn(ctx)
		require.NoError(t, err)
		var organized int
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND status='organized'`, other.team).Row().Scan(&organized))
		if organized > 0 {
			break
		}
	}
	var organized, pending int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND status='organized'`, other.team).Row().Scan(&organized))
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND status='pending'`, f.team).Row().Scan(&pending))
	require.Greater(t, organized, 0)
	require.Greater(t, pending, 20000)
	require.Greater(t, calls(), int32(0))
}

func TestOntologyMaintenanceOversizedGroupClosure(t *testing.T) {
	f := newOrganizationFixture(t)
	var sources []ontology.SourceHandle
	var dependencies []ontology.SourceDependency
	for range 21 {
		handle := f.organizationEvidence(t, 0, "Complete group source.", nil)
		sources = append(sources, handle)
		dependencies = append(dependencies, f.source(t, handle))
	}
	group := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.GroupTogether, Members: sources}, Sources: dependencies}
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("large-group", 0, ontology.Change{Record: group}))
	require.NoError(t, err)
	config := maintenanceSettings(t, f)
	window := maintenanceWindow(t, f, config)
	turn, err := f.store.ClaimMaintenanceTurn(context.Background(), window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	for range 8 {
		require.NoError(t, f.store.DiscoverMaintenance(context.Background(), *turn, ontology.MaintenancePageSize))
	}
	claim, err := f.store.ClaimMaintenanceBatch(context.Background(), *turn, window.ID, time.Now().UTC())
	require.NoError(t, err)
	require.Nil(t, claim)
	status, err := f.store.MaintenanceStatus(context.Background(), time.Now().UTC())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Ambiguous)
	require.False(t, status.CoverageComplete)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(context.Background(), *turn))
}
