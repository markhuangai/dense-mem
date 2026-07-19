package repository

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

type V2AdvanceSourceRevisionInput struct {
	TeamID                        string
	OwnerProfileID                string
	SourceKey                     string
	SourceKind                    string
	Authority                     string
	RevisionToken                 string
	ExpectedPreviousRevisionToken string
	ContentHash                   string
	Envelope                      map[string]any
}

type V2SourceRevisionResult struct {
	TeamID           string
	SourceID         string
	SourceRevisionID string
	RevisionToken    string
}

func (r *V2LedgerRepositoryImpl) AdvanceSourceRevision(ctx context.Context, input V2AdvanceSourceRevisionInput) (*V2SourceRevisionResult, error) {
	input = normalizeV2AdvanceSourceRevisionInput(input)
	if err := validateV2AdvanceSourceRevisionInput(input); err != nil {
		return nil, err
	}
	var result *V2SourceRevisionResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := ensureV2SemanticRefs(ctx, tx, input.TeamID, input.OwnerProfileID); err != nil {
			return err
		}
		var err error
		result, err = advanceV2SourceRevisionInTx(ctx, tx, input, nil)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("v2 ledger: advance source revision: %w", err)
	}
	return result, nil
}

func normalizeV2AdvanceSourceRevisionInput(input V2AdvanceSourceRevisionInput) V2AdvanceSourceRevisionInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
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

func validateV2AdvanceSourceRevisionInput(input V2AdvanceSourceRevisionInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
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

func getOrCreateV2EvidenceSource(ctx context.Context, tx *gorm.DB, input V2AdvanceSourceRevisionInput) (string, string, string, error) {
	var sourceID string
	var currentRevisionID sql.NullString
	var currentToken string
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT source_id::text, current_revision_id::text, current_revision_token
		FROM evidence_sources
		WHERE team_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		  AND source_key = ?
		LIMIT 1
		FOR UPDATE
	`, input.TeamID, input.OwnerProfileID, input.SourceKey).Rows()
	if err != nil {
		return "", "", "", err
	}
	if rows.Next() {
		if err := rows.Scan(&sourceID, &currentRevisionID, &currentToken); err != nil {
			_ = rows.Close()
			return "", "", "", err
		}
		if err := rows.Close(); err != nil {
			return "", "", "", err
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
		return "", "", "", fmt.Errorf("%w: source does not exist", ErrV2SourceRevisionConflict)
	}
	metadata, err := marshalV2JSON(nil)
	if err != nil {
		return "", "", "", err
	}
	insertRows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_sources (
		    team_id, owner_profile_id, source_key, source_kind, authority, metadata
		) VALUES (
		    ?::uuid, ?::uuid, ?, ?, ?, ?::jsonb
		)
		RETURNING source_id::text
	`, input.TeamID, input.OwnerProfileID, input.SourceKey, input.SourceKind, input.Authority, string(metadata)).Rows()
	if err != nil {
		return "", "", "", translateV2SourceCreateError(err)
	}
	defer insertRows.Close()
	if !insertRows.Next() {
		if err := insertRows.Err(); err != nil {
			return "", "", "", translateV2SourceCreateError(err)
		}
		return "", "", "", sql.ErrNoRows
	}
	if err := insertRows.Scan(&sourceID); err != nil {
		return "", "", "", err
	}
	return sourceID, "", "", translateV2SourceCreateError(insertRows.Err())
}

func advanceV2SourceRevisionInTx(
	ctx context.Context,
	tx *gorm.DB,
	input V2AdvanceSourceRevisionInput,
	cache map[string]V2SourceRevisionResult,
) (*V2SourceRevisionResult, error) {
	input = normalizeV2AdvanceSourceRevisionInput(input)
	if err := validateV2AdvanceSourceRevisionInput(input); err != nil {
		return nil, err
	}
	cacheKey := input.SourceKey + "\x00" + input.RevisionToken
	if cache != nil {
		if cached, ok := cache[cacheKey]; ok {
			return &cached, nil
		}
	}
	sourceID, currentRevisionID, currentToken, err := getOrCreateV2EvidenceSource(ctx, tx, input)
	if err != nil {
		return nil, err
	}
	if currentToken == input.RevisionToken && currentRevisionID != "" {
		hash, err := selectV2SourceRevisionContentHash(ctx, tx, input.TeamID, currentRevisionID)
		if err != nil {
			return nil, err
		}
		if hash != input.ContentHash {
			return nil, fmt.Errorf("%w: source revision %q already recorded with a different content hash", ErrV2SourceRevisionConflict, input.RevisionToken)
		}
		result := V2SourceRevisionResult{
			TeamID:           input.TeamID,
			SourceID:         sourceID,
			SourceRevisionID: currentRevisionID,
			RevisionToken:    input.RevisionToken,
		}
		if cache != nil {
			cache[cacheKey] = result
		}
		return &result, nil
	}
	if currentToken != input.ExpectedPreviousRevisionToken {
		return nil, fmt.Errorf("%w: expected %q, got %q", ErrV2SourceRevisionConflict, input.ExpectedPreviousRevisionToken, currentToken)
	}
	revisionID, err := insertV2SourceRevision(ctx, tx, input, sourceID, currentRevisionID)
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
		return nil, fmt.Errorf("%w: source revision changed concurrently", ErrV2SourceRevisionConflict)
	}
	advanced := V2SourceRevisionResult{
		TeamID:           input.TeamID,
		SourceID:         sourceID,
		SourceRevisionID: revisionID,
		RevisionToken:    input.RevisionToken,
	}
	if cache != nil {
		cache[cacheKey] = advanced
	}
	return &advanced, nil
}

func selectV2SourceRevisionContentHash(ctx context.Context, tx *gorm.DB, teamID, revisionID string) (string, error) {
	row := tx.WithContext(ctx).Raw(`
		SELECT content_hash
		FROM evidence_source_revisions
		WHERE team_id = ?::uuid
		  AND source_revision_id = ?::uuid
		LIMIT 1
	`, teamID, revisionID).Row()
	var hash string
	if err := row.Scan(&hash); err != nil {
		return "", err
	}
	return hash, nil
}

func v2SourceKindForEvidence(sourceType string) string {
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

func insertV2SourceRevision(ctx context.Context, tx *gorm.DB, input V2AdvanceSourceRevisionInput, sourceID string, supersedesRevisionID string) (string, error) {
	envelope, err := marshalV2JSON(input.Envelope)
	if err != nil {
		return "", err
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO evidence_source_revisions (
		    team_id, source_id, owner_profile_id, revision_token,
		    expected_previous_revision_token, supersedes_revision_id, content_hash, envelope
		) VALUES (
		    ?::uuid, ?::uuid, ?::uuid, ?, ?, NULLIF(?, '')::uuid, ?, ?::jsonb
		)
		RETURNING source_revision_id::text
	`, input.TeamID, sourceID, input.OwnerProfileID, input.RevisionToken,
		input.ExpectedPreviousRevisionToken, supersedesRevisionID, input.ContentHash, string(envelope)).Rows()
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
