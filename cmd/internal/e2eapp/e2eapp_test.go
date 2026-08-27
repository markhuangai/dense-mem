package e2eapp

import (
	"context"
	"testing"

	"github.com/markhuangai/dense-mem/cmd/internal/serverapp"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
	"github.com/stretchr/testify/require"
)

func TestParseWriteSliceUsesClosedVocabulary(t *testing.T) {
	got, err := ParseWriteSlice("")
	require.NoError(t, err)
	require.Equal(t, WriteSliceLegacy, got)
	for _, expected := range WriteSlices() {
		got, err := ParseWriteSlice(expected)
		require.NoError(t, err)
		require.Equal(t, WriteSlice(expected), got)
	}
	_, err = ParseWriteSlice("future")
	require.Error(t, err)
}

func TestEveryWriteSliceHasAnOverrideSlot(t *testing.T) {
	for _, raw := range WriteSlices() {
		slice := WriteSlice(raw)
		override, ok := sliceOverrides[slice]
		require.True(t, ok, raw)
		write := &serverapp.WriteRuntime{Slice: raw, RegistryOverride: registryOverrideForSlice(slice)}
		require.NoError(t, runOverride(context.Background(), serverapp.RuntimeContext{}, write, override))
		require.Equal(t, raw, write.Slice)
		options := optionsForSlice(slice)
		require.NotNil(t, options.WriteRuntimeOverride)
		write = &serverapp.WriteRuntime{}
		require.NoError(t, options.WriteRuntimeOverride(context.Background(), serverapp.RuntimeContext{}, write))
		require.Equal(t, raw, write.Slice)
		require.NotNil(t, write.RegistryOverride)
		selectedRegistry, err := write.RegistryOverride(context.Background(), serverapp.RuntimeContext{}, registry.New())
		require.NoError(t, err)
		require.NotNil(t, selectedRegistry)
	}
}
