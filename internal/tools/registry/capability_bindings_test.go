package registry

import "testing"

func TestBuildActiveUsesRememberCapabilityFacet(t *testing.T) {
	reg, err := BuildActive(Dependencies{
		RememberBindings: RememberBindings{Service: &stubRememberService{}},
	})
	if err != nil {
		t.Fatalf("BuildActive returned error: %v", err)
	}
	tool, ok := reg.Get(ToolRemember)
	if !ok {
		t.Fatal("remember tool was not registered")
	}
	if tool.Invoke == nil {
		t.Fatal("remember tool did not receive its capability invoker")
	}
}

func TestCapabilityFacetBundleFeedsEveryContractTool(t *testing.T) {
	remember := &stubRememberService{}
	recall := &stubRecallService{}
	lifecycle := &stubLifecycleService{}
	trace := &stubTraceContext{}
	memoryPack := &exportOnlyMemoryPackStub{}
	deps := Dependencies{
		RememberBindings:   RememberBindings{Service: remember},
		RecallBindings:     RecallBindings{Service: recall},
		LifecycleBindings:  LifecycleBindings{Service: lifecycle},
		TraceBindings:      TraceBindings{Service: trace},
		MemoryPackBindings: MemoryPackBindings{Service: memoryPack},
	}
	if deps.RememberBindings.Service != remember || deps.RecallBindings.Service != recall ||
		deps.LifecycleBindings.Service != lifecycle || deps.TraceBindings.Service != trace ||
		deps.MemoryPackBindings.Service != memoryPack {
		t.Fatal("capability bindings did not preserve their services")
	}
	reg, err := BuildActive(deps)
	if err != nil {
		t.Fatalf("BuildActive returned error: %v", err)
	}
	for _, name := range ContractToolNames() {
		tool, ok := reg.Get(name)
		if !ok {
			t.Fatalf("contract tool %q was not registered", name)
		}
		if tool.Invoke == nil {
			t.Fatalf("contract tool %q did not receive a capability binding", name)
		}
	}
}
