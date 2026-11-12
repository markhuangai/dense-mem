package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEvidenceOnlyCatalogCompositionHasNoFactualRuntime(t *testing.T) {
	bundle := buildApplicationBundle(applicationCompositionDependencies{})
	require.NotNil(t, bundle.Remember)
	require.NotNil(t, bundle.Recall)
	require.NotNil(t, bundle.Context)
	require.NotNil(t, bundle.MemoryPack)
	require.NotNil(t, bundle.Lifecycle)
	reg, err := registry.BuildActive(registry.Dependencies{RememberBindings: registry.RememberBindings{Service: bundle.Remember}, RecallBindings: registry.RecallBindings{Service: bundle.Recall}, TraceBindings: registry.TraceBindings{Service: bundle.Context}, MemoryPackBindings: registry.MemoryPackBindings{Service: bundle.MemoryPack}, LifecycleBindings: registry.LifecycleBindings{Service: bundle.Lifecycle}})
	require.NoError(t, err)
	for _, name := range []string{"correct_relationship", "query_graph", "list_dreams", "list_communities"} {
		_, found := reg.Get(name)
		require.False(t, found, name)
	}
	for _, name := range []string{"remember", "recall_memory", "trace_memory", "export_memory_pack", "retract_evidence"} {
		tool, found := reg.Get(name)
		require.True(t, found, name)
		require.NotNil(t, tool.Invoke)
	}
}
