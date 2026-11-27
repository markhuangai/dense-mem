package postgres

import (
	"github.com/lib/pq"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func writeRelationalRecord(tx *gorm.DB, fence scope, record topic.Record, origin string, preserveMaterial bool) error {
	key := []any{fence.TeamID, fence.SpaceID, fence.Generation, record.ID, record.Version, record.Retired}
	var write *gorm.DB
	switch record.Kind {
	case topic.Topic:
		definition := record.Definition
		aliases := definition.Aliases
		if aliases == nil {
			aliases = []string{}
		}
		write = tx.Exec(`INSERT INTO topics(team_id,shared_space_id,space_generation,topic_id,definition_version,
			retired,version,topic_key,label,description,aliases,parent_topic_id,placement,manager_authored,material_definition_version)
			VALUES (?::uuid,?::uuid,?,?::uuid,?,?,?, ?,?,?,?::text[],NULLIF(?,'')::uuid,'declared',?,?)
			ON CONFLICT(team_id,shared_space_id,space_generation,topic_id) DO UPDATE SET
				definition_version=excluded.definition_version,version=topics.version+1,retired=excluded.retired,
				topic_key=excluded.topic_key,label=excluded.label,description=excluded.description,
				aliases=excluded.aliases,
				parent_topic_id=CASE WHEN ? THEN topics.parent_topic_id WHEN excluded.parent_topic_id IS NOT NULL THEN excluded.parent_topic_id
					WHEN topics.placement IN ('placed','standalone') THEN topics.parent_topic_id ELSE NULL END,
				placement=CASE WHEN ? THEN topics.placement WHEN excluded.parent_topic_id IS NOT NULL THEN 'declared'
					WHEN topics.placement IN ('placed','standalone') THEN topics.placement ELSE 'declared' END,
				manager_authored=topics.manager_authored OR excluded.manager_authored,
				material_definition_version=CASE WHEN ? THEN topics.material_definition_version ELSE excluded.definition_version END
			WHERE topics.definition_version=?`, append(key, record.Version, definition.Key, definition.Label,
			definition.Description, pq.Array(aliases), definition.ParentID, origin == "manager" || origin == "rollback",
			record.Version, preserveMaterial, preserveMaterial, preserveMaterial, record.Version-1)...)
	case topic.AssignmentKind:
		var topicVersion int64
		var fingerprint string
		for _, dependency := range record.Dependencies {
			if dependency.ID == record.Assignment.DefinitionID {
				topicVersion = dependency.Version
				break
			}
		}
		for _, source := range record.Sources {
			if source.SourceHandle == record.Assignment.Source {
				fingerprint = source.Fingerprint
				break
			}
		}
		write = tx.Exec(`INSERT INTO topic_memberships(team_id,shared_space_id,space_generation,record_id,version,
			retired,evidence_id,topic_id,topic_version,source_fingerprint)
			VALUES (?::uuid,?::uuid,?,?::uuid,?,?,?::uuid,?::uuid,?,?)
			ON CONFLICT(team_id,shared_space_id,space_generation,record_id) DO UPDATE SET
				version=excluded.version,retired=excluded.retired,evidence_id=excluded.evidence_id,
				topic_id=excluded.topic_id,topic_version=excluded.topic_version,source_fingerprint=excluded.source_fingerprint
			WHERE topic_memberships.version=?`, append(key, record.Assignment.Source.ID,
			record.Assignment.DefinitionID, topicVersion, fingerprint, record.Version-1)...)
	case topic.EvidenceGroup:
		members := make([]string, len(record.Group.Members))
		for i, member := range record.Group.Members {
			members[i] = member.ID
		}
		write = tx.Exec(`INSERT INTO evidence_equivalences(team_id,shared_space_id,space_generation,record_id,version,retired,members)
			VALUES (?::uuid,?::uuid,?,?::uuid,?,?,?::uuid[])
			ON CONFLICT(team_id,shared_space_id,space_generation,record_id) DO UPDATE SET
				version=excluded.version,retired=excluded.retired,members=excluded.members
			WHERE evidence_equivalences.version=?`, append(key, pq.Array(members), record.Version-1)...)
	default:
		return nil // Historical override records retain their original representation.
	}
	if write.Error != nil {
		return write.Error
	}
	if write.RowsAffected != 1 {
		return topic.ErrConflict
	}
	return nil
}
