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
	contract "github.com/markhuangai/dense-mem/internal/topics/contract"
	"golang.org/x/sync/errgroup"
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
	ctx = requestctx.WithSynchronizedProgress(ctx)
	ctx = observability.WithAIOperation(ctx, "topic_assignment", len(slices))
	requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "topic_assignment"})
	seen := make(map[int]bool, len(slices))
	type inputKey struct{ evidenceID, fingerprint, text, prior string }
	type workItem struct {
		request    assignmentRequest
		outcome    assignment.Outcome
		evidenceID string
		pending    bool
	}
	byText := make(map[inputKey]*workItem)
	positions := make([]*workItem, 0, len(slices))
	var pending []*workItem
	for _, slice := range slices {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if slice.Index < 0 || seen[slice.Index] || strings.TrimSpace(slice.Text) == "" || utf8.RuneCountInString(slice.Text) > knowledge.MaxRememberItemRunes {
			return nil, errors.New("invalid assignment slice")
		}
		seen[slice.Index] = true
		cacheKey := inputKey{slice.EvidenceID, slice.SourceFingerprint, slice.Text, slice.PriorInputHash}
		item, found := byText[cacheKey]
		if !found {
			catalog, err := s.reader.ReadAssignmentCandidates(ctx, scope, slice)
			if err != nil {
				if !errors.Is(err, assignment.ErrStale) {
					err = fmt.Errorf("%w: read assignment candidates: %w", assignment.ErrStorage, err)
				}
				if window == nil {
					return nil, err
				}
				item = &workItem{evidenceID: slice.EvidenceID, outcome: assignment.Outcome{Text: slice.Text, Failure: err}}
				byText[cacheKey] = item
				positions = append(positions, item)
				continue
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
				level := slog.LevelDebug
				if catalog.VectorUnavailable || catalog.UnembeddedTopicCount > 0 {
					level = slog.LevelWarn
				}
				s.logger.Log(ctx, level, "topic_assignment.candidates_retrieved", "team_id", scope.TeamID,
					"space_id", scope.SpaceID, "space_generation", scope.Generation, "operation_id", scope.OperationID,
					"evidence_id", slice.EvidenceID, "embedding_contract_id", catalog.EmbeddingContractID,
					"pgvector_count", catalog.VectorCount, "fts_count", catalog.FullTextCount,
					"merged_count", len(candidates), "unembedded_topic_count", catalog.UnembeddedTopicCount,
					"forced_repair_count", catalog.ForcedRepairCount, "vector_unavailable", catalog.VectorUnavailable)
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
			evidenceID := slice.EvidenceID
			if evidenceID == "" {
				evidenceID = slice.CanonicalEvidenceID
			}
			item = &workItem{request: request, evidenceID: evidenceID, pending: slice.PriorInputHash != inputHash || catalog.NeedsRepair}
			item.outcome = assignment.Outcome{Unchanged: !item.pending, Text: slice.Text, Reviewed: candidates, InputHash: inputHash}
			byText[cacheKey] = item
			if item.pending {
				pending = append(pending, item)
			}
		}
		positions = append(positions, item)
	}
	var decisions []assignmentDecisionOutcome
	if len(pending) > 0 && s.decisions != nil && decisionSettings.Effective.TopicAssignment {
		decisions = make([]assignmentDecisionOutcome, 0, len(pending))
		for start := 0; start < len(pending); start += contract.MaxOrganizationSources {
			end := min(start+contract.MaxOrganizationSources, len(pending))
			inputs := make([]assignmentRequest, end-start)
			for i, item := range pending[start:end] {
				inputs[i] = item.request
			}
			batch, err := s.assessDecisionBatch(ctx, scope, inputs, *decisionSettings, accounting)
			if err != nil {
				return nil, err
			}
			decisions = append(decisions, batch...)
		}
	}
	limits := s.limits
	if policy.ContextWindowTokens > 0 {
		limits.MaxInputTokens = min(limits.MaxInputTokens, int(policy.ContextWindowTokens)-limits.MaxOutputTokens)
	}
	assess := func(index int, item *workItem) {
		exchange := observability.ExchangeScopeFromContext(ctx)
		exchange.TeamID, exchange.OperationID, exchange.AssessmentID = scope.TeamID, scope.OperationID, item.request.RequestID
		assessmentCtx := observability.WithExchangeScope(ctx, exchange)
		var outcome assignment.Outcome
		var err error
		var preserved []string
		if decisions != nil {
			outcome = decisions[index].Outcome
			if !decisions[index].Clear {
				for _, match := range outcome.Matches {
					preserved = append(preserved, match.ID)
				}
				outcome, err = s.generate(assessmentCtx, item.request, model, limits, accounting, outcome.Matches, true)
			}
		} else if window != nil {
			outcome, err = s.generate(assessmentCtx, item.request, model, limits, accounting, nil, false)
		} else {
			outcome, err = s.assess(assessmentCtx, item.request, model, policy, decisionSettings, accounting)
		}
		outcome.Text, outcome.Reviewed, outcome.InputHash = item.outcome.Text, item.outcome.Reviewed, item.outcome.InputHash
		outcome.Failure = err
		item.outcome = outcome
		if s.logger != nil {
			if err != nil {
				s.logger.ErrorContext(assessmentCtx, "topic_assignment.generation_failed", "team_id", scope.TeamID,
					"space_id", scope.SpaceID, "space_generation", scope.Generation, "operation_id", scope.OperationID,
					"stage", "assignment", "evidence_id", item.evidenceID, "assessment_id", item.request.RequestID,
					"reason_code", maintenanceDiagnosticError(err), "preserved_decision_match_ids", preserved, "error", err)
			} else {
				s.logger.InfoContext(assessmentCtx, "topic_assignment.prepared", "team_id", scope.TeamID,
					"operation_id", scope.OperationID, "assessment_id", item.request.RequestID, "method", outcome.Method,
					"matched_topics", len(outcome.Matches), "proposed_topics", len(outcome.Proposals))
			}
		}
	}
	var group errgroup.Group
	group.SetLimit(4)
	for i, item := range pending {
		group.Go(func() error { assess(i, item); return nil })
	}
	_ = group.Wait()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if window == nil {
		for _, item := range pending {
			if item.outcome.Failure != nil {
				return nil, item.outcome.Failure
			}
		}
	}
	out := make([]assignment.Outcome, len(slices))
	for i, item := range positions {
		out[i] = item.outcome
		out[i].Index = slices[i].Index
	}
	return out, nil
}

type assignmentRequest struct {
	RequestID string             `json:"request_id"`
	Text      string             `json:"evidence"`
	Topics    []assignment.Topic `json:"topics"`
}

func (s *AssignmentService) assess(ctx context.Context, input assignmentRequest, model string, policy domain.TopicMaintenanceConfig, settings *domain.DecisionRuntimeConfig, accounting assessment.AttemptAccounting) (assignment.Outcome, error) {
	var matched []assignment.Match
	proposalOnly := false
	if s.decisions != nil && settings != nil && settings.Effective.TopicAssignment {
		outcome, clear, err := s.assessDecisions(ctx, input, *settings, accounting)
		if err != nil || clear {
			return outcome, err
		}
		matched = outcome.Matches
		proposalOnly = true
	}
	limits := s.limits
	if policy.ContextWindowTokens > 0 {
		limits.MaxInputTokens = min(limits.MaxInputTokens, int(policy.ContextWindowTokens)-limits.MaxOutputTokens)
	}
	return s.generate(ctx, input, model, limits, accounting, matched, proposalOnly)
}
