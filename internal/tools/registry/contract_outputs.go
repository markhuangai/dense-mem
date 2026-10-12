package registry

import (
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"

	"github.com/markhuangai/dense-mem/internal/graph/contract"
	"github.com/markhuangai/dense-mem/internal/memorypack"
	"github.com/markhuangai/dense-mem/internal/recall"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
	traceapp "github.com/markhuangai/dense-mem/internal/trace"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
)

func rememberRequestFromContractInput(input map[string]any) (rememberapp.RememberRequest, error) {
	var req rememberapp.RememberRequest
	if err := remapInput(input, &req); err != nil {
		return req, err
	}
	req.RelationshipHints = objectArray(input["relationships"])
	return req, nil
}

func recallContractOutput(res *recall.RecallResult) map[string]any {
	if res == nil {
		return map[string]any{
			"recall_id":             "",
			"results":               []any{},
			"conflicts":             []any{},
			"related_relationships": []any{},
			"related_communities":   []any{},
			"search_states": map[string]any{
				"evidence":      string(domain.SearchProjectionCurrent),
				"relationships": string(domain.SearchProjectionCurrent),
			},
			"degradations":      []any{},
			"suggested_actions": []any{},
		}
	}
	results := make([]map[string]any, 0, len(res.Results))
	for _, item := range res.Results {
		results = append(results, map[string]any{
			"evidence_id":             item.EvidenceID,
			"equivalent_evidence_ids": append([]string{}, item.EquivalentEvidenceIDs...),
			"equivalents_truncated":   item.EquivalentsTruncated,
			"context":                 item.Context,
			"space_kind":              item.SpaceKind,
		})
	}
	relatedRelationships := res.RelatedRelationships
	if relatedRelationships == nil {
		relatedRelationships = []recall.RelatedRelationshipSummary{}
	}
	relatedCommunities := res.RelatedCommunities
	if relatedCommunities == nil {
		relatedCommunities = []recall.RecallDiscoveryPath{}
	}
	conflicts := res.Conflicts
	if conflicts == nil {
		conflicts = []recall.RecallConflictSummary{}
	}
	degradations := res.Degradations
	if degradations == nil {
		degradations = []recall.RecallDegradationResult{}
	}
	suggestedActions := res.SuggestedActions
	if suggestedActions == nil {
		suggestedActions = []recall.RecallSuggestedAction{}
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
		"search_states":         searchStates,
		"degradations":          degradations,
		"suggested_actions":     suggestedActions,
	}
}

func traceContractOutput(trace *traceapp.SemanticTrace) (map[string]any, error) {
	if trace == nil {
		trace = &traceapp.SemanticTrace{}
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

func traceRelationshipOutput(record *tracecontract.RelationshipTraceRecord) map[string]any {
	if record == nil {
		return nil
	}
	return traceRelationshipRecordOutput(*record)
}

func traceRelationshipLineageOutputs(records []tracecontract.RelationshipTraceRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, traceRelationshipRecordOutput(record))
	}
	return out
}

func traceRelationshipRecordOutput(record tracecontract.RelationshipTraceRecord) map[string]any {
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

func traceObservationOutputs(records []tracecontract.RelationshipObservationRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"observation_id": record.ObservationID,
			"submission_id":  record.IngestID,
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

func traceEvidenceSupportOutputs(records []tracecontract.RelationshipEvidenceSupportRecord) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"evidence_support_id": record.SupportID,
			"relationship_id":     record.RelationshipID,
			"evidence_id":         record.FragmentID,
			"span_start":          record.SpanStart,
			"span_end":            record.SpanEnd,
		}
		putString(item, "occurrence_id", record.OccurrenceID)
		putString(item, "observation_id", record.ObservationID)
		putString(item, "verification_event_id", record.VerificationEventID)
		putString(item, "evidence_owner_profile_id", record.EvidenceOwnerProfileID)
		putString(item, "source_group_key", record.SourceGroupKey)
		putString(item, "quote", record.Quote)
		putString(item, "authority", record.Authority)
		putTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func traceSupportDecisionOutputs(records []tracecontract.RelationshipSupportDecisionEvent) []map[string]any {
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

func traceEvidenceOutputs(records []tracecontract.TraceEvidenceFragment) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		item := map[string]any{
			"evidence_id":       record.FragmentID,
			"submission_id":     record.IngestID,
			"evidence_index":    record.EvidenceIndex,
			"content_hash":      record.ContentHash,
			"content_truncated": record.ContentTruncated,
		}
		if record.Session != nil {
			provenance := record.Session
			item["session"] = map[string]any{"framework": provenance.Framework, "app_name": provenance.AppName, "user_id": provenance.UserID, "session_id": provenance.SessionID, "event_id": provenance.EventID, "event_index": provenance.EventIndex, "span_start": provenance.SpanStart, "span_end": provenance.SpanEnd}
			if provenance.OccurredAt != nil {
				item["session"].(map[string]any)["occurred_at"] = *provenance.OccurredAt
			}
		}
		putString(item, "occurrence_id", record.OccurrenceID)
		putString(item, "content", record.Content)
		putString(item, "source_type", record.SourceType)
		putString(item, "source", firstNonEmpty(record.SourceRef, record.SourceKey))
		putTime(item, "created_at", record.CreatedAt)
		out = append(out, item)
	}
	return out
}

func traceEvidenceLifecycleEventOutputs(records []tracecontract.TraceEvidenceLifecycleEvent) []map[string]any {
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

func traceVerificationOutputs(records []tracecontract.RelationshipVerificationEvent) []map[string]any {
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		var evidenceVerdict any
		if record.EvidenceVerdict != nil {
			evidenceVerdict = *record.EvidenceVerdict
		}
		item := map[string]any{
			"verification_event_id": record.VerificationEventID,
			"observation_id":        record.ObservationID,
			"evidence_verdict":      evidenceVerdict,
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

func traceTransitionOutputs(records []tracecontract.RelationshipTransitionEvent) []map[string]any {
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

func traceConflictOutputs(records []tracecontract.RelationshipConflictCaseRecord) []map[string]any {
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
	traceConflictSupporterLimit        = 20
	traceConflictRelationshipIDLimit   = 20
	traceConflictOwnerProfileIDLimit   = 20
	traceConflictResultEvidenceIDLimit = 50
)

func traceConflictPositionOutputs(records []domain.RelationshipConflictPositionRecord) []map[string]any {
	if len(records) > traceConflictPositionLimit {
		records = records[:traceConflictPositionLimit]
	}
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		supportersTruncated := record.SupportersTruncated || len(record.Supporters) > traceConflictSupporterLimit
		out = append(out, map[string]any{
			"position_id":          record.PositionID,
			"disposition":          record.Disposition,
			"supporter_count":      record.SupporterCount,
			"supporters_truncated": supportersTruncated,
			"supporters":           traceConflictSupporterOutputs(record.Supporters),
			"relationship_ids":     traceBoundedStringArray(record.RelationshipIDs, traceConflictRelationshipIDLimit),
			"owner_profile_ids":    traceBoundedStringArray(record.OwnerProfileIDs, traceConflictOwnerProfileIDLimit),
			"result_evidence_ids":  traceBoundedStringArray(record.EvidenceIDs, traceConflictResultEvidenceIDLimit),
		})
	}
	return out
}

func traceConflictSupporterOutputs(records []domain.RelationshipConflictSupporterRecord) []map[string]any {
	if len(records) > traceConflictSupporterLimit {
		records = records[:traceConflictSupporterLimit]
	}
	out := make([]map[string]any, 0, len(records))
	for _, record := range records {
		out = append(out, map[string]any{
			"profile_id":          record.ProfileID,
			"profile_name":        record.ProfileName,
			"strongest_authority": record.StrongestAuthority,
			"evidence_id":         record.EvidenceID,
			"accepted_at":         record.AcceptedAt.UTC().Format(time.RFC3339Nano),
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

func traceCrossProfileReferenceOutputs(records []tracecontract.RelationshipCrossReferenceRecord) []map[string]any {
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

func traceIdentityCorrectionOutputs(records []tracecontract.EntityCorrectionEventRecord) []map[string]any {
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

func traceSemanticNodeOutputs(records []contract.Node) []map[string]any {
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

func traceSemanticEdgeOutputs(records []contract.Edge) []map[string]any {
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

func exportMemoryPackContractOutput(res *memorypack.ExportResult) map[string]any {
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
