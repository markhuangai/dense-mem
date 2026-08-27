package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type passthroughRLS struct{}

func (passthroughRLS) WithTeamTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}
func (passthroughRLS) WithTeamProfileTx(_ context.Context, db *gorm.DB, _, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}
func (passthroughRLS) WithSystemTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}
func (passthroughRLS) WithTeamReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}
func (passthroughRLS) WithTeamProfileReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, _, _ string, fn func(*gorm.DB) error) error {
	return fn(db)
}
func (passthroughRLS) WithSystemReadOnlyRepeatableTx(_ context.Context, db *gorm.DB, fn func(*gorm.DB) error) error {
	return fn(db)
}

func newSynchronousRepositorySQLMock(t *testing.T) (*LedgerRepositoryImpl, sqlmock.Sqlmock, func()) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)
	repo := &LedgerRepositoryImpl{db: db, rls: passthroughRLS{}}
	return repo, mock, func() { _ = sqlDB.Close() }
}

func expectActiveTeam(mock sqlmock.Sqlmock, teamID string) {
	mock.ExpectQuery("SELECT id::text").WithArgs(teamID).WillReturnRows(
		sqlmock.NewRows([]string{"id"}).AddRow(teamID),
	)
}

func TestLoadRememberAttemptReturnsSafeReplayResult(t *testing.T) {
	repo, mock, cleanup := newSynchronousRepositorySQLMock(t)
	defer cleanup()
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	expectActiveTeam(mock, teamID)
	attemptID := uuid.NewString()
	mock.ExpectQuery("SELECT attempt_id::text").WithArgs(teamID, ownerID, "remember-key").WillReturnRows(
		sqlmock.NewRows([]string{"attempt_id", "request_hash", "contract_version", "outcome", "public_result"}).AddRow(
			attemptID, "hash-1", "dense-mem.v2.6.1", "completed", []byte(`{"submission_id":"`+attemptID+`","processing_state":"completed"}`),
		),
	)
	attempt, err := repo.LoadRememberAttempt(context.Background(), RememberAttemptLookupInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "remember-key",
	})
	require.NoError(t, err)
	require.Equal(t, attemptID, attempt.AttemptID)
	require.Equal(t, "hash-1", attempt.RequestHash)
	require.Equal(t, "dense-mem.v2.6.1", attempt.ContractVersion)
	require.Equal(t, "completed", attempt.Outcome)
	require.Equal(t, "completed", attempt.PublicResult["processing_state"])
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLoadRememberAttemptValidatesInputAndMissingRows(t *testing.T) {
	repo := &LedgerRepositoryImpl{}
	_, err := repo.LoadRememberAttempt(context.Background(), RememberAttemptLookupInput{TeamID: "bad", OwnerProfileID: uuid.NewString(), IdempotencyKey: "key"})
	require.Error(t, err)
	_, err = repo.LoadRememberAttempt(context.Background(), RememberAttemptLookupInput{TeamID: uuid.NewString(), OwnerProfileID: "bad", IdempotencyKey: "key"})
	require.Error(t, err)
	_, err = repo.LoadRememberAttempt(context.Background(), RememberAttemptLookupInput{TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString()})
	require.Error(t, err)

	validRepo, mock, cleanup := newSynchronousRepositorySQLMock(t)
	defer cleanup()
	teamID, ownerID := uuid.NewString(), uuid.NewString()
	expectActiveTeam(mock, teamID)
	mock.ExpectQuery("SELECT attempt_id::text").WithArgs(teamID, ownerID, "missing").WillReturnError(sql.ErrNoRows)
	_, err = validRepo.LoadRememberAttempt(context.Background(), RememberAttemptLookupInput{TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "missing"})
	require.ErrorIs(t, err, ErrRememberAttemptNotFound)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRememberTerminalRelationshipResultsFromProposalPreservesEveryRef(t *testing.T) {
	results, err := rememberTerminalRelationshipResultsFromProposal(map[string]any{
		"relationship_hints": []any{
			map[string]any{"ref": "first"},
			map[string]any{"ref": "second"},
		},
	}, "security_quarantine")
	require.NoError(t, err)
	require.Equal(t, []SubmissionRelationshipResultInput{
		{RelationshipRef: "first", Disposition: "not_stored", Reason: "security_quarantine"},
		{RelationshipRef: "second", Disposition: "not_stored", Reason: "security_quarantine"},
	}, results)
}

func TestRememberTerminalRelationshipResultsFromProposalRejectsDuplicateRefs(t *testing.T) {
	_, err := rememberTerminalRelationshipResultsFromProposal(map[string]any{
		"relationship_hints": []any{
			map[string]any{"ref": "duplicate"},
			map[string]any{"ref": "duplicate"},
		},
	}, "security_quarantine")
	require.ErrorContains(t, err, "duplicated")
}

func TestRememberPublicResultsHaveNoPollingOrDegradationFields(t *testing.T) {
	input := SynchronousRememberCommitInput{IngestID: uuid.NewString()}
	completed := rememberPublicResult(input, nil, &submissionSemanticCommitState{}, nil)
	terminal := rememberTerminalPublicResult(input, nil, "rejected", "no_supported_memory")
	for name, result := range map[string]map[string]any{"completed": completed, "terminal": terminal} {
		_, hasDegradations := result["degradations"]
		require.False(t, hasDegradations, name)
		_, hasPolling := result["check_after_seconds"]
		require.False(t, hasPolling, name)
	}
}

func TestRememberTerminalErrorGuidanceQuarantineRequiresNewKey(t *testing.T) {
	nextAction, remediation := rememberTerminalErrorGuidance("submission_quarantined")
	require.Equal(t, "resubmit_remember", nextAction)
	require.Equal(t, "Use a new idempotency_key for any later Remember submission.", remediation)
}

func TestRememberTerminalPublicResultUsesBoundedErrorMessages(t *testing.T) {
	input := SynchronousRememberCommitInput{IngestID: uuid.NewString()}
	for _, test := range []struct {
		outcome string
		code    string
		message string
	}{
		{outcome: "rejected", code: "no_supported_memory", message: "no supported memory could be stored from this submission"},
		{outcome: "quarantined", code: "submission_quarantined", message: "submission was quarantined by security policy"},
	} {
		result := rememberTerminalPublicResult(input, nil, test.outcome, test.code)
		errors := result["errors"].([]map[string]any)
		require.Len(t, errors, 1)
		require.Equal(t, test.code, errors[0]["code"])
		require.Equal(t, test.message, errors[0]["message"])
	}
}

func TestRememberTerminalPublicResultDoesNotReportUnappliedSupersessions(t *testing.T) {
	input := SynchronousRememberCommitInput{IngestID: uuid.NewString()}
	evidence := []EvidenceFragment{{EvidenceIndex: 0, SupersededEvidenceIDs: []string{uuid.NewString()}}}
	for _, outcome := range []string{"rejected", "quarantined"} {
		result := rememberTerminalPublicResult(input, evidence, outcome, "")
		items := result["evidence"].([]map[string]any)
		require.Len(t, items, 1)
		require.Empty(t, items[0]["superseded_evidence_ids"], outcome)
	}
}

func TestRememberPublicRelationshipResultsUseOnlyContractReference(t *testing.T) {
	input := SynchronousRememberCommitInput{
		IngestID: uuid.NewString(),
		Commit: CommitSubmissionAssessmentInput{RelationshipResults: []SubmissionRelationshipResultInput{{
			RelationshipRef: "r:one", Disposition: "not_stored", Reason: "not_supported_by_evidence",
		}}},
	}
	result := rememberTerminalPublicResult(input, nil, "rejected", "no_supported_memory")
	relationships := result["relationship_results"].([]map[string]any)
	require.Len(t, relationships, 1)
	_, hasLegacyReference := relationships[0]["relationship_ref"]
	require.False(t, hasLegacyReference)
	require.Equal(t, "r:one", relationships[0]["ref"])
}

func TestRememberAttemptDurationUsesRequestStart(t *testing.T) {
	started := time.Now().Add(-time.Second)
	duration := rememberAttemptDuration(SynchronousRememberCommitInput{
		StartedAt: started,
		Duration:  time.Millisecond,
	})
	require.Greater(t, duration, 500*time.Millisecond)
}
