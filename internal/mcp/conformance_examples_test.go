package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

const discoveryExampleRequestPrefix = "Example request (replace each <returned_...> placeholder with its documented prerequisite result; generate and retain each <new_..._key> before calling):\n"

func testConformanceToolExamples(t *testing.T) {
	t.Helper()
	logger, _ := testLogger(t)
	reg := registry.New()
	invocations := map[string]int{}
	for _, tool := range registry.ContractTools() {
		tool := tool
		tool.Visibility = "active"
		tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
			invocations[tool.Name]++
			return map[string]any{}, nil
		}
		if err := reg.Register(tool); err != nil {
			t.Fatal(err)
		}
	}
	server := NewServerWithScopesTeamContextAndRuntimeConfig(
		reg,
		"profile-a",
		[]string{"read", "write"},
		TeamContext{},
		logger,
		recallFeedbackConfigStub{enabled: true},
		dreamingConfigStub{enabled: true},
	)
	response := conformanceRPC(t, server, `{"jsonrpc":"2.0","id":7,"method":"tools/list","params":{}}`)
	if response.Error != nil {
		t.Fatalf("tools/list error = %+v", response.Error)
	}
	var listed struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(response.Result, &listed); err != nil {
		t.Fatal(err)
	}
	descriptions := make(map[string]string, len(listed.Tools))
	for _, tool := range listed.Tools {
		descriptions[tool.Name] = tool.Description
	}
	for _, name := range registry.ContractToolNames() {
		description, ok := descriptions[name]
		if !ok {
			t.Fatalf("tools/list did not expose %s", name)
		}
		expected, ok := contractDescription(name)
		if !ok {
			t.Fatalf("contract catalog did not expose %s", name)
		}
		if description != expected {
			t.Fatalf("tools/list description for %s diverged from the catalog", name)
		}
		for _, section := range []string{"When to use:", "Prerequisites:", "Example request", "Result:", "Next action:"} {
			if !strings.Contains(description, section) {
				t.Fatalf("%s description missing %q", name, section)
			}
		}
	}

	remember := discoveryExampleRequest(t, descriptions[registry.ToolRemember])
	validCall := conformanceRPC(t, server, toolCallRequest(t, 8, registry.ToolRemember, remember))
	if validCall.Error != nil || invocations[registry.ToolRemember] != 1 {
		t.Fatalf("discovered remember example = error:%+v invocations:%d", validCall.Error, invocations[registry.ToolRemember])
	}
	invalidRemember := cloneDiscoveryExampleRequest(t, remember)
	invalidRemember["unexpected"] = true
	invalidCall := conformanceRPC(t, server, toolCallRequest(t, 9, registry.ToolRemember, invalidRemember))
	if invalidCall.Error == nil || invalidCall.Error.Code != errCodeInvalidParams {
		t.Fatalf("mutated discovered example = %+v, want invalid params", invalidCall.Error)
	}
	if invocations[registry.ToolRemember] != 1 {
		t.Fatalf("invalid discovered example invoked remember %d times", invocations[registry.ToolRemember])
	}

	correction := discoveryExampleRequest(t, descriptions[registry.ToolCorrectRelationship])
	correctionCall := conformanceRPC(t, server, toolCallRequest(t, 10, registry.ToolCorrectRelationship, correction))
	if correctionCall.Error != nil || invocations[registry.ToolCorrectRelationship] != 1 {
		t.Fatalf("discovered correction example = error:%+v invocations:%d", correctionCall.Error, invocations[registry.ToolCorrectRelationship])
	}
}

func contractDescription(name string) (string, bool) {
	for _, tool := range registry.ContractTools() {
		if tool.Name == name {
			return tool.Description, true
		}
	}
	return "", false
}

func discoveryExampleRequest(t *testing.T, description string) map[string]any {
	t.Helper()
	start := strings.Index(description, discoveryExampleRequestPrefix)
	if start < 0 {
		t.Fatal("description has no primary example request")
	}
	encoded := description[start+len(discoveryExampleRequestPrefix):]
	end := strings.Index(encoded, "\n\nResult:")
	if end < 0 {
		t.Fatal("description has no primary example result")
	}
	var request map[string]any
	if err := json.Unmarshal([]byte(encoded[:end]), &request); err != nil {
		t.Fatalf("decode discovered example: %v", err)
	}
	return replaceDiscoveryPlaceholders(request).(map[string]any)
}

func replaceDiscoveryPlaceholders(value any) any {
	switch typed := value.(type) {
	case string:
		if strings.HasPrefix(typed, "<") && strings.HasSuffix(typed, ">") {
			return uuid.NewString()
		}
		return typed
	case map[string]any:
		for key, item := range typed {
			typed[key] = replaceDiscoveryPlaceholders(item)
		}
		return typed
	case []any:
		for index, item := range typed {
			typed[index] = replaceDiscoveryPlaceholders(item)
		}
		return typed
	default:
		return value
	}
}

func cloneDiscoveryExampleRequest(t *testing.T, request map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var cloned map[string]any
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func toolCallRequest(t *testing.T, id int, name string, arguments map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  "tools/call",
		"params":  map[string]any{"name": name, "arguments": arguments},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}
