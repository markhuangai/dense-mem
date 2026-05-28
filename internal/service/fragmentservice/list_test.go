package fragmentservice

import (
	"context"
	"errors"
	"testing"
	"time"
)

type listingScopedReader struct {
	rows            []map[string]any
	err             error
	capturedParams  map[string]any
	capturedProfile string
}

func (l *listingScopedReader) ScopedRead(ctx context.Context, profileID, query string, params map[string]any) (any, []map[string]any, error) {
	l.capturedProfile = profileID
	l.capturedParams = params
	return nil, l.rows, l.err
}

func rowAt(id string, ts time.Time, content string) map[string]any {
	return map[string]any{
		"fragment_id": id,
		"team_id":     "pA",
		"content":     content,
		"source_type": "manual",
		"created_at":  ts,
		"updated_at":  ts,
	}
}

func TestList_DefaultLimit_AppliesTwenty(t *testing.T) {
	reader := &listingScopedReader{}
	svc := NewListFragmentsService(reader)

	_, _, err := svc.List(context.Background(), "pA", ListOptions{Limit: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := reader.capturedParams["limit"]; got != int64(DefaultListLimit+1) {
		t.Errorf("limit param = %v; want %d (default %d + 1 overfetch)", got, DefaultListLimit+1, DefaultListLimit)
	}
}

func TestList_LimitClamp_MaxHundred(t *testing.T) {
	reader := &listingScopedReader{}
	svc := NewListFragmentsService(reader)

	_, _, err := svc.List(context.Background(), "pA", ListOptions{Limit: 500})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := reader.capturedParams["limit"]; got != int64(MaxListLimit+1) {
		t.Errorf("limit param = %v; want %d (max clamp + 1)", got, MaxListLimit+1)
	}
}

func TestList_DescendingOrder_NoNextCursor_WhenPageFits(t *testing.T) {
	t1 := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 16, 11, 0, 0, 0, time.UTC)
	reader := &listingScopedReader{
		rows: []map[string]any{
			rowAt("f2", t2, "second"),
			rowAt("f1", t1, "first"),
		},
	}
	svc := NewListFragmentsService(reader)

	page, nextCursor, err := svc.List(context.Background(), "pA", ListOptions{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("len(page) = %d; want 2", len(page))
	}
	if !page[0].CreatedAt.After(page[1].CreatedAt) {
		t.Error("expected descending created_at ordering")
	}
	if nextCursor != "" {
		t.Errorf("nextCursor = %q; want empty when page fits", nextCursor)
	}
}

func TestList_EmitsNextCursor_WhenMoreAvailable(t *testing.T) {
	// Request limit=1; service overfetches to 2. Second row signals "more".
	t1 := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 16, 11, 0, 0, 0, time.UTC)
	reader := &listingScopedReader{
		rows: []map[string]any{
			rowAt("f2", t2, "newer"),
			rowAt("f1", t1, "older"),
		},
	}
	svc := NewListFragmentsService(reader)

	page, nextCursor, err := svc.List(context.Background(), "pA", ListOptions{Limit: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page) != 1 {
		t.Errorf("len(page) = %d; want 1", len(page))
	}
	if nextCursor == "" {
		t.Fatal("expected nextCursor when more rows available")
	}
	// Verify roundtrip decodes back to the same boundary
	decTs, decID, err := decodeCursor(nextCursor)
	if err != nil {
		t.Fatalf("failed to decode returned cursor: %v", err)
	}
	if decID != "f2" {
		t.Errorf("decoded cursor id = %q; want f2 (last returned item)", decID)
	}
	if !decTs.Equal(t2) {
		t.Errorf("decoded cursor ts = %v; want %v", decTs, t2)
	}
}

func TestList_DecodesCursorIntoQueryParams(t *testing.T) {
	reader := &listingScopedReader{}
	svc := NewListFragmentsService(reader)

	ts := time.Date(2026, 4, 16, 10, 0, 0, 0, time.UTC)
	cursor := encodeCursor(ts, "last-id")

	_, _, err := svc.List(context.Background(), "pA", ListOptions{Limit: 5, Cursor: cursor})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := reader.capturedParams["afterId"]; got != "last-id" {
		t.Errorf("afterId = %v; want last-id", got)
	}
	if got, ok := reader.capturedParams["afterTs"].(time.Time); !ok || !got.Equal(ts) {
		t.Errorf("afterTs = %v; want %v", reader.capturedParams["afterTs"], ts)
	}
}

func TestList_InvalidCursor_ReturnsError(t *testing.T) {
	reader := &listingScopedReader{}
	svc := NewListFragmentsService(reader)

	_, _, err := svc.List(context.Background(), "pA", ListOptions{Cursor: "not-base64!@#$"})
	if !errors.Is(err, ErrInvalidCursor) {
		t.Errorf("err = %v; want ErrInvalidCursor", err)
	}
}

func TestList_SourceTypeFilter_PassedThrough(t *testing.T) {
	reader := &listingScopedReader{}
	svc := NewListFragmentsService(reader)

	_, _, err := svc.List(context.Background(), "pA", ListOptions{SourceType: "conversation"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := reader.capturedParams["srcType"]; got != "conversation" {
		t.Errorf("srcType = %v; want conversation", got)
	}

	reader2 := &listingScopedReader{}
	svc2 := NewListFragmentsService(reader2)
	_, _, err = svc2.List(context.Background(), "pA", ListOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reader2.capturedParams["srcType"] != nil {
		t.Errorf("srcType = %v; want nil when filter unset", reader2.capturedParams["srcType"])
	}
}

func TestList_ProfileIDPropagatedToReader(t *testing.T) {
	reader := &listingScopedReader{}
	svc := NewListFragmentsService(reader)

	_, _, _ = svc.List(context.Background(), "pA", ListOptions{})
	if reader.capturedProfile != "pA" {
		t.Errorf("profile = %q; want pA", reader.capturedProfile)
	}
}
