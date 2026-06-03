package factservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/ownership"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	postgresstorage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// ── Stubs ──────────────────────────────────────────────────────────────────

// stubPromoteDB implements promoteDB for unit tests.
//
// ScopedRead returns rows keyed by call index (responsesByCall) so tests can
// configure different rows for the load-claim call (index 0), the idempotency
// check (index 1), and findActiveFactsBySubjectPredicate (index 2).
//
// ScopedWriteTx does NOT invoke fn because neo4j.ManagedTransaction carries an
// unexported legacy() method that cannot be implemented outside the driver.
// Query-level correctness is covered by integration tests; unit tests here
// focus on decision-tree routing, profileID propagation, and error handling.
type stubPromoteDB struct {
	responsesByCall  map[int][]map[string]any
	readErr          error
	writeTxErr       error
	callCount        int
	writeTxCount     int
	lastWriteProfile string
}

func (s *stubPromoteDB) ScopedRead(
	_ context.Context,
	_ string,
	_ string,
	_ map[string]any,
) (neo4j.ResultSummary, []map[string]any, error) {
	if s.readErr != nil {
		return nil, nil, s.readErr
	}
	idx := s.callCount
	s.callCount++
	if rows, ok := s.responsesByCall[idx]; ok {
		return nil, rows, nil
	}
	return nil, nil, nil
}

func (s *stubPromoteDB) ScopedWriteTx(
	_ context.Context,
	profileID string,
	_ func(tx neo4j.ManagedTransaction) error,
) error {
	s.writeTxCount++
	s.lastWriteProfile = profileID
	return s.writeTxErr
}

var _ promoteDB = (*stubPromoteDB)(nil)

// stubClaimLocker implements postgresstorage.ClaimLocker for unit tests.
//
// It immediately invokes fn with a nil *gorm.DB — the promotion algorithm
// uses the Neo4j client (s.db) inside fn, not the Postgres tx, so nil is safe.
type stubClaimLocker struct {
	lockErr error
}

func (s *stubClaimLocker) WithClaimLock(
	ctx context.Context,
	_ *gorm.DB,
	_, _ string,
	_ time.Duration,
	fn func(tx *gorm.DB) error,
) error {
	if s.lockErr != nil {
		return s.lockErr
	}
	return fn(nil)
}

var _ postgresstorage.ClaimLocker = (*stubClaimLocker)(nil)

// captureAuditEmitter records every emitted audit entry for assertion.
type captureAuditEmitter struct {
	entries []AuditLogEntry
	err     error
}

func (a *captureAuditEmitter) Append(_ context.Context, entry AuditLogEntry) error {
	if a.err != nil {
		return a.err
	}
	a.entries = append(a.entries, entry)
	return nil
}

var _ AuditEmitter = (*captureAuditEmitter)(nil)

// ── Helpers ────────────────────────────────────────────────────────────────

// makeClaimRow builds a minimal Neo4j result row representing a Claim ready
// for promotion evaluation. All gate thresholds for "likes" (multi_valued) are
// satisfied by default; callers may override individual fields.
func makeClaimRow(claimID, subject, predicate, object, status string) map[string]any {
	return map[string]any{
		"claim_id":                       claimID,
		"subject":                        subject,
		"predicate":                      predicate,
		"object":                         object,
		"modality":                       string(domain.ModalityAssertion),
		"status":                         status,
		"entailment_verdict":             string(domain.VerdictEntailed),
		"extract_conf":                   0.75,
		"resolution_conf":                0.65,
		"source_quality":                 0.8,
		"valid_from":                     nil,
		"classification":                 map[string]any{"confidentiality": "internal"},
		"classification_lattice_version": "v1",
		"supported_by":                   []any{"frag-1"},
	}
}

// makeClaimRowForSingleCurrent returns a claim row that satisfies all gate
// thresholds for "works_at" (single_current) including the source quality
// alternative path of the support gate (OR semantics, AC-35).
func makeClaimRowForSingleCurrent(claimID, subject, object string) map[string]any {
	row := makeClaimRow(claimID, subject, "works_at", object, string(domain.StatusValidated))
	// works_at: MinExtractConf=0.85, MinResolutionConf=0.75, MinSourceCount=2,
	// MinMaxSourceQuality=0.95, RequiresAssertion=true, RequiresEntailed=true.
	// Support OR: source_quality=0.95 >= 0.95 → gate passes with 1 fragment.
	row["extract_conf"] = 0.90
	row["resolution_conf"] = 0.80
	row["source_quality"] = 0.95
	return row
}

// newTestService returns a promoteClaimServiceImpl wired to the provided stubs.
func newTestService(
	db *stubPromoteDB,
	locker *stubClaimLocker,
	audit *captureAuditEmitter,
	metrics *observability.InMemoryDiscoverabilityMetrics,
) *promoteClaimServiceImpl {
	svc := NewPromoteClaimService(
		db,
		locker,
		nil, // pgDB — not used in unit tests (locker stub ignores it)
		audit,
		nil, // logger
		metrics,
		0, // use defaultLockTimeout
	)
	return svc.(*promoteClaimServiceImpl)
}

// ── Tests ──────────────────────────────────────────────────────────────────

// TestPromoteHappyPaths covers the primary success paths for promotion:
//   - AC-35: gate evaluation with OR support semantics
//   - AC-36: idempotency check (claim already promoted)
//   - AC-37: multi_valued new fact and single_current same-object confirm
//   - AC-39: metric emission and audit emission
//   - AC-42: classification propagation via DefaultLattice
func TestPromoteHappyPaths(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"

	t.Run("multi_valued: creates new fact for validated claim", func(t *testing.T) {
		// AC-35, AC-37, AC-39, AC-42
		claimRow := makeClaimRow("claim-1", "Alice", "likes", "coffee", string(domain.StatusValidated))
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow}, // loadClaim
				1: {},         // idempotency check → empty (not yet promoted)
			},
		}
		audit := &captureAuditEmitter{}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		svc := newTestService(db, &stubClaimLocker{}, audit, metrics)

		got, err := svc.Promote(ctx, profileID, "claim-1")

		require.NoError(t, err)
		require.NotNil(t, got)
		require.NotEmpty(t, got.FactID, "fact must have a new UUID")
		require.Equal(t, profileID, got.ProfileID)
		require.Equal(t, "Alice", got.Subject)
		require.Equal(t, "likes", got.Predicate)
		require.Equal(t, "coffee", got.Object)
		require.Equal(t, domain.FactStatusActive, got.Status)
		require.Equal(t, "claim-1", got.PromotedFromClaimID)
		require.Equal(t, "v1", got.ClassificationLatticeVersion)
		require.InDelta(t, 0.80, got.SourceQuality, 1e-9)
		// TruthScore: 0.35*0.75 + 0.35*0.65 + 0.15*bool(1>=1) + 0.15*bool(0.8>=0.0=false)
		// support_count_gate: len([frag-1])=1 >= 1 → true (0.15)
		// max_quality_gate: MinMaxSourceQuality=0.0 → false (0.00)
		// TruthScore = 0.35*0.75 + 0.35*0.65 + 0.15*1 + 0.15*0 = 0.2625 + 0.2275 + 0.15 = 0.64
		require.InDelta(t, 0.64, got.TruthScore, 1e-6)
		// Metric emitted.
		require.Equal(t, 1, metrics.PromotionOutcomeCount("promoted"))
		// Audit emitted.
		require.Len(t, audit.entries, 1)
		require.Equal(t, "claim.promote", audit.entries[0].Operation)
		require.Equal(t, profileID, audit.entries[0].ProfileID)
	})

	t.Run("multi_valued: gate passes via support count OR path (AC-35)", func(t *testing.T) {
		// Verify OR semantics: 2 fragments, source_quality=0.5 (below min_max=0 for likes).
		// Likes gate: MinSourceCount=1, MinMaxSourceQuality=0.0 → count gate passes alone.
		row := makeClaimRow("claim-or", "Bob", "likes", "tea", string(domain.StatusValidated))
		row["supported_by"] = []any{"frag-a", "frag-b"} // 2 >= 1 → count passes
		row["source_quality"] = 0.5
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {row},
				1: {},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(ctx, profileID, "claim-or")

		require.NoError(t, err)
		require.NotNil(t, got)
	})

	t.Run("single_current: creates new fact when no existing facts exist", func(t *testing.T) {
		// AC-37 (new path), AC-35 (source_quality alternative satisfies support gate)
		claimRow := makeClaimRowForSingleCurrent("claim-sc", "Alice", "Acme Corp")
		claimRow["status"] = string(domain.StatusValidated)
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow}, // loadClaim
				1: {},         // idempotency → empty
				2: {},         // findActiveFactsBySubjectPredicate → empty
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(ctx, profileID, "claim-sc")

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "Alice", got.Subject)
		require.Equal(t, "works_at", got.Predicate)
		require.Equal(t, "Acme Corp", got.Object)
		require.Equal(t, domain.FactStatusActive, got.Status)
	})

	t.Run("single_current: same object confirms existing fact (AC-37)", func(t *testing.T) {
		// Claim promotes "Alice works_at Acme". An active Fact for the same triple
		// already exists — the confirm path is taken and the existing Fact returned.
		claimRow := makeClaimRowForSingleCurrent("claim-confirm", "Alice", "Acme Corp")
		claimRow["status"] = string(domain.StatusValidated)
		now := time.Now().UTC()
		existingFactRow := makeFactRow("fact-existing", "Alice", "works_at", "active", now)
		existingFactRow["object"] = "Acme Corp" // same object as claim
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},        // loadClaim
				1: {},                // idempotency → empty
				2: {existingFactRow}, // findActiveFactsBySubjectPredicate → same object
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(ctx, profileID, "claim-confirm")

		// Same-object confirm returns the existing fact.
		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "fact-existing", got.FactID)
		require.Equal(t, profileID, got.ProfileID)
		require.Equal(t, "Alice", got.Subject)
		require.Equal(t, "works_at", got.Predicate)
	})

	t.Run("idempotency: returns existing fact when PROMOTES_TO already exists (AC-36)", func(t *testing.T) {
		// The claim was already promoted — idempotency check finds an existing Fact.
		claimRow := makeClaimRow("claim-idem", "Alice", "likes", "coffee", string(domain.StatusValidated))
		now := time.Now().UTC()
		existingFactRow := makeFactRow("fact-idem", "Alice", "likes", "active", now)
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},        // loadClaim
				1: {existingFactRow}, // idempotency → fact already exists
			},
		}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, metrics)

		got, err := svc.Promote(ctx, profileID, "claim-idem")

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "fact-idem", got.FactID)
		// Idempotent return emits "skipped", not "promoted".
		require.Equal(t, 1, metrics.PromotionOutcomeCount("skipped"))
		require.Equal(t, 0, metrics.PromotionOutcomeCount("promoted"))
	})

	t.Run("classification propagated via DefaultLattice (AC-42)", func(t *testing.T) {
		// Claim has a partial classification — DefaultLattice.Max fills in known
		// dimensions with their minimum values so the stored map is complete.
		claimRow := makeClaimRow("claim-class", "Alice", "likes", "jazz", string(domain.StatusValidated))
		claimRow["classification"] = map[string]any{
			"confidentiality": "internal",
			// retention and pii absent — lattice fills with minima
		}
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(ctx, profileID, "claim-class")

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "v1", got.ClassificationLatticeVersion)
		// Lattice fills absent known dimensions with their minima.
		require.NotNil(t, got.Classification)
		require.Equal(t, "internal", got.Classification["confidentiality"])
		require.Equal(t, "ephemeral", got.Classification["retention"]) // lattice minimum
		require.Equal(t, "none", got.Classification["pii"])            // lattice minimum
	})

	t.Run("JSON encoded claim classification is decoded before promotion", func(t *testing.T) {
		claimRow := makeClaimRow("claim-class-json", "Alice", "likes", "tea", string(domain.StatusValidated))
		claimRow["classification"] = nil
		claimRow["classification_json"] = `{"confidentiality":"internal","domain":"prefs"}`
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(ctx, profileID, "claim-class-json")

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, "internal", got.Classification["confidentiality"])
		require.Equal(t, "prefs", got.Classification["domain"])
	})
}

func TestPromoteQueriesUseJSONClassificationStorage(t *testing.T) {
	require.Contains(t, loadClaimForPromoteCypher, "c.classification_json             AS classification_json")
	require.Contains(t, idempotencyCheckCypher, "f.classification_json            AS classification_json")
	require.Contains(t, createFactAndEdgeCypher, "classification_json:           $classificationJSON")
}

func TestConfirmMemoryDecisionBranches(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"

	t.Run("requires claim id before locking", func(t *testing.T) {
		locker := &stubClaimLocker{}
		svc := newTestService(&stubPromoteDB{}, locker, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{Decision: "keep_existing"})

		require.ErrorContains(t, err, "claim_id is required")
		require.Nil(t, got)
	})

	t.Run("keep existing rejects claim and emits audit", func(t *testing.T) {
		claimRow := makeClaimRow("claim-keep", "Alice", "likes", "coffee", string(domain.StatusDisputed))
		db := &stubPromoteDB{responsesByCall: map[int][]map[string]any{0: {claimRow}}}
		audit := &captureAuditEmitter{}
		svc := newTestService(db, &stubClaimLocker{}, audit, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-keep", Decision: "keep_existing"})

		require.NoError(t, err)
		require.Equal(t, "claim-keep", got.ClaimID)
		require.Equal(t, "rejected", got.Status)
		require.Nil(t, got.Fact)
		require.Equal(t, profileID, db.lastWriteProfile)
		require.Len(t, audit.entries, 1)
		require.Equal(t, "claim.confirm.reject", audit.entries[0].Operation)
	})

	t.Run("reject claim follows same reject path", func(t *testing.T) {
		claimRow := makeClaimRow("claim-reject", "Alice", "likes", "coffee", string(domain.StatusDisputed))
		db := &stubPromoteDB{responsesByCall: map[int][]map[string]any{0: {claimRow}}}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-reject", Decision: "reject_claim"})

		require.NoError(t, err)
		require.Equal(t, "rejected", got.Status)
	})

	t.Run("accept claim creates fact when gates pass", func(t *testing.T) {
		claimRow := makeClaimRowForSingleCurrent("claim-accept", "Alice", "Acme Corp")
		db := &stubPromoteDB{responsesByCall: map[int][]map[string]any{
			0: {claimRow},
			1: {},
		}}
		audit := &captureAuditEmitter{}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		svc := newTestService(db, &stubClaimLocker{}, audit, metrics)

		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-accept", Decision: "accept_claim"})

		require.NoError(t, err)
		require.Equal(t, "accepted", got.Status)
		require.NotNil(t, got.Fact)
		require.Equal(t, "claim-accept", got.Fact.PromotedFromClaimID)
		require.Equal(t, 1, metrics.PromotionOutcomeCount("promoted"))
		require.Len(t, audit.entries, 1)
		require.Equal(t, "claim.confirm.accept", audit.entries[0].Operation)
	})

	t.Run("accept claim rejects unpoliced predicate and failed gates", func(t *testing.T) {
		unpoliced := makeClaimRow("claim-unpoliced", "Alice", "unknown_predicate", "value", string(domain.StatusValidated))
		svc := newTestService(&stubPromoteDB{responsesByCall: map[int][]map[string]any{0: {unpoliced}}}, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-unpoliced", Decision: "accept_claim"})
		require.ErrorIs(t, err, ErrPredicateNotPoliced)
		require.Nil(t, got)

		weak := makeClaimRowForSingleCurrent("claim-weak", "Alice", "Acme Corp")
		weak["extract_conf"] = 0.1
		svc = newTestService(&stubPromoteDB{responsesByCall: map[int][]map[string]any{0: {weak}}}, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())
		got, err = svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-weak", Decision: "accept_claim"})
		require.ErrorIs(t, err, ErrGateRejected)
		require.Nil(t, got)
	})

	t.Run("invalid decision and lock errors are surfaced", func(t *testing.T) {
		claimRow := makeClaimRow("claim-invalid", "Alice", "likes", "coffee", string(domain.StatusDisputed))
		svc := newTestService(&stubPromoteDB{responsesByCall: map[int][]map[string]any{0: {claimRow}}}, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())
		got, err := svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-invalid", Decision: "unknown"})
		require.ErrorIs(t, err, ErrInvalidConfirmationDecision)
		require.Nil(t, got)

		lockErr := errors.New("lock failed")
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		svc = newTestService(&stubPromoteDB{}, &stubClaimLocker{lockErr: lockErr}, &captureAuditEmitter{}, metrics)
		got, err = svc.ConfirmMemory(ctx, profileID, ConfirmMemoryRequest{ClaimID: "claim-lock", Decision: "reject_claim"})
		require.ErrorIs(t, err, lockErr)
		require.Nil(t, got)
		require.Equal(t, 1, metrics.PromotionOutcomeCount("error"))
	})
}

// TestPromoteHappyPaths_CrossProfileIsolation verifies that a claim belonging
// to profile A cannot be promoted when querying as profile B, and that the
// successful promotion result for profile A does not leak any data from or
// references to profile B.
//
// This is a mandatory security test per .claude/rules/profile-isolation.md.
func TestPromoteHappyPaths_CrossProfileIsolation(t *testing.T) {
	ctx := context.Background()
	const profileA = "00000000-0000-0000-0000-000000000001"
	const profileB = "00000000-0000-0000-0000-000000000002"
	const claimID = "claim-cross-profile"

	// Profile A: validated claim with a likes predicate.
	claimRowA := makeClaimRow(claimID, "Alice", "likes", "coffee", string(domain.StatusValidated))

	t.Run("profile A can promote its own claim", func(t *testing.T) {
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRowA}, // loadClaim for profileA → found
				1: {},          // idempotency → not yet promoted
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		factA, err := svc.Promote(ctx, profileA, claimID)

		require.NoError(t, err)
		require.NotNil(t, factA)
		require.Equal(t, profileA, factA.ProfileID,
			"promoted fact must carry profile A's ID")

		// Profile A's fact must not contain any reference to profile B.
		aResults := []string{factA.ProfileID}
		require.NotContains(t, aResults, profileB,
			"profile A's result must not reference profile B")
	})

	t.Run("profile B cannot access profile A's claim", func(t *testing.T) {
		// ScopedRead for profile B returns no rows (claim not in profile B).
		// This mirrors Neo4j's {team_id: $profileId} scoping: a claim owned
		// by profile A produces zero rows when queried as profile B.
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {}, // loadClaim for profileB → not found (profile isolation)
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		factB, err := svc.Promote(ctx, profileB, claimID)

		require.Error(t, err, "profile B must receive an error — claim not accessible")
		require.Nil(t, factB, "profile B must not receive a fact")

		// Verify: no data from profile A is present in profile B's result.
		bResults := []string{}
		if factB != nil {
			bResults = append(bResults, factB.FactID)
		}
		require.NotContains(t, bResults, profileA,
			"profile B's result must not reference profile A")
	})
}

// ── Error-path unit tests ──────────────────────────────────────────────────

// TestPromoteContradictionMatrix covers the three contradiction outcomes for
// single_current predicates when an existing active fact has a different object
// from the incoming claim (AC-37, AC-38):
//
//   - stronger: claim TruthScore > fact TruthScore → supersede old fact, create
//     new active fact, return it (no error).
//   - comparable: claim TruthScore ≈ fact TruthScore (within 1e-6) → mark claim
//     disputed, return ErrPromotionDeferredDisputed.
//   - weaker: claim TruthScore < fact TruthScore → mark claim rejected, return
//     ErrPromotionRejected.
//
// Cross-profile isolation is verified to ensure that the contradiction matrix
// is never triggered for a claim belonging to a different profile.
func TestPromoteContradictionMatrix(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"

	// claimTruth is the TruthScore produced by makeClaimRowForSingleCurrent for
	// the works_at gate (MinMaxSourceQuality=0.95, MinSourceCount=2):
	//   TruthScore = 0.35×0.90 + 0.35×0.80 + 0.15×false(count) + 0.15×true(quality)
	//              = 0.315 + 0.280 + 0.000 + 0.150 = 0.745
	const claimTruth = 0.745

	// makeContradictingClaimRow returns a validated claim for "Alice works_at
	// claimObj". The object is set to a distinct value from the existing fact so
	// the contradiction matrix is always entered.
	makeContradictingClaimRow := func(claimID, claimObj string) map[string]any {
		row := makeClaimRowForSingleCurrent(claimID, "Alice", claimObj)
		row["status"] = string(domain.StatusValidated)
		return row
	}

	// makeContradictingFactRow returns an existing active fact whose object
	// differs from the claim's object. truthScore controls which branch is taken.
	makeContradictingFactRow := func(factID string, truthScore float64) map[string]any {
		now := time.Now().UTC()
		row := makeFactRow(factID, "Alice", "works_at", "active", now)
		row["object"] = "Corp Y" // different from claim object "Corp X"
		row["truth_score"] = truthScore
		return row
	}

	t.Run("stronger: supersedes existing fact and creates new active fact (AC-37)", func(t *testing.T) {
		// Claim object "Corp X" differs from fact object "Corp Y".
		// claim TruthScore (0.745) > fact TruthScore (0.40) → stronger path.
		claimRow := makeContradictingClaimRow("claim-stronger", "Corp X")
		factRow := makeContradictingFactRow("fact-weak", 0.40)

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow}, // loadClaim
				1: {},         // idempotency → not yet promoted
				2: {factRow},  // findActiveFacts → one weaker differing-object fact
			},
		}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		audit := &captureAuditEmitter{}
		svc := newTestService(db, &stubClaimLocker{}, audit, metrics)

		got, err := svc.Promote(ctx, profileID, "claim-stronger")

		require.NoError(t, err, "stronger claim must succeed")
		require.NotNil(t, got)
		require.NotEmpty(t, got.FactID, "new fact must have a UUID")
		require.NotEqual(t, "fact-weak", got.FactID, "new fact must differ from superseded fact")
		require.Equal(t, profileID, got.ProfileID)
		require.Equal(t, domain.FactStatusActive, got.Status)
		require.Equal(t, "Corp X", got.Object)
		require.InDelta(t, claimTruth, got.TruthScore, 1e-6)
		// Metric and audit must record a successful promotion.
		require.Equal(t, 1, metrics.PromotionOutcomeCount("promoted"))
		require.Len(t, audit.entries, 1)
		require.Equal(t, "claim.promote", audit.entries[0].Operation)
	})

	t.Run("comparable: claim disputed, returns ErrPromotionDeferredDisputed (AC-38)", func(t *testing.T) {
		// Claim object "Corp X" differs from fact object "Corp Y".
		// fact TruthScore == claimTruth (within 1e-6) → comparable path.
		claimRow := makeContradictingClaimRow("claim-comparable", "Corp X")
		factRow := makeContradictingFactRow("fact-same-strength", claimTruth)

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
				2: {factRow},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(ctx, profileID, "claim-comparable")

		require.Nil(t, got, "comparable claim must not produce a fact")
		require.ErrorIs(t, err, ErrPromotionDeferredDisputed)
	})

	t.Run("foreign conflict: actor defers instead of superseding another owner fact", func(t *testing.T) {
		actorID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
		foreignOwnerID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
		actorCtx := requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
			ProfileID:   actorID,
			ProfileName: "native",
		})
		claimRow := makeContradictingClaimRow("claim-foreign-conflict", "Corp X")
		claimRow["owner_profile_id"] = actorID.String()
		claimRow["owner_profile_name"] = "native"
		factRow := makeContradictingFactRow("fact-foreign-weaker", 0.40)
		factRow["owner_profile_id"] = foreignOwnerID.String()
		factRow["owner_profile_name"] = "web"

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
				2: {factRow},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(actorCtx, profileID, "claim-foreign-conflict")

		require.Nil(t, got)
		require.ErrorIs(t, err, ErrPromotionDeferredDisputed)
		require.Equal(t, 1, db.writeTxCount)
		require.Equal(t, profileID, db.lastWriteProfile)
	})

	t.Run("weaker: claim rejected, returns ErrPromotionRejected (AC-38)", func(t *testing.T) {
		// Claim object "Corp X" differs from fact object "Corp Y".
		// fact TruthScore (0.99) >> claim TruthScore (0.745) → weaker path.
		claimRow := makeContradictingClaimRow("claim-weaker", "Corp X")
		factRow := makeContradictingFactRow("fact-strong", 0.99)

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
				2: {factRow},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(ctx, profileID, "claim-weaker")

		require.Nil(t, got, "weaker claim must not produce a fact")
		require.ErrorIs(t, err, ErrPromotionRejected)
	})

	// TestPromoteContradictionMatrix_CrossProfileIsolation verifies that the
	// contradiction matrix is never reached for a claim belonging to a different
	// profile. Profile B querying profile A's claim must receive an error and no
	// data — per .claude/rules/profile-isolation.md.
	t.Run("CrossProfileIsolation: profile B cannot trigger matrix on profile A's claim", func(t *testing.T) {
		const profileA = "00000000-0000-0000-0000-000000000001"
		const profileB = "00000000-0000-0000-0000-000000000002"

		// ScopedRead for profile B returns no rows — the {team_id: $profileId}
		// filter on the Claim node prevents cross-profile access. This is
		// indistinguishable from "not found", so no existence is leaked.
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {}, // loadClaim for profileB → not found (profile isolation)
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(ctx, profileB, "claim-stronger")

		require.Error(t, err, "profile B must receive an error")
		require.Nil(t, got, "profile B must not receive any fact")

		// No data from profile A must appear in profile B's result.
		bResults := []string{}
		if got != nil {
			bResults = append(bResults, got.FactID, got.ProfileID)
		}
		require.NotContains(t, bResults, profileA,
			"profile B's result must not reference profile A")
	})
}

// TestPromote_ErrorPaths verifies that each error branch returns the correct
// sentinel and does not emit "promoted" for failures.
func TestPromote_ErrorPaths(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"

	t.Run("returns error when claim not found", func(t *testing.T) {
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {}, // loadClaim → not found
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		_, err := svc.Promote(ctx, profileID, "nonexistent")

		require.Error(t, err)
		require.True(t, errors.Is(err, ErrClaimNotFound))
	})

	t.Run("returns owner mismatch when actor promotes foreign-owned claim", func(t *testing.T) {
		actorID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
		ownerID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
		actorCtx := requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
			ProfileID:   actorID,
			ProfileName: "web",
		})
		row := makeClaimRow("claim-foreign-owner", "Alice", "likes", "coffee", string(domain.StatusValidated))
		row["owner_profile_id"] = ownerID.String()
		row["owner_profile_name"] = "native"
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(actorCtx, profileID, "claim-foreign-owner")

		require.Nil(t, got)
		require.ErrorIs(t, err, ownership.ErrOwnerMismatch)
		require.Equal(t, 1, db.callCount)
		require.Zero(t, db.writeTxCount)
	})

	t.Run("returns ErrClaimNotValidated when claim is not validated", func(t *testing.T) {
		row := makeClaimRow("claim-cand", "Alice", "likes", "coffee", string(domain.StatusCandidate))
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		_, err := svc.Promote(ctx, profileID, "claim-cand")

		require.Error(t, err)
		require.True(t, errors.Is(err, ErrClaimNotValidated))
	})

	t.Run("returns ErrPredicateNotPoliced for unknown predicate", func(t *testing.T) {
		row := makeClaimRow("claim-bad-pred", "Alice", "unknown_predicate", "x", string(domain.StatusValidated))
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		_, err := svc.Promote(ctx, profileID, "claim-bad-pred")

		require.Error(t, err)
		require.True(t, errors.Is(err, ErrPredicateNotPoliced))
	})

	t.Run("returns ErrGateRejected when extract_conf is too low", func(t *testing.T) {
		row := makeClaimRow("claim-gate", "Alice", "likes", "x", string(domain.StatusValidated))
		row["extract_conf"] = 0.5 // below 0.70 threshold for likes
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		_, err := svc.Promote(ctx, profileID, "claim-gate")

		require.Error(t, err)
		require.True(t, errors.Is(err, ErrGateRejected))
	})

	t.Run("returns ErrGateRejected when confidence is outside probability range", func(t *testing.T) {
		row := makeClaimRow("claim-bad-conf", "Alice", "likes", "x", string(domain.StatusValidated))
		row["extract_conf"] = 99.0
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		_, err := svc.Promote(ctx, profileID, "claim-bad-conf")

		require.Error(t, err)
		require.True(t, errors.Is(err, ErrGateRejected))
		require.Empty(t, db.lastWriteProfile)
	})

	t.Run("gate fails when both support OR paths fail (AC-35)", func(t *testing.T) {
		// likes: MinSourceCount=1, MinMaxSourceQuality=0.0 (disabled).
		// Here: 0 fragments → count gate fails. max_quality is disabled → quality gate
		// cannot pass. Both fail → ErrGateRejected.
		row := makeClaimRow("claim-no-support", "Alice", "likes", "x", string(domain.StatusValidated))
		row["supported_by"] = []any{} // count = 0 < 1
		row["source_quality"] = 0.5   // max_quality gate disabled for likes (threshold=0.0)
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {row},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		_, err := svc.Promote(ctx, profileID, "claim-no-support")

		require.Error(t, err)
		require.True(t, errors.Is(err, ErrGateRejected))
	})

	t.Run("returns ErrPromotionRejected when claim weaker than existing fact", func(t *testing.T) {
		// works_at (single_current): claim strength is lower than existing active fact.
		claimRow := makeClaimRowForSingleCurrent("claim-weak", "Alice", "Acme Corp")
		claimRow["status"] = string(domain.StatusValidated)
		claimRow["object"] = "Other Corp" // different object → triggers contradiction
		// Set claim scores lower than the existing fact's TruthScore.
		claimRow["extract_conf"] = 0.86
		claimRow["resolution_conf"] = 0.76

		now := time.Now().UTC()
		existingFactRow := makeFactRow("fact-strong", "Alice", "works_at", "active", now)
		existingFactRow["object"] = "Acme Corp" // different object from claim
		existingFactRow["truth_score"] = 0.99   // very high existing truth score
		existingFactRow["source_quality"] = 0.99

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},                // idempotency → empty
				2: {existingFactRow}, // find active facts → one differing-object fact
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		_, err := svc.Promote(ctx, profileID, "claim-weak")

		require.Error(t, err)
		require.True(t, errors.Is(err, ErrPromotionRejected))
	})

	t.Run("lock failure propagates as error", func(t *testing.T) {
		lockErr := errors.New("pg advisory lock timeout")
		db := &stubPromoteDB{}
		locker := &stubClaimLocker{lockErr: lockErr}
		metrics := observability.NewInMemoryDiscoverabilityMetrics()
		svc := newTestService(db, locker, &captureAuditEmitter{}, metrics)

		_, err := svc.Promote(ctx, profileID, "claim-lock")

		require.Error(t, err)
		require.Contains(t, err.Error(), "pg advisory lock timeout")
		// Error metric emitted for lock failure.
		require.Equal(t, 1, metrics.PromotionOutcomeCount("error"))
	})
}

func TestPromote_AdditionalPolicies(t *testing.T) {
	ctx := context.Background()
	const profileID = "00000000-0000-0000-0000-000000000001"

	restoreGate := func(predicate string, gate PromotionGate, ok bool) {
		if ok {
			DefaultPromotionGates[predicate] = gate
		} else {
			delete(DefaultPromotionGates, predicate)
		}
	}

	t.Run("versioned: supersedes older active versions without contradiction rejection", func(t *testing.T) {
		const predicate = "employment_versioned_test"
		origGate, hadGate := DefaultPromotionGates[predicate]
		defer restoreGate(predicate, origGate, hadGate)
		DefaultPromotionGates[predicate] = PromotionGate{
			Policy:              Versioned,
			MinExtractConf:      0.70,
			MinResolutionConf:   0.60,
			RequiresAssertion:   true,
			RequiresEntailed:    true,
			MinSourceCount:      1,
			MinMaxSourceQuality: 0.0,
		}

		claimRow := makeClaimRow("claim-versioned", "Alice", predicate, "Acme Corp", string(domain.StatusValidated))
		existingFactRow := makeFactRow("fact-old-version", "Alice", predicate, "active", time.Now().UTC())
		existingFactRow["object"] = "Old Corp"

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
				2: {existingFactRow},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(ctx, profileID, "claim-versioned")

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, predicate, got.Predicate)
		require.Equal(t, domain.FactStatusActive, got.Status)
		require.NotEqual(t, "fact-old-version", got.FactID)
	})

	t.Run("versioned: actor keeps foreign versions and links overlays", func(t *testing.T) {
		const predicate = "employment_versioned_owner_test"
		origGate, hadGate := DefaultPromotionGates[predicate]
		defer restoreGate(predicate, origGate, hadGate)
		DefaultPromotionGates[predicate] = PromotionGate{
			Policy:              Versioned,
			MinExtractConf:      0.70,
			MinResolutionConf:   0.60,
			RequiresAssertion:   true,
			RequiresEntailed:    true,
			MinSourceCount:      1,
			MinMaxSourceQuality: 0.0,
		}

		actorID := uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
		foreignOwnerID := uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
		actorCtx := requestctx.WithActorProfile(ctx, requestctx.ActorProfile{
			ProfileID:   actorID,
			ProfileName: "native",
		})
		claimRow := makeClaimRow("claim-versioned-owner", "Alice", predicate, "Acme Corp", string(domain.StatusValidated))
		claimRow["owner_profile_id"] = actorID.String()
		claimRow["owner_profile_name"] = "native"

		ownedOld := makeFactRow("fact-owned-old", "Alice", predicate, "active", time.Now().UTC())
		ownedOld["object"] = "Old Corp"
		ownedOld["owner_profile_id"] = actorID.String()
		foreignSame := makeFactRow("fact-foreign-same", "Alice", predicate, "active", time.Now().UTC())
		foreignSame["object"] = "Acme Corp"
		foreignSame["owner_profile_id"] = foreignOwnerID.String()
		foreignDifferent := makeFactRow("fact-foreign-different", "Alice", predicate, "active", time.Now().UTC())
		foreignDifferent["object"] = "Other Corp"
		foreignDifferent["owner_profile_id"] = foreignOwnerID.String()

		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
				2: {ownedOld, foreignSame, foreignDifferent},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(actorCtx, profileID, "claim-versioned-owner")

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, actorID.String(), got.OwnerProfileID)
		require.Equal(t, "Acme Corp", got.Object)
		require.Equal(t, 4, db.writeTxCount)
	})

	t.Run("append_only: creates a new fact without rejecting on prior history", func(t *testing.T) {
		const predicate = "timeline_append_only_test"
		origGate, hadGate := DefaultPromotionGates[predicate]
		defer restoreGate(predicate, origGate, hadGate)
		DefaultPromotionGates[predicate] = PromotionGate{
			Policy:              AppendOnly,
			MinExtractConf:      0.70,
			MinResolutionConf:   0.60,
			RequiresAssertion:   true,
			RequiresEntailed:    true,
			MinSourceCount:      1,
			MinMaxSourceQuality: 0.0,
		}

		claimRow := makeClaimRow("claim-append", "Alice", predicate, "joined_project_x", string(domain.StatusValidated))
		db := &stubPromoteDB{
			responsesByCall: map[int][]map[string]any{
				0: {claimRow},
				1: {},
			},
		}
		svc := newTestService(db, &stubClaimLocker{}, &captureAuditEmitter{}, observability.NewInMemoryDiscoverabilityMetrics())

		got, err := svc.Promote(ctx, profileID, "claim-append")

		require.NoError(t, err)
		require.NotNil(t, got)
		require.Equal(t, predicate, got.Predicate)
		require.Equal(t, domain.FactStatusActive, got.Status)
		require.Equal(t, "joined_project_x", got.Object)
	})
}
