package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/lib/pq"
	semantic "github.com/markhuangai/dense-mem/internal/semanticwrite/contract"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func (s *Store) ReadTopicEmbeddingBatch(ctx context.Context, turn topic.MaintenanceTurn) (*topic.TopicEmbeddingBatch, error) {
	if err := requireAutomatic(ctx); err != nil {
		return nil, err
	}
	var batch topic.TopicEmbeddingBatch
	err := s.withScope(ctx, turn.TeamID, true, func(tx *gorm.DB, fence scope) error {
		if fence.SpaceID != turn.SpaceID || fence.Generation != turn.Generation {
			return topic.ErrLeaseLost
		}
		if err := checkMaintenanceTurn(tx, turn); err != nil {
			return err
		}
		err := tx.Raw(`SELECT contract.model,contract.dimensions,contract.embedding_contract_id::text,
			generation.search_index_generation_id::text,generation.generation
			FROM search_index_generations AS generation JOIN embedding_contracts AS contract
			ON contract.embedding_contract_id=generation.embedding_contract_id AND contract.dimensions=generation.embedding_dimensions
			WHERE contract.lifecycle_state='active' AND generation.activation_state='active' AND contract.distance_metric='cosine'
			ORDER BY contract.version DESC,generation.generation DESC LIMIT 1`).Row().Scan(
			&batch.Fence.Model, &batch.Fence.Dimensions, &batch.Fence.EmbeddingContractID,
			&batch.Fence.SearchGenerationID, &batch.Fence.SearchGenerationVersion)
		if errors.Is(err, sql.ErrNoRows) {
			return topic.ErrConfiguration
		}
		if err != nil {
			return err
		}
		rows, err := tx.Raw(`SELECT current.topic_id::text,current.definition_version,
			concat_ws(' ',current.topic_key,current.label,current.description,array_to_string(current.aliases,' '))
			FROM topics AS current WHERE current.team_id=?::uuid AND current.shared_space_id=?::uuid
			AND current.space_generation=? AND NOT current.retired AND NOT EXISTS (
			SELECT 1 FROM topic_embeddings AS embedding WHERE
			(embedding.team_id,embedding.shared_space_id,embedding.space_generation,embedding.topic_id)=
			(current.team_id,current.shared_space_id,current.space_generation,current.topic_id)
			AND embedding.embedding_contract_id=?::uuid AND embedding.topic_version=current.definition_version)
			ORDER BY current.topic_id LIMIT ?`, fence.TeamID, fence.SpaceID, fence.Generation,
			batch.Fence.EmbeddingContractID, 256).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item topic.TopicEmbeddingCandidate
			if err := rows.Scan(&item.ID, &item.Version, &item.Text); err != nil {
				return err
			}
			batch.Topics = append(batch.Topics, item)
		}
		return rows.Err()
	})
	if err != nil || len(batch.Topics) == 0 {
		return nil, err
	}
	return &batch, nil
}

func (s *Store) CompleteTopicEmbeddingBatch(ctx context.Context, turn topic.MaintenanceTurn, batch topic.TopicEmbeddingBatch, result semantic.Result) (int, error) {
	if err := requireAutomatic(ctx); err != nil {
		return 0, err
	}
	if len(batch.Topics) == 0 || len(batch.Topics) > 256 || result.Fence != batch.Fence || result.Model != batch.Fence.Model || len(result.Embeddings) != len(batch.Topics) {
		return 0, topic.ErrInvalid
	}
	type vector struct {
		item topic.TopicEmbeddingCandidate
		data []float32
	}
	vectors := make([]vector, 0, len(batch.Topics))
	seen := map[string]bool{}
	for i, item := range batch.Topics {
		identity := fmt.Sprintf("%s@%d", item.ID, item.Version)
		if seen[item.ID] || result.Embeddings[i].DocumentHash != identity || len(result.Embeddings[i].Vector) != batch.Fence.Dimensions {
			return 0, topic.ErrInvalid
		}
		seen[item.ID] = true
		for _, value := range result.Embeddings[i].Vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return 0, topic.ErrInvalid
			}
		}
		vectors = append(vectors, vector{item: item, data: result.Embeddings[i].Vector})
	}
	sort.Slice(vectors, func(i, j int) bool { return vectors[i].item.ID < vectors[j].item.ID })
	count := 0
	err := s.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		paused, err := maintenanceStateShared(tx)
		if err != nil {
			return err
		}
		if paused {
			return topic.ErrMaintenancePaused
		}
		enabled, err := maintenanceEnabled(tx)
		if err != nil {
			return err
		}
		if !enabled {
			return topic.ErrMaintenanceDisabled
		}
		fence, err := sharedScope(ctx, tx, turn.TeamID)
		if err != nil {
			return err
		}
		if fence.SpaceID != turn.SpaceID || fence.Generation != turn.Generation {
			return topic.ErrLeaseLost
		}
		var valid bool
		err = tx.Raw(`SELECT true FROM topic_maintenance_runs AS run JOIN topic_maintenance_windows AS maintenance_window USING(window_id)
			WHERE run.run_id=?::uuid AND run.status IN ('pending','running') AND maintenance_window.ends_at>clock_timestamp()
			FOR SHARE OF run,maintenance_window`, turn.RunID).Row().Scan(&valid)
		if errors.Is(err, sql.ErrNoRows) {
			return topic.ErrWindowClosed
		}
		if err != nil {
			return err
		}
		err = tx.Raw(`SELECT lease_until>clock_timestamp() FROM topic_maintenance_teams
			WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid FOR NO KEY UPDATE`,
			turn.TeamID, turn.SpaceID, turn.Generation, turn.LeaseToken).Row().Scan(&valid)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && !valid) {
			return topic.ErrLeaseLost
		}
		if err != nil {
			return err
		}
		err = tx.Raw(`SELECT true FROM memory_spaces WHERE team_id=?::uuid AND id=?::uuid
			AND generation=? AND kind='team_shared' AND lifecycle_state='active' FOR SHARE`,
			fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&valid)
		if errors.Is(err, sql.ErrNoRows) {
			return topic.ErrLeaseLost
		}
		if err != nil {
			return err
		}
		err = tx.Raw(`SELECT true FROM search_index_generations AS generation JOIN embedding_contracts AS contract
			ON contract.embedding_contract_id=generation.embedding_contract_id AND contract.dimensions=generation.embedding_dimensions
			WHERE contract.embedding_contract_id=?::uuid AND contract.model=? AND contract.dimensions=? AND contract.lifecycle_state='active'
			AND generation.search_index_generation_id=?::uuid AND generation.generation=? AND generation.activation_state='active'
			FOR SHARE OF generation,contract`,
			batch.Fence.EmbeddingContractID, batch.Fence.Model, batch.Fence.Dimensions,
			batch.Fence.SearchGenerationID, batch.Fence.SearchGenerationVersion).Row().Scan(&valid)
		if errors.Is(err, sql.ErrNoRows) {
			return topic.ErrSourceStale
		}
		if err != nil {
			return err
		}
		for _, item := range vectors {
			var current int64
			err = tx.Raw(`SELECT definition_version FROM topics WHERE team_id=?::uuid AND shared_space_id=?::uuid
				AND space_generation=? AND topic_id=?::uuid AND NOT retired FOR SHARE`, fence.TeamID, fence.SpaceID, fence.Generation, item.item.ID).Row().Scan(&current)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && current != item.item.Version) {
				continue
			}
			if err != nil {
				return err
			}
			updated := tx.Exec(`INSERT INTO topic_embeddings(team_id,shared_space_id,space_generation,topic_id,topic_version,embedding_contract_id,embedding_dimensions,embedding)
				VALUES(?::uuid,?::uuid,?,?::uuid,?,?::uuid,?,(?::real[])::vector)
				ON CONFLICT(team_id,shared_space_id,space_generation,topic_id,embedding_contract_id) DO UPDATE
				SET consolidation_pending=CASE WHEN topic_embeddings.topic_version<>EXCLUDED.topic_version THEN true ELSE topic_embeddings.consolidation_pending END,
				consolidation_batch_id=CASE WHEN topic_embeddings.topic_version<>EXCLUDED.topic_version THEN NULL ELSE topic_embeddings.consolidation_batch_id END,
				consolidation_lease_until=CASE WHEN topic_embeddings.topic_version<>EXCLUDED.topic_version THEN NULL ELSE topic_embeddings.consolidation_lease_until END,
				topic_version=EXCLUDED.topic_version,embedding_dimensions=EXCLUDED.embedding_dimensions,embedding=EXCLUDED.embedding,updated_at=clock_timestamp()
				WHERE topic_embeddings.topic_version<=EXCLUDED.topic_version`, fence.TeamID, fence.SpaceID, fence.Generation,
				item.item.ID, item.item.Version, batch.Fence.EmbeddingContractID, batch.Fence.Dimensions, pq.Array(item.data))
			if updated.Error != nil {
				return updated.Error
			}
			count += int(updated.RowsAffected)
		}
		if err := checkMaintenanceTurn(tx, turn); err != nil {
			return err
		}
		if count > 0 {
			return recordTeamMaintenanceProgress(tx, fence.TeamID, fence.SpaceID, fence.Generation)
		}
		return nil
	})
	return count, err
}
