package fragmentservice

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/correlation"
)

func TestDelete_Success_EmitsAudit(t *testing.T) {
	reader := &fakeScopedReader{
		results: []map[string]any{
			{"fragment_id": "frag-1"},
		},
	}
	writer := &fakeScopedWriter{}
	audit := &fakeAudit{}
	svc := NewDeleteFragmentService(writer, reader, audit, nil)

	err := svc.Delete(context.Background(), "pA", "frag-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if writer.WriteCount != 1 {
		t.Errorf("writer.WriteCount = %d; want 1", writer.WriteCount)
	}
	if writer.LastProfileID != "pA" {
		t.Errorf("writer.LastProfileID = %q; want pA", writer.LastProfileID)
	}
	if !strings.Contains(writer.LastQuery, "DETACH DELETE") {
		t.Errorf("delete query missing DETACH DELETE: %s", writer.LastQuery)
	}
	if audit.EventCount != 1 {
		t.Fatalf("audit.EventCount = %d; want 1", audit.EventCount)
	}
	if audit.LastEntry.Operation != "fragment.delete" {
		t.Errorf("audit operation = %q; want fragment.delete", audit.LastEntry.Operation)
	}
	if audit.LastEntry.EntityID != "frag-1" {
		t.Errorf("audit entity id = %q; want frag-1", audit.LastEntry.EntityID)
	}
}

func TestDelete_ReturnsNotFound_WhenMissing(t *testing.T) {
	reader := &fakeScopedReader{results: []map[string]any{}}
	writer := &fakeScopedWriter{}
	audit := &fakeAudit{}
	svc := NewDeleteFragmentService(writer, reader, audit, nil)

	err := svc.Delete(context.Background(), "pA", "nope")
	if !errors.Is(err, ErrFragmentNotFound) {
		t.Errorf("err = %v; want ErrFragmentNotFound", err)
	}
	if writer.WriteCount != 0 {
		t.Errorf("writer must not be called when fragment missing; WriteCount = %d", writer.WriteCount)
	}
	if audit.EventCount != 0 {
		t.Errorf("audit must not fire on missing fragment; EventCount = %d", audit.EventCount)
	}
}

func TestDelete_CrossProfile_ReturnsNotFound(t *testing.T) {
	// The reader is scoped by profile, so a fragment owned by pA is invisible to pB —
	// modeled by an empty pre-flight result set.
	reader := &fakeScopedReader{results: []map[string]any{}}
	writer := &fakeScopedWriter{}
	audit := &fakeAudit{}
	svc := NewDeleteFragmentService(writer, reader, audit, nil)

	err := svc.Delete(context.Background(), "pB", "frag-owned-by-pA")
	if !errors.Is(err, ErrFragmentNotFound) {
		t.Errorf("err = %v; want ErrFragmentNotFound (cross-profile must not leak existence)", err)
	}
	if reader.lastProfileID != "pB" {
		t.Errorf("reader lastProfileID = %q; want pB (caller scope applied)", reader.lastProfileID)
	}
	if writer.WriteCount != 0 {
		t.Errorf("writer must not be called on cross-profile miss; WriteCount = %d", writer.WriteCount)
	}
}

func TestDelete_AuditPayload_ExcludesContent(t *testing.T) {
	reader := &fakeScopedReader{
		results: []map[string]any{{"fragment_id": "frag-secret"}},
	}
	writer := &fakeScopedWriter{}
	audit := &fakeAudit{}
	svc := NewDeleteFragmentService(writer, reader, audit, nil)

	if err := svc.Delete(context.Background(), "pA", "frag-secret"); err != nil {
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

// TestDelete_AuditCarriesCorrelationID pins AC-54: the delete audit row is
// stamped with the upstream request id so operators can trace the call.
func TestDelete_AuditCarriesCorrelationID(t *testing.T) {
	reader := &fakeScopedReader{results: []map[string]any{{"fragment_id": "frag-1"}}}
	writer := &fakeScopedWriter{}
	audit := &fakeAudit{}
	svc := NewDeleteFragmentService(writer, reader, audit, nil)

	ctx := correlation.WithID(context.Background(), "corr-delete-abc")
	if err := svc.Delete(ctx, "pA", "frag-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if audit.LastEntry.CorrelationID != "corr-delete-abc" {
		t.Errorf("CorrelationID = %q; want %q", audit.LastEntry.CorrelationID, "corr-delete-abc")
	}
}

func TestDelete_WriteFailure_NoAudit(t *testing.T) {
	reader := &fakeScopedReader{
		results: []map[string]any{{"fragment_id": "frag-1"}},
	}
	writer := &fakeScopedWriter{WriteErr: errors.New("neo4j down")}
	audit := &fakeAudit{}
	svc := NewDeleteFragmentService(writer, reader, audit, nil)

	err := svc.Delete(context.Background(), "pA", "frag-1")
	if err == nil {
		t.Fatal("expected error when writer fails")
	}
	if audit.EventCount != 0 {
		t.Errorf("audit must not fire when write fails; EventCount = %d", audit.EventCount)
	}
}
