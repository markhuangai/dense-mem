package factservice

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/ownership"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
)

type fakeRetractFactDB struct {
	calledProfileID string
	txErr           error
}

func (f *fakeRetractFactDB) ScopedWriteTx(
	_ context.Context,
	profileID string,
	_ func(tx neo4j.ManagedTransaction) error,
) error {
	f.calledProfileID = profileID
	return f.txErr
}

var _ retractFactDB = (*fakeRetractFactDB)(nil)

func TestRetractFact_Success_EmitsAudit(t *testing.T) {
	db := &fakeRetractFactDB{}
	audit := &captureAuditEmitter{}

	svc := NewRetractFactService(db, audit, nil)
	if err := svc.Retract(context.Background(), "profile-a", "fact-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if db.calledProfileID != "profile-a" {
		t.Fatalf("ScopedWriteTx profile = %q; want profile-a", db.calledProfileID)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("audit entries = %d; want 1", len(audit.entries))
	}
	entry := audit.entries[0]
	if entry.Operation != "fact.retract" {
		t.Errorf("operation = %q; want fact.retract", entry.Operation)
	}
	if entry.EntityID != "fact-1" {
		t.Errorf("EntityID = %q; want fact-1", entry.EntityID)
	}
	if got := entry.AfterPayload["status"]; got != "retracted" {
		t.Errorf("AfterPayload[status] = %v; want retracted", got)
	}
}

func TestRetractFact_NotFound_NoAudit(t *testing.T) {
	db := &fakeRetractFactDB{txErr: ErrFactNotFound}
	audit := &captureAuditEmitter{}

	svc := NewRetractFactService(db, audit, nil)
	err := svc.Retract(context.Background(), "profile-a", "missing")

	if !errors.Is(err, ErrFactNotFound) {
		t.Fatalf("err = %v; want ErrFactNotFound", err)
	}
	if len(audit.entries) != 0 {
		t.Fatalf("audit entries = %d; want 0", len(audit.entries))
	}
}

func TestRetractFact_OwnerMismatch_NoAudit(t *testing.T) {
	db := &fakeRetractFactDB{txErr: ownership.ErrOwnerMismatch}
	audit := &captureAuditEmitter{}

	svc := NewRetractFactService(db, audit, nil)
	err := svc.Retract(context.Background(), "profile-a", "fact-owned-by-b")

	if !errors.Is(err, ownership.ErrOwnerMismatch) {
		t.Fatalf("err = %v; want ErrOwnerMismatch", err)
	}
	if len(audit.entries) != 0 {
		t.Fatalf("audit entries = %d; want 0", len(audit.entries))
	}
}

func TestRetractFact_TxFailure_ReturnsError_NoAudit(t *testing.T) {
	db := &fakeRetractFactDB{txErr: errors.New("neo4j down")}
	audit := &captureAuditEmitter{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	svc := NewRetractFactService(db, audit, logger)
	err := svc.Retract(context.Background(), "profile-a", "fact-1")

	if err == nil {
		t.Fatal("expected error when tx fails")
	}
	if len(audit.entries) != 0 {
		t.Fatalf("audit entries = %d; want 0", len(audit.entries))
	}
}

func TestRetractFact_AuditCarriesCorrelationID(t *testing.T) {
	db := &fakeRetractFactDB{}
	audit := &captureAuditEmitter{}
	svc := NewRetractFactService(db, audit, nil)

	ctx := correlation.WithID(context.Background(), "corr-fact-retract")
	if err := svc.Retract(ctx, "profile-a", "fact-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := audit.entries[0].CorrelationID; got != "corr-fact-retract" {
		t.Fatalf("CorrelationID = %q; want corr-fact-retract", got)
	}
}

func TestRetractFact_AuditFailureDoesNotFailRetraction(t *testing.T) {
	db := &fakeRetractFactDB{}
	audit := &captureAuditEmitter{err: errors.New("audit down")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	svc := NewRetractFactService(db, audit, logger)
	if err := svc.Retract(context.Background(), "profile-a", "fact-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
