package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

// Dependencies is the wiring bundle used to construct the active tool catalog.
type Dependencies struct {
	Metrics observability.DiscoverabilityMetrics

	RecallFeedbackConfig RecallFeedbackConfigProvider
	RecallFeedbackEvents RecallFeedbackEventRecorder
	EvaluationAudit      EvaluationAuditAppender

	Context     contextservice.Service
	Remember    memoryservice.RememberService
	Recall      memoryservice.RecallService
	Lifecycle   memoryservice.LifecycleService
	Evaluation  repository.EvaluationRepository
	Communities repository.CommunityRepository
	MemoryPack  skillpackservice.MemoryPackService
	Dreams      dreamservice.Service
}
type RecallFeedbackEventRecorder interface {
	RecordRecallSnapshot(ctx context.Context, event domain.RecallFeedbackEvent) error
	RecordRecallFeedback(ctx context.Context, feedback domain.RecallFeedbackSubmission) error
}

type EvaluationAuditAppender interface {
	Append(ctx context.Context, entry service.AuditLogEntry) error
}

// ErrToolUnavailable is the defensive fallback returned when a tool dependency
// was never wired. Production server wiring should prefer explicit
// error-returning services so callers receive a concrete provider error.
var ErrToolUnavailable = errors.New("tool not available (dependency missing or not yet implemented)")

// BuildActive wires the PostgreSQL-authoritative production registry.
func BuildActive(deps Dependencies) (Registry, error) {
	r := New()
	tools := append(contractTools(deps), evaluationTools(deps)...)
	for _, t := range tools {
		if err := r.Register(t); err != nil {
			return nil, fmt.Errorf("registry: BuildActive: %w", err)
		}
	}
	return r, nil
}

// --- schema + marshaling helpers ------------------------------------------

const (
	memoryEntryMaxLength = 999
	memoryEntryGuidance  = "Split large scenarios into multiple semantic entries under 1000 characters. Store one decision, fact, correction, preference, project milestone, or other claim-worthy unit per entry; attach typed claims to the smallest supporting entry and use multiple supported_by IDs only when one claim needs cross-entry evidence."
)

func memoryEntryString(description string) map[string]any {
	s := schemaString(description+" "+memoryEntryGuidance, memoryEntryMaxLength)
	s["x-validation-hint"] = memoryEntryGuidance
	return s
}

func schemaString(description string, maxLen int) map[string]any {
	s := map[string]any{"type": "string"}
	if description != "" {
		s["description"] = description
	}
	if maxLen > 0 {
		s["maxLength"] = maxLen
	}
	return s
}

func schemaEnum(values []string) map[string]any {
	return map[string]any{"type": "string", "enum": values}
}

func intInput(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), true
	case float64:
		if v == float64(int(v)) {
			return int(v), true
		}
	}
	return 0, false
}

// remapInput roundtrips a map[string]any into a typed request struct so each
// invoker can call its service without hand-written field mapping.
func remapInput(in map[string]any, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// structToMap roundtrips a typed service result back to a map[string]any so
// invokers can return a uniform shape. The cost is two json calls per call;
// acceptable for discovery tooling where the HTTP handlers remain the hot path.
func structToMap(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any)
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
