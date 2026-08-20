package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

func (r *SemanticRepositoryImpl) ListCommunities(ctx context.Context, input CommunityListInput) ([]CommunityRecord, error) {
	input = normalizeCommunityListInput(input)
	if err := validateCommunityListInput(input); err != nil {
		return nil, err
	}
	var records []CommunityRecord
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		fence, err := loadTeamSharedSpaceFence(ctx, tx, input.TeamID)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, community_id::text, COALESCE(logical_community_id, community_id)::text, run_id::text, ordinal, status,
			       summary, summary_version, member_count, source_count,
			       top_entities, top_predicates, source_fingerprint, stale_reason,
			       created_at, updated_at, superseded_at
			FROM community_records
			WHERE team_id = ?::uuid
			  AND space_id = ?::uuid
			  AND space_generation = ?
			  AND status = ?
			ORDER BY member_count DESC, community_id ASC
			LIMIT ?
		`, input.TeamID, fence.ID, fence.Generation, input.Status, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		var errScan error
		records, errScan = scanCommunityRecords(rows)
		return errScan
	})
	if err != nil {
		return nil, fmt.Errorf("community: list communities: %w", err)
	}
	return records, nil
}

func (r *SemanticRepositoryImpl) CountCurrentCommunities(ctx context.Context, teamID string) (int, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return 0, fmt.Errorf("team_id is required: %w", err)
	}
	var count int
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		fence, err := loadTeamSharedSpaceFence(ctx, tx, teamID)
		if err != nil {
			return err
		}
		return tx.WithContext(ctx).Raw(`
			SELECT count(*)::int
			FROM community_records
			WHERE team_id = ?::uuid
			  AND space_id = ?::uuid
			  AND space_generation = ?
			  AND status = 'current'
		`, teamID, fence.ID, fence.Generation).Scan(&count).Error
	})
	if err != nil {
		return 0, fmt.Errorf("community: count current communities: %w", err)
	}
	return count, nil
}

func (r *SemanticRepositoryImpl) GetCommunity(ctx context.Context, input CommunityGetInput) (*CommunityRecord, error) {
	input = normalizeCommunityGetInput(input)
	if err := validateCommunityGetInput(input); err != nil {
		return nil, err
	}
	var record *CommunityRecord
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		fence, err := loadTeamSharedSpaceFence(ctx, tx, input.TeamID)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, community_id::text, COALESCE(logical_community_id, community_id)::text, run_id::text, ordinal, status,
			       summary, summary_version, member_count, source_count,
			       top_entities, top_predicates, source_fingerprint, stale_reason,
			       created_at, updated_at, superseded_at
			FROM community_records
			WHERE team_id = ?::uuid
			  AND space_id = ?::uuid
			  AND space_generation = ?
			  AND community_id = ?::uuid
		`, input.TeamID, fence.ID, fence.Generation, input.CommunityID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		records, err := scanCommunityRecords(rows)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return ErrCommunityNotFound
		}
		record = &records[0]
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("community: get community: %w", err)
	}
	return record, nil
}

func (r *SemanticRepositoryImpl) LatestCommunityRun(ctx context.Context, teamID string) (*CommunityRun, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	var run *CommunityRun
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		fence, err := loadTeamSharedSpaceFence(ctx, tx, teamID)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, run_id::text, window_key, status, algorithm_kind,
			       algorithm_version, profile_version, configuration_hash,
			       source_fingerprint, node_count, edge_count, community_count,
			       max_nodes, max_edges, error, started_at, completed_at,
			       false AS claimed
			FROM community_snapshot_runs
			WHERE team_id = ?::uuid
			  AND space_id = ?::uuid
			  AND space_generation = ?
			ORDER BY started_at DESC, run_id DESC
			LIMIT 1
		`, teamID, fence.ID, fence.Generation).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return nil
		}
		scanned, err := scanCommunityRun(rows)
		if err != nil {
			return err
		}
		run = scanned
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("community: latest run: %w", err)
	}
	return run, nil
}

func (r *SemanticRepositoryImpl) ListCurrentCommunityLineage(ctx context.Context, teamID string) ([]CommunityLineageRecord, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	lineage := []CommunityLineageRecord{}
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		fence, err := loadTeamSharedSpaceFence(ctx, tx, teamID)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT record.community_id::text,
			       COALESCE(record.logical_community_id, record.community_id)::text,
			       COALESCE(array_agg(DISTINCT source.semantic_group_key ORDER BY source.semantic_group_key)
			                FILTER (WHERE source.semantic_group_key <> ''), ARRAY[]::text[]),
			       record.summary_input_hash, record.summary, record.summary_version,
			       record.summary_provider_model, record.summary_prompt_hash, record.summary_response_hash
			FROM community_records AS record
			LEFT JOIN community_sources AS source
			  ON source.team_id = record.team_id
			 AND source.community_id = record.community_id
			 AND source.space_id = record.space_id
			 AND source.space_generation = record.space_generation
			WHERE record.team_id = ?::uuid
			  AND record.space_id = ?::uuid
			  AND record.space_generation = ?
			  AND record.status = 'current'
			GROUP BY record.community_id, record.logical_community_id, record.updated_at,
			         record.summary_input_hash, record.summary, record.summary_version,
			         record.summary_provider_model, record.summary_prompt_hash, record.summary_response_hash
			ORDER BY record.updated_at DESC, record.community_id ASC
		`, teamID, fence.ID, fence.Generation).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var record CommunityLineageRecord
			var groups pq.StringArray
			if err := rows.Scan(&record.CommunityID, &record.LogicalCommunityID, &groups,
				&record.SummaryInputHash, &record.Summary, &record.SummaryVersion,
				&record.SummaryProviderModel, &record.SummaryPromptHash, &record.SummaryResponseHash); err != nil {
				return err
			}
			record.GroupKeys = []string(groups)
			lineage = append(lineage, record)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("community: list lineage: %w", err)
	}
	return lineage, nil
}
