package evalharness

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

type contractMode string

const (
	contractModeEvidenceOnly contractMode = "v2.8.0"
)

type mcpToolsListResponse struct {
	Result struct {
		Tools []mcpToolDefinition `json:"tools"`
	} `json:"result"`
	Error *mcpToolError `json:"error,omitempty"`
}

type mcpToolDefinition struct {
	Name         string         `json:"name"`
	OutputSchema map[string]any `json:"outputSchema"`
}

func (c *HTTPClient) ensureContract(ctx context.Context) (contractMode, error) {
	c.contractMu.Lock()
	defer c.contractMu.Unlock()
	if c.contractMode != "" {
		return c.contractMode, nil
	}
	mode, err := c.discoverContract(ctx)
	if err != nil {
		return "", err
	}
	c.contractMode = mode
	return mode, nil
}

func (c *HTTPClient) discoverContract(ctx context.Context) (contractMode, error) {
	if strings.TrimSpace(c.BaseURL) == "" {
		return "", fmt.Errorf("base URL is required")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		return "", fmt.Errorf("API key is required")
	}
	body := map[string]any{
		"jsonrpc": "2.0",
		"id":      time.Now().UnixNano(),
		"method":  "tools/list",
		"params":  map[string]any{},
	}
	var response mcpToolsListResponse
	if err := c.doJSON(ctx, http.MethodPost, endpoint(c.BaseURL, "/mcp"), c.APIKey, body, &response); err != nil {
		return "", fmt.Errorf("discover MCP contract: %w", err)
	}
	if response.Error != nil {
		return "", fmt.Errorf("discover MCP contract returned error code %d", response.Error.Code)
	}
	return classifyContract(response.Result.Tools)
}

func classifyContract(tools []mcpToolDefinition) (contractMode, error) {
	names := make(map[string]struct{}, len(tools))
	var rememberSchema map[string]any
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			return "", errors.New("MCP tools/list returned a tool without a name")
		}
		if _, exists := names[name]; exists {
			return "", fmt.Errorf("MCP tools/list returned duplicate tool %q", name)
		}
		names[name] = struct{}{}
		if name == registry.ToolRemember {
			rememberSchema = tool.OutputSchema
		}
	}
	if rememberSchema == nil {
		return "", errors.New("MCP tools/list did not describe remember")
	}
	version := contractVersionFromSchema(rememberSchema)
	if domain.ContractVersionCompatible(version) && matchesContractToolSet(names, registry.ContractToolNames()) {
		return contractModeEvidenceOnly, nil
	}
	return "", fmt.Errorf("unsupported or mixed MCP contract: remember=%q", version)
}

func matchesContractToolSet(names map[string]struct{}, expected []string) bool {
	expectedSet := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		expectedSet[name] = struct{}{}
	}
	for name := range names {
		if registry.IsEvaluationTool(name) {
			continue
		}
		if _, ok := expectedSet[name]; !ok {
			return false
		}
	}
	for _, name := range expected {
		if _, ok := names[name]; !ok {
			if !registry.ContractToolRuntimeOptional(name) {
				return false
			}
		}
	}
	return true
}

func contractVersionFromSchema(schema map[string]any) string {
	if schema == nil {
		return ""
	}
	if properties, ok := schema["properties"].(map[string]any); ok {
		if property, ok := properties["contract_version"].(map[string]any); ok {
			if values, ok := property["enum"].([]any); ok {
				return preferredContractVersion(values)
			}
			if values, ok := property["enum"].([]string); ok {
				converted := make([]any, 0, len(values))
				for _, value := range values {
					converted = append(converted, value)
				}
				return preferredContractVersion(converted)
			}
		}
	}
	if variants, ok := schema["oneOf"].([]any); ok {
		version := ""
		for _, raw := range variants {
			variant, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			candidate := contractVersionFromSchema(variant)
			if candidate == "" {
				continue
			}
			if version != "" && version != candidate {
				return ""
			}
			version = candidate
		}
		return version
	}
	return ""
}

func preferredContractVersion(values []any) string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		version := strings.TrimSpace(stringValue(value))
		if version == "" {
			continue
		}
		seen[version] = struct{}{}
	}
	for version := range seen {
		if !domain.ContractVersionCompatible(version) {
			return ""
		}
	}
	if _, ok := seen[domain.ContractVersion]; ok {
		return domain.ContractVersion
	}
	if _, ok := seen[domain.PreviousContractVersion]; ok {
		return domain.PreviousContractVersion
	}
	if len(seen) == 1 {
		for version := range seen {
			return version
		}
	}
	return ""
}

func (c *HTTPClient) importTerminalRememberResult(item CorpusItem, out map[string]any, mapping KnowledgeMapping) (KnowledgeMapping, error) {
	processing := submissionProcessingState(out)
	if processing == "" {
		return mapping, fmt.Errorf("import %s: remember response missing terminal processing_state", item.SourceDocID)
	}
	if processing != "completed" {
		if cause := submissionErrorMessage(out); cause != "" {
			return mapping, fmt.Errorf("import %s: remember processing_state %s: %s", item.SourceDocID, processing, cause)
		}
		return mapping, fmt.Errorf("import %s: remember processing_state %s", item.SourceDocID, processing)
	}
	entries, _ := out["evidence"].([]any)
	if len(entries) == 0 {
		return mapping, fmt.Errorf("import %s: remember response missing evidence id", item.SourceDocID)
	}
	var partitions []ImportedPartition
	pending := newKnowledgeMapping()
	start := 0
	occurrences := map[string]bool{}
	runes := []rune(item.Content)
	for i, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			return mapping, fmt.Errorf("import %s: invalid Evidence entry", item.SourceDocID)
		}
		id := firstNonEmpty(stringValue(entry["evidence_id"]), stringValue(entry["id"]), stringValue(entry["fragment_id"]))
		if id == "" || (stringValue(entry["disposition"]) != "" && stringValue(entry["disposition"]) != "stored") {
			return mapping, fmt.Errorf("import %s: remember response missing stored evidence id", item.SourceDocID)
		}
		if _, partitioned := entry["partition_index"]; partitioned {
			p := ImportedPartition{EvidenceID: id, OccurrenceID: stringValue(entry["occurrence_id"]), SourceItemID: stringValue(entry["source_item_id"]), PartitionIndex: intValue(entry["partition_index"]), SpanStart: intValue(entry["span_start"]), SpanEnd: intValue(entry["span_end"]), ContentHash: stringValue(entry["content_hash"])}
			if intValue(entry["evidence_index"]) != 0 || p.PartitionIndex != i || p.SpanStart != start || p.SpanEnd <= start || p.SpanEnd > len(runes) || p.SourceItemID == "" || p.OccurrenceID == "" || p.ContentHash != knowledge.EvidenceContentHash(string(runes[start:p.SpanEnd])) {
				return mapping, fmt.Errorf("import %s: incomplete or invalid partition mapping", item.SourceDocID)
			}
			if len(partitions) > 0 && p.SourceItemID != partitions[0].SourceItemID || occurrences[p.OccurrenceID] {
				return mapping, fmt.Errorf("import %s: inconsistent partition identity", item.SourceDocID)
			}
			for _, field := range []string{"evidence_index", "partition_index", "span_start", "span_end"} {
				if _, present := entry[field]; !present {
					return mapping, fmt.Errorf("import %s: missing partition %s", item.SourceDocID, field)
				}
			}
			occurrences[p.OccurrenceID] = true
			partitions = append(partitions, p)
			start = p.SpanEnd
		} else if len(partitions) > 0 {
			return mapping, fmt.Errorf("import %s: mixed partition mapping", item.SourceDocID)
		}
		addSourceMapping(&pending, Ref{Type: "fragment", ID: id, SourceDocID: item.SourceDocID}, i == 0)
	}
	if len(partitions) > 0 {
		if len(partitions) != len(entries) || start != len(runes) {
			return mapping, fmt.Errorf("import %s: incomplete partition coverage", item.SourceDocID)
		}
		if mapping.Partitions == nil {
			mapping.Partitions = map[string][]ImportedPartition{}
		}
		mapping.Partitions[item.SourceDocID] = partitions
	}
	mergeKnowledgeMapping(&mapping, pending)
	return mapping, nil
}

func isTransientCallToolError(err error) bool {
	if isTransientHTTPError(err) {
		return true
	}
	var structuredErr *StructuredToolError
	if !errors.As(err, &structuredErr) || structuredErr == nil {
		return false
	}
	return structuredToolErrorRetryable(structuredErr.Result)
}

func structuredToolErrorRetryable(result map[string]any) bool {
	if retryable, _ := result["retryable"].(bool); retryable && stringValue(result["next_action"]) == "retry_same_request" {
		return true
	}
	errorsValue, _ := result["errors"].([]any)
	for _, raw := range errorsValue {
		item, _ := raw.(map[string]any)
		retryable, _ := item["retryable"].(bool)
		if retryable && stringValue(item["next_action"]) == "retry_same_request" {
			return true
		}
	}
	return false
}
