package fragmentservice

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"

	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/http/dto"
)

// --- Fakes ---

type fakeScopedWriter struct {
	mu            sync.Mutex
	WriteCount    int
	LastProfileID string
	LastQuery     string
	LastParams    map[string]any
	WriteErr      error
}

func (f *fakeScopedWriter) ScopedWrite(ctx context.Context, profileID string, query string, params map[string]any) (neo4j.ResultSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.WriteCount++
	f.LastProfileID = profileID
	f.LastQuery = query
	f.LastParams = params
	return nil, f.WriteErr
}

type fakeDedupeLookup struct {
	ByKey           *domain.Fragment
	ByHash          *domain.Fragment
	ByKeyErr        error
	ByHashErr       error
	KeyCallCount    int
	HashCallCount   int
	LastKeyProfile  string
	LastHashProfile string
}

func (f *fakeDedupeLookup) ByIdempotencyKey(ctx context.Context, profileID, key string) (*domain.Fragment, error) {
	f.KeyCallCount++
	f.LastKeyProfile = profileID
	return f.ByKey, f.ByKeyErr
}

func (f *fakeDedupeLookup) ByContentHash(ctx context.Context, profileID, hash string) (*domain.Fragment, error) {
	f.HashCallCount++
	f.LastHashProfile = profileID
	return f.ByHash, f.ByHashErr
}

type fakeAudit struct {
	EventCount      int
	LastEntry       AuditLogEntry
	LastPayloadJSON string
	AppendErr       error
}

func (f *fakeAudit) Append(ctx context.Context, entry AuditLogEntry) error {
	f.EventCount++
	f.LastEntry = entry
	if b, err := json.Marshal(entry); err == nil {
		f.LastPayloadJSON = string(b)
	}
	return f.AppendErr
}

type fakeConsistency struct {
	ValidateErr         error
	RecordErr           error
	RecordCount         int
	ValidateCount       int
	LastRecordedModel   string
	LastRecordedDims    int
	LastValidatedVector []float32
}

func (f *fakeConsistency) ValidateVectorLength(vec []float32) error {
	f.ValidateCount++
	f.LastValidatedVector = vec
	return f.ValidateErr
}

func (f *fakeConsistency) RecordFirstWrite(ctx context.Context, model string, dims int) error {
	f.RecordCount++
	f.LastRecordedModel = model
	f.LastRecordedDims = dims
	return f.RecordErr
}

// --- Tests ---

func TestFragmentCreateService_HappyPath(t *testing.T) {
	mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
	writer := &fakeScopedWriter{}
	lookup := &fakeDedupeLookup{}
	audit := &fakeAudit{}
	consistency := &fakeConsistency{}
	svc := NewCreateFragmentService(mockEmb, writer, lookup, audit, consistency, nil, nil)

	out, err := svc.Create(context.Background(), "pA", &dto.CreateFragmentRequest{Content: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Duplicate {
		t.Error("expected Duplicate=false on happy path")
	}
	if out.Fragment.EmbeddingModel != "m1" {
		t.Errorf("EmbeddingModel = %q; want %q", out.Fragment.EmbeddingModel, "m1")
	}
	if out.Fragment.EmbeddingDimensions != 4 {
		t.Errorf("EmbeddingDimensions = %d; want 4", out.Fragment.EmbeddingDimensions)
	}
	if out.Fragment.SourceType != domain.SourceTypeManual {
		t.Errorf("SourceType = %q; want manual (default)", out.Fragment.SourceType)
	}
	if writer.WriteCount != 1 {
		t.Errorf("WriteCount = %d; want 1", writer.WriteCount)
	}
	if writer.LastProfileID != "pA" {
		t.Errorf("LastProfileID = %q; want pA", writer.LastProfileID)
	}
	if audit.EventCount != 1 {
		t.Errorf("EventCount = %d; want 1", audit.EventCount)
	}
	if strings.Contains(audit.LastPayloadJSON, "hello") {
		t.Errorf("audit payload must not contain content: %s", audit.LastPayloadJSON)
	}
	if consistency.RecordCount != 1 {
		t.Errorf("consistency.RecordCount = %d; want 1", consistency.RecordCount)
	}
}

func TestCreate_RejectsInvalidRequestBeforeEmbedding(t *testing.T) {
	cases := []struct {
		name string
		req  *dto.CreateFragmentRequest
		want string
	}{
		{
			name: "nil request",
			req:  nil,
			want: "fragment request is required",
		},
		{
			name: "oversized metadata",
			req: &dto.CreateFragmentRequest{
				Content:  "x",
				Metadata: map[string]any{"blob": strings.Repeat("x", dto.MaxMetadataBytes)},
			},
			want: "metadata size",
		},
		{
			name: "invalid source type",
			req: &dto.CreateFragmentRequest{
				Content:    "x",
				SourceType: "webhook",
			},
			want: "SourceType",
		},
		{
			name: "blank label",
			req: &dto.CreateFragmentRequest{
				Content: "x",
				Labels:  []string{""},
			},
			want: "Labels",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
			writer := &fakeScopedWriter{}
			svc := NewCreateFragmentService(mockEmb, writer, &fakeDedupeLookup{}, nil, &fakeConsistency{}, nil, nil)

			_, err := svc.Create(context.Background(), "pA", tc.req)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want it to contain %q", err, tc.want)
			}
			if mockEmb.CallCount != 0 {
				t.Fatalf("embedding calls = %d; want 0", mockEmb.CallCount)
			}
			if writer.WriteCount != 0 {
				t.Fatalf("writes = %d; want 0", writer.WriteCount)
			}
		})
	}
}

func TestCreate_IdempotencyReplay_NoEmbedding(t *testing.T) {
	existing := &domain.Fragment{FragmentID: "f-existing", ProfileID: "pA"}
	mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
	lookup := &fakeDedupeLookup{ByKey: existing}
	writer := &fakeScopedWriter{}
	svc := NewCreateFragmentService(mockEmb, writer, lookup, &fakeAudit{}, &fakeConsistency{}, nil, nil)

	out, err := svc.Create(context.Background(), "pA", &dto.CreateFragmentRequest{
		Content: "x", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Duplicate {
		t.Error("expected Duplicate=true on idempotency replay")
	}
	if out.DuplicateOf != "f-existing" {
		t.Errorf("DuplicateOf = %q; want f-existing", out.DuplicateOf)
	}
	if mockEmb.CallCount != 0 {
		t.Errorf("embedding must not be called on replay; CallCount = %d", mockEmb.CallCount)
	}
	if writer.WriteCount != 0 {
		t.Errorf("writer must not be called on replay; WriteCount = %d", writer.WriteCount)
	}
}

func TestCreate_ContentHashReplay_NoPersist(t *testing.T) {
	existing := &domain.Fragment{FragmentID: "f-hash-dup", ProfileID: "pA"}
	mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
	lookup := &fakeDedupeLookup{ByHash: existing}
	writer := &fakeScopedWriter{}
	svc := NewCreateFragmentService(mockEmb, writer, lookup, &fakeAudit{}, &fakeConsistency{}, nil, nil)

	out, err := svc.Create(context.Background(), "pA", &dto.CreateFragmentRequest{Content: "x"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !out.Duplicate {
		t.Error("expected Duplicate=true on content hash replay")
	}
	if mockEmb.CallCount != 0 {
		t.Errorf("embedding must not be called on content hash replay; CallCount = %d", mockEmb.CallCount)
	}
	if writer.WriteCount != 0 {
		t.Errorf("writer must not be called on content hash replay; WriteCount = %d", writer.WriteCount)
	}
}

func TestCreate_EmbeddingFailure_NoPersist(t *testing.T) {
	mockEmb := &stubEmbedding{
		EmbedFunc: func(context.Context, string) ([]float32, string, error) {
			return nil, "", embedding.ErrEmbeddingProvider
		},
	}
	writer := &fakeScopedWriter{}
	audit := &fakeAudit{}
	svc := NewCreateFragmentService(mockEmb, writer, &fakeDedupeLookup{}, audit, &fakeConsistency{}, nil, nil)
	_, err := svc.Create(context.Background(), "pA", &dto.CreateFragmentRequest{Content: "x"})
	if err == nil {
		t.Fatal("expected error when embedding fails")
	}
	if !errors.Is(err, ErrEmbeddingFailed) {
		t.Errorf("error = %v; want wrapping ErrEmbeddingFailed", err)
	}
	if writer.WriteCount != 0 {
		t.Errorf("no write expected on embedding failure; WriteCount = %d", writer.WriteCount)
	}
	if audit.EventCount != 0 {
		t.Errorf("no audit expected on embedding failure; EventCount = %d", audit.EventCount)
	}
}

func TestCreate_CrossProfileIdempotency_NewFragment(t *testing.T) {
	// Dedupe lookup scopes by profile, so same key in different profile = miss → write
	mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
	lookup := &fakeDedupeLookup{ByKey: nil} // miss
	writer := &fakeScopedWriter{}
	svc := NewCreateFragmentService(mockEmb, writer, lookup, &fakeAudit{}, &fakeConsistency{}, nil, nil)

	out, err := svc.Create(context.Background(), "pB", &dto.CreateFragmentRequest{
		Content: "x", IdempotencyKey: "k1",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Duplicate {
		t.Error("expected new fragment when key matches a different profile")
	}
	if lookup.LastKeyProfile != "pB" {
		t.Errorf("lookup profile = %q; want pB (scoped)", lookup.LastKeyProfile)
	}
	if writer.LastProfileID != "pB" {
		t.Errorf("writer profile = %q; want pB", writer.LastProfileID)
	}
}

func TestCreate_VectorLengthMismatch_NoPersist(t *testing.T) {
	mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
	writer := &fakeScopedWriter{}
	consistency := &fakeConsistency{ValidateErr: errors.New("dimension mismatch: got 4 want 8")}
	svc := NewCreateFragmentService(mockEmb, writer, &fakeDedupeLookup{}, &fakeAudit{}, consistency, nil, nil)

	_, err := svc.Create(context.Background(), "pA", &dto.CreateFragmentRequest{Content: "x"})
	if err == nil {
		t.Fatal("expected error on vector length mismatch")
	}
	if !errors.Is(err, ErrVectorLengthMismatch) {
		t.Errorf("error = %v; want wrapping ErrVectorLengthMismatch", err)
	}
	if writer.WriteCount != 0 {
		t.Errorf("no write expected on vector length mismatch; WriteCount = %d", writer.WriteCount)
	}
}

func TestCreate_ExplicitSourceType_Respected(t *testing.T) {
	mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
	writer := &fakeScopedWriter{}
	svc := NewCreateFragmentService(mockEmb, writer, &fakeDedupeLookup{}, &fakeAudit{}, &fakeConsistency{}, nil, nil)

	out, err := svc.Create(context.Background(), "pA", &dto.CreateFragmentRequest{
		Content:    "x",
		SourceType: string(domain.SourceTypeConversation),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Fragment.SourceType != domain.SourceTypeConversation {
		t.Errorf("SourceType = %q; want conversation", out.Fragment.SourceType)
	}
}

func TestCreate_WriteFailure_PropagatesError(t *testing.T) {
	mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
	writer := &fakeScopedWriter{WriteErr: errors.New("neo4j down")}
	audit := &fakeAudit{}
	svc := NewCreateFragmentService(mockEmb, writer, &fakeDedupeLookup{}, audit, &fakeConsistency{}, nil, nil)

	_, err := svc.Create(context.Background(), "pA", &dto.CreateFragmentRequest{Content: "x"})
	if err == nil {
		t.Fatal("expected error from writer")
	}
	if audit.EventCount != 0 {
		t.Errorf("audit must not fire on persist failure; EventCount = %d", audit.EventCount)
	}
}

func TestCreate_LookupFailuresStopBeforeEmbedding(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	cases := []struct {
		name   string
		req    *dto.CreateFragmentRequest
		lookup *fakeDedupeLookup
		want   string
	}{
		{
			name: "idempotency lookup error",
			req: &dto.CreateFragmentRequest{
				Content:        "x",
				IdempotencyKey: "idem-key",
			},
			lookup: &fakeDedupeLookup{ByKeyErr: errors.New("lookup unavailable")},
			want:   "failed to check idempotency key",
		},
		{
			name:   "content hash lookup error",
			req:    &dto.CreateFragmentRequest{Content: "x"},
			lookup: &fakeDedupeLookup{ByHashErr: errors.New("lookup unavailable")},
			want:   "failed to check content hash",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
			writer := &fakeScopedWriter{}
			svc := NewCreateFragmentService(mockEmb, writer, tc.lookup, &fakeAudit{}, &fakeConsistency{}, logger, nil)

			_, err := svc.Create(context.Background(), "pA", tc.req)

			if err == nil {
				t.Fatal("expected lookup error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v; want it to contain %q", err, tc.want)
			}
			if mockEmb.CallCount != 0 {
				t.Fatalf("embedding calls = %d; want 0", mockEmb.CallCount)
			}
			if writer.WriteCount != 0 {
				t.Fatalf("writes = %d; want 0", writer.WriteCount)
			}
		})
	}
}

func TestCreate_ClassificationEncodeFailureStopsBeforePersist(t *testing.T) {
	mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
	writer := &fakeScopedWriter{}
	svc := NewCreateFragmentService(mockEmb, writer, &fakeDedupeLookup{}, &fakeAudit{}, &fakeConsistency{}, nil, nil)

	_, err := svc.Create(context.Background(), "pA", &dto.CreateFragmentRequest{
		Content:        "x",
		Classification: map[string]any{"bad": func() {}},
	})

	if err == nil {
		t.Fatal("expected classification JSON encoding error")
	}
	if !strings.Contains(err.Error(), "fragment classification") {
		t.Fatalf("error = %v; want classification encode context", err)
	}
	if writer.WriteCount != 0 {
		t.Fatalf("writes = %d; want 0", writer.WriteCount)
	}
}

func TestCreate_PostPersistWarningsDoNotFailCreate(t *testing.T) {
	mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
	writer := &fakeScopedWriter{}
	audit := &fakeAudit{AppendErr: errors.New("audit unavailable")}
	consistency := &fakeConsistency{RecordErr: errors.New("config store unavailable")}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewCreateFragmentService(mockEmb, writer, &fakeDedupeLookup{}, audit, consistency, logger, nil)

	out, err := svc.Create(context.Background(), "pA", &dto.CreateFragmentRequest{Content: "x"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out == nil || out.Fragment == nil {
		t.Fatal("expected created fragment")
	}
	if writer.WriteCount != 1 {
		t.Fatalf("writes = %d; want 1", writer.WriteCount)
	}
	if consistency.RecordCount != 1 {
		t.Fatalf("consistency records = %d; want 1", consistency.RecordCount)
	}
	if audit.EventCount != 1 {
		t.Fatalf("audit events = %d; want 1", audit.EventCount)
	}
}

func TestCreate_AuditPayload_ExcludesContentAndEmbedding(t *testing.T) {
	mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
	writer := &fakeScopedWriter{}
	audit := &fakeAudit{}
	svc := NewCreateFragmentService(mockEmb, writer, &fakeDedupeLookup{}, audit, &fakeConsistency{}, nil, nil)

	content := "very-sensitive-unique-string-12345"
	_, err := svc.Create(context.Background(), "pA", &dto.CreateFragmentRequest{Content: content})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(audit.LastPayloadJSON, content) {
		t.Errorf("audit payload leaked content: %s", audit.LastPayloadJSON)
	}
	if _, ok := audit.LastEntry.AfterPayload["content"]; ok {
		t.Error("audit AfterPayload must not contain content key")
	}
	if _, ok := audit.LastEntry.AfterPayload["embedding"]; ok {
		t.Error("audit AfterPayload must not contain embedding key")
	}
	if _, ok := audit.LastEntry.AfterPayload["fragment_id"]; !ok {
		t.Error("audit AfterPayload must contain fragment_id")
	}
}

// TestCreate_AuditCarriesCorrelationID proves the create audit row is stamped with
// the upstream X-Correlation-ID that middleware threads into the context (AC-54).
func TestCreate_AuditCarriesCorrelationID(t *testing.T) {
	mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
	writer := &fakeScopedWriter{}
	audit := &fakeAudit{}
	svc := NewCreateFragmentService(mockEmb, writer, &fakeDedupeLookup{}, audit, &fakeConsistency{}, nil, nil)

	ctx := correlation.WithID(context.Background(), "corr-create-xyz")
	_, err := svc.Create(ctx, "pA", &dto.CreateFragmentRequest{Content: "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if audit.LastEntry.CorrelationID != "corr-create-xyz" {
		t.Errorf("CorrelationID = %q; want %q", audit.LastEntry.CorrelationID, "corr-create-xyz")
	}
}
