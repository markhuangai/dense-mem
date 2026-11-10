package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/lib/pq"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"gorm.io/gorm"
)

type traceExecutionInput struct {
	tracecontract.Input
	spaceID string
}

func (r *Store) TraceEvidence(ctx context.Context, input TraceEvidenceInput) (*EvidenceTraceResult, error) {
	input = tracecontract.NormalizeInput(input)
	if id, err := uuid.Parse(input.TeamID); err != nil || id == uuid.Nil {
		return nil, tracecontract.ErrEvidenceIDInvalid
	}
	if id, err := uuid.Parse(input.EvidenceID); err != nil || id == uuid.Nil {
		return nil, tracecontract.ErrEvidenceIDInvalid
	}
	result := &EvidenceTraceResult{Occurrences: []TraceEvidenceFragment{}, SourceRevisions: []tracecontract.TraceSourceRevision{}, Decisions: []tracecontract.TraceEvidenceDecision{}, Lifecycle: []TraceEvidenceLifecycleEvent{}, SearchDocuments: []TraceSearchDocument{}, Omissions: []string{}}
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		resolved, err := storage.ResolveMemoryHandleTx(ctx, tx, input.TeamID, "evidence_fragments", input.EvidenceID, "")
		if err != nil {
			return err
		}
		input.EvidenceID = resolved
		root, err := loadTraceEvidenceRoot(ctx, tx, input)
		if errors.Is(err, sql.ErrNoRows) {
			return tracecontract.ErrEvidenceNotFound
		}
		if err != nil {
			return err
		}
		result.Evidence = root
		input.EvidenceID = root.FragmentID
		execution := traceExecutionInput{Input: input, spaceID: root.SpaceID}
		remaining := input.MaxEvents
		occurrenceIDs, err := traceEvidenceOccurrenceIDs(ctx, tx, execution)
		if err != nil {
			return err
		}
		occurrences, err := loadTraceEvidenceOccurrences(ctx, tx, execution, occurrenceIDs)
		if err != nil {
			return err
		}
		result.Occurrences = tracecontract.BoundEvents(occurrences, &remaining, result, "occurrences")
		sources := []string{}
		if root.SourceID != "" {
			sources = append(sources, root.SourceID)
		}
		for _, occurrence := range result.Occurrences {
			if occurrence.SourceID != "" {
				sources = append(sources, occurrence.SourceID)
			}
		}
		revisions, err := loadTraceSourceRevisions(ctx, tx, execution, sources, remaining+1)
		if err != nil {
			return err
		}
		result.SourceRevisions = tracecontract.BoundEvents(revisions, &remaining, result, "source_revisions")
		decisions, err := loadTraceEvidenceDecisions(ctx, tx, execution, remaining+1)
		if err != nil {
			return err
		}
		result.Decisions = tracecontract.BoundEvents(decisions, &remaining, result, "decisions")
		if boolDefault(input.IncludeTransitions, true) {
			lifecycleIDs := []string{root.FragmentID}
			for _, occurrence := range occurrences {
				lifecycleIDs = append(lifecycleIDs, occurrence.OccurrenceID)
			}
			lifecycle, err := loadTraceEvidenceLifecycleEvents(ctx, tx, input.TeamID, root.SpaceID, lifecycleIDs, remaining+1)
			if err != nil {
				return err
			}
			result.Lifecycle = tracecontract.BoundEvents(lifecycle, &remaining, result, "lifecycle")
		} else {
			result.Omissions = append(result.Omissions, "lifecycle_excluded_by_request")
		}
		documents, err := loadTraceEvidenceSearch(ctx, tx, execution, remaining+1)
		if err != nil {
			return err
		}
		result.SearchDocuments = tracecontract.BoundEvents(documents, &remaining, result, "search_documents")
		if !boolDefault(input.IncludeEvidenceContent, true) {
			result.Omissions = append(result.Omissions, "evidence_content_excluded_by_request")
		}
		truncated := root.ContentTruncated
		for _, occurrence := range result.Occurrences {
			truncated = truncated || occurrence.ContentTruncated
		}
		if truncated {
			result.Truncated = true
			result.Omissions = append(result.Omissions, "evidence_content_max_runes")
		}
		result.Selection = map[string]any{"evidence_id": root.FragmentID, "space_id": root.SpaceID, "generation": root.SpaceGeneration, "occurrences": len(result.Occurrences), "source_revisions": len(result.SourceRevisions), "decisions": len(result.Decisions), "lifecycle": len(result.Lifecycle), "events_remaining": remaining}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func loadTraceEvidenceRoot(ctx context.Context, tx *gorm.DB, input TraceEvidenceInput) (*TraceEvidenceFragment, error) {
	include := boolDefault(input.IncludeEvidenceContent, true)
	var row TraceEvidenceFragment
	var metadata, sessionJSON string
	err := tx.WithContext(ctx).Raw(`SELECT fragment.team_id::text,fragment.fragment_id::text,fragment.ingest_id::text,
        fragment.owner_profile_id::text,fragment.space_id::text,fragment.space_generation,
        COALESCE(fragment.source_id::text,''),COALESCE(fragment.source_revision_id::text,''),COALESCE(source.source_key,''),
        COALESCE(source.source_kind,''),COALESCE(revision.revision_token,''),COALESCE(source.current_revision_id::text,''),fragment.evidence_index,
        CASE WHEN ? THEN left(fragment.content,?) ELSE '' END,fragment.content_hash,
        CASE WHEN ? THEN char_length(fragment.content)>? ELSE false END,
        fragment.source_type,fragment.authority,fragment.source_ref,fragment.labels,fragment.metadata::text,fragment.created_at,
        CASE WHEN session.submission_id IS NOT NULL THEN COALESCE(fragment.metadata->'session','null'::jsonb)::text ELSE 'null' END
        FROM evidence_fragments AS selected
        LEFT JOIN evidence_exact_aliases AS alias ON alias.team_id=selected.team_id AND alias.alias_fragment_id=selected.fragment_id
        JOIN evidence_fragments AS fragment ON fragment.team_id=selected.team_id AND fragment.fragment_id=COALESCE(alias.canonical_fragment_id,selected.fragment_id)
          AND fragment.space_id=selected.space_id
        JOIN memory_spaces AS space ON space.team_id=fragment.team_id AND space.id=fragment.space_id
        LEFT JOIN evidence_sources AS source ON source.team_id=fragment.team_id AND source.source_id=fragment.source_id
          AND source.owner_profile_id=fragment.owner_profile_id AND source.space_id=fragment.space_id
        LEFT JOIN evidence_source_revisions AS revision ON revision.team_id=fragment.team_id AND revision.source_revision_id=fragment.source_revision_id
          AND revision.owner_profile_id=fragment.owner_profile_id AND revision.space_id=fragment.space_id
        LEFT JOIN session_submissions AS session ON session.team_id=fragment.team_id AND session.submission_id=fragment.ingest_id
          AND session.owner_profile_id=fragment.owner_profile_id AND session.space_id=fragment.space_id AND session.space_generation=fragment.space_generation
        WHERE selected.team_id=?::uuid AND selected.fragment_id=?::uuid
          AND (space.kind='team_shared' OR dense_mem_space_allowed(space.id))
          AND `+activeSemanticSpaceGenerationSQL("selected")+` AND `+activeSemanticSpaceGenerationSQL("fragment")+`
          AND (source.source_id IS NULL OR `+activeSemanticSpaceGenerationSQL("source")+`)
          AND (revision.source_revision_id IS NULL OR `+activeSemanticSpaceGenerationSQL("revision")+`)
        LIMIT 1`, include, input.MaxFragmentContentRunes, include, input.MaxFragmentContentRunes, input.TeamID, input.EvidenceID).Row().Scan(
		&row.TeamID, &row.FragmentID, &row.IngestID, &row.OwnerProfileID, &row.SpaceID, &row.SpaceGeneration,
		&row.SourceID, &row.SourceRevisionID, &row.SourceKey, &row.SourceKind, &row.RevisionToken, &row.CurrentRevisionID, &row.EvidenceIndex,
		&row.Content, &row.ContentHash, &row.ContentTruncated, &row.SourceType, &row.Authority, &row.SourceRef, pq.Array(&row.Labels), &metadata, &row.CreatedAt, &sessionJSON)
	if err != nil {
		return nil, err
	}
	row.ContentOmitted = !include
	if err := json.Unmarshal([]byte(metadata), &row.Metadata); err != nil {
		return nil, err
	}
	if sessionJSON != "null" {
		row.Session = &tracecontract.SessionProvenance{}
		if err := json.Unmarshal([]byte(sessionJSON), row.Session); err != nil {
			return nil, err
		}
	}
	return &row, nil
}

func traceEvidenceOccurrenceIDs(ctx context.Context, tx *gorm.DB, input traceExecutionInput) ([]string, error) {
	var ids []string
	err := tx.WithContext(ctx).Raw(`SELECT occurrence.occurrence_id::text FROM evidence_occurrences AS occurrence
        LEFT JOIN evidence_exact_aliases AS alias ON alias.team_id=occurrence.team_id AND alias.alias_fragment_id=occurrence.occurrence_id
        WHERE occurrence.team_id=?::uuid AND occurrence.space_id=?::uuid
          AND COALESCE(alias.canonical_fragment_id,occurrence.canonical_fragment_id)=?::uuid
          AND `+activeSemanticSpaceGenerationSQL("occurrence")+`
        ORDER BY occurrence.created_at,occurrence.occurrence_id LIMIT ?`, input.TeamID, input.spaceID, input.EvidenceID, input.MaxEvents+1).Scan(&ids).Error
	return ids, err
}

func loadTraceSourceRevisions(ctx context.Context, tx *gorm.DB, input traceExecutionInput, ids []string, limit int) ([]tracecontract.TraceSourceRevision, error) {
	rows, err := tx.WithContext(ctx).Raw(`SELECT source.source_id::text,revision.source_revision_id::text,revision.owner_profile_id::text,
        source.source_key,revision.revision_token,COALESCE(revision.supersedes_revision_id::text,''),COALESCE(source.current_revision_id::text,''),
        revision.content_hash,revision.envelope::text,revision.created_at
        FROM evidence_source_revisions AS revision JOIN evidence_sources AS source ON source.team_id=revision.team_id
          AND source.source_id=revision.source_id AND source.owner_profile_id=revision.owner_profile_id AND source.space_id=revision.space_id
        WHERE revision.team_id=?::uuid AND revision.space_id=?::uuid AND revision.source_id=ANY(?::uuid[])
          AND `+activeSemanticSpaceGenerationSQL("revision")+` AND `+activeSemanticSpaceGenerationSQL("source")+`
        ORDER BY revision.created_at,revision.source_revision_id LIMIT ?`, input.TeamID, input.spaceID, pq.Array(ids), limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []tracecontract.TraceSourceRevision{}
	for rows.Next() {
		var row tracecontract.TraceSourceRevision
		var envelope string
		if err := rows.Scan(&row.SourceID, &row.SourceRevisionID, &row.OwnerProfileID, &row.SourceKey, &row.RevisionToken, &row.SupersedesRevisionID, &row.CurrentRevisionID, &row.ContentHash, &envelope, &row.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(envelope), &row.Envelope); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadTraceEvidenceDecisions(ctx context.Context, tx *gorm.DB, input traceExecutionInput, limit int) ([]tracecontract.TraceEvidenceDecision, error) {
	rows, err := tx.WithContext(ctx).Raw(`WITH selected_occurrences AS (
        SELECT occurrence.* FROM evidence_occurrences AS occurrence
        LEFT JOIN evidence_exact_aliases AS alias ON alias.team_id=occurrence.team_id AND alias.alias_fragment_id=occurrence.occurrence_id
        WHERE occurrence.team_id=?::uuid AND occurrence.space_id=?::uuid
          AND COALESCE(alias.canonical_fragment_id,occurrence.canonical_fragment_id)=?::uuid
          AND `+activeSemanticSpaceGenerationSQL("occurrence")+`
    ), decisions AS (
        SELECT event.security_event_id::text AS decision_id,event.ingest_id::text,COALESCE(event.occurrence_id::text,'') AS occurrence_id,
          event.event_kind AS kind,event.decision,left(event.reason,512) AS reason,event.created_at
        FROM evidence_security_events AS event
        WHERE event.team_id=?::uuid AND event.space_id=?::uuid AND `+activeSemanticSpaceGenerationSQL("event")+`
          AND (event.fragment_id=?::uuid OR event.occurrence_id IN (SELECT occurrence_id FROM selected_occurrences))
        UNION ALL
        SELECT assessment.semantic_assessment_id::text || ':' || policy.kind || ':' || occurrence.evidence_index::text,
          occurrence.ingest_id::text,occurrence.occurrence_id::text,policy.kind,
          COALESCE(item->>'decision',item->>'action'),'assessor_accepted',assessment.validated_at
        FROM selected_occurrences AS occurrence
        JOIN semantic_assessments AS assessment ON assessment.team_id=occurrence.team_id AND assessment.attempt_id=occurrence.ingest_id
          AND assessment.owner_profile_id=occurrence.owner_profile_id AND assessment.space_id=occurrence.space_id
          AND assessment.space_generation=occurrence.space_generation
        CROSS JOIN LATERAL jsonb_array_elements(assessment.response_history) WITH ORDINALITY AS history(body,ordinal)
        CROSS JOIN LATERAL (VALUES ('security',history.body->'normalized_response'->'evidence_security_results'),
          ('equivalence',history.body->'normalized_response'->'evidence_equivalence_results')) AS policy(kind,results)
        CROSS JOIN LATERAL jsonb_array_elements(COALESCE(policy.results,'[]'::jsonb)) AS item
        WHERE history.ordinal=assessment.accepted_revision AND item->>'evidence_id'='evidence:' || occurrence.evidence_index::text
    ) SELECT decision_id,ingest_id,occurrence_id,kind,decision,reason,created_at FROM decisions
      ORDER BY created_at,decision_id LIMIT ?`, input.TeamID, input.spaceID, input.EvidenceID, input.TeamID, input.spaceID, input.EvidenceID, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []tracecontract.TraceEvidenceDecision{}
	for rows.Next() {
		var row tracecontract.TraceEvidenceDecision
		if err := rows.Scan(&row.DecisionID, &row.IngestID, &row.OccurrenceID, &row.Kind, &row.Decision, &row.Reason, &row.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func loadTraceEvidenceSearch(ctx context.Context, tx *gorm.DB, input traceExecutionInput, limit int) ([]TraceSearchDocument, error) {
	rows, err := tx.WithContext(ctx).Raw(`SELECT search_document_id::text,owner_profile_id::text,source_kind,source_id::text,source_version,
        document_version,embedding_contract_id::text,embedding_dimensions,search_state,document_hash,created_at,updated_at
        FROM search_documents AS document WHERE team_id=?::uuid AND space_id=?::uuid AND source_kind='evidence' AND source_id=?::uuid
          AND `+activeSemanticSpaceGenerationSQL("document")+` ORDER BY created_at,search_document_id LIMIT ?`, input.TeamID, input.spaceID, input.EvidenceID, limit).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TraceSearchDocument{}
	for rows.Next() {
		var row TraceSearchDocument
		if err := rows.Scan(&row.SearchDocumentID, &row.OwnerProfileID, &row.SourceKind, &row.SourceID, &row.SourceVersion, &row.DocumentVersion, &row.EmbeddingContractID, &row.EmbeddingDimensions, &row.SearchState, &row.DocumentHash, &row.CreatedAt, &row.UpdatedAt); err != nil {
			return nil, fmt.Errorf("trace: search projection: %w", err)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
