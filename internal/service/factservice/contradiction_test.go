package factservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
)

// stubContradictionTxRunner implements contradictionTxRunner for unit tests.
//
// It records the profileID passed to ScopedWriteTx and can be configured to
// return an error. It does NOT invoke fn because neo4j.ManagedTransaction has
// an unexported method (legacy()) that prevents external stub implementations.
// Query-level correctness is verified by integration tests against a real Neo4j
// instance; unit tests here focus on profileID routing and error propagation.
type stubContradictionTxRunner struct {
	calledProfileID string
	txErr           error
}

func (s *stubContradictionTxRunner) ScopedWriteTx(
	_ context.Context,
	profileID string,
	_ func(tx neo4j.ManagedTransaction) error,
) error {
	s.calledProfileID = profileID
	return s.txErr
}

// Compile-time check: stubContradictionTxRunner satisfies contradictionTxRunner.
var _ contradictionTxRunner = (*stubContradictionTxRunner)(nil)

// TestContradictionPaths covers contradiction resolution helpers:
//   - AC-37: findActiveFactsBySubjectPredicate queries and returns correct facts
//   - AC-39: write path helpers route profileID correctly and propagate errors
//   - AC-40: cross-profile isolation — profile A facts must not appear for profile B
func TestContradictionPaths(t *testing.T) {
	ctx := context.Background()
	const profileA = "00000000-0000-0000-0000-000000000001"
	const profileB = "00000000-0000-0000-0000-000000000002"
	now := time.Date(2024, 6, 15, 12, 0, 0, 0, time.UTC)

	// ── findActiveFactsBySubjectPredicate ────────────────────────────────────

	t.Run("findActiveFacts returns facts for profile", func(t *testing.T) {
		row := makeFactRow("fact-1", "Alice", "works_at", "active", now)
		reader := &stubFactReader{
			rowsByProfile: map[string][]map[string]any{
				profileA: {row},
			},
		}

		facts, err := findActiveFactsBySubjectPredicate(ctx, reader, profileA, "Alice", "works_at")

		require.NoError(t, err)
		require.Len(t, facts, 1)
		require.Equal(t, "fact-1", facts[0].FactID)
		require.Equal(t, profileA, facts[0].ProfileID)
		require.Equal(t, "Alice", facts[0].Subject)
		require.Equal(t, "works_at", facts[0].Predicate)
	})

	t.Run("findActiveFacts returns empty slice when none exist", func(t *testing.T) {
		reader := &stubFactReader{
			rowsByProfile: map[string][]map[string]any{
				profileA: {},
			},
		}

		facts, err := findActiveFactsBySubjectPredicate(ctx, reader, profileA, "Alice", "works_at")

		require.NoError(t, err)
		require.Empty(t, facts)
	})

	t.Run("findActiveFacts propagates reader error", func(t *testing.T) {
		readerErr := errors.New("neo4j unavailable")
		reader := &stubFactReader{err: readerErr}

		_, err := findActiveFactsBySubjectPredicate(ctx, reader, profileA, "Alice", "works_at")

		require.Error(t, err)
		require.Contains(t, err.Error(), "neo4j unavailable")
	})

	t.Run("findActiveFacts hydrates multiple rows", func(t *testing.T) {
		rows := []map[string]any{
			makeFactRow("fact-a", "Bob", "works_at", "active", now),
			makeFactRow("fact-b", "Bob", "works_at", "active", now.Add(-time.Second)),
		}
		reader := &stubFactReader{
			rowsByProfile: map[string][]map[string]any{
				profileA: rows,
			},
		}

		facts, err := findActiveFactsBySubjectPredicate(ctx, reader, profileA, "Bob", "works_at")

		require.NoError(t, err)
		require.Len(t, facts, 2)
		ids := []string{facts[0].FactID, facts[1].FactID}
		require.Contains(t, ids, "fact-a")
		require.Contains(t, ids, "fact-b")
	})

	// ── newActivePath ────────────────────────────────────────────────────────

	t.Run("newActivePath is a documented no-op", func(t *testing.T) {
		// No side effects: returns cleanly with no state change.
		newActivePath()
	})

	// ── sameObjectConfirmPath ────────────────────────────────────────────────

	t.Run("sameObjectConfirmPath routes profileID to ScopedWriteTx", func(t *testing.T) {
		db := &stubContradictionTxRunner{}
		existing := []*domain.Fact{{FactID: "fact-existing", ProfileID: profileA}}

		err := sameObjectConfirmPath(ctx, db, profileA, existing)

		require.NoError(t, err)
		require.Equal(t, profileA, db.calledProfileID)
	})

	t.Run("sameObjectConfirmPath propagates tx error", func(t *testing.T) {
		txErr := errors.New("confirm failed")
		db := &stubContradictionTxRunner{txErr: txErr}

		err := sameObjectConfirmPath(ctx, db, profileA, []*domain.Fact{{FactID: "f1", ProfileID: profileA}})

		require.Error(t, err)
		require.Contains(t, err.Error(), "confirm failed")
	})

	// ── supersedePath ────────────────────────────────────────────────────────

	t.Run("supersedePath routes profileID to ScopedWriteTx", func(t *testing.T) {
		db := &stubContradictionTxRunner{}
		oldFact := &domain.Fact{FactID: "fact-old", ProfileID: profileA}

		err := supersedePath(ctx, db, profileA, []*domain.Fact{oldFact}, "claim-new", nil)

		require.NoError(t, err)
		require.Equal(t, profileA, db.calledProfileID)
	})

	t.Run("supersedePath accepts non-nil newClaimValidFrom", func(t *testing.T) {
		db := &stubContradictionTxRunner{}
		validFrom := now.Add(24 * time.Hour)
		oldFact := &domain.Fact{FactID: "fact-old", ProfileID: profileA}

		err := supersedePath(ctx, db, profileA, []*domain.Fact{oldFact}, "claim-new", &validFrom)

		require.NoError(t, err)
	})

	t.Run("supersedePath propagates tx error", func(t *testing.T) {
		txErr := errors.New("tx failed")
		db := &stubContradictionTxRunner{txErr: txErr}

		err := supersedePath(ctx, db, profileA,
			[]*domain.Fact{{FactID: "f1", ProfileID: profileA}}, "claim-new", nil)

		require.Error(t, err)
		require.Contains(t, err.Error(), "tx failed")
	})

	// ── overlayPath ──────────────────────────────────────────────────────────

	t.Run("overlayPath skips tx when there are no foreign facts", func(t *testing.T) {
		db := &stubContradictionTxRunner{}

		err := overlayPath(ctx, db, profileA, "fact-new", nil)

		require.NoError(t, err)
		require.Empty(t, db.calledProfileID)
	})

	t.Run("overlayPath routes profileID to ScopedWriteTx", func(t *testing.T) {
		db := &stubContradictionTxRunner{}

		err := overlayPath(ctx, db, profileA, "fact-new", []*domain.Fact{{FactID: "fact-foreign"}})

		require.NoError(t, err)
		require.Equal(t, profileA, db.calledProfileID)
	})

	t.Run("overlayPath propagates tx error", func(t *testing.T) {
		txErr := errors.New("overlay failed")
		db := &stubContradictionTxRunner{txErr: txErr}

		err := overlayPath(ctx, db, profileA, "fact-new", []*domain.Fact{{FactID: "fact-foreign"}})

		require.Error(t, err)
		require.Contains(t, err.Error(), "overlay failed")
	})

	// ── alignPath ────────────────────────────────────────────────────────────

	t.Run("alignPath skips tx when there are no foreign facts", func(t *testing.T) {
		db := &stubContradictionTxRunner{}

		err := alignPath(ctx, db, profileA, "fact-new", nil)

		require.NoError(t, err)
		require.Empty(t, db.calledProfileID)
	})

	t.Run("alignPath routes profileID to ScopedWriteTx", func(t *testing.T) {
		db := &stubContradictionTxRunner{}

		err := alignPath(ctx, db, profileA, "fact-new", []*domain.Fact{{FactID: "fact-aligned"}})

		require.NoError(t, err)
		require.Equal(t, profileA, db.calledProfileID)
	})

	t.Run("alignPath propagates tx error", func(t *testing.T) {
		txErr := errors.New("align failed")
		db := &stubContradictionTxRunner{txErr: txErr}

		err := alignPath(ctx, db, profileA, "fact-new", []*domain.Fact{{FactID: "fact-aligned"}})

		require.Error(t, err)
		require.Contains(t, err.Error(), "align failed")
	})

	// ── comparablePath ───────────────────────────────────────────────────────

	t.Run("comparablePath routes profileID to ScopedWriteTx", func(t *testing.T) {
		db := &stubContradictionTxRunner{}
		conflicting := []*domain.Fact{{FactID: "fact-conflict", ProfileID: profileA}}

		err := comparablePath(ctx, db, profileA, "claim-abc", conflicting)

		require.NoError(t, err)
		require.Equal(t, profileA, db.calledProfileID)
	})

	t.Run("comparablePath propagates tx error", func(t *testing.T) {
		txErr := errors.New("write conflict")
		db := &stubContradictionTxRunner{txErr: txErr}

		err := comparablePath(ctx, db, profileA, "claim-abc",
			[]*domain.Fact{{FactID: "f1", ProfileID: profileA}})

		require.Error(t, err)
		require.Contains(t, err.Error(), "write conflict")
	})

	// ── weakerPath ───────────────────────────────────────────────────────────

	t.Run("weakerPath routes profileID to ScopedWriteTx", func(t *testing.T) {
		db := &stubContradictionTxRunner{}

		err := weakerPath(ctx, db, profileA, "claim-weak")

		require.NoError(t, err)
		require.Equal(t, profileA, db.calledProfileID)
	})

	t.Run("weakerPath propagates tx error", func(t *testing.T) {
		txErr := errors.New("write failed")
		db := &stubContradictionTxRunner{txErr: txErr}

		err := weakerPath(ctx, db, profileA, "claim-weak")

		require.Error(t, err)
		require.Contains(t, err.Error(), "write failed")
	})

	// ── AC-40: cross-profile isolation ───────────────────────────────────────

	t.Run("findActiveFacts_CrossProfileIsolation", func(t *testing.T) {
		rowA := makeFactRow("fact-a", "Alice", "works_at", "active", now)

		// Profile A reader: owns fact-a.
		readerA := &stubFactReader{
			rowsByProfile: map[string][]map[string]any{
				profileA: {rowA},
			},
		}
		factsA, errA := findActiveFactsBySubjectPredicate(ctx, readerA, profileA, "Alice", "works_at")
		require.NoError(t, errA)
		require.Len(t, factsA, 1)
		require.Equal(t, "fact-a", factsA[0].FactID)
		require.Equal(t, profileA, factsA[0].ProfileID)

		// Profile B reader: sees no facts — profile A data must not leak.
		readerB := &stubFactReader{
			rowsByProfile: map[string][]map[string]any{
				profileA: {rowA},
				profileB: {},
			},
		}
		factsB, errB := findActiveFactsBySubjectPredicate(ctx, readerB, profileB, "Alice", "works_at")
		require.NoError(t, errB)
		require.Empty(t, factsB, "profile B must not receive profile A's facts")

		bIDs := make([]string, len(factsB))
		for i, f := range factsB {
			bIDs[i] = f.FactID
		}
		require.NotContains(t, bIDs, "fact-a",
			"profile A's fact must not be visible to profile B")
	})
}
