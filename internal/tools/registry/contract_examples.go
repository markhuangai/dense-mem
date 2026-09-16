package registry

import (
	"encoding/json"
	"strings"
)

const (
	returnedEvidenceID        = "<returned_evidence_id>"
	returnedRelationshipID    = "<returned_relationship_id>"
	returnedRecallID          = "<returned_recall_id>"
	returnedHypothesisID      = "<returned_hypothesis_id>"
	returnedSubmissionID      = "<returned_submission_id>"
	returnedConfirmationToken = "<returned_confirmation_token>"
	returnedObjectCandidateID = "<returned_object_candidate_entity_id>"
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
			WhenToUse:     "Store exact evidence and optional grounded Relationship proposals in one synchronous terminal operation. Each proposal cites submitted evidence; the server owns grounding and acceptance. Use exactly one object shape: {\"object\":{\"entity\":{\"name\":\"PostgreSQL\",\"entity_kind\":\"product\"}}} or {\"object\":{\"value\":{\"type\":\"string\",\"value\":\"PostgreSQL\"}}}.",
			Prerequisites: "Create a fresh operation key and retain this complete request before sending it.",
			Request: map[string]any{
				"idempotency_key": newOperationKey,
				"evidence": []any{map[string]any{
					"content":         "Dense-Mem stores durable knowledge in PostgreSQL.",
					"source_type":     "manual",
					"source":          "architecture note",
					"source_key":      "example:durable-knowledge",
					"source_revision": "1",
					"metadata":        map[string]any{"origin": "contract-example"},
				}},
				"relationships": []any{map[string]any{
					"ref":              "stores-durable-knowledge",
					"subject":          map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
					"predicate":        map[string]any{"proposed_key": "stores_in"},
					"object":           map[string]any{"entity": map[string]any{"name": "PostgreSQL", "entity_kind": "product"}},
					"polarity":         "+",
					"evidence_indices": []any{0},
				}},
			},
			Result:     "The terminal result reports completed or failed processing. Stored evidence has an evidence_id; stored Relationship splits have relationship_id values.",
			NextAction: "On retry_same_request, resend the unchanged entire original request, including metadata and idempotency_key. A deliberate changed operation is distinct from a retry: use a fresh key and never rotate a key to bypass a conflict.",
		},
		ToolRetractEvidence: {
			WhenToUse:     "Retract caller-owned evidence while preserving append-only provenance.",
			Prerequisites: "Use an evidence_id returned by remember or trace, and create a fresh retained operation key.",
			Request: map[string]any{
				"evidence_ids":    []any{returnedEvidenceID},
				"reason":          "The source is no longer valid.",
				"idempotency_key": newOperationKey,
			},
			Result:     "The completed result returns a decision_id, the retracted IDs, and counts for affected, pending, and retained active Relationships.",
			NextAction: "Use the counts to explain the durable effect. Retry only with the saved unchanged request; refresh state before a deliberately changed retraction.",
		},
		ToolCorrectRelationship: {
			WhenToUse:     "Replace one caller-owned active Relationship while preserving its evidence provenance and superseding the original atomically.",
			Prerequisites: "Start with an owned active Relationship trace whose stopped_reason is null. Trace evidence_supports is lineage, not a correction-ready set: for each evidence_support_id, retain a support only when its latest evidence_support_decision_events decision is grant or reinstate; exclude revoke. Map its evidence_id, span_start, and span_end to supports[].evidence_id, start, and end. If a support has no latest decision or trace state is bounded or ambiguous, refresh trace or stop. The numeric values in this valid example only show field shapes: replace expected_version and the complete supports array with the current trace values; create and retain a fresh operation key.",
			Request: map[string]any{
				"action":           "submit",
				"relationship_id":  returnedRelationshipID,
				"expected_version": 1,
				"patch":            map[string]any{"predicate": map[string]any{"key": "uses"}},
				"supports": []any{map[string]any{
					"evidence_id": returnedEvidenceID,
					"start":       0,
					"end":         43,
				}},
				"reason":          "The predicate was resolved incorrectly.",
				"idempotency_key": newOperationKey,
			},
			Result:     "A completed result names the superseded original and active successor. An awaiting_confirmation result supplies a submission_id, confirmation_token, and candidate Entity IDs.",
			NextAction: "Use the supplied continuation only when confirmation is requested. For stale state or support_set_mismatch, refresh trace and submit a new correction with a new key.",
			Continuations: []contractToolExampleContinuation{{
				When:          "the submit result has processing_state awaiting_confirmation",
				Prerequisites: "This valid example illustrates object_entity candidates. Use submission_id and confirmation_token returned by submit. For every endpoint represented in candidates, choose exactly one returned entity_id: place a subject_entity choice in subject_entity_id and an object_entity choice in object_entity_id; include no selection field for an endpoint that is not represented. Generate and retain a distinct confirmation key.",
				Request: map[string]any{
					"action":             "confirm",
					"submission_id":      returnedSubmissionID,
					"confirmation_token": returnedConfirmationToken,
					"selection":          map[string]any{"object_entity_id": returnedObjectCandidateID},
					"idempotency_key":    newConfirmationKey,
				},
				Result:     "The terminal result either completes the correction or returns bounded rejection or failure guidance.",
				NextAction: "Retry a confirmation only with its exact saved payload and confirmation key; a new submission is required after expiration or changed state.",
			}},
		},
		ToolRecallMemory: {
			WhenToUse:     "Recall active evidence contexts and graph-shaped Relationship handles for a bounded question.",
			Prerequisites: "None. Use a specific question and preserve returned handles for any follow-up.",
			Request: map[string]any{
				"query":              "Where does Dense-Mem store durable knowledge?",
				"limit":              5,
				"relationship_limit": 5,
				"community_limit":    0,
			},
			Result:     "Results provide bounded evidence context; related_relationships provide relationship_id values; recall_id and suggested_actions identify supported follow-ups. Degradations are explicit.",
			NextAction: "Use a returned relationship_id for trace_memory. If submit_recall_session_feedback is suggested, pass the returned recall_event_id; report degradations instead of treating an empty or partial result as complete.",
		},
		ToolTraceMemory: {
			WhenToUse:     "Trace one same-team Relationship through supporting evidence, decisions, transitions, and lineage.",
			Prerequisites: "Use a relationship_id returned by remember or recall_memory.",
			Request: map[string]any{
				"relationship_id":          returnedRelationshipID,
				"include_evidence_content": false,
				"include_verification":     true,
				"include_transitions":      true,
				"max_depth":                2,
				"max_edges":                50,
			},
			Result:     "The result connects the Relationship to evidence_supports, evidence, verification events, transitions, conflicts, and lineage within the requested bounds.",
			NextAction: "Trace returns support lineage, not a correction-ready set. For an owned correction, use correct_relationship's latest-decision selection and span_start/span_end mapping. A non-null stopped_reason is a bound: refresh trace or stop; never infer a complete support set.",
		},
		ToolSubmitRecallSessionFeedback: {
			WhenToUse:     "Record bounded, truthful feedback about a recall session.",
			Prerequisites: "Use the recall_event_id supplied by recall_memory in suggested_actions after using that recall.",
			Request: map[string]any{
				"recalls": []any{map[string]any{
					"recall_event_id":  returnedRecallID,
					"used":             true,
					"answer_supported": true,
					"quality":          "high",
				}},
			},
			Result:     "The result reports recorded and recorded_count, or a bounded partial failure with the failed index and remediation.",
			NextAction: "For a partial failure, use failed_index with the returned next_action and remediation. Items before failed_index were recorded; omit them. For correct_and_resubmit, correct the failed item and submit it with all later unprocessed items. For retry_same_request, resend the failed item and all later unprocessed items unchanged; stop only when instructed. Do not invent feedback for a recall you did not use.",
		},
		ToolListDreams: {
			WhenToUse:     "List reviewable Hypotheses without treating them as accepted memory.",
			Prerequisites: "None. Dream visibility can be feature-gated, and an empty list is not a memory result.",
			Request: map[string]any{
				"status": "proposed",
				"limit":  20,
			},
			Result:     "The result returns Hypothesis summaries and an optional next_cursor for another page.",
			NextAction: "Use a returned hypothesis_id with get_dream or resolve_dream_feedback. Keep a Hypothesis separate from accepted evidence and default recall.",
		},
		ToolGetDream: {
			WhenToUse:     "Fetch one authorized Hypothesis with its source references before reviewing it.",
			Prerequisites: "Use a hypothesis_id returned by list_dreams, recall_memory, or another authorized workflow.",
			Request: map[string]any{
				"hypothesis_id": returnedHypothesisID,
			},
			Result:     "The result returns the Hypothesis, status, source references, and derivation context; it is not accepted memory or submitted evidence.",
			NextAction: "Use its hypothesis_id only for feedback. Supply independently obtained evidence for confirmation; never turn this Hypothesis or recalled context into new evidence.",
		},
		ToolResolveDreamFeedback: {
			WhenToUse:     "Record a lifecycle decision about one Hypothesis, or confirm it only with independently supplied evidence.",
			Prerequisites: "Use a hypothesis_id returned by an authorized workflow. For confirmation, gather new evidence independently of the Hypothesis and any recalled context.",
			Request: map[string]any{
				"hypothesis_id": returnedHypothesisID,
				"decision":      "reject",
				"reason":        "The hypothesis is not supported by the available evidence.",
			},
			Result:     "Lifecycle feedback returns the Hypothesis status. Successful confirmation returns hypothesis_id, status, and optional submission_id; structured terminal failures carry their own next_action and remediation.",
			NextAction: "For lifecycle feedback, use the returned status. For a confirmation failure, follow its returned next_action and remediation; reuse an exact payload only when that guidance says to retry. Never alter a payload or key to bypass a conflict; make a corrected resubmission only when the returned remediation expressly requires it. Never use the Hypothesis itself as evidence.",
			Continuations: []contractToolExampleContinuation{{
				When:          "confirming a Hypothesis with independently obtained evidence",
				Prerequisites: "Use the returned hypothesis_id and new evidence that was not copied from the Hypothesis or recall output.",
				Request: map[string]any{
					"hypothesis_id": returnedHypothesisID,
					"decision":      "confirm_true",
					"evidence": []any{map[string]any{
						"content":         "An independent architecture source states that Dense-Mem stores durable knowledge in PostgreSQL.",
						"source_type":     "document",
						"source":          "independent architecture source",
						"source_key":      "example:independent-architecture-source",
						"source_revision": "1",
					}},
					"relationships": []any{map[string]any{
						"ref":              "independent-durable-store",
						"subject":          map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
						"predicate":        map[string]any{"proposed_key": "stores_in"},
						"object":           map[string]any{"entity": map[string]any{"name": "PostgreSQL", "entity_kind": "product"}},
						"polarity":         "+",
						"modality":         "statement",
						"evidence_indices": []any{0},
					}},
				},
				Result:     "A successful confirmation returns hypothesis_id, status, and optional submission_id. A structured terminal failure returns bounded next_action and remediation with no partial semantic commit.",
				NextAction: "Follow the returned next_action and remediation. Keep the exact confirmation payload only for a retry the server explicitly authorizes. Never change an input or key to bypass a conflict; make a corrected resubmission only when the returned remediation expressly directs it. A failed confirmation never makes the Hypothesis accepted memory by itself.",
			}},
		},
		ToolExportMemoryPack: {
			WhenToUse:     "Export selected active Relationships with support provenance.",
			Prerequisites: "Use active relationship_id values returned by remember, recall_memory, or trace_memory.",
			Request: map[string]any{
				"name":                 "project-memory-pack",
				"description":          "Active project relationships with support provenance.",
				"relationship_ids":     []any{returnedRelationshipID},
				"include_evidence":     true,
				"include_entity_names": true,
			},
			Result:     "The result returns artifact_json, content_sha256, filename, counts, and explicit omissions.",
			NextAction: "Use the hash and counts to verify the returned artifact. Resolve or report omissions; do not infer that omitted or inactive Relationships were exported.",
		},
	}
}
