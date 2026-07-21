package registry

import "fmt"

var v2MCPOnlyMemoryToolNames = map[string]struct{}{
	V2ToolRemember:                {},
	V2ToolGetMemoryPlacement:      {},
	V2ToolResolveMemoryPlacement:  {},
	V2ToolCorrectEntityResolution: {},
	V2ToolRecallMemory:            {},
	V2ToolTraceMemory:             {},
}

// FilterRegistry returns a read/write registry copy containing only included
// tools from base.
func FilterRegistry(base Registry, include func(Tool) bool) (Registry, error) {
	out := New()
	if base == nil {
		return out, nil
	}
	for _, tool := range base.List() {
		if include != nil && !include(tool) {
			continue
		}
		if err := out.Register(tool); err != nil {
			return nil, fmt.Errorf("registry: filter %s: %w", tool.Name, err)
		}
	}
	return out, nil
}

// HTTPRegistryView returns the generic HTTP/OpenAPI registry view. V2 semantic
// memory tools stay MCP-only even when the UAT registry activates them.
func HTTPRegistryView(base Registry) (Registry, error) {
	return FilterRegistry(base, func(tool Tool) bool {
		return !IsV2MCPOnlyMemoryTool(tool)
	})
}

// IsV2MCPOnlyMemoryTool reports whether tool belongs to the V2 memory surface
// that is advertised and executed only through MCP.
func IsV2MCPOnlyMemoryTool(tool Tool) bool {
	if !IsV2ContractTool(tool) {
		return false
	}
	_, ok := v2MCPOnlyMemoryToolNames[tool.Name]
	return ok
}
