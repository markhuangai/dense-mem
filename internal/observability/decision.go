package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

func LogDecisionStage(ctx context.Context, logger LogProvider, operationID string, stage modelprovider.DecisionStage) {
	if logger == nil {
		return
	}
	attrs := []LogAttr{
		{Key: "event", Value: "decision.policy.completed"}, {Key: "operation_id", Value: operationID}, {Key: "workload", Value: stage.Workload},
		{Key: "settings_revision", Value: stage.Settings.Revision}, {Key: "settings_stale", Value: stage.Settings.Stale}, {Key: "provider", Value: stage.Settings.Provider.Provider},
		{Key: "configured_model", Value: stage.Settings.Provider.Model}, {Key: "preferences", Value: DecisionLogValue(stage.Settings.Preferences)}, {Key: "overrides", Value: DecisionLogValue(stage.Settings.Overrides)},
		{Key: "input_hash", Value: stage.InputHash}, {Key: "policy_revision", Value: stage.PolicyRevision}, {Key: "thresholds", Value: stage.Thresholds},
		{Key: "action", Value: stage.Action}, {Key: "reason_code", Value: stage.Reason},
		{Key: "selected_ids", Value: stage.SelectedIDs},
	}
	level := slog.LevelInfo
	if stage.Action == "fail_required_task" {
		level = slog.LevelError
	} else if stage.Action == "retain_original_ranking" {
		level = slog.LevelWarn
	}
	if contextual, ok := logger.(ContextLogProvider); ok {
		contextual.LogContext(ctx, level, "decision.policy.completed", attrs...)
		contextual.LogContext(ctx, slog.LevelDebug, "decision.policy.answers", append(attrs, LogAttr{Key: "result", Value: DecisionLogValue(stage.Result)}, LogAttr{Key: "references", Value: stage.References})...)
		return
	}
	switch level {
	case slog.LevelError:
		logger.Error("decision.policy.completed", nil, attrs...)
	case slog.LevelWarn:
		logger.Warn("decision.policy.completed", attrs...)
	default:
		logger.Info("decision.policy.completed", attrs...)
	}
	logger.Debug("decision.policy.answers", append(attrs, LogAttr{Key: "result", Value: DecisionLogValue(stage.Result)}, LogAttr{Key: "references", Value: stage.References})...)
}

// DecisionLogValue serializes server-owned records for the existing bounded credential protector.
func DecisionLogValue(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{"capture_state":"unavailable","reason_code":"diagnostic_encoding_failed"}`)
	}
	const limit = MaxOperationMetadataBytes / 4
	if len(encoded) > limit {
		return json.RawMessage(fmt.Sprintf(`{"capture_state":"unavailable","reason_code":"diagnostic_body_limit","body_bytes":%d,"body_limit_bytes":%d}`, len(encoded), limit))
	}
	return encoded
}
