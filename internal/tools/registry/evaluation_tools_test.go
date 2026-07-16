package registry

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	appservice "github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/tools/graphquery"
)

type evaluationAuditStub struct {
	entries []appservice.AuditLogEntry
}

func (s *evaluationAuditStub) Append(_ context.Context, entry appservice.AuditLogEntry) error {
	s.entries = append(s.entries, entry)
	return nil
}

func TestEvalScoreRetrievalCaseWrapperNotRegistered(t *testing.T) {
	reg, err := BuildDefault(Dependencies{
		EvaluationAudit:  &evaluationAuditStub{},
		EvaluationConfig: stubEvaluationConfig{enabled: true},
	})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	if _, ok := reg.Get("eval_score_retrieval_case"); ok {
		t.Fatal("eval_score_retrieval_case must not be registered; scoring runs inside the eval harness")
	}
	if IsEvaluationTool("eval_score_retrieval_case") {
		t.Fatal("eval_score_retrieval_case must not be part of the runtime evaluation tool allowlist")
	}
}

func TestEvalToolsRequireAuditSink(t *testing.T) {
	reg, err := BuildDefault(Dependencies{EvaluationConfig: stubEvaluationConfig{enabled: true}})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	tool, ok := reg.Get("eval_get_manifest")
	if !ok {
		t.Fatal("eval_get_manifest not registered")
	}
	_, err = tool.Invoke(context.Background(), "profile-eval", map[string]any{})
	if !errors.Is(err, ErrToolUnavailable) {
		t.Fatalf("err = %v; want ErrToolUnavailable", err)
	}
}

func TestEvalManifestAndKnowledgeTools(t *testing.T) {
	audit := &evaluationAuditStub{}
	fragments := &evalFragmentStore{
		items: []domain.Fragment{{
			FragmentID:  "fragment-1",
			ProfileID:   "profile-eval",
			Content:     "content must be stripped when metadata_only is set",
			SourceType:  domain.SourceTypeDocument,
			ContentHash: "hash-1",
			CreatedAt:   time.Date(2026, 6, 26, 1, 0, 0, 0, time.UTC),
			UpdatedAt:   time.Date(2026, 6, 26, 1, 1, 0, 0, time.UTC),
		}},
	}
	claims := &evalClaimListFiltered{
		items: []*domain.Claim{{
			ClaimID:    "claim-1",
			ProfileID:  "profile-eval",
			Subject:    "dense-mem",
			Predicate:  "has_eval_mode",
			Object:     "true",
			Status:     domain.StatusValidated,
			Modality:   domain.ModalityAssertion,
			Polarity:   domain.PolarityPlus,
			RecordedAt: time.Date(2026, 6, 26, 1, 2, 0, 0, time.UTC),
		}},
		nextCursor: "claim-next",
	}
	facts := &evalFactStore{
		items: []*domain.Fact{{
			FactID:     "fact-1",
			ProfileID:  "profile-eval",
			Subject:    "dense-mem",
			Predicate:  "tracks",
			Object:     "evaluation metrics",
			Status:     domain.FactStatusActive,
			RecordedAt: time.Date(2026, 6, 26, 1, 3, 0, 0, time.UTC),
		}},
	}
	communities := &evalCommunityStore{
		items: []*domain.Community{{
			CommunityID:      "community-1",
			ProfileID:        "profile-eval",
			Summary:          "content must be stripped for metadata-only community reads",
			SummaryVersion:   "v1",
			MemberCount:      3,
			LastSummarizedAt: time.Date(2026, 6, 26, 1, 4, 0, 0, time.UTC),
		}},
	}
	graph := &evalGraphQuery{rows: []map[string]any{{
		"edge_type": "SUPPORTED_BY",
		"from_id":   "claim-1",
		"to_id":     "fragment-1",
	}}}
	dreams := &stubDreamService{}
	reg, err := BuildDefault(Dependencies{
		EvaluationAudit:   audit,
		FragmentList:      fragments,
		FragmentGet:       fragments,
		ClaimListFiltered: claims,
		ClaimGet:          claims,
		FactList:          facts,
		FactGet:           facts,
		CommunityList:     communities,
		CommunityGet:      communities,
		GraphQuery:        graph,
		Dreams:            dreams,
	})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{
		TeamID:   uuid.MustParse("00000000-0000-0000-0000-000000000101"),
		TeamName: "eval team",
	})

	manifestTool, _ := reg.Get("eval_get_manifest")
	manifest, err := manifestTool.Invoke(ctx, "profile-eval", map[string]any{})
	if err != nil {
		t.Fatalf("eval_get_manifest Invoke: %v", err)
	}
	team := manifest["team"].(map[string]any)
	if team["id"] != "00000000-0000-0000-0000-000000000101" || team["name"] != "eval team" {
		t.Fatalf("manifest team = %v", team)
	}

	listTool, _ := reg.Get("eval_list_knowledge_refs")
	fragmentPage, err := listTool.Invoke(ctx, "profile-eval", map[string]any{
		"type":          "fragment",
		"limit":         2,
		"metadata_only": true,
	})
	if err != nil {
		t.Fatalf("eval_list_knowledge_refs fragment Invoke: %v", err)
	}
	fragment := firstEvalItem(t, fragmentPage)
	if _, ok := fragment["content"]; ok {
		t.Fatalf("metadata-only fragment returned content: %v", fragment)
	}
	if fragments.lastOptions.Limit != 2 {
		t.Fatalf("fragment list limit = %d; want 2", fragments.lastOptions.Limit)
	}

	claimPage, err := listTool.Invoke(ctx, "profile-eval", map[string]any{
		"type":   "claim",
		"status": "validated",
		"cursor": "claim-cursor",
		"limit":  3,
	})
	if err != nil {
		t.Fatalf("eval_list_knowledge_refs claim Invoke: %v", err)
	}
	if claimPage["next_cursor"] != "claim-next" || claimPage["has_more"] != true {
		t.Fatalf("claim page = %v", claimPage)
	}
	if claims.lastOptions.Status != "validated" || claims.lastOptions.Cursor != "claim-cursor" {
		t.Fatalf("claim list options = %+v", claims.lastOptions)
	}

	_, err = listTool.Invoke(ctx, "profile-eval", map[string]any{"type": "fact", "status": "active"})
	if err != nil {
		t.Fatalf("eval_list_knowledge_refs fact Invoke: %v", err)
	}
	if facts.lastFilters.Status != domain.FactStatusActive {
		t.Fatalf("fact list filters = %+v", facts.lastFilters)
	}

	communityPage, err := listTool.Invoke(ctx, "profile-eval", map[string]any{
		"type":          "community",
		"metadata_only": true,
	})
	if err != nil {
		t.Fatalf("eval_list_knowledge_refs community Invoke: %v", err)
	}
	community := firstEvalItem(t, communityPage)
	if _, ok := community["summary"]; ok {
		t.Fatalf("metadata-only community returned summary: %v", community)
	}

	edgePage, err := listTool.Invoke(ctx, "profile-eval", map[string]any{"type": "edge", "limit": 4})
	if err != nil {
		t.Fatalf("eval_list_knowledge_refs edge Invoke: %v", err)
	}
	if graph.profileID != "profile-eval" || !strings.Contains(graph.query, "LIMIT 4") || len(graph.params) != 0 || len(edgePage["items"].([]map[string]any)) != 1 {
		t.Fatalf("edge export graph call/page = %q/%q/%v/%v", graph.profileID, graph.query, graph.params, edgePage)
	}

	dreamPage, err := listTool.Invoke(ctx, "profile-eval", map[string]any{
		"type":          "dream",
		"status":        "proposed",
		"cursor":        "dream-cursor",
		"limit":         5,
		"metadata_only": true,
	})
	if err != nil {
		t.Fatalf("eval_list_knowledge_refs dream Invoke: %v", err)
	}
	dream := firstEvalItem(t, dreamPage)
	if _, ok := dream["hypothesis"]; ok {
		t.Fatalf("metadata-only dream returned hypothesis: %v", dream)
	}
	if dreams.lastListOpts.Status != "proposed" || dreams.lastListOpts.Cursor != "dream-cursor" || dreams.lastListOpts.Limit != 5 {
		t.Fatalf("dream list opts = %+v", dreams.lastListOpts)
	}

	getTool, _ := reg.Get("eval_get_knowledge_item")
	for _, tc := range []struct {
		kind string
		id   string
		key  string
	}{
		{kind: "fragment", id: "fragment-1", key: "content"},
		{kind: "claim", id: "claim-1", key: "object"},
		{kind: "community", id: "community-1", key: "summary"},
		{kind: "dream", id: "dream-1", key: "hypothesis"},
	} {
		itemOut, err := getTool.Invoke(ctx, "profile-eval", map[string]any{
			"type":          tc.kind,
			"id":            tc.id,
			"metadata_only": true,
		})
		if err != nil {
			t.Fatalf("eval_get_knowledge_item %s Invoke: %v", tc.kind, err)
		}
		item := itemOut["item"].(map[string]any)
		if _, ok := item[tc.key]; ok {
			t.Fatalf("metadata-only %s returned %s: %v", tc.kind, tc.key, item)
		}
	}
	itemOut, err := getTool.Invoke(ctx, "profile-eval", map[string]any{
		"type":          "fact",
		"id":            "fact-1",
		"metadata_only": true,
	})
	if err != nil {
		t.Fatalf("eval_get_knowledge_item Invoke: %v", err)
	}
	item := itemOut["item"].(map[string]any)
	if _, ok := item["object"]; ok {
		t.Fatalf("metadata-only fact returned object: %v", item)
	}

	if len(audit.entries) < 7 {
		t.Fatalf("audit entries = %d; want eval tool calls", len(audit.entries))
	}
}

func TestEvalRunDreamCycleToolInvokesDreamService(t *testing.T) {
	audit := &evaluationAuditStub{}
	dreams := &stubDreamService{}
	reg, err := BuildDefault(Dependencies{
		EvaluationAudit: audit,
		Dreams:          dreams,
	})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	tool, _ := reg.Get("eval_run_dream_cycle")
	properties := tool.InputSchema["properties"].(map[string]any)
	maxOutputs, ok := schemaNumber(properties["max_outputs"].(map[string]any)["maximum"])
	if !ok || int(maxOutputs) != evalDreamCycleMaxOutputs {
		t.Fatalf("max_outputs maximum = %v, %v", maxOutputs, ok)
	}
	seedDreamsSchema := properties["seed_dreams"].(map[string]any)
	maxSeedDreams, ok := schemaNumber(seedDreamsSchema["maxItems"])
	if !ok || int(maxSeedDreams) != evalDreamCycleMaxOutputs {
		t.Fatalf("seed_dreams maxItems = %v, %v", maxSeedDreams, ok)
	}

	out, err := tool.Invoke(context.Background(), "profile-eval", map[string]any{
		"manual":             false,
		"reflect_enabled":    false,
		"reevaluate_enabled": false,
		"dream_enabled":      true,
		"max_outputs":        4,
		"seed_dreams": []any{
			map[string]any{
				"hypothesis":       "Employment may explain the location period.",
				"what_if":          "What if SAP employment overlaps the location evidence?",
				"possible_outcome": "Recall should surface both source facts together.",
				"rationale":        "Imported relational eval seed.",
				"likelihood":       0.9,
				"confidence":       0.8,
				"source_refs": []any{
					map[string]any{"type": "fact", "id": "fact-employer"},
					map[string]any{"type": "fact", "id": "fact-location"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("eval_run_dream_cycle Invoke: %v", err)
	}
	if out["run_id"] != "run-1" || out["team_id"] != "profile-eval" {
		t.Fatalf("run output = %v", out)
	}
	if dreams.lastRunReq.Manual || dreams.lastRunReq.MaxOutputs != 4 {
		t.Fatalf("run request = %+v", dreams.lastRunReq)
	}
	if dreams.lastRunReq.ReflectEnabled == nil || *dreams.lastRunReq.ReflectEnabled {
		t.Fatalf("reflect flag = %+v", dreams.lastRunReq.ReflectEnabled)
	}
	if dreams.lastRunReq.DreamEnabled == nil || !*dreams.lastRunReq.DreamEnabled {
		t.Fatalf("dream flag = %+v", dreams.lastRunReq.DreamEnabled)
	}
	if len(dreams.lastRunReq.SeedDreams) != 1 {
		t.Fatalf("seed dreams = %+v", dreams.lastRunReq.SeedDreams)
	}
	seed := dreams.lastRunReq.SeedDreams[0]
	if seed.Hypothesis != "Employment may explain the location period." || seed.Likelihood != 0.9 || seed.Confidence != 0.8 {
		t.Fatalf("seed dream = %+v", seed)
	}
	if len(seed.SourceRefs) != 2 || seed.SourceRefs[0].ID != "fact-employer" || seed.SourceRefs[1].ID != "fact-location" {
		t.Fatalf("seed refs = %+v", seed.SourceRefs)
	}
	if len(audit.entries) != 1 || audit.entries[0].EntityID != "eval_run_dream_cycle" {
		t.Fatalf("audit entries = %+v", audit.entries)
	}
}

func TestEvalRecallFeedbackToolsFilterAndScope(t *testing.T) {
	audit := &evaluationAuditStub{}
	teamID := uuid.MustParse("00000000-0000-0000-0000-000000000202")
	store := &evalRecallFeedbackStore{event: &domain.RecallFeedbackEvent{
		RecallID: "recall-1",
		TeamID:   &teamID,
		Query:    "why did recall miss the target?",
	}}
	reg, err := BuildDefault(Dependencies{
		EvaluationAudit:      audit,
		RecallFeedbackEvents: store,
	})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	ctx := requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{TeamID: teamID})

	listTool, _ := reg.Get("eval_list_recall_feedback_events")
	_, err = listTool.Invoke(ctx, "ignored-profile", map[string]any{
		"limit":           12,
		"offset":          5,
		"quality":         "low",
		"include_pending": true,
		"missing_context": true,
		"irrelevant":      false,
		"from":            "2026-06-25T00:00:00Z",
		"to":              "2026-06-26T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("eval_list_recall_feedback_events Invoke: %v", err)
	}
	if store.lastFilter.TeamID == nil || *store.lastFilter.TeamID != teamID {
		t.Fatalf("filter team = %+v; want actor team", store.lastFilter.TeamID)
	}
	if store.lastFilter.Limit != 12 || store.lastFilter.Offset != 5 || store.lastFilter.Quality != "low" || !store.lastFilter.IncludePending {
		t.Fatalf("filter = %+v", store.lastFilter)
	}
	if store.lastFilter.MissingContext == nil || !*store.lastFilter.MissingContext || store.lastFilter.Irrelevant == nil || *store.lastFilter.Irrelevant {
		t.Fatalf("boolean filters = %+v", store.lastFilter)
	}
	if store.lastFilter.From == nil || store.lastFilter.To == nil {
		t.Fatalf("time filters = %+v", store.lastFilter)
	}

	getTool, _ := reg.Get("eval_get_recall_feedback_event")
	out, err := getTool.Invoke(ctx, "ignored-profile", map[string]any{"recall_id": "recall-1"})
	if err != nil {
		t.Fatalf("eval_get_recall_feedback_event Invoke: %v", err)
	}
	event := out["event"].(map[string]any)
	if event["recall_id"] != "recall-1" {
		t.Fatalf("event = %v", event)
	}

	otherTeamID := uuid.MustParse("00000000-0000-0000-0000-000000000303")
	store.event.TeamID = &otherTeamID
	_, err = getTool.Invoke(ctx, "ignored-profile", map[string]any{"recall_id": "recall-1"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("cross-team event err = %v; want not found", err)
	}

	_, err = listTool.Invoke(ctx, "ignored-profile", map[string]any{"from": "not-a-time"})
	if err == nil || !strings.Contains(err.Error(), "from must be RFC3339") {
		t.Fatalf("invalid from err = %v", err)
	}
}

func TestEvalRunRecallCaseWrapperNotRegistered(t *testing.T) {
	reg, err := BuildDefault(Dependencies{EvaluationAudit: &evaluationAuditStub{}})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	if _, ok := reg.Get("eval_run_recall_case"); ok {
		t.Fatal("eval_run_recall_case must not be registered; evaluator calls recall_memory directly")
	}
	if IsEvaluationTool("eval_run_recall_case") {
		t.Fatal("eval_run_recall_case must not be part of the runtime evaluation tool allowlist")
	}
}

func TestEvalLegacyClaimListAndHelpers(t *testing.T) {
	legacy := &evalLegacyClaimList{
		items: []*domain.Claim{{
			ClaimID:    "claim-legacy",
			ProfileID:  "profile-eval",
			Subject:    "legacy",
			Predicate:  "uses",
			Object:     "offset cursor",
			Status:     domain.StatusCandidate,
			Modality:   domain.ModalityAssertion,
			Polarity:   domain.PolarityPlus,
			RecordedAt: time.Date(2026, 6, 26, 2, 0, 0, 0, time.UTC),
		}},
		total: 4,
	}
	page, err := evalListClaims(context.Background(), Dependencies{ClaimList: legacy}, "profile-eval", map[string]any{
		"cursor":        "2",
		"metadata_only": true,
	}, 1, true)
	if err != nil {
		t.Fatalf("evalListClaims legacy: %v", err)
	}
	if legacy.offset != 2 {
		t.Fatalf("legacy offset = %d; want 2", legacy.offset)
	}
	claim := firstEvalItem(t, page)
	if _, ok := claim["object"]; ok {
		t.Fatalf("metadata-only claim returned object: %v", claim)
	}
	if page["next_cursor"] != "3" {
		t.Fatalf("legacy next cursor = %v; want 3", page["next_cursor"])
	}
	if cursorOffset("-1") != 0 || cursorOffset("not-int") != 0 || cursorOffset("7") != 7 {
		t.Fatalf("cursorOffset edge cases failed")
	}
	if firstNonEmpty(uuid.Nil.String(), " ", "fallback") != "fallback" {
		t.Fatalf("firstNonEmpty did not skip nil UUID/blank values")
	}
}

func TestEvalUnavailableConfigAuditAndParserBranches(t *testing.T) {
	if !IsEvaluationTool("eval_get_manifest") || IsEvaluationTool("recall_memory") {
		t.Fatal("IsEvaluationTool did not classify evaluation tools")
	}

	audit := &evaluationAuditStub{}
	reg, err := BuildDefault(Dependencies{
		EvaluationAudit:  audit,
		EvaluationConfig: evalRuntimeConfig{err: errors.New("config down")},
	})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	manifestTool, _ := reg.Get("eval_get_manifest")
	_, err = manifestTool.Invoke(context.Background(), "profile-eval", map[string]any{})
	if err == nil || !strings.Contains(err.Error(), "runtime config unavailable") {
		t.Fatalf("eval_get_manifest config err = %v", err)
	}

	reg, err = BuildDefault(Dependencies{EvaluationAudit: audit})
	if err != nil {
		t.Fatalf("BuildDefault unavailable deps: %v", err)
	}
	listTool, _ := reg.Get("eval_list_knowledge_refs")
	for _, tc := range []map[string]any{
		{"type": "fragment"},
		{"type": "claim"},
		{"type": "fact"},
		{"type": "community"},
		{"type": "dream"},
		{"type": "edge"},
	} {
		_, err = listTool.Invoke(context.Background(), "profile-eval", tc)
		if !errors.Is(err, ErrToolUnavailable) {
			t.Fatalf("eval_list_knowledge_refs %v err = %v; want ErrToolUnavailable", tc, err)
		}
	}
	_, err = listTool.Invoke(context.Background(), "profile-eval", map[string]any{"type": "unknown"})
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("unsupported list type err = %v", err)
	}

	getTool, _ := reg.Get("eval_get_knowledge_item")
	_, err = getTool.Invoke(context.Background(), "profile-eval", map[string]any{"type": "fragment", "id": "missing"})
	if !errors.Is(err, ErrToolUnavailable) {
		t.Fatalf("eval_get_knowledge_item missing dep err = %v; want ErrToolUnavailable", err)
	}
	_, err = evalGetKnowledgeItem(context.Background(), Dependencies{}, "profile-eval", "unknown", "id")
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Fatalf("evalGetKnowledgeItem unsupported err = %v", err)
	}

	listFeedbackTool, _ := reg.Get("eval_list_recall_feedback_events")
	if _, err = listFeedbackTool.Invoke(context.Background(), "profile-eval", map[string]any{}); !errors.Is(err, ErrToolUnavailable) {
		t.Fatalf("eval_list_recall_feedback_events err = %v; want ErrToolUnavailable", err)
	}
	getFeedbackTool, _ := reg.Get("eval_get_recall_feedback_event")
	if _, err = getFeedbackTool.Invoke(context.Background(), "profile-eval", map[string]any{"recall_id": "recall-1"}); !errors.Is(err, ErrToolUnavailable) {
		t.Fatalf("eval_get_recall_feedback_event err = %v; want ErrToolUnavailable", err)
	}
	if _, ok := reg.Get("eval_run_recall_case"); ok {
		t.Fatal("eval_run_recall_case must not be registered")
	}

	keyID := uuid.MustParse("00000000-0000-0000-0000-000000000404")
	teamID := uuid.MustParse("00000000-0000-0000-0000-000000000505")
	ctx := requestctx.WithActorCredential(
		requestctx.WithActorProfile(context.Background(), requestctx.ActorProfile{TeamID: teamID}),
		requestctx.ActorCredential{KeyID: keyID, Role: "write"},
	)
	audit.entries = nil
	if err := auditEvaluationTool(ctx, Dependencies{EvaluationAudit: audit}, "eval_test", 9, true, map[string]any{"kind": "unit"}); err != nil {
		t.Fatalf("auditEvaluationTool: %v", err)
	}
	entry := audit.entries[0]
	if entry.ProfileID == nil || *entry.ProfileID != teamID.String() || entry.ActorKeyID == nil || *entry.ActorKeyID != keyID.String() || entry.ActorRole != "write" {
		t.Fatalf("audit entry actor fields = %+v", entry)
	}

	limited := evalLimit(context.Background(), Dependencies{
		EvaluationConfig: evalRuntimeConfig{evaluation: domain.EvaluationRuntimeConfig{Enabled: true, ExportMaxPageSize: 900}},
	}, map[string]any{"limit": 700})
	if limited != 500 {
		t.Fatalf("evalLimit = %d; want hard cap 500", limited)
	}
	limited = evalLimit(context.Background(), Dependencies{
		EvaluationConfig: evalRuntimeConfig{evaluation: domain.EvaluationRuntimeConfig{Enabled: true, ExportMaxPageSize: 80}},
	}, map[string]any{"limit": 40})
	if limited != 40 {
		t.Fatalf("evalLimit requested cap = %d; want 40", limited)
	}

	if t0, err := optionalTime(""); err != nil || t0 != nil {
		t.Fatalf("optionalTime empty = %v, %v; want nil nil", t0, err)
	}
	event := &domain.RecallFeedbackEvent{TeamID: &teamID}
	if !evalEventInScope(context.Background(), teamID.String(), event) {
		t.Fatal("evalEventInScope should accept matching profile fallback team")
	}
	if evalEventInScope(context.Background(), "not-a-uuid", event) || evalEventInScope(context.Background(), teamID.String(), nil) {
		t.Fatal("evalEventInScope accepted invalid profile or nil event")
	}
}

func firstEvalItem(t *testing.T, page map[string]any) map[string]any {
	t.Helper()
	items, ok := page["items"].([]map[string]any)
	if !ok || len(items) == 0 {
		t.Fatalf("page items = %#v", page["items"])
	}
	return items[0]
}

type evalFragmentStore struct {
	items       []domain.Fragment
	lastOptions fragmentservice.ListOptions
}

func (s *evalFragmentStore) List(_ context.Context, _ string, opts fragmentservice.ListOptions) ([]domain.Fragment, string, error) {
	s.lastOptions = opts
	return s.items, "", nil
}

func (s *evalFragmentStore) GetByID(_ context.Context, _ string, fragmentID string) (*domain.Fragment, error) {
	for i := range s.items {
		if s.items[i].FragmentID == fragmentID {
			return &s.items[i], nil
		}
	}
	return nil, errors.New("fragment not found")
}

type evalClaimListFiltered struct {
	items       []*domain.Claim
	nextCursor  string
	lastOptions claimservice.ListClaimOptions
}

func (s *evalClaimListFiltered) List(_ context.Context, _ string, opts claimservice.ListClaimOptions) (*claimservice.ListClaimsResult, error) {
	s.lastOptions = opts
	return &claimservice.ListClaimsResult{Items: s.items, NextCursor: s.nextCursor, HasMore: s.nextCursor != ""}, nil
}

func (s *evalClaimListFiltered) Get(_ context.Context, _ string, claimID string) (*domain.Claim, error) {
	for _, claim := range s.items {
		if claim.ClaimID == claimID {
			return claim, nil
		}
	}
	return nil, errors.New("claim not found")
}

type evalLegacyClaimList struct {
	items  []*domain.Claim
	total  int
	offset int
}

func (s *evalLegacyClaimList) List(_ context.Context, _ string, limit, offset int) ([]*domain.Claim, int, error) {
	s.offset = offset
	return s.items, s.total, nil
}

type evalFactStore struct {
	items       []*domain.Fact
	lastFilters factservice.FactListFilters
}

func (s *evalFactStore) List(_ context.Context, _ string, filters factservice.FactListFilters, _ int, _ string) ([]*domain.Fact, string, error) {
	s.lastFilters = filters
	return s.items, "", nil
}

func (s *evalFactStore) Get(_ context.Context, _ string, factID string) (*domain.Fact, error) {
	for _, fact := range s.items {
		if fact.FactID == factID {
			return fact, nil
		}
	}
	return nil, errors.New("fact not found")
}

type evalCommunityStore struct {
	items []*domain.Community
}

func (s *evalCommunityStore) List(_ context.Context, _ string, _ int) ([]*domain.Community, error) {
	return s.items, nil
}

func (s *evalCommunityStore) Get(_ context.Context, _ string, communityID string) (*domain.Community, error) {
	for _, community := range s.items {
		if community.CommunityID == communityID {
			return community, nil
		}
	}
	return nil, errors.New("community not found")
}

type evalGraphQuery struct {
	profileID string
	query     string
	params    map[string]any
	rows      []map[string]any
}

func (s *evalGraphQuery) Execute(_ context.Context, profileID string, query string, params map[string]any) (*graphquery.GraphQueryResult, error) {
	s.profileID = profileID
	s.query = query
	s.params = params
	return &graphquery.GraphQueryResult{Rows: s.rows}, nil
}

type evalRecallFeedbackStore struct {
	lastFilter domain.RecallFeedbackEventFilter
	event      *domain.RecallFeedbackEvent
}

func (s *evalRecallFeedbackStore) RecordRecallSnapshot(context.Context, domain.RecallFeedbackEvent) error {
	return nil
}

func (s *evalRecallFeedbackStore) RecordRecallFeedback(context.Context, domain.RecallFeedbackSubmission) error {
	return nil
}

func (s *evalRecallFeedbackStore) ListRecallFeedbackEvents(_ context.Context, filter domain.RecallFeedbackEventFilter) (*domain.RecallFeedbackEventPage, error) {
	s.lastFilter = filter
	items := []domain.RecallFeedbackEvent{}
	if s.event != nil {
		items = append(items, *s.event)
	}
	return &domain.RecallFeedbackEventPage{Items: items, Total: int64(len(items))}, nil
}

func (s *evalRecallFeedbackStore) GetRecallFeedbackEvent(_ context.Context, recallID string) (*domain.RecallFeedbackEvent, error) {
	if s.event != nil && s.event.RecallID == recallID {
		return s.event, nil
	}
	return nil, errors.New("recall feedback event not found")
}

type evalRecallCapture struct {
	calls     int
	profileID string
	req       recallservice.RecallRequest
}

func (s *evalRecallCapture) Recall(_ context.Context, profileID string, req recallservice.RecallRequest) ([]recallservice.RecallHit, error) {
	s.calls++
	s.profileID = profileID
	s.req = req
	return []recallservice.RecallHit{
		{
			Tier:       recallservice.TierActiveFact,
			Score:      0.9,
			FinalScore: 0.95,
			Fact:       &domain.Fact{FactID: "fact-hit", Status: domain.FactStatusActive},
		},
		{
			Tier:  recallservice.TierValidatedClaim,
			Score: 0.8,
			Claim: &domain.Claim{ClaimID: "claim-hit", Status: domain.StatusValidated},
		},
		{
			Tier:         recallservice.TierFragment,
			Score:        0.7,
			SemanticRank: 2,
			KeywordRank:  3,
			Fragment:     &domain.Fragment{FragmentID: "fragment-hit", Status: domain.FragmentStatusActive},
		},
	}, nil
}

type evalContextCapture struct {
	calls int
	req   contextservice.AssembleRequest
}

func (s *evalContextCapture) Trace(context.Context, string, contextservice.TraceRequest) (*contextservice.TraceResult, error) {
	return &contextservice.TraceResult{}, nil
}

func (s *evalContextCapture) Assemble(_ context.Context, _ string, req contextservice.AssembleRequest) (*contextservice.AssembleResult, error) {
	s.calls++
	s.req = req
	return &contextservice.AssembleResult{
		ContextBlock: "context block",
		Items: []contextservice.ContextItem{{
			Type:  "fact",
			ID:    "fact-context",
			Score: 0.5,
			EvidenceFragments: []*domain.Fragment{{
				FragmentID: "fragment-evidence",
			}},
		}},
	}, nil
}

type evalRuntimeConfig struct {
	evaluation domain.EvaluationRuntimeConfig
	err        error
}

func (s evalRuntimeConfig) RecallFeedbackRuntimeConfig(context.Context) (domain.RecallFeedbackRuntimeConfig, error) {
	return domain.RecallFeedbackRuntimeConfig{}, nil
}

func (s evalRuntimeConfig) EvaluationRuntimeConfig(context.Context) (domain.EvaluationRuntimeConfig, error) {
	return s.evaluation, s.err
}
