// Package e2eapp contains the disposable composition entrypoint. It is kept
// separate from cmd/server so test selectors cannot become release config.
package e2eapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/markhuangai/dense-mem/cmd/internal/serverapp"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

const WriteSliceEnvironment = "DENSE_MEM_E2E_WRITE_SLICE"

type WriteSlice string

const (
	WriteSliceLegacy         WriteSlice = "legacy"
	WriteSliceRemember       WriteSlice = "remember"
	WriteSliceCorrection     WriteSlice = "correction"
	WriteSliceConflict       WriteSlice = "conflict"
	WriteSliceDream          WriteSlice = "dream"
	WriteSliceReconciliation WriteSlice = "reconciliation"
	WriteSliceDiagnostics    WriteSlice = "diagnostics"
	WriteSliceContract       WriteSlice = "contract"
)

var writeSlices = []WriteSlice{
	WriteSliceLegacy,
	WriteSliceRemember,
	WriteSliceCorrection,
	WriteSliceConflict,
	WriteSliceDream,
	WriteSliceReconciliation,
	WriteSliceDiagnostics,
	WriteSliceContract,
}

// WriteSlices returns the closed selector vocabulary in deterministic order.
func WriteSlices() []string {
	result := make([]string, 0, len(writeSlices))
	for _, slice := range writeSlices {
		result = append(result, string(slice))
	}
	return result
}

func ParseWriteSlice(raw string) (WriteSlice, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return WriteSliceLegacy, nil
	}
	for _, slice := range writeSlices {
		if raw == string(slice) {
			return slice, nil
		}
	}
	return "", fmt.Errorf("e2e write slice %q is not supported", raw)
}

// Run is the only function that reads DENSE_MEM_E2E_WRITE_SLICE. The release
// command has no dependency on this package and cannot select a slice.
func Run() {
	slice, err := ParseWriteSlice(getenv(WriteSliceEnvironment))
	if err != nil {
		panic(err)
	}
	serverapp.RunFromEnvironment(optionsForSlice(slice))
}

// optionsForSlice keeps one predeclared slot per adoption ticket. The slots
// intentionally leave the current legacy service unchanged until that ticket
// supplies a real terminal writer.
func optionsForSlice(slice WriteSlice) serverapp.RuntimeOptions {
	selected := sliceOverrides[slice]
	return serverapp.RuntimeOptions{
		WriteRuntimeOverride: func(ctx context.Context, runtime serverapp.RuntimeContext, write *serverapp.WriteRuntime) error {
			write.Slice = string(slice)
			write.RegistryOverride = registryOverrideForSlice(slice)
			return runOverride(ctx, runtime, write, selected)
		},
	}
}

// registryOverrideForSlice is the E2E-only installation point shared by every
// adoption slice. It preserves the active catalog until the owning ticket
// supplies a replacement, while making the selected hook part of composition
// rather than an unread selector label.
func registryOverrideForSlice(slice WriteSlice) func(context.Context, serverapp.RuntimeContext, registry.Registry) (registry.Registry, error) {
	return func(_ context.Context, _ serverapp.RuntimeContext, active registry.Registry) (registry.Registry, error) {
		if active == nil {
			return nil, fmt.Errorf("e2e write slice %q received a nil registry", slice)
		}
		return active, nil
	}
}

type writeSliceOverride func(context.Context, serverapp.RuntimeContext, *serverapp.WriteRuntime) error

var sliceOverrides = map[WriteSlice]writeSliceOverride{
	WriteSliceLegacy:         legacyOverride,
	WriteSliceRemember:       rememberOverride,
	WriteSliceCorrection:     correctionOverride,
	WriteSliceConflict:       conflictOverride,
	WriteSliceDream:          dreamOverride,
	WriteSliceReconciliation: reconciliationOverride,
	WriteSliceDiagnostics:    diagnosticsOverride,
	WriteSliceContract:       contractOverride,
}

func runOverride(ctx context.Context, runtime serverapp.RuntimeContext, write *serverapp.WriteRuntime, selected writeSliceOverride) error {
	if selected == nil {
		return fmt.Errorf("e2e write slice is not registered")
	}
	return selected(ctx, runtime, write)
}

func legacyOverride(_ context.Context, _ serverapp.RuntimeContext, write *serverapp.WriteRuntime) error {
	return validateSliceHook(write, WriteSliceLegacy)
}
func rememberOverride(_ context.Context, _ serverapp.RuntimeContext, write *serverapp.WriteRuntime) error {
	return validateSliceHook(write, WriteSliceRemember)
}
func correctionOverride(_ context.Context, _ serverapp.RuntimeContext, write *serverapp.WriteRuntime) error {
	return validateSliceHook(write, WriteSliceCorrection)
}
func conflictOverride(_ context.Context, _ serverapp.RuntimeContext, write *serverapp.WriteRuntime) error {
	return validateSliceHook(write, WriteSliceConflict)
}
func dreamOverride(_ context.Context, _ serverapp.RuntimeContext, write *serverapp.WriteRuntime) error {
	return validateSliceHook(write, WriteSliceDream)
}
func reconciliationOverride(_ context.Context, _ serverapp.RuntimeContext, write *serverapp.WriteRuntime) error {
	return validateSliceHook(write, WriteSliceReconciliation)
}
func diagnosticsOverride(_ context.Context, _ serverapp.RuntimeContext, write *serverapp.WriteRuntime) error {
	return validateSliceHook(write, WriteSliceDiagnostics)
}
func contractOverride(_ context.Context, _ serverapp.RuntimeContext, write *serverapp.WriteRuntime) error {
	return validateSliceHook(write, WriteSliceContract)
}

func validateSliceHook(write *serverapp.WriteRuntime, expected WriteSlice) error {
	if write == nil || write.Slice != string(expected) {
		return fmt.Errorf("e2e write slice hook selected %q with runtime slice %q", expected, writeSliceName(write))
	}
	if write.RegistryOverride == nil {
		return fmt.Errorf("e2e write slice %q has no registry installation hook", expected)
	}
	return nil
}

func writeSliceName(write *serverapp.WriteRuntime) string {
	if write == nil {
		return ""
	}
	return write.Slice
}

// getenv is a variable for unit tests; only this E2E package owns the lookup.
var getenv = func(key string) string {
	return lookupEnvironment(key)
}
