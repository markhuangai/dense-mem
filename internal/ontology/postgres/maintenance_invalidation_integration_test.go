//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceInvalidationFamilies(t *testing.T) {
	families := []string{"entity_records", "entity_names", "evidence_fragments", "knowledge_ingests", "evidence_sources", "evidence_occurrences", "evidence_quarantines", "relationship_records", "relationship_evidence_supports", "relationship_support_decision_events", "team_predicate_definitions", "ontology_definition", "ontology_override", "teams"}
	for _, family := range families {
		t.Run(family, func(t *testing.T) {
			f := newOrganizationFixture(t)
			ctx := context.Background()
			config := maintenanceSettings(t, f)
			_, err := config.UpdateOntologyMaintenanceSettings(ctx, map[string]string{domain.AppConfigOntologyInputTokens: "1000000000", domain.AppConfigOntologyOutputTokens: "1000000000"}, "control", "", "")
			require.NoError(t, err)
			var target ontology.SourceHandle
			var mutation func()
			unavailable := false
			markerFamily := family
			system := func(fn func(*gorm.DB) error) { require.NoError(t, f.rls.WithSystemTx(ctx, f.app, fn)) }
			switch family {
			case "entity_records", "entity_names":
				target = maintenanceEntity(t, f, "Atlas")
				mutation = func() {
					system(func(tx *gorm.DB) error {
						if family == "entity_names" {
							return tx.Exec(`UPDATE entity_names SET valid_to=clock_timestamp() WHERE team_id=?::uuid AND entity_id=?::uuid AND owner_profile_id=?::uuid AND valid_to IS NULL`, f.team, target.ID, f.owners[0]).Error
						}
						return tx.Exec(`UPDATE entity_records SET version=version+1,identity_context='{"qualification":"changed"}'::jsonb WHERE team_id=?::uuid AND entity_id=?::uuid`, f.team, target.ID).Error
					})
				}
			case "evidence_sources":
				source := func(revision, previous string) ontology.SourceHandle {
					result, err := f.knowledge.CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: f.owners[0], IdempotencyKey: "source-" + revision, RequestHash: testHash(revision), Evidence: []knowledge.EvidenceInput{{Content: "Atlas uses PostgreSQL " + revision, SourceType: "document", Authority: "primary", SourceKey: "doc://maintenance-policy", SourceRevisionToken: revision, ExpectedPreviousRevisionToken: previous}}})
					require.NoError(t, err)
					return ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: result.Evidence[0].FragmentID, Version: 1}
				}
				target = source("rev-1", "")
				unavailable = true
				mutation = func() { source("rev-2", "rev-1") }
			case "relationship_records", "relationship_evidence_supports", "relationship_support_decision_events", "team_predicate_definitions":
				target = maintenanceRelationship(t, f)
				if family == "relationship_records" {
					mutation = func() {
						system(func(tx *gorm.DB) error {
							return tx.Exec(`UPDATE relationship_records SET version=version+1,metadata=metadata||'{"qualification":"changed"}'::jsonb WHERE team_id=?::uuid AND relationship_id=?::uuid`, f.team, target.ID).Error
						})
					}
					break
				}
				var subject, object, predicate, supportID string
				var version int
				require.NoError(t, f.admin.Raw(`SELECT relationship.subject_entity_id::text,relationship.object_entity_id::text,relationship.predicate_key,relationship.predicate_version,support.support_id::text FROM relationship_records AS relationship JOIN relationship_evidence_supports AS support ON support.team_id=relationship.team_id AND support.relationship_id=relationship.relationship_id WHERE relationship.team_id=?::uuid AND relationship.relationship_id=?::uuid LIMIT 1`, f.team, target.ID).Row().Scan(&subject, &object, &predicate, &version, &supportID))
				if family == "relationship_support_decision_events" {
					unavailable = true
					mutation = func() {
						_, err := f.knowledge.ApplyRelationshipSupportDecision(ctx, knowledge.ApplyRelationshipSupportDecisionInput{TeamID: f.team, OwnerProfileID: f.owners[0], RelationshipID: target.ID, SupportID: supportID, Decision: "revoke", Reason: "maintenance source withdrawn", IdempotencyKey: "revoke-last-support"})
						require.NoError(t, err)
					}
					break
				}
				if family == "team_predicate_definitions" {
					target = ontology.SourceHandle{Kind: ontology.PredicateSource, ID: predicate, Version: int64(version)}
					mutation = func() {
						system(func(tx *gorm.DB) error {
							if err := tx.Exec(`INSERT INTO team_predicate_definitions SELECT (jsonb_populate_record(NULL::team_predicate_definitions,to_jsonb(definition)||jsonb_build_object('version',?::integer,'aliases',jsonb_build_array('uses-new')))).* FROM team_predicate_definitions AS definition WHERE team_id=?::uuid AND predicate_key=? AND version=?`, version+1, f.team, predicate, version).Error; err != nil {
								return err
							}
							return tx.Exec(`UPDATE relationship_records SET version=version+1,predicate_version=? WHERE team_id=?::uuid AND predicate_key=? AND predicate_version=?`, version+1, f.team, predicate, version).Error
						})
					}
					break
				}
				mutation = func() {
					text := "Independent evidence confirms Atlas uses PostgreSQL."
					evidence := f.evidence(t, 0, text)
					result, err := f.knowledge.ApplyRelationshipDecision(ctx, knowledge.ApplyRelationshipDecisionInput{TeamID: f.team, OwnerProfileID: f.owners[0], IngestID: evidence.IngestID, SubjectEntityID: subject, PredicateKey: predicate, PredicateVersion: version, ObjectEntityID: object, Polarity: "+", Support: &knowledge.EvidenceSupportInput{FragmentID: evidence.Evidence[0].FragmentID, SourceGroupKey: uuid.NewString(), SpanEnd: len(text), Authority: "primary"}})
					require.NoError(t, err)
					require.Equal(t, target.ID, result.Relationship.RelationshipID)
				}
			case "ontology_definition", "ontology_override":
				markerFamily = "ontology_record_heads"
				target = f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
				var second ontology.SourceHandle
				if family == "ontology_override" {
					second = f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
				}
				mutation = func() {
					page, err := f.store.ListRecords(ctx, f.team, "", "", 1)
					require.NoError(t, err)
					var record ontology.Record
					var expected int64
					if family == "ontology_definition" {
						view, err := f.store.GetRecord(ctx, f.team, ontology.OrganizationRecordID(f.team, ontology.Topic, "postgresql"), 0)
						require.NoError(t, err)
						record = view.Record
						expected = record.Version
						record.Definition.Label = "Database systems"
					} else {
						record = ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.KeepSeparate, Members: []ontology.SourceHandle{target, second}}, Sources: []ontology.SourceDependency{f.source(t, target), f.source(t, second)}}
					}
					_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("definition-change", page.Revision, ontology.Change{ExpectedVersion: expected, Record: record}))
					require.NoError(t, err)
				}
			case "knowledge_ingests":
				initial := f.evidence(t, 0, "Atlas uses PostgreSQL.")
				ingestID, fragmentID := uuid.NewString(), uuid.NewString()
				system(func(tx *gorm.DB) error {
					if err := tx.Exec(`INSERT INTO knowledge_ingests SELECT (jsonb_populate_record(NULL::knowledge_ingests,to_jsonb(ingest)||jsonb_build_object('ingest_id',?::uuid,'idempotency_key',?::text,'status','processing','completed_at',NULL))).* FROM knowledge_ingests AS ingest WHERE team_id=?::uuid AND ingest_id=?::uuid`, ingestID, "maintenance-stage:"+ingestID, f.team, initial.IngestID).Error; err != nil {
						return err
					}
					return tx.Exec(`INSERT INTO evidence_fragments SELECT (jsonb_populate_record(NULL::evidence_fragments,to_jsonb(fragment)||jsonb_build_object('fragment_id',?::uuid,'ingest_id',?::uuid,'force_insert',true))).* FROM evidence_fragments AS fragment WHERE team_id=?::uuid AND fragment_id=?::uuid`, fragmentID, ingestID, f.team, initial.Evidence[0].FragmentID).Error
				})
				target = ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: fragmentID, Version: 1}
				mutation = func() {
					system(func(tx *gorm.DB) error {
						return tx.Exec(`UPDATE knowledge_ingests SET status='completed',completed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE team_id=?::uuid AND ingest_id=?::uuid`, f.team, ingestID).Error
					})
				}
			default:
				initial := f.evidence(t, 0, "Atlas uses PostgreSQL.")
				target = ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: initial.Evidence[0].FragmentID, Version: 1}
				switch family {
				case "evidence_fragments":
					mutation = func() { target = f.organizationEvidence(t, 0, "Atlas uses PostgreSQL for a new workload.", nil) }
				case "evidence_occurrences":
					mutation = func() {
						ingestID := uuid.NewString()
						system(func(tx *gorm.DB) error {
							if err := tx.Exec(`INSERT INTO knowledge_ingests SELECT (jsonb_populate_record(NULL::knowledge_ingests,to_jsonb(ingest)||jsonb_build_object('ingest_id',?::uuid,'idempotency_key',?::text))).* FROM knowledge_ingests AS ingest WHERE team_id=?::uuid AND ingest_id=?::uuid`, ingestID, "occurrence:"+ingestID, f.team, initial.IngestID).Error; err != nil {
								return err
							}
							return tx.Exec(`INSERT INTO evidence_occurrences SELECT (jsonb_populate_record(NULL::evidence_occurrences,to_jsonb(occurrence)||jsonb_build_object('occurrence_id',?::uuid,'ingest_id',?::uuid,'created_at',clock_timestamp()))).* FROM evidence_occurrences AS occurrence WHERE team_id=?::uuid AND canonical_fragment_id=?::uuid LIMIT 1`, uuid.NewString(), ingestID, f.team, target.ID).Error
						})
					}
				case "evidence_quarantines":
					unavailable = true
					mutation = func() {
						system(func(tx *gorm.DB) error {
							return tx.Exec(`INSERT INTO evidence_quarantines(team_id,fragment_id,ingest_id,owner_profile_id,space_id,space_generation,status,reason) SELECT team_id,fragment_id,ingest_id,owner_profile_id,space_id,space_generation,'active','maintenance quarantine' FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid`, f.team, target.ID).Error
						})
					}
				case "teams":
					mutation = func() {
						require.NoError(t, f.rls.WithSystemTx(ctx, f.admin, func(tx *gorm.DB) error {
							if err := tx.Exec(`UPDATE teams SET deleted_at=clock_timestamp() WHERE id=?::uuid`, f.team).Error; err != nil {
								return err
							}
							return tx.Exec(`UPDATE teams SET deleted_at=NULL WHERE id=?::uuid`, f.team).Error
						}))
					}
				}
			}
			service, calls := maintenanceServiceFixture(t, f, config, nil)
			drainMaintenance(t, service)
			status, err := service.Status(ctx)
			require.NoError(t, err)
			require.True(t, status.DiscoveryComplete)
			require.True(t, status.CoverageComplete)
			var baselineSequence, revision int64
			require.NoError(t, f.admin.Raw(`SELECT last_value FROM ontology_maintenance_marker_seq`).Row().Scan(&baselineSequence))
			require.NoError(t, f.admin.Raw(`SELECT revision FROM ontology_maintenance_sources WHERE team_id=?::uuid AND source_kind=? AND source_id=?`, f.team, target.Kind, target.ID).Row().Scan(&revision))
			priorCalls := calls()
			mutation()
			var freshMarkers int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_markers WHERE team_id=?::uuid AND anchor_kind=? AND marker_sequence>?`, f.team, markerFamily, baselineSequence).Row().Scan(&freshMarkers))
			require.Greater(t, freshMarkers, 0)
			status, err = service.Status(ctx)
			require.NoError(t, err)
			require.False(t, status.DiscoveryComplete)
			require.False(t, status.CoverageComplete)
			window := maintenanceWindow(t, f, config)
			turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now().UTC(), time.Minute)
			require.NoError(t, err)
			require.NotNil(t, turn)
			for range 20 {
				require.NoError(t, f.store.DiscoverMaintenance(ctx, *turn, ontology.MaintenancePageSize))
			}
			var currentRevision int64
			var state string
			require.NoError(t, f.admin.Raw(`SELECT revision,status FROM ontology_maintenance_sources WHERE team_id=?::uuid AND source_kind=? AND source_id=?`, f.team, target.Kind, target.ID).Row().Scan(&currentRevision, &state))
			if family == "teams" {
				require.Equal(t, revision, currentRevision)
				require.Equal(t, "organized", state)
			} else if family == "evidence_fragments" {
				require.Equal(t, "pending", state)
			} else {
				require.Greater(t, currentRevision, revision)
				if unavailable {
					require.Equal(t, "unavailable", state)
				} else {
					require.Equal(t, "pending", state)
				}
			}
			require.Equal(t, priorCalls, calls(), "discovery must not dispatch assessments")
			require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
		})
	}
}

func TestOntologyMaintenancePrivateGenerationAndABCIsolation(t *testing.T) {
	f := newOrganizationFixture(t)
	other := maintenanceOtherTeam(t, f)
	ctx := context.Background()
	shared := f.organizationEvidence(t, 1, "Owner B shared fact.", nil)
	private := &domain.Credential{ID: uuid.New(), TeamID: uuid.MustParse(f.team), Name: "private", KeyHash: "synthetic-" + uuid.NewString(), KeyPrefix: uuid.NewString()[:24], KeySuffix: "test", Scopes: []string{"read", "write"}, MemoryBinding: domain.CredentialBindingCredentialPrivate}
	require.NoError(t, access.NewCredentialRepository(f.app, f.rls, nil).CreateCredential(ctx, private))
	privateCtx := requestctx.WithAllowedSpaces(ctx, []domain.MemorySpaceAccess{{ID: private.MemorySpaceID, Kind: domain.MemorySpaceCredentialPrivate, Generation: private.MemorySpaceGeneration}})
	privateResult, err := f.knowledge.CreateIngestForTest(privateCtx, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: private.ID.String(), SpaceID: private.MemorySpaceID.String(), SpaceGeneration: private.MemorySpaceGeneration, Evidence: []knowledge.EvidenceInput{{Content: "Private source must remain excluded."}}})
	require.NoError(t, err)
	other.organizationEvidence(t, 0, "Actor C other-team fact.", nil)
	config := maintenanceSettings(t, f)
	service, _ := maintenanceServiceFixture(t, f, config, nil)
	drainMaintenance(t, service)
	var privateCount int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE source_id=?`, privateResult.Evidence[0].FragmentID).Row().Scan(&privateCount))
	require.Zero(t, privateCount)
	for _, owner := range []int{0, 1} {
		var count int
		require.NoError(t, f.rls.WithTeamTx(f.actor(owner, "member"), f.app, f.team, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND source_id=?`, f.team, shared.ID).Row().Scan(&count)
		}))
		require.Equal(t, 1, count)
	}
	var leaked int
	require.NoError(t, f.rls.WithTeamTx(other.actor(0, "manager"), f.app, other.team, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid`, f.team).Row().Scan(&leaked)
	}))
	require.Zero(t, leaked)
	_, err = service.Status(other.actor(0, "manager"))
	require.ErrorIs(t, err, ontology.ErrUnauthorized)
	require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND id=?::uuid`, f.team, f.space).Error
	}))
	f.generation++
	f.organizationEvidence(t, 1, "New generation shared fact.", nil)
	drainMaintenance(t, service)
	var oldLive int
	require.NoError(t, f.rls.WithTeamTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid AND space_generation<?`, f.team, f.generation).Row().Scan(&oldLive)
	}))
	require.Zero(t, oldLive)
	var current int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_batches WHERE team_id=?::uuid AND space_generation=? AND status='completed'`, f.team, f.generation).Row().Scan(&current))
	require.Greater(t, current, 0)
}

func TestOntologyMaintenanceMarkerFrameRLS(t *testing.T) {
	f := newOrganizationFixture(t)
	other := maintenanceOtherTeam(t, f)
	old := maintenanceEntity(t, f, "Historical marker")
	peer := maintenanceEntity(t, other, "Other-team marker")
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.app, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND id=?::uuid`, f.team, f.space).Error
	}))
	f.generation++
	current := maintenanceEntity(t, f, "Current marker")
	inspect := func(tx *gorm.DB) error {
		var visible, historical, crossTeam int
		err := tx.Raw(`SELECT count(*) FILTER (WHERE anchor_id=?),count(*) FILTER (WHERE anchor_id=?),count(*) FILTER (WHERE anchor_id=?) FROM ontology_maintenance_markers`, current.ID, old.ID, peer.ID).Row().Scan(&visible, &historical, &crossTeam)
		if err != nil {
			return err
		}
		require.Equal(t, 1, visible)
		require.Zero(t, historical)
		require.Zero(t, crossTeam)
		return nil
	}
	for _, owner := range []int{0, 1} {
		ctx := f.actor(owner, "member")
		require.NoError(t, f.rls.WithTeamTx(ctx, f.app, f.team, inspect))
		require.NoError(t, f.rls.WithTeamProfileTx(ctx, f.app, f.team, f.owners[owner], func(tx *gorm.DB) error {
			if err := inspect(tx); err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE ontology_maintenance_markers SET cursor='expanded' WHERE anchor_kind='entity_records' AND anchor_id=?`, current.ID).Error; err != nil {
				return err
			}
			if err := tx.Exec(`SELECT dense_mem_enqueue_ontology_marker(?::uuid,?::uuid,?,'entity_records',?,'entity',?)`, f.team, f.space, f.generation, current.ID, current.ID).Error; err != nil {
				return err
			}
			var cursor string
			if err := tx.Raw(`SELECT cursor FROM ontology_maintenance_markers WHERE anchor_kind='entity_records' AND anchor_id=?`, current.ID).Row().Scan(&cursor); err != nil {
				return err
			}
			require.Empty(t, cursor)
			return nil
		}))
		err := f.rls.WithTeamProfileTx(ctx, f.app, f.team, f.owners[owner], func(tx *gorm.DB) error {
			return tx.Exec(`INSERT INTO ontology_maintenance_markers(team_id,shared_space_id,space_generation,anchor_kind,anchor_id,target_kind,target_id) VALUES (?::uuid,?::uuid,?,'entity_records',?,'entity',?)`, f.team, f.space, f.generation-1, uuid.NewString(), old.ID).Error
		})
		require.ErrorContains(t, err, "row-level security")
	}
	require.NoError(t, f.rls.WithTeamProfileTx(other.actor(0, "member"), f.app, other.team, other.owners[0], func(tx *gorm.DB) error {
		var leaked int
		if err := tx.Raw(`SELECT count(*) FROM ontology_maintenance_markers WHERE team_id=?::uuid`, f.team).Row().Scan(&leaked); err != nil {
			return err
		}
		require.Zero(t, leaked)
		return nil
	}))
	for _, mode := range []string{"system", "migration"} {
		require.NoError(t, f.rls.WithSystemTx(context.Background(), f.app, func(tx *gorm.DB) error {
			if err := tx.Exec(`SELECT set_config('app.tx_mode',?,true)`, mode).Error; err != nil {
				return err
			}
			var visible int
			if err := tx.Raw(`SELECT count(*) FROM ontology_maintenance_markers WHERE anchor_id IN (?,?,?)`, current.ID, old.ID, peer.ID).Row().Scan(&visible); err != nil {
				return err
			}
			require.Equal(t, 3, visible)
			return nil
		}))
	}
}
