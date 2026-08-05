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

func TestSubmissionAssessmentPersistsOneRunAndAtomicallyCommitsEveryItem(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-commit-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-commit-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-commit")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	input := submissionAssessmentCommitFixture(*claimed, ingest, assessment.AssessmentID, false)
	committed, err := repo.CommitSubmissionAssessment(ctx, input)
	require.NoError(t, err)
	require.NotNil(t, committed)
	assert.Equal(t, string(domain.SemanticReviewAccepted), committed.Status)
	assert.Len(t, committed.OutcomeIDs, 2)
	assert.Len(t, committed.EntityResolutionIDs, 4)
	assert.Len(t, committed.RelationshipResults, 2)
	assert.Len(t, committed.SearchDocuments, 4)

	var assessmentScope string
	var assessmentItemIsNull bool
	var runStatus string
	var completedItems, activeRelationships, verificationCount, entityResolutionCount, outcomeCount, evidenceDocumentCount, relationshipDocumentCount int64
	var actions []string
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT assessment_scope, placement_item_id IS NULL
			FROM placement_assessments
			WHERE team_id = ?::uuid AND assessment_id = ?::uuid
		`, teamID, assessment.AssessmentID).Row().Scan(&assessmentScope, &assessmentItemIsNull); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status FROM placement_runs
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Row().Scan(&runStatus); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM placement_items
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid AND status = 'completed'
		`, teamID, ingest.PlacementRunID).Scan(&completedItems).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM relationship_records
			WHERE team_id = ?::uuid AND status = 'active'
		`, teamID).Scan(&activeRelationships).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM verification_events
			WHERE team_id = ?::uuid AND assessment_id = ?::uuid
		`, teamID, assessment.AssessmentID).Scan(&verificationCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM entity_resolution_events
			WHERE team_id = ?::uuid AND assessment_id = ?::uuid
		`, teamID, assessment.AssessmentID).Scan(&entityResolutionCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM placement_outcomes
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
			  AND outcome_kind = 'submission_assessment_commit'
		`, teamID, ingest.PlacementRunID).Scan(&outcomeCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'evidence'
		`, teamID).Scan(&evidenceDocumentCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship'
		`, teamID).Scan(&relationshipDocumentCount).Error; err != nil {
			return err
		}
		rows, err := tx.Raw(`
			SELECT registration_action
			FROM predicate_registration_events
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
			ORDER BY relationship_ref ASC
		`, teamID, ingest.PlacementRunID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var action string
			if err := rows.Scan(&action); err != nil {
				return err
			}
			actions = append(actions, action)
		}
		return rows.Err()
	})
	require.NoError(t, err)
	assert.Equal(t, "submission", assessmentScope)
	assert.True(t, assessmentItemIsNull)
	assert.Equal(t, string(domain.PlacementRunCompleted), runStatus)
	assert.Equal(t, int64(2), completedItems)
	assert.Equal(t, int64(2), activeRelationships)
	assert.Equal(t, int64(2), verificationCount)
	assert.Equal(t, int64(4), entityResolutionCount)
	assert.Equal(t, int64(2), outcomeCount)
	assert.Equal(t, int64(2), evidenceDocumentCount)
	assert.Equal(t, int64(2), relationshipDocumentCount)
	assert.Equal(t, []string{"created", "reused"}, actions)

	err = rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE predicate_registration_events
			SET predicate_key = 'rewritten'
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, ingest.PlacementRunID).Error
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "append-only")
}

func TestSubmissionAssessmentRollsBackEverySemanticWriteWhenOneRelationshipFails(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-rollback-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-rollback-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-rollback")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	input := submissionAssessmentCommitFixture(*claimed, ingest, assessment.AssessmentID, true)
	before := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)

	_, err = repo.CommitSubmissionAssessment(ctx, input)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unresolved relationship endpoint")

	after := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, ingest.PlacementRunID)
	assert.Equal(t, before, after)
}

func TestSubmissionAssessmentCommitRequiresAssessmentForTheSameRun(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-scope-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-scope-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	first := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-scope-first")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	second := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-scope-second")
	wrongAssessment := persistSubmissionAssessment(t, ctx, repo, PlacementRun{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: second.IngestID, PlacementRunID: second.PlacementRunID,
	})
	input := submissionAssessmentCommitFixture(*claimed, first, wrongAssessment.AssessmentID, false)
	before := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, first.PlacementRunID)

	_, err = repo.CommitSubmissionAssessment(ctx, input)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrSubmissionAssessmentScopeMismatch)

	after := submissionAssessmentSemanticCounts(t, ctx, appDB, rls, teamID, ownerID, first.PlacementRunID)
	assert.Equal(t, before, after)
}

type submissionAssessmentSemanticCount struct {
	Entities        int64
	Predicates      int64
	Relationships   int64
	Verifications   int64
	Supports        int64
	Outcomes        int64
	SearchDocuments int64
}

func submissionAssessmentSemanticCounts(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls rLSHelper,
	teamID, ownerID, placementRunID string,
) submissionAssessmentSemanticCount {
	t.Helper()
	counts := submissionAssessmentSemanticCount{}
	err := rls.WithTeamProfileTx(ctx, db, teamID, ownerID, func(tx *gorm.DB) error {
		queries := []struct {
			query string
			dest  *int64
			args  []any
		}{
			{"SELECT COUNT(*) FROM entity_records WHERE team_id = ?::uuid", &counts.Entities, []any{teamID}},
			{"SELECT COUNT(*) FROM team_predicate_definitions WHERE team_id = ?::uuid", &counts.Predicates, []any{teamID}},
			{"SELECT COUNT(*) FROM relationship_records WHERE team_id = ?::uuid", &counts.Relationships, []any{teamID}},
			{"SELECT COUNT(*) FROM verification_events WHERE team_id = ?::uuid", &counts.Verifications, []any{teamID}},
			{"SELECT COUNT(*) FROM relationship_evidence_supports WHERE team_id = ?::uuid", &counts.Supports, []any{teamID}},
			{"SELECT COUNT(*) FROM placement_outcomes WHERE team_id = ?::uuid AND placement_run_id = ?::uuid", &counts.Outcomes, []any{teamID, placementRunID}},
			{"SELECT COUNT(*) FROM search_documents WHERE team_id = ?::uuid", &counts.SearchDocuments, []any{teamID}},
		}
		for _, query := range queries {
			if err := tx.Raw(query.query, query.args...).Scan(query.dest).Error; err != nil {
				return err
			}
		}
		return nil
	})
	require.NoError(t, err)
	return counts
}

func createSubmissionAssessmentIngest(t *testing.T, ctx context.Context, repo *LedgerRepositoryImpl, teamID, ownerID, idempotencyKey string) *CreateIngestResult {
	t.Helper()
	result, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: idempotencyKey,
		RequestHash:    sha256Hex(idempotencyKey),
		Evidence: []EvidenceInput{
			{Content: "Orion links Vega."},
			{Content: "Vega links Lyra."},
		},
	})
	require.NoError(t, err)
	require.Len(t, result.Evidence, 2)
	require.Len(t, result.Items, 2)
	return result
}

func persistSubmissionAssessment(t *testing.T, ctx context.Context, repo *LedgerRepositoryImpl, run PlacementRun) *SubmissionAssessment {
	t.Helper()
	response := json.RawMessage(`{"request_id":"submission-assessment","security_signals":[],"entity_results":[],"relationship_results":[]}`)
	assessment, existing, err := repo.PersistSubmissionAssessment(ctx, PersistSubmissionAssessmentInput{
		TeamID:                  run.TeamID,
		OwnerProfileID:          run.OwnerProfileID,
		IngestID:                run.IngestID,
		PlacementRunID:          run.PlacementRunID,
		RequestID:               "submission-assessment:" + run.PlacementRunID,
		AssessorContractVersion: domain.ContractVersion,
		Model:                   "submission-assessment-test",
		Tokenizer:               "o200k_base",
		InputTokens:             10,
		OutputTokens:            10,
		CandidateContextTokens:  10,
		NormalizedResponse:      response,
		ResponseHash:            "sha256:submission-assessment-test",
		ValidatedAt:             time.Now().UTC(),
	})
	require.NoError(t, err)
	assert.False(t, existing)
	return assessment
}

func submissionAssessmentCommitFixture(run PlacementRun, ingest *CreateIngestResult, assessmentID string, invalidSecondRelationship bool) CommitSubmissionAssessmentInput {
	threshold := 0.7
	items := []SubmissionAssessmentItemInput{
		{PlacementItemID: ingest.Items[0].PlacementItemID, FragmentID: ingest.Evidence[0].FragmentID},
		{PlacementItemID: ingest.Items[1].PlacementItemID, FragmentID: ingest.Evidence[1].FragmentID},
	}
	entity := func(item PlacementItem, fragment EvidenceFragment, ref, name string, start, end int) SubmissionAssessmentEntityResolutionInput {
		return SubmissionAssessmentEntityResolutionInput{
			PlacementItemID: item.PlacementItemID,
			Resolution: PlacementEntityResolutionInput{
				MentionRef: ref, Action: string(domain.EntityResolutionCreate), EntityKind: "concept", CanonicalName: name,
				FragmentID: fragment.FragmentID, SpanStart: &start, SpanEnd: &end, AssessmentID: assessmentID,
				IdentityContext: map[string]any{"source": "submission-assessment-test"},
			},
		}
	}
	observation := func(item PlacementItem, fragment EvidenceFragment, ref, subjectRef, objectRef, quote string, start, end int) SubmissionAssessmentRelationshipObservationInput {
		confidence := 0.9
		return SubmissionAssessmentRelationshipObservationInput{
			PlacementItemID: item.PlacementItemID,
			Observation: PlacementRelationshipDecisionInput{
				Ref: ref, SubjectRef: subjectRef, OriginalPredicate: "links", ObjectRef: objectRef, Polarity: "+",
				EvidenceVerdict: string(domain.VerificationEntailed), Confidence: &confidence, Rationale: "The evidence explicitly states the link.",
				Model: "submission-assessment-test", ResponseHash: "sha256:submission-assessment-test",
				Support:      &EvidenceSupportInput{FragmentID: fragment.FragmentID, SourceGroupKey: "submission-assessment:" + ref, SpanStart: start, SpanEnd: end, Quote: quote, Authority: "primary"},
				AssessmentID: assessmentID, AssessmentPolicyVersion: "submission-assessment-test", ThresholdUsed: &threshold, GateResult: "meets_write_threshold",
			},
		}
	}
	secondObjectRef := "lyra"
	if invalidSecondRelationship {
		secondObjectRef = "missing"
	}
	return CommitSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: run.TeamID, OwnerProfileID: run.OwnerProfileID, IngestID: run.IngestID, PlacementRunID: run.PlacementRunID,
			WorkerID: "submission-worker", ExpectedAttempts: run.Attempts,
		},
		AssessmentID: assessmentID,
		Items:        items,
		EntityResolutions: []SubmissionAssessmentEntityResolutionInput{
			entity(ingest.Items[0], ingest.Evidence[0], "orion", "Orion", 0, 5),
			entity(ingest.Items[0], ingest.Evidence[0], "vega-first", "Vega", 12, 16),
			entity(ingest.Items[1], ingest.Evidence[1], "vega-second", "Vega", 0, 4),
			entity(ingest.Items[1], ingest.Evidence[1], "lyra", "Lyra", 11, 15),
		},
		RelationshipObservations: []SubmissionAssessmentRelationshipObservationInput{
			observation(ingest.Items[0], ingest.Evidence[0], "r:orion-vega", "orion", "vega-first", "Orion links Vega.", 0, 17),
			observation(ingest.Items[1], ingest.Evidence[1], "r:vega-lyra", "vega-second", secondObjectRef, "Vega links Lyra.", 0, 16),
		},
		PredicateRegistrations: []SubmissionPredicateRegistrationInput{
			{RelationshipRef: "r:orion-vega", PredicateKey: "links", SubjectKind: "concept", ObjectKind: "concept"},
			{RelationshipRef: "r:vega-lyra", PredicateKey: "links", SubjectKind: "concept", ObjectKind: "concept"},
		},
		Payload: map[string]any{"assessor_contract": domain.ContractVersion},
	}
}
