package registry

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateContractInputIssuesAggregatesRememberProblems(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)

	args := map[string]any{
		"unknown": true,
		"evidence": []any{
			map[string]any{
				"content":                 "",
				"source_key":              "source-1",
				"supersedes_evidence_ids": []any{"evidence-old"},
				"unknown":                 true,
			},
			"not-an-object",
		},
		"relationships": []any{
			map[string]any{
				"ref":              "duplicate",
				"evidence_indices": []any{"bad"},
				"unknown":          true,
			},
			map[string]any{
				"ref":              "duplicate",
				"evidence_indices": []any{0},
			},
		},
	}

	result := ValidateContractInputIssues(remember, args, []string{"write"})
	require.NotEmpty(t, result.Issues)
	require.True(t, result.IssuesTruncated)
	for index := 1; index < len(result.Issues); index++ {
		previous := result.Issues[index-1]
		current := result.Issues[index]
		require.LessOrEqual(t, previous.Path, current.Path)
	}
	require.Contains(t, result.Issues, ContractValidationIssue{Path: "/unknown", Code: "unknown_field", Message: "unknown field: unknown"})

	data := ContractValidationErrorData(result)
	require.Equal(t, "validation_failed", data["reason"])
	require.Equal(t, result.IssuesTruncated, data["issues_truncated"])
	require.Len(t, data["issues"], len(result.Issues))
}

func TestValidateContractInputIssuesHonorsEarlyScopeAndTenantGuards(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)

	missingScope := ValidateContractInputIssues(remember, validFlatRelationshipSubmission(), nil)
	require.Len(t, missingScope.Issues, 1)
	require.Equal(t, "missing_scope", missingScope.Issues[0].Code)

	withTenant := validFlatRelationshipSubmission()
	withTenant["team_id"] = "caller-selected-team"
	tenant := ValidateContractInputIssues(remember, withTenant, []string{"write"})
	require.Len(t, tenant.Issues, 1)
	require.Equal(t, "tenant_override", tenant.Issues[0].Code)
}

func TestValidateContractInputIssuesDispatchesToolSpecificValidation(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		args  map[string]any
		scope string
		want  string
	}{
		{name: "recall", tool: ToolRecallMemory, args: map[string]any{"known_evidence_ids": []any{"same", "same"}}, scope: "read", want: "duplicate"},
		{name: "retract", tool: ToolRetractEvidence, args: map[string]any{"evidence_ids": []any{"same", "same"}}, scope: "write", want: "duplicate"},
		{name: "trace", tool: ToolTraceMemory, args: map[string]any{"predicate_keys": []any{"same", "same"}}, scope: "read", want: "duplicate"},
		{name: "export", tool: ToolExportMemoryPack, args: map[string]any{"relationship_ids": []any{"same", "same"}}, scope: "read", want: "duplicate"},
		{name: "correction", tool: ToolCorrectRelationship, args: map[string]any{"action": "unknown"}, scope: "write", want: "submit or confirm"},
		{name: "recall feedback", tool: ToolSubmitRecallSessionFeedback, args: map[string]any{"recalls": []any{map[string]any{"quality": "low"}}}, scope: "write", want: "feedback_comment"},
		{name: "dream feedback", tool: ToolResolveDreamFeedback, args: map[string]any{"decision": "unknown"}, scope: "write", want: "reason"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := Tool{Name: tc.tool, RequiredScopes: []string{tc.scope}}
			result := ValidateContractInputIssues(tool, tc.args, []string{tc.scope})
			require.Len(t, result.Issues, 1)
			require.Contains(t, result.Issues[0].Message, tc.want)
		})
	}
}

func TestValidateContractInputIssuesRejectsUnsupportedIdempotencyKey(t *testing.T) {
	tool, err := requireTool(toolMap(t), ToolResolveDreamFeedback)
	require.NoError(t, err)

	for _, decision := range []string{"reject", "stale", "reinforce"} {
		t.Run(decision, func(t *testing.T) {
			result := ValidateContractInputIssues(tool, map[string]any{
				"hypothesis_id":   "dream-1",
				"decision":        decision,
				"reason":          "not useful",
				"idempotency_key": "lifecycle-retry",
			}, []string{"write"})

			require.Contains(t, issueMessages(result), "unknown field: idempotency_key")
		})
	}
}

func TestValidateContractInputIssuesRememberValidAndMalformedShapes(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)
	valid := ValidateContractInputIssues(remember, validFlatRelationshipSubmission(), []string{"write"})
	require.Empty(t, valid.Issues)

	wrongEvidenceType := map[string]any{
		"evidence":      "not-an-array",
		"relationships": []any{},
	}
	result := ValidateContractInputIssues(remember, wrongEvidenceType, []string{"write"})
	require.Contains(t, issueMessages(result), "evidence must be an array")

	wrongRelationshipsType := map[string]any{
		"evidence":      []any{map[string]any{"content": "evidence"}},
		"relationships": "not-an-array",
	}
	result = ValidateContractInputIssues(remember, wrongRelationshipsType, []string{"write"})
	require.Contains(t, issueMessages(result), "relationships must be an array")
}

func TestContractIssueCollectorDeduplicatesAndTruncates(t *testing.T) {
	collector := contractIssueCollector{}
	collector.add("", "", "   ")
	for index := 0; index < maxContractValidationIssues; index++ {
		collector.add(strings.Join([]string{"/field", string(rune('a' + index))}, "/"), "invalid", "different")
	}
	require.Len(t, collector.issues, maxContractValidationIssues)
	require.False(t, collector.truncated)
	collector.add("/field/a", "invalid", "different")
	require.False(t, collector.truncated)
	collector.add("/field/new", "invalid", "different")
	require.True(t, collector.truncated)
}

func TestValidateContractInputIssuesEscapesJSONPointerTokens(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)
	args := validFlatRelationshipSubmission()
	args["source/name"] = true
	args["tilde~field"] = true

	result := ValidateContractInputIssues(remember, args, []string{"write"})
	require.Contains(t, result.Issues, ContractValidationIssue{
		Path: "/source~1name", Code: "unknown_field", Message: "unknown field: source/name",
	})
	require.Contains(t, result.Issues, ContractValidationIssue{
		Path: "/tilde~0field", Code: "unknown_field", Message: "unknown field: tilde~field",
	})
}

func TestValidateContractInputIssuesBoundsUnknownFieldOutput(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)
	longKey := strings.Repeat("x", maxContractValidationPathRunes*4)
	result := ValidateContractInputIssues(remember, map[string]any{longKey: true}, []string{"write"})
	var unknownField *ContractValidationIssue
	for index := range result.Issues {
		if result.Issues[index].Code == "unknown_field" {
			unknownField = &result.Issues[index]
			break
		}
	}
	require.NotNil(t, unknownField)
	require.LessOrEqual(t, len([]rune(unknownField.Path)), maxContractValidationPathRunes)
	require.LessOrEqual(t, len([]rune(unknownField.Message)), maxContractValidationMessageRunes)
	require.NotContains(t, unknownField.Message, longKey)
}

func TestContractIssueCollectorPreservesJSONPointerWhitespace(t *testing.T) {
	collector := contractIssueCollector{}
	collector.add("/field ", "invalid", "bad value")
	require.Contains(t, collector.issues, ContractValidationIssue{Path: "/field ", Code: "invalid", Message: "bad value"})
}

func TestContractValidationErrorsDoNotEchoSubmittedIdentifiers(t *testing.T) {
	const secret = "submitted-secret-id"
	rememberInput := validFlatRelationshipSubmission()
	rememberRelationships := rememberInput["relationships"].([]any)
	duplicate := cloneMap(relationship(rememberInput))
	duplicate["ref"] = secret
	rememberRelationships[0].(map[string]any)["ref"] = secret
	rememberInput["relationships"] = append(rememberRelationships[:1], duplicate)

	sourceRevisions := map[string]contractSourceRevision{}
	_ = validateSourceRevisionBatch(0, map[string]any{
		"source_key":      secret,
		"source_revision": "one",
	}, sourceRevisions)

	tests := []struct {
		name string
		err  error
	}{
		{name: "unique array", err: validateUniqueStringArray(map[string]any{"evidence_ids": []any{secret, secret}}, "evidence_ids")},
		{name: "recall feedback", err: validateRecallFeedback(map[string]any{"recalls": []any{
			map[string]any{"recall_event_id": secret},
			map[string]any{"recall_event_id": secret},
		}})},
		{name: "source revision", err: validateSourceRevisionBatch(1, map[string]any{
			"source_key":      secret,
			"source_revision": "two",
		}, sourceRevisions)},
		{name: "submitted relationship", err: validateSubmittedRelationships(rememberInput["relationships"].([]any), rememberInput["evidence"].([]any), "relationships")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Error(t, test.err)
			require.NotContains(t, test.err.Error(), secret)
		})
	}
}

func TestValidateContractInputIssuesBoundsOversizedTraversal(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)
	evidence := make([]any, maxRememberEvidenceItems+1000)
	for index := range evidence {
		evidence[index] = map[string]any{"content": "evidence"}
	}
	relationships := make([]any, maxRememberRelationshipItems+1000)
	for index := range relationships {
		relationships[index] = map[string]any{"ref": "r", "evidence_indices": []any{0}}
	}
	result := ValidateContractInputIssues(remember, map[string]any{"evidence": evidence, "relationships": relationships}, []string{"write"})
	require.Contains(t, issueMessages(result), "evidence exceeds maximum item count of 20")
	require.Contains(t, issueMessages(result), "relationships exceeds maximum item count of 200")
}

func TestValidateContractInputIssuesCoversRememberShapeBranches(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)

	missingRelationships := ValidateContractInputIssues(remember, map[string]any{
		"evidence": []any{map[string]any{}},
	}, []string{"write"})
	require.Contains(t, issueMessages(missingRelationships), "relationships is required")
	require.Contains(t, issueMessages(missingRelationships), "evidence.content is required")

	nonStringContent := ValidateContractInputIssues(remember, map[string]any{
		"evidence":      []any{map[string]any{"content": 42}},
		"relationships": []any{map[string]any{"ref": ""}},
	}, []string{"write"})
	require.Contains(t, issueMessages(nonStringContent), "evidence.content must be a string")
	require.Contains(t, issueMessages(nonStringContent), "relationship.ref must not be blank")

	sourceBatch := ValidateContractInputIssues(remember, map[string]any{
		"evidence": []any{
			map[string]any{"content": "first", "source_key": "source", "source_revision": "one"},
			map[string]any{"content": "second", "source_key": "source", "source_revision": "two", "previous_source_revision": "one", "supersedes_evidence_ids": []any{"old"}},
		},
		"relationships": []any{map[string]any{"ref": "r", "evidence_indices": []any{0, "bad"}}},
	}, []string{"write"})
	require.Contains(t, strings.Join(issueMessages(sourceBatch), "; "), "revision fields must match")
	require.Contains(t, strings.Join(issueMessages(sourceBatch), "; "), "idempotency_key is required")

	wrongRelationship := ValidateContractInputIssues(remember, map[string]any{
		"evidence":      []any{map[string]any{"content": "evidence"}},
		"relationships": []any{"not-an-object"},
	}, []string{"write"})
	require.Contains(t, issueMessages(wrongRelationship), "relationship must be an object")

	tooManyEvidence := make([]any, 21)
	for index := range tooManyEvidence {
		tooManyEvidence[index] = map[string]any{"content": "evidence"}
	}
	result := ValidateContractInputIssues(remember, map[string]any{
		"evidence": tooManyEvidence, "relationships": []any{map[string]any{"ref": "r", "evidence_indices": []any{0}}},
	}, []string{"write"})
	require.Contains(t, strings.Join(issueMessages(result), "; "), "evidence exceeds maximum item count")

	unknownTool := Tool{Name: "custom", InputSchema: map[string]any{"type": "object", "required": []any{"required"}}}
	unknownResult := ValidateContractInputIssues(unknownTool, map[string]any{}, nil)
	require.Len(t, unknownResult.Issues, 1)
	require.Equal(t, "required", unknownResult.Issues[0].Code)

	for _, message := range []string{"unknown field", "required", "duplicate", "outside", "coverage", "rfc3339", "other"} {
		classifyContractIssue(message)
	}
}

func TestValidateContractInputIssuesUsesExactNestedJSONPointers(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)

	tests := []struct {
		name   string
		mutate func(map[string]any)
		path   string
	}{
		{name: "known entity", path: "/relationships/0/subject/known_entity_id", mutate: func(input map[string]any) {
			relationship(input)["subject"].(map[string]any)["known_entity_id"] = "not-a-uuid"
		}},
		{name: "typed value", path: "/relationships/0/object/value/value", mutate: func(input map[string]any) {
			relationship(input)["object"] = map[string]any{"value": map[string]any{"type": "number", "value": "not-a-number"}}
		}},
		{name: "correction version", path: "/relationships/0/correction_target/expected_version", mutate: func(input map[string]any) {
			relationship(input)["correction_target"] = map[string]any{"relationship_id": "relationship", "expected_version": 0}
		}},
		{name: "source revision", path: "/evidence/0/previous_source_revision", mutate: func(input map[string]any) {
			input["evidence"].([]any)[0].(map[string]any)["previous_source_revision"] = "rev-1"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validFlatRelationshipSubmission()
			test.mutate(input)
			result := ValidateContractInputIssues(remember, input, []string{"write"})
			paths := make([]string, 0, len(result.Issues))
			for _, issue := range result.Issues {
				paths = append(paths, issue.Path)
			}
			require.Contains(t, paths, test.path)
		})
	}
}

func issueMessages(result ContractValidationResult) []string {
	messages := make([]string, 0, len(result.Issues))
	for _, issue := range result.Issues {
		messages = append(messages, issue.Message)
	}
	return messages
}
