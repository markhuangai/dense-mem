// Package registry is the single source of truth for the dense-mem tool catalog.
//
// Every AI-exposed verb is registered once here with its name, description,
// JSON Schemas, required scopes, and a bound invoker. HTTP handlers, the
// catalog endpoint (Unit 21), the OpenAPI generator (Unit 23), and the MCP
// server (Unit 24) all read from this registry instead of duplicating schemas.
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

// ToolInputNormalizer rewrites backward-compatible call shapes into the
// canonical input shape before schema validation and invocation.
type ToolInputNormalizer func(input map[string]any) map[string]any

// Tool is the metadata + executor bundle for a single registered tool.
type Tool struct {
	Name           string
	Description    string
	InputSchema    map[string]any
	OutputSchema   map[string]any
	RequiredScopes []string
	Invoke         ToolInvoker
	NormalizeInput ToolInputNormalizer
	Aliases        []string
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
	mu      sync.RWMutex
	tools   map[string]Tool
	aliases map[string]string
}

var _ Registry = (*inMemoryRegistry)(nil)

// New returns an empty in-memory Registry.
func New() Registry {
	return &inMemoryRegistry{
		tools:   make(map[string]Tool),
		aliases: make(map[string]string),
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
	if canonical, exists := r.aliases[tool.Name]; exists {
		return fmt.Errorf("registry: tool name %q already registered as alias for %q", tool.Name, canonical)
	}
	seenAliases := map[string]struct{}{}
	for _, alias := range tool.Aliases {
		if alias == "" {
			return fmt.Errorf("registry: alias for tool %q must not be empty", tool.Name)
		}
		if !toolNamePattern.MatchString(alias) {
			return fmt.Errorf("registry: alias %q for tool %q must match %s", alias, tool.Name, toolNamePatternText)
		}
		if alias == tool.Name {
			return fmt.Errorf("registry: alias %q duplicates canonical tool name", alias)
		}
		if _, exists := r.tools[alias]; exists {
			return fmt.Errorf("registry: alias %q for tool %q conflicts with registered tool", alias, tool.Name)
		}
		if canonical, exists := r.aliases[alias]; exists {
			return fmt.Errorf("registry: alias %q for tool %q already registered for %q", alias, tool.Name, canonical)
		}
		if _, exists := seenAliases[alias]; exists {
			return fmt.Errorf("registry: alias %q for tool %q is duplicated", alias, tool.Name)
		}
		seenAliases[alias] = struct{}{}
	}
	r.tools[tool.Name] = tool
	for _, alias := range tool.Aliases {
		r.aliases[alias] = tool.Name
	}
	return nil
}

// Get returns the tool by name.
func (r *inMemoryRegistry) Get(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	if ok {
		return t, true
	}
	canonical, ok := r.aliases[name]
	if !ok {
		return Tool{}, false
	}
	t, ok = r.tools[canonical]
	return t, ok
}

// List returns all registered tools sorted alphabetically by Name so the output
// is deterministic for the catalog endpoint, OpenAPI spec, and MCP tool list.
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

// NormalizeInput applies a tool-specific backward-compatibility rewrite, when
// one is registered. The returned map is the input that should be validated and
// passed to Invoke.
func NormalizeInput(tool Tool, args map[string]any) map[string]any {
	if args == nil {
		args = map[string]any{}
	}
	if tool.NormalizeInput == nil {
		return args
	}
	return tool.NormalizeInput(args)
}
