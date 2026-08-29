package registry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/httperr"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

func TestContractV261CatalogIsInactiveAndExactlyTenTools(t *testing.T) {
	tools := ContractV261Tools()
	require.Len(t, tools, 10)
	require.Equal(t, ContractV261ToolNames(), toolNames(tools))
	for _, tool := range tools {
		require.Equal(t, domain.FeatureGate, tool.FeatureGate, tool.Name)
		require.Equal(t, domain.ToolVisibility, tool.Visibility, tool.Name)
		require.Nil(t, tool.Invoke, tool.Name)
	}
	_, present := findTool(tools, ToolGetSubmissionStatus)
	require.False(t, present)

	remember, present := findTool(tools, ToolRemember)
	require.True(t, present)
	require.Equal(t, []string{ContractVersionV261}, remember.OutputSchema["properties"].(map[string]any)["contract_version"].(map[string]any)["enum"])
	correction, present := findTool(tools, ToolCorrectRelationship)
	require.True(t, present)
	correctionProperties := correction.OutputSchema["properties"].(map[string]any)
	require.Contains(t, correctionProperties, "awaiting_confirmation")
	require.Contains(t, correctionProperties, "correction_result")
	require.Contains(t, correctionProperties, "errors")
}

func TestContractToolRuntimeOptionalCoversGatedToolsOnly(t *testing.T) {
	for _, name := range []string{ToolSubmitRecallSessionFeedback, ToolListDreams, ToolGetDream, ToolResolveDreamFeedback} {
		require.True(t, ContractToolRuntimeOptional(name), name)
	}
	for _, name := range []string{ToolRemember, ToolRetractEvidence, ToolCorrectRelationship, ToolRecallMemory, ToolTraceMemory, ToolExportMemoryPack, ToolGetSubmissionStatus} {
		require.False(t, ContractToolRuntimeOptional(name), name)
	}
}

func TestBuildContractV261RemovesStatusWithoutChangingProductionCatalog(t *testing.T) {
	active := New()
	for _, tool := range ContractTools() {
		tool.Visibility = "active"
		tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
			return map[string]any{"marker": "legacy"}, nil
		}
		require.NoError(t, active.Register(tool))
	}
	remember := &v261RememberService{}
	selected, err := BuildContractV261(active, remember)
	require.NoError(t, err)
	require.Len(t, selected.List(), len(ContractV261ToolNames()))
	require.ElementsMatch(t, ContractV261ToolNames(), toolNames(selected.List()))
	_, present := selected.Get(ToolGetSubmissionStatus)
	require.False(t, present)
	for _, name := range ContractV261ToolNames() {
		tool, ok := selected.Get(name)
		require.True(t, ok, name)
		require.Equal(t, "active", tool.Visibility, name)
	}
	require.Len(t, ContractTools(), 11)
	require.Contains(t, ContractToolNames(), ToolGetSubmissionStatus)
}

func TestBuildContractV261RequiresLegacyStatusAndEveryTargetTool(t *testing.T) {
	remember := &v261RememberService{}
	active := New()
	_, err := BuildContractV261(active, remember)
	require.ErrorContains(t, err, ToolGetSubmissionStatus)

	active = New()
	for _, tool := range ContractTools() {
		if tool.Name == ToolRecallMemory {
			continue
		}
		require.NoError(t, active.Register(tool))
	}
	_, err = BuildContractV261(active, remember)
	require.ErrorContains(t, err, ToolRecallMemory)
}

func TestContractV261SchemasValidateRememberAndCorrectionStates(t *testing.T) {
	evidenceID := uuid.NewString()
	relationshipID := uuid.NewString()
	rememberOutput := map[string]any{
		"contract_version": ContractVersionV261,
		"submission_id":    uuid.NewString(),
		"submission_kind":  "remember",
		"processing_state": "completed",
		"search_state":     "current",
		"correlation_id":   "corr-remember",
		"evidence": []any{map[string]any{
			"disposition": "stored", "evidence_id": evidenceID, "evidence_index": 0,
			"superseded_evidence_ids": []any{}, "search_state": "current",
		}},
		"relationship_results": []any{map[string]any{
			"ref": "rel-1", "disposition": "stored",
			"splits": []any{map[string]any{"split_index": 0, "relationship_id": relationshipID, "relationship_version": 1, "status": "active"}},
		}},
		"errors": []any{},
	}
	rememberTool, _ := findTool(ContractV261Tools(), ToolRemember)
	require.NoError(t, ValidateInput(Tool{InputSchema: rememberTool.OutputSchema}, rememberOutput))

	awaiting := map[string]any{
		"contract_version": ContractVersionV261, "submission_id": uuid.NewString(),
		"submission_kind": "relationship_correction", "processing_state": "awaiting_confirmation",
		"search_state": "pending", "correlation_id": "corr-correction",
		"awaiting_confirmation": map[string]any{
			"confirmation_token": "token-1", "expires_at": "2026-08-30T00:00:00Z",
			"candidates": []any{
				map[string]any{"endpoint": "subject_entity", "entity_id": uuid.NewString(), "entity_kind": "project", "canonical_name": "Dense-Mem"},
				map[string]any{"endpoint": "object_entity", "entity_id": uuid.NewString(), "entity_kind": "project", "canonical_name": "PostgreSQL"},
			},
		},
		"errors": []any{},
	}
	correctionTool, _ := findTool(ContractV261Tools(), ToolCorrectRelationship)
	require.NoError(t, ValidateInput(Tool{InputSchema: correctionTool.OutputSchema}, awaiting))

	completed := map[string]any{
		"contract_version": ContractVersionV261, "submission_id": uuid.NewString(),
		"submission_kind": "relationship_correction", "processing_state": "completed",
		"search_state": "current", "correlation_id": "corr-correction", "errors": []any{},
		"correction_result": map[string]any{
			"original_relationship_id": uuid.NewString(), "original_version": 1,
			"successor_relationship_id": uuid.NewString(), "successor_version": 1, "reused_successor": false,
		},
	}
	require.NoError(t, ValidateInput(Tool{InputSchema: correctionTool.OutputSchema}, completed))
}

func TestContractV261FixturesValidateTargetOutputs(t *testing.T) {
	data, err := os.ReadFile("testdata/contract_v261_fixtures.json")
	require.NoError(t, err)
	var fixtures []struct {
		Name   string         `json:"name"`
		Tool   string         `json:"tool"`
		Output map[string]any `json:"output"`
	}
	require.NoError(t, json.Unmarshal(data, &fixtures))
	tools := ContractV261Tools()
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			tool, ok := findTool(tools, fixture.Tool)
			require.True(t, ok)
			require.NoError(t, ValidateInput(Tool{InputSchema: tool.OutputSchema}, fixture.Output))
		})
	}
}

func TestTerminalCorrectionInvokerProjectsReceiptAndStatusOnce(t *testing.T) {
	active := New()
	var calls []string
	for _, tool := range ContractTools() {
		tool.Visibility = "active"
		tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
			return map[string]any{"marker": "legacy"}, nil
		}
		switch tool.Name {
		case ToolCorrectRelationship:
			tool.Invoke = func(ctx context.Context, teamID string, input map[string]any) (map[string]any, error) {
				calls = append(calls, "receipt:"+teamID+":"+contextMarker(ctx))
				return map[string]any{"submission_id": "correction-1", "processing_state": "completed", "correlation_id": "receipt-correlation"}, nil
			}
		case ToolGetSubmissionStatus:
			tool.Invoke = func(ctx context.Context, teamID string, input map[string]any) (map[string]any, error) {
				calls = append(calls, "status:"+teamID+":"+contextMarker(ctx))
				return map[string]any{
					"submission_id": "correction-1", "processing_state": "completed", "search_state": "current",
					"correlation_id": "status-correlation", "correction_result": map[string]any{
						"original_relationship_id": uuid.NewString(), "original_version": 1,
						"successor_relationship_id": uuid.NewString(), "successor_version": 1, "reused_successor": false,
					}, "errors": []any{},
				}, nil
			}
		}
		require.NoError(t, active.Register(tool))
	}
	selected, err := BuildContractV261(active, &v261RememberService{})
	require.NoError(t, err)
	tool, ok := selected.Get(ToolCorrectRelationship)
	require.True(t, ok)
	ctx := requestctx.WithActor(context.WithValue(context.Background(), markerKey{}, "same-context"), requestctx.Actor{Grants: []string{"write"}})
	output, err := tool.Invoke(ctx, "team-1", map[string]any{
		"action": "submit", "relationship_id": uuid.NewString(), "expected_version": 1,
		"patch":    map[string]any{"predicate": map[string]any{"key": "uses"}},
		"supports": []any{map[string]any{"evidence_id": uuid.NewString(), "start": 0, "end": 4}},
		"reason":   "predicate was resolved incorrectly", "idempotency_key": "correction-key",
	})
	require.NoError(t, err)
	require.Equal(t, ContractVersionV261, output["contract_version"])
	require.Equal(t, "completed", output["processing_state"])
	require.NotNil(t, output["correction_result"])
	require.Empty(t, output["errors"])
	require.Equal(t, []string{"receipt:team-1:same-context", "status:team-1:same-context"}, calls)
}

func TestTerminalCorrectionInvokerStatusReadFailurePreservesReplayGuidance(t *testing.T) {
	active := New()
	for _, tool := range ContractTools() {
		tool.Visibility = "active"
		tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
			return nil, errors.New("unused")
		}
		switch tool.Name {
		case ToolCorrectRelationship:
			tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
				return map[string]any{"submission_id": uuid.NewString(), "processing_state": "processing", "correlation_id": "receipt-correlation"}, nil
			}
		case ToolGetSubmissionStatus:
			tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
				return nil, errors.New("database status read failed")
			}
		}
		require.NoError(t, active.Register(tool))
	}
	selected, err := BuildContractV261(active, &v261RememberService{})
	require.NoError(t, err)
	tool, ok := selected.Get(ToolCorrectRelationship)
	require.True(t, ok)
	ctx := requestctx.WithActor(context.Background(), requestctx.Actor{Grants: []string{"write"}})
	_, err = tool.Invoke(ctx, "team-1", map[string]any{
		"action": "submit", "relationship_id": uuid.NewString(), "expected_version": 1,
		"patch":    map[string]any{"predicate": map[string]any{"key": "uses"}},
		"supports": []any{map[string]any{"evidence_id": uuid.NewString(), "start": 0, "end": 4}},
		"reason":   "predicate was resolved incorrectly", "idempotency_key": "correction-replay-key",
	})
	structured, ok := ToolResultFromError(err)
	require.True(t, ok)
	errorItem := structured.Result["errors"].([]any)[0].(map[string]any)
	require.Equal(t, string(rememberapp.TerminalErrorDatabaseFailure), errorItem["code"])
	require.Equal(t, string(rememberapp.TerminalNextActionRetrySameRequest), errorItem["next_action"])
	require.Contains(t, errorItem["remediation"], "same idempotency_key")
	require.NoError(t, ValidateInput(Tool{InputSchema: tool.OutputSchema}, structured.Result))
}

func TestTerminalCorrectionInvokerMapsBoundedDomainErrors(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code string
	}{
		{name: "entity not found", err: httperr.New(httperr.NOT_FOUND, "hidden"), code: "entity_not_found"},
		{name: "relationship changed", err: httperr.New(httperr.CONFLICT, "hidden"), code: "relationship_changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			active := New()
			for _, tool := range ContractTools() {
				tool.Visibility = "active"
				tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) {
					return nil, errors.New("unused")
				}
				if tool.Name == ToolCorrectRelationship {
					tool.Invoke = func(context.Context, string, map[string]any) (map[string]any, error) { return nil, test.err }
				}
				require.NoError(t, active.Register(tool))
			}
			selected, err := BuildContractV261(active, &v261RememberService{})
			require.NoError(t, err)
			tool, _ := selected.Get(ToolCorrectRelationship)
			ctx := requestctx.WithActor(context.Background(), requestctx.Actor{Grants: []string{"write"}})
			_, err = tool.Invoke(ctx, "team-1", map[string]any{
				"action": "submit", "relationship_id": uuid.NewString(), "expected_version": 1,
				"patch":    map[string]any{"predicate": map[string]any{"key": "uses"}},
				"supports": []any{map[string]any{"evidence_id": uuid.NewString(), "start": 0, "end": 4}},
				"reason":   "predicate was resolved incorrectly", "idempotency_key": "correction-key",
			})
			structured, ok := ToolResultFromError(err)
			require.True(t, ok)
			item := structured.Result["errors"].([]any)[0].(map[string]any)
			require.Equal(t, test.code, item["code"])
		})
	}
}

type markerKey struct{}

func contextMarker(ctx context.Context) string {
	value, _ := ctx.Value(markerKey{}).(string)
	return value
}

func toolNames(tools []Tool) []string {
	result := make([]string, 0, len(tools))
	for _, tool := range tools {
		result = append(result, tool.Name)
	}
	return result
}

func findTool(tools []Tool, name string) (Tool, bool) {
	for _, tool := range tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return Tool{}, false
}

type v261RememberService struct{}

func (v261RememberService) Remember(_ context.Context, req rememberapp.RememberRequest) (*rememberapp.RememberResult, error) {
	evidence := make([]rememberapp.TerminalEvidenceResult, len(req.Evidence))
	for index := range evidence {
		evidence[index] = rememberapp.TerminalEvidenceResult{Disposition: "stored", EvidenceID: uuid.NewString(), EvidenceIndex: index, SupersededEvidenceIDs: []string{}, SearchState: string(rememberapp.TerminalSearchCurrent)}
	}
	relationships := make([]rememberapp.SubmissionRelationshipResult, len(req.RelationshipHints))
	for index, hint := range req.RelationshipHints {
		ref, _ := hint["ref"].(string)
		relationships[index] = rememberapp.SubmissionRelationshipResult{RelationshipRef: ref, Disposition: "stored", Splits: []rememberapp.SubmissionRelationshipSplit{{SplitIndex: 0, RelationshipID: uuid.NewString(), RelationshipVersion: 1, Status: "active"}}}
	}
	return &rememberapp.RememberResult{Terminal: &rememberapp.TerminalRememberResult{
		ContractVersion: domain.ContractVersion, SubmissionID: uuid.NewString(), SubmissionKind: "remember",
		ProcessingState: string(rememberapp.TerminalProcessingCompleted), SearchState: string(rememberapp.TerminalSearchCurrent), CorrelationID: "remember-correlation",
		Evidence: evidence, RelationshipResults: relationships, Errors: []rememberapp.SubmissionStatusError{}, Kind: rememberapp.ResultKindTerminal,
	}}, nil
}

func (v261RememberService) GetSubmissionStatus(context.Context, rememberapp.GetSubmissionStatusRequest) (*rememberapp.SubmissionStatusResult, error) {
	return nil, nil
}
