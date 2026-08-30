// Package e2eapp contains the disposable composition entrypoint. It is kept
// separate from cmd/server so test selectors cannot become release config.
package e2eapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/cmd/internal/serverapp"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
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

// optionsForSlice keeps one predeclared slot per adoption ticket. Only the
// selected slice can replace its owned writer or registry entry.
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
func rememberOverride(_ context.Context, runtime serverapp.RuntimeContext, write *serverapp.WriteRuntime) error {
	if err := validateSliceHook(write, WriteSliceRemember); err != nil {
		return err
	}
	write.SynchronousRememberBeforeCommit = synchronousRememberBeforeCommitHook(runtime)
	if write.SynchronousRememberFactory == nil {
		return fmt.Errorf("e2e write slice %q has no synchronous Remember factory", WriteSliceRemember)
	}
	remember := write.SynchronousRememberFactory()
	if remember == nil {
		return fmt.Errorf("e2e write slice %q factory returned nil Remember service", WriteSliceRemember)
	}
	write.Remember = remember
	write.RegistryOverride = terminalRememberRegistryOverride(remember)
	return nil
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
func diagnosticsOverride(_ context.Context, runtime serverapp.RuntimeContext, write *serverapp.WriteRuntime) error {
	if err := validateSliceHook(write, WriteSliceDiagnostics); err != nil {
		return err
	}
	write.SynchronousRememberBeforeCommit = synchronousRememberBeforeCommitHook(runtime)
	if write.SynchronousRememberFactory == nil {
		return fmt.Errorf("e2e write slice %q has no synchronous Remember factory", WriteSliceDiagnostics)
	}
	remember := write.SynchronousRememberFactory()
	if remember == nil {
		return fmt.Errorf("e2e write slice %q factory returned nil Remember service", WriteSliceDiagnostics)
	}
	write.Remember = remember
	write.RegistryOverride = terminalRememberRegistryOverride(remember)
	return nil
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

func terminalRememberRegistryOverride(remember rememberapp.Service) func(context.Context, serverapp.RuntimeContext, registry.Registry) (registry.Registry, error) {
	return func(_ context.Context, _ serverapp.RuntimeContext, active registry.Registry) (registry.Registry, error) {
		if active == nil {
			return nil, fmt.Errorf("e2e terminal Remember received a nil registry")
		}
		if remember == nil {
			return nil, fmt.Errorf("e2e terminal Remember has no service")
		}
		legacy, ok := active.Get(registry.ToolRemember)
		if !ok {
			return nil, fmt.Errorf("e2e terminal Remember registry has no Remember tool")
		}
		replacement := legacy
		replacement.OutputSchema = terminalRememberOutputSchema()
		replacement.Invoke = terminalRememberInvoker(legacy, remember)
		return cloneRegistryReplacing(active, replacement)
	}
}

func cloneRegistryReplacing(active registry.Registry, replacement registry.Tool) (registry.Registry, error) {
	cloned := registry.New()
	replaced := false
	for _, tool := range active.List() {
		if tool.Name == replacement.Name {
			tool = replacement
			replaced = true
		}
		if err := cloned.Register(tool); err != nil {
			return nil, fmt.Errorf("e2e clone registry: %w", err)
		}
	}
	if !replaced {
		return nil, fmt.Errorf("e2e clone registry: tool %q is not registered", replacement.Name)
	}
	return cloned, nil
}

func terminalRememberInvoker(legacy registry.Tool, remember rememberapp.Service) registry.ToolInvoker {
	return func(ctx context.Context, _ string, input map[string]any) (map[string]any, error) {
		actor, ok := requestctx.ActorFromContext(ctx)
		if !ok {
			return nil, fmt.Errorf("remember: authenticated actor context is required")
		}
		if err := registry.ValidateContractInput(legacy, input, actor.Grants); err != nil {
			return nil, fmt.Errorf("remember: invalid input: %w", err)
		}
		if err := validateTerminalRememberSupersessions(input); err != nil {
			return nil, err
		}
		req, err := terminalRememberRequest(input)
		if err != nil {
			return nil, fmt.Errorf("remember: invalid input: %w", err)
		}
		result, err := remember.Remember(ctx, req)
		if err != nil {
			var processErr *rememberapp.RememberProcessError
			if !errors.As(err, &processErr) || processErr.Result == nil {
				return nil, err
			}
			result = &rememberapp.RememberResult{Terminal: processErr.Result}
		}
		if result == nil || result.Terminal == nil {
			return nil, fmt.Errorf("remember: terminal result is required")
		}
		if err := rememberapp.ValidateTerminalRememberResult(result.Terminal, len(req.Evidence), terminalRelationshipRefs(input)); err != nil {
			return nil, fmt.Errorf("remember: invalid terminal result")
		}
		output, err := terminalRememberResultMap(result.Terminal)
		if err != nil {
			return nil, err
		}
		if result.Terminal.ProcessingState != string(rememberapp.TerminalProcessingCompleted) {
			return nil, registry.NewToolResultError(output)
		}
		return output, nil
	}
}

func validateTerminalRememberSupersessions(input map[string]any) error {
	evidence, _ := input["evidence"].([]any)
	seen := make(map[uuid.UUID]int)
	for evidenceIndex, rawEvidence := range evidence {
		item, _ := rawEvidence.(map[string]any)
		targets, _ := item["supersedes_evidence_ids"].([]any)
		for targetIndex, rawTarget := range targets {
			target, _ := rawTarget.(string)
			parsed, err := uuid.Parse(strings.TrimSpace(target))
			if err != nil {
				return terminalRememberValidationFailure(
					fmt.Sprintf("/evidence/%d/supersedes_evidence_ids/%d", evidenceIndex, targetIndex),
					"format",
					fmt.Sprintf("evidence[%d].supersedes_evidence_ids[%d]: target must be a UUID", evidenceIndex, targetIndex),
				)
			}
			if previous, exists := seen[parsed]; exists {
				return terminalRememberValidationFailure(
					fmt.Sprintf("/evidence/%d/supersedes_evidence_ids", evidenceIndex),
					"duplicate",
					fmt.Sprintf("evidence[%d].supersedes_evidence_ids: duplicates target from evidence[%d]", evidenceIndex, previous),
				)
			}
			seen[parsed] = evidenceIndex
		}
	}
	return nil
}

func terminalRememberValidationFailure(path, code, message string) error {
	return &registry.ContractValidationFailure{Result: registry.ContractValidationResult{Issues: []registry.ContractValidationIssue{{
		Path: path, Code: code, Message: message,
	}}}}
}

func terminalRememberRequest(input map[string]any) (rememberapp.RememberRequest, error) {
	copyInput := make(map[string]any, len(input)+1)
	for key, value := range input {
		copyInput[key] = value
	}
	copyInput["relationship_hints"] = input["relationships"]
	encoded, err := json.Marshal(copyInput)
	if err != nil {
		return rememberapp.RememberRequest{}, err
	}
	var request rememberapp.RememberRequest
	if err := json.Unmarshal(encoded, &request); err != nil {
		return rememberapp.RememberRequest{}, err
	}
	return request, nil
}

func terminalRelationshipRefs(input map[string]any) []string {
	switch raw := input["relationships"].(type) {
	case []any:
		refs := make([]string, 0, len(raw))
		for _, value := range raw {
			item, _ := value.(map[string]any)
			ref, _ := item["ref"].(string)
			refs = append(refs, strings.TrimSpace(ref))
		}
		return refs
	case []map[string]any:
		refs := make([]string, 0, len(raw))
		for _, item := range raw {
			ref, _ := item["ref"].(string)
			refs = append(refs, strings.TrimSpace(ref))
		}
		return refs
	}
	return []string{}
}

func terminalRememberResultMap(result *rememberapp.TerminalRememberResult) (map[string]any, error) {
	encoded, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	output := map[string]any{}
	if err := json.Unmarshal(encoded, &output); err != nil {
		return nil, err
	}
	return output, nil
}

func terminalRememberOutputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"required": []string{
			"contract_version", "submission_id", "submission_kind", "processing_state", "search_state",
			"correlation_id", "evidence", "relationship_results", "errors",
		},
		"properties": map[string]any{
			"contract_version":     map[string]any{"type": "string", "enum": []string{domain.ContractVersion}},
			"submission_id":        map[string]any{"type": "string", "maxLength": 128},
			"submission_kind":      map[string]any{"type": "string", "enum": []string{"remember"}},
			"processing_state":     map[string]any{"type": "string", "enum": []string{"completed", "rejected", "quarantined", "failed"}},
			"search_state":         map[string]any{"type": "string", "enum": []string{"current", "not_required"}},
			"correlation_id":       map[string]any{"type": "string", "maxLength": 128},
			"evidence":             map[string]any{"type": "array", "minItems": 0, "maxItems": 20, "items": terminalEvidenceSchema()},
			"relationship_results": map[string]any{"type": "array", "minItems": 0, "maxItems": 200, "items": terminalRelationshipResultSchema()},
			"errors":               map[string]any{"type": "array", "minItems": 0, "maxItems": 50, "items": terminalErrorSchema()},
		},
		"additionalProperties": false,
	}
}

func terminalEvidenceSchema() map[string]any {
	return map[string]any{
		"type": "object", "required": []string{"disposition", "evidence_index", "superseded_evidence_ids", "search_state"},
		"properties": map[string]any{
			"disposition":             map[string]any{"type": "string", "enum": []string{"stored", "not_stored"}},
			"evidence_id":             map[string]any{"type": "string", "maxLength": 128},
			"evidence_index":          map[string]any{"type": "integer", "minimum": 0},
			"superseded_evidence_ids": map[string]any{"type": "array", "maxItems": 50, "items": map[string]any{"type": "string", "maxLength": 128}},
			"search_state":            map[string]any{"type": "string", "enum": []string{"current", "not_required"}},
			"reason":                  map[string]any{"type": "string", "maxLength": 256},
		},
		"additionalProperties": false,
	}
}

func terminalRelationshipResultSchema() map[string]any {
	return map[string]any{
		"type": "object", "required": []string{"ref", "disposition", "splits"},
		"properties": map[string]any{
			"ref":         map[string]any{"type": "string", "maxLength": 128},
			"disposition": map[string]any{"type": "string", "enum": []string{"stored", "not_stored"}},
			"reason":      map[string]any{"type": "string", "maxLength": 256},
			"splits": map[string]any{"type": "array", "maxItems": 50, "items": map[string]any{
				"type": "object", "required": []string{"split_index", "relationship_id", "relationship_version", "status"},
				"properties": map[string]any{
					"split_index":          map[string]any{"type": "integer", "minimum": 0},
					"relationship_id":      map[string]any{"type": "string", "maxLength": 128},
					"relationship_version": map[string]any{"type": "integer", "minimum": 1},
					"status":               map[string]any{"type": "string", "maxLength": 64},
				},
				"additionalProperties": false,
			}},
		},
		"additionalProperties": false,
	}
}

func terminalErrorSchema() map[string]any {
	return map[string]any{
		"type": "object", "required": []string{"code", "message", "retryable", "next_action", "remediation"},
		"properties": map[string]any{
			"code":        map[string]any{"type": "string", "enum": rememberapp.TerminalErrorCodes()},
			"message":     map[string]any{"type": "string", "maxLength": 512},
			"retryable":   map[string]any{"type": "boolean"},
			"next_action": map[string]any{"type": "string", "enum": rememberapp.TerminalNextActions()},
			"remediation": map[string]any{"type": "string", "maxLength": 512},
		},
		"additionalProperties": false,
	}
}

// getenv is a variable for unit tests; only this E2E package owns the lookup.
var getenv = func(key string) string {
	return lookupEnvironment(key)
}
