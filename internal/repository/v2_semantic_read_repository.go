package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

const (
	v2DefaultTraceDepth         = 1
	v2MaxTraceDepth             = 4
	v2DefaultTraceEdges         = 24
	v2MaxTraceEdges             = 100
	v2DefaultTraceEvents        = 100
	v2MaxTraceEvents            = 500
	v2DefaultTraceFragmentRunes = 2000
	v2MaxTraceFragmentRunes     = 8000
	v2DefaultSemanticGraphLimit = 80
	v2MaxSemanticGraphLimit     = 180
	v2DefaultSemanticGraphDepth = 1
	v2MaxSemanticGraphDepth     = 2
)

func (r *V2SemanticRepositoryImpl) TraceRelationship(
	ctx context.Context,
	input V2TraceRelationshipInput,
) (*V2RelationshipTraceResult, error) {
	input = normalizeV2TraceRelationshipInput(input)
	if err := validateV2TraceRelationshipInput(input); err != nil {
		return nil, err
	}
	result := &V2RelationshipTraceResult{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		relationship, err := loadV2TraceRelationship(ctx, tx, input.TeamID, input.RelationshipID)
		if err != nil {
			return err
		}
		result.Relationship = relationship

		observations, err := loadV2TraceObservations(ctx, tx, input)
		if err != nil {
			return err
		}
		result.Observations = observations
		observationIDs := v2TraceObservationIDs(observations)

		supports, err := loadV2TraceSupports(ctx, tx, input)
		if err != nil {
			return err
		}
		result.EvidenceSupports = supports
		fragmentIDs := v2TraceSupportFragmentIDs(supports)

		decisions, err := loadV2TraceSupportDecisions(ctx, tx, input)
		if err != nil {
			return err
		}
		result.SupportDecisionEvents = decisions

		evidence, err := loadV2TraceEvidenceFragments(ctx, tx, input, fragmentIDs)
		if err != nil {
			return err
		}
		result.EvidenceFragments = evidence

		if v2BoolDefault(input.IncludeVerification, true) {
			verification, err := loadV2TraceVerificationEvents(ctx, tx, input)
			if err != nil {
				return err
			}
			result.VerificationEvents = verification
		}
		if v2BoolDefault(input.IncludeTransitions, true) {
			transitions, err := loadV2TraceTransitions(ctx, tx, input)
			if err != nil {
				return err
			}
			result.Transitions = transitions
		}
		conflicts, err := loadV2RelationshipConflictRecords(ctx, tx, input.TeamID, []string{relationship.RelationshipID})
		if err != nil {
			return err
		}
		result.Conflicts = conflicts
		crossRefs, err := loadV2TraceCrossReferences(ctx, tx, input)
		if err != nil {
			return err
		}
		result.CrossProfileReferences = crossRefs

		corrections, err := loadV2TraceIdentityCorrections(ctx, tx, input.TeamID, observationIDs, input.MaxEvents)
		if err != nil {
			return err
		}
		result.IdentityCorrections = corrections

		lineage, err := loadV2TraceSupersessionLineage(ctx, tx, input.TeamID, relationship, input.MaxEvents)
		if err != nil {
			return err
		}
		result.SupersessionLineage = lineage

		searchDocs, err := loadV2TraceSearchDocuments(ctx, tx, input.TeamID, input.RelationshipID, fragmentIDs, input.MaxEvents)
		if err != nil {
			return err
		}
		result.SearchDocuments = searchDocs

		jobs, err := loadV2TraceEmbeddingJobs(ctx, tx, input.TeamID, v2TraceSearchDocumentIDs(searchDocs), input.MaxEvents)
		if err != nil {
			return err
		}
		result.EmbeddingJobs = jobs

		nodes, edges, err := loadV2TraceGraphContext(ctx, tx, relationship, input)
		if err != nil {
			return err
		}
		result.SemanticNodes = nodes
		result.SemanticEdges = edges
		result.VisitedEntityIDs = v2TraceVisitedEntityIDs(relationship, nodes)
		if len(edges) >= input.MaxEdges {
			result.Truncated = true
			result.StoppedReason = "max_edges"
		}
		if len(observations) >= input.MaxEvents || len(supports) >= input.MaxEvents ||
			len(decisions) >= input.MaxEvents || len(result.VerificationEvents) >= input.MaxEvents ||
			len(result.Transitions) >= input.MaxEvents {
			result.Truncated = true
			if result.StoppedReason == "" {
				result.StoppedReason = "max_events"
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic trace: %w", err)
	}
	return result, nil
}

func (r *V2SemanticRepositoryImpl) SemanticGraph(
	ctx context.Context,
	input V2SemanticGraphQuery,
) (*V2SemanticGraphSnapshot, error) {
	input = normalizeV2SemanticGraphQuery(input)
	if err := validateV2SemanticGraphQuery(input); err != nil {
		return nil, err
	}
	var rows []v2SemanticGraphEdgeRow
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		if input.Scope == "local" {
			rows, err = loadV2SemanticLocalGraphRows(ctx, tx, input)
		} else {
			rows, err = loadV2SemanticOverviewGraphRows(ctx, tx, input)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic graph: %w", err)
	}
	return v2SemanticGraphSnapshot(input, rows), nil
}

func (r *V2SemanticRepositoryImpl) SemanticGraphNodeDetail(
	ctx context.Context,
	input V2SemanticGraphNodeDetailInput,
) (*V2SemanticGraphNode, error) {
	input = normalizeV2SemanticGraphNodeDetailInput(input)
	if err := validateV2SemanticGraphNodeDetailInput(input); err != nil {
		return nil, err
	}
	var node *V2SemanticGraphNode
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		switch input.NodeType {
		case "entity":
			node, err = loadV2SemanticEntityGraphNode(ctx, tx, input.TeamID, input.NodeID)
		case "value":
			node, err = loadV2SemanticValueGraphNode(ctx, tx, input.TeamID, input.NodeID)
		default:
			return sql.ErrNoRows
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("v2 semantic graph node: %w", err)
	}
	return node, nil
}

type v2SemanticGraphEdgeRow struct {
	source V2SemanticGraphNode
	target V2SemanticGraphNode
	edge   V2SemanticGraphEdge
}

func normalizeV2TraceRelationshipInput(input V2TraceRelationshipInput) V2TraceRelationshipInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.RelationshipID = strings.TrimSpace(input.RelationshipID)
	input.Topic = strings.TrimSpace(input.Topic)
	input.MaxDepth = clampV2Int(input.MaxDepth, v2DefaultTraceDepth, v2MaxTraceDepth)
	input.MaxEdges = clampV2Int(input.MaxEdges, v2DefaultTraceEdges, v2MaxTraceEdges)
	input.MaxEvents = clampV2Int(input.MaxEvents, v2DefaultTraceEvents, v2MaxTraceEvents)
	input.MaxFragmentContentRunes = clampV2Int(input.MaxFragmentContentRunes, v2DefaultTraceFragmentRunes, v2MaxTraceFragmentRunes)
	input.PredicateKeys = normalizeV2TracePredicateKeys(input.PredicateKeys)
	input.MinRelevance = normalizeV2OptionalRelevance(input.MinRelevance)
	return input
}

func validateV2TraceRelationshipInput(input V2TraceRelationshipInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.RelationshipID); err != nil {
		return fmt.Errorf("relationship_id is required: %w", err)
	}
	return nil
}

func normalizeV2SemanticGraphQuery(input V2SemanticGraphQuery) V2SemanticGraphQuery {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	if input.Scope == "" || input.Scope != "local" {
		input.Scope = "overview"
	}
	input.Query = strings.ToLower(strings.TrimSpace(input.Query))
	input.AnchorType = normalizeV2SemanticGraphNodeType(input.AnchorType)
	input.AnchorID = strings.TrimSpace(input.AnchorID)
	input.Types = normalizeSemanticGraphTypes(input.Types)
	input.Depth = clampV2Int(input.Depth, v2DefaultSemanticGraphDepth, v2MaxSemanticGraphDepth)
	input.Limit = clampV2Int(input.Limit, v2DefaultSemanticGraphLimit, v2MaxSemanticGraphLimit)
	input.MinRelevance = normalizeV2Relevance(input.MinRelevance)
	return input
}

func validateV2SemanticGraphQuery(input V2SemanticGraphQuery) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.Scope == "local" {
		if input.AnchorType == "" {
			return errors.New("anchor_type is required for local graph")
		}
		if _, err := uuid.Parse(input.AnchorID); err != nil {
			return fmt.Errorf("anchor_id is required for local graph: %w", err)
		}
	}
	return nil
}

func normalizeV2SemanticGraphNodeDetailInput(input V2SemanticGraphNodeDetailInput) V2SemanticGraphNodeDetailInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.NodeType = normalizeV2SemanticGraphNodeType(input.NodeType)
	input.NodeID = strings.TrimSpace(input.NodeID)
	return input
}

func validateV2SemanticGraphNodeDetailInput(input V2SemanticGraphNodeDetailInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if input.NodeType == "" {
		return errors.New("node_type must be entity or value")
	}
	if _, err := uuid.Parse(input.NodeID); err != nil {
		return fmt.Errorf("node_id is required: %w", err)
	}
	return nil
}

func loadV2TraceRelationship(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipID string,
) (*V2RelationshipTraceRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT r.team_id::text, r.relationship_id::text, r.owner_profile_id::text,
		       r.semantic_group_key, r.subject_entity_id::text,
		       COALESCE(subject_name.display_name, r.subject_entity_id::text),
		       subject.entity_kind,
		       r.predicate_key, r.predicate_version,
		       COALESCE(r.object_entity_id::text, ''),
		       COALESCE(object_name.display_name, ''),
		       COALESCE(object.entity_kind, ''),
		       COALESCE(r.object_value_id::text, ''),
		       COALESCE(NULLIF(value.display, ''), value.canonical_value, ''),
		       COALESCE(value.value_type, ''),
		       r.relationship_kind, r.current_cardinality, r.tier, r.status,
		       r.polarity, COALESCE(r.scope_key, ''), r.valid_from, r.valid_to,
		       r.support_count, r.source_group_count, r.version,
		       r.created_at, r.updated_at, r.recorded_to
		FROM relationship_records r
		JOIN entity_records subject
		  ON subject.team_id = r.team_id AND subject.entity_id = r.subject_entity_id
		LEFT JOIN LATERAL (
		  SELECT display_name
		  FROM entity_names
		  WHERE team_id = r.team_id
		    AND entity_id = r.subject_entity_id
		    AND name_kind = 'canonical'
		    AND valid_to IS NULL
		  ORDER BY created_at DESC, entity_name_id DESC
		  LIMIT 1
		) subject_name ON true
		LEFT JOIN entity_records object
		  ON object.team_id = r.team_id AND object.entity_id = r.object_entity_id
		LEFT JOIN LATERAL (
		  SELECT display_name
		  FROM entity_names
		  WHERE team_id = r.team_id
		    AND entity_id = r.object_entity_id
		    AND name_kind = 'canonical'
		    AND valid_to IS NULL
		  ORDER BY created_at DESC, entity_name_id DESC
		  LIMIT 1
		) object_name ON true
		LEFT JOIN value_records value
		  ON value.team_id = r.team_id AND value.value_id = r.object_value_id
		WHERE r.team_id = ?::uuid
		  AND r.relationship_id = ?::uuid
		LIMIT 1
	`, teamID, relationshipID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	var record V2RelationshipTraceRecord
	var validFrom, validTo, recordedTo sql.NullTime
	if err := rows.Scan(
		&record.TeamID, &record.RelationshipID, &record.OwnerProfileID,
		&record.SemanticGroupKey, &record.SubjectEntityID, &record.SubjectName,
		&record.SubjectKind, &record.PredicateKey, &record.PredicateVersion,
		&record.ObjectEntityID, &record.ObjectEntityName, &record.ObjectEntityKind,
		&record.ObjectValueID, &record.ObjectValue, &record.ObjectValueType,
		&record.RelationshipKind, &record.CurrentCardinality, &record.Tier, &record.Status,
		&record.Polarity, &record.ScopeKey, &validFrom, &validTo,
		&record.SupportCount, &record.SourceGroupCount, &record.Version,
		&record.CreatedAt, &record.UpdatedAt, &recordedTo,
	); err != nil {
		return nil, err
	}
	record.ValidFrom = v2TimePtr(validFrom)
	record.ValidTo = v2TimePtr(validTo)
	record.RecordedTo = v2TimePtr(recordedTo)
	return &record, rows.Err()
}

func loadV2TraceObservations(
	ctx context.Context,
	tx *gorm.DB,
	input V2TraceRelationshipInput,
) ([]V2RelationshipObservationRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT observation_id::text, relationship_id::text, ingest_id::text,
		       COALESCE(placement_item_id::text, ''), owner_profile_id::text,
		       subject_ref, original_predicate, object_ref,
		       COALESCE(subject_entity_id::text, ''), COALESCE(predicate_key, ''),
		       COALESCE(predicate_version, 0), COALESCE(object_entity_id::text, ''),
		       COALESCE(object_value_id::text, ''), polarity, COALESCE(scope_key, ''),
		       valid_from, valid_to, evidence::text, metadata::text, created_at
		FROM relationship_observations
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		ORDER BY created_at ASC, observation_id ASC
		LIMIT ?
	`, input.TeamID, input.RelationshipID, input.MaxEvents).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []V2RelationshipObservationRecord
	for rows.Next() {
		var row V2RelationshipObservationRecord
		var validFrom, validTo sql.NullTime
		var evidenceJSON, metadataJSON string
		if err := rows.Scan(
			&row.ObservationID, &row.RelationshipID, &row.IngestID,
			&row.PlacementItemID, &row.OwnerProfileID, &row.SubjectRef,
			&row.OriginalPredicate, &row.ObjectRef, &row.SubjectEntityID,
			&row.PredicateKey, &row.PredicateVersion, &row.ObjectEntityID,
			&row.ObjectValueID, &row.Polarity, &row.ScopeKey, &validFrom,
			&validTo, &evidenceJSON, &metadataJSON, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.ValidFrom = v2TimePtr(validFrom)
		row.ValidTo = v2TimePtr(validTo)
		row.Evidence = v2JSON(evidenceJSON)
		row.Metadata = v2JSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadV2TraceSupports(
	ctx context.Context,
	tx *gorm.DB,
	input V2TraceRelationshipInput,
) ([]V2RelationshipEvidenceSupportRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT support_id::text, relationship_id::text, observation_id::text,
		       verification_event_id::text, fragment_id::text, owner_profile_id::text,
		       source_group_key, COALESCE(source_id::text, ''),
		       COALESCE(source_revision_id::text, ''), span_start, span_end,
		       quote, authority, metadata::text, created_at
		FROM relationship_evidence_supports
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		ORDER BY created_at ASC, support_id ASC
		LIMIT ?
	`, input.TeamID, input.RelationshipID, input.MaxEvents).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []V2RelationshipEvidenceSupportRecord
	for rows.Next() {
		var row V2RelationshipEvidenceSupportRecord
		var metadataJSON string
		if err := rows.Scan(
			&row.SupportID, &row.RelationshipID, &row.ObservationID,
			&row.VerificationEventID, &row.FragmentID, &row.OwnerProfileID,
			&row.SourceGroupKey, &row.SourceID, &row.SourceRevisionID,
			&row.SpanStart, &row.SpanEnd, &row.Quote, &row.Authority,
			&metadataJSON, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.Metadata = v2JSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadV2TraceSupportDecisions(
	ctx context.Context,
	tx *gorm.DB,
	input V2TraceRelationshipInput,
) ([]V2RelationshipSupportDecisionEvent, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT support_decision_id::text, support_id::text, relationship_id::text,
		       owner_profile_id::text, COALESCE(actor_profile_id::text, ''),
		       decision, reason, metadata::text, created_at
		FROM relationship_support_decision_events
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		ORDER BY created_at ASC, support_decision_id ASC
		LIMIT ?
	`, input.TeamID, input.RelationshipID, input.MaxEvents).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []V2RelationshipSupportDecisionEvent
	for rows.Next() {
		var row V2RelationshipSupportDecisionEvent
		var metadataJSON string
		if err := rows.Scan(&row.SupportDecisionID, &row.SupportID, &row.RelationshipID,
			&row.OwnerProfileID, &row.ActorProfileID, &row.Decision, &row.Reason,
			&metadataJSON, &row.CreatedAt); err != nil {
			return nil, err
		}
		row.Metadata = v2JSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadV2TraceEvidenceFragments(
	ctx context.Context,
	tx *gorm.DB,
	input V2TraceRelationshipInput,
	fragmentIDs []string,
) ([]V2TraceEvidenceFragment, error) {
	if len(fragmentIDs) == 0 {
		return nil, nil
	}
	includeContent := v2BoolDefault(input.IncludeEvidenceContent, true)
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT f.fragment_id::text, f.ingest_id::text, f.owner_profile_id::text,
		       COALESCE(f.source_id::text, ''), COALESCE(f.source_revision_id::text, ''),
		       COALESCE(src.source_key, ''), COALESCE(src.source_kind, ''),
		       COALESCE(rev.revision_token, ''), COALESCE(src.current_revision_id::text, ''),
		       f.evidence_index,
		       CASE WHEN ? THEN left(f.content, ?) ELSE '' END,
		       f.content_hash,
		       CASE WHEN ? THEN char_length(f.content) > ? ELSE false END,
		       f.source_type, f.authority, f.source_ref, f.labels,
		       f.metadata::text, f.created_at
		FROM evidence_fragments f
		LEFT JOIN evidence_sources src
		  ON src.team_id = f.team_id AND src.source_id = f.source_id
		LEFT JOIN evidence_source_revisions rev
		  ON rev.team_id = f.team_id AND rev.source_revision_id = f.source_revision_id
		WHERE f.team_id = ?::uuid
		  AND f.fragment_id = ANY(?::uuid[])
		ORDER BY f.evidence_index ASC, f.fragment_id ASC
	`, includeContent, input.MaxFragmentContentRunes, includeContent, input.MaxFragmentContentRunes,
		input.TeamID, pq.Array(fragmentIDs)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []V2TraceEvidenceFragment
	for rows.Next() {
		var row V2TraceEvidenceFragment
		var metadataJSON string
		if err := rows.Scan(
			&row.FragmentID, &row.IngestID, &row.OwnerProfileID,
			&row.SourceID, &row.SourceRevisionID, &row.SourceKey,
			&row.SourceKind, &row.RevisionToken, &row.CurrentRevisionID,
			&row.EvidenceIndex, &row.Content, &row.ContentHash,
			&row.ContentTruncated, &row.SourceType, &row.Authority,
			&row.SourceRef, pq.Array(&row.Labels), &metadataJSON, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.Metadata = v2JSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadV2TraceVerificationEvents(
	ctx context.Context,
	tx *gorm.DB,
	input V2TraceRelationshipInput,
) ([]V2RelationshipVerificationEvent, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT v.verification_event_id::text, v.observation_id::text,
		       v.owner_profile_id::text, v.evidence_verdict, v.confidence,
		       v.rationale, v.model, v.response_hash, v.metadata::text, v.created_at
		FROM verification_events v
		JOIN relationship_observations o
		  ON o.team_id = v.team_id AND o.observation_id = v.observation_id
		WHERE v.team_id = ?::uuid
		  AND o.relationship_id = ?::uuid
		ORDER BY v.created_at ASC, v.verification_event_id ASC
		LIMIT ?
	`, input.TeamID, input.RelationshipID, input.MaxEvents).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []V2RelationshipVerificationEvent
	for rows.Next() {
		var row V2RelationshipVerificationEvent
		var confidence sql.NullFloat64
		var metadataJSON string
		if err := rows.Scan(&row.VerificationEventID, &row.ObservationID,
			&row.OwnerProfileID, &row.EvidenceVerdict, &confidence,
			&row.Rationale, &row.Model, &row.ResponseHash, &metadataJSON,
			&row.CreatedAt); err != nil {
			return nil, err
		}
		if confidence.Valid {
			row.Confidence = &confidence.Float64
		}
		row.Metadata = v2JSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadV2TraceTransitions(
	ctx context.Context,
	tx *gorm.DB,
	input V2TraceRelationshipInput,
) ([]V2RelationshipTransitionEvent, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT transition_id::text, relationship_id::text, owner_profile_id::text,
		       COALESCE(from_tier, ''), COALESCE(from_status, ''), to_tier, to_status,
		       reason, COALESCE(verification_event_id::text, ''),
		       COALESCE(support_decision_id::text, ''), metadata::text, created_at
		FROM relationship_transition_events
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		ORDER BY created_at ASC, transition_id ASC
		LIMIT ?
	`, input.TeamID, input.RelationshipID, input.MaxEvents).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []V2RelationshipTransitionEvent
	for rows.Next() {
		var row V2RelationshipTransitionEvent
		var metadataJSON string
		if err := rows.Scan(
			&row.TransitionID, &row.RelationshipID, &row.OwnerProfileID,
			&row.FromTier, &row.FromStatus, &row.ToTier, &row.ToStatus,
			&row.Reason, &row.VerificationEventID, &row.SupportDecisionID,
			&metadataJSON, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.Metadata = v2JSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadV2TraceCrossReferences(
	ctx context.Context,
	tx *gorm.DB,
	input V2TraceRelationshipInput,
) ([]V2RelationshipCrossReferenceRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT cross_reference_id::text, author_profile_id::text,
		       source_relationship_id::text, source_relationship_version,
		       target_relationship_id::text, target_relationship_version,
		       kind, verification_event_id::text, metadata::text, created_at
		FROM relationship_cross_references
		WHERE team_id = ?::uuid
		  AND (
		    source_relationship_id = ?::uuid
		    OR target_relationship_id = ?::uuid
		  )
		ORDER BY created_at ASC, cross_reference_id ASC
		LIMIT ?
	`, input.TeamID, input.RelationshipID, input.RelationshipID, input.MaxEvents).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []V2RelationshipCrossReferenceRecord
	for rows.Next() {
		var row V2RelationshipCrossReferenceRecord
		var metadataJSON string
		if err := rows.Scan(
			&row.CrossReferenceID, &row.AuthorProfileID,
			&row.SourceRelationshipID, &row.SourceRelationshipVersion,
			&row.TargetRelationshipID, &row.TargetRelationshipVersion,
			&row.Kind, &row.VerificationEventID, &metadataJSON, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.Metadata = v2JSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadV2TraceIdentityCorrections(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	observationIDs []string,
	limit int,
) ([]V2EntityCorrectionEventRecord, error) {
	if len(observationIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT correction_event_id::text, owner_profile_id::text, action,
		       COALESCE(survivor_entity_id::text, ''), COALESCE(new_entity_id::text, ''),
		       selected_observation_ids::text[], reason, metadata::text, created_at
		FROM entity_correction_events
		WHERE team_id = ?::uuid
		  AND selected_observation_ids && ?::uuid[]
		ORDER BY created_at ASC, correction_event_id ASC
		LIMIT ?
	`, teamID, pq.Array(observationIDs), limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []V2EntityCorrectionEventRecord
	for rows.Next() {
		var row V2EntityCorrectionEventRecord
		var metadataJSON string
		if err := rows.Scan(
			&row.CorrectionEventID, &row.OwnerProfileID, &row.Action,
			&row.SurvivorEntityID, &row.NewEntityID,
			pq.Array(&row.SelectedObservationIDs), &row.Reason,
			&metadataJSON, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.Metadata = v2JSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadV2TraceSupersessionLineage(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationship *V2RelationshipTraceRecord,
	limit int,
) ([]V2RelationshipTraceRecord, error) {
	if relationship == nil || relationship.SemanticGroupKey == "" {
		return nil, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT relationship_id::text
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND semantic_group_key = ?
		  AND relationship_id <> ?::uuid
		  AND status IN ('superseded', 'retracted', 'disputed')
		ORDER BY updated_at DESC, relationship_id ASC
		LIMIT ?
	`, teamID, relationship.SemanticGroupKey, relationship.RelationshipID, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]V2RelationshipTraceRecord, 0, len(ids))
	for _, id := range ids {
		record, err := loadV2TraceRelationship(ctx, tx, teamID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *record)
	}
	return out, nil
}

func loadV2TraceSearchDocuments(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipID string,
	fragmentIDs []string,
	limit int,
) ([]V2TraceSearchDocument, error) {
	query := `
		SELECT search_document_id::text, owner_profile_id::text, source_kind,
		       source_id::text, source_version, document_version,
		       embedding_contract_id::text, embedding_dimensions, search_state,
		       document_hash, created_at, updated_at
		FROM search_documents
		WHERE team_id = ?::uuid
		  AND source_kind = 'relationship'
		  AND source_id = ?::uuid
		ORDER BY source_kind ASC, updated_at DESC, search_document_id ASC
		LIMIT ?
	`
	args := []any{teamID, relationshipID, limit}
	if len(fragmentIDs) > 0 {
		query = `
			SELECT search_document_id::text, owner_profile_id::text, source_kind,
			       source_id::text, source_version, document_version,
			       embedding_contract_id::text, embedding_dimensions, search_state,
			       document_hash, created_at, updated_at
			FROM search_documents
			WHERE team_id = ?::uuid
			  AND (
			    (source_kind = 'relationship' AND source_id = ?::uuid)
			    OR (source_kind = 'evidence' AND source_id = ANY(?::uuid[]))
			  )
			ORDER BY source_kind ASC, updated_at DESC, search_document_id ASC
			LIMIT ?
		`
		args = []any{teamID, relationshipID, pq.Array(fragmentIDs), limit}
	}
	rows, err := tx.WithContext(ctx).Raw(query, args...).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []V2TraceSearchDocument
	for rows.Next() {
		var row V2TraceSearchDocument
		if err := rows.Scan(
			&row.SearchDocumentID, &row.OwnerProfileID, &row.SourceKind,
			&row.SourceID, &row.SourceVersion, &row.DocumentVersion,
			&row.EmbeddingContractID, &row.EmbeddingDimensions, &row.SearchState,
			&row.DocumentHash, &row.CreatedAt, &row.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadV2TraceEmbeddingJobs(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	searchDocumentIDs []string,
	limit int,
) ([]V2TraceEmbeddingJob, error) {
	if len(searchDocumentIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT embedding_job_id::text, search_document_id::text, owner_profile_id::text,
		       source_kind, source_id::text, source_version, document_version,
		       embedding_contract_id::text, embedding_dimensions, status, attempts,
		       error, created_at, updated_at, completed_at
		FROM embedding_jobs
		WHERE team_id = ?::uuid
		  AND search_document_id = ANY(?::uuid[])
		ORDER BY created_at ASC, embedding_job_id ASC
		LIMIT ?
	`, teamID, pq.Array(searchDocumentIDs), limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []V2TraceEmbeddingJob
	for rows.Next() {
		var row V2TraceEmbeddingJob
		var completedAt sql.NullTime
		if err := rows.Scan(
			&row.EmbeddingJobID, &row.SearchDocumentID, &row.OwnerProfileID,
			&row.SourceKind, &row.SourceID, &row.SourceVersion, &row.DocumentVersion,
			&row.EmbeddingContractID, &row.EmbeddingDimensions, &row.Status,
			&row.Attempts, &row.Error, &row.CreatedAt, &row.UpdatedAt, &completedAt,
		); err != nil {
			return nil, err
		}
		row.CompletedAt = v2TimePtr(completedAt)
		out = append(out, row)
	}
	return out, rows.Err()
}

func v2TraceObservationIDs(observations []V2RelationshipObservationRecord) []string {
	out := make([]string, 0, len(observations))
	for _, observation := range observations {
		if observation.ObservationID != "" {
			out = append(out, observation.ObservationID)
		}
	}
	return out
}

func v2TraceSupportFragmentIDs(supports []V2RelationshipEvidenceSupportRecord) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(supports))
	for _, support := range supports {
		if support.FragmentID == "" {
			continue
		}
		if _, exists := seen[support.FragmentID]; exists {
			continue
		}
		seen[support.FragmentID] = struct{}{}
		out = append(out, support.FragmentID)
	}
	return out
}

func v2TraceSearchDocumentIDs(docs []V2TraceSearchDocument) []string {
	out := make([]string, 0, len(docs))
	for _, doc := range docs {
		if doc.SearchDocumentID != "" {
			out = append(out, doc.SearchDocumentID)
		}
	}
	return out
}

func v2TraceVisitedEntityIDs(relationship *V2RelationshipTraceRecord, nodes []V2SemanticGraphNode) []string {
	seen := map[string]struct{}{}
	out := []string{}
	add := func(id string) {
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	if relationship != nil {
		add(relationship.SubjectEntityID)
		add(relationship.ObjectEntityID)
	}
	for _, node := range nodes {
		if node.Type == "entity" {
			add(node.ID)
		}
	}
	return out
}

func v2JSONMap(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" || raw == "[]" {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err == nil {
		return out
	}
	return map[string]any{"raw": raw}
}

func v2JSON(raw string) any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out any
	if err := json.Unmarshal([]byte(raw), &out); err == nil {
		return out
	}
	return map[string]any{"raw": raw}
}

func v2BoolDefault(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}

func v2TimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}

func clampV2Int(value, defaultValue, maxValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func normalizeV2OptionalRelevance(value *float64) *float64 {
	if value == nil {
		return nil
	}
	normalized := normalizeV2Relevance(*value)
	return &normalized
}

func normalizeV2Relevance(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
