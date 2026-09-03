package repository

import "fmt"

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
