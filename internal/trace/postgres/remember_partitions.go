package postgres

import (
	"context"
	"github.com/lib/pq"
	trace "github.com/markhuangai/dense-mem/internal/trace/contract"
	"gorm.io/gorm"
)

func loadTracePartitions(ctx context.Context, tx *gorm.DB, team, space string, ids []string) (map[string]*trace.PartitionProvenance, error) {
	rows, err := tx.WithContext(ctx).Raw(`SELECT mapping.occurrence_id::text,mapping.source_item_id::text,original.submitted_index,mapping.partition_index,mapping.span_start,mapping.span_end,original.content_hash,original.policy_version
 FROM remember_item_partitions mapping JOIN remember_submitted_items original
 ON original.team_id=mapping.team_id AND original.source_item_id=mapping.source_item_id AND original.owner_profile_id=mapping.owner_profile_id AND original.space_id=mapping.space_id AND original.space_generation=mapping.space_generation
 WHERE mapping.team_id=?::uuid AND mapping.space_id=?::uuid AND mapping.occurrence_id=ANY(?::uuid[])
 AND `+activeSemanticSpaceGenerationSQL("mapping")+` AND `+activeSemanticSpaceGenerationSQL("original"), team, space, pq.Array(ids)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]*trace.PartitionProvenance{}
	for rows.Next() {
		var id string
		var p trace.PartitionProvenance
		if err := rows.Scan(&id, &p.SourceItemID, &p.SubmittedIndex, &p.PartitionIndex, &p.SpanStart, &p.SpanEnd, &p.OriginalContentHash, &p.PolicyVersion); err != nil {
			return nil, err
		}
		result[id] = &p
	}
	return result, rows.Err()
}
