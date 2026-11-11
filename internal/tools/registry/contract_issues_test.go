package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/correlation"
)

func TestValidateContractInputIssuesAggregatesRememberProblems(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)

	evidence := make([]any, 21)
	for index := range evidence {
		evidence[index] = map[string]any{
			"content": "", "source_key": "source-1", "unknown": true,
			"supersedes_evidence_ids": []any{"evidence-old"},
		}
	}
	args := map[string]any{
		"unknown":  true,
		"evidence": evidence,
	}

	result := ValidateContractInputIssues(remember, args, []string{"read", "write"})
	require.NotEmpty(t, result.Issues)
	require.True(t, result.IssuesTruncated)
	for index := 1; index < len(result.Issues); index++ {
		previous := result.Issues[index-1]
		current := result.Issues[index]
		require.LessOrEqual(t, previous.Path, current.Path)
	}
	require.Contains(t, result.Issues, ContractValidationIssue{Path: "/unknown", Code: "unknown_field", Message: "unknown field: unknown"})

	data := ContractValidationErrorData(correlation.WithID(context.Background(), "validation-correlation"), result)
	require.Equal(t, "validation_failed", data["reason"])
	require.Equal(t, "validation-correlation", data["correlation_id"])
	require.Equal(t, result.IssuesTruncated, data["issues_truncated"])
	require.Len(t, data["issues"], len(result.Issues))
}

func TestValidateContractInputIssuesHonorsEarlyScopeAndTenantGuards(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)

	missingScope := ValidateContractInputIssues(remember, validEvidenceOnlySubmission(), nil)
	require.Len(t, missingScope.Issues, 1)
	require.Equal(t, "missing_scope", missingScope.Issues[0].Code)

	withTenant := validEvidenceOnlySubmission()
	withTenant["team_id"] = "caller-selected-team"
	tenant := ValidateContractInputIssues(remember, withTenant, []string{"read", "write"})
	require.Len(t, tenant.Issues, 1)
	require.Equal(t, "tenant_override", tenant.Issues[0].Code)
}

func TestValidateContractInputIssuesDispatchesToolSpecificValidation(t *testing.T) {
	const duplicateID = "11111111-1111-4111-8111-111111111111"
	cases := []struct {
		name  string
		tool  string
		args  map[string]any
		scope string
		want  string
	}{
		{name: "recall", tool: ToolRecallMemory, args: map[string]any{"query": "durable memory", "known_evidence_ids": []any{duplicateID, duplicateID}}, scope: "read", want: "duplicate"},
		{name: "retract", tool: ToolRetractEvidence, args: map[string]any{"evidence_ids": []any{duplicateID, duplicateID}}, scope: "write", want: "duplicate"},
		{name: "export", tool: ToolExportMemoryPack, args: map[string]any{"evidence_ids": []any{duplicateID, duplicateID}}, scope: "read", want: "duplicate"},
		{name: "recall feedback", tool: ToolSubmitRecallSessionFeedback, args: map[string]any{"recalls": []any{map[string]any{"quality": "low"}}}, scope: "write", want: "feedback_comment"},
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

func TestValidateContractInputIssuesRejectsMalformedRecallUUIDFilters(t *testing.T) {
	recall, err := requireTool(toolMap(t), ToolRecallMemory)
	require.NoError(t, err)

	for _, field := range []string{"known_evidence_ids", "topic_ids"} {
		t.Run(field, func(t *testing.T) {
			result := ValidateContractInputIssues(recall, map[string]any{
				"query": "durable memory",
				field:   []any{"not-a-uuid"},
			}, []string{"read"})
			require.Len(t, result.Issues, 1)
			require.Equal(t, field+"[0] must be a valid UUID", result.Issues[0].Message)
		})
	}
}

func TestValidateContractInputIssuesRememberValidAndMalformedShapes(t *testing.T) {
	remember, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)
	valid := ValidateContractInputIssues(remember, validEvidenceOnlySubmission(), []string{"read", "write"})
	require.Empty(t, valid.Issues)

	wrongEvidenceType := map[string]any{
		"evidence":      "not-an-array",
		"relationships": []any{},
	}
	result := ValidateContractInputIssues(remember, wrongEvidenceType, []string{"read", "write"})
	require.Contains(t, issueMessages(result), "evidence must be an array")

	retired := validEvidenceOnlySubmission()
	retired["relationships"] = []any{}
	result = ValidateContractInputIssues(remember, retired, []string{"read", "write"})
	require.Contains(t, result.Issues, ContractValidationIssue{Path: "/relationships", Code: "unknown_field", Message: "unknown field: relationships"})
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
	args := validEvidenceOnlySubmission()
	args["source/name"] = true
	args["tilde~field"] = true

	result := ValidateContractInputIssues(remember, args, []string{"read", "write"})
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
	result := ValidateContractInputIssues(remember, map[string]any{longKey: true}, []string{"read", "write"})
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
	result := ValidateContractInputIssues(remember, map[string]any{"evidence": evidence}, []string{"read", "write"})
	require.Contains(t, issueMessages(result), "evidence exceeds maximum item count of 20")
}

func TestEvidenceOnlyCatalogValidationRejectsMalformedEvidence(t *testing.T) {
	tool, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)
	for _, evidence := range []any{nil, []any{}, []any{map[string]any{}}, []any{map[string]any{"content": 42}}, []any{"not-an-object"}} {
		result := ValidateContractInputIssues(tool, map[string]any{"target_space": "team_shared", "idempotency_key": "key", "evidence": evidence}, []string{"read", "write"})
		require.NotEmpty(t, result.Issues)
	}
}

func TestEvidenceOnlyCatalogValidationUsesExactEvidencePointers(t *testing.T) {
	tool, err := requireTool(toolMap(t), ToolRemember)
	require.NoError(t, err)
	for _, tc := range []struct {
		field string
		value any
		path  string
	}{
		{"previous_source_revision", "rev-1", "/evidence/0/previous_source_revision"},
		{"content", 42, "/evidence/0/content"},
		{"supersedes_evidence_ids", []any{"not-a-uuid"}, "/evidence/0/supersedes_evidence_ids/0"},
	} {
		input := validEvidenceOnlySubmission()
		input["evidence"].([]any)[0].(map[string]any)[tc.field] = tc.value
		result := ValidateContractInputIssues(tool, input, []string{"read", "write"})
		paths := []string{}
		for _, issue := range result.Issues {
			paths = append(paths, issue.Path)
		}
		require.Contains(t, paths, tc.path)
	}
}

func issueMessages(result ContractValidationResult) []string {
	messages := make([]string, 0, len(result.Issues))
	for _, issue := range result.Issues {
		messages = append(messages, issue.Message)
	}
	return messages
}
