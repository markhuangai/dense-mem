package registry

import (
	"fmt"
	"sort"
	"strings"
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
func ContractValidationErrorData(result ContractValidationResult) map[string]any {
	issues := make([]map[string]any, 0, len(result.Issues))
	for _, issue := range result.Issues {
		issues = append(issues, map[string]any{
			"path":    issue.Path,
			"code":    issue.Code,
			"message": issue.Message,
		})
	}
	return map[string]any{
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
	if _, ok := args["relationships"]; !ok {
		collector.add("/relationships", "required", "relationships is required")
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
			collector.add(path, classifyContractIssue(err.Error()), err.Error())
		}
		if err := validateSourceRevisionBatch(index, fields, sourceRevisions); err != nil {
			collector.add(path, classifyContractIssue(err.Error()), err.Error())
		}
	}
	if err := validateDirectEvidenceSupersessions(evidence); err != nil {
		collector.add("/evidence", classifyContractIssue(err.Error()), err.Error())
	}

	relationships := collectAnyArray(args["relationships"])
	if _, exists := args["relationships"]; exists && relationships == nil {
		collector.add("/relationships", "type", "relationships must be an array")
		return
	}
	if len(relationships) == 0 && args["relationships"] != nil {
		collector.add("/relationships", "required", "relationships must contain at least one item")
	}
	if len(relationships) > maxRememberRelationshipItems {
		collector.add("/relationships", "too_many_items", fmt.Sprintf("relationships exceeds maximum item count of %d", maxRememberRelationshipItems))
		relationships = relationships[:maxRememberRelationshipItems]
	}
	seenRefs := map[string]struct{}{}
	covered := make([]bool, len(evidence))
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
		for _, required := range []string{"subject", "predicate", "object", "polarity", "modality"} {
			if _, exists := relationship[required]; !exists {
				collector.add(path+"/"+required, "required", "relationship."+required+" is required")
			}
		}
		if err := validateSubmittedRelationship(relationship, evidence, path); err != nil {
			collector.add(path, classifyContractIssue(err.Error()), err.Error())
		}
		indices := collectAnyArray(relationship["evidence_indices"])
		for position, rawIndex := range indices {
			number, ok := schemaNumber(rawIndex)
			if ok && number == float64(int(number)) && int(number) >= 0 && int(number) < len(covered) {
				covered[int(number)] = true
			} else if !ok {
				collector.add(fmt.Sprintf("%s/evidence_indices/%d", path, position), "type", "evidence index must be an integer")
			}
		}
	}
	missing := make([]string, 0)
	for index, present := range covered {
		if !present {
			missing = append(missing, fmt.Sprintf("%d", index))
		}
	}
	if len(missing) > 0 {
		collector.add("/relationships", "coverage", "relationships must cover every submitted evidence item; missing evidence indexes: ["+strings.Join(missing, ", ")+"]")
	}
	if err := ValidateInput(tool, args); err != nil {
		collector.add("", classifyContractIssue(err.Error()), err.Error())
	}
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
