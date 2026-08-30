package e2eapp

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/cmd/internal/serverapp"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
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
		write := &serverapp.WriteRuntime{Slice: raw, RegistryOverride: registryOverrideForSlice(slice), SynchronousRememberFactory: terminalRememberServiceFactory}
		require.NoError(t, runOverride(context.Background(), serverapp.RuntimeContext{}, write, override))
		require.Equal(t, raw, write.Slice)
		options := optionsForSlice(slice)
		require.NotNil(t, options.WriteRuntimeOverride)
		write = &serverapp.WriteRuntime{SynchronousRememberFactory: terminalRememberServiceFactory}
		require.NoError(t, options.WriteRuntimeOverride(context.Background(), serverapp.RuntimeContext{}, write))
		require.Equal(t, raw, write.Slice)
		require.NotNil(t, write.RegistryOverride)
		active := registry.New()
		if slice == WriteSliceRemember || slice == WriteSliceDiagnostics || slice == WriteSliceDream {
			require.NoError(t, active.Register(registry.Tool{Name: registry.ToolRemember, InputSchema: map[string]any{"type": "object"}}))
		} else if slice == WriteSliceContract {
			for _, tool := range registry.ContractTools() {
				tool.Visibility = "active"
				require.NoError(t, active.Register(tool))
			}
		}
		if slice == WriteSliceRemember || slice == WriteSliceDiagnostics || slice == WriteSliceDream || slice == WriteSliceContract {
			selectedRegistry, err := write.RegistryOverride(context.Background(), serverapp.RuntimeContext{}, active)
			require.NoError(t, err)
			require.NotNil(t, selectedRegistry)
		}
	}
}

func TestRememberOverrideClonesOnlyRememberWithTerminalInvoker(t *testing.T) {
	active := registry.New()
	legacyRemember := registry.Tool{
		Name: registry.ToolRemember, InputSchema: map[string]any{"type": "object"},
		OutputSchema: map[string]any{"type": "object", "properties": map[string]any{"status_tool": map[string]any{"type": "string"}}, "additionalProperties": false},
		Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
			return map[string]any{"status_tool": "get_submission_status"}, nil
		},
	}
	require.NoError(t, active.Register(legacyRemember))
	require.NoError(t, active.Register(registry.Tool{
		Name: registry.ToolGetSubmissionStatus, InputSchema: map[string]any{"type": "object"}, OutputSchema: map[string]any{"type": "object"},
		Invoke: func(context.Context, string, map[string]any) (map[string]any, error) {
			return map[string]any{"submission_id": "status-preserved", "processing_state": "completed"}, nil
		},
	}))
	require.NoError(t, active.Register(registry.Tool{Name: registry.ToolRecallMemory, InputSchema: map[string]any{"type": "object"}}))

	write := &serverapp.WriteRuntime{
		Slice:                      string(WriteSliceRemember),
		RegistryOverride:           registryOverrideForSlice(WriteSliceRemember),
		SynchronousRememberFactory: terminalRememberServiceFactory,
	}
	require.NoError(t, rememberOverride(context.Background(), serverapp.RuntimeContext{}, write))
	selected, err := write.RegistryOverride(context.Background(), serverapp.RuntimeContext{}, active)
	require.NoError(t, err)

	original, ok := active.Get(registry.ToolRemember)
	require.True(t, ok)
	require.Equal(t, legacyRemember.OutputSchema, original.OutputSchema)
	replacement, ok := selected.Get(registry.ToolRemember)
	require.True(t, ok)
	require.NotEqual(t, original.OutputSchema, replacement.OutputSchema)
	recall, ok := selected.Get(registry.ToolRecallMemory)
	require.True(t, ok)
	require.Equal(t, registry.ToolRecallMemory, recall.Name)
	status, ok := selected.Get(registry.ToolGetSubmissionStatus)
	require.True(t, ok)
	statusResult, err := status.Invoke(context.Background(), "ignored", map[string]any{"submission_id": "status-preserved"})
	require.NoError(t, err)
	require.Equal(t, "status-preserved", statusResult["submission_id"])
	require.Len(t, selected.List(), len(active.List()))
}

func TestSlicesWithoutSynchronousRememberAdoptionDoNotInvokeFactory(t *testing.T) {
	calls := 0
	factory := func() rememberapp.Service {
		calls++
		return terminalRememberService{}
	}
	for _, raw := range WriteSlices() {
		slice := WriteSlice(raw)
		if slice == WriteSliceRemember || slice == WriteSliceDiagnostics || slice == WriteSliceDream || slice == WriteSliceContract {
			continue
		}
		options := optionsForSlice(slice)
		write := &serverapp.WriteRuntime{SynchronousRememberFactory: factory}
		require.NoError(t, options.WriteRuntimeOverride(context.Background(), serverapp.RuntimeContext{}, write), slice)
	}
	require.Zero(t, calls)
}

func TestDiagnosticsOverrideInstallsTerminalRememberRuntime(t *testing.T) {
	calls := 0
	factory := func() rememberapp.Service {
		calls++
		return terminalRememberService{}
	}
	write := &serverapp.WriteRuntime{SynchronousRememberFactory: factory}
	options := optionsForSlice(WriteSliceDiagnostics)
	require.NoError(t, options.WriteRuntimeOverride(context.Background(), serverapp.RuntimeContext{}, write))
	require.Equal(t, 1, calls)
	require.NotNil(t, write.Remember)

	active := registry.New()
	require.NoError(t, active.Register(registry.Tool{Name: registry.ToolRemember, InputSchema: map[string]any{"type": "object"}}))
	original, ok := active.Get(registry.ToolRemember)
	require.True(t, ok)
	selected, err := write.RegistryOverride(context.Background(), serverapp.RuntimeContext{}, active)
	require.NoError(t, err)
	replacement, ok := selected.Get(registry.ToolRemember)
	require.True(t, ok)
	require.NotEqual(t, original.OutputSchema, replacement.OutputSchema)
}

func TestDreamOverrideInstallsSynchronousRemember(t *testing.T) {
	active := registry.New()
	registryOverride := registryOverrideForSlice(WriteSliceDream)
	factoryCalls := 0
	write := &serverapp.WriteRuntime{
		Slice:                      string(WriteSliceDream),
		RegistryOverride:           registryOverride,
		SynchronousRememberFactory: func() rememberapp.Service { factoryCalls++; return terminalRememberService{} },
	}

	require.NoError(t, dreamOverride(context.Background(), serverapp.RuntimeContext{}, write))
	require.Equal(t, 1, factoryCalls)
	require.NotNil(t, write.Remember)
	require.NotNil(t, write.RegistryOverride)
	selected, err := write.RegistryOverride(context.Background(), serverapp.RuntimeContext{}, active)
	require.NoError(t, err)
	require.Same(t, active, selected)
}

func TestRememberOverridePreservesEveryNonRememberTool(t *testing.T) {
	active := registry.New()
	originals := make(map[string]registry.Tool)
	for _, tool := range registry.ContractTools() {
		marker := tool.Name
		tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
			return map[string]any{"marker": marker}, nil
		}
		require.NoError(t, active.Register(tool))
		originals[tool.Name] = tool
	}

	write := &serverapp.WriteRuntime{
		Slice:                      string(WriteSliceRemember),
		RegistryOverride:           registryOverrideForSlice(WriteSliceRemember),
		SynchronousRememberFactory: terminalRememberServiceFactory,
	}
	require.NoError(t, rememberOverride(context.Background(), serverapp.RuntimeContext{}, write))
	selected, err := write.RegistryOverride(context.Background(), serverapp.RuntimeContext{}, active)
	require.NoError(t, err)

	for name, original := range originals {
		replacement, ok := selected.Get(name)
		require.True(t, ok, name)
		if name == registry.ToolRemember {
			require.NotEqual(t, original.OutputSchema, replacement.OutputSchema)
			continue
		}
		require.Equal(t, original.Description, replacement.Description, name)
		require.Equal(t, original.InputSchema, replacement.InputSchema, name)
		require.Equal(t, original.OutputSchema, replacement.OutputSchema, name)
		require.Equal(t, original.RequiredScopes, replacement.RequiredScopes, name)
		require.Equal(t, original.FeatureGate, replacement.FeatureGate, name)
		require.Equal(t, original.Visibility, replacement.Visibility, name)
		result, err := replacement.Invoke(context.Background(), "ignored", map[string]any{})
		require.NoError(t, err, name)
		require.Equal(t, name, result["marker"], name)
	}
}

func TestTerminalRememberInvokerReturnsPollingFreeTerminalResult(t *testing.T) {
	legacy := registry.Tool{Name: registry.ToolRemember, InputSchema: map[string]any{"type": "object"}, RequiredScopes: []string{"write"}}
	service := terminalRememberServiceFactory()
	active := registry.New()
	require.NoError(t, active.Register(legacy))
	selected, err := registry.WithTerminalRemember(active, service)
	require.NoError(t, err)
	remember, ok := selected.Get(registry.ToolRemember)
	require.True(t, ok)
	invoker := remember.Invoke
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{Grants: []string{"write"}})
	output, err := invoker(ctx, "ignored", map[string]any{
		"evidence": []any{map[string]any{"content": "terminal evidence"}},
		"relationships": []any{map[string]any{
			"ref": "r-1", "subject": map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
			"predicate": map[string]any{"proposed_key": "uses"},
			"object":    map[string]any{"value": map[string]any{"type": "string", "value": "PostgreSQL"}},
			"polarity":  "+", "evidence_indices": []any{0},
		}},
		"idempotency_key": "terminal-idempotency",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", output["processing_state"])
	require.Equal(t, "current", output["search_state"])
	_, hasStatus := output["status_tool"]
	require.False(t, hasStatus)
	_, hasPoll := output["check_after_seconds"]
	require.False(t, hasPoll)
	require.NoError(t, registry.ValidateInput(registry.Tool{InputSchema: remember.OutputSchema}, output))
}

func TestTerminalRememberInvokerReturnsStructuredToolErrorForTerminalFailure(t *testing.T) {
	legacy := registry.Tool{Name: registry.ToolRemember, InputSchema: map[string]any{"type": "object"}, RequiredScopes: []string{"write"}}
	active := registry.New()
	require.NoError(t, active.Register(legacy))
	selected, err := registry.WithTerminalRemember(active, terminalRememberFailureService{})
	require.NoError(t, err)
	remember, ok := selected.Get(registry.ToolRemember)
	require.True(t, ok)
	invoker := remember.Invoke
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{Grants: []string{"write"}})

	_, err = invoker(ctx, "ignored", map[string]any{
		"evidence": []any{map[string]any{"content": "terminal evidence"}},
		"relationships": []any{map[string]any{
			"ref": "r-1", "subject": map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
			"predicate": map[string]any{"proposed_key": "uses"},
			"object":    map[string]any{"value": map[string]any{"type": "string", "value": "PostgreSQL"}},
			"polarity":  "+", "evidence_indices": []any{0},
		}},
		"idempotency_key": "terminal-failure-idempotency",
	})

	structured, ok := registry.ToolResultFromError(err)
	require.True(t, ok)
	require.Equal(t, "rejected", structured.Result["processing_state"])
	require.NotNil(t, structured.Result["errors"])
}

func TestTerminalRememberInvokerValidatesSupersessionUUIDsLocally(t *testing.T) {
	targetID := uuid.New()
	for _, test := range []struct {
		name     string
		evidence []any
		path     string
		code     string
	}{
		{
			name: "invalid UUID",
			evidence: []any{map[string]any{
				"content": "replacement", "supersedes_evidence_ids": []any{"not-a-uuid"},
			}},
			path: "/evidence/0/supersedes_evidence_ids/0", code: "format",
		},
		{
			name: "equivalent UUID spellings",
			evidence: []any{
				map[string]any{"content": "first", "supersedes_evidence_ids": []any{strings.ToUpper(targetID.String())}},
				map[string]any{"content": "second", "supersedes_evidence_ids": []any{targetID.String()}},
			},
			path: "/evidence/1/supersedes_evidence_ids", code: "duplicate",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			legacy := registry.Tool{Name: registry.ToolRemember, InputSchema: map[string]any{"type": "object"}, RequiredScopes: []string{"write"}}
			active := registry.New()
			require.NoError(t, active.Register(legacy))
			selected, err := registry.WithTerminalRemember(active, terminalRememberService{})
			require.NoError(t, err)
			remember, ok := selected.Get(registry.ToolRemember)
			require.True(t, ok)
			invoker := remember.Invoke
			ctx := requestctx.WithActor(context.Background(), requestctx.Actor{Grants: []string{"write"}})
			evidenceIndices := make([]any, len(test.evidence))
			for index := range test.evidence {
				evidenceIndices[index] = index
			}
			input := map[string]any{
				"idempotency_key": "local-supersession-validation",
				"evidence":        test.evidence,
				"relationships": []any{map[string]any{
					"ref": "uses-postgresql", "subject": map[string]any{"name": "Dense-Mem"},
					"predicate": map[string]any{"proposed_key": "uses"},
					"object":    map[string]any{"value": map[string]any{"type": "string", "value": "PostgreSQL"}},
					"polarity":  "+", "evidence_indices": evidenceIndices,
				}},
			}

			_, err = invoker(ctx, "ignored", input)

			result, ok := registry.ContractValidationResultFromError(err)
			require.True(t, ok)
			require.Equal(t, test.path, result.Issues[0].Path)
			require.Equal(t, test.code, result.Issues[0].Code)
		})
	}
}

func TestTerminalRelationshipRefsTrimWhitespace(t *testing.T) {
	legacy := registry.Tool{Name: registry.ToolRemember, InputSchema: map[string]any{"type": "object"}, RequiredScopes: []string{"write"}}
	active := registry.New()
	require.NoError(t, active.Register(legacy))
	selected, err := registry.WithTerminalRemember(active, terminalRememberService{})
	require.NoError(t, err)
	remember, ok := selected.Get(registry.ToolRemember)
	require.True(t, ok)
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{Grants: []string{"write"}})
	output, err := remember.Invoke(ctx, "ignored", map[string]any{
		"evidence": []any{map[string]any{"content": "terminal evidence"}},
		"relationships": []any{map[string]any{
			"ref": "first", "subject": map[string]any{"name": "Dense-Mem", "entity_kind": "project"},
			"predicate": map[string]any{"proposed_key": "uses"},
			"object":    map[string]any{"value": map[string]any{"type": "string", "value": "PostgreSQL"}},
			"polarity":  "+", "evidence_indices": []any{0},
		}},
		"idempotency_key": "relationship-ref-trim",
	})
	require.NoError(t, err)
	results := output["relationship_results"].([]any)
	first := results[0].(map[string]any)
	require.Equal(t, "first", first["ref"])
}

func terminalRememberServiceFactory() rememberapp.Service {
	return terminalRememberService{}
}

type terminalRememberService struct{}

func (terminalRememberService) Remember(_ context.Context, req rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
	evidence := make([]rememberapp.TerminalEvidenceResult, len(req.Evidence))
	for index := range evidence {
		evidence[index] = rememberapp.TerminalEvidenceResult{Disposition: "stored", EvidenceID: uuid.NewString(), EvidenceIndex: index, SupersededEvidenceIDs: []string{}, SearchState: string(rememberapp.TerminalSearchCurrent)}
	}
	relationships := make([]rememberapp.SubmissionRelationshipResult, len(req.RelationshipHints))
	for index, hint := range req.RelationshipHints {
		ref, _ := hint["ref"].(string)
		relationships[index] = rememberapp.SubmissionRelationshipResult{RelationshipRef: ref, Disposition: "stored", Splits: []rememberapp.SubmissionRelationshipSplit{{SplitIndex: 0, RelationshipID: uuid.NewString(), RelationshipVersion: 1, Status: "active"}}}
	}
	terminal := &rememberapp.TerminalRememberResult{ContractVersion: domain.ContractVersion, SubmissionID: uuid.NewString(), SubmissionKind: "remember", ProcessingState: string(rememberapp.TerminalProcessingCompleted), SearchState: string(rememberapp.TerminalSearchCurrent), CorrelationID: "terminal-correlation", Evidence: evidence, RelationshipResults: relationships, Errors: []rememberapp.SubmissionStatusError{}, Kind: rememberapp.ResultKindTerminal}
	return &rememberapp.RememberResult{Terminal: terminal}, nil
}

func (terminalRememberService) GetSubmissionStatus(context.Context, rememberapp.GetSubmissionStatusRequest) (*rememberapp.SubmissionStatusResult, error) {
	return nil, nil
}

type terminalRememberFailureService struct{}

func (terminalRememberFailureService) Remember(_ context.Context, req rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
	base := &rememberapp.TerminalRememberResult{
		ContractVersion: domain.ContractVersion, SubmissionID: uuid.NewString(), SubmissionKind: "remember",
		CorrelationID: "terminal-failure-correlation", Evidence: make([]rememberapp.TerminalEvidenceResult, len(req.Evidence)),
		RelationshipResults: make([]rememberapp.SubmissionRelationshipResult, len(req.RelationshipHints)),
		Errors:              []rememberapp.SubmissionStatusError{}, Kind: rememberapp.ResultKindTerminal,
	}
	for index := range base.RelationshipResults {
		ref, _ := req.RelationshipHints[index]["ref"].(string)
		base.RelationshipResults[index] = rememberapp.SubmissionRelationshipResult{RelationshipRef: ref, Splits: []rememberapp.SubmissionRelationshipSplit{}}
	}
	return nil, rememberapp.TerminalResultWithError(base, rememberapp.TerminalErrorNoSupportedMemory)
}

func (terminalRememberFailureService) GetSubmissionStatus(context.Context, rememberapp.GetSubmissionStatusRequest) (*rememberapp.SubmissionStatusResult, error) {
	return nil, nil
}
