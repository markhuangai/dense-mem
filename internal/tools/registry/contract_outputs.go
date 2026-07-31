package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/contextservice"
	"github.com/markhuangai/dense-mem/internal/service/dreamservice"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/service/skillpackservice"
)

func rememberRequestFromContractInput(input map[string]any) (memoryservice.RememberRequest, error) {
	var req memoryservice.RememberRequest
	if err := remapInput(input, &req); err != nil {
		return req, err
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		req.IdempotencyKey = rememberIngestIdempotencyKey(req.Evidence)
	}
	proposal, ok := objectFields(input["proposal"])
	if !ok {
		return req, nil
	}
	req.EntityHints = objectArray(proposal["entities"])
	req.RelationshipHints = objectArray(proposal["relationships"])
	return req, nil
}

func resolveDreamFeedbackRequestFromContractInput(input map[string]any) (dreamservice.ResolveFeedbackRequest, error) {
	var req dreamservice.ResolveFeedbackRequest
	if err := remapInput(input, &req); err != nil {
		return req, err
	}
	req.DreamID = stringInput(input["hypothesis_id"])
	req.Feedback = stringInput(input["reason"])
	proposal, ok := objectFields(input["proposal"])
	if !ok {
		return req, nil
	}
	req.EntityHints = objectArray(proposal["entities"])
	req.RelationshipHints = objectArray(proposal["relationships"])
	return req, nil
}

func rememberIngestIdempotencyKey(evidence []memoryservice.RememberEvidenceInput) string {
	if len(evidence) == 0 {
		return ""
	}
	if len(evidence) == 1 {
		return strings.TrimSpace(evidence[0].IdempotencyKey)
	}
	h := sha256.New()
	for _, item := range evidence {
		key := strings.TrimSpace(item.IdempotencyKey)
		if key == "" {
			return ""
		}
		_, _ = h.Write([]byte(key))
		_, _ = h.Write([]byte{0})
	}
	return "batch:" + hex.EncodeToString(h.Sum(nil))
}

func recallContractOutput(res *memoryservice.RecallResult) map[string]any {
	if res == nil {
		return map[string]any{
			"recall_id":             "",
			"results":               []any{},
			"conflicts":             []any{},
			"related_relationships": []any{},
			"related_communities":   []any{},
			"related_hypotheses":    []any{},
			"search_states": map[string]any{
				"evidence":      string(domain.SearchProjectionCurrent),
				"relationships": string(domain.SearchProjectionCurrent),
			},
			"degradations": []any{},
		}
	}
	results := make([]map[string]any, 0, len(res.Results))
	for _, item := range res.Results {
		results = append(results, map[string]any{
			"evidence_id": item.EvidenceID,
			"context":     item.Context,
		})
	}
	relatedRelationships := res.RelatedRelationships
	if relatedRelationships == nil {
		relatedRelationships = []memoryservice.RelatedRelationshipSummary{}
	}
	relatedCommunities := res.RelatedCommunities
	if relatedCommunities == nil {
		relatedCommunities = []memoryservice.RecallDiscoveryPath{}
	}
	conflicts := res.Conflicts
	if conflicts == nil {
		conflicts = []memoryservice.RecallConflictSummary{}
	}
	relatedHypotheses := res.RelatedHypotheses
	if relatedHypotheses == nil {
		relatedHypotheses = []memoryservice.RelatedHypothesisSummary{}
	}
	degradations := res.Degradations
	if degradations == nil {
		degradations = []memoryservice.RecallDegradationResult{}
	}
	searchStates := map[string]any{
		"evidence":      res.SearchStates.Evidence,
		"relationships": res.SearchStates.Relationships,
	}
	if strings.TrimSpace(res.SearchStates.Evidence) == "" {
		searchStates["evidence"] = res.SearchState
	}
	if strings.TrimSpace(searchStates["evidence"].(string)) == "" {
		searchStates["evidence"] = string(domain.SearchProjectionCurrent)
	}
	if strings.TrimSpace(searchStates["relationships"].(string)) == "" {
		searchStates["relationships"] = string(domain.SearchProjectionCurrent)
	}
	return map[string]any{
		"recall_id":             res.RecallID,
		"results":               results,
		"conflicts":             conflicts,
		"related_relationships": relatedRelationships,
		"related_communities":   relatedCommunities,
		"related_hypotheses":    relatedHypotheses,
		"search_states":         searchStates,
		"degradations":          degradations,
	}
}

func traceContractOutput(trace *contextservice.SemanticTrace) (map[string]any, error) {
	if trace == nil {
		trace = &contextservice.SemanticTrace{}
	}
	stoppedReason := any(nil)
	if strings.TrimSpace(trace.StoppedReason) != "" {
		stoppedReason = trace.StoppedReason
	}
	return map[string]any{
		"relationship":                     traceRelationshipOutput(trace.Relationship),
		"observations":                     traceObservationOutputs(trace.Observations),
		"evidence_supports":                traceEvidenceSupportOutputs(trace.EvidenceSupports),
		"evidence_support_decision_events": traceSupportDecisionOutputs(trace.EvidenceSupportDecisionEvents),
		"evidence":                         traceEvidenceOutputs(trace.Evidence),
		"evidence_lifecycle_events":        traceEvidenceLifecycleEventOutputs(trace.EvidenceLifecycleEvents),
		"verification_events":              traceVerificationOutputs(trace.VerificationEvents),
		"transitions":                      traceTransitionOutputs(trace.Transitions),
		"conflicts":                        traceConflictOutputs(trace.Conflicts),
		"cross_profile_references":         traceCrossProfileReferenceOutputs(trace.CrossProfileReferences),
		"identity_corrections":             traceIdentityCorrectionOutputs(trace.IdentityCorrections),
		"supersession_lineage":             traceRelationshipLineageOutputs(trace.SupersessionLineage),
		"semantic_nodes":                   traceSemanticNodeOutputs(trace.SemanticNodes),
		"semantic_edges":                   traceSemanticEdgeOutputs(trace.SemanticEdges),
		"visited_entity_ids":               traceStringArray(trace.VisitedEntityIDs),
		"stopped_reason":                   stoppedReason,
	}, nil
}

func traceRelationshipOutput(record *repository.RelationshipTraceRecord) map[string]any {
	if record == nil {
		return nil
	}
	return traceRelationshipRecordOutput(*record)
}

func traceRelationshipLineageOutputs(records []repository.RelationshipTraceRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, traceRelationshipRecordOutput(record))
	}
	return out
}

func traceRelationshipRecordOutput(record repository.RelationshipTraceRecord) map[string]any {
	out := map[string]any{
		"relationship_id": record.RelationshipID,
	}
	putString(out, "owner_profile_id", record.OwnerProfileID)
	putString(out, "subject_entity_id", record.SubjectEntityID)
	putString(out, "subject_name", record.SubjectName)
	putString(out, "predicate_key", record.PredicateKey)
	putInt(out, "predicate_version", record.PredicateVersion)
	putNullableString(out, "object_entity_id", record.ObjectEntityID)
	putNullableString(out, "object_value_id", record.ObjectValueID)
	putString(out, "polarity", record.Polarity)
	putString(out, "relationship_status", record.Status)
	putInt(out, "version", record.Version)
	putNullableTime(out, "valid_from", record.ValidFrom)
	putNullableTime(out, "valid_to", record.ValidTo)
	putTime(out, "created_at", record.CreatedAt)
	putTime(out, "updated_at", record.UpdatedAt)
	return out
}

func traceObservationOutputs(records []repository.RelationshipObservationRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"observation_id":    record.ObservationID,
			"ingest_id":         record.IngestID,
			"placement_item_id": record.PlacementItemID,
		}
		putNullableString(item, "relationship_id", record.RelationshipID)
		putString(item, "subject_ref", record.SubjectRef)
		putString(item, "original_predicate", record.OriginalPredicate)
		putString(item, "object_ref", record.ObjectRef)
		putString(item, "polarity", record.Polarity)
		putTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func traceEvidenceSupportOutputs(records []repository.RelationshipEvidenceSupportRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"evidence_support_id": record.SupportID,
			"relationship_id":     record.RelationshipID,
			"evidence_id":         record.FragmentID,
			"span_start":          record.SpanStart,
			"span_end":            record.SpanEnd,
		}
		putString(item, "observation_id", record.ObservationID)
		putString(item, "verification_event_id", record.VerificationEventID)
		putString(item, "source_group_key", record.SourceGroupKey)
		putString(item, "quote", record.Quote)
		putString(item, "authority", record.Authority)
		putTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func traceSupportDecisionOutputs(records []repository.RelationshipSupportDecisionEvent) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"evidence_support_decision_id": record.SupportDecisionID,
			"evidence_support_id":          record.SupportID,
			"relationship_id":              record.RelationshipID,
			"decision":                     record.Decision,
		}
		putString(item, "actor_profile_id", record.ActorProfileID)
		putString(item, "reason", record.Reason)
		putRequiredTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func traceEvidenceOutputs(records []repository.TraceEvidenceFragment) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"evidence_id":       record.FragmentID,
			"ingest_id":         record.IngestID,
			"evidence_index":    record.EvidenceIndex,
			"content_hash":      record.ContentHash,
			"content_truncated": record.ContentTruncated,
		}
		putString(item, "content", record.Content)
		putString(item, "source_type", record.SourceType)
		putString(item, "source", firstNonEmpty(record.SourceRef, record.SourceKey))
		putTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func traceEvidenceLifecycleEventOutputs(records []repository.TraceEvidenceLifecycleEvent) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"lifecycle_event_id":     record.LifecycleEventID,
			"lifecycle_operation_id": record.LifecycleOperationID,
			"target_evidence_id":     record.TargetFragmentID,
			"action":                 record.Action,
		}
		putNullableString(item, "replacement_evidence_id", record.ReplacementFragmentID)
		putString(item, "reason", record.Reason)
		putRequiredTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func traceVerificationOutputs(records []repository.RelationshipVerificationEvent) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"verification_event_id": record.VerificationEventID,
			"observation_id":        record.ObservationID,
			"evidence_verdict":      record.EvidenceVerdict,
		}
		if record.Confidence != nil {
			item["confidence"] = *record.Confidence
		}
		putString(item, "rationale", record.Rationale)
		putRequiredTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func traceTransitionOutputs(records []repository.RelationshipTransitionEvent) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"transition_id":   record.TransitionID,
			"relationship_id": record.RelationshipID,
			"to_status":       record.ToStatus,
			"reason":          record.Reason,
		}
		putNullableString(item, "from_status", record.FromStatus)
		putRequiredTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func traceConflictOutputs(records []repository.RelationshipConflictCaseRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		positions := traceConflictPositionOutputs(record.Positions)
		item := map[string]any{
			"conflict_id":         record.ConflictID,
			"version":             record.Version,
			"kind":                record.Kind,
			"status":              record.Status,
			"question":            record.Question,
			"positions":           positions,
			"positions_truncated": len(record.Positions) > traceConflictPositionLimit,
		}
		putTime(item, "review_due_at", record.ReviewDueAt)
		putNullableTime(item, "effective_at", record.EffectiveAt)
		putNullableString(item, "effective_time_basis", record.EffectiveTimeBasis)
		putNullableString(item, "preferred_position_id", record.PreferredPositionID)
		out = append(out, item)
	}
	return out
}

const (
	traceConflictPositionLimit         = 10
	traceConflictRelationshipIDLimit   = 20
	traceConflictOwnerProfileIDLimit   = 20
	traceConflictResultEvidenceIDLimit = 50
)

func traceConflictPositionOutputs(records []repository.RelationshipConflictPositionRecord) []map[string]any {
	if len(records) > traceConflictPositionLimit {
		records = records[:traceConflictPositionLimit]
	}
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"position_id":         record.PositionID,
			"disposition":         record.Disposition,
			"relationship_ids":    traceBoundedStringArray(record.RelationshipIDs, traceConflictRelationshipIDLimit),
			"owner_profile_ids":   traceBoundedStringArray(record.OwnerProfileIDs, traceConflictOwnerProfileIDLimit),
			"result_evidence_ids": traceBoundedStringArray(record.EvidenceIDs, traceConflictResultEvidenceIDLimit),
		})
	}
	return out
}

func traceBoundedStringArray(values []string, limit int) []string {
	if limit >= 0 && len(values) > limit {
		values = values[:limit]
	}
	return traceStringArray(values)
}

func traceCrossProfileReferenceOutputs(records []repository.RelationshipCrossReferenceRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"reference_id":           record.CrossReferenceID,
			"source_relationship_id": record.SourceRelationshipID,
			"target_relationship_id": record.TargetRelationshipID,
			"unchanged":              true,
		})
	}
	return out
}

func traceIdentityCorrectionOutputs(records []repository.EntityCorrectionEventRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"correction_event_id": record.CorrectionEventID,
			"operation":           record.Action,
			"source_entity_id":    firstNonEmpty(record.SurvivorEntityID, record.NewEntityID),
		}
		putNullableString(item, "target_entity_id", record.NewEntityID)
		item["owned_observation_ids"] = traceStringArray(record.SelectedObservationIDs)
		putString(item, "reason", record.Reason)
		putRequiredTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func traceSemanticNodeOutputs(records []repository.SemanticGraphNode) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"id":   record.ID,
			"kind": record.Type,
			"name": record.Title,
		})
	}
	return out
}

func traceSemanticEdgeOutputs(records []repository.SemanticGraphEdge) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"relationship_id": record.RelationshipID,
			"source_id":       traceGraphID(record.Source),
			"target_id":       traceGraphID(record.Target),
			"predicate":       record.Relationship,
			"polarity":        "+",
		})
	}
	return out
}

func traceStringArray(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func traceGraphID(key string) string {
	if strings.HasPrefix(key, "entity:") {
		return strings.TrimPrefix(key, "entity:")
	}
	if strings.HasPrefix(key, "value:") {
		return strings.TrimPrefix(key, "value:")
	}
	return key
}

func putString(out map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		out[key] = value
	}
}

func putNullableString(out map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		out[key] = value
	}
}

func putInt(out map[string]any, key string, value int) {
	if value != 0 {
		out[key] = value
	}
}

func putTime(out map[string]any, key string, value time.Time) {
	if !value.IsZero() {
		out[key] = value.UTC().Format(time.RFC3339Nano)
	}
}

func putRequiredTime(out map[string]any, key string, value time.Time) {
	out[key] = value.UTC().Format(time.RFC3339Nano)
}

func putNullableTime(out map[string]any, key string, value *time.Time) {
	if value != nil {
		out[key] = value.UTC().Format(time.RFC3339Nano)
	}
}

func listDreamsContractOutput(dreams []*domain.Dream, next string) map[string]any {
	items := make([]map[string]any, 0, len(dreams))
	for _, dream := range dreams {
		items = append(items, dreamSummaryContractOutput(dream))
	}
	out := map[string]any{"dreams": items}
	if strings.TrimSpace(next) != "" {
		out["next_cursor"] = next
	}
	return out
}

func resolveDreamFeedbackContractOutput(res *dreamservice.ResolveFeedbackResult) map[string]any {
	out := map[string]any{
		"hypothesis_id": "",
		"status":        string(domain.HypothesisProposed),
	}
	if res == nil || res.Dream == nil {
		return out
	}
	out["hypothesis_id"] = res.Dream.DreamID
	out["status"] = string(res.Dream.Status)
	if res.Memory != nil && strings.TrimSpace(res.Memory.IngestID) != "" {
		out["ingest_id"] = res.Memory.IngestID
	}
	return out
}

func findMemoryPackCandidatesContractOutput(res *skillpackservice.FindCandidatesResult) map[string]any {
	if res == nil {
		return map[string]any{"candidates": []any{}}
	}
	candidates := make([]map[string]any, 0, len(res.Candidates))
	for i, candidate := range res.Candidates {
		candidates = append(candidates, map[string]any{
			"relationship_id": candidate.RelationshipID,
			"subject": map[string]any{
				"entity_id": candidate.SubjectEntityID,
				"name":      candidate.Subject,
			},
			"predicate": candidate.PredicateKey,
			"object":    memoryPackObjectContractOutput(candidate),
			"polarity":  firstNonEmpty(candidate.Polarity, "+"),
			"rank":      i + 1,
		})
	}
	return map[string]any{"candidates": candidates}
}

func memoryPackObjectContractOutput(candidate skillpackservice.MemoryPackCandidate) map[string]any {
	if strings.TrimSpace(candidate.ObjectValueID) != "" {
		return map[string]any{
			"value_id": candidate.ObjectValueID,
			"type":     firstNonEmpty(candidate.ObjectValueType, string(domain.ValueTypeString)),
			"value":    candidate.Object,
		}
	}
	return map[string]any{
		"entity_id": candidate.ObjectEntityID,
		"name":      candidate.Object,
	}
}

func exportMemoryPackContractOutput(res *skillpackservice.ExportResult) map[string]any {
	if res == nil {
		return map[string]any{
			"artifact_json":  "",
			"content_sha256": strings.Repeat("0", 64),
			"filename":       "",
			"counts":         map[string]any{},
			"omissions":      []any{},
		}
	}
	return map[string]any{
		"artifact_json":  res.CanonicalJSON,
		"content_sha256": res.SHA256,
		"filename":       res.Filename,
		"counts": map[string]any{
			"relationships":     res.ItemCount,
			"evidence":          len(res.Artifact.Evidence),
			"evidence_supports": len(res.Artifact.EvidenceSupports),
		},
		"omissions": memoryPackOmissionsContractOutput(res.Omissions),
	}
}

func inspectMemoryPackContractOutput(res *skillpackservice.InspectResult, mode string) map[string]any {
	if res == nil {
		return map[string]any{
			"valid":             false,
			"format":            skillpackservice.MemoryPackFormat,
			"content_sha256":    strings.Repeat("0", 64),
			"mode":              firstNonEmpty(mode, "review"),
			"counts":            map[string]any{},
			"conflicts":         []any{},
			"expected_outcomes": map[string]any{},
		}
	}
	ready := 0
	review := 0
	for _, item := range res.Items {
		if item.Status == "ready" {
			ready++
			continue
		}
		review++
	}
	return map[string]any{
		"valid":          true,
		"format":         res.Format,
		"content_sha256": res.ArtifactHash,
		"mode":           firstNonEmpty(mode, "review"),
		"counts": map[string]any{
			"relationships":     res.ItemCount,
			"selected":          res.SelectedCount,
			"evidence":          res.SupportSummary.EvidenceCount,
			"evidence_supports": res.SupportSummary.SupportCount,
		},
		"conflicts": memoryPackConflictsContractOutput(res.DecisionsRequired),
		"expected_outcomes": map[string]any{
			"create": ready,
			"review": review,
			"skip":   0,
		},
	}
}

func importMemoryPackContractOutput(res *skillpackservice.ImportResult) map[string]any {
	if res == nil {
		return map[string]any{
			"import_id":        "",
			"processing_state": "failed",
			"ingest_ids":       []any{},
			"omissions":        []any{},
		}
	}
	ingestIDs := []string{}
	if strings.TrimSpace(res.IngestID) != "" {
		ingestIDs = append(ingestIDs, res.IngestID)
	}
	return map[string]any{
		"import_id":        res.ImportID,
		"processing_state": memoryPackProcessingState(res),
		"ingest_ids":       ingestIDs,
		"omissions":        importMemoryPackOmissionsContractOutput(res.Items),
	}
}

func rollbackMemoryPackContractOutput(res *skillpackservice.RollbackResult) map[string]any {
	if res == nil {
		return map[string]any{
			"import_id":                 "",
			"dry_run":                   true,
			"safe":                      false,
			"blockers":                  []any{},
			"affected_relationship_ids": []any{},
		}
	}
	out := map[string]any{
		"import_id":                 res.ImportID,
		"dry_run":                   res.DryRun,
		"safe":                      len(res.Conflicts) == 0,
		"blockers":                  rollbackBlockersContractOutput(res.Conflicts),
		"affected_relationship_ids": res.AffectedRelationshipIDs,
	}
	if strings.TrimSpace(res.ImpactToken) != "" {
		out["impact_token"] = res.ImpactToken
	}
	if res.Status == domain.SkillPackImportStatusRolledBack {
		out["applied"] = true
	}
	return out
}

func memoryPackOmissionsContractOutput(omissions []string) []map[string]any {
	out := make([]map[string]any, 0, len(omissions))
	for _, omission := range omissions {
		if strings.TrimSpace(omission) == "" {
			continue
		}
		out = append(out, map[string]any{"item_id": "artifact", "reason": omission})
	}
	return out
}

func memoryPackConflictsContractOutput(prompts []skillpackservice.ConflictPrompt) []map[string]any {
	out := make([]map[string]any, 0, len(prompts))
	for _, prompt := range prompts {
		out = append(out, map[string]any{
			"item_id":           prompt.ItemID,
			"kind":              firstNonEmpty(prompt.Reason, "review_required"),
			"allowed_decisions": prompt.AllowedActions,
		})
	}
	return out
}

func importMemoryPackOmissionsContractOutput(items []skillpackservice.ImportItemResult) []map[string]any {
	out := []map[string]any{}
	for _, item := range items {
		switch item.Status {
		case "skipped":
			out = append(out, map[string]any{"item_id": item.ItemID, "reason": firstNonEmpty(item.Decision, "skipped")})
		case "failed":
			out = append(out, map[string]any{"item_id": item.ItemID, "reason": firstNonEmpty(item.Error, "failed")})
		}
	}
	return out
}

func memoryPackProcessingState(res *skillpackservice.ImportResult) string {
	switch res.Status {
	case domain.SkillPackImportStatusFailed, "status_update_failed", "change_ledger_failed":
		return "failed"
	case domain.SkillPackImportStatusInspecting:
		return "processing"
	case domain.SkillPackImportStatusApplied:
		if strings.TrimSpace(res.IngestID) != "" {
			return "queued"
		}
		return "completed"
	default:
		return "completed"
	}
}

func rollbackBlockersContractOutput(conflicts []string) []map[string]any {
	out := make([]map[string]any, 0, len(conflicts))
	for _, conflict := range conflicts {
		out = append(out, map[string]any{
			"code":    "rollback_blocked",
			"message": conflict,
		})
	}
	return out
}

func dreamSummaryContractOutput(dream *domain.Dream) map[string]any {
	if dream == nil {
		return map[string]any{}
	}
	out := map[string]any{
		"hypothesis_id":           dream.DreamID,
		"subject_entity_id":       dream.SubjectEntityID,
		"predicate_key":           dream.PredicateKey,
		"statement":               dream.Hypothesis,
		"status":                  string(dream.Status),
		"source_relationship_ids": dreamOutputSourceIDs(dream, false),
		"generator_kind":          dreamOutputGeneratorKind(dream),
		"generator_version":       firstNonEmpty(dream.GeneratorVersion, dream.GeneratorModel, "dream-v2"),
		"created_at":              dreamOutputCreatedAt(dream),
	}
	if strings.TrimSpace(dream.ObjectEntityID) != "" {
		out["object_entity_id"] = dream.ObjectEntityID
	}
	if strings.TrimSpace(dream.ObjectValueID) != "" {
		out["object_value_id"] = dream.ObjectValueID
	}
	return out
}

func dreamContractOutput(dream *domain.Dream) map[string]any {
	out := dreamSummaryContractOutput(dream)
	if dream == nil {
		return out
	}
	out["source_owner_profile_ids"] = dreamOutputOwnerIDs(dream)
	out["rationale"] = firstNonEmpty(dream.Rationale, "No rationale supplied.")
	out["likelihood"] = dream.Likelihood
	out["confidence"] = dream.Confidence
	out["source_candidate_relationship_ids"] = dreamOutputSourceIDs(dream, true)
	out["source_versions"] = dreamOutputSourceVersions(dream)
	return out
}

func dreamOutputOwnerIDs(dream *domain.Dream) []string {
	if len(dream.SourceOwnerProfileIDs) > 0 {
		return append([]string(nil), dream.SourceOwnerProfileIDs...)
	}
	return []string{}
}

func dreamOutputSourceIDs(dream *domain.Dream, candidates bool) []string {
	if candidates && len(dream.SourceCandidateRelationshipIDs) > 0 {
		return append([]string(nil), dream.SourceCandidateRelationshipIDs...)
	}
	if !candidates && len(dream.SourceRelationshipIDs) > 0 {
		return append([]string(nil), dream.SourceRelationshipIDs...)
	}
	out := []string{}
	for _, ref := range dream.SourceRefs {
		isCandidate := ref.Type == "candidate_relationship"
		if isCandidate == candidates && strings.TrimSpace(ref.ID) != "" {
			out = append(out, ref.ID)
		}
	}
	return out
}

func dreamOutputSourceVersions(dream *domain.Dream) map[string]int {
	if dream.SourceVersions == nil {
		return map[string]int{}
	}
	out := make(map[string]int, len(dream.SourceVersions))
	for key, value := range dream.SourceVersions {
		out[key] = value
	}
	return out
}

func dreamOutputGeneratorKind(dream *domain.Dream) string {
	if strings.TrimSpace(dream.GeneratorKind) == "provider" {
		return "provider"
	}
	return "deterministic"
}

func dreamOutputCreatedAt(dream *domain.Dream) string {
	createdAt := dream.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	return createdAt.UTC().Format(time.RFC3339Nano)
}
