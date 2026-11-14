package postgres

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"gorm.io/gorm"
)

func insertRememberPartitions(ctx context.Context, tx *gorm.DB, input SynchronousRememberCommitInput, evidence []EvidenceFragment) error {
	if err := knowledge.ValidateRememberPartitions(input.SubmittedItems, input.Evidence); err != nil {
		return err
	}
	if len(input.SubmittedItems) == 0 {
		return nil
	}
	for _, item := range input.SubmittedItems {
		if _, err := uuid.Parse(item.ID); err != nil {
			return fmt.Errorf("submitted source item ID is invalid: %w", err)
		}
		sourceID, revisionID := "", ""
		for i, e := range input.Evidence {
			if e.Partition.SubmittedIndex == item.Index {
				sourceID, revisionID = evidence[i].SourceID, evidence[i].SourceRevisionID
				break
			}
		}
		if err := tx.WithContext(ctx).Exec(`INSERT INTO remember_submitted_items(team_id,source_item_id,ingest_id,owner_profile_id,space_id,space_generation,submitted_index,content,content_hash,policy_version,source_id,source_revision_id)
 VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?::uuid,?,?,?,?,?,NULLIF(?,'')::uuid,NULLIF(?,'')::uuid)`, input.TeamID, item.ID, input.IngestID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration, item.Index, item.Content, item.ContentHash, item.PolicyVersion, sourceID, revisionID).Error; err != nil {
			return err
		}
	}
	for i, item := range input.Evidence {
		p := item.Partition
		evidence[i].Partition = p
		if err := tx.WithContext(ctx).Exec(`INSERT INTO remember_item_partitions(team_id,source_item_id,owner_profile_id,space_id,space_generation,partition_index,occurrence_id,span_start,span_end)
 VALUES(?::uuid,?::uuid,?::uuid,?::uuid,?,?,?::uuid,?,?)`, input.TeamID, p.SourceItemID, input.OwnerProfileID, input.SpaceID, input.SpaceGeneration, p.Index, evidence[i].OccurrenceID, p.Start, p.End).Error; err != nil {
			return err
		}
	}
	return nil
}
