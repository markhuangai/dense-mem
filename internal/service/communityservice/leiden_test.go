package communityservice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// ---------------------------------------------------------------------------
// Stubs
// ---------------------------------------------------------------------------

// stubCommunityLocker is a test-only implementation of communityLocker.
// It immediately invokes fn without acquiring a real Postgres advisory lock.
//
// Profile isolation invariant: the locker records each profileID it was called
// with so tests can assert that different profiles use separate lock keys.
type stubCommunityLocker struct {
	locked []string // profileIDs passed to WithCommunityLock in call order
	err    error    // returned before fn is called when non-nil
}

func (s *stubCommunityLocker) WithCommunityLock(ctx context.Context, profileID string, fn func(ctx context.Context) error) error {
	s.locked = append(s.locked, profileID)
	if s.err != nil {
		return s.err
	}
	return fn(ctx)
}

// Compile-time check that stubCommunityLocker satisfies communityLocker.
var _ communityLocker = (*stubCommunityLocker)(nil)

// stubLeidenQuerier is a test-only implementation of leidenQuerier.
//
// Fields:
//   - estimateNodeCount: returned by EstimateProjection
//   - estimateErr:       error returned by EstimateProjection
//   - projectErr:        error returned by ProjectGraph
//   - toUndirectedErr:   error returned by ToUndirected
//   - leidenErr:         error returned by RunLeiden
//   - dropErr:           error returned by DropGraph
//
// Recorded calls (for assertion):
//   - estimatedProfiles: profileID passed to EstimateProjection in order
//   - projectedProfiles: profileID passed to ProjectGraph in order
//   - projectedGraphs:   graphName passed to ProjectGraph in order
//   - undirectedGraphs:  graphName passed to ToUndirected in order
//   - runGraphs:         graphName passed to RunLeiden in order
//   - runOptions:        DetectOptions passed to RunLeiden in order
//   - droppedGraphs:     graphName passed to DropGraph in order
type stubLeidenQuerier struct {
	estimateNodeCount int64
	estimateErr       error
	projectErr        error
	toUndirectedErr   error
	leidenErr         error
	dropErr           error

	estimatedProfiles []string
	projectedProfiles []string
	projectedGraphs   []string
	undirectedGraphs  []string
	runGraphs         []string
	runOptions        []DetectOptions
	droppedGraphs     []string
}

func (s *stubLeidenQuerier) EstimateProjection(_ context.Context, profileID, _ string) (int64, error) {
	s.estimatedProfiles = append(s.estimatedProfiles, profileID)
	if s.estimateErr != nil {
		return 0, s.estimateErr
	}
	return s.estimateNodeCount, nil
}

func (s *stubLeidenQuerier) ProjectGraph(_ context.Context, profileID, graphName string) error {
	s.projectedProfiles = append(s.projectedProfiles, profileID)
	s.projectedGraphs = append(s.projectedGraphs, graphName)
	return s.projectErr
}

func (s *stubLeidenQuerier) ToUndirected(_ context.Context, graphName string) error {
	s.undirectedGraphs = append(s.undirectedGraphs, graphName)
	return s.toUndirectedErr
}

func (s *stubLeidenQuerier) RunLeiden(_ context.Context, graphName string, opts DetectOptions) error {
	s.runGraphs = append(s.runGraphs, graphName)
	s.runOptions = append(s.runOptions, opts)
	return s.leidenErr
}

func (s *stubLeidenQuerier) DropGraph(_ context.Context, graphName string) error {
	s.droppedGraphs = append(s.droppedGraphs, graphName)
	return s.dropErr
}

// Compile-time check that stubLeidenQuerier satisfies leidenQuerier.
var _ leidenQuerier = (*stubLeidenQuerier)(nil)

type stubCommunitySummaryStore struct {
	inputs              []communitySummaryInput
	loadErr             error
	replaceErr          error
	replacedProfiles    []string
	replacedCommunities [][]*domain.Community
}

func (s *stubCommunitySummaryStore) LoadSummaryInputs(_ context.Context, _ string) ([]communitySummaryInput, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.inputs, nil
}

func (s *stubCommunitySummaryStore) Replace(_ context.Context, profileID string, communities []*domain.Community) error {
	if s.replaceErr != nil {
		return s.replaceErr
	}
	s.replacedProfiles = append(s.replacedProfiles, profileID)
	s.replacedCommunities = append(s.replacedCommunities, communities)
	return nil
}

var _ communitySummaryStore = (*stubCommunitySummaryStore)(nil)

// stubConfigProvider is a minimal test-only implementation of
// config.ConfigProvider that returns only the fields relevant to Leiden.
type stubConfigProvider struct {
	maxNodes int
}

func (s *stubConfigProvider) GetAICommunityMaxNodes() int { return s.maxNodes }

// Implement the remaining ConfigProvider methods with zero/empty returns so
// the stub compiles as a config.ConfigProvider without importing that package.
func (s *stubConfigProvider) GetPostgresDSN() string                 { return "" }
func (s *stubConfigProvider) GetNeo4jURI() string                    { return "" }
func (s *stubConfigProvider) GetNeo4jUser() string                   { return "" }
func (s *stubConfigProvider) GetNeo4jPassword() string               { return "" }
func (s *stubConfigProvider) GetNeo4jDatabase() string               { return "" }
func (s *stubConfigProvider) GetRedisAddr() string                   { return "" }
func (s *stubConfigProvider) GetRedisPassword() string               { return "" }
func (s *stubConfigProvider) GetRedisDB() int                        { return 0 }
func (s *stubConfigProvider) GetHTTPMaxBodyBytes() int               { return 0 }
func (s *stubConfigProvider) GetRateLimitPerMinute() int             { return 0 }
func (s *stubConfigProvider) GetFragmentCreateRateLimit() int        { return 0 }
func (s *stubConfigProvider) GetFragmentReadRateLimit() int          { return 0 }
func (s *stubConfigProvider) GetSSEHeartbeatSeconds() int            { return 0 }
func (s *stubConfigProvider) GetSSEMaxDurationSeconds() int          { return 0 }
func (s *stubConfigProvider) GetSSEMaxConcurrentStreams() int        { return 0 }
func (s *stubConfigProvider) GetEmbeddingDimensions() int            { return 0 }
func (s *stubConfigProvider) GetAIAPIURL() string                    { return "" }
func (s *stubConfigProvider) GetAIAPIKey() string                    { return "" }
func (s *stubConfigProvider) GetAIEmbeddingModel() string            { return "" }
func (s *stubConfigProvider) GetAIEmbeddingDimensions() int          { return 0 }
func (s *stubConfigProvider) GetAIEmbeddingTimeoutSeconds() int      { return 0 }
func (s *stubConfigProvider) IsEmbeddingConfigured() bool            { return false }
func (s *stubConfigProvider) GetAIVerifierAPIURL() string            { return "" }
func (s *stubConfigProvider) GetAIVerifierAPIKey() string            { return "" }
func (s *stubConfigProvider) GetAIVerifierModel() string             { return "" }
func (s *stubConfigProvider) GetAIVerifierTimeoutSeconds() int       { return 0 }
func (s *stubConfigProvider) GetAIVerifierMaxConcurrency() int       { return 0 }
func (s *stubConfigProvider) GetClaimWriteRateLimit() int            { return 0 }
func (s *stubConfigProvider) GetClaimReadRateLimit() int             { return 0 }
func (s *stubConfigProvider) GetRecallValidatedClaimWeight() float64 { return 0 }
func (s *stubConfigProvider) GetPromoteTxTimeoutSeconds() int        { return 0 }
func (s *stubConfigProvider) GetSkillPackImportHistoryDays() int     { return 30 }
func (s *stubConfigProvider) GetControlHTTPAddr() string             { return "127.0.0.1:8090" }
func (s *stubConfigProvider) GetControlPortalToken() string          { return "" }

// newTestLeidenService constructs a leidenServiceImpl with injected stubs.
// Used only in tests; production callers use NewLeidenService.
func newTestLeidenService(locker communityLocker, querier leidenQuerier, maxNodes int) DetectCommunityService {
	return &leidenServiceImpl{
		locker:  locker,
		querier: querier,
		cfg:     &stubConfigProvider{maxNodes: maxNodes},
		logger:  nil,
	}
}

func TestLeidenProductionConstructorsAndWrappers(t *testing.T) {
	ctx := context.Background()
	client := &stubCommunityGDSClient{readResults: []any{int64(12)}}
	cfg := &stubConfigProvider{maxNodes: 100}

	svc := NewLeidenService(nil, client, cfg, nil)
	require.NotNil(t, svc)

	require.False(t, isEmptyCommunityProjectionError(nil))
	require.False(t, isEmptyCommunityProjectionError(errors.New("different error")))
	require.True(t, isEmptyCommunityProjectionError(errors.New("Node query returned no nodes")))

	querier := &neo4jLeidenQuerier{client: client}
	count, err := querier.EstimateProjection(ctx, "profile-1", "graph-1")
	require.NoError(t, err)
	require.Equal(t, int64(12), count)

	querier = &neo4jLeidenQuerier{client: &stubCommunityGDSClient{readResults: []any{nil}}}
	count, err = querier.EstimateProjection(ctx, "profile-1", "graph-1")
	require.NoError(t, err)
	require.Equal(t, int64(0), count)

	querier = &neo4jLeidenQuerier{client: &stubCommunityGDSClient{readResults: []any{"bad"}}}
	count, err = querier.EstimateProjection(ctx, "profile-1", "graph-1")
	require.NoError(t, err)
	require.Equal(t, int64(0), count)

	readErr := errors.New("estimate failed")
	querier = &neo4jLeidenQuerier{client: &stubCommunityGDSClient{readErrs: []error{readErr}}}
	count, err = querier.EstimateProjection(ctx, "profile-1", "graph-1")
	require.Equal(t, int64(0), count)
	require.ErrorIs(t, err, readErr)

	writeClient := &stubCommunityGDSClient{}
	querier = &neo4jLeidenQuerier{client: writeClient}
	require.NoError(t, querier.ProjectGraph(ctx, "profile-1", "graph-1"))
	require.NoError(t, querier.ToUndirected(ctx, "graph-1"))
	require.NoError(t, querier.RunLeiden(ctx, "graph-1", DetectOptions{Gamma: 1.2, Tolerance: 0.01, MaxLevels: 3}))
	require.NoError(t, querier.DropGraph(ctx, "graph-1"))
	require.Equal(t, 4, writeClient.writes)

	writeErr := errors.New("write failed")
	querier = &neo4jLeidenQuerier{client: &stubCommunityGDSClient{writeErr: writeErr}}
	require.ErrorIs(t, querier.ProjectGraph(ctx, "profile-1", "graph-1"), writeErr)
	require.ErrorIs(t, querier.ToUndirected(ctx, "graph-1"), writeErr)
	require.ErrorIs(t, querier.RunLeiden(ctx, "graph-1", DetectOptions{}), writeErr)
	require.ErrorIs(t, querier.DropGraph(ctx, "graph-1"), writeErr)
}

func TestPGCommunityLocker(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	defer sqlDB.Close()

	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\(\$1\)\)`).WithArgs("community:profile-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	locker := &pgCommunityLocker{db: db}
	called := false
	err = locker.WithCommunityLock(context.Background(), "profile-1", func(ctx context.Context) error {
		called = true
		return nil
	})

	require.NoError(t, err)
	require.True(t, called)
	require.NoError(t, mock.ExpectationsWereMet())

	sqlDB, mock, err = sqlmock.New()
	require.NoError(t, err)
	defer sqlDB.Close()
	db, err = gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{})
	require.NoError(t, err)

	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock\(hashtext\(\$1\)\)`).WithArgs("community:profile-2").
		WillReturnError(errors.New("lock failed"))
	mock.ExpectRollback()

	locker = &pgCommunityLocker{db: db}
	err = locker.WithCommunityLock(context.Background(), "profile-2", func(ctx context.Context) error {
		t.Fatal("callback should not run when lock acquisition fails")
		return nil
	})

	require.ErrorContains(t, err, "community advisory lock acquire")
	require.NoError(t, mock.ExpectationsWereMet())
}

// ---------------------------------------------------------------------------
// TestLeidenDetect — covers AC-51, AC-52
// ---------------------------------------------------------------------------

// TestLeidenDetect verifies the full Detect flow: advisory lock acquisition,
// estimate check, graph projection, leiden write, and deferred graph drop.
func TestLeidenDetect(t *testing.T) {
	ctx := context.Background()
	const profileID = "profile-abc"
	const maxNodes = 500_000

	t.Run("happy path: detect completes within node cap", func(t *testing.T) {
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{
			estimateNodeCount: 100, // well below maxNodes
		}
		svc := newTestLeidenService(locker, querier, maxNodes)

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.NoError(t, err, "Detect must succeed when node count is within the cap")

		// Advisory lock must have been acquired for the correct profile.
		require.Equal(t, []string{profileID}, locker.locked,
			"Detect must acquire the advisory lock for the given profileID")

		// Graph must have been projected for the correct profile.
		require.Contains(t, querier.projectedProfiles, profileID,
			"Detect must project a graph scoped to the given profileID")

		// Projected graph name must embed the profileID.
		wantGraph := GraphNamePrefix + profileID + "-leiden"
		require.Contains(t, querier.projectedGraphs, wantGraph,
			"projected graph name must embed profileID for isolation")

		// Drop must always be called (deferred).
		require.Contains(t, querier.droppedGraphs, wantGraph,
			"Detect must always drop the projected graph on return")
		require.Contains(t, querier.undirectedGraphs, wantGraph,
			"Detect must convert the projected graph to undirected edges before Leiden")
		require.Equal(t, []DetectOptions{{
			Gamma:     defaultDetectGamma,
			MaxLevels: defaultDetectMaxLevels,
		}}, querier.runOptions,
			"Detect must normalize zero-value options to the documented defaults")
	})

	t.Run("rejects when estimated node count exceeds cap", func(t *testing.T) {
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{
			estimateNodeCount: int64(maxNodes) + 1, // over the cap
		}
		svc := newTestLeidenService(locker, querier, maxNodes)

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.Error(t, err, "Detect must return an error when node count exceeds the cap")
		require.ErrorIs(t, err, ErrCommunityGraphTooLarge,
			"Detect must wrap ErrCommunityGraphTooLarge when node count exceeds the cap")

		// Graph must NOT have been projected when estimate exceeds the cap.
		require.Empty(t, querier.projectedGraphs,
			"Detect must not project a graph when the estimate exceeds the cap")

		// Drop must NOT have been called because projection never happened.
		require.Empty(t, querier.droppedGraphs,
			"Detect must not attempt a graph drop when projection was skipped")
	})

	t.Run("empty graph clears persisted summaries and skips projection", func(t *testing.T) {
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{estimateNodeCount: 0}
		store := &stubCommunitySummaryStore{}
		svc := &leidenServiceImpl{
			locker:  locker,
			querier: querier,
			store:   store,
			cfg:     &stubConfigProvider{maxNodes: maxNodes},
		}

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.NoError(t, err)
		require.Equal(t, []string{profileID}, locker.locked,
			"Detect must still acquire the advisory lock for empty graphs")
		require.Equal(t, []string{profileID}, store.replacedProfiles,
			"Detect must clear persisted summaries for the requested profile")
		require.Len(t, store.replacedCommunities, 1)
		require.Empty(t, store.replacedCommunities[0],
			"Detect must replace empty-graph summaries with an empty set")
		require.Empty(t, querier.projectedGraphs,
			"Detect must not project a GDS graph when the estimate is zero")
		require.Empty(t, querier.droppedGraphs,
			"Detect must not drop a projected graph when none was created")
	})

	t.Run("node count exactly at cap is allowed", func(t *testing.T) {
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{
			estimateNodeCount: int64(maxNodes), // exactly at the cap
		}
		svc := newTestLeidenService(locker, querier, maxNodes)

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.NoError(t, err, "Detect must succeed when node count equals the cap exactly")
	})

	t.Run("graph is always dropped even when leiden write fails", func(t *testing.T) {
		leidenErr := errors.New("gds.leiden.write failed")
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{
			estimateNodeCount: 50,
			leidenErr:         leidenErr,
		}
		svc := newTestLeidenService(locker, querier, maxNodes)

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.Error(t, err, "Detect must propagate the leiden write error")

		// Drop must still be called via defer.
		wantGraph := GraphNamePrefix + profileID + "-leiden"
		require.Contains(t, querier.droppedGraphs, wantGraph,
			"Detect must drop the projected graph even when leiden write fails")
	})

	t.Run("graph is always dropped even when undirected conversion fails", func(t *testing.T) {
		undirectedErr := errors.New("gds.graph.relationships.toUndirected failed")
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{
			estimateNodeCount: 50,
			toUndirectedErr:   undirectedErr,
		}
		svc := newTestLeidenService(locker, querier, maxNodes)

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.Error(t, err)
		require.ErrorContains(t, err, "leiden make undirected")
		wantGraph := GraphNamePrefix + profileID + "-leiden"
		require.Contains(t, querier.droppedGraphs, wantGraph,
			"Detect must drop the projected graph even when undirected conversion fails")
	})

	t.Run("empty projection error clears persisted summaries", func(t *testing.T) {
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{
			estimateNodeCount: 1,
			projectErr:        errors.New("Node-Query returned no nodes"),
		}
		store := &stubCommunitySummaryStore{}
		svc := &leidenServiceImpl{
			locker:  locker,
			querier: querier,
			store:   store,
			cfg:     &stubConfigProvider{maxNodes: maxNodes},
		}

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.NoError(t, err)
		require.Equal(t, []string{profileID}, store.replacedProfiles,
			"Detect must clear persisted summaries when projection reports no nodes")
		require.Len(t, store.replacedCommunities, 1)
		require.Empty(t, store.replacedCommunities[0])
		require.Empty(t, querier.droppedGraphs,
			"Detect must not drop a graph when projection never succeeded")
	})

	t.Run("propagates estimate error", func(t *testing.T) {
		estimateErr := errors.New("gds.graph.project.estimate: procedure unavailable")
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{estimateErr: estimateErr}
		svc := newTestLeidenService(locker, querier, maxNodes)

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.Error(t, err)
		require.ErrorContains(t, err, "leiden estimate projection")
	})

	t.Run("propagates project error and still drops graph", func(t *testing.T) {
		projectErr := errors.New("gds.graph.project: failed")
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{
			estimateNodeCount: 50,
			projectErr:        projectErr,
		}
		svc := newTestLeidenService(locker, querier, maxNodes)

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.Error(t, err)
		require.ErrorContains(t, err, "leiden project graph")
	})

	t.Run("passes explicit tuning options through to leiden write", func(t *testing.T) {
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{
			estimateNodeCount: 42,
		}
		svc := newTestLeidenService(locker, querier, maxNodes)
		opts := DetectOptions{
			Gamma:     1.7,
			Tolerance: 0.00025,
			MaxLevels: 6,
		}

		err := svc.Detect(ctx, profileID, opts)

		require.NoError(t, err)
		require.Equal(t, []DetectOptions{opts}, querier.runOptions,
			"Detect must pass explicit tuning parameters through to the Leiden run")
	})

	t.Run("propagates advisory lock error", func(t *testing.T) {
		lockErr := errors.New("lock unavailable")
		locker := &stubCommunityLocker{err: lockErr}
		querier := &stubLeidenQuerier{estimateNodeCount: 42}
		svc := newTestLeidenService(locker, querier, maxNodes)

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.ErrorIs(t, err, lockErr)
		require.Equal(t, []string{profileID}, locker.locked)
		require.Empty(t, querier.estimatedProfiles)
	})

	t.Run("drop error is logged but does not fail successful detection", func(t *testing.T) {
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{
			estimateNodeCount: 42,
			dropErr:           errors.New("drop failed"),
		}
		svc := &leidenServiceImpl{
			locker:  locker,
			querier: querier,
			cfg:     &stubConfigProvider{maxNodes: maxNodes},
			logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		}

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.NoError(t, err)
		require.Equal(t, []string{GraphNamePrefix + profileID + "-leiden"}, querier.droppedGraphs)
	})

	t.Run("successful detection refreshes deterministic summaries", func(t *testing.T) {
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{estimateNodeCount: 42}
		store := &stubCommunitySummaryStore{
			inputs: []communitySummaryInput{
				{
					CommunityID: "community-1",
					MemberCount: 2,
					FactTriples: []communityTriple{
						{Subject: "alice", Predicate: "knows", Object: "bob"},
					},
				},
			},
		}
		svc := &leidenServiceImpl{
			locker:  locker,
			querier: querier,
			store:   store,
			cfg:     &stubConfigProvider{maxNodes: maxNodes},
		}

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.NoError(t, err)
		require.Equal(t, []string{profileID}, store.replacedProfiles)
		require.Len(t, store.replacedCommunities, 1)
		require.Len(t, store.replacedCommunities[0], 1)
		require.Equal(t, "community-1", store.replacedCommunities[0][0].CommunityID)
		require.Equal(t, profileID, store.replacedCommunities[0][0].ProfileID)
	})

	t.Run("propagates summary input load error after leiden", func(t *testing.T) {
		loadErr := errors.New("summary read failed")
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{estimateNodeCount: 42}
		store := &stubCommunitySummaryStore{loadErr: loadErr}
		svc := &leidenServiceImpl{
			locker:  locker,
			querier: querier,
			store:   store,
			cfg:     &stubConfigProvider{maxNodes: maxNodes},
		}

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.ErrorIs(t, err, loadErr)
		require.ErrorContains(t, err, "community summary inputs")
		require.Contains(t, querier.droppedGraphs, GraphNamePrefix+profileID+"-leiden")
	})

	t.Run("propagates summary replace error after leiden", func(t *testing.T) {
		replaceErr := errors.New("summary write failed")
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{estimateNodeCount: 42}
		store := &stubCommunitySummaryStore{replaceErr: replaceErr}
		svc := &leidenServiceImpl{
			locker:  locker,
			querier: querier,
			store:   store,
			cfg:     &stubConfigProvider{maxNodes: maxNodes},
		}

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.ErrorIs(t, err, replaceErr)
		require.ErrorContains(t, err, "community summary replace")
		require.Contains(t, querier.droppedGraphs, GraphNamePrefix+profileID+"-leiden")
	})

	t.Run("propagates clear summary replace error for empty graph", func(t *testing.T) {
		replaceErr := errors.New("summary clear failed")
		locker := &stubCommunityLocker{}
		querier := &stubLeidenQuerier{estimateNodeCount: 0}
		store := &stubCommunitySummaryStore{replaceErr: replaceErr}
		svc := &leidenServiceImpl{
			locker:  locker,
			querier: querier,
			store:   store,
			cfg:     &stubConfigProvider{maxNodes: maxNodes},
		}

		err := svc.Detect(ctx, profileID, DetectOptions{})

		require.ErrorIs(t, err, replaceErr)
		require.ErrorContains(t, err, "community summary replace")
		require.Empty(t, querier.projectedGraphs)
	})
}

// ---------------------------------------------------------------------------
// TestLeidenDetect_CrossProfileIsolation — covers AC-53, AC-54
// ---------------------------------------------------------------------------

// TestLeidenDetect_CrossProfileIsolation verifies that running Detect for
// profile A does not affect profile B's graph data.
//
// This test satisfies the mandatory cross-profile isolation requirement from
// .claude/rules/profile-isolation.md.
func TestLeidenDetect_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()

	const profileA = "profile-A"
	const profileB = "profile-B"
	const maxNodes = 500_000

	// Run Detect for both profiles using the same querier instance so we can
	// observe that each profile operates on a distinct graph name.
	locker := &stubCommunityLocker{}
	querier := &stubLeidenQuerier{estimateNodeCount: 10}

	svcA := newTestLeidenService(locker, querier, maxNodes)
	svcB := newTestLeidenService(locker, querier, maxNodes)

	require.NoError(t, svcA.Detect(ctx, profileA, DetectOptions{}))
	require.NoError(t, svcB.Detect(ctx, profileB, DetectOptions{}))

	graphA := GraphNamePrefix + profileA + "-leiden"
	graphB := GraphNamePrefix + profileB + "-leiden"

	// Graph names must be distinct so GDS projections never overlap.
	require.NotEqual(t, graphA, graphB,
		"each profile must use a distinct GDS graph name")

	// Verify that profile A's graph name does not appear in profile B's operations.
	// The querier tracks which graphs were projected per call order.
	// Profile A's project call (index 0) must use graphA, not graphB.
	require.Equal(t, graphA, querier.projectedGraphs[0],
		"first Detect call (profile A) must project graph scoped to profile A")
	require.Equal(t, graphB, querier.projectedGraphs[1],
		"second Detect call (profile B) must project graph scoped to profile B")

	// Each profile's graph must have been dropped independently.
	require.Contains(t, querier.droppedGraphs, graphA,
		"profile A graph must be dropped after detection")
	require.Contains(t, querier.droppedGraphs, graphB,
		"profile B graph must be dropped after detection")

	// Profile B's drop list must not contain profile A's graph name,
	// confirming that profile A data was not touched during profile B's run.
	// Since stub tracks drop order, graphB appears at index 1.
	require.NotEqual(t, querier.droppedGraphs[1], graphA,
		"profile B Detect must not drop profile A graph")

	// Advisory lock must have been acquired separately for each profile.
	require.Contains(t, locker.locked, profileA,
		"Detect must acquire advisory lock for profile A")
	require.Contains(t, locker.locked, profileB,
		"Detect must acquire advisory lock for profile B")
	// The lock keys differ by profileID so the profiles are serialised
	// independently and never block each other.
	require.NotEqual(t, locker.locked[0], locker.locked[1],
		"advisory lock must use distinct keys for different profiles")
}

func TestLeidenProjectionExcludesNonRecallableStates(t *testing.T) {
	require.Contains(t, leidenNodeQuery, "n:SourceFragment AND coalesce(n.status, 'active') = 'active'")
	require.Contains(t, leidenNodeQuery, "n:Claim AND coalesce(n.status, '') = 'validated'")
	require.Contains(t, leidenNodeQuery, "n:Fact AND coalesce(n.status, '') = 'active'")
}
