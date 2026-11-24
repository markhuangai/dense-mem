package recall

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"log/slog"
	"sort"

	"github.com/markhuangai/dense-mem/internal/domain"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
)

const maxEquivalentEvidenceIDs = 20

func organizationDegradation(frontier, code string) []RecallDegradationResult {
	if code == "" {
		return nil
	}
	message := "Topic and equivalence metadata was unavailable. Ordinary eligible retrieval was used."
	switch code {
	case "ontology_temporal_not_supported":
		message = "Temporal Recall omits current Topic and equivalence metadata."
	case "ontology_stale":
		message = "Stale Topic or equivalence metadata was omitted. Ordinary eligible retrieval remains available."
	case "ontology_bound_exceeded":
		message = "Topic and equivalence metadata exceeded its read bound. Ordinary eligible retrieval was used."
	case "ontology_group_overlap":
		message = "Overlapping equivalence groups were omitted. Ordinary eligible retrieval was used."
	}
	return []RecallDegradationResult{{Frontier: frontier, Optional: true, Code: code, Message: message}}
}

func eligibleEvidenceGroups(groups []topic.RecallEvidenceGroup, hits map[string]recallcontract.RecallEvidenceHit, knownIDs []string) (map[string]topic.RecallEvidenceGroup, map[string]bool, bool) {
	known, membership, overlap := map[string]bool{}, map[string]string{}, map[string]bool{}
	for _, id := range knownIDs {
		if _, eligible := hits[id]; eligible {
			known[id] = true
		}
	}
	for _, group := range groups {
		for _, id := range group.Members {
			if _, eligible := hits[id]; !eligible {
				continue
			}
			if previous, exists := membership[id]; exists && previous != group.ID {
				overlap[previous], overlap[group.ID] = true, true
			}
			membership[id] = group.ID
		}
	}
	byID, suppressed := map[string]topic.RecallEvidenceGroup{}, map[string]bool{}
	for _, group := range groups {
		if overlap[group.ID] {
			continue
		}
		eligible := topic.RecallEvidenceGroup{ID: group.ID}
		knownGroup := false
		for _, id := range group.Members {
			if _, ok := hits[id]; ok {
				eligible.Members = append(eligible.Members, id)
				knownGroup = knownGroup || known[id]
			}
		}
		eligible.Members = domain.NormalizeReadIDList(eligible.Members)
		for _, id := range eligible.Members {
			byID[id] = eligible
			if knownGroup {
				suppressed[id] = true
			}
		}
	}
	return byID, suppressed, len(overlap) > 0
}

func selectRecallEvidence(candidates []recallCandidate, hydration *recallcontract.EvidenceHydration, input recallcontract.RecallEvidenceInput) ([]recallcontract.RecallEvidenceHit, []recallcontract.RecallEvidenceHit, string) {
	groups, suppressed, overlap := eligibleEvidenceGroups(hydration.Groups, hydration.Hits, input.KnownEvidenceIDs)
	seenGroups := map[string]bool{}
	results, conflicts := []recallcontract.RecallEvidenceHit{}, []recallcontract.RecallEvidenceHit{}
	for _, candidate := range candidates {
		hit, eligible := hydration.Hits[candidate.ID]
		if !eligible || suppressed[candidate.ID] {
			continue
		}
		group := groups[candidate.ID]
		if group.ID != "" && seenGroups[group.ID] {
			continue
		}
		seenGroups[group.ID] = true
		hit.EquivalentEvidenceIDs = []string{}
		for _, id := range group.Members {
			if id != hit.EvidenceID {
				hit.EquivalentEvidenceIDs = append(hit.EquivalentEvidenceIDs, id)
			}
		}
		sort.Strings(hit.EquivalentEvidenceIDs)
		hit.EquivalentsTruncated = len(hit.EquivalentEvidenceIDs) > maxEquivalentEvidenceIDs
		if hit.EquivalentsTruncated {
			hit.EquivalentEvidenceIDs = hit.EquivalentEvidenceIDs[:maxEquivalentEvidenceIDs]
		}
		for _, id := range hit.EquivalentEvidenceIDs {
			alternate := hydration.Hits[id]
			hit.SearchState = domain.CombineSearchProjectionStates(hit.SearchState, alternate.SearchState)
			conflicts = append(conflicts, alternate)
		}
		hit.Score, hit.SpaceKind, hit.Rank = candidate.Score, input.SpaceKind, len(results)+1
		hit.SearchState = domain.CombineSearchProjectionStates(candidate.SearchState, hit.SearchState)
		results = append(results, hit)
		conflicts = append(conflicts, hit)
		if len(results) == input.Limit {
			break
		}
	}
	code := hydration.OrganizationDegradation
	if overlap {
		code = "ontology_group_overlap"
	}
	return results, conflicts, code
}

func (s *recallService) relatedTopics(ctx context.Context, req RecallRequest, result *RecallResult) error {
	result.RelatedTopics = []recallcontract.RelatedTopic{}
	limit := recallOptionalLimitValue(req.TopicLimit)
	if limit == 0 {
		return nil
	}
	actor, _ := requestctx.ActorFromContext(ctx)
	input := recallcontract.RecallEvidenceInput{TeamID: actor.TeamID.String(), Query: req.Query, TopicIDs: req.TopicIDs, KnownEvidenceIDs: req.KnownEvidenceIDs, ValidAt: req.ValidAt, KnownAt: req.KnownAt, SpaceKind: string(domain.MemorySpaceTeamShared)}
	shared := false
	for _, space := range actor.AllowedSpaces {
		if space.Kind == domain.MemorySpaceTeamShared {
			input.SpaceID = space.ID.String()
			shared = true
			break
		}
	}
	if len(actor.AllowedSpaces) == 0 {
		shared = true
	}
	if !shared {
		if req.TopicIDs != nil {
			return ErrTopicUnavailable
		}
		return nil
	}
	var selection recallcontract.RelatedTopicResult
	var err error
	if s.topicMaintenanceConfig == nil {
		err = ErrTopicUnavailable
	} else {
		config, configErr := s.topicMaintenanceConfig.TopicMaintenanceRuntimeConfig(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if configErr != nil {
			err = configErr
		} else if !config.Enabled {
			if req.TopicIDs != nil {
				err = ErrTopicUnavailable
			} else {
				return nil
			}
		} else if reader, ok := s.search.(recallcontract.TopicDiscoveryRepository); ok {
			input.OrganizationEnabled = true
			requestctx.ReportProgress(ctx, requestctx.ProgressUpdate{Stage: "recall_topics"})
			selection, err = reader.RelatedEvidenceTopics(ctx, input, recallResultEvidenceIDs(result.Results), limit)
		} else {
			err = ErrTopicUnavailable
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	captureID := uuid.NewString()
	attrs := []observability.LogAttr{observability.String("team_id", input.TeamID), observability.String("owner_profile_id", actor.OwnerID.String()), observability.String("recall_id", result.RecallID), observability.String("diagnostic_capture_id", captureID), {Key: "requested_ids", Value: req.TopicIDs}, {Key: "known_evidence_ids", Value: req.KnownEvidenceIDs}, {Key: "returned_evidence_ids", Value: recallResultEvidenceIDs(result.Results)}, {Key: "topic_limit", Value: limit}, {Key: "topic_count", Value: len(selection.Topics)}, {Key: "truncated", Value: selection.Truncated}}
	if root, ok := s.logger.(interface{ ContextSlog() *slog.Logger }); ok {
		body, encodeErr := json.Marshal(selection)
		if encodeErr != nil {
			s.logTopics(ctx, slog.LevelWarn, "diagnostic_body_unavailable", append(attrs, observability.String("reason_code", "diagnostic_encoding_failed")))
		} else {
			observability.LogDiagnosticBody(ctx, root.ContextSlog(), s.diagnosticProtector, "recall_topic_selection_payload", body, modelprovider.MaxProviderDiagnosticBodyBytes,
				slog.String("team_id", input.TeamID), slog.String("recall_id", result.RecallID), slog.String("diagnostic_capture_id", captureID))
		}
	}
	if err != nil {
		attrs = append(attrs, observability.String("reason", err.Error()), observability.String("outcome", "unavailable"))
		s.logTopics(ctx, slog.LevelWarn, "recall_topic_degraded", attrs)
		if req.TopicIDs != nil {
			return ErrTopicUnavailable
		}
		result.Degradations = append(result.Degradations, RecallDegradationResult{Frontier: "topics", Optional: true, Code: "topic_unavailable", Message: "Related topics are unavailable."})
		result.Degradation = &result.Degradations[0]
		return nil
	}
	result.RelatedTopics = selection.Topics
	result.TopicsTruncated = selection.Truncated
	attrs = append(attrs, observability.String("outcome", "selected"))
	s.logTopics(ctx, slog.LevelDebug, "recall_topic_selection", attrs)
	return nil
}

func (s *recallService) logTopics(ctx context.Context, level slog.Level, event string, attrs []observability.LogAttr) {
	if s.logger == nil {
		return
	}
	if contextual, ok := s.logger.(observability.ContextLogProvider); ok {
		contextual.LogContext(ctx, level, event, attrs...)
	} else if level >= slog.LevelWarn {
		s.logger.Warn(event, attrs...)
	} else {
		s.logger.Debug(event, attrs...)
	}
}
