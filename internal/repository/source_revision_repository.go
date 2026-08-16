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

type AdvanceSourceRevisionInput struct {
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

type SourceRevisionResult struct {
	TeamID                       string
	SourceID                     string
	SourceRevisionID             string
	RevisionToken                string
	SupersededSourceRevisionID   string
	SupersededSourceRevisionSeen bool
}

func (r *LedgerRepositoryImpl) AdvanceSourceRevision(ctx context.Context, input AdvanceSourceRevisionInput) (*SourceRevisionResult, error) {
	input = normalizeAdvanceSourceRevisionInput(input)
	if err := validateAdvanceSourceRevisionInput(input); err != nil {
		return nil, err
	}
	var result *SourceRevisionResult
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
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
		return "", "", "", fmt.Errorf("%w: source does not exist", ErrSourceRevisionConflict)
	}
	metadata, err := marshalJSON(nil)
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
		hash, err := selectSourceRevisionContentHash(ctx, tx, input.TeamID, currentRevisionID)
		if err != nil {
			return nil, err
		}
		if hash != input.ContentHash {
			return nil, fmt.Errorf("%w: source revision %q already recorded with a different content hash", ErrSourceRevisionConflict, input.RevisionToken)
		}
		supersededRevisionID, err := selectSourceRevisionSupersededRevisionID(ctx, tx, input.TeamID, currentRevisionID)
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
	supportsToRevoke, err := loadEffectiveSourceRevisionSupports(ctx, tx, input.TeamID, sourceID, currentRevisionID)
	if err != nil {
		return nil, err
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
	if err := invalidateSourceRevisionSupports(ctx, tx, input, sourceID, currentRevisionID, revisionID, supportsToRevoke); err != nil {
		return nil, err
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

type sourceRevisionSupportInvalidation struct {
	SupportID      string
	RelationshipID string
	OwnerProfileID string
}

func loadEffectiveSourceRevisionSupports(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	sourceID string,
	sourceRevisionID string,
) ([]sourceRevisionSupportInvalidation, error) {
	if sourceRevisionID == "" {
		return nil, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		WITH latest AS (
			SELECT DISTINCT ON (support_id)
			       support_id,
			       decision
			FROM relationship_support_decision_events
			WHERE team_id = ?::uuid
			ORDER BY support_id, created_at DESC, support_decision_id DESC
		)
		SELECT support.support_id::text,
		       support.relationship_id::text,
		       support.owner_profile_id::text
		FROM relationship_evidence_supports AS support
		JOIN latest
		  ON latest.support_id = support.support_id
		 AND latest.decision IN ('grant', 'reinstate')
		LEFT JOIN evidence_quarantines AS quarantine
		  ON quarantine.team_id = support.team_id
		 AND quarantine.fragment_id = support.fragment_id
		 AND quarantine.status = 'active'
		WHERE support.team_id = ?::uuid
		  AND support.source_id = ?::uuid
		  AND support.source_revision_id = ?::uuid
		  AND quarantine.quarantine_id IS NULL
		ORDER BY support.relationship_id ASC, support.support_id ASC
	`, teamID, teamID, sourceID, sourceRevisionID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []sourceRevisionSupportInvalidation{}
	for rows.Next() {
		var item sourceRevisionSupportInvalidation
		if err := rows.Scan(&item.SupportID, &item.RelationshipID, &item.OwnerProfileID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func invalidateSourceRevisionSupports(
	ctx context.Context,
	tx *gorm.DB,
	input AdvanceSourceRevisionInput,
	sourceID string,
	supersededRevisionID string,
	supersedingRevisionID string,
	supports []sourceRevisionSupportInvalidation,
) error {
	if len(supports) == 0 {
		return nil
	}
	return withSystemModeInTx(ctx, tx, input.TeamID, input.OwnerProfileID, func(systemTx *gorm.DB) error {
		relationships := make(map[string]string)
		for _, support := range supports {
			decisionID, err := insertSupportDecisionEvent(ctx, systemTx, supportDecisionInput{
				TeamID:         input.TeamID,
				OwnerProfileID: support.OwnerProfileID,
				ActorProfileID: input.OwnerProfileID,
				SupportID:      support.SupportID,
				RelationshipID: support.RelationshipID,
				Decision:       string(domain.SupportRevoke),
				Reason:         "source_revision_superseded",
				IdempotencyKey: "source-revision:" + supersedingRevisionID + ":support:" + support.SupportID,
				Metadata: map[string]any{
					"source_id":                      sourceID,
					"source_owner_profile_id":        input.OwnerProfileID,
					"superseded_source_revision_id":  supersededRevisionID,
					"superseding_source_revision_id": supersedingRevisionID,
				},
			})
			if err != nil {
				return err
			}
			relationships[support.RelationshipID] = decisionID
		}
		for relationshipID, decisionID := range relationships {
			if _, err := recomputeRelationshipFromEffectiveSupport(
				ctx,
				systemTx,
				input.TeamID,
				relationshipID,
				decisionID,
				"source_revision_superseded",
			); err != nil {
				return err
			}
		}
		return nil
	})
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

func selectSourceRevisionContentHash(ctx context.Context, tx *gorm.DB, teamID, revisionID string) (string, error) {
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

func selectSourceRevisionSupersededRevisionID(ctx context.Context, tx *gorm.DB, teamID, revisionID string) (string, error) {
	var supersededRevisionID sql.NullString
	err := tx.WithContext(ctx).Raw(`
		SELECT supersedes_revision_id::text
		FROM evidence_source_revisions
		WHERE team_id = ?::uuid
		  AND source_revision_id = ?::uuid
		LIMIT 1
	`, teamID, revisionID).Row().Scan(&supersededRevisionID)
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
