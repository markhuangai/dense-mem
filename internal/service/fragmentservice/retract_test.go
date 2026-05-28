package fragmentservice

import (
	"context"
	"errors"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
)

// fakeRetractDB implements retractDB for unit tests.
//
// ScopedWriteTx does NOT invoke fn because neo4j.ManagedTransaction carries an
// unexported legacy() method that prevents external stub implementations.
// Query-level correctness is verified by integration tests against a real Neo4j
// instance; unit tests here focus on profileID routing, error propagation, and
// post-tx side-effect emission (metrics, audit).
type fakeRetractDB struct {
	calledProfileID string
	txErr           error
}

func (f *fakeRetractDB) ScopedWriteTx(
	_ context.Context,
	profileID string,
	_ func(tx neo4j.ManagedTransaction) error,
) error {
	f.calledProfileID = profileID
	return f.txErr
}

var _ retractDB = (*fakeRetractDB)(nil)

// ---- tests ----------------------------------------------------------------

func TestRetractFragment_Success_EmitsMetricsAndAudit(t *testing.T) {
	db := &fakeRetractDB{txErr: nil}
	audit := &fakeAudit{}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()

	svc := NewRetractFragmentService(db, audit, nil, metrics)
	err := svc.Retract(context.Background(), "pA", "frag-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Fragment retract metric incremented after successful tx.
	if got := metrics.FragmentRetractCount(); got != 1 {
		t.Errorf("FragmentRetractCount = %d; want 1", got)
	}
	// No revalidation occurred (fn not invoked, count=0).
	if got := metrics.FactNeedsRevalidationCount(); got != 0 {
		t.Errorf("FactNeedsRevalidationCount = %d; want 0", got)
	}

	// Audit event emitted with correct operation.
	if audit.EventCount != 1 {
		t.Fatalf("audit.EventCount = %d; want 1", audit.EventCount)
	}
	if audit.LastEntry.Operation != "fragment.retract" {
		t.Errorf("audit operation = %q; want fragment.retract", audit.LastEntry.Operation)
	}
	if audit.LastEntry.EntityID != "frag-1" {
		t.Errorf("audit EntityID = %q; want frag-1", audit.LastEntry.EntityID)
	}
}

func TestRetractFragment_NotFound_PropagatesError(t *testing.T) {
	// When the tx-local existence check inside fn detects a missing fragment it
	// returns ErrFragmentNotFound; ScopedWriteTx propagates it to the service.
	db := &fakeRetractDB{txErr: ErrFragmentNotFound}
	audit := &fakeAudit{}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()

	svc := NewRetractFragmentService(db, audit, nil, metrics)

	err := svc.Retract(context.Background(), "pA", "nope")
	if !errors.Is(err, ErrFragmentNotFound) {
		t.Errorf("err = %v; want ErrFragmentNotFound", err)
	}

	// No metrics or audit on missing fragment.
	if got := metrics.FragmentRetractCount(); got != 0 {
		t.Errorf("FragmentRetractCount = %d; want 0 (no commit on not-found)", got)
	}
	if audit.EventCount != 0 {
		t.Errorf("audit must not fire on missing fragment; EventCount = %d", audit.EventCount)
	}
}

func TestRetractFragment_CrossProfileIsolation(t *testing.T) {
	// Profile pB requests a fragment owned by pA.
	// The ScopedWriteTx implementation enforces profile scope so pA's data is
	// never returned to pB. The service must route the call with pB's profileID,
	// not pA's.
	db := &fakeRetractDB{txErr: ErrFragmentNotFound}

	svc := NewRetractFragmentService(db, nil, nil, nil)
	err := svc.Retract(context.Background(), "pB", "frag-owned-by-pA")

	if !errors.Is(err, ErrFragmentNotFound) {
		t.Errorf("err = %v; want ErrFragmentNotFound (cross-profile must not leak)", err)
	}
	// Verify the tx was opened with pB — not pA.
	if db.calledProfileID != "pB" {
		t.Errorf("ScopedWriteTx called with profile %q; want pB (pB scope enforced)", db.calledProfileID)
	}
}

func TestRetractFragment_TxFailure_ReturnsError_NoSideEffects(t *testing.T) {
	// When the transaction fails for any reason (tombstone write error, etc.)
	// the service must propagate the error and must NOT emit metrics or audit.
	db := &fakeRetractDB{txErr: errors.New("neo4j down")}
	audit := &fakeAudit{}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()

	svc := NewRetractFragmentService(db, audit, nil, metrics)

	err := svc.Retract(context.Background(), "pA", "frag-1")
	if err == nil {
		t.Fatal("expected error when tx fails")
	}

	// No partial state: metrics and audit must be silent on tx failure.
	if got := metrics.FragmentRetractCount(); got != 0 {
		t.Errorf("FragmentRetractCount = %d; want 0 (no commit on failure)", got)
	}
	if got := metrics.FactNeedsRevalidationCount(); got != 0 {
		t.Errorf("FactNeedsRevalidationCount = %d; want 0 (no commit on failure)", got)
	}
	if audit.EventCount != 0 {
		t.Errorf("audit must not fire when tx fails; EventCount = %d", audit.EventCount)
	}
}

func TestRetractFragment_Atomicity_MidTxFailure_NoPartialState(t *testing.T) {
	// Simulate a mid-transaction failure (e.g., network partition after tombstone
	// but before the revalidation UNWIND SET). The neo4j driver rolls back the
	// entire transaction, so neither the tombstone nor any revalidation flag is
	// committed. The service must propagate the error and emit no side effects.
	midTxErr := errors.New("simulated mid-tx failure")
	db := &fakeRetractDB{txErr: midTxErr}
	audit := &fakeAudit{}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()

	svc := NewRetractFragmentService(db, audit, nil, metrics)

	err := svc.Retract(context.Background(), "pA", "frag-1")
	if err == nil {
		t.Fatal("expected error on mid-tx failure")
	}

	// No partial state: the service must not emit any counter or audit entry
	// because the transaction was never committed.
	if got := metrics.FragmentRetractCount(); got != 0 {
		t.Errorf("FragmentRetractCount = %d; want 0 (tx rolled back)", got)
	}
	if got := metrics.FactNeedsRevalidationCount(); got != 0 {
		t.Errorf("FactNeedsRevalidationCount = %d; want 0 (tx rolled back)", got)
	}
	if audit.EventCount != 0 {
		t.Errorf("audit.EventCount = %d; want 0 (tx rolled back, no partial state)", audit.EventCount)
	}
}

func TestRetractFragment_ProfileID_RoutedToTx(t *testing.T) {
	// Verify profileID propagation: every ScopedWriteTx call carries the caller's
	// profileID so the neo4j driver enforces the per-profile isolation boundary.
	db := &fakeRetractDB{txErr: nil}

	svc := NewRetractFragmentService(db, nil, nil, nil)
	if err := svc.Retract(context.Background(), "profile-XYZ", "frag-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if db.calledProfileID != "profile-XYZ" {
		t.Errorf("ScopedWriteTx called with profile %q; want profile-XYZ", db.calledProfileID)
	}
}

func TestRetractFragment_AuditPayload_ExcludesContent(t *testing.T) {
	db := &fakeRetractDB{txErr: nil}
	audit := &fakeAudit{}

	svc := NewRetractFragmentService(db, audit, nil, nil)
	if err := svc.Retract(context.Background(), "pA", "frag-secret"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := audit.LastEntry.AfterPayload["content"]; ok {
		t.Error("audit AfterPayload must not contain content key")
	}
	if _, ok := audit.LastEntry.AfterPayload["embedding"]; ok {
		t.Error("audit AfterPayload must not contain embedding key")
	}
	if got := audit.LastEntry.AfterPayload["fragment_id"]; got != "frag-secret" {
		t.Errorf("audit AfterPayload[fragment_id] = %v; want frag-secret", got)
	}
	if got := audit.LastEntry.AfterPayload["team_id"]; got != "pA" {
		t.Errorf("audit AfterPayload[team_id] = %v; want pA", got)
	}
}

func TestRetractFragment_AuditCarriesCorrelationID(t *testing.T) {
	db := &fakeRetractDB{txErr: nil}
	audit := &fakeAudit{}

	svc := NewRetractFragmentService(db, audit, nil, nil)

	ctx := correlation.WithID(context.Background(), "corr-retract-xyz")
	if err := svc.Retract(ctx, "pA", "frag-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if audit.LastEntry.CorrelationID != "corr-retract-xyz" {
		t.Errorf("CorrelationID = %q; want corr-retract-xyz", audit.LastEntry.CorrelationID)
	}
}

// ---- passesGate unit tests -------------------------------------------------
//
// These tests call passesGate directly to verify the OR-semantics support gate
// and the deny-by-default rule (AC-34, AC-35). They do not depend on ScopedWriteTx
// being invoked and provide coverage for the gate evaluation logic.

func TestRetractFragment_PassesGate_OrSemantics_QualityArmAlone(t *testing.T) {
	// AC-35: quality arm alone satisfies gate even when count is zero.
	// born_on gate: MinSourceCount=1, MinMaxSourceQuality=0.95.
	// count=0 (fails count arm) but quality=0.97 (passes quality arm) → gate passes.
	svc := &retractFragmentService{gates: factservice.DefaultPromotionGates}
	if !svc.passesGate("born_on", 0, 0.97) {
		t.Error("passesGate(born_on, count=0, quality=0.97) = false; want true (quality arm passes)")
	}
}

func TestRetractFragment_PassesGate_OrSemantics_CountArmAlone(t *testing.T) {
	// AC-35: count arm alone satisfies gate.
	// born_on gate: MinSourceCount=1, MinMaxSourceQuality=0.95.
	// count=1 (passes count arm), quality=0.0 (fails quality arm) → gate passes.
	svc := &retractFragmentService{gates: factservice.DefaultPromotionGates}
	if !svc.passesGate("born_on", 1, 0.0) {
		t.Error("passesGate(born_on, count=1, quality=0.0) = false; want true (count arm passes)")
	}
}

func TestRetractFragment_PassesGate_BothArmsFail(t *testing.T) {
	// Both arms fail → gate fails.
	// born_on gate: MinSourceCount=1, MinMaxSourceQuality=0.95.
	// count=0, quality=0.0 → both fail.
	svc := &retractFragmentService{gates: factservice.DefaultPromotionGates}
	if svc.passesGate("born_on", 0, 0.0) {
		t.Error("passesGate(born_on, count=0, quality=0.0) = true; want false (both arms fail)")
	}
}

func TestRetractFragment_PassesGate_UnknownPredicate_DenyByDefault(t *testing.T) {
	// AC-34: predicates not in DefaultPromotionGates are denied by default.
	svc := &retractFragmentService{gates: factservice.DefaultPromotionGates}
	if svc.passesGate("unknown_predicate", 100, 1.0) {
		t.Error("passesGate(unknown_predicate, ...) = true; want false (deny by default for unknown predicates)")
	}
}

func TestRetractFragment_PassesGate_WorksAt_MultiSourceRequired(t *testing.T) {
	// works_at gate: MinSourceCount=2, MinMaxSourceQuality=0.95.
	// count=1 fails count arm; quality=0.94 fails quality arm → gate fails.
	svc := &retractFragmentService{gates: factservice.DefaultPromotionGates}
	if svc.passesGate("works_at", 1, 0.94) {
		t.Error("passesGate(works_at, count=1, quality=0.94) = true; want false")
	}
	// count=2 satisfies count arm → gate passes.
	if !svc.passesGate("works_at", 2, 0.0) {
		t.Error("passesGate(works_at, count=2, quality=0.0) = false; want true (count arm passes)")
	}
}
