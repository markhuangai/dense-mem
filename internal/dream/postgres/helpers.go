package postgres

import (
	"context"
	"strconv"
	"strings"

	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type predicateDefinition = postgres.PredicateDefinition

func ensureActiveTeamForMutation(ctx context.Context, tx *gorm.DB, teamID string) error {
	return postgres.EnsureActiveTeamForMutation(ctx, tx, teamID)
}

func seedTeamPredicateDefinitions(ctx context.Context, tx *gorm.DB, teamID string) error {
	return postgres.SeedTeamPredicateDefinitions(ctx, tx, teamID)
}

func loadPredicateDefinition(ctx context.Context, tx *gorm.DB, teamID, predicateKey string, version int) (*predicateDefinition, error) {
	return postgres.LoadPredicateDefinition(ctx, tx, teamID, predicateKey, version)
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func evaluationCursorOffset(cursor string) int {
	offset, err := strconv.Atoi(strings.TrimSpace(cursor))
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}
