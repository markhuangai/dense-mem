package postgres

import (
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func recallEventAt(validAt, knownAt *time.Time) *time.Time {
	if knownAt != nil {
		return knownAt
	}
	return validAt
}

func recallEvidenceAliasVisibilitySQL(fragmentAlias string) string {
	return fmt.Sprintf(`AND (
		NOT EXISTS (
		    SELECT 1 FROM evidence_exact_aliases AS alias
		    WHERE alias.team_id = %[1]s.team_id
		      AND alias.alias_fragment_id = %[1]s.fragment_id
		)
		OR EXISTS (
		    SELECT 1 FROM evidence_exact_aliases AS historical_alias
		    WHERE historical_alias.team_id = %[1]s.team_id
		      AND historical_alias.alias_fragment_id = %[1]s.fragment_id
		      AND historical_alias.created_at > COALESCE(?::timestamptz, 'infinity'::timestamptz)
		)
	)`, fragmentAlias)
}

func recallEvidenceHistoricalSourceVisibilitySQL(fragmentAlias, sourceAlias string) string {
	return fmt.Sprintf(`AND (
		%s.source_id IS NULL
		OR %s.current_revision_id = %s.source_revision_id
		OR EXISTS (
		    SELECT 1 FROM evidence_exact_aliases AS historical_alias
		    WHERE historical_alias.team_id = %s.team_id
		      AND historical_alias.alias_fragment_id = %s.fragment_id
		      AND historical_alias.created_at > COALESCE(?::timestamptz, 'infinity'::timestamptz)
		      AND NOT EXISTS (
		          SELECT 1
		          FROM evidence_source_revisions AS superseding_revision
		          WHERE superseding_revision.team_id = %s.team_id
		            AND superseding_revision.source_id = %s.source_id
		            AND superseding_revision.owner_profile_id = %s.owner_profile_id
		            AND superseding_revision.supersedes_revision_id = %s.source_revision_id
		            AND superseding_revision.created_at <= COALESCE(?::timestamptz, 'infinity'::timestamptz)
		      )
		)
	)`, fragmentAlias, sourceAlias, fragmentAlias, fragmentAlias, fragmentAlias,
		fragmentAlias, fragmentAlias, fragmentAlias, fragmentAlias)
}

func activeSemanticSpaceGenerationSQL(alias string) string {
	return storagepostgres.ActiveSemanticSpaceGenerationSQL(alias)
}

func normalizeRecallUUIDList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if _, err := uuid.Parse(value); err != nil {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// recallSpacePredicate is assembled from validated server-owned UUIDs and
// fixed space kinds so a read branch cannot become SQL input or an authority
// override.
func recallSpacePredicate(column, teamID, spaceID, spaceKind string) string {
	generationColumn := column
	if separator := strings.LastIndexByte(column, '.'); separator >= 0 {
		generationColumn = column[:separator] + ".space_generation"
	} else {
		generationColumn = "space_generation"
	}
	if strings.TrimSpace(spaceID) != "" {
		parsed, err := uuid.Parse(strings.TrimSpace(spaceID))
		if err == nil {
			literal := pq.QuoteLiteral(parsed.String())
			return fmt.Sprintf(
				" AND %s = %s::uuid AND %s = (SELECT generation FROM memory_spaces WHERE id = %s AND lifecycle_state = 'active')",
				column, literal, generationColumn, column,
			)
		}
		return " AND FALSE"
	}
	if strings.TrimSpace(spaceKind) == "" || strings.TrimSpace(spaceKind) == string(domain.MemorySpaceTeamShared) {
		parsed, err := uuid.Parse(strings.TrimSpace(teamID))
		if err == nil {
			literal := pq.QuoteLiteral(parsed.String())
			return fmt.Sprintf(
				" AND %s = dense_mem_team_shared_space(%s::uuid) AND %s = dense_mem_team_shared_generation(%s::uuid)",
				column, literal, generationColumn, literal,
			)
		}
	}
	return " AND FALSE"
}
