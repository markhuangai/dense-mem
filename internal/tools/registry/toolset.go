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
	"github.com/markhuangai/dense-mem/internal/observability"
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
	SkillPack         skillpackservice.Service
	Dreams            dreamservice.Service
	Hypotheses        HypothesisRecallService
}

type HypothesisRecallService interface {
	RecallHypotheses(ctx context.Context, profileID, query string, limit int) ([]*domain.Dream, error)
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

func defaultTools(deps Dependencies) []Tool {
	tools := []Tool{
		// server-owned memory tools
		recallMemoryTool(deps),
		traceMemoryTool(deps),
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
	}
	return tools
}

// --- recall_memory ---------------------------------------------------------

func recallMemoryTool(deps Dependencies) Tool {
	return Tool{
		Name:        "recall_memory",
		Description: "Retrieve bounded evidence contexts for the caller's team, plus compact relationship paths for follow-up discovery. Useful for prior user preferences, corrections, project decisions, active goals, reusable instructions, identity/profile facts, and other remembered context. Follow discovery_guidance when the first bounded response leaves uncertainty. related_hypotheses are server-controlled hypotheses, not validated memory. recall_id can be used for later recall-quality feedback.",
		InputSchema: map[string]any{
			"type":     "object",
			"required": []string{"query"},
			"properties": map[string]any{
				"query":    schemaString("Natural-language query.", 512),
				"limit":    map[string]any{"type": "integer", "minimum": 1, "maximum": 50},
				"valid_at": map[string]any{"type": "string", "format": "date-time"},
				"known_at": map[string]any{"type": "string", "format": "date-time"},
				"known_evidence_ids": map[string]any{
					"type":        "array",
					"description": "Previously seen evidence IDs to suppress from returned results.",
					"maxItems":    recallservice.MaxKnownEvidenceIDs,
					"uniqueItems": true,
					"items":       map[string]any{"type": "string", "format": "uuid"},
				},
				"expand_from_entity_ids": map[string]any{
					"type":        "array",
					"description": "Entity IDs to use as explicit graph expansion anchors for a focused follow-up.",
					"maxItems":    recallservice.MaxExpandFromEntityIDs,
					"uniqueItems": true,
					"items":       map[string]any{"type": "string", "format": "uuid"},
				},
				"known_relationship_ids": map[string]any{
					"type":        "array",
					"description": "Previously seen relationship IDs to use as traversal context while suppressing them from returned results.",
					"maxItems":    recallservice.MaxKnownRelationshipIDs,
					"uniqueItems": true,
					"items":       map[string]any{"type": "string", "format": "uuid"},
				},
			},
			"additionalProperties": false,
		},
		OutputSchema:   publicRecallOutputSchema(),
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
			publicResponse, err := recallservice.RenderPublicRecall(req, hits)
			if err != nil {
				return nil, err
			}
			out, err := structToMap(publicResponse)
			if err != nil {
				return nil, err
			}
			recallID := "rec_" + uuid.NewString()
			out["recall_id"] = recallID
			out["related_hypotheses"] = []any{}
			var relatedHypotheses []*domain.Dream
			if deps.Dreams != nil && teamDreamingEnabled(ctx, deps.Dreams, profileID) {
				dreams, err := recallRelatedHypotheses(ctx, deps, profileID, req.Query, 5)
				if err == nil {
					relatedHypotheses = dreams
					normalizedDreams, err := structSliceToAny(dreams)
					if err != nil {
						return nil, err
					}
					out["related_hypotheses"] = normalizedDreams
				} else {
					slog.Default().Warn("related hypotheses not fetched",
						slog.String("error", err.Error()),
					)
				}
			}
			if RecallFeedbackEnabled(ctx, deps.RecallFeedbackConfig) && deps.Metrics != nil {
				if deps.RecallFeedbackEvents != nil {
					err := deps.RecallFeedbackEvents.RecordRecallSnapshot(ctx, domain.RecallFeedbackEvent{
						RecallID:   recallID,
						ToolName:   "recall_memory",
						Query:      req.Query,
						ToolArgs:   recallFeedbackToolArgs(input, req),
						ResultRefs: recallFeedbackResultRefs(hits, relatedHypotheses),
					})
					if err != nil {
						slog.Default().Warn("recall feedback snapshot not recorded",
							slog.String("recall_id", recallID),
							slog.String("error", err.Error()),
						)
					}
				}
			}
			return out, nil
		},
	}
}

func recallRelatedHypotheses(ctx context.Context, deps Dependencies, profileID, query string, limit int) ([]*domain.Dream, error) {
	if deps.Hypotheses != nil {
		return deps.Hypotheses.RecallHypotheses(ctx, profileID, query, limit)
	}
	return deps.Dreams.Recall(ctx, profileID, query, limit)
}

const (
	recallFeedbackCommentMaxLength     = 1000
	recallFeedbackIrrelevantRefsMax    = 20
	recallFeedbackJudgedRefIDMaxLength = 128
	recallFeedbackJudgedRefMaxRank     = 50
)

func teamDreamingEnabled(ctx context.Context, dreams dreamservice.Service, profileID string) bool {
	cfg, err := dreams.EffectiveConfig(ctx, profileID)
	if err != nil {
		slog.Default().Warn("dreaming config not fetched", slog.String("error", err.Error()))
		return false
	}
	return cfg.Enabled && cfg.DreamEnabled
}

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

func submitRecallSessionFeedbackTool(deps Dependencies) Tool {
	return Tool{
		Name:        SubmitRecallSessionFeedbackToolName,
		Description: "Records session-level recall quality feedback for recall_memory events. Accepts recall_memory recall_id values, whether each recall was used, answer support, quality, missing or irrelevant context flags, optional feedback_comment, and dream_feedback for related dream hypotheses that were useful, weak, or contradicted.",
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
							"recall_id":        schemaString("Opaque recall id from recall_memory.recall_id.", 128),
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
										"type": schemaEnum([]string{
											domain.RecallFeedbackResultTypeFragment,
											domain.RecallFeedbackResultTypeClaim,
											domain.RecallFeedbackResultTypeFact,
											domain.RecallFeedbackResultTypeDream,
										}),
										"id":   schemaString("Result id from recall feedback result_refs.", recallFeedbackJudgedRefIDMaxLength),
										"rank": map[string]any{"type": "integer", "minimum": 1, "maximum": recallFeedbackJudgedRefMaxRank},
									},
									"additionalProperties": false,
								},
							},
							"dream_feedback": map[string]any{
								"type":        "array",
								"description": "Optional host-LLM judgments about related dream hypotheses returned with recall_memory. This records quality only; it does not change dream status.",
								"maxItems":    recallFeedbackIrrelevantRefsMax,
								"items": map[string]any{
									"type":     "object",
									"required": []string{"dream_id", "used", "quality", "contradicted"},
									"properties": map[string]any{
										"dream_id":         schemaString("Dream id from recall_memory.related_hypotheses.", recallFeedbackJudgedRefIDMaxLength),
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
					RecallID        string                               `json:"recall_id"`
					Used            bool                                 `json:"used"`
					AnswerSupported bool                                 `json:"answer_supported"`
					Quality         string                               `json:"quality"`
					MissingContext  bool                                 `json:"missing_context"`
					Irrelevant      bool                                 `json:"irrelevant"`
					FeedbackComment string                               `json:"feedback_comment"`
					FailureReason   string                               `json:"failure_reason"`
					ExpectedContext string                               `json:"expected_context"`
					IrrelevantRefs  []recallFeedbackJudgedResultRefInput `json:"irrelevant_result_refs"`
					DreamFeedback   []recallFeedbackDreamFeedbackInput   `json:"dream_feedback"`
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
				dreamFeedback, err := normalizeRecallFeedbackDreamFeedback(i, recall.DreamFeedback)
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

func normalizeRecallFeedbackJudgedRefs(recallIndex int, refs []recallFeedbackJudgedResultRefInput) ([]domain.RecallFeedbackJudgedResultRef, error) {
	if len(refs) > recallFeedbackIrrelevantRefsMax {
		return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].irrelevant_result_refs must contain at most %d items", recallIndex, recallFeedbackIrrelevantRefsMax)
	}
	out := make([]domain.RecallFeedbackJudgedResultRef, 0, len(refs))
	for i, ref := range refs {
		refType := strings.TrimSpace(ref.Type)
		switch refType {
		case domain.RecallFeedbackResultTypeFragment, domain.RecallFeedbackResultTypeClaim, domain.RecallFeedbackResultTypeFact, domain.RecallFeedbackResultTypeDream,
			domain.RecallFeedbackResultTypeRelationship, domain.RecallFeedbackResultTypeEvidence:
		default:
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].irrelevant_result_refs[%d].type must be one of fragment, claim, fact, dream, relationship, evidence", recallIndex, i)
		}
		id := strings.TrimSpace(ref.ID)
		if id == "" {
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].irrelevant_result_refs[%d].id is required", recallIndex, i)
		}
		if runeLen(id) > recallFeedbackJudgedRefIDMaxLength {
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].irrelevant_result_refs[%d].id must be at most %d characters", recallIndex, i, recallFeedbackJudgedRefIDMaxLength)
		}
		judged := domain.RecallFeedbackJudgedResultRef{Type: refType, ID: id}
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

func normalizeRecallFeedbackDreamFeedback(recallIndex int, feedback []recallFeedbackDreamFeedbackInput) ([]domain.RecallFeedbackDreamFeedback, error) {
	if len(feedback) > recallFeedbackIrrelevantRefsMax {
		return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].dream_feedback must contain at most %d items", recallIndex, recallFeedbackIrrelevantRefsMax)
	}
	out := make([]domain.RecallFeedbackDreamFeedback, 0, len(feedback))
	for i, item := range feedback {
		dreamID := strings.TrimSpace(item.DreamID)
		if dreamID == "" {
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].dream_feedback[%d].dream_id is required", recallIndex, i)
		}
		if runeLen(dreamID) > recallFeedbackJudgedRefIDMaxLength {
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].dream_feedback[%d].dream_id must be at most %d characters", recallIndex, i, recallFeedbackJudgedRefIDMaxLength)
		}
		quality := strings.ToLower(strings.TrimSpace(item.Quality))
		switch quality {
		case "high", "medium", "low":
		default:
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].dream_feedback[%d].quality must be one of high, medium, low", recallIndex, i)
		}
		comment := strings.TrimSpace(item.FeedbackComment)
		if (quality != "high" || item.Contradicted) && comment == "" {
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].dream_feedback[%d].feedback_comment is required unless quality is high and contradicted is false", recallIndex, i)
		}
		if runeLen(comment) > recallFeedbackCommentMaxLength {
			return nil, fmt.Errorf("submit_recall_session_feedback: recalls[%d].dream_feedback[%d].feedback_comment must be at most %d characters", recallIndex, i, recallFeedbackCommentMaxLength)
		}
		out = append(out, domain.RecallFeedbackDreamFeedback{
			DreamID:         dreamID,
			Used:            item.Used,
			Quality:         quality,
			Contradicted:    item.Contradicted,
			FeedbackComment: comment,
		})
	}
	if out == nil {
		return []domain.RecallFeedbackDreamFeedback{}, nil
	}
	return out, nil
}

func runeLen(value string) int {
	return len([]rune(value))
}

func recallFeedbackToolArgs(input map[string]any, req recallservice.RecallRequest) map[string]any {
	effective := map[string]any{
		"query": req.Query,
		"limit": req.Limit,
	}
	if req.ValidAt != nil {
		effective["valid_at"] = req.ValidAt.UTC().Format(time.RFC3339Nano)
	}
	if req.KnownAt != nil {
		effective["known_at"] = req.KnownAt.UTC().Format(time.RFC3339Nano)
	}
	if len(req.KnownEvidenceIDs) > 0 {
		effective["known_evidence_ids"] = append([]string(nil), req.KnownEvidenceIDs...)
	}
	if len(req.ExpandFromEntityIDs) > 0 {
		effective["expand_from_entity_ids"] = append([]string(nil), req.ExpandFromEntityIDs...)
	}
	if len(req.KnownRelationshipIDs) > 0 {
		effective["known_relationship_ids"] = append([]string(nil), req.KnownRelationshipIDs...)
	}
	return map[string]any{
		"input":     recallFeedbackInputCopy(input),
		"effective": effective,
	}
}

func recallFeedbackInputCopy(input map[string]any) map[string]any {
	out := map[string]any{}
	for _, key := range []string{"query", "limit", "valid_at", "known_at", "known_evidence_ids", "expand_from_entity_ids", "known_relationship_ids"} {
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
		case hit.Relationship != nil:
			ref.Type = domain.RecallFeedbackResultTypeRelationship
			ref.ID = hit.Relationship.RelationshipID
			ref.StatusAtRecall = string(hit.Relationship.Status)
			ref.RecordedAt = timePtrIfNonZero(hit.Relationship.RecordedAt)
			ref.ValidFrom = hit.Relationship.ValidFrom
			ref.ValidTo = hit.Relationship.ValidTo
		case hit.Evidence != nil:
			ref.Type = domain.RecallFeedbackResultTypeEvidence
			ref.ID = hit.Evidence.FragmentID
			ref.StatusAtRecall = "active"
			ref.CreatedAt = timePtrIfNonZero(hit.Evidence.CreatedAt)
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

// --- schema + marshaling helpers ------------------------------------------

const (
	memoryEntryMaxLength = 10000
	memoryEntryGuidance  = "Store one coherent evidence item per entry; split only when distinct sources, turns, or documents should retain separate provenance."
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

func publicRecallOutputSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"recall_id", "results", "discovery_paths", "discovery_guidance", "related_hypotheses"},
		"properties": map[string]any{
			"recall_id": schemaString("Opaque recall id for deferred session feedback.", 128),
			"results":   map[string]any{"type": "array", "items": publicEvidenceContextObjectSchema()},
			"discovery_paths": map[string]any{
				"type":     "array",
				"maxItems": recallservice.MaxDiscoveryPaths,
				"items":    publicDiscoveryPathObjectSchema(),
			},
			"discovery_guidance": schemaString("How to request focused follow-up context or supporting evidence.", 512),
			"related_hypotheses": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		},
		"additionalProperties": false,
	}
}

func publicRecallEntityObjectSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"entity_id": map[string]any{"type": "string"},
			"name":      map[string]any{"type": "string"},
			"kind":      map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}

func publicRecallObjectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"entity_id": map[string]any{"type": "string"},
			"name":      map[string]any{"type": "string"},
			"kind":      map[string]any{"type": "string"},
			"value":     map[string]any{"type": "string"},
			"type":      map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}

func publicRelationshipObjectSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"relationship_id", "subject", "predicate", "object", "tier", "evidence_ids"},
		"properties": map[string]any{
			"relationship_id":                map[string]any{"type": "string"},
			"subject":                        publicRecallEntityObjectSchema(),
			"predicate":                      map[string]any{"type": "string"},
			"object":                         publicRecallObjectSchema(),
			"tier":                           schemaEnum([]string{"validated_claim", "fact"}),
			"polarity":                       schemaEnum([]string{"+", "-"}),
			"valid_from":                     map[string]any{"type": "string", "format": "date-time"},
			"valid_to":                       map[string]any{"type": "string", "format": "date-time"},
			"evidence_ids":                   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"corroborating_relationship_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"conflicting_relationship_ids":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"additionalProperties": false,
	}
}

func publicEvidenceContextObjectSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"evidence_id", "context"},
		"properties": map[string]any{
			"evidence_id": map[string]any{"type": "string"},
			"context":     map[string]any{"type": "string"},
		},
		"additionalProperties": false,
	}
}

func publicDiscoveryPathObjectSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"relationships", "evidence_ids"},
		"properties": map[string]any{
			"relationships": map[string]any{
				"type":     "array",
				"maxItems": recallservice.MaxDiscoveryPathRelationships,
				"items":    publicPathRelationshipObjectSchema(),
			},
			"evidence_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
		"additionalProperties": false,
	}
}

func publicPathRelationshipObjectSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"relationship_id", "subject", "predicate", "object"},
		"properties": map[string]any{
			"relationship_id": map[string]any{"type": "string"},
			"subject":         publicRecallEntityObjectSchema(),
			"predicate":       map[string]any{"type": "string"},
			"object":          publicRecallObjectSchema(),
			"polarity":        schemaEnum([]string{"+", "-"}),
		},
		"additionalProperties": false,
	}
}

func semanticRelationshipObjectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"relationship_id":   map[string]any{"type": "string"},
			"team_id":           map[string]any{"type": "string"},
			"owner_profile_id":  map[string]any{"type": "string"},
			"subject_entity_id": map[string]any{"type": "string"},
			"predicate":         map[string]any{"type": "string"},
			"object_entity_id":  map[string]any{"type": "string"},
			"object_value":      map[string]any{"type": "string"},
			"object_kind":       map[string]any{"type": "string"},
			"tier":              map[string]any{"type": "string", "enum": []string{"candidate", "validated_claim", "fact"}},
			"status":            map[string]any{"type": "string"},
			"confidence":        map[string]any{"type": "number"},
			"recorded_at":       map[string]any{"type": "string", "format": "date-time"},
			"updated_at":        map[string]any{"type": "string", "format": "date-time"},
		},
	}
}

func semanticEvidenceObjectSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"fragment_id":      map[string]any{"type": "string"},
			"team_id":          map[string]any{"type": "string"},
			"owner_profile_id": map[string]any{"type": "string"},
			"content":          map[string]any{"type": "string"},
			"source":           map[string]any{"type": "string"},
			"source_doc_id":    map[string]any{"type": "string"},
			"source_type":      map[string]any{"type": "string"},
			"authority":        map[string]any{"type": "string"},
			"labels":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"metadata":         map[string]any{"type": "object"},
			"content_hash":     map[string]any{"type": "string"},
			"created_at":       map[string]any{"type": "string", "format": "date-time"},
		},
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

func structSliceToAny(v any) ([]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := []any{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}
