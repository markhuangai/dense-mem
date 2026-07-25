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

func v2RememberRequestFromContractInput(input map[string]any) (memoryservice.V2RememberRequest, error) {
	var req memoryservice.V2RememberRequest
	if err := remapInput(input, &req); err != nil {
		return req, err
	}
	if strings.TrimSpace(req.IdempotencyKey) == "" {
		req.IdempotencyKey = v2RememberIngestIdempotencyKey(req.Evidence)
	}
	proposal, ok := objectFields(input["proposal"])
	if !ok {
		return req, nil
	}
	req.EntityHints = v2ObjectArray(proposal["entities"])
	req.RelationshipHints = v2ObjectArray(proposal["relationships"])
	return req, nil
}

func v2ResolveDreamFeedbackRequestFromContractInput(input map[string]any) (dreamservice.ResolveFeedbackRequest, error) {
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
	req.EntityHints = v2ObjectArray(proposal["entities"])
	req.RelationshipHints = v2ObjectArray(proposal["relationships"])
	return req, nil
}

func v2RememberIngestIdempotencyKey(evidence []memoryservice.V2RememberEvidenceInput) string {
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

func v2RecallContractOutput(res *memoryservice.V2RecallResult) map[string]any {
	if res == nil {
		return map[string]any{
			"recall_id":          "",
			"results":            []any{},
			"conflicts":          []any{},
			"discovery_paths":    []any{},
			"discovery_guidance": "",
			"related_hypotheses": []any{},
		}
	}
	results := make([]map[string]any, 0, len(res.Results))
	for _, item := range res.Results {
		results = append(results, map[string]any{
			"evidence_id": item.EvidenceID,
			"context":     item.Context,
		})
	}
	discoveryPaths := res.DiscoveryPaths
	if discoveryPaths == nil {
		discoveryPaths = []memoryservice.V2RecallDiscoveryPath{}
	}
	conflicts := res.Conflicts
	if conflicts == nil {
		conflicts = []memoryservice.V2RecallConflictSummary{}
	}
	relatedHypotheses := res.RelatedHypotheses
	if relatedHypotheses == nil {
		relatedHypotheses = []memoryservice.V2RelatedHypothesisSummary{}
	}
	guidance := strings.TrimSpace(res.DiscoveryGuidance)
	if guidance == "" {
		guidance = "No additional discovery guidance."
	}
	return map[string]any{
		"recall_id":          res.RecallID,
		"results":            results,
		"conflicts":          conflicts,
		"discovery_paths":    discoveryPaths,
		"discovery_guidance": guidance,
		"related_hypotheses": relatedHypotheses,
	}
}

func v2TraceContractOutput(trace *contextservice.SemanticTrace) (map[string]any, error) {
	if trace == nil {
		trace = &contextservice.SemanticTrace{}
	}
	stoppedReason := any(nil)
	if strings.TrimSpace(trace.StoppedReason) != "" {
		stoppedReason = trace.StoppedReason
	}
	return map[string]any{
		"relationship":             v2TraceRelationshipOutput(trace.Relationship),
		"observations":             v2TraceObservationOutputs(trace.Observations),
		"evidence_supports":        v2TraceEvidenceSupportOutputs(trace.EvidenceSupports),
		"support_decision_events":  v2TraceSupportDecisionOutputs(trace.SupportDecisionEvents),
		"evidence_fragments":       v2TraceEvidenceFragmentOutputs(trace.EvidenceFragments),
		"verification_events":      v2TraceVerificationOutputs(trace.VerificationEvents),
		"transitions":              v2TraceTransitionOutputs(trace.Transitions),
		"conflicts":                v2TraceConflictOutputs(trace.Conflicts),
		"cross_profile_references": v2TraceCrossProfileReferenceOutputs(trace.CrossProfileReferences),
		"identity_corrections":     v2TraceIdentityCorrectionOutputs(trace.IdentityCorrections),
		"supersession_lineage":     v2TraceRelationshipLineageOutputs(trace.SupersessionLineage),
		"semantic_nodes":           v2TraceSemanticNodeOutputs(trace.SemanticNodes),
		"semantic_edges":           v2TraceSemanticEdgeOutputs(trace.SemanticEdges),
		"visited_entity_ids":       v2TraceStringArray(trace.VisitedEntityIDs),
		"stopped_reason":           stoppedReason,
	}, nil
}

func v2TraceRelationshipOutput(record *repository.V2RelationshipTraceRecord) map[string]any {
	if record == nil {
		return nil
	}
	return v2TraceRelationshipRecordOutput(*record)
}

func v2TraceRelationshipLineageOutputs(records []repository.V2RelationshipTraceRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, v2TraceRelationshipRecordOutput(record))
	}
	return out
}

func v2TraceRelationshipRecordOutput(record repository.V2RelationshipTraceRecord) map[string]any {
	out := map[string]any{
		"relationship_id": record.RelationshipID,
	}
	v2PutString(out, "owner_profile_id", record.OwnerProfileID)
	v2PutString(out, "subject_entity_id", record.SubjectEntityID)
	v2PutString(out, "subject_name", record.SubjectName)
	v2PutString(out, "predicate_key", record.PredicateKey)
	v2PutInt(out, "predicate_version", record.PredicateVersion)
	v2PutNullableString(out, "object_entity_id", record.ObjectEntityID)
	v2PutNullableString(out, "object_value_id", record.ObjectValueID)
	v2PutString(out, "polarity", record.Polarity)
	v2PutString(out, "tier", record.Tier)
	v2PutString(out, "relationship_status", record.Status)
	v2PutInt(out, "version", record.Version)
	v2PutNullableTime(out, "valid_from", record.ValidFrom)
	v2PutNullableTime(out, "valid_to", record.ValidTo)
	v2PutTime(out, "created_at", record.CreatedAt)
	v2PutTime(out, "updated_at", record.UpdatedAt)
	return out
}

func v2TraceObservationOutputs(records []repository.V2RelationshipObservationRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"observation_id":    record.ObservationID,
			"ingest_id":         record.IngestID,
			"placement_item_id": record.PlacementItemID,
		}
		v2PutNullableString(item, "relationship_id", record.RelationshipID)
		v2PutString(item, "subject_ref", record.SubjectRef)
		v2PutString(item, "original_predicate", record.OriginalPredicate)
		v2PutString(item, "object_ref", record.ObjectRef)
		v2PutString(item, "polarity", record.Polarity)
		v2PutTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func v2TraceEvidenceSupportOutputs(records []repository.V2RelationshipEvidenceSupportRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"support_id":      record.SupportID,
			"relationship_id": record.RelationshipID,
			"fragment_id":     record.FragmentID,
			"span_start":      record.SpanStart,
			"span_end":        record.SpanEnd,
		}
		v2PutString(item, "observation_id", record.ObservationID)
		v2PutString(item, "verification_event_id", record.VerificationEventID)
		v2PutString(item, "source_group_key", record.SourceGroupKey)
		v2PutString(item, "quote", record.Quote)
		v2PutString(item, "authority", record.Authority)
		v2PutTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func v2TraceSupportDecisionOutputs(records []repository.V2RelationshipSupportDecisionEvent) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"support_decision_id": record.SupportDecisionID,
			"support_id":          record.SupportID,
			"relationship_id":     record.RelationshipID,
			"decision":            record.Decision,
		}
		v2PutString(item, "actor_profile_id", record.ActorProfileID)
		v2PutString(item, "reason", record.Reason)
		v2PutRequiredTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func v2TraceEvidenceFragmentOutputs(records []repository.V2TraceEvidenceFragment) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"fragment_id":       record.FragmentID,
			"ingest_id":         record.IngestID,
			"evidence_index":    record.EvidenceIndex,
			"content_hash":      record.ContentHash,
			"content_truncated": record.ContentTruncated,
		}
		v2PutString(item, "content", record.Content)
		v2PutString(item, "source_type", record.SourceType)
		v2PutString(item, "source", firstNonEmpty(record.SourceRef, record.SourceKey))
		v2PutTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func v2TraceVerificationOutputs(records []repository.V2RelationshipVerificationEvent) []map[string]any {
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
		v2PutString(item, "rationale", record.Rationale)
		v2PutRequiredTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func v2TraceTransitionOutputs(records []repository.V2RelationshipTransitionEvent) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"transition_id":   record.TransitionID,
			"relationship_id": record.RelationshipID,
			"to_tier":         record.ToTier,
			"to_status":       record.ToStatus,
			"reason":          record.Reason,
		}
		v2PutNullableString(item, "from_tier", record.FromTier)
		v2PutNullableString(item, "from_status", record.FromStatus)
		v2PutRequiredTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func v2TraceConflictOutputs(records []repository.V2RelationshipConflictCaseRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"conflict_id":         record.ConflictID,
			"version":             record.Version,
			"kind":                record.Kind,
			"status":              record.Status,
			"question":            record.Question,
			"positions":           v2TraceConflictPositionOutputs(record.Positions),
			"positions_truncated": false,
		}
		v2PutTime(item, "review_due_at", record.ReviewDueAt)
		v2PutNullableTime(item, "effective_at", record.EffectiveAt)
		v2PutNullableString(item, "effective_time_basis", record.EffectiveTimeBasis)
		v2PutNullableString(item, "preferred_position_id", record.PreferredPositionID)
		out = append(out, item)
	}
	return out
}

func v2TraceConflictPositionOutputs(records []repository.V2RelationshipConflictPositionRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"position_id":         record.PositionID,
			"disposition":         record.Disposition,
			"relationship_ids":    v2TraceStringArray(record.RelationshipIDs),
			"owner_profile_ids":   v2TraceStringArray(record.OwnerProfileIDs),
			"result_evidence_ids": v2TraceStringArray(record.EvidenceIDs),
		})
	}
	return out
}

func v2TraceCrossProfileReferenceOutputs(records []repository.V2RelationshipCrossReferenceRecord) []map[string]any {
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

func v2TraceIdentityCorrectionOutputs(records []repository.V2EntityCorrectionEventRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"correction_event_id": record.CorrectionEventID,
			"operation":           record.Action,
			"source_entity_id":    firstNonEmpty(record.SurvivorEntityID, record.NewEntityID),
		}
		v2PutNullableString(item, "target_entity_id", record.NewEntityID)
		item["owned_observation_ids"] = v2TraceStringArray(record.SelectedObservationIDs)
		v2PutString(item, "reason", record.Reason)
		v2PutRequiredTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func v2TraceSemanticNodeOutputs(records []repository.V2SemanticGraphNode) []map[string]any {
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

func v2TraceSemanticEdgeOutputs(records []repository.V2SemanticGraphEdge) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"relationship_id": record.RelationshipID,
			"source_id":       v2TraceGraphID(record.Source),
			"target_id":       v2TraceGraphID(record.Target),
			"predicate":       record.Relationship,
			"polarity":        "+",
		})
	}
	return out
}

func v2TraceStringArray(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func v2TraceGraphID(key string) string {
	if strings.HasPrefix(key, "entity:") {
		return strings.TrimPrefix(key, "entity:")
	}
	if strings.HasPrefix(key, "value:") {
		return strings.TrimPrefix(key, "value:")
	}
	return key
}

func v2PutString(out map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		out[key] = value
	}
}

func v2PutNullableString(out map[string]any, key, value string) {
	if strings.TrimSpace(value) != "" {
		out[key] = value
	}
}

func v2PutInt(out map[string]any, key string, value int) {
	if value != 0 {
		out[key] = value
	}
}

func v2PutTime(out map[string]any, key string, value time.Time) {
	if !value.IsZero() {
		out[key] = value.UTC().Format(time.RFC3339Nano)
	}
}

func v2PutRequiredTime(out map[string]any, key string, value time.Time) {
	out[key] = value.UTC().Format(time.RFC3339Nano)
}

func v2PutNullableTime(out map[string]any, key string, value *time.Time) {
	if value != nil {
		out[key] = value.UTC().Format(time.RFC3339Nano)
	}
}

func v2ListDreamsContractOutput(dreams []*domain.Dream, next string) map[string]any {
	items := make([]map[string]any, 0, len(dreams))
	for _, dream := range dreams {
		items = append(items, v2DreamSummaryContractOutput(dream))
	}
	out := map[string]any{"dreams": items}
	if strings.TrimSpace(next) != "" {
		out["next_cursor"] = next
	}
	return out
}

func v2ResolveDreamFeedbackContractOutput(res *dreamservice.ResolveFeedbackResult) map[string]any {
	out := map[string]any{
		"hypothesis_id": "",
		"status":        string(domain.V2HypothesisProposed),
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

func v2FindMemoryPackCandidatesContractOutput(res *skillpackservice.V2FindCandidatesResult) map[string]any {
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
			"object":    v2MemoryPackObjectContractOutput(candidate),
			"polarity":  firstNonEmpty(candidate.Polarity, "+"),
			"rank":      i + 1,
		})
	}
	return map[string]any{"candidates": candidates}
}

func v2MemoryPackObjectContractOutput(candidate skillpackservice.V2MemoryPackCandidate) map[string]any {
	if strings.TrimSpace(candidate.ObjectValueID) != "" {
		return map[string]any{
			"value_id": candidate.ObjectValueID,
			"type":     firstNonEmpty(candidate.ObjectValueType, string(domain.V2ValueTypeString)),
			"value":    candidate.Object,
		}
	}
	return map[string]any{
		"entity_id": candidate.ObjectEntityID,
		"name":      candidate.Object,
	}
}

func v2ExportMemoryPackContractOutput(res *skillpackservice.V2ExportResult) map[string]any {
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
			"relationships":      res.ItemCount,
			"evidence_fragments": len(res.Artifact.EvidenceFragments),
			"evidence_supports":  len(res.Artifact.EvidenceSupports),
		},
		"omissions": v2MemoryPackOmissionsContractOutput(res.Omissions),
	}
}

func v2InspectMemoryPackContractOutput(res *skillpackservice.V2InspectResult, mode string) map[string]any {
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
			"relationships":      res.ItemCount,
			"selected":           res.SelectedCount,
			"evidence_fragments": res.SupportSummary.FragmentCount,
			"evidence_supports":  res.SupportSummary.SupportCount,
		},
		"conflicts": v2MemoryPackConflictsContractOutput(res.DecisionsRequired),
		"expected_outcomes": map[string]any{
			"create": ready,
			"review": review,
			"skip":   0,
		},
	}
}

func v2ImportMemoryPackContractOutput(res *skillpackservice.V2ImportResult) map[string]any {
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
		"processing_state": v2MemoryPackProcessingState(res),
		"ingest_ids":       ingestIDs,
		"omissions":        v2ImportMemoryPackOmissionsContractOutput(res.Items),
	}
}

func v2RollbackMemoryPackContractOutput(res *skillpackservice.V2RollbackResult) map[string]any {
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
		"blockers":                  v2RollbackBlockersContractOutput(res.Conflicts),
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

func v2MemoryPackOmissionsContractOutput(omissions []string) []map[string]any {
	out := make([]map[string]any, 0, len(omissions))
	for _, omission := range omissions {
		if strings.TrimSpace(omission) == "" {
			continue
		}
		out = append(out, map[string]any{"item_id": "artifact", "reason": omission})
	}
	return out
}

func v2MemoryPackConflictsContractOutput(prompts []skillpackservice.V2ConflictPrompt) []map[string]any {
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

func v2ImportMemoryPackOmissionsContractOutput(items []skillpackservice.V2ImportItemResult) []map[string]any {
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

func v2MemoryPackProcessingState(res *skillpackservice.V2ImportResult) string {
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

func v2RollbackBlockersContractOutput(conflicts []string) []map[string]any {
	out := make([]map[string]any, 0, len(conflicts))
	for _, conflict := range conflicts {
		out = append(out, map[string]any{
			"code":    "rollback_blocked",
			"message": conflict,
		})
	}
	return out
}

func v2DreamSummaryContractOutput(dream *domain.Dream) map[string]any {
	if dream == nil {
		return map[string]any{}
	}
	out := map[string]any{
		"hypothesis_id":           dream.DreamID,
		"subject_entity_id":       dream.SubjectEntityID,
		"predicate_key":           dream.PredicateKey,
		"statement":               dream.Hypothesis,
		"status":                  string(dream.Status),
		"source_relationship_ids": v2DreamOutputSourceIDs(dream, false),
		"generator_kind":          v2DreamOutputGeneratorKind(dream),
		"generator_version":       firstNonEmpty(dream.GeneratorVersion, dream.GeneratorModel, "dream-v2"),
		"created_at":              v2DreamOutputCreatedAt(dream),
	}
	if strings.TrimSpace(dream.ObjectEntityID) != "" {
		out["object_entity_id"] = dream.ObjectEntityID
	}
	if strings.TrimSpace(dream.ObjectValueID) != "" {
		out["object_value_id"] = dream.ObjectValueID
	}
	return out
}

func v2DreamContractOutput(dream *domain.Dream) map[string]any {
	out := v2DreamSummaryContractOutput(dream)
	if dream == nil {
		return out
	}
	out["source_owner_profile_ids"] = v2DreamOutputOwnerIDs(dream)
	out["rationale"] = firstNonEmpty(dream.Rationale, "No rationale supplied.")
	out["likelihood"] = dream.Likelihood
	out["confidence"] = dream.Confidence
	out["source_candidate_relationship_ids"] = v2DreamOutputSourceIDs(dream, true)
	out["source_versions"] = v2DreamOutputSourceVersions(dream)
	return out
}

func v2DreamOutputOwnerIDs(dream *domain.Dream) []string {
	if len(dream.SourceOwnerProfileIDs) > 0 {
		return append([]string(nil), dream.SourceOwnerProfileIDs...)
	}
	if strings.TrimSpace(dream.ProfileID) != "" {
		return []string{dream.ProfileID}
	}
	return []string{}
}

func v2DreamOutputSourceIDs(dream *domain.Dream, candidates bool) []string {
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

func v2DreamOutputSourceVersions(dream *domain.Dream) map[string]int {
	if dream.SourceVersions == nil {
		return map[string]int{}
	}
	out := make(map[string]int, len(dream.SourceVersions))
	for key, value := range dream.SourceVersions {
		out[key] = value
	}
	return out
}

func v2DreamOutputGeneratorKind(dream *domain.Dream) string {
	if strings.TrimSpace(dream.GeneratorKind) == "provider" {
		return "provider"
	}
	return "deterministic"
}

func v2DreamOutputCreatedAt(dream *domain.Dream) string {
	createdAt := dream.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Unix(0, 0).UTC()
	}
	return createdAt.UTC().Format(time.RFC3339Nano)
}
