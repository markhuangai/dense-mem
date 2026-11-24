package topicassignment

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
	"github.com/markhuangai/dense-mem/internal/topicassignment/contract"
)

type Scope = contract.Scope
type Slice = contract.Slice
type Topic = contract.Topic

type CandidateReader interface {
	ReadAssignmentCandidates(context.Context, Scope, Slice) (contract.Candidates, error)
}

type ConfigSource interface {
	TopicMaintenanceRuntimeConfig(context.Context) (domain.TopicMaintenanceConfig, error)
}

type Service struct {
	reader     CandidateReader
	config     ConfigSource
	generation generation.Service[contract.Outcome]
	decisions  modelprovider.DecisionProvider
	limits     assessor.SemanticAssessmentLimits
	model      string
	logger     *slog.Logger
}

func NewService(reader CandidateReader, config ConfigSource, transport modelprovider.StructuredTransport, decisions modelprovider.DecisionProvider, limits assessor.SemanticAssessmentLimits, defaultModel string, logger *slog.Logger) *Service {
	return &Service{reader: reader, config: config, generation: generation.Service[contract.Outcome]{Transport: transport, Logger: logger}, decisions: decisions, limits: assessor.NormalizeSemanticAssessmentLimits(limits), model: defaultModel, logger: logger}
}

// Prepare evaluates immutable source slices without creating Evidence, definitions, or memberships.
func (s *Service) Prepare(ctx context.Context, scope Scope, slices []Slice, decisionSettings *domain.DecisionRuntimeConfig) ([]contract.Outcome, error) {
	return s.prepare(ctx, scope, slices, decisionSettings, nil)
}

// PrepareMaintenance uses the settings frozen for the active maintenance window.
func (s *Service) PrepareMaintenance(ctx context.Context, scope Scope, slices []Slice, decisionSettings *domain.DecisionRuntimeConfig, window domain.TopicMaintenanceConfig) ([]contract.Outcome, error) {
	return s.prepare(ctx, scope, slices, decisionSettings, &window)
}

func (s *Service) prepare(ctx context.Context, scope Scope, slices []Slice, decisionSettings *domain.DecisionRuntimeConfig, window *domain.TopicMaintenanceConfig) ([]contract.Outcome, error) {
	if len(slices) == 0 {
		return nil, nil
	}
	if s.config == nil || s.reader == nil {
		return nil, fmt.Errorf("%w: assignment dependencies missing", contract.ErrStorage)
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
		return nil, fmt.Errorf("%w: read assignment settings: %w", contract.ErrStorage, err)
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
	byText := make(map[inputKey]contract.Outcome)
	out := make([]contract.Outcome, 0, len(slices))
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
				return nil, fmt.Errorf("%w: read assignment candidates: %w", contract.ErrStorage, err)
			}
			candidates := catalog.Topics
			if len(candidates) > contract.MaxTopics {
				return nil, errors.New("assignment candidate bound exceeded")
			}
			request := assignmentRequest{RequestID: uuid.NewString(), Text: slice.Text, Topics: candidates}
			if request.Topics == nil {
				request.Topics = []Topic{}
			}
			if s.logger != nil {
				refs := make([]string, 0, len(candidates))
				for _, candidate := range candidates {
					refs = append(refs, fmt.Sprintf("%s@%d", candidate.ID, candidate.Version))
				}
				s.logger.DebugContext(ctx, "topic_assignment.candidates", "team_id", scope.TeamID,
					"operation_id", scope.OperationID, "assessment_id", request.RequestID,
					"slice", slice.Index, "policy", contract.PolicyVersion, "model", model, "candidates", refs)
			}
			hashInput := struct {
				Policy, Model, DecisionModel, DecisionVendor string
				UseDecisions                                 bool
				Text                                         string
				Topics                                       []Topic
			}{Policy: contract.PolicyVersion, Model: model, Text: slice.Text, Topics: candidates}
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
				outcome, err = s.assess(assessmentCtx, request, model, policy, decisionSettings)
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
	RequestID string  `json:"request_id"`
	Text      string  `json:"evidence"`
	Topics    []Topic `json:"topics"`
}

func (s *Service) assess(ctx context.Context, input assignmentRequest, model string, policy domain.TopicMaintenanceConfig, settings *domain.DecisionRuntimeConfig) (contract.Outcome, error) {
	if s.decisions != nil && settings != nil && settings.Effective.TopicAssignment && len(input.Topics) > 0 {
		outcome, clear, err := s.assessDecisions(ctx, input, *settings)
		if err != nil || clear {
			return outcome, err
		}
	}
	limits := s.limits
	if policy.ContextWindowTokens > 0 {
		limits.MaxInputTokens = min(limits.MaxInputTokens, int(policy.ContextWindowTokens)-limits.MaxOutputTokens)
	}
	return s.generate(ctx, input, model, limits)
}
