package repository

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

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
) *EvidenceIngestResult {
	t.Helper()
	result, err := createTestIngest(ctx, repo, CreateIngestInput{
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

func createTestIngest(ctx context.Context, repo *LedgerRepositoryImpl, raw CreateIngestInput) (*EvidenceIngestResult, error) {
	input := normalizeCreateIngestInput(raw)
	if err := validateCreateIngestInput(input); err != nil {
		return nil, err
	}
	result := &EvidenceIngestResult{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: input.IngestID,
		Status: input.Status, Proposal: input.Proposal,
	}
	err := repo.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		ingestID, created, err := insertKnowledgeIngest(ctx, tx, input)
		if err != nil {
			return err
		}
		result.IngestID, result.Existing = ingestID, !created
		if !created {
			rows, err := tx.WithContext(ctx).Raw(`
				SELECT fragment_id::text, evidence_index, content, content_hash, authority,
				       COALESCE(source_id::text, ''), COALESCE(source_revision_id::text, '')
				FROM evidence_fragments
				WHERE team_id = ?::uuid AND ingest_id = ?::uuid AND owner_profile_id = ?::uuid
				ORDER BY evidence_index
			`, input.TeamID, ingestID, input.OwnerProfileID).Rows()
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var fragment EvidenceFragment
				if err := rows.Scan(&fragment.FragmentID, &fragment.EvidenceIndex, &fragment.Content, &fragment.ContentHash, &fragment.Authority, &fragment.SourceID, &fragment.SourceRevisionID); err != nil {
					return err
				}
				result.Evidence = append(result.Evidence, fragment)
			}
			return rows.Err()
		}
		sources := make(map[string]SourceRevisionResult, len(input.Evidence))
		for index, item := range input.Evidence {
			var source *SourceRevisionResult
			if item.SourceKey != "" {
				advanced, err := advanceSourceRevisionInTx(ctx, tx, AdvanceSourceRevisionInput{
					TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID,
					SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
					SourceKey: item.SourceKey, SourceKind: sourceKindForEvidence(item.SourceType),
					Authority: item.Authority, RevisionToken: item.SourceRevisionToken,
					ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken,
					ContentHash:                   item.SourceRevisionContentHash, Envelope: item.SourceRevisionEnvelope,
				}, sources)
				if err != nil {
					return err
				}
				source = advanced
			}
			fragment, err := insertEvidenceFragment(ctx, tx, input, ingestID, index, item, source)
			if err != nil {
				return err
			}
			result.Evidence = append(result.Evidence, fragment)
			if item.InitialEvent != nil {
				eventInput := SecurityEventInput{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, IngestID: ingestID, FragmentID: fragment.FragmentID, SecurityEventDraft: *item.InitialEvent}
				if _, err := insertSecurityEvent(ctx, tx, eventInput); err != nil {
					return err
				}
				if item.InitialEvent.Decision == "quarantine" {
					if err := insertEvidenceQuarantine(ctx, tx, input, ingestID, fragment.FragmentID, item.InitialEvent.Reason); err != nil {
						return err
					}
				}
			}
		}
		return applyEvidenceSupersessions(ctx, tx, input, ingestID, result.Evidence)
	})
	return result, err
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
