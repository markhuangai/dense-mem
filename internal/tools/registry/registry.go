// Package registry is the single source of truth for the dense-mem tool catalog.
//
// Every AI-exposed verb is registered once here with its name, description,
// JSON Schemas, required scopes, and a bound invoker. MCP handlers read from
// this registry instead of duplicating schemas.
package registry

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"sync"
)

const toolNamePatternText = `^[a-z][a-z0-9_]{0,63}$`

var toolNamePattern = regexp.MustCompile(toolNamePatternText)

// ToolInvoker is the uniform execution contract for every registered tool.
// The caller provides the profile scope explicitly so nothing inside the tool
// has to parse headers or context keys — the registry stays transport-agnostic.
type ToolInvoker func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error)

// Tool is the metadata + executor bundle for a single registered tool.
type Tool struct {
	Name           string
	Description    string
	InputSchema    map[string]any
	OutputSchema   map[string]any
	RequiredScopes []string
	FeatureGate    string
	Visibility     string
	Invoke         ToolInvoker
}

// Registry holds a set of Tools and answers register/list/get queries.
// Implementations must be safe for concurrent use.
type Registry interface {
	Register(tool Tool) error
	Get(name string) (Tool, bool)
	List() []Tool
}

// inMemoryRegistry is the default Registry implementation.
type inMemoryRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

var _ Registry = (*inMemoryRegistry)(nil)

// New returns an empty in-memory Registry.
func New() Registry {
	return &inMemoryRegistry{
		tools: make(map[string]Tool),
	}
}

// Register stores the tool. Returns an error when a tool with the same Name is
// already registered or when the Name is empty.
func (r *inMemoryRegistry) Register(tool Tool) error {
	if tool.Name == "" {
		return fmt.Errorf("registry: tool name must not be empty")
	}
	if !toolNamePattern.MatchString(tool.Name) {
		return fmt.Errorf("registry: tool name %q must match %s", tool.Name, toolNamePatternText)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[tool.Name]; exists {
		return fmt.Errorf("registry: tool %q already registered", tool.Name)
	}
	r.tools[tool.Name] = tool
	return nil
}

// Get returns the tool by name.
func (r *inMemoryRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools sorted alphabetically by Name so the MCP
// tool list is deterministic.
func (r *inMemoryRegistry) List() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
