package dreamservice

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
)

func (s *service) materializeSeedDreams(ctx context.Context, profileID, runID string, seeds []SeedDream, maxOutputs int) (int, error) {
	if len(seeds) == 0 {
		return 0, nil
	}
	now := s.now().UTC()
	created := 0
	materialized := 0
	for _, seed := range seeds {
		dream := &domain.Dream{
			DreamID:         uuid.NewString(),
			ProfileID:       profileID,
			Hypothesis:      strings.TrimSpace(seed.Hypothesis),
			WhatIf:          strings.TrimSpace(seed.WhatIf),
			PossibleOutcome: strings.TrimSpace(seed.PossibleOutcome),
			Rationale:       strings.TrimSpace(seed.Rationale),
			Likelihood:      clamp01(seed.Likelihood),
			Confidence:      clamp01(seed.Confidence),
			Status:          domain.DreamStatusProposed,
			Cycle:           CycleDream,
			CycleRunID:      runID,
			GeneratorModel:  "dense-mem.eval-seeded-dream",
			SourceRefs:      seed.SourceRefs,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if dream.Hypothesis == "" || len(dream.SourceRefs) == 0 {
			continue
		}
		if maxOutputs > 0 && materialized >= maxOutputs {
			break
		}
		dream.ContentHash = dreamContentHash(dream)
		inserted, err := s.upsertDream(ctx, profileID, dream)
		if err != nil {
			return created, err
		}
		materialized++
		if inserted {
			created++
		}
	}
	return created, nil
}
