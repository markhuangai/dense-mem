package communityservice

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"gorm.io/gorm"
)

// communityLocker acquires a per-profile Postgres advisory transaction lock
// before invoking fn. The lock key is hashtext('community:'||profileID), so
// concurrent Leiden runs for the same profile are serialised while different
// profiles never contend on the same Postgres lock slot.
//
// Profile isolation invariant: profileID is always embedded in the lock key,
// ensuring community-0 in profile-A and community-0 in profile-B hash to
// different int4 values and never contend.
type communityLocker interface {
	WithCommunityLock(ctx context.Context, profileID string, fn func(ctx context.Context) error) error
}

// leidenQuerier abstracts the GDS procedure calls used by leidenServiceImpl.
//
// This intermediate interface exists because neo4j.ManagedTransaction contains
// an unexported legacy() method, making it impossible to implement in test
// packages. Unit tests inject a stub instead of a real Neo4j connection.
type leidenQuerier interface {
	// EstimateProjection calls gds.graph.project.estimate and returns the
	// projected node count. The graph is never actually created on disk.
	EstimateProjection(ctx context.Context, profileID, graphName string) (int64, error)

	// ProjectGraph creates a GDS in-memory graph named graphName scoped to
	// same-profile SourceFragment, Claim, and Fact nodes only.
	ProjectGraph(ctx context.Context, profileID, graphName string) error

	// ToUndirected mutates the projected graph to add undirected relationships
	// suitable for algorithms like Leiden that reject directed graphs.
	ToUndirected(ctx context.Context, graphName string) error

	// RunLeiden runs gds.leiden.write against graphName, writing community
	// membership to each node's community_id property.
	RunLeiden(ctx context.Context, graphName string, opts DetectOptions) error

	// DropGraph drops the named GDS projected graph (failIfMissing=false).
	// Concurrent or repeated drops are safe.
	DropGraph(ctx context.Context, graphName string) error
}

const (
	defaultDetectGamma     = 1.0
	defaultDetectMaxLevels = 10
)

// leidenServiceImpl is the production implementation of DetectCommunityService
// backed by Postgres advisory locks and Neo4j GDS.
type leidenServiceImpl struct {
	locker  communityLocker
	querier leidenQuerier
	store   communitySummaryStore
	cfg     config.ConfigProvider
	logger  *slog.Logger
}

// Compile-time check that leidenServiceImpl satisfies DetectCommunityService.
var _ DetectCommunityService = (*leidenServiceImpl)(nil)

// NewLeidenService constructs a DetectCommunityService backed by Postgres
// advisory locks (db) and Neo4j GDS (neo4jClient).
//
// Profile isolation: every GDS operation is scoped to a graph name that
// embeds profileID, and Cypher projections filter by n.team_id = $profileId
// so communities from different profiles are never mixed.
func NewLeidenService(
	db *gorm.DB,
	neo4jClient gdsClient,
	cfg config.ConfigProvider,
	logger *slog.Logger,
) DetectCommunityService {
	return &leidenServiceImpl{
		locker:  &pgCommunityLocker{db: db},
		querier: &neo4jLeidenQuerier{client: neo4jClient},
		store:   newNeo4jCommunityStore(neo4jClient),
		cfg:     cfg,
		logger:  logger,
	}
}

func isEmptyCommunityProjectionError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "returned no nodes")
}

// Detect runs the Leiden community detection algorithm for profileID's
// knowledge graph.
//
// Step sequence:
//  1. Acquire a per-profile Postgres advisory transaction lock.
//  2. Estimate the GDS projection size against the AI_COMMUNITY_MAX_NODES cap.
//  3. Clear persisted summaries and return when the profile graph is empty.
//  4. Reject with ErrCommunityGraphTooLarge when the cap is exceeded.
//  5. Project only same-profile SourceFragment|Claim|Fact nodes and their
//     same-profile relationships into a named GDS in-memory graph.
//  6. Defer gds.graph.drop so the projection is always released on return.
//  7. Mutate the projected graph to an undirected relationship set.
//  8. Run gds.leiden.write, writing community_id to each projected node.
func normalizeDetectOptions(opts DetectOptions) DetectOptions {
	normalized := opts
	if normalized.Gamma == 0 {
		normalized.Gamma = defaultDetectGamma
	}
	if normalized.MaxLevels == 0 {
		normalized.MaxLevels = defaultDetectMaxLevels
	}
	return normalized
}

func (s *leidenServiceImpl) Detect(ctx context.Context, profileID string, opts DetectOptions) error {
	graphName := GraphNamePrefix + profileID + "-leiden"
	normalized := normalizeDetectOptions(opts)

	return s.locker.WithCommunityLock(ctx, profileID, func(ctx context.Context) error {
		clearCommunities := func() error {
			if s.store == nil {
				return nil
			}
			if err := s.store.Replace(ctx, profileID, nil); err != nil {
				return fmt.Errorf("community summary replace (profile=%s): %w", profileID, err)
			}
			return nil
		}

		// --- 1. Estimate ---
		nodeCount, err := s.querier.EstimateProjection(ctx, profileID, graphName)
		if err != nil {
			return fmt.Errorf("leiden estimate projection (profile=%s): %w", profileID, err)
		}

		if nodeCount == 0 {
			return clearCommunities()
		}

		maxNodes := s.cfg.GetAICommunityMaxNodes()
		if nodeCount > int64(maxNodes) {
			if s.logger != nil {
				s.logger.Warn("leiden: community graph exceeds max nodes cap",
					slog.String("profileID", profileID),
					slog.Int64("nodeCount", nodeCount),
					slog.Int("maxNodes", maxNodes),
				)
			}
			return fmt.Errorf("leiden detect (profile=%s nodeCount=%d maxNodes=%d): %w",
				profileID, nodeCount, maxNodes, ErrCommunityGraphTooLarge)
		}

		// --- 2. Project ---
		if err := s.querier.ProjectGraph(ctx, profileID, graphName); err != nil {
			if isEmptyCommunityProjectionError(err) {
				return clearCommunities()
			}
			return fmt.Errorf("leiden project graph (profile=%s): %w", profileID, err)
		}

		// --- 3. Always drop the in-memory graph, even on leiden write failure ---
		defer func() {
			if dropErr := s.querier.DropGraph(ctx, graphName); dropErr != nil {
				if s.logger != nil {
					s.logger.Error("leiden: failed to drop GDS projected graph",
						slog.String("graphName", graphName),
						slog.String("profileID", profileID),
						slog.String("error", dropErr.Error()),
					)
				}
			}
		}()

		// --- 4. Convert the projected graph to an undirected topology for Leiden ---
		if err := s.querier.ToUndirected(ctx, graphName); err != nil {
			return fmt.Errorf("leiden make undirected (profile=%s): %w", profileID, err)
		}

		// --- 5. Run Leiden write ---
		if err := s.querier.RunLeiden(ctx, graphName, normalized); err != nil {
			return fmt.Errorf("leiden write (profile=%s): %w", profileID, err)
		}

		if s.store != nil {
			inputs, err := s.store.LoadSummaryInputs(ctx, profileID)
			if err != nil {
				return fmt.Errorf("community summary inputs (profile=%s): %w", profileID, err)
			}
			communities := buildCommunitySummaries(profileID, inputs, time.Now().UTC())
			if err := s.store.Replace(ctx, profileID, communities); err != nil {
				return fmt.Errorf("community summary replace (profile=%s): %w", profileID, err)
			}
		}

		return nil
	})
}

// ---------------------------------------------------------------------------
// pgCommunityLocker — production communityLocker backed by Postgres
// ---------------------------------------------------------------------------

// pgCommunityLocker implements communityLocker using a Postgres advisory
// transaction lock. The lock is automatically released when the enclosing
// transaction commits or rolls back — no manual unlock step is required.
type pgCommunityLocker struct {
	db *gorm.DB
}

// WithCommunityLock acquires a Postgres advisory transaction lock for
// profileID then invokes fn inside the same SQL transaction.
//
// Lock key: hashtext('community:' || profileID). Parameter binding prevents
// SQL injection: the key string is concatenated in Go and passed as a single
// bound parameter to hashtext(), never interpolated into the SQL template.
func (l *pgCommunityLocker) WithCommunityLock(
	ctx context.Context,
	profileID string,
	fn func(ctx context.Context) error,
) error {
	key := "community:" + profileID
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(hashtext(?))", key).Error; err != nil {
			return fmt.Errorf("community advisory lock acquire (profile=%s): %w", profileID, err)
		}
		return fn(ctx)
	})
}

// ---------------------------------------------------------------------------
// neo4jLeidenQuerier — production leidenQuerier backed by Neo4j GDS
// ---------------------------------------------------------------------------

// Cypher queries used by neo4jLeidenQuerier are declared as constants so they
// are visible to reviewers and auditable for team_id filter presence.
// Both queries carry $profileId as a GDS parameter, enforcing the profile
// isolation invariant at the database level.
const (
	// leidenNodeQuery selects SourceFragment, Claim, and Fact nodes that
	// belong to the given profile. Passed as the nodeQuery argument to GDS
	// projection procedures; $profileId is supplied via the parameters map.
	leidenNodeQuery = "MATCH (n) WHERE n.team_id = $profileId AND (n:SourceFragment OR n:Claim OR n:Fact) RETURN id(n) AS id"

	// leidenRelQuery selects only relationships whose source and target both
	// belong to the same profile, preventing cross-profile edges from leaking
	// into the projection. Community detection only depends on connectivity,
	// so the relationship type is normalized for later undirected mutation.
	leidenRelQuery = "MATCH (s)-[r]->(t) WHERE s.team_id = $profileId AND t.team_id = $profileId RETURN id(s) AS source, id(t) AS target, 'RELATED' AS type"
)

// neo4jLeidenQuerier implements leidenQuerier using a real Neo4j GDS client.
type neo4jLeidenQuerier struct {
	client gdsClient
}

// Compile-time check that neo4jLeidenQuerier satisfies leidenQuerier.
var _ leidenQuerier = (*neo4jLeidenQuerier)(nil)

// EstimateProjection calls gds.graph.project.estimate to obtain the projected
// node count without creating the in-memory graph.
func (q *neo4jLeidenQuerier) EstimateProjection(ctx context.Context, profileID, graphName string) (int64, error) {
	raw, err := q.client.ExecuteRead(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, runErr := tx.Run(ctx,
			`CALL gds.graph.project.cypher.estimate(
				$nodeQuery,
				$relQuery,
				{parameters: {profileId: $profileId}}
			)
			YIELD nodeCount`,
			map[string]any{
				"graphName": graphName,
				"nodeQuery": leidenNodeQuery,
				"relQuery":  leidenRelQuery,
				"profileId": profileID,
			},
		)
		if runErr != nil {
			return nil, runErr
		}
		if result.Next(ctx) {
			v, ok := result.Record().Get("nodeCount")
			if !ok {
				return int64(0), nil
			}
			return v, nil
		}
		if err := result.Err(); err != nil {
			return nil, err
		}
		return int64(0), nil
	})
	if err != nil {
		return 0, err
	}
	if raw == nil {
		return 0, nil
	}
	if count, ok := raw.(int64); ok {
		return count, nil
	}
	return 0, nil
}

// ProjectGraph creates the GDS in-memory graph named graphName containing
// only same-profile SourceFragment, Claim, and Fact nodes and relationships.
func (q *neo4jLeidenQuerier) ProjectGraph(ctx context.Context, profileID, graphName string) error {
	_, err := q.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, runErr := tx.Run(ctx,
			`CALL gds.graph.project.cypher(
				$graphName,
				$nodeQuery,
				$relQuery,
				{parameters: {profileId: $profileId}}
			)
			YIELD nodeCount, relationshipCount`,
			map[string]any{
				"graphName": graphName,
				"nodeQuery": leidenNodeQuery,
				"relQuery":  leidenRelQuery,
				"profileId": profileID,
			},
		)
		if runErr != nil {
			return nil, runErr
		}
		_, consumeErr := result.Consume(ctx)
		return nil, consumeErr
	})
	return err
}

// ToUndirected mutates the synthetic RELATED edges into an undirected
// relationship set so Leiden can run on the projected graph.
func (q *neo4jLeidenQuerier) ToUndirected(ctx context.Context, graphName string) error {
	_, err := q.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, runErr := tx.Run(ctx,
			`CALL gds.graph.relationships.toUndirected(
				$graphName,
				{
					relationshipType: 'RELATED',
					mutateRelationshipType: 'RELATED_UNDIRECTED'
				}
			)
			YIELD inputRelationships, relationshipsWritten`,
			map[string]any{"graphName": graphName},
		)
		if runErr != nil {
			return nil, runErr
		}
		_, consumeErr := result.Consume(ctx)
		return nil, consumeErr
	})
	return err
}

// RunLeiden executes gds.leiden.write against graphName, writing community
// membership to the community_id property on each projected node.
func (q *neo4jLeidenQuerier) RunLeiden(ctx context.Context, graphName string, opts DetectOptions) error {
	config := map[string]any{
		"writeProperty":     "community_id",
		"relationshipTypes": []string{"RELATED_UNDIRECTED"},
		"gamma":             opts.Gamma,
		"maxLevels":         opts.MaxLevels,
	}
	if opts.Tolerance > 0 {
		config["tolerance"] = opts.Tolerance
	}

	_, err := q.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, runErr := tx.Run(ctx,
			`CALL gds.leiden.write(
				$graphName,
				$config
			)
			YIELD communityCount, nodeCount`,
			map[string]any{
				"graphName": graphName,
				"config":    config,
			},
		)
		if runErr != nil {
			return nil, runErr
		}
		_, consumeErr := result.Consume(ctx)
		return nil, consumeErr
	})
	return err
}

// DropGraph drops the named GDS projected graph using failIfMissing=false so
// concurrent or repeated drops never return an error.
func (q *neo4jLeidenQuerier) DropGraph(ctx context.Context, graphName string) error {
	_, err := q.client.ExecuteWrite(ctx, func(tx neo4j.ManagedTransaction) (any, error) {
		result, runErr := tx.Run(ctx,
			"CALL gds.graph.drop($name, false) YIELD graphName RETURN graphName",
			map[string]any{"name": graphName},
		)
		if runErr != nil {
			return nil, runErr
		}
		_, consumeErr := result.Consume(ctx)
		return nil, consumeErr
	})
	return err
}
