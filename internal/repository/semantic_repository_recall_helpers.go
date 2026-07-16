package repository

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func scanSemanticRecallCandidates(rows *sql.Rows) ([]domain.SemanticRecallCandidate, error) {
	var candidates []domain.SemanticRecallCandidate
	for rows.Next() {
		var candidate domain.SemanticRecallCandidate
		var branch string
		var latestValidFrom, latestRecordedAt sql.NullTime
		var relationshipIDs, matchedEntityIDs pq.StringArray
		if err := rows.Scan(
			&candidate.EvidenceID,
			&branch,
			&candidate.Rank,
			&candidate.RawScore,
			&candidate.ExactMatch,
			&candidate.PreciseMatch,
			&candidate.PhraseMatch,
			&candidate.AllHardAnchorsMatched,
			&candidate.FactSupport,
			&candidate.IndependentSourceGroups,
			&latestValidFrom,
			&latestRecordedAt,
			&relationshipIDs,
			&matchedEntityIDs,
		); err != nil {
			return nil, err
		}
		candidate.Branch = domain.SemanticRecallBranch(branch)
		candidate.LatestValidFrom = sqlTimePtr(latestValidFrom)
		if latestRecordedAt.Valid {
			candidate.LatestRecordedAt = latestRecordedAt.Time.UTC()
		}
		candidate.RelationshipIDs = append([]string(nil), relationshipIDs...)
		candidate.MatchedEntityIDs = append([]string(nil), matchedEntityIDs...)
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func scanSemanticRecallEntitySeeds(rows *sql.Rows) ([]domain.SemanticRecallEntitySeed, error) {
	var seeds []domain.SemanticRecallEntitySeed
	for rows.Next() {
		var seed domain.SemanticRecallEntitySeed
		if err := rows.Scan(&seed.EntityID, &seed.Rank, &seed.Exact, &seed.HardAnchor, &seed.Explicit, &seed.Score); err != nil {
			return nil, err
		}
		seeds = append(seeds, seed)
	}
	return seeds, rows.Err()
}

func semanticRecallSeedArrays(seeds []domain.SemanticRecallEntitySeed) ([]string, []string, []string) {
	seedIDs := make([]string, 0, len(seeds))
	exactSeedIDs := make([]string, 0, len(seeds))
	hardSeedIDs := make([]string, 0, len(seeds))
	for _, seed := range seeds {
		id := strings.TrimSpace(seed.EntityID)
		if id == "" {
			continue
		}
		seedIDs = append(seedIDs, id)
		if seed.Exact || seed.Explicit {
			exactSeedIDs = append(exactSeedIDs, id)
		}
		if seed.HardAnchor {
			hardSeedIDs = append(hardSeedIDs, id)
		}
	}
	return uniqueRepositoryStrings(seedIDs), uniqueRepositoryStrings(exactSeedIDs), uniqueRepositoryStrings(hardSeedIDs)
}

func normalizeSemanticRecallScope(scope domain.SemanticRecallSearchScope) domain.SemanticRecallSearchScope {
	scope.TeamID = strings.TrimSpace(scope.TeamID)
	scope.Features.Query = strings.TrimSpace(scope.Features.Query)
	if scope.Features.ContentQuery == "" {
		scope.Features.ContentQuery = scope.Features.Query
	}
	if scope.Features.RelaxedQuery == "" {
		scope.Features.RelaxedQuery = scope.Features.Query
	}
	if scope.BranchLimit <= 0 {
		scope.BranchLimit = 60
	}
	if scope.BranchLimit > 200 {
		scope.BranchLimit = 200
	}
	if scope.ValidAt.IsZero() {
		scope.ValidAt = time.Now().UTC()
	}
	if scope.KnownAt.IsZero() {
		scope.KnownAt = time.Now().UTC()
	}
	if scope.KnownEvidenceIDs == nil {
		scope.KnownEvidenceIDs = []string{}
	}
	if scope.KnownRelationshipIDs == nil {
		scope.KnownRelationshipIDs = []string{}
	}
	if scope.ExpandFromEntityIDs == nil {
		scope.ExpandFromEntityIDs = []string{}
	}
	return scope
}

func setSemanticVectorSearchSettings(ctx context.Context, tx *gorm.DB) error {
	for _, setting := range []string{
		"SET LOCAL hnsw.ef_search = 100",
		"SET LOCAL hnsw.iterative_scan = strict_order",
		"SET LOCAL hnsw.max_scan_tuples = 20000",
	} {
		if err := tx.WithContext(ctx).Exec(setting).Error; err != nil {
			return err
		}
	}
	return nil
}

func lowerStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func uniqueRepositoryStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}
