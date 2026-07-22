package registry

import (
	"context"
	"testing"
)

func TestBuildV2UATActivatesMCPTools(t *testing.T) {
	reg, err := BuildV2UAT(Dependencies{})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	for _, name := range []string{
		V2ToolRemember,
		V2ToolGetMemoryPlacement,
		V2ToolResolveMemoryPlacement,
		V2ToolCorrectEntityResolution,
		V2ToolRecallMemory,
		V2ToolTraceMemory,
	} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("BuildV2UAT missing %s", name)
		}
		if !ToolVisible(context.Background(), tool, nil) {
			t.Fatalf("%s is not visible in the UAT MCP registry", name)
		}
	}
}

func TestBuildV2ActiveActivatesMCPTools(t *testing.T) {
	reg, err := BuildV2Active(Dependencies{})
	if err != nil {
		t.Fatalf("BuildV2Active: %v", err)
	}
	for _, name := range []string{
		V2ToolRemember,
		V2ToolRecallMemory,
		V2ToolTraceMemory,
	} {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("BuildV2Active missing %s", name)
		}
		if !ToolVisible(context.Background(), tool, nil) {
			t.Fatalf("%s is not visible in the active MCP registry", name)
		}
	}
}

func TestHTTPRegistryViewKeepsV2MemoryToolsMCPOnly(t *testing.T) {
	uat, err := BuildV2UAT(Dependencies{})
	if err != nil {
		t.Fatalf("BuildV2UAT: %v", err)
	}
	httpReg, err := HTTPRegistryView(uat)
	if err != nil {
		t.Fatalf("HTTPRegistryView: %v", err)
	}

	for _, name := range []string{
		V2ToolRemember,
		V2ToolGetMemoryPlacement,
		V2ToolResolveMemoryPlacement,
		V2ToolCorrectEntityResolution,
		V2ToolRecallMemory,
		V2ToolTraceMemory,
	} {
		if _, ok := uat.Get(name); !ok {
			t.Fatalf("UAT registry missing %s", name)
		}
		if _, ok := httpReg.Get(name); ok {
			t.Fatalf("HTTP registry exposed MCP-only V2 memory tool %s", name)
		}
	}

	if _, ok := httpReg.Get(V2ToolListDreams); !ok {
		t.Fatal("HTTP registry filtered non-memory V2 tool list_dreams")
	}
}

func TestHTTPRegistryViewDoesNotHideLegacyToolNames(t *testing.T) {
	defaultReg, err := BuildDefault(Dependencies{})
	if err != nil {
		t.Fatalf("BuildDefault: %v", err)
	}
	httpReg, err := HTTPRegistryView(defaultReg)
	if err != nil {
		t.Fatalf("HTTPRegistryView: %v", err)
	}
	for _, name := range []string{V2ToolRemember, V2ToolRecallMemory, V2ToolTraceMemory} {
		if _, ok := httpReg.Get(name); !ok {
			t.Fatalf("HTTP registry hid legacy tool name %s", name)
		}
	}
}
