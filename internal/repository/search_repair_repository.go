package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const searchRepairCandidateLimit = 256

const searchRepairActiveContractFenceSQL = `
	EXISTS (
		SELECT 1
		FROM search_index_generations AS generation
		JOIN embedding_contracts AS contract
		  ON contract.embedding_contract_id = generation.embedding_contract_id
		 AND contract.dimensions = generation.embedding_dimensions
		WHERE generation.search_index_generation_id = ?::uuid
		  AND generation.embedding_contract_id = ?::uuid
		  AND generation.embedding_dimensions = ?
		  AND generation.generation = ?
		  AND generation.activation_state = 'active'
		  AND contract.lifecycle_state = 'active'
		  AND contract.distance_metric = 'cosine'
	)
`

const searchRepairRelationshipProjectionFenceSQL = `
	NULLIF(?, '')::uuid IS NOT DISTINCT FROM COALESCE(
		(
			SELECT generation.projection_generation_id
			FROM search_projection_generations AS generation
			WHERE generation.team_id = ?::uuid
			  AND generation.source_kind = 'relationship'
			  AND generation.projection_format_version = 2
			  AND generation.state = 'current'
			  AND generation.activated_at IS NOT NULL
			ORDER BY generation.generation DESC, generation.created_at DESC
			LIMIT 1
		),
		(
			SELECT generation.projection_generation_id
			FROM search_projection_generations AS generation
			WHERE generation.team_id = ?::uuid
			  AND generation.source_kind = 'relationship'
			  AND generation.projection_format_version = 2
			ORDER BY generation.generation DESC, generation.created_at DESC
			LIMIT 1
		)
	)
`

// searchRepairEvidenceTerminalPlacementJoinSQL keeps evidence search eligibility
// tied to the terminal placement outcome rather than the staged-ingest status.
const searchRepairEvidenceTerminalPlacementJoinSQL = `
	JOIN placement_runs AS placement_run
	  ON placement_run.team_id = fragment.team_id
	 AND placement_run.ingest_id = fragment.ingest_id
	 AND placement_run.owner_profile_id = fragment.owner_profile_id
	 AND placement_run.space_id IS NOT DISTINCT FROM fragment.space_id
	 AND placement_run.space_generation IS NOT DISTINCT FROM fragment.space_generation
	 AND placement_run.status = 'completed'
	JOIN placement_items AS placement_item
	  ON placement_item.team_id = fragment.team_id
	 AND placement_item.placement_run_id = placement_run.placement_run_id
	 AND placement_item.ingest_id = fragment.ingest_id
	 AND placement_item.owner_profile_id = fragment.owner_profile_id
	 AND placement_item.fragment_id = fragment.fragment_id
	 AND placement_item.space_id IS NOT DISTINCT FROM fragment.space_id
	 AND placement_item.space_generation IS NOT DISTINCT FROM fragment.space_generation
	 AND placement_item.status = 'completed'
	LEFT JOIN evidence_sources AS source
	  ON source.team_id = fragment.team_id
	 AND source.source_id = fragment.source_id
`

var _ SearchRepairRepository = (*SearchRepositoryImpl)(nil)

func (r *SearchRepositoryImpl) GetSearchRepairTime(ctx context.Context) (time.Time, error) {
	var now time.Time
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(&now).Error
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("search: repair clock: %w", err)
	}
	return now.UTC(), nil
}

func (r *SearchRepositoryImpl) ReserveSearchRepairRun(ctx context.Context, input SearchRepairRunInput) (*SearchRepairRun, bool, error) {
	input = normalizeSearchRepairRunInput(input)
	if err := validateSearchRepairRunInput(input); err != nil {
		return nil, false, err
	}
	var run *SearchRepairRun
	claimed := false
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		if input.CreateIfMissing {
			var runExists bool
			if err := tx.WithContext(ctx).Raw(`
				SELECT EXISTS (
					SELECT 1
					FROM embedding_reconciliation_runs
					WHERE embedding_contract_id = ?::uuid
					  AND embedding_dimensions = ?
					  AND local_run_date = ?
				)
			`, input.EmbeddingContractID, input.EmbeddingDimensions, input.LocalRunDate.Format("2006-01-02")).Scan(&runExists).Error; err != nil {
				return err
			}
			if !runExists {
				var potentiallyEligible bool
				if err := tx.WithContext(ctx).Raw(searchRepairReservationProbeSQL,
					input.EmbeddingContractID, input.EmbeddingDimensions,
				).Scan(&potentiallyEligible).Error; err != nil {
					return err
				}
				if potentiallyEligible {
					if err := tx.WithContext(ctx).Exec(`
						INSERT INTO embedding_reconciliation_runs (
							embedding_contract_id, embedding_dimensions, local_run_date,
							candidate_cutoff, status
						) VALUES (?, ?, ?, clock_timestamp(), 'reserved')
						ON CONFLICT (embedding_contract_id, embedding_dimensions, local_run_date) DO NOTHING
					`, input.EmbeddingContractID, input.EmbeddingDimensions, input.LocalRunDate.Format("2006-01-02")).Error; err != nil {
						return err
					}
				}
			}
		}
		var value SearchRepairRun
		var leaseToken sql.NullString
		var leaseUntil, startedAt, completedAt sql.NullTime
		err := tx.WithContext(ctx).Raw(`
			SELECT reconciliation_run_id::text, embedding_contract_id::text,
			       embedding_dimensions, local_run_date, status, lease_token::text,
			       lease_until, selected_count, embedded_count, updated_count,
			       drifted_count, last_error, started_at, completed_at, updated_at
			FROM embedding_reconciliation_runs
			WHERE embedding_contract_id = ?::uuid
			  AND embedding_dimensions = ?
			  AND local_run_date = ?
			FOR UPDATE
		`, input.EmbeddingContractID, input.EmbeddingDimensions, input.LocalRunDate.Format("2006-01-02")).Row().Scan(
			&value.RunID, &value.EmbeddingContractID, &value.EmbeddingDimensions,
			&value.LocalRunDate, &value.Status, &leaseToken, &leaseUntil,
			&value.SelectedCount, &value.EmbeddedCount, &value.UpdatedCount,
			&value.DriftedCount, &value.LastError, &startedAt, &completedAt, &value.UpdatedAt,
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if leaseToken.Valid {
			value.LeaseToken = leaseToken.String
		}
		if leaseUntil.Valid {
			lease := leaseUntil.Time.UTC()
			value.LeaseUntil = &lease
		}
		if startedAt.Valid {
			started := startedAt.Time.UTC()
			value.StartedAt = &started
		}
		if completedAt.Valid {
			completed := completedAt.Time.UTC()
			value.CompletedAt = &completed
		}
		if value.Status == string(domain.EmbeddingReconciliationCompleted) ||
			value.Status == string(domain.EmbeddingReconciliationDeferred) ||
			value.Status == string(domain.EmbeddingReconciliationFailed) ||
			value.Status == string(domain.EmbeddingReconciliationAmbiguous) {
			run = &value
			return nil
		}
		var now time.Time
		if err := tx.WithContext(ctx).Raw("SELECT clock_timestamp()").Scan(&now).Error; err != nil {
			return err
		}
		if value.LeaseUntil != nil && value.LeaseUntil.After(now) {
			run = &value
			return nil
		}
		newToken := uuid.NewString()
		leaseUntilValue := now.Add(input.Lease)
		if err := tx.WithContext(ctx).Exec(`
			UPDATE embedding_reconciliation_runs
			SET status = 'running', worker_id = ?, lease_token = ?::uuid,
			    lease_until = ?, started_at = COALESCE(started_at, ?),
			    updated_at = clock_timestamp()
			WHERE reconciliation_run_id = ?::uuid
		`, input.WorkerID, newToken, leaseUntilValue, now, value.RunID).Error; err != nil {
			return err
		}
		value.Status = string(domain.EmbeddingReconciliationRunning)
		value.LeaseToken = newToken
		value.LeaseUntil = &leaseUntilValue
		if value.StartedAt == nil {
			value.StartedAt = &now
		}
		run, claimed = &value, true
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("search: reserve repair run: %w", err)
	}
	return run, claimed, nil
}

func (r *SearchRepositoryImpl) SelectSearchRepairDocuments(ctx context.Context, input SearchRepairSelectionInput) ([]SearchRepairDocument, bool, error) {
	input = normalizeSearchRepairSelectionInput(input)
	if err := validateSearchRepairSelectionInput(input); err != nil {
		return nil, false, err
	}
	limit := input.Limit
	if limit <= 0 {
		limit = searchRepairCandidateLimit
	}
	if limit > searchRepairCandidateLimit {
		return nil, false, fmt.Errorf("search: repair batch exceeds %d documents", searchRepairCandidateLimit)
	}
	items := make([]SearchRepairDocument, 0, limit+1)
	hasMore := false
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		contract := &ActiveSearchContract{EmbeddingContractID: input.EmbeddingContractID, EmbeddingDimensions: input.EmbeddingDimensions}
		cursor := searchRepairCursor{}
		pageSize := limit + 1
		if pageSize > searchRepairCandidateLimit {
			pageSize = searchRepairCandidateLimit
		}
		for len(items) < limit+1 {
			candidates, err := selectSearchRepairCandidatePage(ctx, tx, input, cursor, pageSize)
			if err != nil {
				return err
			}
			if len(candidates) == 0 {
				break
			}
			for _, candidate := range candidates {
				cursor = searchRepairCursorFrom(candidate)
				item, include, err := hydrateSearchRepairCandidate(ctx, tx, contract, candidate)
				if err != nil {
					return err
				}
				if !include {
					continue
				}
				items = append(items, item)
				if len(items) >= limit+1 {
					hasMore = true
					break
				}
			}
			if len(candidates) < pageSize {
				break
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, fmt.Errorf("search: select repair documents: %w", err)
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func hydrateSearchRepairCandidate(ctx context.Context, tx *gorm.DB, contract *ActiveSearchContract, candidate SearchRepairDocument) (SearchRepairDocument, bool, error) {
	if candidate.SearchDocumentID == "" {
		expected, known, err := canonicalSearchRepairDocument(ctx, tx, contract, candidate)
		if err != nil {
			return SearchRepairDocument{}, false, err
		}
		returnSearch := expected != nil && known
		if !returnSearch {
			return SearchRepairDocument{}, false, nil
		}
		return *expected, true, nil
	}
	item, vectorCurrent, err := loadSearchRepairDocument(ctx, tx, candidate.TeamID, candidate.SearchDocumentID)
	if errors.Is(err, sql.ErrNoRows) {
		return SearchRepairDocument{}, false, nil
	}
	if err != nil {
		return SearchRepairDocument{}, false, err
	}
	item.Retired = candidate.Retired
	item.StoredDocumentHash = item.DocumentHash
	item.StoredOwnerProfileID = item.OwnerProfileID
	if candidate.OwnerProfileID != "" {
		item.OwnerProfileID = candidate.OwnerProfileID
	}
	if candidate.Retired {
		expected, known, err := canonicalSearchRepairDocument(ctx, tx, contract, item)
		if err != nil {
			return SearchRepairDocument{}, false, err
		}
		return item, known && expected == nil && item.SearchState != "not_required", nil
	}
	expected, known, err := canonicalSearchRepairDocument(ctx, tx, contract, item)
	if err != nil {
		return SearchRepairDocument{}, false, err
	}
	if known {
		ownerCurrent := item.StoredOwnerProfileID
		if ownerCurrent == "" {
			ownerCurrent = item.OwnerProfileID
		}
		if expected == nil || (ownerCurrent == expected.OwnerProfileID && searchRepairDocumentMatches(item, *expected) && vectorCurrent) {
			return SearchRepairDocument{}, false, nil
		}
		expected.SearchDocumentID = item.SearchDocumentID
		expected.DocumentVersion = item.DocumentVersion
		expected.StoredDocumentHash = item.StoredDocumentHash
		return *expected, true, nil
	}
	return item, !vectorCurrent, nil
}

func (r *SearchRepositoryImpl) ApplySearchRepair(ctx context.Context, input ApplySearchRepairInput) (*SearchRepairApplyResult, error) {
	input = normalizeApplySearchRepairInput(input)
	if err := validateApplySearchRepairInput(input); err != nil {
		return nil, err
	}
	result := &SearchRepairApplyResult{}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		runOwned, err := lockSearchRepairRun(ctx, tx, input.RunID, input.LeaseToken)
		if err != nil {
			return err
		}
		if !runOwned {
			return gorm.ErrRecordNotFound
		}
		active, err := loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		if active.EmbeddingContractID != input.EmbeddingContractID ||
			active.EmbeddingDimensions != input.EmbeddingDimensions ||
			active.SearchIndexGenerationID != input.SearchIndexGenerationID ||
			active.IndexGeneration != input.IndexGeneration {
			return ErrSearchContractMismatch
		}
		if err := lockSearchRepairContractActivation(ctx, tx, active); err != nil {
			return err
		}
		active, err = loadActiveSearchContractInTx(ctx, tx)
		if err != nil {
			return err
		}
		if active.EmbeddingContractID != input.EmbeddingContractID ||
			active.EmbeddingDimensions != input.EmbeddingDimensions ||
			active.SearchIndexGenerationID != input.SearchIndexGenerationID ||
			active.IndexGeneration != input.IndexGeneration {
			return ErrSearchContractMismatch
		}
		leaseActive, err := searchRepairRunLeaseActive(ctx, tx, input.RunID, input.LeaseToken)
		if err != nil {
			return err
		}
		if !leaseActive {
			return gorm.ErrRecordNotFound
		}
		for _, item := range input.Documents {
			updated, skipped, err := applySearchRepairDocument(ctx, tx, active, item)
			if err != nil {
				return err
			}
			leaseActive, err := searchRepairRunLeaseActive(ctx, tx, input.RunID, input.LeaseToken)
			if err != nil {
				return err
			}
			if !leaseActive {
				return gorm.ErrRecordNotFound
			}
			if updated {
				result.UpdatedCount++
			}
			if skipped {
				result.SkippedCount++
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search: apply repair: %w", err)
	}
	remaining, _, err := r.SelectSearchRepairDocuments(ctx, SearchRepairSelectionInput{
		EmbeddingContractID: input.EmbeddingContractID, EmbeddingDimensions: input.EmbeddingDimensions, Limit: 1,
	})
	if err != nil {
		return nil, err
	}
	result.RemainingDrifted = len(remaining) > 0
	return result, nil
}

func lockSearchRepairRun(ctx context.Context, tx *gorm.DB, runID, leaseToken string) (bool, error) {
	var lockedRunID string
	err := tx.WithContext(ctx).Raw(`
		SELECT reconciliation_run_id::text
		FROM embedding_reconciliation_runs
		WHERE reconciliation_run_id = ?::uuid
		  AND status = 'running'
		  AND lease_token = ?::uuid
		  AND lease_until > clock_timestamp()
		FOR UPDATE
	`, runID, leaseToken).Row().Scan(&lockedRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return lockedRunID != "", nil
}

func searchRepairRunLeaseActive(ctx context.Context, tx *gorm.DB, runID, leaseToken string) (bool, error) {
	var active bool
	err := tx.WithContext(ctx).Raw(`
		SELECT EXISTS (
			SELECT 1
			FROM embedding_reconciliation_runs
			WHERE reconciliation_run_id = ?::uuid
			  AND status = 'running'
			  AND lease_token = ?::uuid
			  AND lease_until > clock_timestamp()
		)
	`, runID, leaseToken).Scan(&active).Error
	return active, err
}

func (r *SearchRepositoryImpl) FinishSearchRepairRun(ctx context.Context, input FinishSearchRepairRunInput) error {
	input = normalizeFinishSearchRepairRunInput(input)
	if err := validateFinishSearchRepairRunInput(input); err != nil {
		return err
	}
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		query := `
			UPDATE embedding_reconciliation_runs
			SET status = ?, selected_count = ?, embedded_count = ?, updated_count = ?,
			    drifted_count = ?, last_error = ?, completed_at = clock_timestamp(),
			    lease_until = NULL, updated_at = clock_timestamp()
			WHERE reconciliation_run_id = ?::uuid
			  AND status = 'running'
			  AND lease_until > clock_timestamp()`
		args := []any{input.Status, input.SelectedCount, input.EmbeddedCount, input.UpdatedCount, input.DriftedCount, input.LastError, input.RunID}
		if input.LeaseToken != "" {
			query += " AND lease_token = ?::uuid"
			args = append(args, input.LeaseToken)
		}
		result := tx.WithContext(ctx).Exec(query, args...)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("search: finish repair run: %w", err)
	}
	return nil
}

func applySearchRepairDocument(ctx context.Context, tx *gorm.DB, contract *ActiveSearchContract, item SearchRepairEmbedding) (bool, bool, error) {
	if item.Retired {
		return retireSearchRepairDocument(ctx, tx, contract, item.SearchRepairDocument)
	}
	expected, known, err := canonicalSearchRepairDocumentWithSourceLock(ctx, tx, contract, item.SearchRepairDocument, true)
	if err != nil {
		return false, false, err
	}
	if known && (expected == nil || !searchRepairDocumentMatches(*expected, item.SearchRepairDocument)) {
		return false, true, nil
	}
	if !known {
		expected = &item.SearchRepairDocument
	}
	if item.SearchDocumentID == "" {
		return insertSearchRepairDocument(ctx, tx, contract, item, *expected)
	}
	return updateSearchRepairDocument(ctx, tx, contract, item, *expected)
}

func canonicalSearchRepairDocument(ctx context.Context, tx *gorm.DB, contract *ActiveSearchContract, snapshot SearchRepairDocument) (*SearchRepairDocument, bool, error) {
	return canonicalSearchRepairDocumentWithSourceLock(ctx, tx, contract, snapshot, false)
}

func canonicalSearchRepairDocumentWithSourceLock(
	ctx context.Context,
	tx *gorm.DB,
	contract *ActiveSearchContract,
	snapshot SearchRepairDocument,
	lockSource bool,
) (*SearchRepairDocument, bool, error) {
	teamActive, err := lockSearchRepairActiveTeam(ctx, tx, snapshot.TeamID)
	if err != nil {
		return nil, true, err
	}
	if !teamActive {
		return nil, true, nil
	}
	switch snapshot.SourceKind {
	case "evidence":
		if lockSource {
			if err := lockSearchRepairEvidenceSource(ctx, tx, snapshot); err != nil {
				return nil, true, err
			}
		}
		var content, spaceID string
		var spaceGeneration int64
		err := tx.WithContext(ctx).Raw(`
			SELECT fragment.content, COALESCE(fragment.space_id::text, ''), COALESCE(fragment.space_generation, 0)
				FROM evidence_fragments AS fragment
`+searchRepairEvidenceTerminalPlacementJoinSQL+`
				WHERE fragment.team_id = ?::uuid
				  AND fragment.owner_profile_id = ?::uuid
				  AND fragment.fragment_id = ?::uuid
				  AND fragment.space_generation = dense_mem_active_space_generation(fragment.team_id, fragment.space_id)
				  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
				  AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
			  AND NOT EXISTS (SELECT 1 FROM evidence_quarantines q WHERE q.team_id = fragment.team_id AND q.fragment_id = fragment.fragment_id AND q.status = 'active')
			  AND NOT EXISTS (SELECT 1 FROM evidence_lifecycle_events e WHERE e.team_id = fragment.team_id AND e.target_fragment_id = fragment.fragment_id)
		`, snapshot.TeamID, snapshot.OwnerProfileID, snapshot.SourceID).Row().Scan(&content, &spaceID, &spaceGeneration)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, true, nil
		}
		if err != nil {
			return nil, true, err
		}
		expected := snapshot
		expected.SourceVersion = 1
		expected.ProjectionFormat = 1
		expected.ProjectionGenerationID = ""
		expected.DocumentText = strings.TrimSpace(content)
		expected.DocumentHash = searchRepairHash(expected.DocumentText)
		expected.SpaceID, expected.SpaceGeneration = strings.TrimSpace(spaceID), spaceGeneration
		expected.EmbeddingContractID, expected.EmbeddingDimensions = contract.EmbeddingContractID, contract.EmbeddingDimensions
		return &expected, true, nil
	case "relationship":
		var relationship *RelationshipRecord
		if lockSource {
			relationship, err = loadRelationshipRecordForUpdate(ctx, tx, snapshot.TeamID, snapshot.SourceID)
		} else {
			relationship, err = loadRelationshipRecord(ctx, tx, snapshot.TeamID, snapshot.SourceID)
		}
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, true, nil
		}
		if err != nil {
			return nil, true, err
		}
		if !relationshipSearchEligible(relationship) {
			return nil, true, nil
		}
		active, err := searchRepairSpaceIsActive(ctx, tx, relationship.TeamID, relationship.SpaceID, relationship.SpaceGeneration)
		if err != nil {
			return nil, true, err
		}
		if !active {
			return nil, true, nil
		}
		text, err := placementRelationshipSearchText(ctx, tx, relationship)
		if err != nil {
			return nil, true, err
		}
		generationID, err := relationshipForegroundRecallGenerationID(ctx, tx, snapshot.TeamID)
		if err != nil {
			return nil, true, err
		}
		expected := snapshot
		expected.SourceVersion = int64(relationship.Version)
		expected.ProjectionFormat = 2
		expected.ProjectionGenerationID = generationID
		expected.DocumentText = strings.TrimSpace(text)
		expected.DocumentHash = searchRepairHash(expected.DocumentText)
		expected.SpaceID, expected.SpaceGeneration = relationship.SpaceID, relationship.SpaceGeneration
		expected.EmbeddingContractID, expected.EmbeddingDimensions = contract.EmbeddingContractID, contract.EmbeddingDimensions
		return &expected, true, nil
	default:
		return &snapshot, false, nil
	}
}

func lockSearchRepairEvidenceSource(ctx context.Context, tx *gorm.DB, snapshot SearchRepairDocument) error {
	var sourceID sql.NullString
	err := tx.WithContext(ctx).Raw(`
		SELECT source_id::text
		FROM evidence_fragments
		WHERE team_id = ?::uuid AND fragment_id = ?::uuid
	`, snapshot.TeamID, snapshot.SourceID).Row().Scan(&sourceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if sourceID.Valid && sourceID.String != "" {
		var lockedSourceID string
		err = tx.WithContext(ctx).Raw(`
			SELECT source_id::text
			FROM evidence_sources
			WHERE team_id = ?::uuid AND source_id = ?::uuid
			FOR UPDATE
		`, snapshot.TeamID, sourceID.String).Row().Scan(&lockedSourceID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
	}
	var lockedFragmentID string
	err = tx.WithContext(ctx).Raw(`
		SELECT fragment_id::text
		FROM evidence_fragments
		WHERE team_id = ?::uuid AND fragment_id = ?::uuid
		FOR UPDATE
	`, snapshot.TeamID, snapshot.SourceID).Row().Scan(&lockedFragmentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}

func lockSearchRepairActiveTeam(ctx context.Context, tx *gorm.DB, teamID string) (bool, error) {
	// Row-level locks evaluate the teams UPDATE policy, so scope the lock to the
	// target team's self-access policy without changing the surrounding system read context.
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.current_team_id', ?, true)", teamID).Error; err != nil {
		return false, err
	}
	var lockedTeamID string
	lockErr := tx.WithContext(ctx).Raw(`
		SELECT id::text
		FROM teams
		WHERE id = ?::uuid AND status = 'active' AND deleted_at IS NULL
		FOR SHARE
	`, teamID).Row().Scan(&lockedTeamID)
	resetErr := tx.WithContext(ctx).Exec("SELECT set_config('app.current_team_id', '', true)").Error
	if resetErr != nil {
		return false, resetErr
	}
	if errors.Is(lockErr, sql.ErrNoRows) {
		return false, nil
	}
	if lockErr != nil {
		return false, lockErr
	}
	return lockedTeamID != "", nil
}

func searchRepairSpaceIsActive(ctx context.Context, tx *gorm.DB, teamID, spaceID string, generation int64) (bool, error) {
	var active bool
	err := tx.WithContext(ctx).Raw(`
		SELECT ?::bigint = dense_mem_active_space_generation(?::uuid, ?::uuid)
	`, generation, teamID, spaceID).Scan(&active).Error
	return active, err
}

func searchRepairDocumentMatches(left, right SearchRepairDocument) bool {
	return left.TeamID == right.TeamID && left.OwnerProfileID == right.OwnerProfileID &&
		left.SourceKind == right.SourceKind && left.SourceID == right.SourceID &&
		left.SourceVersion == right.SourceVersion && left.ProjectionFormat == right.ProjectionFormat &&
		left.ProjectionGenerationID == right.ProjectionGenerationID && left.DocumentText == right.DocumentText &&
		left.DocumentHash == right.DocumentHash && left.SpaceID == right.SpaceID && left.SpaceGeneration == right.SpaceGeneration &&
		left.EmbeddingContractID == right.EmbeddingContractID && left.EmbeddingDimensions == right.EmbeddingDimensions
}

func searchRepairHash(text string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(sum[:])
}

func lockSearchRepairContractActivation(ctx context.Context, tx *gorm.DB, contract *ActiveSearchContract) error {
	if contract == nil || contract.EmbeddingContractID == "" {
		return errors.New("active search contract is required")
	}
	return tx.WithContext(ctx).Exec(
		`SELECT pg_advisory_xact_lock(hashtext(?))`,
		"search-index-generation:"+contract.EmbeddingContractID,
	).Error
}

func appendSearchRepairActiveContractFenceArgs(args []any, contract *ActiveSearchContract) []any {
	return append(args,
		contract.SearchIndexGenerationID,
		contract.EmbeddingContractID,
		contract.EmbeddingDimensions,
		contract.IndexGeneration,
	)
}

func appendSearchRepairRelationshipProjectionFenceArgs(args []any, teamID, projectionGenerationID string) []any {
	return append(args, projectionGenerationID, teamID, teamID)
}

func insertSearchRepairDocument(ctx context.Context, tx *gorm.DB, contract *ActiveSearchContract, item SearchRepairEmbedding, expected SearchRepairDocument) (bool, bool, error) {
	vector, err := vectorLiteral(item.Embedding)
	if err != nil {
		return false, false, err
	}
	query := `
		INSERT INTO search_documents (
			team_id, owner_profile_id, space_id, space_generation, source_kind, source_id,
			source_version, projection_format_version, projection_generation_id, document_version,
			embedding_contract_id, embedding_dimensions, search_state, document_text,
			document_hash, embedding, embedding_updated_at, embedding_error, metadata
		) SELECT
			?::uuid, ?::uuid, COALESCE(NULLIF(?, '')::uuid, dense_mem_team_shared_space(?::uuid)),
			NULLIF(?, 0)::bigint, ?, ?::uuid, ?, ?, NULLIF(?, '')::uuid, 1,
			?::uuid, ?, 'current', ?, ?, ?::vector, clock_timestamp(), '', '{}'::jsonb
		WHERE ` + searchRepairActiveContractFenceSQL + `
		  AND ?::bigint = dense_mem_active_space_generation(?::uuid, ?::uuid)`
	args := []any{
		expected.TeamID, expected.OwnerProfileID, expected.SpaceID, expected.TeamID, expected.SpaceGeneration,
		expected.SourceKind, expected.SourceID, expected.SourceVersion, expected.ProjectionFormat,
		expected.ProjectionGenerationID, contract.EmbeddingContractID, contract.EmbeddingDimensions,
		expected.DocumentText, expected.DocumentHash, vector,
	}
	args = appendSearchRepairActiveContractFenceArgs(args, contract)
	args = append(args, expected.SpaceGeneration, expected.TeamID, expected.SpaceID)
	if expected.SourceKind == "relationship" {
		query += "\n  AND " + searchRepairRelationshipProjectionFenceSQL
		args = appendSearchRepairRelationshipProjectionFenceArgs(args, expected.TeamID, expected.ProjectionGenerationID)
	}
	query += `
		ON CONFLICT (team_id, source_kind, source_id, embedding_contract_id) DO NOTHING
	`
	result := tx.WithContext(ctx).Exec(query, args...)
	if result.Error != nil {
		return false, false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, true, nil
	}
	if expected.SourceKind == "relationship" && expected.ProjectionGenerationID != "" {
		if err := refreshRelationshipProjectionGeneration(ctx, tx, expected.TeamID, expected.ProjectionGenerationID); err != nil {
			return false, false, err
		}
	}
	return true, false, nil
}

func updateSearchRepairDocument(ctx context.Context, tx *gorm.DB, contract *ActiveSearchContract, item SearchRepairEmbedding, expected SearchRepairDocument) (bool, bool, error) {
	current, err := loadSearchRepairDocumentForUpdate(ctx, tx, item.SearchRepairDocument)
	if errors.Is(err, sql.ErrNoRows) {
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	if current.DocumentVersion != item.DocumentVersion || current.DocumentHash != item.StoredDocumentHash ||
		current.EmbeddingContractID != contract.EmbeddingContractID || current.EmbeddingDimensions != contract.EmbeddingDimensions {
		return false, true, nil
	}
	storedOwner := item.StoredOwnerProfileID
	if storedOwner == "" {
		storedOwner = item.OwnerProfileID
	}
	if current.OwnerProfileID != storedOwner {
		return false, true, nil
	}
	if current.SearchDocumentID == "" {
		return false, true, nil
	}
	canonicalSnapshot := current
	if item.OwnerProfileID != "" {
		canonicalSnapshot.OwnerProfileID = item.OwnerProfileID
	}
	revalidated, known, err := canonicalSearchRepairDocumentWithSourceLock(ctx, tx, contract, canonicalSnapshot, true)
	if err != nil {
		return false, false, err
	}
	if known {
		if revalidated == nil || !searchRepairDocumentMatches(*revalidated, item.SearchRepairDocument) {
			return false, true, nil
		}
		expected = *revalidated
	} else {
		expected = current
	}
	var state string
	var vectorCurrent bool
	if err := tx.WithContext(ctx).Raw(`
		SELECT search_state, embedding IS NOT NULL AND vector_dims(embedding) = embedding_dimensions
		FROM search_documents WHERE team_id = ?::uuid AND search_document_id = ?::uuid
	`, current.TeamID, current.SearchDocumentID).Row().Scan(&state, &vectorCurrent); err != nil {
		return false, false, err
	}
	if searchRepairDocumentMatches(current, expected) && state == string(domain.SearchProjectionCurrent) && vectorCurrent {
		return false, true, nil
	}
	vector, err := vectorLiteral(item.Embedding)
	if err != nil {
		return false, false, err
	}
	query := `
		UPDATE search_documents
		SET owner_profile_id = ?::uuid, source_version = ?, projection_format_version = ?,
		    projection_generation_id = NULLIF(?, '')::uuid, space_id = NULLIF(?, '')::uuid,
		    space_generation = NULLIF(?, 0)::bigint, document_text = ?, document_hash = ?,
		    document_version = document_version + 1, embedding = ?::vector, search_state = 'current',
		    embedding_updated_at = clock_timestamp(), embedding_error = '', updated_at = clock_timestamp()
		WHERE team_id = ?::uuid AND search_document_id = ?::uuid AND owner_profile_id = ?::uuid
		  AND document_version = ? AND document_hash = ? AND embedding_contract_id = ?::uuid
		  AND embedding_dimensions = ? AND source_kind = ? AND source_id = ?::uuid
		  AND source_version = ? AND projection_format_version = ?
		  AND projection_generation_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND space_id IS NOT DISTINCT FROM NULLIF(?, '')::uuid
		  AND space_generation IS NOT DISTINCT FROM NULLIF(?, 0)::bigint
		  AND document_text = ?
		  AND space_generation = dense_mem_active_space_generation(team_id, space_id)
		  AND ` + searchRepairActiveContractFenceSQL
	args := []any{
		expected.OwnerProfileID, expected.SourceVersion, expected.ProjectionFormat,
		expected.ProjectionGenerationID, expected.SpaceID, expected.SpaceGeneration,
		expected.DocumentText, expected.DocumentHash, vector,
		item.TeamID, item.SearchDocumentID, storedOwner, item.DocumentVersion,
		item.StoredDocumentHash, contract.EmbeddingContractID, contract.EmbeddingDimensions,
		item.SourceKind, item.SourceID, item.SourceVersion, item.ProjectionFormat,
		item.ProjectionGenerationID, item.SpaceID, item.SpaceGeneration, item.DocumentText,
	}
	args = appendSearchRepairActiveContractFenceArgs(args, contract)
	if expected.SourceKind == "relationship" {
		query += "\n  AND " + searchRepairRelationshipProjectionFenceSQL
		args = appendSearchRepairRelationshipProjectionFenceArgs(args, expected.TeamID, expected.ProjectionGenerationID)
	}
	result := tx.WithContext(ctx).Exec(query, args...)
	if result.Error != nil {
		return false, false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, true, nil
	}
	updated := searchRepairDocumentResult(current)
	updated.DocumentVersion++
	updated.OwnerProfileID = expected.OwnerProfileID
	updated.SourceVersion = expected.SourceVersion
	updated.ProjectionFormat = expected.ProjectionFormat
	updated.ProjectionGenerationID = expected.ProjectionGenerationID
	updated.SpaceID = expected.SpaceID
	updated.SpaceGeneration = expected.SpaceGeneration
	if err := retireSupersededEmbeddingJobs(ctx, tx, updated); err != nil {
		return false, false, err
	}
	if expected.SourceKind == "relationship" && expected.ProjectionGenerationID != "" {
		if err := refreshRelationshipProjectionGeneration(ctx, tx, expected.TeamID, expected.ProjectionGenerationID); err != nil {
			return false, false, err
		}
	}
	if current.SourceKind == "relationship" && current.ProjectionGenerationID != "" && current.ProjectionGenerationID != expected.ProjectionGenerationID {
		if err := refreshRelationshipProjectionGeneration(ctx, tx, current.TeamID, current.ProjectionGenerationID); err != nil {
			return false, false, err
		}
	}
	return true, false, nil
}

func retireSearchRepairDocument(ctx context.Context, tx *gorm.DB, contract *ActiveSearchContract, snapshot SearchRepairDocument) (bool, bool, error) {
	current, err := loadSearchRepairDocumentForUpdate(ctx, tx, snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return false, true, nil
	}
	if err != nil {
		return false, false, err
	}
	if current.DocumentVersion != snapshot.DocumentVersion || current.DocumentHash != snapshot.StoredDocumentHash {
		return false, true, nil
	}
	expected, known, err := canonicalSearchRepairDocumentWithSourceLock(ctx, tx, contract, current, true)
	if err != nil {
		return false, false, err
	}
	if !known || expected != nil {
		return false, true, nil
	}
	query := `
		UPDATE search_documents
		SET document_version = document_version + 1, search_state = 'not_required',
		    embedding = NULL, embedding_updated_at = NULL, embedding_error = '', updated_at = clock_timestamp()
		WHERE team_id = ?::uuid AND search_document_id = ?::uuid AND owner_profile_id = ?::uuid
		  AND document_version = ? AND document_hash = ? AND embedding_contract_id = ?::uuid
		  AND embedding_dimensions = ?
		  AND space_generation = dense_mem_active_space_generation(team_id, space_id)
		  AND ` + searchRepairActiveContractFenceSQL
	args := []any{
		current.TeamID, current.SearchDocumentID, current.OwnerProfileID, current.DocumentVersion,
		current.DocumentHash, contract.EmbeddingContractID, contract.EmbeddingDimensions,
	}
	args = appendSearchRepairActiveContractFenceArgs(args, contract)
	result := tx.WithContext(ctx).Exec(query, args...)
	if result.Error != nil {
		return false, false, result.Error
	}
	if result.RowsAffected != 1 {
		return false, true, nil
	}
	updated := searchRepairDocumentResult(current)
	updated.DocumentVersion++
	if err := retireSupersededEmbeddingJobs(ctx, tx, updated); err != nil {
		return false, false, err
	}
	if current.SourceKind == "relationship" && current.ProjectionGenerationID != "" {
		if err := refreshRelationshipProjectionGeneration(ctx, tx, current.TeamID, current.ProjectionGenerationID); err != nil {
			return false, false, err
		}
	}
	return true, false, nil
}

func loadSearchRepairDocumentForUpdate(ctx context.Context, tx *gorm.DB, snapshot SearchRepairDocument) (SearchRepairDocument, error) {
	var current SearchRepairDocument
	err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, search_document_id::text, owner_profile_id::text,
		       source_kind, source_id::text, source_version, projection_format_version,
		       COALESCE(projection_generation_id::text, ''), document_version,
		       embedding_contract_id::text, embedding_dimensions, search_state, COALESCE(space_id::text, ''),
		       COALESCE(space_generation, 0), document_text, document_hash, document_hash, false
		FROM search_documents
		WHERE team_id = ?::uuid AND search_document_id = ?::uuid
		FOR UPDATE
	`, snapshot.TeamID, snapshot.SearchDocumentID).Row().Scan(
		&current.TeamID, &current.SearchDocumentID, &current.OwnerProfileID,
		&current.SourceKind, &current.SourceID, &current.SourceVersion,
		&current.ProjectionFormat, &current.ProjectionGenerationID, &current.DocumentVersion,
		&current.EmbeddingContractID, &current.EmbeddingDimensions, &current.SearchState, &current.SpaceID,
		&current.SpaceGeneration, &current.DocumentText, &current.DocumentHash,
		&current.StoredDocumentHash, &current.Retired,
	)
	current.StoredOwnerProfileID = current.OwnerProfileID
	return current, err
}

func loadSearchRepairDocument(ctx context.Context, tx *gorm.DB, teamID, documentID string) (SearchRepairDocument, bool, error) {
	var current SearchRepairDocument
	var vectorCurrent bool
	err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, search_document_id::text, owner_profile_id::text,
		       source_kind, source_id::text, source_version, projection_format_version,
		       COALESCE(projection_generation_id::text, ''), document_version,
		       embedding_contract_id::text, embedding_dimensions, search_state, COALESCE(space_id::text, ''),
		       COALESCE(space_generation, 0), document_text, document_hash, document_hash, false,
		       search_state = 'current' AND embedding IS NOT NULL AND vector_dims(embedding) = embedding_dimensions
		FROM search_documents
		WHERE team_id = ?::uuid AND search_document_id = ?::uuid
	`, teamID, documentID).Row().Scan(
		&current.TeamID, &current.SearchDocumentID, &current.OwnerProfileID,
		&current.SourceKind, &current.SourceID, &current.SourceVersion,
		&current.ProjectionFormat, &current.ProjectionGenerationID, &current.DocumentVersion,
		&current.EmbeddingContractID, &current.EmbeddingDimensions, &current.SearchState, &current.SpaceID,
		&current.SpaceGeneration, &current.DocumentText, &current.DocumentHash,
		&current.StoredDocumentHash, &current.Retired, &vectorCurrent,
	)
	current.StoredOwnerProfileID = current.OwnerProfileID
	return current, vectorCurrent, err
}

func searchRepairDocumentResult(document SearchRepairDocument) SearchDocumentResult {
	return SearchDocumentResult{
		TeamID: document.TeamID, SearchDocumentID: document.SearchDocumentID,
		OwnerProfileID: document.OwnerProfileID, SourceKind: document.SourceKind,
		SourceID: document.SourceID, SourceVersion: document.SourceVersion,
		ProjectionFormat: document.ProjectionFormat, ProjectionGenerationID: document.ProjectionGenerationID,
		DocumentVersion: document.DocumentVersion, EmbeddingContractID: document.EmbeddingContractID,
		EmbeddingDimensions: document.EmbeddingDimensions, SpaceID: document.SpaceID,
		SpaceGeneration: document.SpaceGeneration,
	}
}
