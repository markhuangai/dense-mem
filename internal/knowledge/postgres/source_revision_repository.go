package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type evidenceSourceCurrentState struct {
	SourceID          string
	CurrentRevisionID string
	CurrentToken      string
	SpaceID           string
	SpaceGeneration   int64
}

func loadEvidenceSourceForUpdate(ctx context.Context, tx *gorm.DB, input AdvanceSourceRevisionInput) (*evidenceSourceCurrentState, error) {
	state := evidenceSourceCurrentState{}
	err := tx.WithContext(ctx).Raw(`
		SELECT source_id::text, COALESCE(current_revision_id::text, ''), current_revision_token,
		       space_id::text, space_generation
		FROM evidence_sources
		WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND source_key = ? AND space_id = ?::uuid
		LIMIT 1 FOR UPDATE
	`, input.TeamID, input.OwnerProfileID, input.SourceKey, input.SpaceID).Row().Scan(
		&state.SourceID, &state.CurrentRevisionID, &state.CurrentToken, &state.SpaceID, &state.SpaceGeneration,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &state, nil
}

// validateTerminalSourceRevisionInTx preserves source CAS fencing for a
// terminal Remember without activating a source revision or its supports.
func validateTerminalSourceRevisionInTx(ctx context.Context, tx *gorm.DB, input AdvanceSourceRevisionInput) error {
	input = normalizeAdvanceSourceRevisionInput(input)
	if err := validateAdvanceSourceRevisionInput(input); err != nil {
		return err
	}
	var err error
	input, err = resolveAdvanceSourceRevisionSpace(ctx, tx, input)
	if err != nil {
		return err
	}
	current, err := loadEvidenceSourceForUpdate(ctx, tx, input)
	if err != nil {
		return err
	}
	if current == nil {
		if input.ExpectedPreviousRevisionToken != "" {
			return fmt.Errorf("%w: source does not exist", ErrSourceRevisionConflict)
		}
		return nil
	}
	if current.SpaceID != input.SpaceID || current.SpaceGeneration != input.SpaceGeneration {
		return errors.New("evidence source memory space does not match ingest")
	}
	if current.CurrentToken == input.RevisionToken && current.CurrentRevisionID != "" {
		_, err := selectMatchingSourceRevisionSupersededRevisionID(ctx, tx, input, current.SourceID, current.CurrentRevisionID)
		return err
	}
	if current.CurrentToken != input.ExpectedPreviousRevisionToken {
		return fmt.Errorf("%w: expected %q, got %q", ErrSourceRevisionConflict, input.ExpectedPreviousRevisionToken, current.CurrentToken)
	}
	return nil
}

func (r *Store) AdvanceSourceRevision(ctx context.Context, input AdvanceSourceRevisionInput) (*SourceRevisionResult, error) {
	input = normalizeAdvanceSourceRevisionInput(input)
	if err := validateAdvanceSourceRevisionInput(input); err != nil {
		return nil, err
	}
	var result *SourceRevisionResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		var err error
		result, err = advanceSourceRevisionInTx(ctx, tx, input, nil)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: advance source revision: %w", err)
	}
	return result, nil
}

func normalizeAdvanceSourceRevisionInput(input AdvanceSourceRevisionInput) AdvanceSourceRevisionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.IngestID = strings.TrimSpace(input.IngestID)
	input.SpaceID = strings.TrimSpace(input.SpaceID)
	input.SourceKey = strings.TrimSpace(input.SourceKey)
	input.SourceKind = strings.TrimSpace(input.SourceKind)
	if input.SourceKind == "" {
		input.SourceKind = "conversation"
	}
	input.Authority = strings.TrimSpace(input.Authority)
	if input.Authority == "" {
		input.Authority = "primary"
	}
	input.RevisionToken = strings.TrimSpace(input.RevisionToken)
	input.ExpectedPreviousRevisionToken = strings.TrimSpace(input.ExpectedPreviousRevisionToken)
	input.ContentHash = strings.TrimSpace(input.ContentHash)
	return input
}

func validateAdvanceSourceRevisionInput(input AdvanceSourceRevisionInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if input.IngestID != "" {
		if _, err := uuid.Parse(input.IngestID); err != nil {
			return fmt.Errorf("ingest_id is invalid: %w", err)
		}
	}
	if input.SpaceID != "" {
		if _, err := uuid.Parse(input.SpaceID); err != nil {
			return fmt.Errorf("space_id is invalid: %w", err)
		}
		if input.SpaceGeneration <= 0 {
			return errors.New("space_generation must be positive when space_id is provided")
		}
	} else if input.SpaceGeneration != 0 {
		return errors.New("space_generation requires space_id")
	}
	if input.SourceKey == "" {
		return errors.New("source_key is required")
	}
	if input.RevisionToken == "" {
		return errors.New("revision_token is required")
	}
	if input.ContentHash == "" {
		return errors.New("content_hash is required")
	}
	if !domain.Authority(input.Authority).IsValid() {
		return fmt.Errorf("authority is unsupported: %q", input.Authority)
	}
	return nil
}

func getOrCreateEvidenceSource(ctx context.Context, tx *gorm.DB, input AdvanceSourceRevisionInput) (string, string, string, error) {
	var sourceID string
	var currentRevisionID sql.NullString
	var currentToken string
	var currentSpaceID string
	var currentSpaceGeneration int64
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT source_id::text, current_revision_id::text, current_revision_token,
		       space_id::text, space_generation
		FROM evidence_sources
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND source_key = ? AND space_id = ?::uuid
		LIMIT 1
		FOR UPDATE
	`, input.TeamID, input.OwnerProfileID, input.SourceKey, input.SpaceID).Rows()
	if err != nil {
		return "", "", "", err
	}
	if rows.Next() {
		if err := rows.Scan(&sourceID, &currentRevisionID, &currentToken, &currentSpaceID, &currentSpaceGeneration); err != nil {
			_ = rows.Close()
			return "", "", "", err
		}
		if err := rows.Close(); err != nil {
			return "", "", "", err
		}
		if currentSpaceID != input.SpaceID || currentSpaceGeneration != input.SpaceGeneration {
			return "", "", "", errors.New("evidence source memory space does not match ingest")
		}
		return sourceID, currentRevisionID.String, currentToken, nil
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", "", "", err
	}
	if err := rows.Close(); err != nil {
		return "", "", "", err
	}
	if input.ExpectedPreviousRevisionToken != "" {
		return "", "", "", fmt.Errorf("%w: source does not exist", ErrSourceRevisionConflict)
	}
	metadata, err := marshalJSON(nil)
	if err != nil {
		return "", "", "", err
	}
	insertRows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_sources (
		    team_id, owner_profile_id, source_key, source_kind, authority, metadata,
		    space_id, space_generation
		) VALUES (
		    ?::uuid, ?::uuid, ?, ?, ?, ?::jsonb, ?::uuid, ?::bigint
		)
		RETURNING source_id::text
	`, input.TeamID, input.OwnerProfileID, input.SourceKey, input.SourceKind, input.Authority, string(metadata), input.SpaceID, input.SpaceGeneration).Rows()
	if err != nil {
		return "", "", "", translateSourceCreateError(err)
	}
	defer insertRows.Close()
	if !insertRows.Next() {
		if err := insertRows.Err(); err != nil {
			return "", "", "", translateSourceCreateError(err)
		}
		return "", "", "", sql.ErrNoRows
	}
	if err := insertRows.Scan(&sourceID); err != nil {
		return "", "", "", err
	}
	return sourceID, "", "", translateSourceCreateError(insertRows.Err())
}

func resolveAdvanceSourceRevisionSpace(ctx context.Context, tx *gorm.DB, input AdvanceSourceRevisionInput) (AdvanceSourceRevisionInput, error) {
	if input.SpaceID != "" {
		return input, nil
	}
	if input.IngestID != "" {
		if err := tx.WithContext(ctx).Raw(`
			SELECT space_id::text, space_generation
			FROM knowledge_ingests
			WHERE team_id = ?::uuid
			  AND ingest_id = ?::uuid
			  AND owner_profile_id = ?::uuid
		`, input.TeamID, input.IngestID, input.OwnerProfileID).Row().Scan(&input.SpaceID, &input.SpaceGeneration); err != nil {
			return input, err
		}
		return input, nil
	}
	fence, err := loadTeamSharedSpaceFence(ctx, tx, input.TeamID)
	if err != nil {
		return input, err
	}
	input.SpaceID, input.SpaceGeneration = fence.ID, fence.Generation
	return input, nil
}

func advanceSourceRevisionInTx(
	ctx context.Context,
	tx *gorm.DB,
	input AdvanceSourceRevisionInput,
	cache map[string]SourceRevisionResult,
) (*SourceRevisionResult, error) {
	input = normalizeAdvanceSourceRevisionInput(input)
	if err := validateAdvanceSourceRevisionInput(input); err != nil {
		return nil, err
	}
	var err error
	input, err = resolveAdvanceSourceRevisionSpace(ctx, tx, input)
	if err != nil {
		return nil, err
	}
	cacheKey := input.SourceKey + "\x00" + input.RevisionToken
	if cache != nil {
		if cached, ok := cache[cacheKey]; ok {
			return &cached, nil
		}
	}
	sourceID, currentRevisionID, currentToken, err := getOrCreateEvidenceSource(ctx, tx, input)
	if err != nil {
		return nil, err
	}
	if currentToken == input.RevisionToken && currentRevisionID != "" {
		supersededRevisionID, err := selectMatchingSourceRevisionSupersededRevisionID(
			ctx, tx, input, sourceID, currentRevisionID,
		)
		if err != nil {
			return nil, err
		}
		result := SourceRevisionResult{
			TeamID:                       input.TeamID,
			SourceID:                     sourceID,
			SourceRevisionID:             currentRevisionID,
			RevisionToken:                input.RevisionToken,
			SupersededSourceRevisionID:   supersededRevisionID,
			SupersededSourceRevisionSeen: supersededRevisionID != "",
		}
		if cache != nil {
			cache[cacheKey] = result
		}
		return &result, nil
	}
	if currentToken != input.ExpectedPreviousRevisionToken {
		return nil, fmt.Errorf("%w: expected %q, got %q", ErrSourceRevisionConflict, input.ExpectedPreviousRevisionToken, currentToken)
	}
	revisionID, err := insertSourceRevision(ctx, tx, input, sourceID, currentRevisionID)
	if err != nil {
		return nil, err
	}
	result := tx.WithContext(ctx).Exec(`
		UPDATE evidence_sources
		SET current_revision_id = ?::uuid,
		    current_revision_token = ?,
		    updated_at = now()
		WHERE team_id = ?::uuid
		  AND source_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND current_revision_token = ?
	`, revisionID, input.RevisionToken, input.TeamID, sourceID, input.OwnerProfileID, currentToken)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, fmt.Errorf("%w: source revision changed concurrently", ErrSourceRevisionConflict)
	}
	advanced := SourceRevisionResult{
		TeamID:                       input.TeamID,
		SourceID:                     sourceID,
		SourceRevisionID:             revisionID,
		RevisionToken:                input.RevisionToken,
		SupersededSourceRevisionID:   currentRevisionID,
		SupersededSourceRevisionSeen: true,
	}
	if cache != nil {
		cache[cacheKey] = advanced
	}
	return &advanced, nil
}

func withSystemModeInTx(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	profileID string,
	fn func(systemTx *gorm.DB) error,
) error {
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.current_team_id', '', true)").Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.current_profile_id', '', true)").Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.tx_mode', 'system', true)").Error; err != nil {
		return err
	}
	fnErr := fn(tx)
	resetErr := resetProfileModeInTx(ctx, tx, teamID, profileID)
	if fnErr != nil {
		return fnErr
	}
	return resetErr
}

func resetProfileModeInTx(ctx context.Context, tx *gorm.DB, teamID, profileID string) error {
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.current_team_id', ?, true)", teamID).Error; err != nil {
		return err
	}
	if err := tx.WithContext(ctx).Exec("SELECT set_config('app.current_profile_id', ?, true)", profileID).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec("SELECT set_config('app.tx_mode', 'profile', true)").Error
}

func selectMatchingSourceRevisionSupersededRevisionID(
	ctx context.Context,
	tx *gorm.DB,
	input AdvanceSourceRevisionInput,
	sourceID string,
	revisionID string,
) (string, error) {
	envelope, err := marshalJSON(input.Envelope)
	if err != nil {
		return "", err
	}
	var supersededRevisionID sql.NullString
	err = tx.WithContext(ctx).Raw(`
		SELECT revision.supersedes_revision_id::text
		FROM evidence_source_revisions AS revision
		JOIN evidence_sources AS source
		  ON source.team_id = revision.team_id
		 AND source.source_id = revision.source_id
		 AND source.owner_profile_id = revision.owner_profile_id
		WHERE revision.team_id = ?::uuid
		  AND revision.source_id = ?::uuid
		  AND revision.source_revision_id = ?::uuid
		  AND revision.owner_profile_id = ?::uuid
		  AND revision.revision_token = ?
		  AND revision.expected_previous_revision_token = ?
		  AND revision.content_hash = ?
		  AND revision.envelope = ?::jsonb
		  AND source.source_key = ?
		  AND source.source_kind = ?
		  AND source.authority = ?
		LIMIT 1
	`, input.TeamID, sourceID, revisionID, input.OwnerProfileID,
		input.RevisionToken, input.ExpectedPreviousRevisionToken, input.ContentHash, string(envelope),
		input.SourceKey, input.SourceKind, input.Authority).Row().Scan(&supersededRevisionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf(
			"%w: source revision %q already recorded with different provenance",
			ErrSourceRevisionConflict,
			input.RevisionToken,
		)
	}
	if err != nil {
		return "", err
	}
	return supersededRevisionID.String, nil
}

func sourceKindForEvidence(sourceType string) string {
	switch strings.TrimSpace(sourceType) {
	case "document":
		return "document"
	case "manual":
		return "manual"
	case "observation":
		return "integration"
	default:
		return "conversation"
	}
}

func insertSourceRevision(ctx context.Context, tx *gorm.DB, input AdvanceSourceRevisionInput, sourceID string, supersedesRevisionID string) (string, error) {
	envelope, err := marshalJSON(input.Envelope)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_source_revisions (
		    team_id, source_id, owner_profile_id, revision_token,
		    expected_previous_revision_token, supersedes_revision_id, content_hash, envelope,
		    space_id, space_generation
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?, NULLIF(?, '')::uuid, ?, ?::jsonb,
		    ?::uuid, ?::bigint
		)
		RETURNING source_revision_id::text
	`, input.TeamID, sourceID, input.OwnerProfileID, input.RevisionToken,
		input.ExpectedPreviousRevisionToken, supersedesRevisionID, input.ContentHash, string(envelope),
		input.SpaceID, input.SpaceGeneration).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", sql.ErrNoRows
	}
	var revisionID string
	if err := rows.Scan(&revisionID); err != nil {
		return "", err
	}
	return revisionID, rows.Err()
}
