package evalharness

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

type contractMode string

const (
	contractModeLegacy contractMode = "legacy"
	contractModeV261   contractMode = "v2.6.1"
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
	c.contractOnce.Do(func() {
		c.contractMode, c.contractErr = c.discoverContract(ctx)
	})
	return c.contractMode, c.contractErr
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
		return "", fmt.Errorf("discover MCP contract returned %d: %s", response.Error.Code, response.Error.Message)
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
	_, statusPresent := names[registry.ToolGetSubmissionStatus]
	if statusPresent && version == domain.ContractVersion && containsToolNames(names, registry.ContractToolNames()) {
		return contractModeLegacy, nil
	}
	if !statusPresent && version == registry.ContractVersionV261 && containsToolNames(names, registry.ContractV261ToolNames()) {
		return contractModeV261, nil
	}
	return "", fmt.Errorf("unsupported or mixed MCP contract: remember=%q status_present=%t", version, statusPresent)
}

func containsToolNames(names map[string]struct{}, expected []string) bool {
	for _, name := range expected {
		if _, ok := names[name]; !ok {
			return false
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
			if values, ok := property["enum"].([]any); ok && len(values) == 1 {
				return stringValue(values[0])
			}
			if values, ok := property["enum"].([]string); ok && len(values) == 1 {
				return strings.TrimSpace(values[0])
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
	fragmentID := evidenceIDFromSubmission(out)
	if fragmentID == "" {
		return mapping, fmt.Errorf("import %s: remember response missing evidence id", item.SourceDocID)
	}
	addSourceMapping(&mapping, Ref{Type: "fragment", ID: fragmentID, SourceDocID: item.SourceDocID}, true)
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
