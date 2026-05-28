package fragmentservice

import (
	"context"
	"errors"
	"testing"

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
