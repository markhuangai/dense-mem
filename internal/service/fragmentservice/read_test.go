package fragmentservice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type fakeScopedReader struct {
	lastProfileID string
	lastParams    map[string]any
	results       []map[string]any
	err           error
}

func (f *fakeScopedReader) ScopedRead(ctx context.Context, profileID string, query string, params map[string]any) (any, []map[string]any, error) {
	f.lastProfileID = profileID
	f.lastParams = params
	return nil, f.results, f.err
}

func TestGetByID_ReturnsFragmentOnHit(t *testing.T) {
	reader := &fakeScopedReader{
		results: []map[string]any{
			{
				"fragment_id":          "frag-1",
				"team_id":              "pA",
				"content":              "hello",
				"source":               "",
				"source_type":          "manual",
				"content_hash":         "abc",
				"embedding_model":      "m1",
				"embedding_dimensions": int64(4),
			},
		},
	}
	svc := NewGetFragmentService(reader)

	got, err := svc.GetByID(context.Background(), "pA", "frag-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.FragmentID != "frag-1" {
		t.Errorf("FragmentID = %q; want frag-1", got.FragmentID)
	}
	if got.SourceType != domain.SourceTypeManual {
		t.Errorf("SourceType = %q; want manual", got.SourceType)
	}
	if got.EmbeddingDimensions != 4 {
		t.Errorf("EmbeddingDimensions = %d; want 4", got.EmbeddingDimensions)
	}
	if reader.lastProfileID != "pA" {
		t.Errorf("lastProfileID = %q; want pA", reader.lastProfileID)
	}
}

func TestGetByID_ReturnsNotFoundOnEmpty(t *testing.T) {
	reader := &fakeScopedReader{results: []map[string]any{}}
	svc := NewGetFragmentService(reader)

	_, err := svc.GetByID(context.Background(), "pA", "missing")
	if !errors.Is(err, ErrFragmentNotFound) {
		t.Errorf("err = %v; want ErrFragmentNotFound", err)
	}
}

func TestGetByID_CoercesLegacyNullSourceType(t *testing.T) {
	reader := &fakeScopedReader{
		results: []map[string]any{
			{
				"fragment_id": "frag-legacy",
				"team_id":     "pA",
				"content":     "legacy",
				"source_type": nil, // legacy null
			},
		},
	}
	svc := NewGetFragmentService(reader)

	got, err := svc.GetByID(context.Background(), "pA", "frag-legacy")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.SourceType != domain.SourceTypeManual {
		t.Errorf("SourceType = %q; want manual (coerced from null)", got.SourceType)
	}
}

func TestGetByID_DecodesJSONEncodedMaps(t *testing.T) {
	reader := &fakeScopedReader{
		results: []map[string]any{
			{
				"fragment_id":         "frag-json",
				"team_id":             "pA",
				"content":             "json encoded",
				"source_type":         "manual",
				"metadata_json":       `{"origin":"uat"}`,
				"classification_json": `{"topic":"science"}`,
			},
		},
	}
	svc := NewGetFragmentService(reader)

	got, err := svc.GetByID(context.Background(), "pA", "frag-json")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Metadata["origin"] != "uat" {
		t.Fatalf("Metadata[origin] = %v; want uat", got.Metadata["origin"])
	}
	if got.Classification["topic"] != "science" {
		t.Fatalf("Classification[topic] = %v; want science", got.Classification["topic"])
	}
}

func TestGetByID_PropagatesReaderError(t *testing.T) {
	reader := &fakeScopedReader{err: errors.New("neo4j down")}
	svc := NewGetFragmentService(reader)

	_, err := svc.GetByID(context.Background(), "pA", "anything")
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, ErrFragmentNotFound) {
		t.Error("reader error must not be mapped to ErrFragmentNotFound")
	}
}

func TestGetByIDsAndMapRowToFragmentBranches(t *testing.T) {
	createdAt := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)
	recordedTo := createdAt.Add(2 * time.Hour)
	retractedAt := createdAt.Add(3 * time.Hour)
	reader := &fakeScopedReader{
		results: []map[string]any{
			{
				"fragment_id":             "frag-1",
				"team_id":                 "pA",
				"created_by_profile_id":   "creator-1",
				"created_by_profile_name": "Creator",
				"content":                 "hello",
				"source":                  "chat",
				"source_type":             "conversation",
				"authority":               "authoritative",
				"labels":                  []any{"one", 2, "two"},
				"metadata":                map[string]any{"origin": "test"},
				"content_hash":            "hash-1",
				"idempotency_key":         "idem-1",
				"embedding_model":         "embed",
				"embedding_dimensions":    int(3),
				"source_quality":          float64(0.7),
				"classification":          map[string]any{"topic": "unit"},
				"recorded_to":             recordedTo,
				"retracted_at":            retractedAt,
				"created_at":              createdAt,
				"updated_at":              updatedAt,
			},
			{
				"fragment_id":          "",
				"team_id":              "pA",
				"embedding_dimensions": int64(4),
			},
		},
	}
	svc := NewGetFragmentService(reader).(BatchGetFragmentService)

	empty, err := svc.GetByIDs(context.Background(), "pA", nil)
	if err != nil {
		t.Fatalf("GetByIDs empty: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("GetByIDs empty len = %d, want 0", len(empty))
	}

	got, err := svc.GetByIDs(context.Background(), "pA", []string{"frag-1", "missing"})
	if err != nil {
		t.Fatalf("GetByIDs: %v", err)
	}
	frag := got["frag-1"]
	if frag == nil {
		t.Fatal("frag-1 missing from batch output")
	}
	if frag.CreatedByProfileID != "creator-1" || frag.CreatedByProfileName != "Creator" {
		t.Fatalf("creator fields = %q/%q", frag.CreatedByProfileID, frag.CreatedByProfileName)
	}
	if frag.Authority != domain.AuthorityAuthoritative {
		t.Fatalf("Authority = %q, want authoritative", frag.Authority)
	}
	if len(frag.Labels) != 2 || frag.Labels[0] != "one" || frag.Labels[1] != "two" {
		t.Fatalf("Labels = %#v, want [one two]", frag.Labels)
	}
	if frag.Metadata["origin"] != "test" || frag.Classification["topic"] != "unit" {
		t.Fatalf("decoded maps = metadata %#v classification %#v", frag.Metadata, frag.Classification)
	}
	if frag.EmbeddingDimensions != 3 || frag.SourceQuality < 0.699999 || frag.SourceQuality > 0.700001 {
		t.Fatalf("embedding/source quality = %d/%f", frag.EmbeddingDimensions, frag.SourceQuality)
	}
	if frag.RecordedTo == nil || !frag.RecordedTo.Equal(recordedTo) {
		t.Fatalf("RecordedTo = %v, want %s", frag.RecordedTo, recordedTo)
	}
	if frag.RetractedAt == nil || !frag.RetractedAt.Equal(retractedAt) {
		t.Fatalf("RetractedAt = %v, want %s", frag.RetractedAt, retractedAt)
	}
	if !frag.CreatedAt.Equal(createdAt) || !frag.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("times = %s/%s", frag.CreatedAt, frag.UpdatedAt)
	}
	if _, ok := got[""]; ok {
		t.Fatal("empty fragment_id row should be skipped")
	}

	reader.err = errors.New("neo4j down")
	got, err = svc.GetByIDs(context.Background(), "pA", []string{"frag-1"})
	if err == nil || !errors.Is(err, reader.err) {
		t.Fatalf("GetByIDs error = %v, want wrapped reader error", err)
	}
	if got != nil {
		t.Fatalf("GetByIDs error output = %#v, want nil", got)
	}
}
