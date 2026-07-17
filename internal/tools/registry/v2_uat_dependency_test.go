package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestBuildV2UATExecutableToolsRequireDependencies(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	tests := []struct {
		name string
		args map[string]any
	}{
		{
			name: V2ToolRemember,
			args: map[string]any{
				"evidence": []any{map[string]any{"content": "remember"}},
			},
		},
		{
			name: V2ToolRecallMemory,
			args: map[string]any{
				"query": "PostgreSQL",
			},
		},
		{
			name: V2ToolResolveMemoryPlacement,
			args: map[string]any{
				"action":          string(domain.V2ResolveForget),
				"relationship_id": "relationship-v2",
				"reason":          "forget this relationship",
				"idempotency_key": "forget-1",
				"evidence":        []any{map[string]any{"content": "forget"}},
			},
		},
		{
			name: V2ToolCorrectEntityResolution,
			args: map[string]any{
				"operation":             string(domain.V2EntityCorrectionSplit),
				"source_entity_id":      "entity-source",
				"target_entity_id":      nil,
				"owned_observation_ids": []any{"obs-1"},
				"dry_run":               true,
			},
		},
		{
			name: V2ToolTraceMemory,
			args: map[string]any{
				"relationship_id": "relationship-v2",
			},
		},
		{
			name: V2ToolListDreams,
			args: map[string]any{
				"status": "proposed",
			},
		},
		{
			name: V2ToolGetDream,
			args: map[string]any{
				"hypothesis_id": "dream-v2",
			},
		},
		{
			name: V2ToolResolveDreamFeedback,
			args: map[string]any{
				"hypothesis_id": "dream-v2",
				"decision":      "confirm_true",
				"evidence":      []any{map[string]any{"content": "Independent evidence."}},
			},
		},
		{
			name: V2ToolListCommunities,
			args: map[string]any{
				"limit": float64(2),
			},
		},
		{
			name: V2ToolFindMemoryPackCandidates,
			args: map[string]any{
				"query": "PostgreSQL",
			},
		},
		{
			name: V2ToolExportMemoryPack,
			args: map[string]any{
				"name":             "PostgreSQL pack",
				"relationship_ids": []any{"relationship-v2"},
			},
		},
		{
			name: V2ToolInspectMemoryPack,
			args: map[string]any{
				"artifact_json": "{}",
				"mode":          "review",
			},
		},
		{
			name: V2ToolImportMemoryPack,
			args: map[string]any{
				"artifact_json": "{}",
				"mode":          "review",
			},
		},
		{
			name: V2ToolRollbackMemoryPackImport,
			args: map[string]any{
				"import_id": "import-v2",
				"dry_run":   true,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tool, ok := reg.Get(tc.name)
			if !ok || tool.Invoke == nil {
				t.Fatalf("tool %s not executable", tc.name)
			}
			_, err := tool.Invoke(context.Background(), "ignored-profile", tc.args)
			if !errors.Is(err, ErrToolUnavailable) {
				t.Fatalf("%s err = %v, want ErrToolUnavailable", tc.name, err)
			}
		})
	}
}

func TestBuildV2UATWiresExecutableCommunityTools(t *testing.T) {
	teamID := uuid.New()
	communities := &stubV2CommunityRepository{
		records: []repository.V2CommunityRecord{{
			TeamID:         teamID.String(),
			CommunityID:    uuid.NewString(),
			Status:         "current",
			Summary:        "PostgreSQL community",
			SummaryVersion: "community-deterministic-v1",
			MemberCount:    2,
			UpdatedAt:      time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC),
		}},
	}
	reg, err := BuildV2UAT(Dependencies{V2Communities: communities})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	tool, ok := reg.Get(V2ToolListCommunities)
	if !ok || tool.Invoke == nil {
		t.Fatal("BuildV2UAT did not register executable list_communities")
	}
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		TeamID:    teamID,
		ProfileID: uuid.New(),
	})
	out, err := tool.Invoke(ctx, "ignored-profile", map[string]any{
		"limit": float64(2),
	})
	if err != nil {
		t.Fatalf("list_communities.Invoke: %v", err)
	}
	if out["communities"] == nil || communities.lastInput.TeamID != teamID.String() || communities.lastInput.Limit != 2 {
		t.Fatalf("list_communities output = %#v input = %#v", out, communities.lastInput)
	}
}

func TestBuildV2UATWiresExecutableDreamTools(t *testing.T) {
	dreams := &stubDreamService{}
	reg, err := BuildV2UAT(Dependencies{Dreams: dreams})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}

	list, ok := reg.Get(V2ToolListDreams)
	if !ok || list.Invoke == nil {
		t.Fatal("BuildV2UAT did not register executable list_dreams")
	}
	listOut, err := list.Invoke(context.Background(), "ignored-profile", map[string]any{
		"limit":  float64(2),
		"status": "submitted",
	})
	if err != nil {
		t.Fatalf("list_dreams.Invoke: %v", err)
	}
	if listOut["dreams"] == nil || dreams.lastListOpts.Limit != 2 || dreams.lastListOpts.Status != "submitted" {
		t.Fatalf("list_dreams output = %#v opts = %#v", listOut, dreams.lastListOpts)
	}

	get, ok := reg.Get(V2ToolGetDream)
	if !ok || get.Invoke == nil {
		t.Fatal("BuildV2UAT did not register executable get_dream")
	}
	getOut, err := get.Invoke(context.Background(), "ignored-profile", map[string]any{
		"hypothesis_id": "dream-v2",
	})
	if err != nil {
		t.Fatalf("get_dream.Invoke: %v", err)
	}
	hypothesis, ok := getOut["hypothesis"].(map[string]any)
	if !ok || hypothesis["hypothesis_id"] != "dream-v2" {
		t.Fatalf("get_dream hypothesis = %#v", getOut["hypothesis"])
	}

	resolve, ok := reg.Get(V2ToolResolveDreamFeedback)
	if !ok || resolve.Invoke == nil {
		t.Fatal("BuildV2UAT did not register executable resolve_dream_feedback")
	}
	_, err = resolve.Invoke(context.Background(), "ignored-profile", map[string]any{
		"hypothesis_id": "dream-v2",
		"decision":      "confirm_true",
	})
	if err == nil || !strings.Contains(err.Error(), "evidence is required") {
		t.Fatalf("resolve_dream_feedback missing evidence err = %v", err)
	}
	resolveOut, err := resolve.Invoke(context.Background(), "ignored-profile", map[string]any{
		"hypothesis_id": "dream-v2",
		"decision":      "confirm_true",
		"evidence": []any{
			map[string]any{"content": "A deployment note independently confirms Dense-Mem uses PostgreSQL."},
		},
		"idempotency_key": "dream-feedback-v2",
	})
	if err != nil {
		t.Fatalf("resolve_dream_feedback.Invoke: %v", err)
	}
	if resolveOut["hypothesis_id"] != "dream-1" || dreams.lastResolveReq.Decision != "confirm_true" {
		t.Fatalf("resolve_dream_feedback output = %#v request = %#v", resolveOut, dreams.lastResolveReq)
	}
	if dreams.lastResolveReq.DreamID != "dream-v2" {
		t.Fatalf("resolve_dream_feedback dream id = %q", dreams.lastResolveReq.DreamID)
	}
	if len(dreams.lastResolveReq.Evidence) != 1 ||
		dreams.lastResolveReq.Evidence[0].Content != "A deployment note independently confirms Dense-Mem uses PostgreSQL." {
		t.Fatalf("resolve_dream_feedback evidence = %#v", dreams.lastResolveReq.Evidence)
	}
	if dreams.lastResolveReq.IdempotencyKey != "dream-feedback-v2" {
		t.Fatalf("resolve_dream_feedback idempotency_key = %q", dreams.lastResolveReq.IdempotencyKey)
	}
}

type stubV2CommunityRepository struct {
	records   []repository.V2CommunityRecord
	lastInput repository.V2CommunityListInput
}

func (s *stubV2CommunityRepository) ClaimV2CommunityRun(context.Context, repository.V2CommunityRunClaimInput) (*repository.V2CommunityRun, error) {
	return nil, errors.New("unused")
}

func (s *stubV2CommunityRepository) CompleteV2CommunityRun(context.Context, repository.V2CommunityRunCompleteInput) error {
	return errors.New("unused")
}

func (s *stubV2CommunityRepository) ListV2CommunityInputs(context.Context, repository.V2CommunityInputListInput) ([]repository.V2CommunityInput, error) {
	return nil, errors.New("unused")
}

func (s *stubV2CommunityRepository) PublishV2CommunitySnapshot(context.Context, repository.V2CommunitySnapshotPublishInput) error {
	return errors.New("unused")
}

func (s *stubV2CommunityRepository) RefreshV2CommunityStaleness(context.Context, repository.V2CommunityStalenessInput) (int, error) {
	return 0, errors.New("unused")
}

func (s *stubV2CommunityRepository) ListV2Communities(_ context.Context, input repository.V2CommunityListInput) ([]repository.V2CommunityRecord, error) {
	s.lastInput = input
	return s.records, nil
}

func (s *stubV2CommunityRepository) GetV2Community(context.Context, repository.V2CommunityGetInput) (*repository.V2CommunityRecord, error) {
	return nil, errors.New("unused")
}

func (s *stubV2CommunityRepository) LatestV2CommunityRun(context.Context, string) (*repository.V2CommunityRun, error) {
	return nil, errors.New("unused")
}
