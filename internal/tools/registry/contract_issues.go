package registry

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/markhuangai/dense-mem/internal/correlation"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

// ContractValidationIssue is the bounded, machine-readable form of one
// contract problem. Paths are JSON Pointers and never contain submitted
// values.
type ContractValidationIssue struct {
	Path    string `json:"path"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ContractValidationResult contains every deterministic issue collected up to
// the public limit. The truncation bit lets clients decide whether to retry
// after fixing the visible issues.
type ContractValidationResult struct {
	Issues          []ContractValidationIssue `json:"issues"`
	IssuesTruncated bool                      `json:"issues_truncated"`
}

type ContractValidationFailure struct {
	Result ContractValidationResult
}

func (e *ContractValidationFailure) Error() string {
	if e == nil || len(e.Result.Issues) == 0 {
		return "contract validation failed"
	}
	return e.Result.Issues[0].Message
}

func wrapRememberValidationError(err error) error {
	var validation *rememberapp.RememberValidationError
	if !errors.As(err, &validation) {
		return err
	}
	result := ContractValidationResult{IssuesTruncated: validation.IssuesTruncated}
	for _, issue := range validation.Issues {
		result.Issues = append(result.Issues, ContractValidationIssue{
			Path: issue.Path, Code: issue.Code, Message: issue.Message,
		})
	}
	return &ContractValidationFailure{Result: result}
}

func ContractValidationResultFromError(err error) (ContractValidationResult, bool) {
	var validation *ContractValidationFailure
	if !errors.As(err, &validation) || validation == nil || len(validation.Result.Issues) == 0 {
		return ContractValidationResult{}, false
	}
	return validation.Result, true
}

const (
	maxContractValidationIssues       = 20
	maxContractValidationPathRunes    = 128
	maxContractValidationMessageRunes = 512
	maxRememberEvidenceItems          = 20
	maxRememberRelationshipItems      = 200
)

// ValidateContractInputIssues validates a contract without stopping at the
// first deterministic error. Remember gets field-level aggregation; other
// tools retain their existing validation path and return one bounded issue.
func ValidateContractInputIssues(tool Tool, args map[string]any, scopes []string) ContractValidationResult {
	collector := contractIssueCollector{}
	if !ToolScopesSatisfied(tool, scopes) {
		collector.add("", "missing_scope", fmt.Sprintf("%s: missing required scope", tool.Name))
	}
	if HasTenantOverrideArgs(args) {
		collector.add("", "tenant_override", fmt.Sprintf("%s: team_id and profile_id are not accepted", tool.Name))
	}
	if len(collector.issues) == 0 {
		if tool.Name == ToolRemember {
			collectRememberContractIssues(tool, args, &collector)
		} else if err := ValidateInput(tool, args); err != nil {
			collector.add("", classifyContractIssue(err.Error()), err.Error())
		} else {
			switch tool.Name {
			case ToolRecallMemory:
				if err := validateRecall(args); err != nil {
					collector.add("", classifyContractIssue(err.Error()), err.Error())
				}
			case ToolRetractEvidence, ToolTraceMemory, ToolExportMemoryPack:
				field := "evidence_ids"
				if tool.Name == ToolTraceMemory {
					field = "predicate_keys"
				} else if tool.Name == ToolExportMemoryPack {
					field = "relationship_ids"
				}
				if err := validateUniqueStringArray(args, field); err != nil {
					collector.add("/"+jsonPointerToken(field), classifyContractIssue(err.Error()), err.Error())
				}
			case ToolCorrectRelationship:
				if err := validateCorrectRelationship(args); err != nil {
					collector.add("", classifyContractIssue(err.Error()), err.Error())
				}
			case ToolSubmitRecallSessionFeedback:
				if err := validateRecallFeedback(args); err != nil {
					collector.add("", classifyContractIssue(err.Error()), err.Error())
				}
			case ToolResolveDreamFeedback:
				if err := validateDreamFeedback(args); err != nil {
					collector.add("", classifyContractIssue(err.Error()), err.Error())
				}
			}
		}
	}
	sort.SliceStable(collector.issues, func(i, j int) bool {
		if collector.issues[i].Path != collector.issues[j].Path {
			return collector.issues[i].Path < collector.issues[j].Path
		}
		if collector.issues[i].Code != collector.issues[j].Code {
			return collector.issues[i].Code < collector.issues[j].Code
		}
		return collector.issues[i].Message < collector.issues[j].Message
	})
	return ContractValidationResult{Issues: collector.issues, IssuesTruncated: collector.truncated}
}

// ContractValidationErrorData returns the stable JSON-RPC error.data payload.
func ContractValidationErrorData(ctx context.Context, result ContractValidationResult) map[string]any {
	issues := make([]map[string]any, 0, len(result.Issues))
	for _, issue := range result.Issues {
		issues = append(issues, map[string]any{
			"path":    issue.Path,
			"code":    issue.Code,
			"message": issue.Message,
		})
	}
	return map[string]any{
		"code":             "invalid_input",
		"reason_code":      "validation_failed",
		"message":          "The tool arguments failed validation; correct the listed fields and submit again.",
		"retryable":        false,
		"next_action":      "correct_and_resubmit",
		"remediation":      "Correct the listed argument paths and submit the request again.",
		"correlation_id":   rememberapp.NormalizeTerminalCorrelationID(correlation.FromContext(ctx)),
		"reason":           "validation_failed",
		"issues":           issues,
		"issues_truncated": result.IssuesTruncated,
	}
}

type contractIssueCollector struct {
	issues    []ContractValidationIssue
	truncated bool
}

func (c *contractIssueCollector) add(path, code, message string) {
	path = boundedContractText(path, maxContractValidationPathRunes)
	message = strings.TrimSpace(message)
	message = boundedContractText(message, maxContractValidationMessageRunes)
	if message == "" {
		return
	}
	for _, issue := range c.issues {
		if issue.Path == path && issue.Code == code && issue.Message == message {
			return
		}
	}
	if len(c.issues) >= maxContractValidationIssues {
		c.truncated = true
		return
	}
	c.issues = append(c.issues, ContractValidationIssue{Path: path, Code: code, Message: message})
}

func boundedContractText(value string, maxRunes int) string {
	if len([]rune(value)) <= maxRunes {
		return value
	}
	return string([]rune(value)[:maxRunes])
}

func jsonPointerToken(value string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(value)
}

func collectRememberContractIssues(tool Tool, args map[string]any, collector *contractIssueCollector) {
	properties := schemaProperties(tool.InputSchema)
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, ok := properties[key]; !ok {
			collector.add("/"+jsonPointerToken(key), "unknown_field", "unknown field: "+key)
		}
	}
	if _, ok := args["idempotency_key"]; !ok {
		collector.add("/idempotency_key", "required", "idempotency_key is required")
	}

	evidence := collectAnyArray(args["evidence"])
	if _, exists := args["evidence"]; exists && evidence == nil {
		collector.add("/evidence", "type", "evidence must be an array")
	}
	if len(evidence) > maxRememberEvidenceItems {
		collector.add("/evidence", "too_many_items", fmt.Sprintf("evidence exceeds maximum item count of %d", maxRememberEvidenceItems))
		evidence = evidence[:maxRememberEvidenceItems]
	}
	sourceRevisions := map[string]contractSourceRevision{}
	for index, item := range evidence {
		path := fmt.Sprintf("/evidence/%d", index)
		fields, ok := objectFields(item)
		if !ok {
			collector.add(path, "type", "evidence item must be an object")
			continue
		}
		if _, ok := fields["content"]; !ok {
			collector.add(path+"/content", "required", "evidence.content is required")
		}
		if value, ok := fields["content"]; ok {
			content, stringOK := value.(string)
			if !stringOK {
				collector.add(path+"/content", "type", "evidence.content must be a string")
			} else if strings.TrimSpace(content) == "" {
				collector.add(path+"/content", "required", "evidence.content must not be blank")
			}
		}
		itemProperties := schemaProperties(evidenceArraySchema()["items"].(map[string]any))
		itemKeys := make([]string, 0, len(fields))
		for key := range fields {
			itemKeys = append(itemKeys, key)
		}
		sort.Strings(itemKeys)
		for _, key := range itemKeys {
			if _, ok := itemProperties[key]; !ok {
				collector.add(path+"/"+jsonPointerToken(key), "unknown_field", "unknown field: "+key)
			}
		}
		if err := validateSourceRevisionFields(index, fields); err != nil {
			collector.add(contractIssuePath(err, path), classifyContractIssue(err.Error()), err.Error())
		}
		if err := validateSourceRevisionBatch(index, fields, sourceRevisions); err != nil {
			collector.add(contractIssuePath(err, path), classifyContractIssue(err.Error()), err.Error())
		}
	}
	if err := validateDirectEvidenceSupersessions(evidence); err != nil {
		collector.add(contractIssuePath(err, "/evidence"), classifyContractIssue(err.Error()), err.Error())
	}

	relationships := collectAnyArray(args["relationships"])
	if _, exists := args["relationships"]; exists && relationships == nil {
		collector.add("/relationships", "type", "relationships must be an array")
		return
	}
	if len(relationships) > maxRememberRelationshipItems {
		collector.add("/relationships", "too_many_items", fmt.Sprintf("relationships exceeds maximum item count of %d", maxRememberRelationshipItems))
		relationships = relationships[:maxRememberRelationshipItems]
	}
	seenRefs := map[string]struct{}{}
	for index, item := range relationships {
		path := fmt.Sprintf("/relationships/%d", index)
		relationship, ok := objectFields(item)
		if !ok {
			collector.add(path, "type", "relationship must be an object")
			continue
		}
		relationshipProperties := schemaProperties(relationshipSubmissionSchema())
		keys := make([]string, 0, len(relationship))
		for key := range relationship {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if _, ok := relationshipProperties[key]; !ok {
				collector.add(path+"/"+jsonPointerToken(key), "unknown_field", "unknown field: "+key)
			}
		}
		ref := strings.TrimSpace(stringField(relationship, "ref"))
		if ref == "" {
			collector.add(path+"/ref", "required", "relationship.ref must not be blank")
		} else if _, exists := seenRefs[ref]; exists {
			collector.add(path+"/ref", "duplicate", "relationship.ref must be unique")
		} else {
			seenRefs[ref] = struct{}{}
		}
		for _, required := range []string{"subject", "predicate", "object", "polarity"} {
			if _, exists := relationship[required]; !exists {
				collector.add(path+"/"+required, "required", "relationship."+required+" is required")
			}
		}
		if err := validateSubmittedRelationship(relationship, evidence, path); err != nil {
			collector.add(contractIssuePath(err, path), classifyContractIssue(err.Error()), err.Error())
		}
		if subject, exists := relationship["subject"]; exists {
			if err := validateRememberEntityHintValue(subject, path+"/subject"); err != nil {
				collector.add(contractIssuePath(err, path+"/subject"), classifyContractIssue(err.Error()), err.Error())
			}
		}
		if predicate, exists := relationship["predicate"]; exists {
			if err := validateRememberPredicateHintValue(predicate, path+"/predicate"); err != nil {
				collector.add(contractIssuePath(err, path+"/predicate"), classifyContractIssue(err.Error()), err.Error())
			}
		}
		if object, ok := objectFields(relationship["object"]); ok {
			if entity, exists := object["entity"]; exists {
				if err := validateRememberEntityHintValue(entity, path+"/object/entity"); err != nil {
					collector.add(contractIssuePath(err, path+"/object/entity"), classifyContractIssue(err.Error()), err.Error())
				}
			}
		}
		indices := collectAnyArray(relationship["evidence_indices"])
		for position, rawIndex := range indices {
			if _, ok := schemaNumber(rawIndex); !ok {
				collector.add(fmt.Sprintf("%s/evidence_indices/%d", path, position), "type", "evidence index must be an integer")
			}
		}
	}
	if err := ValidateInput(tool, args); err != nil {
		collector.add("", classifyContractIssue(err.Error()), err.Error())
	}
}

func contractIssuePath(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	message := strings.TrimSpace(err.Error())
	end := len(message)
	for index, char := range message {
		if char == ':' || char == ' ' || char == '\t' || char == '\n' {
			end = index
			break
		}
	}
	path := message[:end]
	if strings.HasPrefix(path, "/") {
		dot := strings.IndexByte(path, '.')
		if dot < 0 {
			return path
		}
		suffix, ok := contractDotPathPointer(path[dot+1:])
		if !ok {
			return fallback
		}
		return path[:dot] + suffix
	}
	if !strings.HasPrefix(path, "evidence[") && !strings.HasPrefix(path, "relationships[") {
		return fallback
	}
	pointer, ok := contractDotPathPointer(path)
	if !ok {
		return fallback
	}
	return pointer
}

func contractDotPathPointer(path string) (string, bool) {
	parts := make([]string, 0, 8)
	for index := 0; index < len(path); {
		start := index
		for index < len(path) && path[index] != '.' && path[index] != '[' {
			index++
		}
		if index > start {
			parts = append(parts, path[start:index])
		}
		if index == len(path) {
			break
		}
		if path[index] == '.' {
			index++
			continue
		}
		index++
		start = index
		for index < len(path) && path[index] != ']' {
			index++
		}
		if index == len(path) || index == start {
			return "", false
		}
		parts = append(parts, path[start:index])
		index++
	}
	if len(parts) == 0 {
		return "", false
	}
	var pointer strings.Builder
	for _, part := range parts {
		if part == "" {
			return "", false
		}
		pointer.WriteByte('/')
		pointer.WriteString(jsonPointerToken(part))
	}
	return pointer.String(), true
}

func collectAnyArray(value any) []any {
	if value == nil {
		return nil
	}
	items, ok := value.([]any)
	if ok {
		return items
	}
	return nil
}

func classifyContractIssue(message string) string {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "unknown field"):
		return "unknown_field"
	case strings.Contains(lower, "required"):
		return "required"
	case strings.Contains(lower, "duplicate"):
		return "duplicate"
	case strings.Contains(lower, "outside"):
		return "out_of_range"
	case strings.Contains(lower, "coverage") || strings.Contains(lower, "missing evidence"):
		return "coverage"
	case strings.Contains(lower, "uuid") || strings.Contains(lower, "rfc3339"):
		return "format"
	default:
		return "invalid"
	}
}
