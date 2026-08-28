package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func correctRelationshipWithTestEmbeddings(ctx context.Context, semantic *SemanticRepositoryImpl, input CorrectRelationshipInput) (*CorrectRelationshipResult, error) {
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	if err != nil {
		return nil, err
	}
	return semantic.CorrectRelationshipWithEmbeddings(ctx, input, relationshipCorrectionTestEmbeddings(plan))
}

func relationshipCorrectionTestEmbeddings(plan *RelationshipCorrectionEmbeddingPlan) []RelationshipCorrectionEmbedding {
	embeddings := make([]RelationshipCorrectionEmbedding, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		embeddings = append(embeddings, RelationshipCorrectionEmbedding{
			DocumentHash:            document.DocumentHash,
			Embedding:               make([]float32, plan.EmbeddingDimensions),
			EmbeddingContractID:     plan.EmbeddingContractID,
			EmbeddingDimensions:     plan.EmbeddingDimensions,
			EmbeddingModel:          plan.EmbeddingModel,
			SearchIndexGenerationID: plan.SearchIndexGenerationID,
			IndexGeneration:         plan.IndexGeneration,
		})
	}
	return embeddings
}

func assertGraphHasNode(t *testing.T, nodes []SemanticGraphNode, nodeType, id, title string) {
	t.Helper()
	for _, node := range nodes {
		if node.Type == nodeType && node.ID == id {
			assert.Equal(t, title, node.Title)
			return
		}
	}
	t.Fatalf("missing %s node %s in %+v", nodeType, id, nodes)
}

func semanticGraphNodeIDs(nodes []SemanticGraphNode) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func semanticGraphEdgeIDs(edges []SemanticGraphEdge) []string {
	ids := make([]string, 0, len(edges))
	for _, edge := range edges {
		ids = append(ids, edge.ID)
	}
	return ids
}

func createSemanticEntity(
	t *testing.T,
	ctx context.Context,
	repo *SemanticRepositoryImpl,
	teamID string,
	ownerID string,
	kind string,
	name string,
) *EntityRecord {
	t.Helper()
	entity, err := repo.CreateEntity(ctx, CreateEntityInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		EntityKind:     kind,
		CanonicalName:  name,
	})
	require.NoError(t, err)
	return entity
}

func createSemanticIngest(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	idempotencyKey string,
	content string,
) *CreateIngestResult {
	t.Helper()
	result, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		IdempotencyKey: idempotencyKey,
		RequestHash:    sha256Hex(content),
		Evidence: []EvidenceInput{{
			Content: content,
		}},
	})
	require.NoError(t, err)
	require.Len(t, result.Evidence, 1)
	return result
}

func applySemanticDecision(
	t *testing.T,
	ctx context.Context,
	repo *SemanticRepositoryImpl,
	input ApplyRelationshipDecisionInput,
) *RelationshipDecisionResult {
	t.Helper()
	result, err := repo.ApplyRelationshipDecision(ctx, input)
	require.NoError(t, err)
	return result
}

func commitAcceptedSubmissionFixture(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	input CommitPlacementSemanticInput,
) (*CommitSubmissionAssessmentResult, error) {
	t.Helper()
	status, err := repo.GetPlacementRun(ctx, GetPlacementRunInput{
		TeamID:         input.TeamID,
		OwnerProfileID: input.OwnerProfileID,
		IngestID:       input.IngestID,
	})
	if err != nil {
		return nil, err
	}
	assessment := persistSubmissionAssessment(t, ctx, repo, PlacementRun{
		TeamID:         input.TeamID,
		OwnerProfileID: input.OwnerProfileID,
		IngestID:       input.IngestID,
		PlacementRunID: input.PlacementRunID,
		Attempts:       input.ExpectedAttempts,
	})
	fragmentByItemID := make(map[string]EvidenceFragment, len(status.Items))
	for index, item := range status.Items {
		if index < len(status.Evidence) {
			fragmentByItemID[item.PlacementItemID] = status.Evidence[index]
		}
	}
	itemID := input.PlacementItemID
	fragment, ok := fragmentByItemID[itemID]
	if !ok {
		return nil, ErrPlacementLeaseLost
	}
	entityResolutions := make([]SubmissionAssessmentEntityResolutionInput, 0, len(input.EntityResolutions))
	for _, resolution := range input.EntityResolutions {
		if resolution.FragmentID == "" {
			resolution.FragmentID = fragment.FragmentID
		}
		resolution.AssessmentID = assessment.AssessmentID
		entityResolutions = append(entityResolutions, SubmissionAssessmentEntityResolutionInput{
			PlacementItemID: itemID,
			Resolution:      resolution,
		})
	}
	relationships := make([]SubmissionAssessmentRelationshipObservationInput, 0, len(input.RelationshipObservations))
	for _, observation := range input.RelationshipObservations {
		observation = normalizePlacementRelationshipDecisionInput(observation)
		observation.AssessorAccepted = true
		observation.EvidenceVerdict = ""
		observation.Confidence = nil
		observation.Rationale = ""
		observation.AssessmentID = assessment.AssessmentID
		observation.AssessmentPolicyVersion = ""
		observation.ThresholdUsed = nil
		observation.GateResult = ""
		relationships = append(relationships, SubmissionAssessmentRelationshipObservationInput{
			PlacementItemID: itemID,
			RelationshipRef: observation.Ref,
			SplitIndex:      0,
			Observation:     observation,
		})
	}
	relationshipResults := make([]SubmissionRelationshipResultInput, 0, len(relationships))
	for _, relationship := range relationships {
		relationshipResults = append(relationshipResults, SubmissionRelationshipResultInput{
			RelationshipRef: relationship.Observation.Ref,
			Disposition:     "stored",
		})
	}
	return repo.CommitSubmissionAssessment(ctx, CommitSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID:           input.TeamID,
			OwnerProfileID:   input.OwnerProfileID,
			IngestID:         input.IngestID,
			PlacementRunID:   input.PlacementRunID,
			WorkerID:         input.WorkerID,
			ExpectedAttempts: input.ExpectedAttempts,
		},
		AssessmentID: assessment.AssessmentID,
		Items: []SubmissionAssessmentItemInput{{
			PlacementItemID: itemID,
			FragmentID:      fragment.FragmentID,
		}},
		EntityResolutions:        entityResolutions,
		RelationshipObservations: relationships,
		RelationshipResults:      relationshipResults,
		Payload:                  input.Payload,
	})
}

func assertSameTeamCanReadSemanticEdge(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	teamID string,
	profileID string,
	relationshipID string,
) {
	t.Helper()
	var count int64
	err := rls.WithTeamProfileTx(ctx, db, teamID, profileID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM semantic_edges
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, relationshipID).Scan(&count).Error
	})
	require.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func assertCrossTeamCannotReadSemanticEdge(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	targetTeamID string,
	readerTeamID string,
	readerProfileID string,
) {
	t.Helper()
	var count int64
	err := rls.WithTeamProfileTx(ctx, db, readerTeamID, readerProfileID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*)
			FROM semantic_edges
			WHERE team_id = ?::uuid
		`, targetTeamID).Scan(&count).Error
	})
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}
