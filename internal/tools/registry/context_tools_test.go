package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestBuildDefaultContextTools_InvokeAndScope(t *testing.T) {
	ctxSvc := &stubContextService{}
	reg, _ := BuildDefault(Dependencies{Context: ctxSvc})

	traceTool, ok := reg.Get("trace_memory")
	if !ok {
		t.Fatal("trace_memory not registered")
	}
	if len(traceTool.RequiredScopes) != 1 || traceTool.RequiredScopes[0] != "read" {
		t.Fatalf("trace_memory scopes = %v; want [read]", traceTool.RequiredScopes)
	}
	traceOut, err := traceTool.Invoke(context.Background(), "profile-context", map[string]any{
		"type": "fact",
		"id":   "fact-1",
	})
	if err != nil {
		t.Fatalf("trace_memory Invoke: %v", err)
	}
	if ctxSvc.lastProfile != "profile-context" || ctxSvc.lastTrace.Type != "fact" || ctxSvc.lastTrace.ID != "fact-1" {
		t.Fatalf("trace_memory routed profile/request = %q/%+v", ctxSvc.lastProfile, ctxSvc.lastTrace)
	}
	anchor := traceOut["anchor"].(map[string]any)
	if anchor["type"] != "fact" {
		t.Fatalf("trace_memory anchor = %v; want fact", anchor)
	}

	assembleTool, ok := reg.Get("assemble_context")
	if !ok {
		t.Fatal("assemble_context not registered")
	}
	if len(assembleTool.RequiredScopes) != 1 || assembleTool.RequiredScopes[0] != "read" {
		t.Fatalf("assemble_context scopes = %v; want [read]", assembleTool.RequiredScopes)
	}
	contextOut, err := assembleTool.Invoke(context.Background(), "profile-context", map[string]any{
		"query":    "deployment memory",
		"valid_at": "2026-06-22T00:00:00Z",
		"known_at": "2026-06-23T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("assemble_context Invoke: %v", err)
	}
	if ctxSvc.lastProfile != "profile-context" || ctxSvc.lastAssemble.Query != "deployment memory" {
		t.Fatalf("assemble_context routed profile/request = %q/%+v", ctxSvc.lastProfile, ctxSvc.lastAssemble)
	}
	if ctxSvc.lastAssemble.ValidAt == nil || ctxSvc.lastAssemble.ValidAt.Format(time.RFC3339) != "2026-06-22T00:00:00Z" {
		t.Fatalf("assemble_context valid_at = %+v", ctxSvc.lastAssemble.ValidAt)
	}
	if ctxSvc.lastAssemble.KnownAt == nil || ctxSvc.lastAssemble.KnownAt.Format(time.RFC3339) != "2026-06-23T00:00:00Z" {
		t.Fatalf("assemble_context known_at = %+v", ctxSvc.lastAssemble.KnownAt)
	}
	if contextOut["context_block"] != "Dense-Mem context." {
		t.Fatalf("assemble_context context_block = %v", contextOut["context_block"])
	}
}

func TestBuildDefaultContextTools_UnavailableAndInvalidInput(t *testing.T) {
	reg, _ := BuildDefault(Dependencies{})

	for _, tc := range []struct {
		name  string
		input map[string]any
	}{
		{name: "trace_memory", input: map[string]any{"type": "fact", "id": "fact-1"}},
		{name: "assemble_context", input: map[string]any{"query": "hello"}},
	} {
		tool, _ := reg.Get(tc.name)
		if _, err := tool.Invoke(context.Background(), "profile-context", tc.input); !errors.Is(err, ErrToolUnavailable) {
			t.Fatalf("%s err = %v; want ErrToolUnavailable", tc.name, err)
		}
	}

	ctxSvc := &stubContextService{}
	reg, _ = BuildDefault(Dependencies{Context: ctxSvc})

	traceTool, _ := reg.Get("trace_memory")
	if err := ValidateInput(traceTool, map[string]any{"type": "free", "id": "fact-1"}); err == nil || !strings.Contains(err.Error(), "type must be one of") {
		t.Fatalf("trace_memory invalid type err = %v; want enum validation", err)
	}
	if _, err := traceTool.Invoke(context.Background(), "profile-context", map[string]any{"id": func() {}}); err == nil || !strings.Contains(err.Error(), "trace_memory: invalid input") {
		t.Fatalf("trace_memory invalid input err = %v", err)
	}

	assembleTool, _ := reg.Get("assemble_context")
	if _, err := assembleTool.Invoke(context.Background(), "profile-context", map[string]any{"query": func() {}}); err == nil || !strings.Contains(err.Error(), "assemble_context: invalid input") {
		t.Fatalf("assemble_context invalid input err = %v", err)
	}
}

type stubContextService struct {
	lastProfile  string
	lastTrace    contextservice.TraceRequest
	lastAssemble contextservice.AssembleRequest
}

func (s *stubContextService) Trace(ctx context.Context, profileID string, req contextservice.TraceRequest) (*contextservice.TraceResult, error) {
	s.lastProfile = profileID
	s.lastTrace = req
	return &contextservice.TraceResult{
		Anchor: contextservice.TraceAnchor{
			Type: req.Type,
			Fact: &domain.Fact{FactID: req.ID, ProfileID: profileID},
		},
	}, nil
}

func (s *stubContextService) Assemble(ctx context.Context, profileID string, req contextservice.AssembleRequest) (*contextservice.AssembleResult, error) {
	s.lastProfile = profileID
	s.lastAssemble = req
	return &contextservice.AssembleResult{
		Query:        req.Query,
		ContextBlock: "Dense-Mem context.",
		Items: []contextservice.ContextItem{{
			Type: "fragment",
			ID:   "fragment-1",
			Fragment: &domain.Fragment{
				FragmentID: "fragment-1",
				ProfileID:  profileID,
				Content:    "context",
			},
		}},
		Clarifications: []memoryservice.Clarification{},
	}, nil
}
