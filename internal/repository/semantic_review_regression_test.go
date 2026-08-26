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

func TestSemanticSupportMustMatchSuppliedIngest(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "semantic-support-ingest-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	sourceOne, err := ledgerRepo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKey:      "doc://ingest-one",
		RevisionToken:  "rev-1",
		ContentHash:    "sha256:one",
	})
	require.NoError(t, err)
	sourceTwo, err := ledgerRepo.AdvanceSourceRevision(ctx, AdvanceSourceRevisionInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		SourceKey:      "doc://ingest-two",
		RevisionToken:  "rev-1",
		ContentHash:    "sha256:two",
	})
	require.NoError(t, err)

	firstIngest := createSemanticSourceIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"Dense-Mem uses PostgreSQL.", "doc://ingest-one", "sha256:one")
	secondIngest := createSemanticSourceIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"Dense-Mem used GraphDB.", "doc://ingest-two", "sha256:two")
	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")

	_, err = semanticRepo.ApplyRelationshipDecision(ctx, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: semanticSupportFromSource(
			secondIngest.Evidence[0].FragmentID, sourceTwo, "doc://ingest-two", "Dense-Mem used GraphDB.",
		),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSemanticOwnerMismatch), err)

	_, err = semanticRepo.ApplyRelationshipDecision(ctx, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: semanticSupportFromSource(
			firstIngest.Evidence[0].FragmentID, sourceTwo, "doc://ingest-two", "Dense-Mem uses PostgreSQL.",
		),
	})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrSemanticOwnerMismatch), err)

	matching := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        firstIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    "uses",
		ObjectEntityID:  postgres.EntityID,
		Support: semanticSupportFromSource(
			firstIngest.Evidence[0].FragmentID, sourceOne, "doc://ingest-one", "Dense-Mem uses PostgreSQL.",
		),
	})
	require.NotNil(t, matching.Relationship)
	assert.NotEmpty(t, matching.SupportID)
}

func TestSemanticOneCardinalityUpgradeSupersedesLegacyManyRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "one-cardinality-upgrade-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "GraphDB")
	predicateKey := "sole_runtime_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	require.NoError(t, insertCardinalityUpgradePredicate(ctx, adminDB, rls, predicateKey))

	postgresIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, "uses postgres", "Dense-Mem uses PostgreSQL.")
	postgresUse := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        postgresIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    predicateKey,
		ObjectEntityID:  postgres.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     postgresIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:uses-postgres",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses PostgreSQL."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "many", postgresUse.Relationship.CurrentCardinality)

	graphdbIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID, "uses graphdb", "Dense-Mem uses GraphDB.")
	graphdbUse := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        graphdbIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    predicateKey,
		ObjectEntityID:  graphdb.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     graphdbIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:uses-graphdb",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses GraphDB."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "many", graphdbUse.Relationship.CurrentCardinality)

	upgradeIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"uses postgres as sole database", "Dense-Mem uses PostgreSQL as its sole durable database.")
	upgraded := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         upgradeIngest.IngestID,
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     predicateKey,
		PredicateVersion: 2,
		ObjectEntityID:   postgres.EntityID,
		Support: &EvidenceSupportInput{
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
		`, teamID, graphdbUse.Relationship.RelationshipID).Scan(&legacySiblingStatus).Error
	})
	require.NoError(t, err)
	assert.Equal(t, "superseded", legacySiblingStatus)

	edges, err := semanticRepo.ListSemanticEdges(ctx, teamID, 20)
	require.NoError(t, err)
	require.Len(t, edges, 1)
	assert.Equal(t, upgraded.Relationship.RelationshipID, edges[0].RelationshipID)
}

func TestSemanticDelayedOlderOneVersionDoesNotSupersedeNewerManyRows(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "one-cardinality-reversal-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledgerRepo := NewLedgerRepository(appDB, rls)
	semanticRepo := NewSemanticRepository(appDB, rls)

	denseMem := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "project", "Dense-Mem")
	postgres := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "PostgreSQL")
	graphdb := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "GraphDB")
	redis := createSemanticEntity(t, ctx, semanticRepo, teamID, ownerID, "product", "Redis")
	predicateKey := "runtime_policy_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	require.NoError(t, insertPolicyReversalPredicate(ctx, adminDB, rls, predicateKey))

	postgresIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"policy reversal postgres", "Dense-Mem uses PostgreSQL as a durable store.")
	postgresUse := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         postgresIngest.IngestID,
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     predicateKey,
		PredicateVersion: 2,
		ObjectEntityID:   postgres.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     postgresIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:policy-reversal-postgres",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem uses PostgreSQL as a durable store."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "many", postgresUse.Relationship.CurrentCardinality)

	graphdbIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"policy reversal graphdb", "GraphDB remains legacy migration input.")
	graphdbUse := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:           teamID,
		OwnerProfileID:   ownerID,
		IngestID:         graphdbIngest.IngestID,
		SubjectEntityID:  denseMem.EntityID,
		PredicateKey:     predicateKey,
		PredicateVersion: 2,
		ObjectEntityID:   graphdb.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     graphdbIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:policy-reversal-graphdb",
			SpanStart:      0,
			SpanEnd:        len("GraphDB remains legacy migration input."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "many", graphdbUse.Relationship.CurrentCardinality)

	delayedIngest := createSemanticIngest(t, ctx, ledgerRepo, teamID, ownerID,
		"delayed old redis", "Dense-Mem used Redis as its runtime memory store.")
	delayed := applySemanticDecision(t, ctx, semanticRepo, ApplyRelationshipDecisionInput{
		TeamID:          teamID,
		OwnerProfileID:  ownerID,
		IngestID:        delayedIngest.IngestID,
		SubjectEntityID: denseMem.EntityID,
		PredicateKey:    predicateKey,
		ObjectEntityID:  redis.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID:     delayedIngest.Evidence[0].FragmentID,
			SourceGroupKey: "conversation:delayed-old-redis",
			SpanStart:      0,
			SpanEnd:        len("Dense-Mem used Redis as its runtime memory store."),
			Authority:      "primary",
		},
	})
	require.Equal(t, "one", delayed.Relationship.CurrentCardinality)

	statuses := loadRelationshipStatuses(t, ctx, appDB, rls, teamID, ownerID,
		postgresUse.Relationship.RelationshipID,
		graphdbUse.Relationship.RelationshipID,
		delayed.Relationship.RelationshipID,
	)
	assert.Equal(t, "active", statuses[postgresUse.Relationship.RelationshipID])
	assert.Equal(t, "active", statuses[graphdbUse.Relationship.RelationshipID])
	assert.Equal(t, "active", statuses[delayed.Relationship.RelationshipID])
}

func createSemanticSourceIngest(
	t *testing.T,
	ctx context.Context,
	repo *LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	content string,
	sourceKey string,
	sourceRevisionContentHash string,
) *EvidenceIngestResult {
	t.Helper()
	result, err := createTestIngest(ctx, repo, CreateIngestInput{
		TeamID:         teamID,
		OwnerProfileID: ownerID,
		Evidence: []EvidenceInput{{
			Content:                   content,
			SourceKey:                 sourceKey,
			SourceRevisionToken:       "rev-1",
			SourceRevisionContentHash: sourceRevisionContentHash,
		}},
	})
	require.NoError(t, err)
	require.Len(t, result.Evidence, 1)
	return result
}

func semanticSupportFromSource(
	fragmentID string,
	source *SourceRevisionResult,
	sourceGroupKey string,
	content string,
) *EvidenceSupportInput {
	support := semanticSupport(sourceGroupKey, content)
	support.FragmentID = fragmentID
	support.SourceID = source.SourceID
	support.SourceRevisionID = source.SourceRevisionID
	return support
}

func semanticSupport(sourceGroupKey string, content string) *EvidenceSupportInput {
	return &EvidenceSupportInput{
		SourceGroupKey: sourceGroupKey,
		SpanStart:      0,
		SpanEnd:        len(content),
		Authority:      "primary",
	}
}

func loadRelationshipStatuses(
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

func insertCardinalityUpgradePredicate(
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	predicateKey string,
) error {
	return rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
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

func insertPolicyReversalPredicate(
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithSystemTx(context.Context, *gorm.DB, func(*gorm.DB) error) error
	},
	predicateKey string,
) error {
	return rls.WithSystemTx(ctx, db, func(tx *gorm.DB) error {
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
