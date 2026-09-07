package http

import "testing"

func TestMCPBindingsPopulateProtectedCompatibilityView(t *testing.T) {
	repo := &routerCredentialRepo{}
	deps := ProtectedDeps{MCP: MCPBindings{CredentialRepo: repo}}
	wired := deps.withMCPBindings()
	if wired.CredentialRepo != repo {
		t.Fatal("MCP capability binding did not populate the protected dependency view")
	}
}

func TestMemoryPortalBindingsPopulateUserPortalCompatibilityView(t *testing.T) {
	privateMemory := &privateMemoryServiceStub{}
	deps := UserPortalDeps{Memory: MemoryPortalBindings{PrivateMemory: privateMemory}}
	wired := deps.withMemoryBindings()
	if wired.PrivateMemory != privateMemory {
		t.Fatal("memory capability binding did not populate the user portal dependency view")
	}
}
