package fragmentservice

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/markhuangai/dense-mem/internal/http/dto"
)

// TestCreatePersistsSourceQualityAndClassification verifies that source_quality and
// classification are propagated from the request DTO through to the persisted fragment
// (both returned in the domain object and written into the Neo4j params).
func TestCreatePersistsSourceQualityAndClassification(t *testing.T) {
	mockEmb := &stubEmbedding{DimensionsResult: 4, ModelNameResult: "m1"}
	writer := &fakeScopedWriter{}
	lookup := &fakeDedupeLookup{}
	audit := &fakeAudit{}
	consistency := &fakeConsistency{}
	svc := NewCreateFragmentService(mockEmb, writer, lookup, audit, consistency, nil, nil)

	req := &dto.CreateFragmentRequest{
		Content:       "test content for quality check",
		SourceQuality: 0.85,
		Classification: map[string]any{
			"topic":     "science",
			"sentiment": "neutral",
		},
	}

	out, err := svc.Create(context.Background(), "pA", req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Duplicate {
		t.Error("expected Duplicate=false on happy path")
	}

	// Assert domain.Fragment carries the values.
	if out.Fragment.SourceQuality != 0.85 {
		t.Errorf("Fragment.SourceQuality = %v; want 0.85", out.Fragment.SourceQuality)
	}
	if out.Fragment.Classification == nil {
		t.Fatal("Fragment.Classification is nil; want non-nil map")
	}
	if out.Fragment.Classification["topic"] != "science" {
		t.Errorf("Fragment.Classification[topic] = %v; want science", out.Fragment.Classification["topic"])
	}
	if out.Fragment.Classification["sentiment"] != "neutral" {
		t.Errorf("Fragment.Classification[sentiment] = %v; want neutral", out.Fragment.Classification["sentiment"])
	}

	// Assert the values were passed to the writer as Neo4j params.
	if sq, ok := writer.LastParams["sourceQuality"]; !ok {
		t.Error("writer params missing sourceQuality")
	} else if sq != 0.85 {
		t.Errorf("writer params sourceQuality = %v; want 0.85", sq)
	}

	cls, ok := writer.LastParams["classificationJSON"]
	if !ok {
		t.Fatal("writer params missing classificationJSON")
	}
	clsJSON, ok := cls.(string)
	if !ok {
		t.Fatalf("writer params classificationJSON is %T; want string", cls)
	}
	var clsMap map[string]any
	if err := json.Unmarshal([]byte(clsJSON), &clsMap); err != nil {
		t.Fatalf("failed to decode classificationJSON: %v", err)
	}
	if clsMap["topic"] != "science" {
		t.Errorf("writer params classificationJSON[topic] = %v; want science", clsMap["topic"])
	}
}
