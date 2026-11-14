package evalharness

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/uuid"
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
		"params":  map[string]any{"_meta": map[string]any{"io.modelcontextprotocol/protocolVersion": "2026-07-28", "io.modelcontextprotocol/clientCapabilities": map[string]any{}}},
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
	if submissionProcessingState(out) != "completed" {
		return mapping, fmt.Errorf("import %s: Remember has no completed terminal receipt", item.SourceDocID)
	}
	entries, ok := out["evidence"].([]any)
	if !ok || len(entries) == 0 {
		return mapping, errors.New("remember receipt has no Evidence partitions")
	}
	pending := newKnowledgeMapping()
	parts := []ImportedPartition{}
	start := 0
	sourceItem := ""
	occurrences := map[string]bool{}
	runes := []rune(item.Content)
	for index, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok || stringValue(entry["disposition"]) != "stored" {
			return mapping, errors.New("invalid stored Evidence partition")
		}
		for _, field := range []string{"evidence_index", "partition_index", "span_start", "span_end"} {
			if !integerValue(entry[field]) {
				return mapping, fmt.Errorf("partition %s must be an integer", field)
			}
		}
		part := ImportedPartition{EvidenceID: stringValue(entry["evidence_id"]), OccurrenceID: stringValue(entry["occurrence_id"]), SourceItemID: stringValue(entry["source_item_id"]), PartitionIndex: intValue(entry["partition_index"]), SpanStart: intValue(entry["span_start"]), SpanEnd: intValue(entry["span_end"]), ContentHash: stringValue(entry["content_hash"])}
		for _, id := range []string{part.EvidenceID, part.OccurrenceID, part.SourceItemID} {
			parsed, err := uuid.Parse(id)
			if err != nil || parsed == uuid.Nil {
				return mapping, errors.New("invalid partition identity")
			}
		}
		if intValue(entry["evidence_index"]) != 0 || part.PartitionIndex != index || part.SpanStart != start || part.SpanEnd <= start || part.SpanEnd > len(runes) || occurrences[part.OccurrenceID] {
			return mapping, errors.New("incomplete or invalid partition mapping")
		}
		if sourceItem != "" && sourceItem != part.SourceItemID {
			return mapping, errors.New("inconsistent submitted item identity")
		}
		sourceItem = part.SourceItemID
		if part.ContentHash != knowledge.EvidenceContentHash(string(runes[start:part.SpanEnd])) {
			return mapping, errors.New("partition content hash differs from exact source span")
		}
		occurrences[part.OccurrenceID] = true
		parts = append(parts, part)
		start = part.SpanEnd
		addSourceMapping(&pending, Ref{Type: "fragment", ID: part.EvidenceID, SourceDocID: item.SourceDocID}, index == 0)
	}
	if start != len(runes) {
		return mapping, errors.New("incomplete partition coverage")
	}
	pending.Partitions = map[string][]ImportedPartition{item.SourceDocID: parts}
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
