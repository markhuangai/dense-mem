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
	defaultTraceDepth         = 1
	maxTraceDepth             = 4
	defaultTraceEdges         = 24
	maxTraceEdges             = 100
	defaultTraceEvents        = 100
	maxTraceEvents            = 500
	defaultTraceFragmentRunes = 2000
	maxTraceFragmentRunes     = 8000
	defaultSemanticGraphLimit = 80
	defaultSemanticGraphDepth = 2
	maxSemanticGraphDepth     = 5
)

var ErrTraceRelationshipNotFound = errors.New("trace relationship not found")

func (r *SemanticRepositoryImpl) TraceRelationship(
	ctx context.Context,
	input TraceRelationshipInput,
) (*RelationshipTraceResult, error) {
	input = normalizeTraceRelationshipInput(input)
	if err := validateTraceRelationshipInput(input); err != nil {
		return nil, err
	}
	result := &RelationshipTraceResult{}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		relationship, err := loadTraceRelationship(ctx, tx, input.TeamID, input.RelationshipID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTraceRelationshipNotFound
		}
		if err != nil {
			return err
		}
		result.Relationship = relationship
		input.spaceID = relationship.SpaceID

		observations, err := loadTraceObservations(ctx, tx, input)
		if err != nil {
			return err
		}
		result.Observations = observations
		observationIDs := traceObservationIDs(observations)

		supports, err := loadTraceSupports(ctx, tx, input)
		if err != nil {
			return err
		}
		result.EvidenceSupports = supports
		fragmentIDs := traceSupportFragmentIDs(supports)

		decisions, err := loadTraceSupportDecisions(ctx, tx, input)
		if err != nil {
			return err
		}
		result.SupportDecisionEvents = decisions

		evidence, err := loadTraceEvidenceFragments(ctx, tx, input, fragmentIDs)
		if err != nil {
			return err
		}
		result.EvidenceFragments = evidence
		lifecycleEvents, err := loadTraceEvidenceLifecycleEvents(ctx, tx, input.TeamID, input.spaceID, fragmentIDs, input.MaxEvents)
		if err != nil {
			return err
		}
		result.EvidenceLifecycleEvents = lifecycleEvents

		if boolDefault(input.IncludeVerification, true) {
			verification, err := loadTraceVerificationEvents(ctx, tx, input)
			if err != nil {
				return err
			}
			result.VerificationEvents = verification
		}
		if boolDefault(input.IncludeTransitions, true) {
			transitions, err := loadTraceTransitions(ctx, tx, input)
			if err != nil {
				return err
			}
			result.Transitions = transitions
		}
		conflicts, err := loadRelationshipConflictRecordsInSpace(ctx, tx, input.TeamID, []string{relationship.RelationshipID}, nil, input.spaceID)
		if err != nil {
			return err
		}
		result.Conflicts = conflicts
		crossRefs, err := loadTraceCrossReferences(ctx, tx, input)
		if err != nil {
			return err
		}
		result.CrossProfileReferences = crossRefs

		corrections, err := loadTraceIdentityCorrections(ctx, tx, input.TeamID, input.spaceID, observationIDs, input.MaxEvents)
		if err != nil {
			return err
		}
		result.IdentityCorrections = corrections

		lineage, err := loadTraceSupersessionLineage(ctx, tx, input.TeamID, relationship, input.MaxEvents)
		if err != nil {
			return err
		}
		result.SupersessionLineage = lineage

		searchDocs, err := loadTraceSearchDocuments(ctx, tx, input.TeamID, input.spaceID, input.RelationshipID, fragmentIDs, input.MaxEvents)
		if err != nil {
			return err
		}
		result.SearchDocuments = searchDocs

		nodes, edges, err := loadTraceGraphContext(ctx, tx, relationship, input)
		if err != nil {
			return err
		}
		result.SemanticNodes = nodes
		result.SemanticEdges = edges
		result.VisitedEntityIDs = traceVisitedEntityIDs(relationship, nodes)
		if len(edges) >= input.MaxEdges {
			result.Truncated = true
			result.StoppedReason = "max_edges"
		}
		if len(observations) >= input.MaxEvents || len(supports) >= input.MaxEvents ||
			len(decisions) >= input.MaxEvents || len(result.VerificationEvents) >= input.MaxEvents ||
			len(result.Transitions) >= input.MaxEvents || len(lifecycleEvents) >= input.MaxEvents {
			result.Truncated = true
			if result.StoppedReason == "" {
				result.StoppedReason = "max_events"
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("semantic trace: %w", err)
	}
	return result, nil
}

func (r *SemanticRepositoryImpl) SemanticGraph(
	ctx context.Context,
	input SemanticGraphQuery,
) (*SemanticGraphSnapshot, error) {
	input = normalizeSemanticGraphQuery(input)
	if err := validateSemanticGraphQuery(input); err != nil {
		return nil, err
	}
	var rows []semanticGraphEdgeRow
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		if input.Scope == "local" {
			rows, err = loadSemanticLocalGraphRows(ctx, tx, input)
		} else {
			rows, err = loadSemanticOverviewGraphRows(ctx, tx, input)
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("semantic graph: %w", err)
	}
	return semanticGraphSnapshot(input, rows), nil
}

func (r *SemanticRepositoryImpl) SemanticGraphNodeDetail(
	ctx context.Context,
	input SemanticGraphNodeDetailInput,
) (*SemanticGraphNode, error) {
	input = normalizeSemanticGraphNodeDetailInput(input)
	if err := validateSemanticGraphNodeDetailInput(input); err != nil {
		return nil, err
	}
	var node *SemanticGraphNode
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		var err error
		switch input.NodeType {
		case "entity":
			node, err = loadSemanticEntityGraphNode(ctx, tx, input.TeamID, input.NodeID)
		case "value":
			node, err = loadSemanticValueGraphNode(ctx, tx, input.TeamID, input.NodeID)
		default:
			return sql.ErrNoRows
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("semantic graph node: %w", err)
	}
	return node, nil
}

type semanticGraphEdgeRow struct {
	source SemanticGraphNode
	target SemanticGraphNode
	edge   SemanticGraphEdge
}

func normalizeTraceRelationshipInput(input TraceRelationshipInput) TraceRelationshipInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.RelationshipID = strings.TrimSpace(input.RelationshipID)
	input.Topic = strings.TrimSpace(input.Topic)
	input.MaxDepth = clampInt(input.MaxDepth, defaultTraceDepth, maxTraceDepth)
	input.MaxEdges = clampInt(input.MaxEdges, defaultTraceEdges, maxTraceEdges)
	input.MaxEvents = clampInt(input.MaxEvents, defaultTraceEvents, maxTraceEvents)
	input.MaxFragmentContentRunes = clampInt(input.MaxFragmentContentRunes, defaultTraceFragmentRunes, maxTraceFragmentRunes)
	input.PredicateKeys = normalizeTracePredicateKeys(input.PredicateKeys)
	input.MinRelevance = normalizeOptionalRelevance(input.MinRelevance)
	return input
}

func validateTraceRelationshipInput(input TraceRelationshipInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.RelationshipID); err != nil {
		return fmt.Errorf("relationship_id is required: %w", err)
	}
	return nil
}

func normalizeSemanticGraphQuery(input SemanticGraphQuery) SemanticGraphQuery {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.spaceID = strings.TrimSpace(input.spaceID)
	input.Scope = strings.ToLower(strings.TrimSpace(input.Scope))
	if input.Scope == "" || input.Scope != "local" {
		input.Scope = "overview"
	}
	input.Query = strings.ToLower(strings.TrimSpace(input.Query))
	input.AnchorType = normalizeSemanticGraphNodeType(input.AnchorType)
	input.AnchorID = strings.TrimSpace(input.AnchorID)
	input.Types = normalizeSemanticGraphTypes(input.Types)
	input.Depth = clampInt(input.Depth, defaultSemanticGraphDepth, maxSemanticGraphDepth)
	input.Limit = defaultPositiveInt(input.Limit, defaultSemanticGraphLimit)
	input.MinRelevance = normalizeRelevance(input.MinRelevance)
	return input
}

func validateSemanticGraphQuery(input SemanticGraphQuery) error {
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

func normalizeSemanticGraphNodeDetailInput(input SemanticGraphNodeDetailInput) SemanticGraphNodeDetailInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.NodeType = normalizeSemanticGraphNodeType(input.NodeType)
	input.NodeID = strings.TrimSpace(input.NodeID)
	return input
}

func validateSemanticGraphNodeDetailInput(input SemanticGraphNodeDetailInput) error {
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

func loadTraceRelationship(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationshipID string,
) (*RelationshipTraceRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT r.team_id::text, r.relationship_id::text, r.owner_profile_id::text,
		       r.space_id::text,
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
		       r.relationship_kind, r.current_cardinality, r.status,
		       r.polarity, COALESCE(r.scope_key, ''), r.valid_from, r.valid_to,
		       COALESCE(r.identity_alias_of_relationship_id::text, ''),
		       r.support_count, r.source_group_count, r.version,
		       r.created_at, r.updated_at, r.recorded_to
		FROM relationship_records r
		JOIN entity_records subject
		  ON subject.team_id = r.team_id
		 AND subject.entity_id = r.subject_entity_id
		 AND subject.space_id = r.space_id
		 AND `+activeSemanticSpaceGenerationSQL("subject")+`
		LEFT JOIN LATERAL (
		  SELECT display_name
		  FROM entity_names
		  WHERE team_id = r.team_id
		    AND entity_id = r.subject_entity_id
		    AND space_id = r.space_id
		    AND `+activeSemanticSpaceGenerationSQL("entity_names")+`
		    AND name_kind = 'canonical'
		    AND valid_to IS NULL
		  ORDER BY created_at DESC, entity_name_id DESC
		  LIMIT 1
		) subject_name ON true
		LEFT JOIN entity_records object
		  ON object.team_id = r.team_id
		 AND object.entity_id = r.object_entity_id
		 AND object.space_id = r.space_id
		 AND `+activeSemanticSpaceGenerationSQL("object")+`
		LEFT JOIN LATERAL (
		  SELECT display_name
		  FROM entity_names
		  WHERE team_id = r.team_id
		    AND entity_id = r.object_entity_id
		    AND space_id = r.space_id
		    AND `+activeSemanticSpaceGenerationSQL("entity_names")+`
		    AND name_kind = 'canonical'
		    AND valid_to IS NULL
		  ORDER BY created_at DESC, entity_name_id DESC
		  LIMIT 1
		) object_name ON true
		LEFT JOIN value_records value
		  ON value.team_id = r.team_id
		 AND value.value_id = r.object_value_id
		 AND value.space_id = r.space_id
		 AND `+activeSemanticSpaceGenerationSQL("value")+`
		WHERE r.team_id = ?::uuid
		  AND r.relationship_id = ?::uuid
		  AND (
		    r.space_id = dense_mem_team_shared_space(r.team_id)
		    OR dense_mem_space_allowed(r.space_id)
		  )
		  AND `+activeSemanticSpaceGenerationSQL("r")+`
		LIMIT 1
	`, teamID, relationshipID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	var record RelationshipTraceRecord
	var validFrom, validTo, recordedTo sql.NullTime
	if err := rows.Scan(
		&record.TeamID, &record.RelationshipID, &record.OwnerProfileID, &record.SpaceID,
		&record.SemanticGroupKey, &record.SubjectEntityID, &record.SubjectName,
		&record.SubjectKind, &record.PredicateKey, &record.PredicateVersion,
		&record.ObjectEntityID, &record.ObjectEntityName, &record.ObjectEntityKind,
		&record.ObjectValueID, &record.ObjectValue, &record.ObjectValueType,
		&record.RelationshipKind, &record.CurrentCardinality, &record.Status,
		&record.Polarity, &record.ScopeKey, &validFrom, &validTo,
		&record.IdentityAliasOfID,
		&record.SupportCount, &record.SourceGroupCount, &record.Version,
		&record.CreatedAt, &record.UpdatedAt, &recordedTo,
	); err != nil {
		return nil, err
	}
	record.ValidFrom = timePtr(validFrom)
	record.ValidTo = timePtr(validTo)
	record.RecordedTo = timePtr(recordedTo)
	return &record, rows.Err()
}

func loadTraceObservations(
	ctx context.Context,
	tx *gorm.DB,
	input TraceRelationshipInput,
) ([]RelationshipObservationRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT observation_id::text, relationship_id::text, ingest_id::text,
		       owner_profile_id::text,
		       subject_ref, original_predicate, object_ref,
		       COALESCE(subject_entity_id::text, ''), COALESCE(predicate_key, ''),
		       COALESCE(predicate_version, 0), COALESCE(object_entity_id::text, ''),
		       COALESCE(object_value_id::text, ''), polarity, COALESCE(scope_key, ''),
		       valid_from, valid_to, evidence::text, metadata::text, created_at
		FROM relationship_observations
		WHERE team_id = ?::uuid
		  AND relationship_id = ?::uuid
		  AND space_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("relationship_observations")+`
		ORDER BY created_at ASC, observation_id ASC
		LIMIT ?
	`, input.TeamID, input.RelationshipID, input.spaceID, input.MaxEvents).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelationshipObservationRecord
	for rows.Next() {
		var row RelationshipObservationRecord
		var validFrom, validTo sql.NullTime
		var evidenceJSON, metadataJSON string
		if err := rows.Scan(
			&row.ObservationID, &row.RelationshipID, &row.IngestID,
			&row.OwnerProfileID, &row.SubjectRef,
			&row.OriginalPredicate, &row.ObjectRef, &row.SubjectEntityID,
			&row.PredicateKey, &row.PredicateVersion, &row.ObjectEntityID,
			&row.ObjectValueID, &row.Polarity, &row.ScopeKey, &validFrom,
			&validTo, &evidenceJSON, &metadataJSON, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.ValidFrom = timePtr(validFrom)
		row.ValidTo = timePtr(validTo)
		row.Evidence = jSON(evidenceJSON)
		row.Metadata = jSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadTraceSupports(
	ctx context.Context,
	tx *gorm.DB,
	input TraceRelationshipInput,
) ([]RelationshipEvidenceSupportRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT support_id::text, relationship_id::text, observation_id::text,
		       verification_event_id::text, fragment_id::text, owner_profile_id::text,
		       evidence_owner_profile_id::text,
		       source_group_key, COALESCE(source_id::text, ''),
		       COALESCE(source_revision_id::text, ''), span_start, span_end,
		       quote, authority, metadata::text, created_at
		FROM relationship_evidence_supports AS support
		WHERE support.team_id = ?::uuid
		  AND support.relationship_id = ?::uuid
		  AND support.space_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("support")+`
		ORDER BY support.created_at ASC, support.support_id ASC
		LIMIT ?
	`, input.TeamID, input.RelationshipID, input.spaceID, input.MaxEvents).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelationshipEvidenceSupportRecord
	for rows.Next() {
		var row RelationshipEvidenceSupportRecord
		var metadataJSON string
		if err := rows.Scan(
			&row.SupportID, &row.RelationshipID, &row.ObservationID,
			&row.VerificationEventID, &row.FragmentID, &row.OwnerProfileID,
			&row.EvidenceOwnerProfileID,
			&row.SourceGroupKey, &row.SourceID, &row.SourceRevisionID,
			&row.SpanStart, &row.SpanEnd, &row.Quote, &row.Authority,
			&metadataJSON, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.Metadata = jSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadTraceSupportDecisions(
	ctx context.Context,
	tx *gorm.DB,
	input TraceRelationshipInput,
) ([]RelationshipSupportDecisionEvent, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT support_decision_id::text, support_id::text, relationship_id::text,
		       owner_profile_id::text, COALESCE(actor_profile_id::text, ''),
		       decision, reason, metadata::text, created_at
		FROM relationship_support_decision_events AS decision
		WHERE decision.team_id = ?::uuid
		  AND decision.relationship_id = ?::uuid
		  AND decision.space_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("decision")+`
		ORDER BY decision.created_at ASC, decision.support_decision_id ASC
		LIMIT ?
	`, input.TeamID, input.RelationshipID, input.spaceID, input.MaxEvents).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelationshipSupportDecisionEvent
	for rows.Next() {
		var row RelationshipSupportDecisionEvent
		var metadataJSON string
		if err := rows.Scan(&row.SupportDecisionID, &row.SupportID, &row.RelationshipID,
			&row.OwnerProfileID, &row.ActorProfileID, &row.Decision, &row.Reason,
			&metadataJSON, &row.CreatedAt); err != nil {
			return nil, err
		}
		row.Metadata = jSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadTraceEvidenceFragments(
	ctx context.Context,
	tx *gorm.DB,
	input TraceRelationshipInput,
	fragmentIDs []string,
) ([]TraceEvidenceFragment, error) {
	if len(fragmentIDs) == 0 {
		return nil, nil
	}
	includeContent := boolDefault(input.IncludeEvidenceContent, true)
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
		  ON src.team_id = f.team_id
		 AND src.source_id = f.source_id
		 AND src.space_id = f.space_id
		LEFT JOIN evidence_source_revisions rev
		  ON rev.team_id = f.team_id
		 AND rev.source_revision_id = f.source_revision_id
		 AND rev.space_id = f.space_id
		WHERE f.team_id = ?::uuid
		  AND f.fragment_id = ANY(?::uuid[])
		  AND f.space_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("f")+`
		  AND (
		    src.source_id IS NULL
		    OR `+activeSemanticSpaceGenerationSQL("src")+`
		  )
		  AND (
		    rev.source_revision_id IS NULL
		    OR `+activeSemanticSpaceGenerationSQL("rev")+`
		  )
		ORDER BY f.evidence_index ASC, f.fragment_id ASC
	`, includeContent, input.MaxFragmentContentRunes, includeContent, input.MaxFragmentContentRunes,
		input.TeamID, pq.Array(fragmentIDs), input.spaceID).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TraceEvidenceFragment
	for rows.Next() {
		var row TraceEvidenceFragment
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
		row.Metadata = jSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadTraceVerificationEvents(
	ctx context.Context,
	tx *gorm.DB,
	input TraceRelationshipInput,
) ([]RelationshipVerificationEvent, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT v.verification_event_id::text, v.observation_id::text,
		       v.owner_profile_id::text, COALESCE(v.evidence_verdict, ''), v.confidence,
		       COALESCE(v.rationale, ''), v.model, v.response_hash, v.metadata::text, v.created_at
		FROM verification_events v
		JOIN relationship_observations o
		  ON o.team_id = v.team_id
		 AND o.observation_id = v.observation_id
		 AND o.space_id = v.space_id
		WHERE v.team_id = ?::uuid
		  AND o.relationship_id = ?::uuid
		  AND v.space_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("v")+`
		  AND `+activeSemanticSpaceGenerationSQL("o")+`
		ORDER BY v.created_at ASC, v.verification_event_id ASC
		LIMIT ?
	`, input.TeamID, input.RelationshipID, input.spaceID, input.MaxEvents).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelationshipVerificationEvent
	for rows.Next() {
		var row RelationshipVerificationEvent
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
		row.Metadata = jSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadTraceTransitions(
	ctx context.Context,
	tx *gorm.DB,
	input TraceRelationshipInput,
) ([]RelationshipTransitionEvent, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT transition_id::text, relationship_id::text, owner_profile_id::text,
		       COALESCE(from_status, ''), to_status,
		       reason, COALESCE(verification_event_id::text, ''),
		       COALESCE(support_decision_id::text, ''), metadata::text, created_at
		FROM relationship_transition_events AS transition
		WHERE transition.team_id = ?::uuid
		  AND transition.relationship_id = ?::uuid
		  AND transition.space_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("transition")+`
		ORDER BY transition.created_at ASC, transition.transition_id ASC
		LIMIT ?
	`, input.TeamID, input.RelationshipID, input.spaceID, input.MaxEvents).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelationshipTransitionEvent
	for rows.Next() {
		var row RelationshipTransitionEvent
		var metadataJSON string
		if err := rows.Scan(
			&row.TransitionID, &row.RelationshipID, &row.OwnerProfileID,
			&row.FromStatus, &row.ToStatus, &row.Reason, &row.VerificationEventID, &row.SupportDecisionID,
			&metadataJSON, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.Metadata = jSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadTraceCrossReferences(
	ctx context.Context,
	tx *gorm.DB,
	input TraceRelationshipInput,
) ([]RelationshipCrossReferenceRecord, error) {
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT cross_reference.cross_reference_id::text, cross_reference.author_profile_id::text,
		       cross_reference.source_relationship_id::text, cross_reference.source_relationship_version,
		       cross_reference.target_relationship_id::text, cross_reference.target_relationship_version,
		       cross_reference.kind, cross_reference.verification_event_id::text,
		       cross_reference.metadata::text, cross_reference.created_at
		FROM relationship_cross_references AS cross_reference
		JOIN relationship_records AS source_relationship
		  ON source_relationship.team_id = cross_reference.team_id
		 AND source_relationship.relationship_id = cross_reference.source_relationship_id
		 AND source_relationship.space_id = cross_reference.space_id
		JOIN relationship_records AS target_relationship
		  ON target_relationship.team_id = cross_reference.team_id
		 AND target_relationship.relationship_id = cross_reference.target_relationship_id
		WHERE cross_reference.team_id = ?::uuid
		  AND `+activeSemanticSpaceGenerationSQL("cross_reference")+`
		  AND (
		    source_relationship.space_id = dense_mem_team_shared_space(cross_reference.team_id)
		    OR dense_mem_space_allowed(source_relationship.space_id)
		  )
		  AND `+activeSemanticSpaceGenerationSQL("source_relationship")+`
		  AND (
		    target_relationship.space_id = dense_mem_team_shared_space(cross_reference.team_id)
		    OR dense_mem_space_allowed(target_relationship.space_id)
		  )
		  AND `+activeSemanticSpaceGenerationSQL("target_relationship")+`
		  AND (
		    cross_reference.source_relationship_id = ?::uuid
		    OR cross_reference.target_relationship_id = ?::uuid
		  )
		ORDER BY cross_reference.created_at ASC, cross_reference.cross_reference_id ASC
		LIMIT ?
	`, input.TeamID, input.RelationshipID, input.RelationshipID, input.MaxEvents).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RelationshipCrossReferenceRecord
	for rows.Next() {
		var row RelationshipCrossReferenceRecord
		var metadataJSON string
		if err := rows.Scan(
			&row.CrossReferenceID, &row.AuthorProfileID,
			&row.SourceRelationshipID, &row.SourceRelationshipVersion,
			&row.TargetRelationshipID, &row.TargetRelationshipVersion,
			&row.Kind, &row.VerificationEventID, &metadataJSON, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.Metadata = jSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadTraceIdentityCorrections(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	spaceID string,
	observationIDs []string,
	limit int,
) ([]EntityCorrectionEventRecord, error) {
	if len(observationIDs) == 0 {
		return nil, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT correction_event_id::text, owner_profile_id::text, action,
		       COALESCE(survivor_entity_id::text, ''), COALESCE(new_entity_id::text, ''),
		       selected_observation_ids::text[], reason, metadata::text, created_at
		FROM entity_correction_events
		WHERE team_id = ?::uuid
		  AND space_id = ?::uuid
		  AND selected_observation_ids && ?::uuid[]
		  AND `+activeSemanticSpaceGenerationSQL("entity_correction_events")+`
		ORDER BY created_at ASC, correction_event_id ASC
		LIMIT ?
	`, teamID, spaceID, pq.Array(observationIDs), limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntityCorrectionEventRecord
	for rows.Next() {
		var row EntityCorrectionEventRecord
		var metadataJSON string
		if err := rows.Scan(
			&row.CorrectionEventID, &row.OwnerProfileID, &row.Action,
			&row.SurvivorEntityID, &row.NewEntityID,
			pq.Array(&row.SelectedObservationIDs), &row.Reason,
			&metadataJSON, &row.CreatedAt,
		); err != nil {
			return nil, err
		}
		row.Metadata = jSONMap(metadataJSON)
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadTraceSupersessionLineage(
	ctx context.Context,
	tx *gorm.DB,
	teamID string,
	relationship *RelationshipTraceRecord,
	limit int,
) ([]RelationshipTraceRecord, error) {
	if relationship == nil || relationship.SemanticGroupKey == "" {
		return nil, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT relationship_id::text
		FROM relationship_records
		WHERE team_id = ?::uuid
		  AND space_id = ?::uuid
		  AND semantic_group_key = ?
		  AND relationship_id <> ?::uuid
		  AND status IN ('superseded', 'retracted', 'disputed')
		  AND `+activeSemanticSpaceGenerationSQL("relationship_records")+`
		ORDER BY updated_at DESC, relationship_id ASC
		LIMIT ?
	`, teamID, relationship.SpaceID, relationship.SemanticGroupKey, relationship.RelationshipID, limit).Rows()
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
	out := make([]RelationshipTraceRecord, 0, len(ids))
	for _, id := range ids {
		record, err := loadTraceRelationship(ctx, tx, teamID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *record)
	}
	return out, nil
}

func traceObservationIDs(observations []RelationshipObservationRecord) []string {
	out := make([]string, 0, len(observations))
	for _, observation := range observations {
		if observation.ObservationID != "" {
			out = append(out, observation.ObservationID)
		}
	}
	return out
}

func traceSupportFragmentIDs(supports []RelationshipEvidenceSupportRecord) []string {
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

func traceVisitedEntityIDs(relationship *RelationshipTraceRecord, nodes []SemanticGraphNode) []string {
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

func jSONMap(raw string) map[string]any {
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

func jSON(raw string) any {
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

func boolDefault(value *bool, defaultValue bool) bool {
	if value == nil {
		return defaultValue
	}
	return *value
}

func timePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}

func clampInt(value, defaultValue, maxValue int) int {
	if value <= 0 {
		return defaultValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func defaultPositiveInt(value, defaultValue int) int {
	if value <= 0 {
		return defaultValue
	}
	return value
}

func normalizeOptionalRelevance(value *float64) *float64 {
	if value == nil {
		return nil
	}
	normalized := normalizeRelevance(*value)
	return &normalized
}

func normalizeRelevance(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
		return 0
	}
	if value > 1 {
		return 1
	}
	return value
}
