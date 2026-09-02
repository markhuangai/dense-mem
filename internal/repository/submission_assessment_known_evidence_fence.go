package repository

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

type submissionKnownEvidenceFenceRow struct {
	TeamID                  string
	EvidenceID              string
	FragmentID              string
	IngestID                string
	OwnerProfileID          string
	Content                 string
	ContentHash             string
	Authority               string
	SourceID                string
	SourceRevisionID        string
	CurrentSourceRevisionID string
	SpaceID                 string
	SpaceGeneration         int64
}

// reauthorizeSubmissionKnownEvidence locks every snapshot source row that can
// change eligibility and compares the complete request-time fence before any
// semantic decision is written.
func reauthorizeSubmissionKnownEvidence(
	ctx context.Context,
	tx *gorm.DB,
	input CommitSubmissionAssessmentInput,
) error {
	for _, expected := range input.KnownEvidenceSnapshot {
		if err := lockKnownEvidenceSource(ctx, tx, expected.TeamID, expected.SourceID, expected.OwnerProfileID); err != nil {
			if errors.Is(err, sql.ErrNoRows) || isPostgresLockNotAvailable(err) {
				return ErrSubmissionAssessmentKnownEvidenceStale
			}
			return err
		}
		if err := lockEvidenceLifecycleTarget(ctx, tx, expected.TeamID, expected.FragmentID); err != nil {
			return err
		}
		actual, err := loadSubmissionKnownEvidenceFenceRow(ctx, tx, expected)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrSubmissionAssessmentKnownEvidenceStale
			}
			return err
		}
		if !submissionKnownEvidenceFenceMatches(expected, actual) {
			return ErrSubmissionAssessmentKnownEvidenceStale
		}
	}
	return nil
}

// lockKnownEvidenceSource prevents a concurrent source revision update from
// changing current-source eligibility between the fence check and support
// insertion. NOWAIT fails closed instead of allowing cross-owner source locks
// to form a transaction wait cycle.
func lockKnownEvidenceSource(ctx context.Context, tx *gorm.DB, teamID, sourceID, ownerProfileID string) error {
	if strings.TrimSpace(sourceID) == "" {
		return nil
	}
	var lockedSourceID string
	return tx.WithContext(ctx).Raw(`
		SELECT source_id::text
		FROM evidence_sources
		WHERE team_id = ?::uuid
		  AND source_id = ?::uuid
		  AND owner_profile_id = ?::uuid
		FOR SHARE NOWAIT
	`, teamID, sourceID, ownerProfileID).Row().Scan(&lockedSourceID)
}

func isPostgresLockNotAvailable(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "55P03"
}

func loadSubmissionKnownEvidenceFenceRow(
	ctx context.Context,
	tx *gorm.DB,
	expected SubmissionAssessmentKnownEvidence,
) (submissionKnownEvidenceFenceRow, error) {
	var actual submissionKnownEvidenceFenceRow
	err := tx.WithContext(ctx).Raw(`
		SELECT fragment.team_id::text,
		       fragment.fragment_id::text,
		       fragment.fragment_id::text,
		       fragment.ingest_id::text,
		       fragment.owner_profile_id::text,
		       fragment.content,
		       fragment.content_hash,
		       fragment.authority,
		       COALESCE(fragment.source_id::text, ''),
		       COALESCE(fragment.source_revision_id::text, ''),
		       COALESCE(source.current_revision_id::text, ''),
		       fragment.space_id::text,
		       fragment.space_generation
		FROM evidence_fragments AS fragment
		JOIN knowledge_ingests AS ingest
		  ON ingest.team_id = fragment.team_id
		 AND ingest.ingest_id = fragment.ingest_id
		 AND ingest.owner_profile_id = fragment.owner_profile_id
		JOIN memory_spaces AS space
		  ON space.team_id = fragment.team_id
		 AND space.id = fragment.space_id
		LEFT JOIN evidence_sources AS source
		  ON source.team_id = fragment.team_id
		 AND source.source_id = fragment.source_id
		 AND source.owner_profile_id = fragment.owner_profile_id
		WHERE fragment.team_id = ?::uuid
		  AND fragment.fragment_id = ?::uuid
		  AND fragment.owner_profile_id = ?::uuid
		  AND ingest.status = 'completed'
		  AND space.lifecycle_state = 'active'
		  AND (space.kind = 'team_shared' OR dense_mem_space_allowed(space.id))
		  AND fragment.space_generation = dense_mem_active_space_generation(fragment.team_id, fragment.space_id)
		  AND NOT EXISTS (
		      SELECT 1
		      FROM evidence_quarantines AS quarantine
		      WHERE quarantine.team_id = fragment.team_id
		        AND quarantine.fragment_id = fragment.fragment_id
		        AND quarantine.status = 'active'
		  )
		  AND NOT EXISTS (
		      SELECT 1
		      FROM evidence_lifecycle_events AS lifecycle
		      WHERE lifecycle.team_id = fragment.team_id
		        AND lifecycle.target_fragment_id = fragment.fragment_id
		  )
		  AND NOT (
		      ingest.source_summary = 'overdue conflict deletion-only derivation'
		      AND ingest.metadata ->> 'conflict_resolution_deletion_only' = 'true'
		  )
		  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
	`, expected.TeamID, expected.FragmentID, expected.OwnerProfileID).Row().Scan(
		&actual.TeamID,
		&actual.EvidenceID,
		&actual.FragmentID,
		&actual.IngestID,
		&actual.OwnerProfileID,
		&actual.Content,
		&actual.ContentHash,
		&actual.Authority,
		&actual.SourceID,
		&actual.SourceRevisionID,
		&actual.CurrentSourceRevisionID,
		&actual.SpaceID,
		&actual.SpaceGeneration,
	)
	if err != nil {
		return submissionKnownEvidenceFenceRow{}, err
	}
	if actual.SourceID != "" {
		var sourceOwner, currentRevision string
		row := tx.WithContext(ctx).Raw(`
			SELECT owner_profile_id::text, COALESCE(current_revision_id::text, '')
			FROM evidence_sources
			WHERE team_id = ?::uuid
			  AND source_id = ?::uuid
			  AND owner_profile_id = ?::uuid
		`, actual.TeamID, actual.SourceID, actual.OwnerProfileID).Row()
		if err := row.Scan(&sourceOwner, &currentRevision); err != nil {
			return submissionKnownEvidenceFenceRow{}, err
		}
		actual.CurrentSourceRevisionID = strings.TrimSpace(currentRevision)
		if sourceOwner != actual.OwnerProfileID {
			return submissionKnownEvidenceFenceRow{}, sql.ErrNoRows
		}
	}
	return actual, nil
}

func lockEvidenceLifecycleTarget(ctx context.Context, tx *gorm.DB, teamID, fragmentID string) error {
	return tx.WithContext(ctx).Exec(
		"SELECT pg_advisory_xact_lock(hashtextextended(?::text, 0))",
		strings.Join([]string{teamID, "evidence-lifecycle-target", fragmentID}, ":"),
	).Error
}

func submissionKnownEvidenceFenceMatches(
	expected SubmissionAssessmentKnownEvidence,
	actual submissionKnownEvidenceFenceRow,
) bool {
	return expected.TeamID == actual.TeamID &&
		expected.EvidenceID == actual.EvidenceID &&
		expected.FragmentID == actual.FragmentID &&
		expected.IngestID == actual.IngestID &&
		expected.OwnerProfileID == actual.OwnerProfileID &&
		expected.Content == actual.Content &&
		expected.ContentHash == actual.ContentHash &&
		expected.Authority == actual.Authority &&
		expected.SourceID == actual.SourceID &&
		expected.SourceRevisionID == actual.SourceRevisionID &&
		expected.CurrentSourceRevisionID == actual.CurrentSourceRevisionID &&
		expected.SpaceID == actual.SpaceID &&
		expected.SpaceGeneration == actual.SpaceGeneration
}
