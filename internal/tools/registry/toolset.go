package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
	"github.com/markhuangai/dense-mem/internal/service/claimservice"
	"github.com/markhuangai/dense-mem/internal/service/communityservice"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/factservice"
	"github.com/markhuangai/dense-mem/internal/service/fragmentservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
	"github.com/markhuangai/dense-mem/internal/tools/graphquery"
)

// Dependencies is the wiring bundle that BuildDefault uses to construct the
// canonical v1 tool catalog.
type Dependencies struct {
	// Memory retrieval and evaluation export dependencies.
	FragmentList fragmentservice.ListFragmentsService
	Recall       recallservice.RecallService
	Metrics      observability.DiscoverabilityMetrics

	// RecallFeedbackConfig controls whether recall_memory emits deferred
	// recall events and whether session feedback submission is callable.
	RecallFeedbackConfig RecallFeedbackConfigProvider
	RecallFeedbackEvents RecallFeedbackEventRecorder
	EvaluationConfig     EvaluationConfigProvider
	EvaluationAudit      EvaluationAuditAppender

	// Knowledge pipeline tools
	FragmentGet       fragmentservice.GetFragmentService
	ClaimGet          claimservice.GetClaimService
	ClaimList         claimservice.ListClaimsService
	ClaimListFiltered claimservice.ListClaimsFilteredService
	FactGet           factservice.GetFactService
	FactList          factservice.ListFactsService
	CommunityGet      communityservice.GetCommunitySummaryService
	CommunityList     communityservice.ListCommunitiesService
	GraphQuery        graphquery.GraphQueryService
	Context           contextservice.Service
	Memory            memoryservice.Service
	V2Remember        memoryservice.V2RememberService
	V2Recall          memoryservice.V2RecallService
	V2Lifecycle       memoryservice.V2LifecycleService
	V2Evaluation      repository.V2EvaluationRepository
	V2Communities     repository.V2CommunityRepository
	SkillPack         skillpackservice.Service
	Dreams            dreamservice.Service
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

// BuildDefault wires the v1 tool catalog into a new Registry. No global state.
// The caller owns the returned Registry and should wire explicit fallback
// services when a capability is disabled by deployment configuration.
func BuildDefault(deps Dependencies) (Registry, error) {
	r := New()
	for _, t := range defaultTools(deps) {
		if err := r.Register(t); err != nil {
			return nil, fmt.Errorf("registry: BuildDefault: %w", err)
		}
	}
	return r, nil
}

// BuildV2UAT wires the dormant V2 executable test/UAT registry. It is separate
// from BuildDefault so production V1 tool names are not replaced before cutover.
func BuildV2UAT(deps Dependencies) (Registry, error) {
	r := New()
	for _, t := range v2UATTools(deps) {
		if err := r.Register(t); err != nil {
			return nil, fmt.Errorf("registry: BuildV2UAT: %w", err)
		}
	}
	return r, nil
}

func defaultTools(deps Dependencies) []Tool {
	tools := []Tool{
		// server-owned memory tools
		recallMemoryTool(deps),
		traceMemoryTool(deps),
		assembleContextTool(deps),
		rememberTool(deps),
		getMemoryPlacementTool(deps),
		disputeMemoryPlacementTool(deps),
		importMemoriesTool(deps),
		reflectMemoriesTool(deps),
		confirmMemoryTool(deps),
		listDreamsTool(deps),
		getDreamTool(deps),
		resolveDreamFeedbackTool(deps),
		findSkillPackCandidatesTool(deps),
		exportSkillPackTool(deps),
		inspectSkillPackTool(deps),
		importSkillPackTool(deps),
		rollbackSkillPackImportTool(deps),
		submitRecallSessionFeedbackTool(deps),
		evalGetManifestTool(deps),
		evalListKnowledgeRefsTool(deps),
		evalGetKnowledgeItemTool(deps),
		evalListRecallFeedbackEventsTool(deps),
		evalGetRecallFeedbackEventTool(deps),
		evalRunDreamCycleTool(deps),
		evalRunRecallCaseTool(deps),
		evalScoreRetrievalCaseTool(deps),
	}
	return tools
}

// --- recall_memory ---------------------------------------------------------

func recallMemoryTool(deps Dependencies) Tool {
	return Tool{
		Name:        "recall_memory",
		Description: "Hybrid semantic + keyword recall over stored facts, validated claims, and fragments for the caller's profile. Useful for prior user preferences, corrections, project decisions, active goals, reusable instructions, identity/profile facts, and other remembered context. related_dreams are hypotheses, not validated memory. recall_event carries an id for later recall-quality feedback, including dream_feedback when related dreams influenced or contradicted the answer.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query":            schemaString("Natural-language query.", 512),
				"limit":            map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
				"valid_at":         map[string]any{"type": "string", "format": "date-time"},
				"known_at":         map[string]any{"type": "string", "format": "date-time"},
				"include_evidence": map[string]any{"type": "boolean"},
				"use_communities":  map[string]any{"type": "boolean"},
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"results": map[string]any{
					"type":  "array",
					"items": recallHitObjectSchema(),
				},
				"clarifications": clarificationArraySchema(),
				"related_dreams": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"recall_event": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"recall_id":       schemaString("Opaque recall event id to include in deferred session feedback.", 128),
						"feedback_tool":   schemaString("Session feedback submission tool name.", 64),
						"feedback_timing": schemaString("When feedback should be submitted.", 64),
					},
				},
			},
		},
		RequiredScopes: []string{"read"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if deps.Recall == nil {
				return nil, ErrToolUnavailable
			}
			q, _ := input["query"].(string)
			var req recallservice.RecallRequest
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("recall_memory: invalid input: %w", err)
			}
			req.Query = q
			hits, err := deps.Recall.Recall(ctx, profileID, req)
			if err != nil {
				return nil, err
			}
			results := make([]map[string]any, 0, len(hits))
			for i := range hits {
				m, err := recallHitToMap(hits[i])
				if err != nil {
					return nil, err
				}
				results = append(results, m)
			}
			out := map[string]any{"results": results, "clarifications": []any{}, "related_dreams": []any{}}
			var relatedDreams []*domain.Dream
			if deps.Dreams != nil {
				dreams, err := deps.Dreams.Recall(ctx, profileID, req.Query, 5)
				if err == nil {
					relatedDreams = dreams
					out["related_dreams"] = dreams
				} else {
					slog.Default().Warn("related dreams not fetched",
						slog.String("error", err.Error()),
					)
				}
			}
			if RecallFeedbackEnabled(ctx, deps.RecallFeedbackConfig) && deps.Metrics != nil {
				recallID := "rec_" + uuid.NewString()
				recallEvent := map[string]any{
					"recall_id":       recallID,
					"feedback_tool":   SubmitRecallSessionFeedbackToolName,
					"feedback_timing": "deferred_until_final_answer",
				}
				if deps.RecallFeedbackEvents != nil {
					err := deps.RecallFeedbackEvents.RecordRecallSnapshot(ctx, domain.RecallFeedbackEvent{
						RecallID:   recallID,
						ToolName:   "recall_memory",
						Query:      req.Query,
						ToolArgs:   recallFeedbackToolArgs(input, req),
						ResultRefs: recallFeedbackResultRefs(hits, relatedDreams),
					})
					if err != nil {
						slog.Default().Warn("recall feedback snapshot not recorded",
							slog.String("recall_id", recallID),
							slog.String("error", err.Error()),
						)
					} else {
						out["recall_event"] = recallEvent
					}
				} else {
					out["recall_event"] = recallEvent
				}
			}
			if deps.Memory != nil {
				reflection, err := deps.Memory.Reflect(ctx, profileID, memoryservice.ReflectRequest{Limit: 20})
				if err != nil {
					return nil, err
				}
				out["clarifications"] = reflection.Clarifications
			}
			return out, nil
		},
	}
}

const (
	recallFeedbackCommentMaxLength     = 1000
	recallFeedbackIrrelevantRefsMax    = 20
	recallFeedbackJudgedRefIDMaxLength = 128
	recallFeedbackJudgedRefMaxRank     = 50
)

type recallFeedbackJudgedResultRefInput struct {
	Type string `json:"type"`
	ID   string `json:"id"`
	Rank *int   `json:"rank,omitempty"`
}

type recallFeedbackDreamFeedbackInput struct {
	DreamID         string `json:"dream_id"`
	Used            bool   `json:"used"`
	Quality         string `json:"quality"`
	Contradicted    bool   `json:"contradicted"`
	FeedbackComment string `json:"feedback_comment"`
}

type recallFeedbackHypothesisFeedbackInput struct {
	HypothesisID    string `json:"hypothesis_id"`
	Used            bool   `json:"used"`
	Quality         string `json:"quality"`
	Contradicted    bool   `json:"contradicted"`
	FeedbackComment string `json:"feedback_comment"`
}

func submitRecallSessionFeedbackTool(deps Dependencies) Tool {
	return Tool{
		Name:        SubmitRecallSessionFeedbackToolName,
		Description: "Records session-level recall quality feedback for recall_memory events. Accepts recall_event.recall_id values, whether each recall was used, answer support, quality, missing or irrelevant context flags, optional feedback_comment, and dream_feedback for related dream hypotheses that were useful, weak, or contradicted.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"recalls"},
			"properties": map[string]any{
				"recalls": map[string]any{
					"type":     "array",
					"minItems": 1,
					"maxItems": 20,
					"items": map[string]any{
						"type": "object",
						"required": []string{
							"recall_id",
							"used",
							"answer_supported",
							"quality",
							"missing_context",
							"irrelevant",
						},
						"properties": map[string]any{
							"recall_id":        schemaString("Opaque recall id from recall_memory.recall_event.recall_id.", 128),
							"used":             map[string]any{"type": "boolean", "description": "Whether this recall informed the final answer."},
							"answer_supported": map[string]any{"type": "boolean", "description": "Whether this recall's returned context supported the final answer."},
							"quality":          schemaEnum([]string{"high", "medium", "low"}),
							"missing_context":  map[string]any{"type": "boolean", "description": "Whether important memory context still appeared missing after investigation."},
							"irrelevant":       map[string]any{"type": "boolean", "description": "Whether this recall's returned context was irrelevant."},
							"feedback_comment": schemaString("Required unless quality is high, answer_supported is true, missing_context is false, and irrelevant is false. Explain the recall quality without including user content.", recallFeedbackCommentMaxLength),
							"failure_reason": map[string]any{
								"type":        "string",
								"maxLength":   recallFeedbackCommentMaxLength,
								"deprecated":  true,
								"description": "Deprecated alias for feedback_comment. Use feedback_comment.",
							},
							"expected_context": schemaString("Deprecated alias for feedback_comment. Use feedback_comment.", recallFeedbackCommentMaxLength),
							"irrelevant_result_refs": map[string]any{
								"type":        "array",
								"description": "Optional references to returned results judged irrelevant.",
								"maxItems":    recallFeedbackIrrelevantRefsMax,
								"items": map[string]any{
									"type":     "object",
									"required": []string{"type", "id"},
									"properties": map[string]any{
										"type": schemaEnum(recallFeedbackResultTypes()),
										"id":   schemaString("Result id from recall feedback result_refs.", recallFeedbackJudgedRefIDMaxLength),
										"rank": map[string]any{"type": "integer", "minimum": 1, "maximum": recallFeedbackJudgedRefMaxRank},
									},
									"additionalProperties": false,
								},
							},
							"dream_feedback": map[string]any{
								"type":        "array",
								"description": "Deprecated alias for hypothesis_feedback. This records quality only; it does not change hypothesis status.",
								"maxItems":    recallFeedbackIrrelevantRefsMax,
								"items": map[string]any{
									"type":     "object",
									"required": []string{"dream_id", "used", "quality", "contradicted"},
									"properties": map[string]any{
										"dream_id":         schemaString("Dream id from recall_memory.related_dreams.", recallFeedbackJudgedRefIDMaxLength),
										"used":             map[string]any{"type": "boolean"},
										"quality":          schemaEnum([]string{"high", "medium", "low"}),
										"contradicted":     map[string]any{"type": "boolean"},
										"feedback_comment": schemaString("Required unless quality is high and contradicted is false.", recallFeedbackCommentMaxLength),
									},
									"additionalProperties": false,
								},
							},
							"hypothesis_feedback": map[string]any{
								"type":        "array",
								"description": "Optional host-LLM judgments about related hypotheses returned with recall_memory. This records quality only; it does not change hypothesis status.",
								"maxItems":    recallFeedbackIrrelevantRefsMax,
								"items": map[string]any{
									"type":     "object",
									"required": []string{"hypothesis_id", "used", "quality", "contradicted"},
									"properties": map[string]any{
										"hypothesis_id":    schemaString("Hypothesis id from recall_memory related hypotheses.", recallFeedbackJudgedRefIDMaxLength),
										"used":             map[string]any{"type": "boolean"},
										"quality":          schemaEnum([]string{"high", "medium", "low"}),
										"contradicted":     map[string]any{"type": "boolean"},
										"feedback_comment": schemaString("Required unless quality is high and contradicted is false.", recallFeedbackCommentMaxLength),
									},
									"additionalProperties": false,
								},
							},
						},
						"additionalProperties": false,
					},
				},
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"recorded":       map[string]any{"type": "boolean"},
				"recorded_count": map[string]any{"type": "integer"},
			},
		},
		RequiredScopes: []string{"write"},
		Invoke: func(ctx context.Context, profileID string, input map[string]any) (map[string]any, error) {
			if !RecallFeedbackEnabled(ctx, deps.RecallFeedbackConfig) {
				return nil, ErrToolDisabled
			}
			if deps.Metrics == nil {
				return nil, ErrToolUnavailable
			}
			var req struct {
				Recalls []struct {
					RecallID           string                                  `json:"recall_id"`
					Used               bool                                    `json:"used"`
					AnswerSupported    bool                                    `json:"answer_supported"`
					Quality            string                                  `json:"quality"`
					MissingContext     bool                                    `json:"missing_context"`
					Irrelevant         bool                                    `json:"irrelevant"`
					FeedbackComment    string                                  `json:"feedback_comment"`
					FailureReason      string                                  `json:"failure_reason"`
					ExpectedContext    string                                  `json:"expected_context"`
					IrrelevantRefs     []recallFeedbackJudgedResultRefInput    `json:"irrelevant_result_refs"`
					DreamFeedback      []recallFeedbackDreamFeedbackInput      `json:"dream_feedback"`
					HypothesisFeedback []recallFeedbackHypothesisFeedbackInput `json:"hypothesis_feedback"`
				} `json:"recalls"`
			}
			if err := remapInput(input, &req); err != nil {
				return nil, fmt.Errorf("submit_recall_session_feedback: invalid input: %w", err)
			}
			if len(req.Recalls) == 0 {
				return nil, errors.New("submit_recall_session_feedback: recalls is required")
			}
			submissions := make([]domain.RecallFeedbackSubmission, 0, len(req.Recalls))
			feedbacks := make([]observability.RecallFeedback, 0, len(req.Recalls))
			for i, recall := range req.Recalls {
				if strings.TrimSpace(recall.RecallID) == "" {
					return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].recall_id is required", i)
				}
				quality := strings.ToLower(strings.TrimSpace(recall.Quality))
				switch quality {
				case "high", "medium", "low":
				default:
					return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].quality must be one of high, medium, low", i)
				}
				feedbackComment := recallFeedbackComment(recall.FeedbackComment, recall.ExpectedContext, recall.FailureReason)
				if recallFeedbackNeedsComment(quality, recall.AnswerSupported, recall.MissingContext, recall.Irrelevant) && feedbackComment == "" {
					return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].feedback_comment is required unless quality is high with no negative flags", i)
				}
				if runeLen(feedbackComment) > recallFeedbackCommentMaxLength {
					return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].feedback_comment must be at most %d characters", i, recallFeedbackCommentMaxLength)
				}
				irrelevantRefs, err := normalizeRecallFeedbackJudgedRefs(i, recall.IrrelevantRefs)
				if err != nil {
					return nil, err
				}
				dreamFeedback, err := normalizeRecallFeedbackDreamFeedback(i, recall.DreamFeedback, recall.HypothesisFeedback)
				if err != nil {
					return nil, err
				}
				submissions = append(submissions, domain.RecallFeedbackSubmission{
					RecallID:        strings.TrimSpace(recall.RecallID),
					Used:            recall.Used,
					AnswerSupported: recall.AnswerSupported,
					Quality:         quality,
					MissingContext:  recall.MissingContext,
					Irrelevant:      recall.Irrelevant,
					FeedbackComment: feedbackComment,
					IrrelevantRefs:  irrelevantRefs,
					DreamFeedback:   dreamFeedback,
				})
				feedbacks = append(feedbacks, observability.RecallFeedback{
					Used:            recall.Used,
					AnswerSupported: recall.AnswerSupported,
					Quality:         quality,
					MissingContext:  recall.MissingContext,
					Irrelevant:      recall.Irrelevant,
				})
			}
			if deps.RecallFeedbackEvents != nil {
				for i, submission := range submissions {
					if err := deps.RecallFeedbackEvents.RecordRecallFeedback(ctx, submission); err != nil {
						return nil, fmt.Errorf("submit_recall_session_feedback: failed to record recalls[%d]: %w", i, err)
					}
				}
			}
			for _, feedback := range feedbacks {
				observability.RecordRecallFeedback(ctx, deps.Metrics, feedback)
			}
			return map[string]any{"recorded": true, "recorded_count": len(submissions)}, nil
		},
	}
}

func recallFeedbackComment(feedbackComment string, legacyExpectedContext string, legacyFailureReason string) string {
	for _, candidate := range []string{feedbackComment, legacyExpectedContext, legacyFailureReason} {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func recallFeedbackNeedsComment(quality string, answerSupported bool, missingContext bool, irrelevant bool) bool {
	return quality != "high" || !answerSupported || missingContext || irrelevant
}

func recallFeedbackResultTypes() []string {
	return []string{
		domain.RecallFeedbackResultTypeFragment,
		domain.RecallFeedbackResultTypeClaim,
		domain.RecallFeedbackResultTypeFact,
		domain.RecallFeedbackResultTypeDream,
		domain.RecallFeedbackResultTypeEvidence,
		domain.RecallFeedbackResultTypeRelationship,
		domain.RecallFeedbackResultTypeEntity,
		domain.RecallFeedbackResultTypeValue,
		domain.RecallFeedbackResultTypeCommunity,
		domain.RecallFeedbackResultTypeHypothesis,
	}
}

func normalizeRecallFeedbackJudgedRefs(recallIndex int, refs []recallFeedbackJudgedResultRefInput) ([]domain.RecallFeedbackJudgedResultRef, error) {
	if len(refs) > recallFeedbackIrrelevantRefsMax {
		return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].irrelevant_result_refs must contain at most %d items", recallIndex, recallFeedbackIrrelevantRefsMax)
	}
	out := make([]domain.RecallFeedbackJudgedResultRef, 0, len(refs))
	seen := map[string]struct{}{}
	for i, ref := range refs {
		refType := strings.TrimSpace(ref.Type)
		switch refType {
		case domain.RecallFeedbackResultTypeFragment,
			domain.RecallFeedbackResultTypeClaim,
			domain.RecallFeedbackResultTypeFact,
			domain.RecallFeedbackResultTypeDream,
			domain.RecallFeedbackResultTypeEvidence,
			domain.RecallFeedbackResultTypeRelationship,
			domain.RecallFeedbackResultTypeEntity,
			domain.RecallFeedbackResultTypeValue,
			domain.RecallFeedbackResultTypeCommunity,
			domain.RecallFeedbackResultTypeHypothesis:
		default:
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].irrelevant_result_refs[%d].type must be one of %s", recallIndex, i, strings.Join(recallFeedbackResultTypes(), ", "))
		}
		id := strings.TrimSpace(ref.ID)
		if id == "" {
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].irrelevant_result_refs[%d].id is required", recallIndex, i)
		}
		if runeLen(id) > recallFeedbackJudgedRefIDMaxLength {
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].irrelevant_result_refs[%d].id must be at most %d characters", recallIndex, i, recallFeedbackJudgedRefIDMaxLength)
		}
		judged := domain.RecallFeedbackJudgedResultRef{Type: refType, ID: id}
		key := refType + "\x00" + id
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].irrelevant_result_refs must not contain duplicates", recallIndex)
		}
		seen[key] = struct{}{}
		if ref.Rank != nil {
			if *ref.Rank < 1 || *ref.Rank > recallFeedbackJudgedRefMaxRank {
				return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].irrelevant_result_refs[%d].rank must be between 1 and %d", recallIndex, i, recallFeedbackJudgedRefMaxRank)
			}
			judged.Rank = *ref.Rank
		}
		out = append(out, judged)
	}
	if out == nil {
		return []domain.RecallFeedbackJudgedResultRef{}, nil
	}
	return out, nil
}

func normalizeRecallFeedbackDreamFeedback(
	recallIndex int,
	legacyDreams []recallFeedbackDreamFeedbackInput,
	hypotheses []recallFeedbackHypothesisFeedbackInput,
) ([]domain.RecallFeedbackDreamFeedback, error) {
	if len(legacyDreams)+len(hypotheses) > recallFeedbackIrrelevantRefsMax {
		return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].dream_feedback must contain at most %d items", recallIndex, recallFeedbackIrrelevantRefsMax)
	}
	out := make([]domain.RecallFeedbackDreamFeedback, 0, len(legacyDreams)+len(hypotheses))
	seen := map[string]struct{}{}
	for i, item := range legacyDreams {
		normalized, err := normalizeRecallFeedbackHypothesisJudgment(recallIndex, i, "dream_feedback", strings.TrimSpace(item.DreamID), item.Used, item.Quality, item.Contradicted, item.FeedbackComment)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized.DreamID]; ok {
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].dream_feedback must not contain duplicates", recallIndex)
		}
		seen[normalized.DreamID] = struct{}{}
		out = append(out, normalized)
	}
	for i, item := range hypotheses {
		normalized, err := normalizeRecallFeedbackHypothesisJudgment(recallIndex, i, "hypothesis_feedback", strings.TrimSpace(item.HypothesisID), item.Used, item.Quality, item.Contradicted, item.FeedbackComment)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized.DreamID]; ok {
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].hypothesis_feedback must not contain duplicates", recallIndex)
		}
		seen[normalized.DreamID] = struct{}{}
		out = append(out, normalized)
	}
	if out == nil {
		return []domain.RecallFeedbackDreamFeedback{}, nil
	}
	return out, nil
}

func normalizeRecallFeedbackHypothesisJudgment(
	recallIndex int,
	feedbackIndex int,
	field string,
	hypothesisID string,
	used bool,
	qualityRaw string,
	contradicted bool,
	feedbackComment string,
) (domain.RecallFeedbackDreamFeedback, error) {
	hypothesisID = strings.TrimSpace(hypothesisID)
	if hypothesisID == "" {
		return domain.RecallFeedbackDreamFeedback{}, fmt.Errorf("submit_recall_session_feedback: recalls[%d].%s[%d].hypothesis_id is required", recallIndex, field, feedbackIndex)
	}
	if runeLen(hypothesisID) > recallFeedbackJudgedRefIDMaxLength {
		return domain.RecallFeedbackDreamFeedback{}, fmt.Errorf("submit_recall_session_feedback: recalls[%d].%s[%d].hypothesis_id must be at most %d characters", recallIndex, field, feedbackIndex, recallFeedbackJudgedRefIDMaxLength)
	}
	quality := strings.ToLower(strings.TrimSpace(qualityRaw))
	switch quality {
	case "high", "medium", "low":
	default:
		return domain.RecallFeedbackDreamFeedback{}, fmt.Errorf("submit_recall_session_feedback: recalls[%d].%s[%d].quality must be one of high, medium, low", recallIndex, field, feedbackIndex)
	}
	comment := strings.TrimSpace(feedbackComment)
	if (quality != "high" || contradicted) && comment == "" {
		return domain.RecallFeedbackDreamFeedback{}, fmt.Errorf("submit_recall_session_feedback: recalls[%d].%s[%d].feedback_comment is required unless quality is high and contradicted is false", recallIndex, field, feedbackIndex)
	}
	if runeLen(comment) > recallFeedbackCommentMaxLength {
		return domain.RecallFeedbackDreamFeedback{}, fmt.Errorf("submit_recall_session_feedback: recalls[%d].%s[%d].feedback_comment must be at most %d characters", recallIndex, field, feedbackIndex, recallFeedbackCommentMaxLength)
	}
	return domain.RecallFeedbackDreamFeedback{
		DreamID:         hypothesisID,
		Used:            used,
		Quality:         quality,
		Contradicted:    contradicted,
		FeedbackComment: comment,
	}, nil
}

func runeLen(value string) int {
	return len([]rune(value))
}

func recallFeedbackToolArgs(input map[string]any, req recallservice.RecallRequest) map[string]any {
	effective := map[string]any{
		"query":            req.Query,
		"limit":            req.Limit,
		"include_evidence": req.IncludeEvidence,
		"use_communities":  req.UseCommunities,
	}
	if req.ValidAt != nil {
		effective["valid_at"] = req.ValidAt.UTC().Format(time.RFC3339Nano)
	}
	if req.KnownAt != nil {
		effective["known_at"] = req.KnownAt.UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{
		"input":     recallFeedbackInputCopy(input),
		"effective": effective,
	}
}

func recallFeedbackInputCopy(input map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"query", "limit", "valid_at", "known_at", "include_evidence", "use_communities"} {
		if value, ok := input[key]; ok {
			out[key] = value
		}
	}
	return out
}

func recallFeedbackResultRefs(hits []recallservice.RecallHit, dreams []*domain.Dream) []domain.RecallFeedbackResultRef {
	refs := make([]domain.RecallFeedbackResultRef, 0, len(hits)+len(dreams))
	for i := range hits {
		hit := hits[i]
		ref := domain.RecallFeedbackResultRef{
			Rank:         i + 1,
			Tier:         hit.Tier,
			SemanticRank: hit.SemanticRank,
			KeywordRank:  hit.KeywordRank,
			Score:        floatPtrIfNonZero(hit.Score),
			FinalScore:   floatPtrIfNonZero(hit.FinalScore),
		}
		switch {
		case hit.Fact != nil:
			ref.Type = domain.RecallFeedbackResultTypeFact
			ref.ID = hit.Fact.FactID
			ref.StatusAtRecall = string(hit.Fact.Status)
			ref.RecordedAt = timePtrIfNonZero(hit.Fact.RecordedAt)
			ref.ValidFrom = hit.Fact.ValidFrom
			ref.ValidTo = hit.Fact.ValidTo
			ref.RetractedAt = hit.Fact.RetractedAt
		case hit.Claim != nil:
			ref.Type = domain.RecallFeedbackResultTypeClaim
			ref.ID = hit.Claim.ClaimID
			ref.StatusAtRecall = string(hit.Claim.Status)
			ref.RecordedAt = timePtrIfNonZero(hit.Claim.RecordedAt)
			ref.ValidFrom = hit.Claim.ValidFrom
			ref.ValidTo = hit.Claim.ValidTo
		case hit.Fragment != nil:
			ref.Type = domain.RecallFeedbackResultTypeFragment
			ref.ID = hit.Fragment.FragmentID
			ref.StatusAtRecall = string(hit.Fragment.Status)
			if ref.StatusAtRecall == "" {
				ref.StatusAtRecall = string(domain.FragmentStatusActive)
			}
			ref.CreatedAt = timePtrIfNonZero(hit.Fragment.CreatedAt)
			ref.UpdatedAt = timePtrIfNonZero(hit.Fragment.UpdatedAt)
			ref.RetractedAt = hit.Fragment.RetractedAt
		}
		if ref.Type != "" && ref.ID != "" {
			refs = append(refs, ref)
		}
	}
	for i, dream := range dreams {
		if dream == nil || strings.TrimSpace(dream.DreamID) == "" {
			continue
		}
		score := dream.Likelihood
		refs = append(refs, domain.RecallFeedbackResultRef{
			Type:           domain.RecallFeedbackResultTypeDream,
			ID:             dream.DreamID,
			Rank:           i + 1,
			Tier:           "dream",
			Score:          floatPtrIfNonZero(score),
			StatusAtRecall: string(dream.Status),
			CreatedAt:      timePtrIfNonZero(dream.CreatedAt),
			UpdatedAt:      timePtrIfNonZero(dream.UpdatedAt),
		})
	}
	return refs
}

func floatPtrIfNonZero(value float64) *float64 {
	if value == 0 {
		return nil
	}
	return &value
}

func timePtrIfNonZero(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}

func recallHitToMap(hit recallservice.RecallHit) (map[string]any, error) {
	tier := hit.Tier
	out := map[string]any{
		"semantic_rank": hit.SemanticRank,
		"keyword_rank":  hit.KeywordRank,
		"final_score":   hit.FinalScore,
	}
	if hit.Score != 0 {
		out["score"] = hit.Score
	}

	if hit.Fragment != nil {
		if tier == "" {
			tier = recallservice.TierFragment
		}
		fragment, err := structToMap(hit.Fragment)
		if err != nil {
			return nil, err
		}
		for key, value := range fragment {
			out[key] = value
		}
		out["fragment"] = fragment
		if _, ok := out["score"]; !ok && hit.FinalScore != 0 {
			out["score"] = hit.FinalScore
		}
	}
	if hit.Claim != nil {
		if tier == "" {
			tier = recallservice.TierValidatedClaim
		}
		claim, err := structToMap(response.ToClaimResponse(hit.Claim, ""))
		if err != nil {
			return nil, err
		}
		out["claim"] = claim
	}
	if hit.Fact != nil {
		if tier == "" {
			tier = recallservice.TierActiveFact
		}
		fact, err := structToMap(hit.Fact)
		if err != nil {
			return nil, err
		}
		out["fact"] = fact
	}
	if tier == "" {
		return nil, errors.New("recall_memory: hit missing payload")
	}
	out["tier"] = tier
	return out, nil
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

func recallHitObjectSchema() map[string]any {
	fragment := fragmentObjectSchema()
	properties := map[string]any{}
	if fragmentProperties, ok := fragment["properties"].(map[string]any); ok {
		for key, value := range fragmentProperties {
			properties[key] = value
		}
	}
	properties["tier"] = map[string]any{"type": "string", "description": "1 = active Fact, 1.5 = validated Claim, 2 = SourceFragment."}
	properties["score"] = map[string]any{"type": "number", "description": "Tier-specific relevance or confidence score."}
	properties["fragment"] = fragmentObjectSchema()
	properties["claim"] = claimObjectSchema()
	properties["fact"] = factObjectSchema()
	properties["semantic_rank"] = map[string]any{"type": "integer", "description": "1-based rank from semantic branch; 0 if absent."}
	properties["keyword_rank"] = map[string]any{"type": "integer", "description": "1-based rank from keyword branch; 0 if absent."}
	properties["final_score"] = map[string]any{"type": "number", "description": "Reciprocal Rank Fusion score for fragment hits."}

	return map[string]any{
		"type":       "object",
		"properties": properties,
	}
}

// fragmentObjectSchema mirrors dto.FragmentResponse. Kept as a hand-built
// map[string]any to avoid reflection (plan constraint).
func fragmentObjectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":                   map[string]any{"type": "string"},
			"content":              map[string]any{"type": "string"},
			"source_type":          map[string]any{"type": "string"},
			"source":               map[string]any{"type": "string"},
			"authority":            map[string]any{"type": "string"},
			"labels":               map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"metadata":             map[string]any{"type": "object"},
			"content_hash":         map[string]any{"type": "string"},
			"idempotency_key":      map[string]any{"type": "string"},
			"embedding_model":      map[string]any{"type": "string"},
			"embedding_dimensions": map[string]any{"type": "integer"},
			"created_at":           map[string]any{"type": "string", "format": "date-time"},
			"updated_at":           map[string]any{"type": "string", "format": "date-time"},
		},
	}
}

func evidenceObjectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"fragment_id":        map[string]any{"type": "string"},
			"speaker":            map[string]any{"type": "string"},
			"span_start":         map[string]any{"type": "integer"},
			"span_end":           map[string]any{"type": "integer"},
			"extract_conf":       map[string]any{"type": "number"},
			"extraction_model":   map[string]any{"type": "string"},
			"extraction_version": map[string]any{"type": "string"},
			"pipeline_run_id":    map[string]any{"type": "string"},
			"authority":          map[string]any{"type": "string"},
		},
	}
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
