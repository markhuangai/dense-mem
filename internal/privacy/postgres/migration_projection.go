package postgres

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/lib/pq"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"gorm.io/gorm"
)

func (p *privateMigrationPlan) planConflictRestoration(tx *gorm.DB) error {
	var mixed []string
	for _, space := range p.Spaces {
		if len(space.Writers) > 1 && space.State == "active" {
			mixed = append(mixed, space.ID)
		}
	}
	if len(mixed) == 0 {
		return nil
	}
	rows, err := tx.Raw(`SELECT relationship.team_id::text,relationship.relationship_id::text,
   COALESCE((SELECT jsonb_agg(DISTINCT jsonb_build_object('valid_to',observation.valid_to)) FROM relationship_observations observation
    WHERE observation.team_id=relationship.team_id AND observation.relationship_id=relationship.relationship_id AND observation.created_at=relationship.created_at),'[]'::jsonb)
  FROM relationship_records relationship JOIN LATERAL (
   SELECT reason,to_status FROM relationship_transition_events event WHERE event.team_id=relationship.team_id AND event.relationship_id=relationship.relationship_id
   ORDER BY created_at DESC,transition_id DESC LIMIT 1
  ) transition ON transition.reason='conflict_resolved' AND transition.to_status=relationship.status
  WHERE relationship.space_id=ANY(?::uuid[]) AND relationship.status='superseded'`, pq.Array(mixed)).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	records := map[string]*privateMigrationRecord{}
	for _, record := range p.Records {
		if record.Table.Name == "relationship_records" {
			records[record.Key] = record
		}
	}
	for rows.Next() {
		var team, id string
		var raw []byte
		if err := rows.Scan(&team, &id, &raw); err != nil {
			return err
		}
		key, _ := json.Marshal([]string{team, id})
		record := records[string(key)]
		if record == nil {
			return fmt.Errorf("conflict projection has no migration record")
		}
		var observations []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &observations); err != nil {
			return err
		}
		if len(observations) != 1 {
			p.Report.Blockers = append(p.Report.Blockers, "ambiguous_pre_conflict_validity:"+id)
			continue
		}
		record.RestoreConflict = true
		record.RestoredValidTo = observations[0]["valid_to"]
	}
	return rows.Err()
}

func (r *PrivateMemoryRepositoryImpl) rebuildPrivateMigrationBatch(ctx context.Context) (int, error) {
	count := 0
	err := r.privateMigrationTx(ctx, func(tx *gorm.DB) error {
		if err := tx.Exec(`UPDATE memory_spaces space SET generation=dest.generation FROM (SELECT destination_space_id,max(destination_generation) generation FROM private_memory_migration_records GROUP BY destination_space_id) dest WHERE space.id=dest.destination_space_id AND EXISTS(SELECT 1 FROM private_memory_migration_state WHERE singleton AND phase<>'rebuilding')`).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE private_memory_migration_state SET phase='rebuilding',updated_at=now() WHERE singleton`).Error; err != nil {
			return err
		}
		rows, err := tx.Raw(`SELECT mapping.record_id::text,COALESCE((mapping.replacements->>'_restore_conflict')::boolean,false),
   relationship.team_id::text,relationship.relationship_id::text,relationship.owner_profile_id::text,relationship.space_id::text,relationship.space_generation,
   relationship.status,relationship.version,relationship.support_count,relationship.source_group_count,COALESCE(relationship.identity_alias_of_relationship_id::text,''),
   relationship.subject_entity_id::text,relationship.predicate_key,COALESCE(relationship.object_entity_id::text,''),COALESCE(relationship.object_value_id::text,''),relationship.polarity,COALESCE(relationship.scope_key,''),relationship.valid_from,relationship.valid_to
   FROM private_memory_migration_records mapping JOIN relationship_records relationship ON mapping.team_id=relationship.team_id AND mapping.destination_key=jsonb_build_array(relationship.team_id,relationship.relationship_id)
   WHERE mapping.source_table='relationship_records' AND mapping.completed AND NOT mapping.sealed AND NOT mapping.projection_rebuilt
   ORDER BY mapping.record_id LIMIT ? FOR UPDATE OF mapping,relationship`, privateMigrationBatchSize).Rows()
		if err != nil {
			return err
		}
		type projection struct {
			id       string
			restored bool
			record   knowledge.RelationshipRecord
		}
		var projections []projection
		for rows.Next() {
			var item projection
			record := &item.record
			if err := rows.Scan(&item.id, &item.restored, &record.TeamID, &record.RelationshipID, &record.OwnerProfileID, &record.SpaceID, &record.SpaceGeneration, &record.Status, &record.Version, &record.SupportCount, &record.SourceGroupCount, &record.IdentityAliasOfID, &record.SubjectEntityID, &record.PredicateKey, &record.ObjectEntityID, &record.ObjectValueID, &record.Polarity, &record.ScopeKey, &record.ValidFrom, &record.ValidTo); err != nil {
				rows.Close()
				return err
			}
			projections = append(projections, item)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, item := range projections {
			if item.restored {
				if err := tx.Exec(`INSERT INTO relationship_transition_events(team_id,relationship_id,owner_profile_id,from_status,to_status,reason,space_id,space_generation,idempotency_key)
      VALUES(?::uuid,?::uuid,?::uuid,'superseded','active','private space split removes foreign conflict suppression',?::uuid,?,?)`, item.record.TeamID, item.record.RelationshipID, item.record.OwnerProfileID, item.record.SpaceID, item.record.SpaceGeneration, "private-migration:"+item.id).Error; err != nil {
					return err
				}
			}
			if err := refreshPrivateRelationshipTx(tx, item.record, "private space split recomputes support"); err != nil {
				return err
			}
			if err := tx.Exec(`UPDATE private_memory_migration_records SET projection_rebuilt=true WHERE record_id=?::uuid`, item.id).Error; err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return count, err
}
