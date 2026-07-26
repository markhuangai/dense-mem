package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
)

func TestBuildActiveRecallRecordsFeedbackSnapshot(t *testing.T) {
	recorder := &stubRecallFeedbackRecorder{}
	reg, err := BuildActive(Dependencies{
		Recall:               &stubRecallService{},
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		RecallFeedbackEvents: recorder,
		Metrics:              observability.NewInMemoryDiscoverabilityMetrics(),
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	recall, _ := reg.Get(V2ToolRecallMemory)
	out, err := recall.Invoke(v2ContractInvokeContext("read"), "ignored-profile", map[string]any{
		"query": "PostgreSQL memory",
	})
	if err != nil {
		t.Fatalf("recall_memory.Invoke: %v", err)
	}
	if out["recall_id"] != "rec-v2" {
		t.Fatalf("recall_id = %#v, want rec-v2", out["recall_id"])
	}
	if len(recorder.snapshots) != 1 {
		t.Fatalf("snapshots = %d; want 1", len(recorder.snapshots))
	}
	snapshot := recorder.snapshots[0]
	if snapshot.ContractVersion != domain.V2ContractVersion || snapshot.SearchState != string(domain.V2SearchProjectionCurrent) {
		t.Fatalf("snapshot metadata = %+v", snapshot)
	}
	if got := snapshot.SnapshotMetadata["result_schema"]; got != "v2.evidence_relationship_refs.v1" {
		t.Fatalf("snapshot result schema = %#v", got)
	}
	if len(snapshot.ResultRefs) != 2 {
		t.Fatalf("snapshot refs = %+v; want evidence and relationship", snapshot.ResultRefs)
	}
	if snapshot.ResultRefs[0].Type != domain.RecallFeedbackResultTypeEvidence || snapshot.ResultRefs[0].ID != "evidence-v2" {
		t.Fatalf("evidence ref = %+v", snapshot.ResultRefs[0])
	}
	if snapshot.ResultRefs[1].Type != domain.RecallFeedbackResultTypeRelationship || snapshot.ResultRefs[1].ID != "relationship-v2" {
		t.Fatalf("relationship ref = %+v", snapshot.ResultRefs[1])
	}
}

func TestBuildActiveSubmitRecallSessionFeedbackRecordsFeedback(t *testing.T) {
	recorder := &stubRecallFeedbackRecorder{}
	metrics := observability.NewInMemoryDiscoverabilityMetrics()
	reg, err := BuildActive(Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		RecallFeedbackEvents: recorder,
		Metrics:              metrics,
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	submit, _ := reg.Get(V2ToolSubmitRecallSessionFeedback)
	out, err := submit.Invoke(v2ContractInvokeContext("write"), "ignored-profile", map[string]any{
		"recalls": []any{map[string]any{
			"recall_event_id":  "rec-v2",
			"used":             true,
			"answer_supported": true,
			"quality":          "high",
			"missing_context":  false,
			"irrelevant":       false,
			"irrelevant_result_refs": []any{map[string]any{
				"type": "relationship",
				"id":   "relationship-v2",
				"rank": 1,
			}},
			"hypothesis_feedback": []any{map[string]any{
				"hypothesis_id": "dream-v2",
				"used":          true,
				"quality":       "medium",
				"contradicted":  false,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("submit_recall_session_feedback.Invoke: %v", err)
	}
	if out["recorded"] != true || out["recorded_count"] != 1 {
		t.Fatalf("submit output = %#v", out)
	}
	if len(recorder.feedback) != 1 {
		t.Fatalf("feedback submissions = %d; want 1", len(recorder.feedback))
	}
	feedback := recorder.feedback[0]
	if feedback.RecallID != "rec-v2" || !feedback.Used || !feedback.AnswerSupported || feedback.Quality != "high" {
		t.Fatalf("feedback submission = %+v", feedback)
	}
	if len(feedback.IrrelevantRefs) != 1 || feedback.IrrelevantRefs[0].ID != "relationship-v2" {
		t.Fatalf("irrelevant refs = %+v", feedback.IrrelevantRefs)
	}
	if len(feedback.DreamFeedback) != 1 || feedback.DreamFeedback[0].DreamID != "dream-v2" {
		t.Fatalf("hypothesis feedback = %+v", feedback.DreamFeedback)
	}
	if got := len(metrics.RecallFeedbackSamples()); got != 1 {
		t.Fatalf("metrics feedback samples = %d; want 1", got)
	}
}

func TestBuildActiveSubmitRecallSessionFeedbackReportsPartialSuccess(t *testing.T) {
	recorder := &stubRecallFeedbackRecorder{failAfter: 1}
	reg, err := BuildActive(Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		RecallFeedbackEvents: recorder,
		Metrics:              observability.NewInMemoryDiscoverabilityMetrics(),
	})
	if err != nil {
		t.Fatalf("BuildActive: %v", err)
	}
	submit, _ := reg.Get(V2ToolSubmitRecallSessionFeedback)
	out, err := submit.Invoke(v2ContractInvokeContext("write"), "ignored-profile", map[string]any{
		"recalls": []any{
			map[string]any{
				"recall_event_id":  "rec-v2-a",
				"used":             true,
				"answer_supported": true,
				"quality":          "high",
			},
			map[string]any{
				"recall_event_id":  "rec-v2-b",
				"used":             true,
				"answer_supported": false,
				"quality":          "low",
				"feedback_comment": "The recall missed the relevant correction.",
			},
		},
	})
	if err != nil {
		t.Fatalf("submit_recall_session_feedback.Invoke: %v", err)
	}
	if out["recorded"] != true || out["recorded_count"] != 1 || out["partial_success"] != true || out["failed_index"] != 1 {
		t.Fatalf("partial submit output = %#v", out)
	}
	if len(recorder.feedback) != 1 || recorder.feedback[0].RecallID != "rec-v2-a" {
		t.Fatalf("feedback submissions = %#v", recorder.feedback)
	}
}

func TestV2RecallFeedbackSnapshotHelpersCoverOptionalBranches(t *testing.T) {
	validAt := time.Date(2026, 7, 17, 14, 0, 0, 0, time.UTC)
	knownAt := validAt.Add(time.Hour)
	args := v2RecallFeedbackToolArgs(map[string]any{
		"query":                  "PostgreSQL memory",
		"known_evidence_ids":     []any{"evidence-v2"},
		"known_relationship_ids": []any{"relationship-v2"},
		"expand_from_entity_ids": []any{"entity-v2"},
		"contract_version":       "should not persist",
		"include_evidence":       true,
		"use_communities":        true,
		"ignored":                "not persisted",
	}, memoryservice.V2RecallRequest{
		ContractVersion:      domain.V2ContractVersion,
		Query:                "PostgreSQL memory",
		Limit:                7,
		ValidAt:              &validAt,
		KnownAt:              &knownAt,
		KnownEvidenceIDs:     []string{"evidence-v2"},
		KnownRelationshipIDs: []string{"relationship-v2"},
		ExpandFromEntityIDs:  []string{"entity-v2"},
	})
	input := args["input"].(map[string]any)
	if _, ok := input["ignored"]; ok {
		t.Fatalf("input copy persisted ignored key: %#v", input)
	}
	for _, key := range []string{"contract_version", "include_evidence", "use_communities"} {
		if _, ok := input[key]; ok {
			t.Fatalf("input copy persisted legacy key %q: %#v", key, input)
		}
	}
	effective := args["effective"].(map[string]any)
	if effective["valid_at"] != validAt.Format(v2FeedbackTimeFormat) ||
		effective["known_at"] != knownAt.Format(v2FeedbackTimeFormat) {
		t.Fatalf("effective args = %#v", effective)
	}
	for _, key := range []string{"contract_version", "include_evidence", "use_communities"} {
		if _, ok := effective[key]; ok {
			t.Fatalf("effective args persisted legacy key %q: %#v", key, effective)
		}
	}

	refs := v2RecallFeedbackResultRefs(&memoryservice.V2RecallResult{
		SearchState: string(domain.V2SearchProjectionFailed),
		Results: []memoryservice.V2RecallResultItem{{
			RelationshipIDs: []string{"", "relationship-v2"},
		}},
	})
	if len(refs) != 1 || refs[0].Type != domain.RecallFeedbackResultTypeRelationship || refs[0].Rank != 1 {
		t.Fatalf("relationship-only refs = %+v", refs)
	}
	if refs := v2RecallFeedbackResultRefs(nil); len(refs) != 0 {
		t.Fatalf("nil result refs = %+v", refs)
	}

	recorder := &stubRecallFeedbackRecorder{err: errors.New("record failed")}
	res := &memoryservice.V2RecallResult{
		RecallID: "rec-v2",
		Results:  []memoryservice.V2RecallResultItem{{EvidenceID: "evidence-v2", Rank: 1}},
	}
	recordV2RecallFeedbackSnapshot(context.Background(), Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		RecallFeedbackEvents: recorder,
		Metrics:              observability.NewInMemoryDiscoverabilityMetrics(),
	}, map[string]any{"query": "PostgreSQL memory"}, memoryservice.V2RecallRequest{Query: "PostgreSQL memory"}, res)
	if res.RecallID != "" {
		t.Fatalf("recall id = %q; want cleared after snapshot failure", res.RecallID)
	}
}

func TestRecordV2RecallFeedbackSnapshotPrerequisitesAndDegradation(t *testing.T) {
	input := map[string]any{"query": "PostgreSQL memory"}
	req := memoryservice.V2RecallRequest{
		ContractVersion: domain.V2ContractVersion,
		Query:           "PostgreSQL memory",
	}
	recorder := &stubRecallFeedbackRecorder{}
	enabledDeps := Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		RecallFeedbackEvents: recorder,
		Metrics:              observability.NewInMemoryDiscoverabilityMetrics(),
	}

	recordV2RecallFeedbackSnapshot(context.Background(), enabledDeps, input, req, nil)
	recordV2RecallFeedbackSnapshot(context.Background(), enabledDeps, input, req, &memoryservice.V2RecallResult{})
	resWithoutRecorder := &memoryservice.V2RecallResult{RecallID: "rec-v2"}
	recordV2RecallFeedbackSnapshot(context.Background(), Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		Metrics:              observability.NewInMemoryDiscoverabilityMetrics(),
	}, input, req, resWithoutRecorder)
	resFeedbackDisabled := &memoryservice.V2RecallResult{RecallID: "rec-v2"}
	recordV2RecallFeedbackSnapshot(context.Background(), Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: false},
		RecallFeedbackEvents: recorder,
		Metrics:              observability.NewInMemoryDiscoverabilityMetrics(),
	}, input, req, resFeedbackDisabled)
	resWithoutMetrics := &memoryservice.V2RecallResult{RecallID: "rec-v2"}
	recordV2RecallFeedbackSnapshot(context.Background(), Dependencies{
		RecallFeedbackConfig: stubRecallFeedbackConfig{enabled: true},
		RecallFeedbackEvents: recorder,
	}, input, req, resWithoutMetrics)
	if len(recorder.snapshots) != 0 {
		t.Fatalf("snapshots = %d; want no snapshots before prerequisites are met", len(recorder.snapshots))
	}
	if resWithoutRecorder.RecallID == "" || resFeedbackDisabled.RecallID == "" || resWithoutMetrics.RecallID == "" {
		t.Fatal("recall ids should not be cleared for no-op prerequisite exits")
	}

	res := &memoryservice.V2RecallResult{
		RecallID:    "rec-v2",
		SearchState: string(domain.V2SearchProjectionCurrent),
		Degradation: &memoryservice.V2RecallDegradationResult{
			Optional: true,
			Code:     "embedding_provider_unavailable",
			Message:  "embedding provider unavailable",
		},
		Results: []memoryservice.V2RecallResultItem{{
			EvidenceID: "evidence-v2",
			Rank:       1,
		}},
	}
	recordV2RecallFeedbackSnapshot(context.Background(), enabledDeps, input, req, res)
	if len(recorder.snapshots) != 1 {
		t.Fatalf("snapshots = %d; want 1", len(recorder.snapshots))
	}
	degradation := recorder.snapshots[0].Degradation
	if degradation["code"] != "embedding_provider_unavailable" || degradation["optional"] != true {
		t.Fatalf("degradation = %#v", degradation)
	}
	if res.RecallID != "rec-v2" {
		t.Fatalf("recall id = %q; want preserved after snapshot success", res.RecallID)
	}
}
