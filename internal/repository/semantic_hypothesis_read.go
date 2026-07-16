package repository

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type semanticHypothesisRow struct {
	HypothesisID   string
	OwnerProfileID string
	Text           string
	Status         string
	SourceRefs     []byte
	Metadata       []byte
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (r *SemanticRepositoryImpl) RecallHypotheses(ctx context.Context, teamID, query string, limit int) ([]*domain.Dream, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	pattern := ""
	if trimmed := strings.TrimSpace(query); trimmed != "" {
		pattern = "%" + trimmed + "%"
	}
	rows := []semanticHypothesisRow{}
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		return tx.Raw(`
SELECT hypothesis_id::text,
       owner_profile_id::text,
       text,
       status,
       source_refs,
       metadata,
       created_at,
       updated_at
FROM semantic_hypotheses
WHERE team_id = ?
  AND status IN ('proposed', 'reinforced')
  AND (? = '' OR text ILIKE ? OR metadata::text ILIKE ?)
ORDER BY CASE WHEN ? <> '' AND text ILIKE ? THEN 0 ELSE 1 END,
         updated_at DESC,
         hypothesis_id
LIMIT ?`,
			teamID,
			pattern, pattern, pattern,
			pattern, pattern,
			limit,
		).Scan(&rows).Error
	})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Dream, 0, len(rows))
	for _, row := range rows {
		out = append(out, semanticHypothesisRowDream(row))
	}
	return out, nil
}

func semanticHypothesisRowDream(row semanticHypothesisRow) *domain.Dream {
	metadata := map[string]any{}
	_ = json.Unmarshal(row.Metadata, &metadata)
	return &domain.Dream{
		DreamID:         row.HypothesisID,
		ProfileID:       row.OwnerProfileID,
		Hypothesis:      row.Text,
		WhatIf:          metadataString(metadata, "what_if"),
		PossibleOutcome: metadataString(metadata, "possible_outcome"),
		Rationale:       metadataString(metadata, "rationale"),
		Likelihood:      metadataFloat(metadata, "likelihood"),
		Confidence:      metadataFloat(metadata, "confidence"),
		Status:          domain.DreamStatus(row.Status),
		Cycle:           metadataString(metadata, "cycle"),
		CycleRunID:      metadataString(metadata, "cycle_run_id"),
		GeneratorModel:  metadataString(metadata, "generator_model"),
		SourceRefs:      hypothesisSourceRefs(row.SourceRefs),
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}

func hypothesisSourceRefs(raw []byte) []domain.DreamSourceRef {
	if len(raw) == 0 {
		return nil
	}
	refs := []domain.DreamSourceRef{}
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil
	}
	return refs
}

func metadataString(metadata map[string]any, key string) string {
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

func metadataFloat(metadata map[string]any, key string) float64 {
	switch value := metadata[key].(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int64:
		return float64(value)
	default:
		return 0
	}
}
