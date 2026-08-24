package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestSubmissionAssessmentQuarantineRetainsRawCopyUntilSystemPurge(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-quarantine-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-quarantine-owner")
	foreignID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-quarantine-foreign")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-quarantine")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-quarantine-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	revisionJSON := json.RawMessage(`{"request_id":"submission-assessment-quarantine-latest-revision"}`)
	_, _, err = repo.AppendSubmissionAssessmentRevision(ctx, AppendSubmissionAssessmentRevisionInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
			PlacementRunID: ingest.PlacementRunID, WorkerID: "submission-quarantine-worker", ExpectedAttempts: claimed.Attempts,
		},
		AssessmentID: assessment.AssessmentID, ProviderTurns: 2,
		InputTokens: 20, OutputTokens: 10, CandidateContextTokens: 5,
		NormalizedResponse: revisionJSON, ResponseHash: sha256Hex(string(revisionJSON)), ValidatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	securityQuarantine := submissionAssessmentSecurityQuarantine(ingest.Evidence[0].FragmentID)
	completed, err := repo.CompleteSubmissionAssessment(ctx, CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID, PlacementRunID: ingest.PlacementRunID,
			WorkerID: "submission-quarantine-worker", ExpectedAttempts: claimed.Attempts,
		},
		Status:              string(domain.SemanticReviewQuarantined),
		Category:            "quarantined",
		Payload:             map[string]any{"failure_stage": "deterministic_security_scan"},
		SecurityQuarantines: []SubmissionAssessmentSecurityQuarantineInput{securityQuarantine},
		RelationshipResults: []SubmissionRelationshipResultInput{{
			RelationshipRef: "quarantined-ref",
			Disposition:     "not_stored",
			Reason:          "security_quarantine",
		}},
	})
	require.NoError(t, err)
	require.Equal(t, string(domain.SemanticReviewQuarantined), completed.Status)
	status, err := repo.GetPlacementRun(ctx, GetPlacementRunInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
	})
	require.NoError(t, err)
	require.Len(t, status.RelationshipResults, 1)
	assert.Equal(t, "quarantined-ref", status.RelationshipResults[0].RelationshipRef)
	assert.Equal(t, "not_stored", status.RelationshipResults[0].Disposition)
	assert.Equal(t, "security_quarantine", status.RelationshipResults[0].Reason)
	assert.Empty(t, status.RelationshipResults[0].Splits)

	var payloadCount, tombstoneCount, sourceCount int64
	var payloadSHA string
	var rawEvidence, rawAssessment string
	var retentionSeconds float64
	var placementCompletedAt, placementExpiry time.Time
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT COUNT(*), COALESCE(MAX(payload_sha256), ''),
			       COALESCE(MAX(evidence::text), ''), COALESCE(MAX(assessor_response::text), ''),
			       COALESCE(EXTRACT(EPOCH FROM MAX(expires_at - quarantined_at)), 0)
			FROM submission_quarantine_payloads
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&payloadCount, &payloadSHA, &rawEvidence, &rawAssessment, &retentionSeconds); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM submission_quarantine_tombstones
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, ingest.IngestID).Scan(&tombstoneCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM evidence_fragments
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, ingest.IngestID).Scan(&sourceCount).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT completed_at, quarantine_expires_at
			FROM placement_runs
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&placementCompletedAt, &placementExpiry)
	}))
	assert.Equal(t, int64(1), payloadCount)
	assert.NotEmpty(t, payloadSHA)
	assert.Contains(t, rawEvidence, "Orion links Vega.")
	assert.Contains(t, rawAssessment, "submission-assessment-quarantine-latest-revision")
	assert.InDelta(t, float64((24 * time.Hour).Seconds()), retentionSeconds, 1)
	assert.WithinDuration(t, placementCompletedAt.Add(24*time.Hour), placementExpiry, time.Second)
	assert.Equal(t, int64(2), tombstoneCount)
	assert.Equal(t, int64(2), sourceCount)
	for _, expiryExpression := range []string{
		"completed_at + interval '25 hours'",
		"NULL",
	} {
		err := rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
			return tx.Exec(`
				UPDATE placement_runs
				SET quarantine_expires_at = `+expiryExpression+`
				WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
			`, teamID, ingest.PlacementRunID).Error
		})
		require.Error(t, err, "quarantined placement runs must reject expiry %s", expiryExpression)
	}

	for _, profileID := range []string{ownerID, foreignID} {
		var visible int64
		require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, profileID, func(tx *gorm.DB) error {
			return tx.Raw(`
				SELECT COUNT(*) FROM submission_quarantine_payloads
				WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
			`, teamID, ingest.PlacementRunID).Scan(&visible).Error
		}))
		assert.Zero(t, visible, "raw quarantine payload must remain system-only")
	}

	referenceTime := time.Now().UTC()
	purged, err := repo.PurgeExpiredSubmissionQuarantinePayloads(ctx, referenceTime.Add(23*time.Hour), 100)
	require.NoError(t, err)
	assert.Zero(t, purged)
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*) FROM submission_quarantine_payloads
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Scan(&payloadCount).Error
	}))
	assert.Equal(t, int64(1), payloadCount)

	purged, err = repo.PurgeExpiredSubmissionQuarantinePayloads(ctx, referenceTime.Add(25*time.Hour), 100)
	require.NoError(t, err)
	assert.Equal(t, 1, purged)
	require.NoError(t, rls.WithSystemTx(ctx, appDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT COUNT(*) FROM submission_quarantine_payloads
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Scan(&payloadCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM submission_quarantine_tombstones
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, ingest.IngestID).Scan(&tombstoneCount).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COUNT(*) FROM evidence_fragments
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, ingest.IngestID).Scan(&sourceCount).Error
	}))
	assert.Zero(t, payloadCount)
	assert.Equal(t, int64(2), tombstoneCount)
	assert.Equal(t, int64(2), sourceCount)
}

func submissionAssessmentSecurityQuarantine(fragmentID string) SubmissionAssessmentSecurityQuarantineInput {
	return SubmissionAssessmentSecurityQuarantineInput{
		FragmentID: fragmentID,
		SecurityEventDraft: SecurityEventDraft{
			EventKind: "deterministic_scan",
			Decision:  "quarantine",
			Reason:    "deterministic intake scan rejected evidence",
			Signals: []SecuritySignalInput{{
				Kind:      "instruction_override",
				Severity:  "critical",
				SpanStart: 0,
				SpanEnd:   6,
				Metadata:  map[string]any{"rule_id": "instruction_override"},
			}},
		},
	}
}
