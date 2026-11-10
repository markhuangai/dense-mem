package registry

import (
	"encoding/json"
	"strings"
)

const (
	returnedEvidenceID        = "<returned_evidence_id>"
	returnedRecallID          = "<returned_recall_id>"
	returnedSubmissionID      = "<returned_submission_id>"
	returnedConfirmationToken = "<returned_confirmation_token>"
	newOperationKey           = "<new_retained_operation_key>"
	newConfirmationKey        = "<new_retained_confirmation_key>"

	contractExampleRequestInstruction = "Example request (replace each <returned_...> placeholder with its documented prerequisite result; generate and retain each <new_..._key> before calling):\n"
)

type contractToolExample struct {
	WhenToUse     string
	Prerequisites string
	Request       map[string]any
	Result        string
	NextAction    string
	Continuations []contractToolExampleContinuation
}

type contractToolExampleContinuation struct {
	When          string
	Prerequisites string
	Request       map[string]any
	Result        string
	NextAction    string
}

func contractToolDescription(name string) string {
	example, ok := contractToolExamples()[name]
	if !ok {
		panic("missing contract tool example for " + name)
	}
	sections := []string{
		"When to use: " + example.WhenToUse,
		"Prerequisites: " + example.Prerequisites,
		contractExampleRequestInstruction + contractExampleJSON(example.Request),
		"Result: " + example.Result,
		"Next action: " + example.NextAction,
	}
	for _, continuation := range example.Continuations {
		sections = append(sections, strings.Join([]string{
			"Conditional continuation — " + continuation.When,
			"Prerequisites: " + continuation.Prerequisites,
			contractExampleRequestInstruction + contractExampleJSON(continuation.Request),
			"Result: " + continuation.Result,
			"Next action: " + continuation.NextAction,
		}, "\n"))
	}
	return strings.Join(sections, "\n\n")
}

func contractExampleJSON(request map[string]any) string {
	encoded, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		panic("encode contract tool example: " + err.Error())
	}
	return string(encoded)
}

func contractToolExamples() map[string]contractToolExample {
	return map[string]contractToolExample{
		ToolRemember: {
			WhenToUse:     "Store exact Evidence and provenance in one synchronous operation.",
			Prerequisites: "Choose private or team_shared from user intent. Retain a fresh operation key and complete request before sending. Refresh tools/list after upgrading.",
			Request:       map[string]any{"target_space": "private", "idempotency_key": newOperationKey, "evidence": []any{map[string]any{"content": "Dense-Mem stores durable knowledge in PostgreSQL.", "source_type": "manual", "source": "architecture note", "source_key": "example:durable-knowledge", "source_revision": "1"}}},
			Result:        "Completed returns durable Evidence IDs after assessment and required embeddings commit atomically. Failure commits no new memory.",
			NextAction:    "Retry only the unchanged complete request with its retained key when instructed. retired_contract cannot be replayed; inspect its retained original outcome before starting a distinct v3 operation. To supersede owned Evidence, submit exact replacement Evidence with supersedes_evidence_ids and a new retained key.",
		},
		ToolIngestSession: {
			WhenToUse:     "Save exact current user-authored session events in private memory. Dense-Mem selects verbatim excerpts and preserves original Unicode-rune offsets.",
			Prerequisites: "Use an authorized private credential. Retain a fresh operation key and all original events.",
			Request:       map[string]any{"idempotency_key": newOperationKey, "framework": "generic", "app_name": "notes", "user_id": "external-user", "session_id": "external-session", "events": []any{map[string]any{"event_id": "user-event-1", "text": "Ari uses Go.", "occurred_at": "2026-10-10T10:00:00Z"}}},
			Result:        "The terminal result includes accepted/duplicate event counts, Evidence IDs, and memory_outcome. no_memory explicitly means no excerpts were selected.",
			NextAction:    "Use returned Evidence IDs for trace_memory. Retry unfinished work only with the unchanged original events and retained request key.",
		},
		ToolRecallMemory: {
			WhenToUse: "Find eligible Evidence and useful current related-topic hints.", Prerequisites: "Use a specific question or 1–10 current topic IDs returned by Recall.",
			Request:    map[string]any{"query": "Where does Dense-Mem store durable knowledge?", "limit": 5, "topic_limit": 3},
			Result:     "Results contain exact bounded Evidence contexts and alternate equivalent Evidence IDs. related_topics includes identity, name, description, eligible Evidence count and rank; topics_truncated is explicit.",
			NextAction: "Follow a returned topic with topic_ids and known_evidence_ids to exclude seen Evidence. Topic filters select the direct union without descendants. Refresh unavailable topics and report optional degradations.",
		},
		ToolTraceMemory: {
			WhenToUse: "Inspect authorized Evidence provenance, decisions and lifecycle.", Prerequisites: "Use an Evidence ID returned by Remember, a session or Recall.",
			Request:    map[string]any{"evidence_id": returnedEvidenceID, "include_evidence_content": true, "include_transitions": true, "max_events": 100, "max_fragment_content_runes": 2000},
			Result:     "The result includes Evidence, exact occurrences, source revisions, decisions, lifecycle and search projection metadata within a shared event budget.",
			NextAction: "Report omissions and truncation. Do not infer that a bounded history is complete.",
		},
		ToolRetractEvidence: {
			WhenToUse: "Retract caller-owned Evidence while retaining provenance.", Prerequisites: "Use owned Evidence IDs and retain a new operation key.",
			Request: map[string]any{"evidence_ids": []any{returnedEvidenceID}, "reason": "The source is no longer valid.", "idempotency_key": newOperationKey},
			Result:  "The completed result returns an append-only decision ID and retracted Evidence IDs.", NextAction: "Retry only the saved unchanged request. Trace the Evidence to inspect its retained lifecycle.",
		},
		ToolSubmitRecallSessionFeedback: {
			WhenToUse: "Record voluntary truthful feedback after using a Recall.", Prerequisites: "Use its returned recall_id as recall_event_id.",
			Request: map[string]any{"recalls": []any{map[string]any{"recall_event_id": returnedRecallID, "used": true, "answer_supported": true, "quality": "high"}}},
			Result:  "Returns recorded_count or an explicit partial failure and failed_index.", NextAction: "For partial failure, omit recorded items before failed_index and follow next_action for the failed and later items. Never invent feedback.",
		},
		ToolExportMemoryPack: {
			WhenToUse: "Export 1–500 authorized Evidence IDs with provenance.", Prerequisites: "Use Evidence IDs returned by supported memory tools.",
			Request: map[string]any{"name": "project-memory-pack", "description": "Project Evidence and provenance.", "evidence_ids": []any{returnedEvidenceID}, "include_evidence": true},
			Result:  "Returns canonical dense-mem.memory-pack.v3.0 JSON, content_sha256, counts and explicit omissions.", NextAction: "Verify the hash and counts and report all omissions.",
		},
	}
}
