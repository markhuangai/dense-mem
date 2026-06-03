package neo4j

import (
	"context"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

// Canonical index names for Neo4j schema elements.
const (
	IndexFragmentContent   = "fragment_content_idx"
	IndexFragmentEmbedding = "fragment_embedding_idx"
	IndexFactPredicate     = "fact_predicate_idx"
	IndexFactRecall        = "fact_recall_idx"
	IndexClaimRecall       = "claim_recall_idx"

	// Composite indexes for fragment deduplication and lookup (Unit 12)
	IndexFragmentProfileIdempotency = "fragment_team_idempotency_idx"
	IndexFragmentProfileContentHash = "fragment_profile_content_hash_idx"
	IndexFragmentProfileCreatedAt   = "fragment_profile_created_at_idx"
	IndexFragmentOwnerIdempotency   = "fragment_owner_idempotency_idx"
	IndexFragmentOwnerContentHash   = "fragment_owner_content_hash_idx"

	// Composite indexes for Claim nodes — team_id is leading key (Unit 12, AC-3)
	IndexClaimProfileClaimID          = "claim_profile_claim_id_idx"
	IndexClaimProfileStatus           = "claim_profile_status_idx"
	IndexClaimProfilePredicate        = "claim_profile_predicate_idx"
	IndexClaimProfileSubjectPredicate = "claim_profile_subject_predicate_idx"
	IndexClaimProfileIdempotency      = "claim_team_idempotency_idx"
	IndexClaimProfileContentHash      = "claim_profile_content_hash_idx"
	IndexClaimProfileRecordedAt       = "claim_profile_recorded_at_idx"
	IndexClaimOwnerIdempotency        = "claim_owner_idempotency_idx"
	IndexClaimOwnerContentHash        = "claim_owner_content_hash_idx"

	// Composite indexes for Fact nodes — team_id is leading key (Unit 12, AC-4)
	IndexFactProfileStatus                 = "fact_profile_status_idx"
	IndexFactProfileSubjectPredicateStatus = "fact_profile_subject_predicate_status_idx"
	IndexFactProfileRecordedAt             = "fact_profile_recorded_at_idx"

	// Composite index for SourceFragment nodes — team_id is leading key (Unit 12, AC-5)
	IndexSourceFragmentProfileStatus = "sourcefragment_profile_status_idx"
	// Persisted community summary lookups.
	IndexCommunityProfileCommunityID = "community_profile_community_id_idx"

	// Relationship team_id existence constraints (Unit 13, AC-X1)
	// These names are canonical identifiers stored in Neo4j metadata.
	ConstraintSupportedByProfileIDExists  = "supported_by_team_id_exists"
	ConstraintPromotesToProfileIDExists   = "promotes_to_team_id_exists"
	ConstraintSupersededByProfileIDExists = "superseded_by_team_id_exists"
	ConstraintContradictsProfileIDExists  = "contradicts_team_id_exists"
	ConstraintOverlaysProfileIDExists     = "overlays_team_id_exists"
	ConstraintAlignsWithProfileIDExists   = "aligns_with_team_id_exists"
)

// SchemaBootstrapperInterface is the companion interface for SchemaBootstrapper.
// Consumers and tests depend on this abstraction rather than the concrete struct.
type SchemaBootstrapperInterface interface {
	EnsureSchema(ctx context.Context) error
}

// SchemaBootstrapper creates Neo4j schema elements (constraints, indexes).
// It is idempotent - re-running against an existing database must not error.
type SchemaBootstrapper struct {
	client              Neo4jClientInterface
	embeddingDimensions int
	logger              observability.LogProvider
}

// Ensure SchemaBootstrapper implements SchemaBootstrapperInterface
var _ SchemaBootstrapperInterface = (*SchemaBootstrapper)(nil)

// NewSchemaBootstrapper creates a new schema bootstrapper.
func NewSchemaBootstrapper(client Neo4jClientInterface, embeddingDimensions int, logger observability.LogProvider) *SchemaBootstrapper {
	return &SchemaBootstrapper{
		client:              client,
		embeddingDimensions: embeddingDimensions,
		logger:              logger,
	}
}

func isUnsupportedRelationshipConstraintError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "property existence constraint requires neo4j enterprise edition") ||
		strings.Contains(msg, "relationship property existence constraints are not allowed") ||
		strings.Contains(msg, "unsupported administration command")
}

// EnsureSchema creates all required constraints and indexes if they don't exist.
// All CREATE statements use IF NOT EXISTS for idempotency.
func (s *SchemaBootstrapper) EnsureSchema(ctx context.Context) error {
	s.logger.Info("Ensuring Neo4j schema exists")

	edition, err := DetectEdition(ctx, s.client)
	if err != nil {
		s.logger.Warn("Neo4j edition probe failed; continuing with community-safe schema bootstrap",
			observability.String("error", err.Error()),
		)
		edition = EditionUnknown
	} else {
		s.logger.Info("Detected Neo4j edition", observability.String("edition", string(edition)))
	}

	if err := s.backfillLegacyTeamID(ctx); err != nil {
		return err
	}

	// Create unique constraints
	constraints := []string{
		"CREATE CONSTRAINT sourcefragment_fragment_id_unique IF NOT EXISTS FOR (sf:SourceFragment) REQUIRE sf.fragment_id IS UNIQUE",
		"CREATE CONSTRAINT claim_claim_id_unique IF NOT EXISTS FOR (c:Claim) REQUIRE c.claim_id IS UNIQUE",
		"CREATE CONSTRAINT fact_fact_id_unique IF NOT EXISTS FOR (f:Fact) REQUIRE f.fact_id IS UNIQUE",
	}

	for _, cypher := range constraints {
		_, err := s.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			_, err := tx.Run(ctx, cypher, nil)
			return nil, err
		})
		if err != nil {
			return fmt.Errorf("failed to create constraint: %w", err)
		}
		s.logger.Debug("Created constraint", observability.String("query", cypher))
	}

	// Create relationship team_id existence constraints (Unit 13, AC-X1).
	// team_id is required on all pipeline edges so that no relationship can
	// escape profile isolation if a node-level filter is accidentally omitted.
	if edition == EditionEnterprise {
		relationshipConstraints := []struct {
			cypher string
			name   string
		}{
			{
				"CREATE CONSTRAINT supported_by_team_id_exists IF NOT EXISTS FOR ()-[r:SUPPORTED_BY]-() REQUIRE r.team_id IS NOT NULL",
				ConstraintSupportedByProfileIDExists,
			},
			{
				"CREATE CONSTRAINT promotes_to_team_id_exists IF NOT EXISTS FOR ()-[r:PROMOTES_TO]-() REQUIRE r.team_id IS NOT NULL",
				ConstraintPromotesToProfileIDExists,
			},
			{
				"CREATE CONSTRAINT superseded_by_team_id_exists IF NOT EXISTS FOR ()-[r:SUPERSEDED_BY]-() REQUIRE r.team_id IS NOT NULL",
				ConstraintSupersededByProfileIDExists,
			},
			{
				"CREATE CONSTRAINT contradicts_team_id_exists IF NOT EXISTS FOR ()-[r:CONTRADICTS]-() REQUIRE r.team_id IS NOT NULL",
				ConstraintContradictsProfileIDExists,
			},
			{
				"CREATE CONSTRAINT overlays_team_id_exists IF NOT EXISTS FOR ()-[r:OVERLAYS]-() REQUIRE r.team_id IS NOT NULL",
				ConstraintOverlaysProfileIDExists,
			},
			{
				"CREATE CONSTRAINT aligns_with_team_id_exists IF NOT EXISTS FOR ()-[r:ALIGNS_WITH]-() REQUIRE r.team_id IS NOT NULL",
				ConstraintAlignsWithProfileIDExists,
			},
		}

		for _, rc := range relationshipConstraints {
			_, err := s.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
				_, err := tx.Run(ctx, rc.cypher, nil)
				return nil, err
			})
			if err != nil {
				if isUnsupportedRelationshipConstraintError(err) {
					s.logger.Warn(
						"skipping relationship team_id constraint; unsupported by connected Neo4j deployment",
						observability.String("name", rc.name),
						observability.String("error", err.Error()),
					)
					continue
				}
				return fmt.Errorf("failed to create relationship constraint %s: %w", rc.name, err)
			}
			s.logger.Debug("Created relationship constraint", observability.String("name", rc.name))
		}
	} else {
		s.logger.Info("Skipping enterprise-only relationship constraints",
			observability.String("edition", string(edition)),
		)
	}

	// Create team_id indexes
	indexes := []string{
		"CREATE INDEX sourcefragment_team_id_idx IF NOT EXISTS FOR (sf:SourceFragment) ON (sf.team_id)",
		"CREATE INDEX claim_team_id_idx IF NOT EXISTS FOR (c:Claim) ON (c.team_id)",
		"CREATE INDEX fact_team_id_idx IF NOT EXISTS FOR (f:Fact) ON (f.team_id)",
		"CREATE INDEX community_team_id_idx IF NOT EXISTS FOR (c:Community) ON (c.team_id)",
	}

	for _, cypher := range indexes {
		_, err := s.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			_, err := tx.Run(ctx, cypher, nil)
			return nil, err
		})
		if err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
		s.logger.Debug("Created index", observability.String("query", cypher))
	}

	// Drop legacy indexes before creating canonical ones.
	// Also drops fact_predicate_idx by canonical name so it can be recreated
	// if a prior deployment created it with incorrect configuration.
	legacyDrops := []string{
		"DROP INDEX sourcefragment_content IF EXISTS",
		"DROP INDEX sourcefragment_embedding IF EXISTS",
		"DROP INDEX fact_predicate IF EXISTS",
		"DROP INDEX fact_predicate_idx IF EXISTS",
		"DROP INDEX fact_recall_idx IF EXISTS",
		"DROP INDEX claim_recall_idx IF EXISTS",
	}

	for _, cypher := range legacyDrops {
		_, err := s.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			_, err := tx.Run(ctx, cypher, nil)
			return nil, err
		})
		if err != nil {
			// Soft-fail: legacy drops are opportunistic cleanups, the index may not exist
			s.logger.Debug("could not drop legacy index (may not exist)", observability.String("query", cypher), observability.String("error", err.Error()))
			continue
		}
		s.logger.Debug("Dropped legacy index", observability.String("query", cypher))
	}

	// Create full-text indexes with canonical names
	fullTextIndexes := []struct {
		cypher string
		name   string
	}{
		{
			"CREATE FULLTEXT INDEX fragment_content_idx IF NOT EXISTS FOR (sf:SourceFragment) ON EACH [sf.content]",
			"fragment_content_idx",
		},
		{
			"CREATE FULLTEXT INDEX fact_predicate_idx IF NOT EXISTS FOR (f:Fact) ON EACH [f.predicate]",
			"fact_predicate_idx",
		},
		{
			"CREATE FULLTEXT INDEX fact_recall_idx IF NOT EXISTS FOR (f:Fact) ON EACH [f.subject, f.predicate, f.object]",
			"fact_recall_idx",
		},
		{
			"CREATE FULLTEXT INDEX claim_recall_idx IF NOT EXISTS FOR (c:Claim) ON EACH [c.subject, c.predicate, c.object]",
			"claim_recall_idx",
		},
	}

	for _, idx := range fullTextIndexes {
		_, err := s.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			_, err := tx.Run(ctx, idx.cypher, nil)
			return nil, err
		})
		if err != nil {
			return fmt.Errorf("failed to create full-text index: %w", err)
		}
		s.logger.Info("ensured index", observability.String("name", idx.name))
	}

	// Create vector index with canonical name
	vectorIndex := fmt.Sprintf(
		"CREATE VECTOR INDEX fragment_embedding_idx IF NOT EXISTS FOR (sf:SourceFragment) ON sf.embedding OPTIONS {indexConfig: {`vector.dimensions`: %d, `vector.similarity_function`: 'cosine'}}",
		s.embeddingDimensions,
	)

	_, err = s.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
		_, err := tx.Run(ctx, vectorIndex, nil)
		return nil, err
	})
	if err != nil {
		return fmt.Errorf("failed to create vector index: %w", err)
	}
	s.logger.Info("ensured index", observability.String("name", "fragment_embedding_idx"))

	// Create composite indexes for fragment deduplication and lookup (Unit 12)
	// These are ADDITIVE migrations - no DROP of existing indexes.
	// AC-44: Idempotency-key uniqueness scoped to (team_id, idempotency_key)
	// AC-45: Content-hash lookup profile-scoped
	// AC-29: Created-at ordering profile-scoped
	compositeIndexes := []struct {
		cypher string
		name   string
	}{
		{
			"CREATE INDEX fragment_team_idempotency_idx IF NOT EXISTS FOR (sf:SourceFragment) ON (sf.team_id, sf.idempotency_key)",
			"fragment_team_idempotency_idx",
		},
		{
			"CREATE INDEX fragment_profile_content_hash_idx IF NOT EXISTS FOR (sf:SourceFragment) ON (sf.team_id, sf.content_hash)",
			"fragment_profile_content_hash_idx",
		},
		{
			"CREATE INDEX fragment_profile_created_at_idx IF NOT EXISTS FOR (sf:SourceFragment) ON (sf.team_id, sf.created_at)",
			"fragment_profile_created_at_idx",
		},
		{
			"CREATE INDEX fragment_owner_idempotency_idx IF NOT EXISTS FOR (sf:SourceFragment) ON (sf.team_id, sf.owner_profile_id, sf.idempotency_key)",
			IndexFragmentOwnerIdempotency,
		},
		{
			"CREATE INDEX fragment_owner_content_hash_idx IF NOT EXISTS FOR (sf:SourceFragment) ON (sf.team_id, sf.owner_profile_id, sf.content_hash)",
			IndexFragmentOwnerContentHash,
		},
	}

	for _, idx := range compositeIndexes {
		_, err := s.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			_, err := tx.Run(ctx, idx.cypher, nil)
			return nil, err
		})
		if err != nil {
			return fmt.Errorf("failed to create composite index: %w", err)
		}
		s.logger.Info("ensured index", observability.String("name", idx.name))
	}

	// Create claim, fact, and sourcefragment composite indexes (Unit 12)
	// team_id is always the leading key for efficient profile-scoped lookups.
	// AC-3: Claim composite indexes, AC-4: Fact composite indexes, AC-5: SF status index.
	pipelineIndexes := []struct {
		cypher string
		name   string
	}{
		// Claim indexes (AC-3)
		{
			"CREATE INDEX claim_profile_claim_id_idx IF NOT EXISTS FOR (c:Claim) ON (c.team_id, c.claim_id)",
			IndexClaimProfileClaimID,
		},
		{
			"CREATE INDEX claim_profile_status_idx IF NOT EXISTS FOR (c:Claim) ON (c.team_id, c.status)",
			IndexClaimProfileStatus,
		},
		{
			"CREATE INDEX claim_profile_predicate_idx IF NOT EXISTS FOR (c:Claim) ON (c.team_id, c.predicate)",
			IndexClaimProfilePredicate,
		},
		{
			"CREATE INDEX claim_profile_subject_predicate_idx IF NOT EXISTS FOR (c:Claim) ON (c.team_id, c.subject, c.predicate)",
			IndexClaimProfileSubjectPredicate,
		},
		{
			"CREATE INDEX claim_team_idempotency_idx IF NOT EXISTS FOR (c:Claim) ON (c.team_id, c.idempotency_key)",
			IndexClaimProfileIdempotency,
		},
		{
			"CREATE INDEX claim_profile_content_hash_idx IF NOT EXISTS FOR (c:Claim) ON (c.team_id, c.content_hash)",
			IndexClaimProfileContentHash,
		},
		{
			"CREATE INDEX claim_profile_recorded_at_idx IF NOT EXISTS FOR (c:Claim) ON (c.team_id, c.recorded_at, c.claim_id)",
			IndexClaimProfileRecordedAt,
		},
		{
			"CREATE INDEX claim_owner_idempotency_idx IF NOT EXISTS FOR (c:Claim) ON (c.team_id, c.owner_profile_id, c.idempotency_key)",
			IndexClaimOwnerIdempotency,
		},
		{
			"CREATE INDEX claim_owner_content_hash_idx IF NOT EXISTS FOR (c:Claim) ON (c.team_id, c.owner_profile_id, c.content_hash)",
			IndexClaimOwnerContentHash,
		},
		// Fact indexes (AC-4)
		{
			"CREATE INDEX fact_profile_status_idx IF NOT EXISTS FOR (f:Fact) ON (f.team_id, f.status)",
			IndexFactProfileStatus,
		},
		{
			"CREATE INDEX fact_profile_subject_predicate_status_idx IF NOT EXISTS FOR (f:Fact) ON (f.team_id, f.subject, f.predicate, f.status)",
			IndexFactProfileSubjectPredicateStatus,
		},
		{
			"CREATE INDEX fact_profile_recorded_at_idx IF NOT EXISTS FOR (f:Fact) ON (f.team_id, f.recorded_at, f.fact_id)",
			IndexFactProfileRecordedAt,
		},
		// SourceFragment status index (AC-5)
		{
			"CREATE INDEX sourcefragment_profile_status_idx IF NOT EXISTS FOR (sf:SourceFragment) ON (sf.team_id, sf.status)",
			IndexSourceFragmentProfileStatus,
		},
		{
			"CREATE INDEX community_profile_community_id_idx IF NOT EXISTS FOR (c:Community) ON (c.team_id, c.community_id)",
			IndexCommunityProfileCommunityID,
		},
	}

	for _, idx := range pipelineIndexes {
		_, err := s.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			_, err := tx.Run(ctx, idx.cypher, nil)
			return nil, err
		})
		if err != nil {
			return fmt.Errorf("failed to create pipeline index %s: %w", idx.name, err)
		}
		s.logger.Info("ensured index", observability.String("name", idx.name))
	}

	s.logger.Info("Neo4j schema ensured successfully")
	return nil
}

func (s *SchemaBootstrapper) backfillLegacyTeamID(ctx context.Context) error {
	legacyBackfills := []struct {
		name   string
		cypher string
	}{
		{
			name: "legacy_node_team_id",
			cypher: `
MATCH (n)
WHERE n.team_id IS NULL AND n.profile_id IS NOT NULL
SET n.team_id = n.profile_id
RETURN count(n) AS updated`,
		},
		{
			name: "legacy_relationship_team_id",
			cypher: `
MATCH ()-[r]->()
WHERE r.team_id IS NULL AND r.profile_id IS NOT NULL
SET r.team_id = r.profile_id
RETURN count(r) AS updated`,
		},
		{
			name: "legacy_relationship_team_id_from_endpoints",
			cypher: `
MATCH (a)-[r]->(b)
WHERE r.team_id IS NULL
  AND a.team_id IS NOT NULL
  AND b.team_id IS NOT NULL
  AND a.team_id = b.team_id
SET r.team_id = a.team_id
RETURN count(r) AS updated`,
		},
		{
			name: "legacy_sourcefragment_owner",
			cypher: `
MATCH (sf:SourceFragment)
WHERE sf.owner_profile_id IS NULL AND sf.created_by_profile_id IS NOT NULL
SET sf.owner_profile_id = sf.created_by_profile_id,
    sf.owner_profile_name = sf.created_by_profile_name
RETURN count(sf) AS updated`,
		},
		{
			name: "legacy_claim_owner",
			cypher: `
MATCH (c:Claim)
WHERE c.owner_profile_id IS NULL AND c.created_by_profile_id IS NOT NULL
SET c.owner_profile_id = c.created_by_profile_id,
    c.owner_profile_name = c.created_by_profile_name
RETURN count(c) AS updated`,
		},
		{
			name: "legacy_fact_owner",
			cypher: `
MATCH (f:Fact)
WHERE f.owner_profile_id IS NULL
  AND coalesce(f.created_by_profile_id, f.promoted_by_profile_id) IS NOT NULL
SET f.owner_profile_id = coalesce(f.created_by_profile_id, f.promoted_by_profile_id),
    f.owner_profile_name = coalesce(f.created_by_profile_name, f.promoted_by_profile_name)
RETURN count(f) AS updated`,
		},
	}

	for _, backfill := range legacyBackfills {
		_, err := s.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (interface{}, error) {
			result, err := tx.Run(ctx, backfill.cypher, nil)
			if err != nil {
				return nil, err
			}
			_, err = result.Consume(ctx)
			return nil, err
		})
		if err != nil {
			return fmt.Errorf("failed to backfill legacy Neo4j team_id (%s): %w", backfill.name, err)
		}
		s.logger.Debug("backfilled legacy Neo4j team_id", observability.String("name", backfill.name))
	}

	return nil
}
