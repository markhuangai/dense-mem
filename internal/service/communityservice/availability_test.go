package communityservice

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Stub: gdsQuerier
// ---------------------------------------------------------------------------

// stubGDSQuerier is a test-only implementation of gdsQuerier.
//
// It models GDS behaviour without a real Neo4j connection:
//   - probeErr controls what ProbeAvailability returns
//   - graphsByPrefix maps prefix → graph names returned by ListProjectedGraphs
//   - dropErr controls what DropProjectedGraph returns
//   - dropped records every graph name passed to DropProjectedGraph
//
// Profile isolation invariant: ListProjectedGraphs returns only graphs whose
// names start with the supplied prefix, mirroring the parameterised STARTS WITH
// clause in the production Cypher. Tests rely on this to assert cross-profile
// isolation without a real database.
type stubGDSQuerier struct {
	probeErr       error
	graphsByPrefix map[string][]string
	dropErr        error
	dropped        []string
}

func (s *stubGDSQuerier) ProbeAvailability(_ context.Context) error {
	return s.probeErr
}

func (s *stubGDSQuerier) ListProjectedGraphs(_ context.Context, prefix string) ([]string, error) {
	if s.graphsByPrefix == nil {
		return nil, nil
	}
	return s.graphsByPrefix[prefix], nil
}

func (s *stubGDSQuerier) DropProjectedGraph(_ context.Context, name string) error {
	if s.dropErr != nil {
		return s.dropErr
	}
	s.dropped = append(s.dropped, name)
	return nil
}

// Compile-time check: stubGDSQuerier satisfies gdsQuerier.
var _ gdsQuerier = (*stubGDSQuerier)(nil)

// newTestAvailabilityService constructs an AvailabilityService with an
// injected stub querier. Only used in unit tests; the exported constructor
// NewAvailabilityService is used in production.
func newTestAvailabilityService(q gdsQuerier) AvailabilityService {
	return &availabilityServiceImpl{querier: q, logger: nil}
}

// ---------------------------------------------------------------------------
// TestProbeGDS — covers AC-49
// ---------------------------------------------------------------------------

// TestProbeGDS verifies that ProbeGDS correctly maps GDS availability to a
// boolean result without surfacing errors to callers (startup must not fail
// when GDS is absent).
func TestProbeGDS(t *testing.T) {
	ctx := context.Background()

	t.Run("returns true when GDS is reachable", func(t *testing.T) {
		stub := &stubGDSQuerier{probeErr: nil}
		svc := newTestAvailabilityService(stub)

		got := svc.ProbeGDS(ctx)

		require.True(t, got, "ProbeGDS must return true when GDS probe succeeds")
	})

	t.Run("returns false when GDS is unavailable", func(t *testing.T) {
		stub := &stubGDSQuerier{
			probeErr: errors.New("procedure not found: gds.list"),
		}
		svc := newTestAvailabilityService(stub)

		got := svc.ProbeGDS(ctx)

		require.False(t, got, "ProbeGDS must return false when GDS probe fails")
	})

	t.Run("returns false on transient neo4j error", func(t *testing.T) {
		stub := &stubGDSQuerier{
			probeErr: errors.New("neo4j: connection refused"),
		}
		svc := newTestAvailabilityService(stub)

		// The error must be swallowed — ProbeGDS MUST NOT panic or propagate.
		got := svc.ProbeGDS(ctx)

		require.False(t, got)
	})
}

func TestAvailabilityProductionWrappers(t *testing.T) {
	ctx := context.Background()
	client := &stubCommunityGDSClient{readResults: []any{nil, []string{"dense-mem-profile-g"}}}

	svc := NewAvailabilityService(client, nil)
	require.NotNil(t, svc)

	querier := &neo4jGDSQuerier{client: client}
	require.NoError(t, querier.ProbeAvailability(ctx))

	names, err := querier.ListProjectedGraphs(ctx, GraphNamePrefix)
	require.NoError(t, err)
	require.Equal(t, []string{"dense-mem-profile-g"}, names)

	require.NoError(t, querier.DropProjectedGraph(ctx, "dense-mem-profile-g"))

	readErr := errors.New("read failed")
	querier = &neo4jGDSQuerier{client: &stubCommunityGDSClient{readErrs: []error{readErr}}}
	require.ErrorIs(t, querier.ProbeAvailability(ctx), readErr)

	querier = &neo4jGDSQuerier{client: &stubCommunityGDSClient{readErrs: []error{readErr}}}
	names, err = querier.ListProjectedGraphs(ctx, GraphNamePrefix)
	require.Nil(t, names)
	require.ErrorIs(t, err, readErr)

	writeErr := errors.New("write failed")
	querier = &neo4jGDSQuerier{client: &stubCommunityGDSClient{writeErr: writeErr}}
	require.ErrorIs(t, querier.DropProjectedGraph(ctx, "dense-mem-profile-g"), writeErr)
}

// ---------------------------------------------------------------------------
// TestSweepOrphanGraphs — covers AC-50
// ---------------------------------------------------------------------------

// TestSweepOrphanGraphs verifies that SweepOrphanGraphs lists orphan graphs
// by prefix and drops each one, that errors propagate correctly, and that the
// cross-profile isolation invariant is respected.
func TestSweepOrphanGraphs(t *testing.T) {
	ctx := context.Background()

	const prefixA = GraphNamePrefix + "profile-A-"

	t.Run("drops all graphs matching the prefix", func(t *testing.T) {
		stub := &stubGDSQuerier{
			graphsByPrefix: map[string][]string{
				prefixA: {"dense-mem-profile-A-g1", "dense-mem-profile-A-g2"},
			},
		}
		svc := newTestAvailabilityService(stub)

		err := svc.SweepOrphanGraphs(ctx, prefixA)

		require.NoError(t, err)
		require.ElementsMatch(t,
			[]string{"dense-mem-profile-A-g1", "dense-mem-profile-A-g2"},
			stub.dropped,
			"SweepOrphanGraphs must drop every graph returned by the list step",
		)
	})

	t.Run("returns nil when no graphs match the prefix", func(t *testing.T) {
		stub := &stubGDSQuerier{
			graphsByPrefix: map[string][]string{},
		}
		svc := newTestAvailabilityService(stub)

		err := svc.SweepOrphanGraphs(ctx, prefixA)

		require.NoError(t, err)
		require.Empty(t, stub.dropped)
	})

	t.Run("propagates list error", func(t *testing.T) {
		listErr := errors.New("gds.graph.list unavailable")
		// Override ListProjectedGraphs to return an error via a custom stub.
		errStub := &errListQuerier{listErr: listErr}
		svc := newTestAvailabilityService(errStub)

		err := svc.SweepOrphanGraphs(ctx, prefixA)

		require.Error(t, err)
		require.ErrorContains(t, err, "sweep orphan graphs list")
	})

	t.Run("propagates drop error and stops after first failure", func(t *testing.T) {
		dropErr := errors.New("gds.graph.drop failed")
		stub := &stubGDSQuerier{
			graphsByPrefix: map[string][]string{
				prefixA: {"dense-mem-profile-A-g1", "dense-mem-profile-A-g2"},
			},
			dropErr: dropErr,
		}
		svc := newTestAvailabilityService(stub)

		err := svc.SweepOrphanGraphs(ctx, prefixA)

		require.Error(t, err)
		require.ErrorContains(t, err, "sweep orphan graphs drop")
	})
}

// TestSweepOrphanGraphs_CrossProfileIsolation verifies that sweeping with a
// profile-A-scoped prefix does not drop profile-B graphs. This is the
// mandatory cross-profile isolation test required by
// AGENTS.md.
func TestSweepOrphanGraphs_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()

	const prefixA = GraphNamePrefix + "profile-A-"
	const prefixB = GraphNamePrefix + "profile-B-"

	const graphA1 = "dense-mem-profile-A-g1"
	const graphB1 = "dense-mem-profile-B-g1"

	// The stub models the STARTS WITH $prefix filter in the production Cypher:
	// ListProjectedGraphs for prefixA returns only A's graphs; B's graph is
	// invisible to the A-scoped sweep.
	stub := &stubGDSQuerier{
		graphsByPrefix: map[string][]string{
			prefixA: {graphA1},
			prefixB: {graphB1},
		},
	}
	svc := newTestAvailabilityService(stub)

	// Sweep profile A only.
	err := svc.SweepOrphanGraphs(ctx, prefixA)
	require.NoError(t, err)

	// A's graph must have been dropped.
	require.Contains(t, stub.dropped, graphA1,
		"SweepOrphanGraphs must drop profile A graph when called with prefix A")

	// B's graph must NOT have been touched.
	require.NotContains(t, stub.dropped, graphB1,
		"SweepOrphanGraphs must not drop profile B graph when called with prefix A")
}

// ---------------------------------------------------------------------------
// errListQuerier — stub whose ListProjectedGraphs always returns an error
// ---------------------------------------------------------------------------

type errListQuerier struct {
	listErr error
}

func (e *errListQuerier) ProbeAvailability(_ context.Context) error { return nil }
func (e *errListQuerier) ListProjectedGraphs(_ context.Context, _ string) ([]string, error) {
	return nil, e.listErr
}
func (e *errListQuerier) DropProjectedGraph(_ context.Context, _ string) error { return nil }

var _ gdsQuerier = (*errListQuerier)(nil)
