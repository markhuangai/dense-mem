package repository

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

func TestSubmissionRepositoryStagesBeforeCanonicalEvidenceAndPurgesQuarantine(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "secure-submission-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "secure-submission-owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "secure-submission-other")
	repo := NewLedgerRepository(appDB, rls)

	created, err := repo.CreateSubmission(ctx, CreateSubmissionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: "submission-1",
		RequestHash:    "sha256:submission-1",
		Proposal: map[string]any{
			"entities":      []any{},
			"relationships": []any{},
		},
		Evidence: []SubmissionEvidenceInput{{
			Content:    "Dense-Mem uses PostgreSQL.",
			SourceType: "document",
			Authority:  string(domain.AuthorityPrimary),
		}},
	})
	require.NoError(t, err)
	require.Equal(t, string(domain.SubmissionQueued), created.Status)
	require.Len(t, created.Evidence, 1)

	var canonicalCount int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*)::int FROM evidence_fragments WHERE team_id = ?::uuid`, teamID).Scan(&canonicalCount).Error
	}))
	require.Zero(t, canonicalCount)

	status, err := repo.GetSubmissionStatus(ctx, GetSubmissionStatusInput{TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: created.SubmissionID})
	require.NoError(t, err)
	require.Equal(t, string(domain.SubmissionQueued), status.ProcessingState)
	require.Len(t, status.Evidence, 1)

	_, err = repo.GetSubmissionStatus(ctx, GetSubmissionStatusInput{TeamID: teamID, OwnerProfileID: otherOwnerID, SubmissionID: created.SubmissionID})
	require.ErrorIs(t, err, ErrSubmissionNotFound)

	claim, err := repo.ClaimNextSubmission(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, created.SubmissionID, claim.SubmissionID)
	loaded, err := repo.LoadClaimedSubmission(ctx, LoadClaimedSubmissionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: created.SubmissionID, WorkerID: "submission-worker", Attempts: claim.Attempts,
	})
	require.NoError(t, err)
	require.Equal(t, "Dense-Mem uses PostgreSQL.", loaded.Evidence[0].Content)

	payload, err := json.Marshal(map[string]any{"request_id": "submission-assessment"})
	require.NoError(t, err)
	_, existing, err := repo.PersistSubmissionAssessment(ctx, PersistSubmissionAssessmentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: created.SubmissionID,
		WorkerID: "submission-worker", ExpectedAttempts: claim.Attempts, RequestID: "submission-assessment",
		Model: "test-model", Tokenizer: "o200k_base", NormalizedResponse: payload,
		ResponseHash: "sha256:assessment", ValidatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.False(t, existing)

	require.NoError(t, repo.QuarantineSubmission(ctx, QuarantineSubmissionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: created.SubmissionID,
		WorkerID: "submission-worker", ExpectedAttempts: claim.Attempts, ReasonCode: "assessor_security_concern",
		EvidenceOutcomes: []SubmissionEvidenceStatus{{EvidenceIndex: 0, Status: "quarantined", ReasonCode: "assessor_security_concern", SearchState: string(domain.SearchProjectionNotRequired)}},
	}))
	status, err = repo.GetSubmissionStatus(ctx, GetSubmissionStatusInput{TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: created.SubmissionID})
	require.NoError(t, err)
	require.Equal(t, string(domain.SubmissionQuarantined), status.ProcessingState)
	require.NotNil(t, status.QuarantineExpiresAt)
	var exactlyTwentyFourHours bool
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT quarantine_expires_at = completed_at + interval '24 hours'
			FROM submission_runs
			WHERE team_id = ?::uuid AND submission_id = ?::uuid
		`, teamID, created.SubmissionID).Scan(&exactlyTwentyFourHours).Error
	}))
	require.True(t, exactlyTwentyFourHours)

	deleted, err := repo.CleanupExpiredSubmissions(ctx, time.Now().UTC().Add(25*time.Hour), 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	_, err = repo.GetSubmissionStatus(ctx, GetSubmissionStatusInput{TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: created.SubmissionID})
	require.True(t, errors.Is(err, ErrSubmissionNotFound))
}

func TestSubmissionRepositoryIdempotencyAndReplacementOwnership(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "secure-submission-replacement-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "secure-submission-replacement-owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "secure-submission-replacement-other")
	repo := NewLedgerRepository(appDB, rls)

	firstInput := CreateSubmissionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "same-key", RequestHash: "sha256:one",
		Proposal: map[string]any{}, Evidence: []SubmissionEvidenceInput{{Content: "first", Authority: "primary"}},
	}
	first, err := repo.CreateSubmission(ctx, firstInput)
	require.NoError(t, err)
	replayed, err := repo.CreateSubmission(ctx, firstInput)
	require.NoError(t, err)
	require.Equal(t, first.SubmissionID, replayed.SubmissionID)
	firstInput.RequestHash = "sha256:different"
	_, err = repo.CreateSubmission(ctx, firstInput)
	require.ErrorIs(t, err, ErrSubmissionConflict)

	foreignQuarantine := uuid.NewString()
	_, err = repo.CreateSubmission(ctx, CreateSubmissionInput{
		TeamID: teamID, OwnerProfileID: otherOwnerID, RequestHash: "sha256:foreign", Proposal: map[string]any{},
		Evidence: []SubmissionEvidenceInput{{Content: "foreign", Authority: "primary"}}, ReplacesQuarantinedSubmissionID: foreignQuarantine,
	})
	require.ErrorIs(t, err, ErrSubmissionNotFound)
}

func TestSubmissionRepositoryPersistsAuthenticatedSubmissionProvenance(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-provenance-input-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-provenance-input-owner")
	credentialID := uuid.NewString()
	repo := NewLedgerRepository(appDB, rls)

	created, err := repo.CreateSubmission(ctx, CreateSubmissionInput{
		TeamID: teamID, OwnerProfileID: ownerID, RequestHash: "sha256:submission-provenance-input",
		ActorCredentialID: credentialID, ActorAuthMethod: "api_key", ActorRole: "member",
		ActorScopes: []string{"write", "read", "write"}, CorrelationID: "corr-submission-provenance",
		Proposal: map[string]any{"entities": []any{}, "relationships": []any{}},
		Evidence: []SubmissionEvidenceInput{{Content: "Submission provenance is durable.", Authority: "primary"}},
	})
	require.NoError(t, err)
	require.Equal(t, credentialID, created.ActorCredentialID)
	require.Equal(t, "api_key", created.ActorAuthMethod)
	require.Equal(t, "member", created.ActorRole)
	require.Equal(t, []string{"read", "write"}, created.ActorScopes)
	require.Equal(t, "corr-submission-provenance", created.CorrelationID)
}

func TestSubmissionRepositoryFinalExpiredLeaseIsTerminalized(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-expired-final-attempt-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-expired-final-attempt-owner")
	repo := NewLedgerRepository(appDB, rls)

	created, err := repo.CreateSubmission(ctx, CreateSubmissionInput{
		TeamID: teamID, OwnerProfileID: ownerID, RequestHash: "sha256:expired-final-attempt",
		Proposal: map[string]any{"entities": []any{}, "relationships": []any{}},
		Evidence: []SubmissionEvidenceInput{{Content: "This attempt will expire.", Authority: "primary"}},
	})
	require.NoError(t, err)
	claim, err := repo.ClaimNextSubmission(ctx, teamID, "expired-final-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)

	expiredAt := time.Now().UTC().Add(-time.Minute)
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE submission_runs
			SET attempts = max_attempts, lease_until = ?, worker_id = 'expired-final-worker', status = 'processing'
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND submission_id = ?::uuid
		`, expiredAt, teamID, ownerID, created.SubmissionID).Error
	}))

	processed, err := repo.CleanupExpiredSubmissions(ctx, time.Now().UTC(), 100)
	require.NoError(t, err)
	require.EqualValues(t, 1, processed)
	status, err := repo.GetSubmissionStatus(ctx, GetSubmissionStatusInput{TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: created.SubmissionID})
	require.NoError(t, err)
	require.Equal(t, string(domain.SubmissionFailed), status.ProcessingState)
	require.Contains(t, status.Errors, SubmissionStatusError{Code: "lease_expired_final_attempt"})
	var stagedEvidence, stagedProposals int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*)::int FROM submission_staged_evidence WHERE team_id = ?::uuid AND submission_id = ?::uuid`, teamID, created.SubmissionID).Scan(&stagedEvidence).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*)::int FROM submission_staged_proposals WHERE team_id = ?::uuid AND submission_id = ?::uuid`, teamID, created.SubmissionID).Scan(&stagedProposals).Error
	}))
	require.Zero(t, stagedEvidence)
	require.Zero(t, stagedProposals)
	claim, err = repo.ClaimNextSubmission(ctx, teamID, "next-worker", time.Minute)
	require.NoError(t, err)
	require.Nil(t, claim)
}

func TestSubmissionRepositoryConcurrentIdempotencyReturnsExistingSubmission(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-idempotency-race-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-idempotency-race-owner")
	repo := NewLedgerRepository(appDB, rls)
	input := CreateSubmissionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: "submission-race", RequestHash: "sha256:submission-race",
		Proposal: map[string]any{"entities": []any{}, "relationships": []any{}},
		Evidence: []SubmissionEvidenceInput{{Content: "Concurrent submission evidence.", Authority: "primary"}},
	}

	const workers = 16
	start := make(chan struct{})
	type outcome struct {
		submission *Submission
		err        error
	}
	outcomes := make(chan outcome, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			submission, err := repo.CreateSubmission(ctx, input)
			outcomes <- outcome{submission: submission, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(outcomes)

	submissionIDs := map[string]struct{}{}
	for result := range outcomes {
		require.NoError(t, result.err)
		require.NotNil(t, result.submission)
		submissionIDs[result.submission.SubmissionID] = struct{}{}
	}
	require.Len(t, submissionIDs, 1)
	var runCount, stagedEvidenceCount int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT count(*)::int FROM submission_runs
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = 'submission-race'
		`, teamID, ownerID).Scan(&runCount).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*)::int FROM submission_staged_evidence WHERE team_id = ?::uuid`, teamID).Scan(&stagedEvidenceCount).Error
	}))
	require.Equal(t, 1, runCount)
	require.Equal(t, 1, stagedEvidenceCount)
}

func TestSubmissionSearchProjectionState(t *testing.T) {
	for _, test := range []struct {
		name   string
		states []string
		want   string
	}{
		{name: "no linked document", states: nil, want: string(domain.SearchProjectionNotRequired)},
		{name: "current", states: []string{string(domain.SearchProjectionCurrent)}, want: string(domain.SearchProjectionCurrent)},
		{name: "not required", states: []string{string(domain.SearchProjectionNotRequired)}, want: string(domain.SearchProjectionNotRequired)},
		{name: "pending dominates", states: []string{string(domain.SearchProjectionCurrent), string(domain.SearchProjectionPending), string(domain.SearchProjectionFailed)}, want: string(domain.SearchProjectionPending)},
		{name: "failed after work finishes", states: []string{string(domain.SearchProjectionCurrent), string(domain.SearchProjectionFailed)}, want: string(domain.SearchProjectionFailed)},
	} {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, submissionSearchProjectionState(test.states))
		})
	}
}

func TestSubmissionPromotionCreatesCanonicalEvidenceAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-promotion-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-promotion-owner")
	repo := NewLedgerRepository(appDB, rls)
	ensureSubmissionPromotionSearchContract(t, ctx, adminDB, rls)

	created, err := repo.CreateSubmission(ctx, CreateSubmissionInput{
		TeamID: teamID, OwnerProfileID: ownerID, RequestHash: "sha256:promotion", SourceSummary: "submission promotion",
		Proposal: map[string]any{"entities": []any{}, "relationships": []any{}},
		Evidence: []SubmissionEvidenceInput{{Content: "Dense-Mem uses PostgreSQL.", SourceType: "document", Authority: "primary"}},
	})
	require.NoError(t, err)
	claim, err := repo.ClaimNextSubmission(ctx, teamID, "promotion-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)

	promotion := testSubmissionPromotionInput(teamID, ownerID, created.SubmissionID, claim.Attempts, "promotion-worker", "sha256:promotion", "submission promotion")
	promoted, err := repo.PromoteSubmission(ctx, promotion)
	require.NoError(t, err)
	require.Equal(t, promotion.Canonical.IngestID, promoted.CanonicalIngestID)

	status, err := repo.GetSubmissionStatus(ctx, GetSubmissionStatusInput{TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: created.SubmissionID})
	require.NoError(t, err)
	require.Equal(t, string(domain.SubmissionCompleted), status.ProcessingState)
	require.Len(t, status.Evidence, 1)
	require.Equal(t, "accepted", status.Evidence[0].Status)

	var canonicalCount, stagedEvidenceCount int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT count(*)::int FROM evidence_fragments WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, promotion.Canonical.IngestID).Scan(&canonicalCount).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT count(*)::int FROM submission_staged_evidence WHERE team_id = ?::uuid AND submission_id = ?::uuid`, teamID, created.SubmissionID).Scan(&stagedEvidenceCount).Error
	}))
	require.Equal(t, 1, canonicalCount)
	require.Zero(t, stagedEvidenceCount)
}

func TestSubmissionPromotionLinksSemanticEventsToSubmissionAssessment(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-provenance-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-provenance-owner")
	repo := NewLedgerRepository(appDB, rls)
	ensureSubmissionPromotionSearchContract(t, ctx, adminDB, rls)

	created, err := repo.CreateSubmission(ctx, CreateSubmissionInput{
		TeamID: teamID, OwnerProfileID: ownerID, RequestHash: "sha256:submission-provenance", SourceSummary: "submission provenance",
		Proposal: map[string]any{"entities": []any{}, "relationships": []any{}},
		Evidence: []SubmissionEvidenceInput{{Content: "Dense-Mem uses PostgreSQL.", SourceType: "document", Authority: "primary"}},
	})
	require.NoError(t, err)
	claim, err := repo.ClaimNextSubmission(ctx, teamID, "submission-provenance-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)

	assessment, existing, err := repo.PersistSubmissionAssessment(ctx, PersistSubmissionAssessmentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: created.SubmissionID,
		WorkerID: "submission-provenance-worker", ExpectedAttempts: claim.Attempts,
		RequestID: "submission-assessment:" + created.SubmissionID,
		Model:     "test-submission-assessor", Tokenizer: "test-tokenizer",
		NormalizedResponse: json.RawMessage(`{}`), ResponseHash: "sha256:submission-provenance-assessment",
		ValidatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.False(t, existing)

	promotion := testSubmissionPromotionInput(teamID, ownerID, created.SubmissionID, claim.Attempts, "submission-provenance-worker", "sha256:submission-provenance", "submission provenance")
	promotion.Canonical.IdempotencyKey = "submission:" + created.SubmissionID
	fragment := promotion.Canonical.Evidence[0]
	confidence := 0.99
	threshold := 0.8
	subjectStart, subjectEnd := 0, 9
	objectStart, objectEnd := 15, 25
	promotion.Commits[0].EntityResolutions = []PlacementEntityResolutionInput{
		{
			MentionRef: "subject", Action: string(domain.EntityResolutionCreate), EntityKind: string(domain.EntityKindProject),
			CanonicalName: "Dense-Mem", FragmentID: fragment.FragmentID, SpanStart: &subjectStart, SpanEnd: &subjectEnd,
			VerifierResult: map[string]any{"source": "submission_assessment"}, Metadata: map[string]any{},
			SubmissionAssessmentID: assessment.AssessmentID,
		},
		{
			MentionRef: "object", Action: string(domain.EntityResolutionCreate), EntityKind: string(domain.EntityKindProduct),
			CanonicalName: "PostgreSQL", FragmentID: fragment.FragmentID, SpanStart: &objectStart, SpanEnd: &objectEnd,
			VerifierResult: map[string]any{"source": "submission_assessment"}, Metadata: map[string]any{},
			SubmissionAssessmentID: assessment.AssessmentID,
		},
	}
	promotion.Commits[0].RelationshipObservations = []PlacementRelationshipDecisionInput{{
		Ref: "relationship_1", SubjectRef: "subject", OriginalPredicate: "uses", PredicateKey: "uses", PredicateVersion: 1,
		ObjectRef: "object", EvidenceVerdict: string(domain.VerificationEntailed), Confidence: &confidence,
		Rationale: "The evidence explicitly states the relationship.", Model: assessment.Model, ResponseHash: assessment.ResponseHash,
		Support: &EvidenceSupportInput{
			FragmentID: fragment.FragmentID, SourceGroupKey: "submission-provenance", SpanStart: 0, SpanEnd: len(fragment.Content),
			Quote: fragment.Content, Authority: "primary",
		},
		SubmissionAssessmentID: assessment.AssessmentID, AssessmentPolicyVersion: "test:submission", ThresholdUsed: &threshold,
		GateResult: "meets_write_threshold",
	}}

	_, err = repo.PromoteSubmission(ctx, promotion)
	require.NoError(t, err)

	var entityEventCount, verificationEventCount int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT count(*)::int
			FROM entity_resolution_events
			WHERE team_id = ?::uuid
			  AND submission_assessment_id = ?::uuid
			  AND assessment_id IS NULL
		`, teamID, assessment.AssessmentID).Scan(&entityEventCount).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT count(*)::int
			FROM verification_events
			WHERE team_id = ?::uuid
			  AND submission_assessment_id = ?::uuid
			  AND assessment_id IS NULL
		`, teamID, assessment.AssessmentID).Scan(&verificationEventCount).Error
	}))
	require.Equal(t, 2, entityEventCount)
	require.Equal(t, 1, verificationEventCount)
}

func TestSubmissionPromotionResolvedPredicateBypassesAmbiguousAliasLookup(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-resolved-predicate-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-resolved-predicate-owner")
	repo := NewLedgerRepository(appDB, rls)
	ensureSubmissionPromotionSearchContract(t, ctx, adminDB, rls)

	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := ensureSemanticRefs(ctx, tx, teamID, ownerID); err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO team_predicate_definitions (
				team_id, predicate_key, version, aliases, allowed_subject_kinds,
				allowed_object_kinds, relationship_kind, current_cardinality,
				lifecycle_state, origin, metadata
			) VALUES
				(?::uuid, 'resolved_submission_is', 1, ARRAY['is'], ARRAY['other'], ARRAY['other'], 'state', 'many', 'active', 'test', '{}'::jsonb),
				(?::uuid, 'alias_submission_is', 1, ARRAY['is'], ARRAY['other'], ARRAY['other'], 'state', 'many', 'active', 'test', '{}'::jsonb)
		`, teamID, teamID).Error
	}))

	createPromotion := func(idempotencyKey string) PromoteSubmissionInput {
		requestHash := "sha256:" + idempotencyKey
		created, err := repo.CreateSubmission(ctx, CreateSubmissionInput{
			TeamID: teamID, OwnerProfileID: ownerID, IdempotencyKey: idempotencyKey, RequestHash: requestHash, SourceSummary: "submission predicate alias",
			Proposal: map[string]any{"entities": []any{}, "relationships": []any{}},
			Evidence: []SubmissionEvidenceInput{{Content: "Dense-Mem uses PostgreSQL.", SourceType: "document", Authority: "primary"}},
		})
		require.NoError(t, err)
		claim, err := repo.ClaimNextSubmission(ctx, teamID, "resolved-predicate-worker", time.Minute)
		require.NoError(t, err)
		require.NotNil(t, claim)

		promotion := testSubmissionPromotionInput(teamID, ownerID, created.SubmissionID, claim.Attempts, "resolved-predicate-worker", requestHash, "submission predicate alias")
		fragment := promotion.Canonical.Evidence[0]
		confidence := 0.99
		subjectStart, subjectEnd := 0, 9
		objectStart, objectEnd := 15, 25
		promotion.Commits[0].EntityResolutions = []PlacementEntityResolutionInput{
			{MentionRef: "subject", Action: string(domain.EntityResolutionCreate), EntityKind: string(domain.EntityKindOther), CanonicalName: "Dense-Mem", FragmentID: fragment.FragmentID, SpanStart: &subjectStart, SpanEnd: &subjectEnd, VerifierResult: map[string]any{}, Metadata: map[string]any{}},
			{MentionRef: "object", Action: string(domain.EntityResolutionCreate), EntityKind: string(domain.EntityKindOther), CanonicalName: "PostgreSQL", FragmentID: fragment.FragmentID, SpanStart: &objectStart, SpanEnd: &objectEnd, VerifierResult: map[string]any{}, Metadata: map[string]any{}},
		}
		promotion.Commits[0].RelationshipObservations = []PlacementRelationshipDecisionInput{{
			Ref: "relationship", SubjectRef: "subject", OriginalPredicate: "is", PredicateKey: "resolved_submission_is", PredicateVersion: 1,
			ObjectRef: "object", EvidenceVerdict: string(domain.VerificationEntailed), Confidence: &confidence,
			Support: &EvidenceSupportInput{FragmentID: fragment.FragmentID, SourceGroupKey: "submission-resolved-predicate", SpanStart: 0, SpanEnd: len(fragment.Content), Quote: fragment.Content, Authority: "primary"},
		}}
		return promotion
	}

	ambiguous := createPromotion("submission-predicate-ambiguous")
	ambiguous.Commits[0].RelationshipObservations[0].PredicateCandidate = &PlacementPredicateCandidateInput{
		PredicateKey: "resolved_submission_is", PredicateVersion: 1, RelationshipKind: "state",
	}
	_, err := repo.PromoteSubmission(ctx, ambiguous)
	require.ErrorIs(t, err, ErrSubmissionConflict)

	exact := createPromotion("submission-predicate-exact")
	_, err = repo.PromoteSubmission(ctx, exact)
	require.NoError(t, err)

	var relationshipCount int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT count(*)::int
			FROM relationship_records
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND predicate_key = 'resolved_submission_is'
		`, teamID, ownerID).Scan(&relationshipCount).Error
	}))
	require.Equal(t, 1, relationshipCount)
}

func TestSubmissionPromotionRollsBackCanonicalWritesOnSemanticFailure(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-promotion-rollback-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-promotion-rollback-owner")
	repo := NewLedgerRepository(appDB, rls)
	ensureSubmissionPromotionSearchContract(t, ctx, adminDB, rls)
	created, err := repo.CreateSubmission(ctx, CreateSubmissionInput{
		TeamID: teamID, OwnerProfileID: ownerID, RequestHash: "sha256:rollback", SourceSummary: "submission rollback",
		Proposal: map[string]any{"entities": []any{}, "relationships": []any{}},
		Evidence: []SubmissionEvidenceInput{{Content: "Dense-Mem uses PostgreSQL.", SourceType: "document", Authority: "primary"}},
	})
	require.NoError(t, err)
	claim, err := repo.ClaimNextSubmission(ctx, teamID, "rollback-worker", time.Minute)
	require.NoError(t, err)
	promotion := testSubmissionPromotionInput(teamID, ownerID, created.SubmissionID, claim.Attempts, "rollback-worker", "sha256:rollback", "submission rollback")
	promotion.Commits[0].EntityResolutions = []PlacementEntityResolutionInput{{
		MentionRef: "missing-entity",
		Action:     string(domain.EntityResolutionReuse),
		EntityID:   uuid.NewString(),
	}}

	_, err = repo.PromoteSubmission(ctx, promotion)
	require.Error(t, err)
	var canonicalCount int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*)::int FROM evidence_fragments WHERE team_id = ?::uuid`, teamID).Scan(&canonicalCount).Error
	}))
	require.Zero(t, canonicalCount)
	status, err := repo.GetSubmissionStatus(ctx, GetSubmissionStatusInput{TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: created.SubmissionID})
	require.NoError(t, err)
	require.Equal(t, string(domain.SubmissionProcessing), status.ProcessingState)
}

func testSubmissionPromotionInput(teamID, ownerID, submissionID string, attempts int, workerID, requestHash, sourceSummary string) PromoteSubmissionInput {
	ingestID := uuid.NewString()
	runID := uuid.NewString()
	fragmentID := uuid.NewString()
	itemID := uuid.NewString()
	return PromoteSubmissionInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		SubmissionID:     submissionID,
		WorkerID:         workerID,
		ExpectedAttempts: attempts,
		Lease:            time.Minute,
		Canonical: CreateIngestInput{
			IngestID:       ingestID,
			PlacementRunID: runID,
			TeamID:         teamID,
			OwnerProfileID: ownerID,
			RequestHash:    requestHash,
			SourceSummary:  sourceSummary,
			Status:         string(domain.PlacementRunProcessing),
			Proposal:       map[string]any{"entities": []any{}, "relationships": []any{}},
			Metadata:       map[string]any{"contract_version": domain.ContractVersion},
			Evidence: []EvidenceInput{{
				FragmentID:      fragmentID,
				PlacementItemID: itemID,
				Content:         "Dense-Mem uses PostgreSQL.",
				SourceType:      "document",
				Authority:       "primary",
				Metadata:        map[string]any{},
			}},
		},
		Commits: []CommitPlacementSemanticInput{{
			TeamID:           teamID,
			OwnerProfileID:   ownerID,
			IngestID:         ingestID,
			PlacementRunID:   runID,
			PlacementItemID:  itemID,
			WorkerID:         workerID,
			ExpectedAttempts: attempts,
			OutcomeKind:      "submission_assessment_commit",
			Status:           string(domain.SemanticReviewAccepted),
			Category:         "validated_claim",
			Payload:          map[string]any{"source": "test"},
		}},
		EvidenceOutcomes: []SubmissionEvidenceStatus{{
			EvidenceIndex: 0,
			Status:        "accepted",
			ReasonCode:    "assessed_and_promoted",
			SearchState:   string(domain.SearchProjectionPending),
		}},
	}
}

func ensureSubmissionPromotionSearchContract(t *testing.T, ctx context.Context, db *gorm.DB, rls *storagepostgres.RLS) {
	t.Helper()
	_, err := NewSearchRepository(db, rls).EnsureActiveSearchContract(ctx, EnsureActiveSearchContractInput{
		Provider:   "test",
		Model:      "submission-promotion",
		Dimensions: 3,
	})
	require.NoError(t, err)
}
