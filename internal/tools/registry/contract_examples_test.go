package registry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

func TestContractToolExamplesValidateAgainstCurrentSchemas(t *testing.T) {
	tools := toolMap(t)
	examples := contractToolExamples()
	if len(examples) != len(ContractToolNames()) {
		t.Fatalf("example count = %d, want %d", len(examples), len(ContractToolNames()))
	}
	for _, name := range ContractToolNames() {
		example, ok := examples[name]
		if !ok {
			t.Fatalf("missing example for %s", name)
		}
		tool, err := requireTool(tools, name)
		if err != nil {
			t.Fatal(err)
		}
		for _, request := range append([]map[string]any{example.Request}, continuationRequests(example.Continuations)...) {
			input := instantiateContractExampleRequest(t, request)
			if err := ValidateContractInput(tool, input, tool.RequiredScopes); err != nil {
				t.Fatalf("%s example validation: %v\ninput: %#v", name, err, input)
			}
		}
		description := contractToolDescription(name)
		if got, want := strings.Count(description, contractExampleRequestInstruction), 1+len(example.Continuations); got != want {
			t.Fatalf("%s example instructions = %d, want %d", name, got, want)
		}
		for _, section := range []string{"When to use:", "Prerequisites:", "Example request", "Result:", "Next action:"} {
			if !strings.Contains(description, section) {
				t.Fatalf("%s description missing %q", name, section)
			}
		}
	}
}

func TestRememberExampleRetainsTheWholePayloadForReplay(t *testing.T) {
	tools := toolMap(t)
	remember, err := requireTool(tools, ToolRemember)
	if err != nil {
		t.Fatal(err)
	}
	original := instantiateContractExampleRequest(t, contractToolExamples()[ToolRemember].Request)
	replay := cloneContractExampleRequest(t, original)
	if err := ValidateContractInput(remember, original, remember.RequiredScopes); err != nil {
		t.Fatal(err)
	}
	if err := ValidateContractInput(remember, replay, remember.RequiredScopes); err != nil {
		t.Fatal(err)
	}
	originalRequest, err := rememberRequestFromContractInput(original)
	if err != nil {
		t.Fatal(err)
	}
	replayRequest, err := rememberRequestFromContractInput(replay)
	if err != nil {
		t.Fatal(err)
	}
	originalHash, err := rememberapp.CanonicalRequestBodyHash(originalRequest.Evidence, originalRequest.EntityHints, originalRequest.RelationshipHints)
	if err != nil {
		t.Fatal(err)
	}
	replayHash, err := rememberapp.CanonicalRequestBodyHash(replayRequest.Evidence, replayRequest.EntityHints, replayRequest.RelationshipHints)
	if err != nil {
		t.Fatal(err)
	}
	if originalHash != replayHash || !reflect.DeepEqual(original, replay) {
		t.Fatalf("unchanged request did not retain replay identity")
	}

	changed := cloneContractExampleRequest(t, original)
	changedEvidence := changed["evidence"].([]any)[0].(map[string]any)
	changedEvidence["metadata"].(map[string]any)["origin"] = "changed-example"
	if err := ValidateContractInput(remember, changed, remember.RequiredScopes); err != nil {
		t.Fatal(err)
	}
	changedRequest, err := rememberRequestFromContractInput(changed)
	if err != nil {
		t.Fatal(err)
	}
	changedHash, err := rememberapp.CanonicalRequestBodyHash(changedRequest.Evidence, changedRequest.EntityHints, changedRequest.RelationshipHints)
	if err != nil {
		t.Fatal(err)
	}
	if original["idempotency_key"] != changed["idempotency_key"] || originalHash == changedHash {
		t.Fatalf("metadata change did not distinguish the request")
	}
}

func TestDreamConfirmationExampleRequiresIndependentEvidence(t *testing.T) {
	example := contractToolExamples()[ToolResolveDreamFeedback]
	if len(example.Continuations) != 1 {
		t.Fatalf("confirmation continuations = %d, want 1", len(example.Continuations))
	}
	input := instantiateContractExampleRequest(t, example.Continuations[0].Request)
	if input["decision"] != "confirm_true" {
		t.Fatalf("confirmation decision = %#v", input["decision"])
	}
	evidence := input["evidence"].([]any)
	if len(evidence) != 1 || !strings.Contains(evidence[0].(map[string]any)["content"].(string), "independent") {
		t.Fatalf("confirmation evidence = %#v", evidence)
	}
	relationship := input["relationships"].([]any)[0].(map[string]any)
	if _, exists := relationship["known_evidence_ids"]; exists {
		t.Fatalf("confirmation example reused known evidence: %#v", relationship)
	}
	tool, err := requireTool(toolMap(t), ToolResolveDreamFeedback)
	if err != nil {
		t.Fatal(err)
	}
	require.NoError(t, ValidateContractInput(tool, input, tool.RequiredScopes))
}

func TestCorrectionExampleUsesReturnedTraceState(t *testing.T) {
	description := contractToolDescription(ToolCorrectRelationship)
	for _, want := range []string{
		"owned active Relationship trace whose stopped_reason is null",
		"Trace evidence_supports is lineage, not a correction-ready set",
		"latest evidence_support_decision_events decision is grant or reinstate",
		"exclude revoke",
		"span_start, and span_end to supports[].evidence_id, start, and end",
		"refresh trace or stop",
		"numeric values in this valid example only",
		"complete supports array",
		"support_set_mismatch",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("correction guidance missing %q", want)
		}
	}
	traceDescription := contractToolDescription(ToolTraceMemory)
	for _, want := range []string{"support lineage, not a correction-ready set", "latest-decision selection", "span_start/span_end mapping", "non-null stopped_reason", "refresh trace or stop"} {
		if !strings.Contains(traceDescription, want) {
			t.Fatalf("trace guidance missing %q", want)
		}
	}

	example := contractToolExamples()[ToolCorrectRelationship].Request
	if version, ok := example["expected_version"].(int); !ok || version != 1 {
		t.Fatalf("correction version = %#v", example["expected_version"])
	}
	support := example["supports"].([]any)[0].(map[string]any)
	if start, ok := support["start"].(int); !ok || start != 0 {
		t.Fatalf("correction support start = %#v", support["start"])
	}
	if end, ok := support["end"].(int); !ok || end != 43 {
		t.Fatalf("correction support end = %#v", support["end"])
	}
	tool, err := requireTool(toolMap(t), ToolCorrectRelationship)
	if err != nil {
		t.Fatal(err)
	}
	require.NoError(t, ValidateContractInput(tool, instantiateContractExampleRequest(t, example), tool.RequiredScopes))
}

func TestCorrectionContinuationMapsReturnedCandidateEndpoints(t *testing.T) {
	description := contractToolDescription(ToolCorrectRelationship)
	for _, want := range []string{
		"valid example illustrates object_entity candidates",
		"every endpoint represented in candidates",
		"subject_entity choice in subject_entity_id",
		"object_entity choice in object_entity_id",
		"no selection field for an endpoint that is not represented",
		"Generate and retain a distinct confirmation key",
	} {
		if !strings.Contains(description, want) {
			t.Fatalf("correction continuation guidance missing %q", want)
		}
	}

	continuations := contractToolExamples()[ToolCorrectRelationship].Continuations
	if len(continuations) != 1 {
		t.Fatalf("correction continuations = %d, want 1", len(continuations))
	}
	selection := continuations[0].Request["selection"].(map[string]any)
	if got := selection["object_entity_id"]; got != returnedObjectCandidateID {
		t.Fatalf("object candidate placeholder = %#v", got)
	}
	if _, exists := selection["subject_entity_id"]; exists {
		t.Fatalf("object-only example included subject selection: %#v", selection)
	}
	tool, err := requireTool(toolMap(t), ToolCorrectRelationship)
	if err != nil {
		t.Fatal(err)
	}
	require.NoError(t, ValidateContractInput(tool, instantiateContractExampleRequest(t, continuations[0].Request), tool.RequiredScopes))
}

func TestContractToolExamplesDescribeBoundedRecovery(t *testing.T) {
	feedback := contractToolDescription(ToolSubmitRecallSessionFeedback)
	for _, want := range []string{
		"failed_index",
		"Items before failed_index were recorded; omit them",
		"correct the failed item and submit it with all later unprocessed items",
		"resend the failed item and all later unprocessed items unchanged",
		"next_action",
		"remediation",
		"correct_and_resubmit",
		"retry_same_request",
		"stop",
	} {
		if !strings.Contains(feedback, want) {
			t.Fatalf("feedback guidance missing %q", want)
		}
	}

	dream := contractToolDescription(ToolResolveDreamFeedback)
	for _, want := range []string{"hypothesis_id, status, and optional submission_id", "structured terminal failure", "next_action", "remediation", "bypass a conflict", "corrected resubmission", "expressly requires it"} {
		if !strings.Contains(dream, want) {
			t.Fatalf("Dream guidance missing %q", want)
		}
	}
}

func continuationRequests(continuations []contractToolExampleContinuation) []map[string]any {
	requests := make([]map[string]any, 0, len(continuations))
	for _, continuation := range continuations {
		requests = append(requests, continuation.Request)
	}
	return requests
}

func instantiateContractExampleRequest(t *testing.T, request map[string]any) map[string]any {
	t.Helper()
	bindings := map[string]string{
		returnedEvidenceID:        uuid.NewString(),
		returnedRelationshipID:    uuid.NewString(),
		returnedRecallID:          "rec_" + uuid.NewString(),
		returnedHypothesisID:      uuid.NewString(),
		returnedSubmissionID:      uuid.NewString(),
		returnedConfirmationToken: uuid.NewString(),
		returnedObjectCandidateID: uuid.NewString(),
		newOperationKey:           "example-operation-" + uuid.NewString(),
		newConfirmationKey:        "example-confirmation-" + uuid.NewString(),
	}
	return replaceContractExamplePlaceholders(t, cloneContractExampleRequest(t, request), bindings).(map[string]any)
}

func replaceContractExamplePlaceholders(t *testing.T, value any, bindings map[string]string) any {
	t.Helper()
	switch typed := value.(type) {
	case string:
		if replacement, ok := bindings[typed]; ok {
			return replacement
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = replaceContractExamplePlaceholders(t, item, bindings)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = replaceContractExamplePlaceholders(t, item, bindings)
		}
		return typed
	default:
		return value
	}
}

func cloneContractExampleRequest(t *testing.T, request map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var cloned map[string]any
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}
