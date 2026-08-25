package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSubmissionAssessmentInlinePlanUsesPersistedValueDisplay(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "submission-assessment-persisted-display")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "submission-assessment-persisted-display-owner")
	insertSearchTestContract(t, adminDB, rls, "submission-assessment-persisted-display-search", 3, "exact", "")
	repo := NewLedgerRepository(appDB, rls)
	ingest := createSubmissionAssessmentIngest(t, ctx, repo, teamID, ownerID, "submission-assessment-persisted-display")
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID, "submission-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assessment := persistSubmissionAssessment(t, ctx, repo, *claimed)
	input := submissionAssessmentCommitFixture(*claimed, ingest, assessment.AssessmentID, false)
	input.RelationshipObservations[0].Observation.ObjectRef = ""
	input.RelationshipObservations[0].Observation.ObjectValue = &PlacementValueInput{
		Ref: "value:persisted-display", ValueType: "string", CanonicalValue: "PostgreSQL", Display: "Request Display",
	}
	input.PredicateRegistrations[0] = SubmissionPredicateRegistrationInput{
		RelationshipRef: input.RelationshipObservations[0].Observation.Ref,
		PredicateKey:    "stores_value", SubjectKind: "concept", ObjectKind: "string",
		RelationshipKind: "state", CurrentCardinality: "many",
	}
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO value_records (team_id, value_type, canonical_value, unit, display, normalization_version, metadata)
			VALUES (?::uuid, 'string', 'PostgreSQL', NULL, 'Persisted Display', 1, '{}'::jsonb)
		`, teamID).Error
	}))

	plan, err := repo.PlanSubmissionAssessmentEmbeddings(ctx, input)
	require.NoError(t, err)
	foundPlanDisplay := false
	for _, document := range plan.Documents {
		if document.SourceKind == "relationship" && strings.Contains(document.DocumentText, "Persisted Display") {
			foundPlanDisplay = true
		}
		require.NotContains(t, document.DocumentText, "Request Display")
	}
	require.True(t, foundPlanDisplay, "plan documents = %#v", plan.Documents)

	embeddings := make([]InlineEmbeddingResult, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		embeddings = append(embeddings, InlineEmbeddingResult{DocumentHash: document.DocumentHash, Embedding: []float32{1, 0, 0}})
	}
	_, err = repo.CommitSubmissionAssessmentWithEmbeddings(ctx, input, embeddings)
	require.NoError(t, err)
	var committedTexts []string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT document_text FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship'
			ORDER BY updated_at DESC, search_document_id DESC
		`, teamID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var text string
			if err := rows.Scan(&text); err != nil {
				return err
			}
			committedTexts = append(committedTexts, text)
		}
		return rows.Err()
	}))
	foundCommittedDisplay := false
	for _, text := range committedTexts {
		if strings.Contains(text, "Persisted Display") {
			foundCommittedDisplay = true
		}
		require.NotContains(t, text, "Request Display")
	}
	require.True(t, foundCommittedDisplay, "committed documents = %#v", committedTexts)
}
