package repository

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestV2SemanticSupportMustMatchSuppliedIngest(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "semantic-support-ingest-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	sourceOne, err := ledgerRepo.AdvanceSourceRevision(ctx, V2AdvanceSourceRevisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKey:      "doc://ingest-one",
		RevisionToken:  "rev-1",
		ContentHash:    "sha256:one",
	})
	require.NoError(t, err)
	sourceTwo, err := ledgerRepo.AdvanceSourceRevision(ctx, V2AdvanceSourceRevisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKey:      "doc://ingest-two",
		RevisionToken:  "rev-1",
		ContentHash:    "sha256:two",
	})
	require.NoError(t, err)

	firstIngest := createV2SemanticSourceIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"Dense-Mem uses PostgreSQL.", sourceOne)
	secondIngest := createV2SemanticSourceIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"Dense-Mem used Neo4j.", sourceTwo)
	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")

	_, err = semanticRepo.ApplyRelationshipDecision(ctx, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: v2SemanticSupportFromSource(
			secondIngest.Evidence[0].FragmentID, sourceTwo, "doc://ingest-two", "Dense-Mem used Neo4j.",
		),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2SemanticOwnerMismatch), err)

	_, err = semanticRepo.ApplyRelationshipDecision(ctx, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: v2SemanticSupportFromSource(
			firstIngest.Evidence[0].FragmentID, sourceTwo, "doc://ingest-two", "Dense-Mem uses PostgreSQL.",
		),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrV2SemanticOwnerMismatch), err)

	matching := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: v2SemanticSupportFromSource(
			firstIngest.Evidence[0].FragmentID, sourceOne, "doc://ingest-one", "Dense-Mem uses PostgreSQL.",
		),
	})
	require.NotNil(t, matching.Relationship)
	assert.NotEmpty(t, matching.SupportID)
}

func TestV2SemanticOneCardinalityUpgradeSupersedesLegacyManyRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "one-cardinality-upgrade-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	neo4j := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "Neo4j")
	predicateKey := "sole_runtime_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	require.NoError(t, insertV2CardinalityUpgradePredicate(ctx, adminDB, rls, predicateKey))

	postgresIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, "uses postgres", "Dense-Mem uses PostgreSQL.")
	postgresUse := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        postgresIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    predicateKey,
		ObjectEntityID:  postgres.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     postgresIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:uses-postgres",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses PostgreSQL."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "many", postgresUse.Relationship.CurrentCardinality)

	neo4jIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, "uses neo4j", "Dense-Mem uses Neo4j.")
	neo4jUse := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        neo4jIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    predicateKey,
		ObjectEntityID:  neo4j.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     neo4jIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:uses-neo4j",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses Neo4j."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "many", neo4jUse.Relationship.CurrentCardinality)

	upgradeIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"uses postgres as sole database", "Dense-Mem uses PostgreSQL as its sole durable database.")
	upgraded := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         upgradeIngest.IngestID,
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     predicateKey,
		PredicateVersion: 2,
		ObjectEntityID:   postgres.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     upgradeIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:uses-postgres-sole",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses PostgreSQL as its sole durable database."),
			Authority:      "primary",
		},
	})
	require.Equal(t, postgresUse.Relationship.RelationshipID, upgraded.Relationship.RelationshipID)
	assert.Equal(t, "one", upgraded.Relationship.CurrentCardinality)

	var legacySiblingStatus string
	err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status
			FROM relationship_records
			WHERE team_id = ?::uuid
			  AND relationship_id = ?::uuid
		`, teamID, neo4jUse.Relationship.RelationshipID).Scan(&legacySiblingStatus).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "superseded", legacySiblingStatus)

	edges, err := semanticRepo.ListSemanticEdges(ctx, teamID, 20)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, upgraded.Relationship.RelationshipID, edges[0].RelationshipID)
}

func TestV2SemanticDelayedOlderOneVersionDoesNotSupersedeNewerManyRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupV2LedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createV2LedgerTeam(t, adminDB, rls, "one-cardinality-reversal-team")
	ownerID := createV2LedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewV2LedgerRepository(appDB, rls)
	semanticRepo := NewV2SemanticRepository(appDB, rls)

	denseMem := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	neo4j := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "Neo4j")
	redis := createV2SemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "Redis")
	predicateKey := "runtime_policy_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	require.NoError(t, insertV2PolicyReversalPredicate(ctx, adminDB, rls, predicateKey))

	postgresIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"policy reversal postgres", "Dense-Mem uses PostgreSQL as a durable store.")
	postgresUse := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         postgresIngest.IngestID,
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     predicateKey,
		PredicateVersion: 2,
		ObjectEntityID:   postgres.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     postgresIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:policy-reversal-postgres",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses PostgreSQL as a durable store."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "many", postgresUse.Relationship.CurrentCardinality)

	neo4jIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"policy reversal neo4j", "Neo4j remains legacy migration input.")
	neo4jUse := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         neo4jIngest.IngestID,
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     predicateKey,
		PredicateVersion: 2,
		ObjectEntityID:   neo4j.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     neo4jIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:policy-reversal-neo4j",
			SpanStart:      0,
			SpanEnd:        len("Neo4j remains legacy migration input."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "many", neo4jUse.Relationship.CurrentCardinality)

	delayedIngest := createV2SemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"delayed old redis", "Dense-Mem used Redis as its runtime memory store.")
	delayed := applyV2SemanticDecision(t, ctx, semanticRepo, V2ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        delayedIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    predicateKey,
		ObjectEntityID:  redis.EntityID,
		Support: &V2EvidenceSupportInput{
			FragmentID:     delayedIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:delayed-old-redis",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem used Redis as its runtime memory store."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "one", delayed.Relationship.CurrentCardinality)

	statuses := loadV2RelationshipStatuses(t, ctx, appDB, rls, teamID, ownerID,
		postgresUse.Relationship.RelationshipID,
		neo4jUse.Relationship.RelationshipID,
		delayed.Relationship.RelationshipID,
	)
	assert.Equal(t, "active", statuses[postgresUse.Relationship.RelationshipID])
	assert.Equal(t, "active", statuses[neo4jUse.Relationship.RelationshipID])
	assert.Equal(t, "active", statuses[delayed.Relationship.RelationshipID])
}

func createV2SemanticSourceIngest(
	t *testing.T,
	ctx context.Context,
	repo *V2LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	content string,
	source *V2SourceRevisionResult,
) *V2CreateIngestResult {
	t.Helper()
	result, err := repo.CreateIngest(ctx, V2CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []V2EvidenceInput{{
			Content:          content,
			SourceID:         source.SourceID,
			SourceRevisionID: source.SourceRevisionID,
		}},
	})
	require.NoError(t, err)
	require.Len(t, result.Evidence, 1)
	return result
}

func v2SemanticSupportFromSource(
	fragmentID string,
	source *V2SourceRevisionResult,
	sourceGroupKey string,
	content string,
) *V2EvidenceSupportInput {
	support := v2SemanticSupport(sourceGroupKey, content)
	support.FragmentID = fragmentID
	support.SourceID = source.SourceID
	support.SourceRevisionID = source.SourceRevisionID
	return support
}

func v2SemanticSupport(sourceGroupKey string, content string) *V2EvidenceSupportInput {
	return &V2EvidenceSupportInput{
		SourceGroupKey: sourceGroupKey,
		SpanStart:      0,
		SpanEnd:        len(content),
		Authority:      "primary",
	}
}

func loadV2RelationshipStatuses(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	teamID string,
	ownerID string,
	relationshipIDs ...string,
) map[string]string {
	t.Helper()
	statuses := make(map[string]string, len(relationshipIDs))
	err := rls.WithTeamProfileTx(ctx, db, teamID, ownerID, func(tx *gorm.DB) error {
		for _, relationshipID := range relationshipIDs {
			var status string
			if err := tx.Raw(`
				SELECT status
				FROM relationship_records
				WHERE team_id = ?::uuid
				  AND relationship_id = ?::uuid
			`, teamID, relationshipID).Scan(&status).Error; err != nil {
				return err
			}
			statuses[relationshipID] = status
		}
		return nil
	})
	require.NoError(t, err)
	return statuses
}

func insertV2CardinalityUpgradePredicate(
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithMigrationTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	predicateKey string,
) error {
	return rls.WithMigrationTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO predicate_definitions (
			    predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
			    relationship_kind, current_cardinality
			) VALUES
			    (
			        ?, 1, ARRAY[]::text[],
			        ARRAY['project','product','organization','other']::text[],
			        ARRAY['project','product','concept','other']::text[],
			        'state', 'many'
			    ),
			    (
			        ?, 2, ARRAY['primary_runtime']::text[],
			        ARRAY['project','product','organization','other']::text[],
			        ARRAY['project','product','concept','other']::text[],
			        'state', 'one'
			    )
		`, predicateKey, predicateKey).Error
	})
}

func insertV2PolicyReversalPredicate(
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithMigrationTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	predicateKey string,
) error {
	return rls.WithMigrationTx(ctx, db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO predicate_definitions (
			    predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
			    relationship_kind, current_cardinality
			) VALUES
			    (
			        ?, 1, ARRAY[]::text[],
			        ARRAY['project','product','organization','other']::text[],
			        ARRAY['project','product','concept','other']::text[],
			        'state', 'one'
			    ),
			    (
			        ?, 2, ARRAY['runtime_component']::text[],
			        ARRAY['project','product','organization','other']::text[],
			        ARRAY['project','product','concept','other']::text[],
			        'state', 'many'
			    )
		`, predicateKey, predicateKey).Error
	})
}
