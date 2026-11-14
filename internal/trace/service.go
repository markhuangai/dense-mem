package trace

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"log/slog"
	"strings"
)

var ErrTraceAuthContext = errors.New("trace: authenticated actor context is required")
var ErrTraceEvidenceNotFound = tracecontract.ErrEvidenceNotFound
var ErrTraceRepositoryTeamMismatch = errors.New("trace: repository returned a mismatched team")

type SemanticTraceStore interface {
	TraceEvidence(context.Context, tracecontract.Input) (*tracecontract.EvidenceTraceResult, error)
}
type semanticTraceService struct {
	store  SemanticTraceStore
	logger observability.LogProvider
}

func NewSemantic(store SemanticTraceStore, loggers ...observability.LogProvider) Service {
	service := &semanticTraceService{store: store}
	if len(loggers) > 0 {
		service.logger = loggers[0]
	}
	return service
}
func (s *semanticTraceService) Trace(ctx context.Context, _ string, req TraceRequest) (*TraceResult, error) {
	requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "validating_input"})
	if s == nil || s.store == nil {
		return nil, errors.New("trace: Evidence store is required")
	}
	actor, ok := requestctx.ActorFromContext(ctx)
	if !ok || actor.TeamID == uuid.Nil || actor.OwnerID == uuid.Nil {
		return nil, ErrTraceAuthContext
	}
	id, err := uuid.Parse(strings.TrimSpace(req.EvidenceID))
	if err != nil || id == uuid.Nil {
		return nil, tracecontract.ErrEvidenceIDInvalid
	}
	input := tracecontract.NormalizeInput(tracecontract.Input{TeamID: actor.TeamID.String(), EvidenceID: id.String(), IncludeEvidenceContent: req.IncludeEvidenceContent, IncludeTransitions: req.IncludeTransitions, MaxEvents: req.MaxEvents, MaxFragmentContentRunes: req.MaxFragmentContentRunes})
	requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "trace_read"})
	result, err := s.store.TraceEvidence(ctx, input)
	if err == nil && (result == nil || result.Evidence == nil) {
		err = ErrTraceEvidenceNotFound
	}
	if err == nil && result.Evidence.TeamID != actor.TeamID.String() {
		err = ErrTraceRepositoryTeamMismatch
	}
	if s.logger != nil {
		attrs := []observability.LogAttr{observability.String("team_id", actor.TeamID.String()), observability.String("owner_profile_id", actor.OwnerID.String()), observability.String("evidence_id", input.EvidenceID), {Key: "limits", Value: observability.DecisionLogValue(input)}}
		if err != nil {
			attrs = append(attrs, observability.String("outcome", "failed"), observability.String("reason", err.Error()))
		} else {
			attrs = append(attrs, observability.String("outcome", "completed"), observability.LogAttr{Key: "omissions", Value: result.Omissions}, observability.LogAttr{Key: "selection", Value: result.Selection})
		}
		if logger, ok := s.logger.(observability.ContextLogProvider); ok {
			logger.LogContext(ctx, slog.LevelDebug, "trace_evidence_selection", attrs...)
		} else {
			s.logger.Debug("trace_evidence_selection", attrs...)
		}
	}
	if err != nil {
		return nil, err
	}
	requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "preparing_result"})
	return &TraceResult{Semantic: result}, nil
}
