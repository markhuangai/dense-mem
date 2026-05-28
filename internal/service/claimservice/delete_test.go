package claimservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

// stubDeleteResultSummary is a minimal neo4j.ResultSummary for delete unit tests.
//
// Only Counters() is meaningful; all other methods return zero values. The
// nodesDeleted field drives the "found vs not found" determination in
// deleteClaimServiceImpl.Delete.
type stubDeleteResultSummary struct {
	nodesDeleted int
}

func (s *stubDeleteResultSummary) Server() neo4j.ServerInfo                  { return nil }
func (s *stubDeleteResultSummary) Query() neo4j.Query                        { return nil }
func (s *stubDeleteResultSummary) StatementType() neo4j.StatementType        { return 0 }
func (s *stubDeleteResultSummary) Plan() neo4j.Plan                          { return nil }
func (s *stubDeleteResultSummary) Profile() neo4j.ProfiledPlan               { return nil }
func (s *stubDeleteResultSummary) Notifications() []neo4j.Notification       { return nil }
func (s *stubDeleteResultSummary) GqlStatusObjects() []neo4j.GqlStatusObject { return nil }
func (s *stubDeleteResultSummary) ResultAvailableAfter() time.Duration       { return 0 }
func (s *stubDeleteResultSummary) ResultConsumedAfter() time.Duration        { return 0 }
func (s *stubDeleteResultSummary) Database() neo4j.DatabaseInfo              { return nil }
func (s *stubDeleteResultSummary) Counters() neo4j.Counters {
	return &stubDeleteCounters{nodesDeleted: s.nodesDeleted}
}

// Compile-time check: stubDeleteResultSummary satisfies neo4j.ResultSummary.
var _ neo4j.ResultSummary = (*stubDeleteResultSummary)(nil)

// stubDeleteCounters is a minimal neo4j.Counters for delete unit tests.
type stubDeleteCounters struct {
	nodesDeleted int
}

func (c *stubDeleteCounters) ContainsUpdates() bool       { return c.nodesDeleted > 0 }
func (c *stubDeleteCounters) NodesCreated() int           { return 0 }
func (c *stubDeleteCounters) NodesDeleted() int           { return c.nodesDeleted }
func (c *stubDeleteCounters) RelationshipsCreated() int   { return 0 }
func (c *stubDeleteCounters) RelationshipsDeleted() int   { return 0 }
func (c *stubDeleteCounters) PropertiesSet() int          { return 0 }
func (c *stubDeleteCounters) LabelsAdded() int            { return 0 }
func (c *stubDeleteCounters) LabelsRemoved() int          { return 0 }
func (c *stubDeleteCounters) IndexesAdded() int           { return 0 }
func (c *stubDeleteCounters) IndexesRemoved() int         { return 0 }
func (c *stubDeleteCounters) ConstraintsAdded() int       { return 0 }
func (c *stubDeleteCounters) ConstraintsRemoved() int     { return 0 }
func (c *stubDeleteCounters) SystemUpdates() int          { return 0 }
func (c *stubDeleteCounters) ContainsSystemUpdates() bool { return false }

// Compile-time check: stubDeleteCounters satisfies neo4j.Counters.
var _ neo4j.Counters = (*stubDeleteCounters)(nil)

// stubProfileDeleteWriter models profile-scoped delete behavior for unit tests.
//
// existingClaims maps profileID to the set of claimIDs that exist in that
// profile. ScopedWrite returns nodesDeleted=1 when the claim exists and removes
// it from the set; returns nodesDeleted=0 when absent. This mirrors the Neo4j
// MATCH ... DETACH DELETE semantics scoped by team_id.
type stubProfileDeleteWriter struct {
	// existingClaims maps profileID → set of claimIDs present in that profile.
	existingClaims map[string]map[string]bool
	// err, when non-nil, is returned from every ScopedWrite call.
	err error
}

func (s *stubProfileDeleteWriter) ScopedWrite(
	_ context.Context,
	profileID string,
	_ string,
	params map[string]any,
) (neo4j.ResultSummary, error) {
	if s.err != nil {
		return nil, s.err
	}
	claimID, _ := params["claimId"].(string)
	claims, ok := s.existingClaims[profileID]
	if !ok || !claims[claimID] {
		// MATCH found nothing — Neo4j DETACH DELETE is a no-op.
		return &stubDeleteResultSummary{nodesDeleted: 0}, nil
	}
	// Found: remove the claim and report one node deleted.
	delete(claims, claimID)
	return &stubDeleteResultSummary{nodesDeleted: 1}, nil
}

// Compile-time check: stubProfileDeleteWriter satisfies claimWriter.
var _ claimWriter = (*stubProfileDeleteWriter)(nil)

// TestDeleteClaim covers AC-15: claim deletion must be profile-scoped and must
// return ErrClaimNotFound when the claim does not exist for the given profile.
func TestDeleteClaim(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"
	const claimID = "claim-del-001"

	t.Run("deletes existing claim and returns nil", func(t *testing.T) {
		writer := &stubProfileDeleteWriter{
			existingClaims: map[string]map[string]bool{
				profileID: {claimID: true},
			},
		}
		svc := NewDeleteClaimService(writer, nil, nil)

		err := svc.Delete(ctx, profileID, claimID)

		require.NoError(t, err)
		// Confirm the stub removed the claim from the set.
		require.False(t, writer.existingClaims[profileID][claimID],
			"claim must be removed from the store after deletion")
	})

	t.Run("returns ErrClaimNotFound when claim does not exist", func(t *testing.T) {
		writer := &stubProfileDeleteWriter{
			existingClaims: map[string]map[string]bool{
				profileID: {}, // profile exists but has no claims
			},
		}
		svc := NewDeleteClaimService(writer, nil, nil)

		err := svc.Delete(ctx, profileID, "nonexistent-claim")

		require.Error(t, err)
		require.True(t, errors.Is(err, ErrClaimNotFound),
			"missing claim must return ErrClaimNotFound, got: %v", err)
	})

	t.Run("propagates writer error", func(t *testing.T) {
		writerErr := errors.New("neo4j unavailable")
		writer := &stubProfileDeleteWriter{err: writerErr}
		svc := NewDeleteClaimService(writer, nil, nil)

		err := svc.Delete(ctx, profileID, claimID)

		require.Error(t, err)
		require.Contains(t, err.Error(), "neo4j unavailable")
	})

	t.Run("audit failure is swallowed — primary operation succeeds", func(t *testing.T) {
		writer := &stubProfileDeleteWriter{
			existingClaims: map[string]map[string]bool{
				profileID: {claimID: true},
			},
		}
		badAudit := &stubFailingAudit{err: errors.New("audit store down")}
		svc := NewDeleteClaimService(writer, badAudit, nil)

		err := svc.Delete(ctx, profileID, claimID)

		require.NoError(t, err,
			"audit failure must not surface as a Delete error")
	})
}

// stubFailingAudit always returns an error from Append.
type stubFailingAudit struct {
	err error
}

func (s *stubFailingAudit) Append(_ context.Context, _ AuditLogEntry) error {
	return s.err
}

var _ AuditEmitter = (*stubFailingAudit)(nil)

// TestDeleteClaim_CrossProfileIsolation verifies that a claim belonging to
// profile A cannot be deleted by profile B, and that no existence leak occurs.
// This is a mandatory security test per .claude/rules/profile-isolation.md.
func TestDeleteClaim_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()
	const profileA = "00000000-0000-0000-0000-000000000001"
	const profileB = "00000000-0000-0000-0000-000000000002"
	const claimID = "claim-isolation-001"

	// The stub models Neo4j's team_id scoping: ScopedWrite only deletes
	// claims registered under the given profileID. Profile B cannot delete
	// profile A's claim because the MATCH filter on team_id prevents it.
	writer := &stubProfileDeleteWriter{
		existingClaims: map[string]map[string]bool{
			profileA: {claimID: true}, // claim belongs to profile A only
			profileB: {},              // profile B has no claims
		},
	}
	svc := NewDeleteClaimService(writer, nil, nil)

	// Profile B must not be able to delete profile A's claim.
	errB := svc.Delete(ctx, profileB, claimID)
	require.Error(t, errB)
	require.True(t, errors.Is(errB, ErrClaimNotFound),
		"profile B must receive ErrClaimNotFound (no existence leak), got: %v", errB)

	// Profile A's claim must still exist after the failed profile B attempt.
	require.True(t, writer.existingClaims[profileA][claimID],
		"profile A's claim must not be deleted by profile B's request")

	// Profile A can delete its own claim.
	errA := svc.Delete(ctx, profileA, claimID)
	require.NoError(t, errA, "profile A must be able to delete its own claim")

	// Verify the claim is actually gone from profile A's set.
	aResults := []string{}
	for id := range writer.existingClaims[profileA] {
		aResults = append(aResults, id)
	}
	require.NotContains(t, aResults, claimID,
		"claim must be removed from profile A after successful deletion")
}
