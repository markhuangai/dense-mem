package topics

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/markhuangai/dense-mem/internal/service/generation"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	"github.com/markhuangai/dense-mem/internal/topics/assignment"
)

type AssignmentCandidateReader interface {
	ReadAssignmentCandidates(context.Context, assignment.Scope, assignment.Slice) (assignment.Candidates, error)
}

type AssignmentService struct {
	reader     AssignmentCandidateReader
	config     MaintenanceConfigSource
	generation generation.Service[assignment.Outcome]
	decisions  modelprovider.DecisionProvider
	limits     assessor.SemanticAssessmentLimits
	model      string
	logger     *slog.Logger
}

func NewAssignmentService(reader AssignmentCandidateReader, config MaintenanceConfigSource, transport modelprovider.StructuredTransport, decisions modelprovider.DecisionProvider, limits assessor.SemanticAssessmentLimits, defaultModel string, logger *slog.Logger) *AssignmentService {
	return &AssignmentService{reader: reader, config: config, generation: generation.Service[assignment.Outcome]{Transport: transport, Logger: logger}, decisions: decisions, limits: assessor.NormalizeSemanticAssessmentLimits(limits), model: defaultModel, logger: logger}
}

// Prepare evaluates immutable source slices without creating Evidence, definitions, or memberships.
func (s *AssignmentService) Prepare(ctx context.Context, scope assignment.Scope, slices []assignment.Slice, decisionSettings *domain.DecisionRuntimeConfig) ([]assignment.Outcome, error) {
	return s.prepare(ctx, scope, slices, decisionSettings, nil, nil)
}

// PrepareMaintenance uses the settings frozen for the active maintenance window.
func (s *AssignmentService) PrepareMaintenance(ctx context.Context, scope assignment.Scope, slices []assignment.Slice, decisionSettings *domain.DecisionRuntimeConfig, window domain.TopicMaintenanceConfig, accounting assessment.AttemptAccounting) ([]assignment.Outcome, error) {
	return s.prepare(ctx, scope, slices, decisionSettings, &window, accounting)
}

func (s *AssignmentService) prepare(ctx context.Context, scope assignment.Scope, slices []assignment.Slice, decisionSettings *domain.DecisionRuntimeConfig, window *domain.TopicMaintenanceConfig, accounting assessment.AttemptAccounting) ([]assignment.Outcome, error) {
	if len(slices) == 0 {
		return nil, nil
	}
	if s.config == nil || s.reader == nil {
		return nil, fmt.Errorf("%w: assignment dependencies missing", assignment.ErrStorage)
	}
	if _, err := uuid.Parse(scope.TeamID); err != nil {
		return nil, errors.New("invalid assignment team")
	}
	if _, err := uuid.Parse(scope.SpaceID); err != nil {
		return nil, errors.New("invalid assignment space")
	}
	if scope.Generation < 1 || scope.OperationID == "" || len(slices) > knowledge.MaxRememberPartitions {
		return nil, errors.New("invalid assignment scope or slice count")
	}
	policy, err := s.config.TopicMaintenanceRuntimeConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: read assignment settings: %w", assignment.ErrStorage, err)
	}
	if !policy.Enabled {
		return nil, nil
	}
	if window != nil {
		policy.Model = window.Model
		policy.ContextWindowTokens = window.ContextWindowTokens
	}
	model := policy.Model
	if strings.TrimSpace(model) == "" {
		model = s.model
	}
	if s.decisions != nil && decisionSettings == nil {
		return nil, errors.New("topic assignment decision settings unavailable")
	}
	ctx = requestctx.WithProgressOperation(ctx, "topic_assignment")
	ctx = observability.WithAIOperation(ctx, "topic_assignment", len(slices))
	requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "topic_assignment"})
	seen := make(map[int]bool, len(slices))
	type inputKey struct{ evidenceID, fingerprint, text, prior string }
	byText := make(map[inputKey]assignment.Outcome)
	out := make([]assignment.Outcome, 0, len(slices))
	for _, slice := range slices {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if slice.Index < 0 || seen[slice.Index] || strings.TrimSpace(slice.Text) == "" || utf8.RuneCountInString(slice.Text) > knowledge.MaxRememberItemRunes {
			return nil, errors.New("invalid assignment slice")
		}
		seen[slice.Index] = true
		cacheKey := inputKey{slice.EvidenceID, slice.SourceFingerprint, slice.Text, slice.PriorInputHash}
		outcome, found := byText[cacheKey]
		if !found {
			catalog, err := s.reader.ReadAssignmentCandidates(ctx, scope, slice)
			if err != nil {
				return nil, fmt.Errorf("%w: read assignment candidates: %w", assignment.ErrStorage, err)
			}
			candidates := catalog.Topics
			if len(candidates) > assignment.MaxTopics {
				return nil, errors.New("assignment candidate bound exceeded")
			}
			request := assignmentRequest{RequestID: uuid.NewString(), Text: slice.Text, Topics: candidates}
			if request.Topics == nil {
				request.Topics = []assignment.Topic{}
			}
			if s.logger != nil {
				refs := make([]string, 0, len(candidates))
				for _, candidate := range candidates {
					refs = append(refs, fmt.Sprintf("%s@%d", candidate.ID, candidate.Version))
				}
				s.logger.DebugContext(ctx, "topic_assignment.candidates", "team_id", scope.TeamID,
					"operation_id", scope.OperationID, "assessment_id", request.RequestID,
					"slice", slice.Index, "policy", assignment.PolicyVersion, "model", model, "candidates", refs)
			}
			hashInput := struct {
				Policy, Model, DecisionModel, DecisionVendor string
				UseDecisions                                 bool
				Text                                         string
				Topics                                       []assignment.Topic
			}{Policy: assignment.PolicyVersion, Model: model, Text: slice.Text, Topics: candidates}
			if decisionSettings != nil && decisionSettings.Effective.TopicAssignment {
				hashInput.UseDecisions = true
				hashInput.DecisionModel = decisionSettings.Provider.Model
				hashInput.DecisionVendor = decisionSettings.Provider.Provider
			}
			inputHash, err := modelprovider.DecisionInputHash(hashInput)
			if err != nil {
				return nil, err
			}
			if slice.PriorInputHash == inputHash && !catalog.NeedsRepair {
				outcome.Unchanged = true
			} else {
				exchange := observability.ExchangeScopeFromContext(ctx)
				exchange.TeamID, exchange.OperationID, exchange.AssessmentID = scope.TeamID, scope.OperationID, request.RequestID
				assessmentCtx := observability.WithExchangeScope(ctx, exchange)
				outcome, err = s.assess(assessmentCtx, request, model, policy, decisionSettings, accounting)
				if err != nil {
					return nil, err
				}
				if s.logger != nil {
					s.logger.InfoContext(assessmentCtx, "topic_assignment.prepared", "team_id", scope.TeamID,
						"operation_id", scope.OperationID, "assessment_id", request.RequestID, "method", outcome.Method,
						"matched_topics", len(outcome.Matches), "proposed_topics", len(outcome.Proposals))
				}
			}
			outcome.Text = slice.Text
			outcome.Reviewed = candidates
			outcome.InputHash = inputHash
			byText[cacheKey] = outcome
		}
		outcome.Index = slice.Index
		out = append(out, outcome)
	}
	return out, nil
}

type assignmentRequest struct {
	RequestID string             `json:"request_id"`
	Text      string             `json:"evidence"`
	Topics    []assignment.Topic `json:"topics"`
}

func (s *AssignmentService) assess(ctx context.Context, input assignmentRequest, model string, policy domain.TopicMaintenanceConfig, settings *domain.DecisionRuntimeConfig, accounting assessment.AttemptAccounting) (assignment.Outcome, error) {
	if s.decisions != nil && settings != nil && settings.Effective.TopicAssignment && len(input.Topics) > 0 {
		outcome, clear, err := s.assessDecisions(ctx, input, *settings, accounting)
		if err != nil || clear {
			return outcome, err
		}
	}
	limits := s.limits
	if policy.ContextWindowTokens > 0 {
		limits.MaxInputTokens = min(limits.MaxInputTokens, int(policy.ContextWindowTokens)-limits.MaxOutputTokens)
	}
	return s.generate(ctx, input, model, limits, accounting)
}
