package repository

import (
	"context"
	"strings"

	"github.com/lib/pq"
	"gorm.io/gorm"
)

func loadTraceEvidenceForSupports(
	ctx context.Context,
	tx *gorm.DB,
	input TraceRelationshipInput,
	supports []RelationshipEvidenceSupportRecord,
) ([]TraceEvidenceFragment, error) {
	occurrenceIDs := traceSupportOccurrenceIDs(supports)
	if len(occurrenceIDs) == 0 {
		return loadTraceEvidenceFragments(ctx, tx, input, traceSupportFragmentIDs(supports))
	}
	return loadTraceEvidenceOccurrences(ctx, tx, input, occurrenceIDs)
}

func loadTraceEvidenceOccurrences(
	ctx context.Context,
	tx *gorm.DB,
	input TraceRelationshipInput,
	occurrenceIDs []string,
) ([]TraceEvidenceFragment, error) {
	includeContent := boolDefault(input.IncludeEvidenceContent, true)
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT COALESCE(alias.canonical_fragment_id, occurrence.canonical_fragment_id)::text,
		       occurrence.occurrence_id::text,
		       occurrence.ingest_id::text,
		       COALESCE(alias.canonical_owner_profile_id, occurrence.canonical_owner_profile_id)::text,
		       COALESCE(occurrence.source_id::text, ''), COALESCE(occurrence.source_revision_id::text, ''),
		       COALESCE(source.source_key, ''), COALESCE(source.source_kind, ''),
		       COALESCE(revision.revision_token, ''), COALESCE(source.current_revision_id::text, ''),
		       occurrence.evidence_index,
		       CASE WHEN ? THEN left(occurrence.content, ?) ELSE '' END,
		       occurrence.content_hash,
		       CASE WHEN ? THEN char_length(occurrence.content) > ? ELSE false END,
		       occurrence.source_type, occurrence.authority, occurrence.source_ref,
		       occurrence.labels, occurrence.metadata::text, occurrence.created_at
		FROM evidence_occurrences AS occurrence
		LEFT JOIN evidence_exact_aliases AS alias
		  ON alias.team_id = occurrence.team_id
		 AND alias.alias_fragment_id = occurrence.occurrence_id
		JOIN evidence_fragments AS canonical
		  ON canonical.team_id = occurrence.team_id
		 AND canonical.fragment_id = COALESCE(alias.canonical_fragment_id, occurrence.canonical_fragment_id)
		 AND canonical.owner_profile_id = COALESCE(alias.canonical_owner_profile_id, occurrence.canonical_owner_profile_id)
		LEFT JOIN evidence_sources AS source
		  ON source.team_id = occurrence.team_id
		 AND source.source_id = occurrence.source_id
		 AND source.owner_profile_id = occurrence.owner_profile_id
		 AND source.space_id = occurrence.space_id
		LEFT JOIN evidence_source_revisions AS revision
		  ON revision.team_id = occurrence.team_id
		 AND revision.source_revision_id = occurrence.source_revision_id
		 AND revision.owner_profile_id = occurrence.owner_profile_id
		 AND revision.space_id = occurrence.space_id
		WHERE occurrence.team_id = ?::uuid
		  AND occurrence.occurrence_id = ANY(?::uuid[])
		  AND occurrence.space_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("occurrence")+`
		  AND `+activeSemanticSpaceGenerationSQL("canonical")+`
		  AND (
		      source.source_id IS NULL
		      OR `+activeSemanticSpaceGenerationSQL("source")+`
		  )
		  AND (
		      revision.source_revision_id IS NULL
		      OR `+activeSemanticSpaceGenerationSQL("revision")+`
		  )
		ORDER BY occurrence.evidence_index ASC, occurrence.occurrence_id ASC
	`, includeContent, input.MaxFragmentContentRunes, includeContent, input.MaxFragmentContentRunes,
		input.TeamID, pq.Array(occurrenceIDs), input.spaceID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TraceEvidenceFragment
	for rows.Next() {
		var row TraceEvidenceFragment
		var metadataJSON string
		if err := rows.Scan(
			&row.FragmentID, &row.OccurrenceID, &row.IngestID, &row.OwnerProfileID,
			&row.SourceID, &row.SourceRevisionID, &row.SourceKey, &row.SourceKind,
			&row.RevisionToken, &row.CurrentRevisionID, &row.EvidenceIndex,
			&row.Content, &row.ContentHash, &row.ContentTruncated, &row.SourceType,
			&row.Authority, &row.SourceRef, pq.Array(&row.Labels), &metadataJSON,
			&row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.Metadata = jSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func traceSupportOccurrenceIDs(supports []RelationshipEvidenceSupportRecord) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(supports))
	for _, support := range supports {
		occurrenceID := strings.TrimSpace(support.OccurrenceID)
		if occurrenceID == "" {
			continue
		}
		if _, exists := seen[occurrenceID]; exists {
			continue
		}
		seen[occurrenceID] = struct{}{}
		out = append(out, occurrenceID)
	}
	return out
}
