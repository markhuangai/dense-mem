package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// GetSearchConvergence compares active-contract search documents with the
// canonical semantic projections. A document is current only when its active
// contract vector is present and has the declared dimensions.
func (r *SearchRepositoryImpl) GetSearchConvergence(ctx context.Context, input SearchConvergenceInput) (*SearchConvergence, error) {
	input = normalizeSearchConvergenceInput(input)
	if err := validateSearchConvergenceInput(input); err != nil {
		return nil, err
	}
	contract, err := r.GetActiveSearchContract(ctx)
	if err != nil {
		return nil, err
	}
	if input.EmbeddingContractID != "" && input.EmbeddingContractID != contract.EmbeddingContractID {
		return nil, fmt.Errorf("%w: requested convergence contract is not active", ErrSearchContractMismatch)
	}
	if input.EmbeddingDimensions > 0 && input.EmbeddingDimensions != contract.EmbeddingDimensions {
		return nil, fmt.Errorf("%w: requested convergence dimensions are not active", ErrSearchContractMismatch)
	}

	convergence := &SearchConvergence{
		ObservedAt:   time.Now().UTC(),
		Status:       "converged",
		Contract:     contract,
		DriftClasses: []SearchDocumentDriftCount{},
	}
	err = r.withSystemTx(ctx, func(tx *gorm.DB) error {
		// not_required documents have no vector obligation. All other active
		// contract documents are expected to have a current vector.
		if err := tx.WithContext(ctx).Raw(`
			SELECT count(*)
			FROM search_documents AS document
			JOIN teams AS team
			  ON team.id = document.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE document.embedding_contract_id = ?::uuid
			  AND document.embedding_dimensions = ?
			  AND document.search_state <> 'not_required'
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&convergence.ExpectedDocuments).Error; err != nil {
			return err
		}
		if err := tx.WithContext(ctx).Raw(`
			SELECT count(*)
			FROM search_documents AS document
			JOIN teams AS team
			  ON team.id = document.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE document.embedding_contract_id = ?::uuid
			  AND document.embedding_dimensions = ?
			  AND document.search_state = 'current'
			  AND document.embedding IS NOT NULL
			  AND vector_dims(document.embedding) = document.embedding_dimensions
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&convergence.CurrentDocuments).Error; err != nil {
			return err
		}
		convergence.DriftedDocuments = convergence.ExpectedDocuments - convergence.CurrentDocuments
		if convergence.DriftedDocuments < 0 {
			convergence.DriftedDocuments = 0
		}
		if err := tx.WithContext(ctx).Raw(`
			SELECT count(DISTINCT document.team_id)
			FROM search_documents AS document
			JOIN teams AS team
			  ON team.id = document.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE document.embedding_contract_id = ?::uuid
			  AND document.embedding_dimensions = ?
			  AND (
			    document.search_state <> 'current'
			    OR document.embedding IS NULL
			    OR vector_dims(document.embedding) <> document.embedding_dimensions
			  )
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&convergence.AffectedTeamCount).Error; err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT CASE
			         WHEN document.embedding IS NULL THEN 'vector_missing'
			         WHEN document.search_state <> 'current' THEN 'state_not_current'
			         WHEN vector_dims(document.embedding) <> document.embedding_dimensions THEN 'vector_dimension_mismatch'
			         ELSE 'current'
			       END AS drift_class, count(*)
			FROM search_documents AS document
			JOIN teams AS team
			  ON team.id = document.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE document.embedding_contract_id = ?::uuid
			  AND document.embedding_dimensions = ?
			  AND (
			    document.search_state <> 'current'
			    OR document.embedding IS NULL
			    OR vector_dims(document.embedding) <> document.embedding_dimensions
			  )
			GROUP BY 1
			ORDER BY 1
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item SearchDocumentDriftCount
			if err := rows.Scan(&item.Class, &item.Count); err != nil {
				return err
			}
			convergence.DriftClasses = append(convergence.DriftClasses, item)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		var oldestSeconds float64
		if err := tx.WithContext(ctx).Raw(`
			SELECT COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - min(document.updated_at))), 0)
			FROM search_documents AS document
			JOIN teams AS team
			  ON team.id = document.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE document.embedding_contract_id = ?::uuid
			  AND document.embedding_dimensions = ?
			  AND document.search_state <> 'current'
		`, contract.EmbeddingContractID, contract.EmbeddingDimensions).Scan(&oldestSeconds).Error; err != nil {
			return err
		}
		convergence.OldestDriftAge = time.Duration(oldestSeconds * float64(time.Second))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("search: convergence projection: %w", err)
	}
	canonical, err := r.canonicalSearchConvergence(ctx, contract)
	if err != nil {
		return nil, err
	}
	convergence.ExpectedDocuments = canonical.ExpectedDocuments
	convergence.CurrentDocuments = canonical.CurrentDocuments
	convergence.DriftedDocuments = canonical.DriftedDocuments
	convergence.AffectedTeamCount = canonical.AffectedTeamCount
	convergence.OldestDriftAge = canonical.OldestDriftAge
	convergence.DriftClasses = canonical.DriftClasses
	if convergence.DriftedDocuments > 0 {
		convergence.Status = "attention_required"
	}
	convergence.LatestRun, err = r.latestSearchReconciliationRun(ctx)
	if err != nil {
		return nil, err
	}
	return convergence, nil
}

type canonicalSearchConvergence struct {
	ExpectedDocuments int64
	CurrentDocuments  int64
	DriftedDocuments  int64
	AffectedTeamCount int64
	OldestDriftAge    time.Duration
	DriftClasses      []SearchDocumentDriftCount
}

func (r *SearchRepositoryImpl) canonicalSearchConvergence(ctx context.Context, contract *ActiveSearchContract) (*canonicalSearchConvergence, error) {
	result := &canonicalSearchConvergence{}
	classes := map[string]int64{}
	teams := map[string]struct{}{}
	now := time.Now().UTC()
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		rows, err := selectSearchConvergenceDocuments(ctx, tx, contract)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			projection, err := scanSearchConvergenceProjection(rows)
			if err != nil {
				return err
			}
			item := projection.item
			vectorCurrent := projection.vectorCurrent
			updatedAt := projection.updatedAt
			expected, known := projection.canonical()
			if !known {
				expected = &item
			}
			if known && expected == nil {
				if item.SearchState == string(domain.SearchProjectionNotRequired) {
					continue
				}
				classes["canonical_source_missing"]++
				result.DriftedDocuments++
				teams[item.TeamID] = struct{}{}
				if age := now.Sub(updatedAt); age > result.OldestDriftAge {
					result.OldestDriftAge = age
				}
				continue
			}
			result.ExpectedDocuments++
			canonicalMatch := searchDocumentMatchesCanonical(item, *expected)
			if canonicalMatch && item.SearchState == string(domain.SearchProjectionCurrent) && vectorCurrent {
				result.CurrentDocuments++
				continue
			}
			class := "canonical_projection_mismatch"
			if canonicalMatch {
				switch {
				case item.SearchState != string(domain.SearchProjectionCurrent):
					class = "state_not_current"
				case !vectorCurrent:
					class = "vector_missing_or_dimension_mismatch"
				}
			}
			classes[class]++
			result.DriftedDocuments++
			teams[item.TeamID] = struct{}{}
			if age := now.Sub(updatedAt); age > result.OldestDriftAge {
				result.OldestDriftAge = age
			}
		}
		if err := addMissingCanonicalSearchStats(ctx, tx, contract, result, classes, teams, now); err != nil {
			return err
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("search: canonical convergence projection: %w", err)
	}
	result.AffectedTeamCount = int64(len(teams))
	result.DriftClasses = make([]SearchDocumentDriftCount, 0, len(classes))
	for class, count := range classes {
		result.DriftClasses = append(result.DriftClasses, SearchDocumentDriftCount{Class: class, Count: count})
	}
	sort.Slice(result.DriftClasses, func(i, j int) bool { return result.DriftClasses[i].Class < result.DriftClasses[j].Class })
	return result, nil
}

// searchConvergenceProjection carries a search document and the canonical
// source data needed to compare it. The source joins are performed in one
// PostgreSQL query so convergence does not issue one canonical lookup per
// document.
type searchConvergenceProjection struct {
	item           SearchDocumentForEmbedding
	vectorCurrent  bool
	updatedAt      time.Time
	canonicalKnown bool
	evidence       convergenceEvidenceProjection
	relationship   convergenceRelationshipProjection
}

type convergenceEvidenceProjection struct {
	eligible        bool
	content         string
	spaceID         string
	spaceGeneration int64
}

type convergenceRelationshipProjection struct {
	eligible             bool
	subjectEntityID      string
	predicateKey         string
	predicateVersion     int
	objectEntityID       string
	objectValueID        string
	relationshipKind     string
	currentCardinality   string
	status               string
	polarity             string
	scopeKey             string
	validFrom            *time.Time
	validTo              *time.Time
	identityAliasOfID    string
	supportCount         int
	sourceGroupCount     int
	version              int
	spaceID              string
	spaceGeneration      int64
	names                relationshipProjectionNames
	foregroundGeneration string
}

func selectSearchConvergenceDocuments(ctx context.Context, tx *gorm.DB, contract *ActiveSearchContract) (*sql.Rows, error) {
	return selectSearchConvergenceDocumentsPage(ctx, tx, contract, nil, 0)
}

// searchConvergenceCursor keeps reconciliation pages keyset-ordered without
// buffering the full search-document table in memory.
type searchConvergenceCursor struct {
	UpdatedAt  time.Time
	TeamID     string
	DocumentID string
}

// selectSearchConvergenceDocumentsPage joins canonical source projections for
// one bounded page, so reconciliation does not issue one lookup per document.
func selectSearchConvergenceDocumentsPage(
	ctx context.Context,
	tx *gorm.DB,
	contract *ActiveSearchContract,
	cursor *searchConvergenceCursor,
	limit int,
) (*sql.Rows, error) {
	query := `
		WITH activated_generations AS (
			SELECT DISTINCT ON (generation.team_id)
			       generation.team_id,
			       generation.projection_generation_id::text AS projection_generation_id
			FROM search_projection_generations AS generation
			WHERE generation.source_kind = 'relationship'
			  AND generation.projection_format_version = 2
			  AND generation.state = 'current'
			  AND generation.activated_at IS NOT NULL
			ORDER BY generation.team_id, generation.generation DESC, generation.created_at DESC
		), fallback_generations AS (
			SELECT DISTINCT ON (generation.team_id)
			       generation.team_id,
			       generation.projection_generation_id::text AS projection_generation_id
			FROM search_projection_generations AS generation
			WHERE generation.source_kind = 'relationship'
			  AND generation.projection_format_version = 2
			ORDER BY generation.team_id, generation.generation DESC, generation.created_at DESC
		), foreground_generations AS (
			SELECT fallback.team_id,
			       COALESCE(activated.projection_generation_id, fallback.projection_generation_id) AS projection_generation_id
			FROM fallback_generations AS fallback
			LEFT JOIN activated_generations AS activated ON activated.team_id = fallback.team_id
			UNION ALL
			SELECT activated.team_id, activated.projection_generation_id
			FROM activated_generations AS activated
			WHERE NOT EXISTS (
				SELECT 1 FROM fallback_generations AS fallback
				WHERE fallback.team_id = activated.team_id
			)
		)
		SELECT document.team_id::text, document.search_document_id::text,
		       document.owner_profile_id::text, document.source_kind,
		       document.source_id::text, document.source_version,
		       document.projection_format_version,
		       COALESCE(document.projection_generation_id::text, ''),
		       document.document_version, document.embedding_contract_id::text,
		       document.embedding_dimensions, document.search_state,
		       COALESCE(document.space_id::text, ''), COALESCE(document.space_generation, 0),
		       document.document_text, document.document_hash,
		       document.embedding IS NOT NULL
		         AND vector_dims(document.embedding) = document.embedding_dimensions,
		       document.updated_at,
		       document.source_kind IN ('evidence', 'relationship'),
		       document.source_kind = 'evidence'
		         AND fragment.fragment_id IS NOT NULL
		         AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
		         AND NOT EXISTS (
		             SELECT 1 FROM evidence_quarantines AS quarantine
		             WHERE quarantine.team_id = fragment.team_id
		               AND quarantine.fragment_id = fragment.fragment_id
		               AND quarantine.status = 'active'
		         )
		         AND NOT EXISTS (
		             SELECT 1 FROM evidence_lifecycle_events AS lifecycle
		             WHERE lifecycle.team_id = fragment.team_id
		               AND lifecycle.target_fragment_id = fragment.fragment_id
		         ),
		       COALESCE(fragment.content, ''),
		       COALESCE(fragment.space_id::text, ''), COALESCE(fragment.space_generation, 0),
		       document.source_kind = 'relationship'
		         AND relationship.relationship_id IS NOT NULL
		         AND relationship.status = 'active'
		         AND relationship.support_count > 0
		         AND relationship.identity_alias_of_relationship_id IS NULL,
		       COALESCE(relationship.subject_entity_id::text, ''),
		       COALESCE(relationship.predicate_key, ''), COALESCE(relationship.predicate_version, 0),
		       COALESCE(relationship.object_entity_id::text, ''),
		       COALESCE(relationship.object_value_id::text, ''),
		       COALESCE(relationship.relationship_kind, ''),
		       COALESCE(relationship.current_cardinality, ''), COALESCE(relationship.status, ''),
		       COALESCE(relationship.polarity, ''), COALESCE(relationship.scope_key, ''),
		       relationship.valid_from, relationship.valid_to,
		       COALESCE(relationship.identity_alias_of_relationship_id::text, ''),
		       COALESCE(relationship.support_count, 0), COALESCE(relationship.source_group_count, 0),
		       COALESCE(relationship.version, 0),
		       COALESCE(relationship.space_id::text, ''), COALESCE(relationship.space_generation, 0),
		       COALESCE(subject_name.display_name, ''),
		       COALESCE(object_name.display_name, NULLIF(value_record.display, ''),
		                NULLIF(value_record.canonical_value, ''),
		                relationship.object_entity_id::text, relationship.object_value_id::text, ''),
		       COALESCE(value_record.value_type, ''), COALESCE(value_record.canonical_value, ''),
		       COALESCE(value_record.unit, ''),
		       COALESCE(foreground.projection_generation_id, '')
		FROM search_documents AS document
		JOIN teams AS team
		  ON team.id = document.team_id
		 AND team.status = 'active'
		 AND team.deleted_at IS NULL
		JOIN embedding_contracts AS active_contract
		  ON active_contract.embedding_contract_id = document.embedding_contract_id
		 AND active_contract.dimensions = document.embedding_dimensions
		 AND active_contract.lifecycle_state = 'active'
		LEFT JOIN evidence_fragments AS fragment
		  ON document.source_kind = 'evidence'
		 AND fragment.team_id = document.team_id
		 AND fragment.fragment_id = document.source_id
		 AND fragment.owner_profile_id = document.owner_profile_id
		LEFT JOIN relationship_records AS relationship
		  ON document.source_kind = 'relationship'
		 AND relationship.team_id = document.team_id
		 AND relationship.relationship_id = document.source_id
		LEFT JOIN entity_names AS subject_name
		  ON document.source_kind = 'relationship'
		 AND subject_name.team_id = relationship.team_id
		 AND subject_name.entity_id = relationship.subject_entity_id
		 AND subject_name.name_kind = 'canonical'
		 AND subject_name.valid_to IS NULL
		LEFT JOIN entity_names AS object_name
		  ON document.source_kind = 'relationship'
		 AND object_name.team_id = relationship.team_id
		 AND object_name.entity_id = relationship.object_entity_id
		 AND object_name.name_kind = 'canonical'
		 AND object_name.valid_to IS NULL
		LEFT JOIN value_records AS value_record
		  ON document.source_kind = 'relationship'
		 AND value_record.team_id = relationship.team_id
		 AND value_record.value_id = relationship.object_value_id
		LEFT JOIN foreground_generations AS foreground ON foreground.team_id = document.team_id
		WHERE document.embedding_contract_id = ?::uuid
		  AND document.embedding_dimensions = ?
		  AND EXISTS (
		      SELECT 1
		      FROM search_index_generations AS generation
		      WHERE generation.embedding_contract_id = document.embedding_contract_id
		        AND generation.embedding_dimensions = document.embedding_dimensions
		        AND generation.activation_state = 'active'
		  )
	`
	args := []any{contract.EmbeddingContractID, contract.EmbeddingDimensions}
	if cursor != nil && !cursor.UpdatedAt.IsZero() {
		query += `
		  AND (document.updated_at, document.team_id, document.search_document_id) > (?, ?::uuid, ?::uuid)
		`
		args = append(args, cursor.UpdatedAt, cursor.TeamID, cursor.DocumentID)
	}
	query += `
		ORDER BY document.updated_at ASC, document.team_id, document.search_document_id
	`
	if limit > 0 {
		query += "\n\t\tLIMIT ?\n"
		args = append(args, limit)
	}
	return tx.WithContext(ctx).Raw(query, args...).Rows()
}

func scanSearchConvergenceProjection(rows *sql.Rows) (*searchConvergenceProjection, error) {
	projection := &searchConvergenceProjection{}
	item := &projection.item
	relationship := &projection.relationship
	return projection, rows.Scan(
		&item.TeamID, &item.SearchDocumentID, &item.OwnerProfileID,
		&item.SourceKind, &item.SourceID, &item.SourceVersion,
		&item.ProjectionFormat, &item.ProjectionGenerationID,
		&item.DocumentVersion, &item.EmbeddingContractID,
		&item.EmbeddingDimensions, &item.SearchState, &item.SpaceID,
		&item.SpaceGeneration, &item.DocumentText, &item.DocumentHash,
		&projection.vectorCurrent, &projection.updatedAt,
		&projection.canonicalKnown, &projection.evidence.eligible,
		&projection.evidence.content, &projection.evidence.spaceID,
		&projection.evidence.spaceGeneration,
		&relationship.eligible, &relationship.subjectEntityID,
		&relationship.predicateKey, &relationship.predicateVersion,
		&relationship.objectEntityID, &relationship.objectValueID,
		&relationship.relationshipKind, &relationship.currentCardinality,
		&relationship.status, &relationship.polarity, &relationship.scopeKey,
		&relationship.validFrom, &relationship.validTo,
		&relationship.identityAliasOfID, &relationship.supportCount,
		&relationship.sourceGroupCount, &relationship.version,
		&relationship.spaceID, &relationship.spaceGeneration,
		&relationship.names.SubjectName, &relationship.names.ObjectName,
		&relationship.names.ObjectValueType, &relationship.names.ObjectValue,
		&relationship.names.ObjectUnit,
		&relationship.foregroundGeneration,
	)
}

func (p *searchConvergenceProjection) canonical() (*SearchDocumentForEmbedding, bool) {
	if !p.canonicalKnown {
		return nil, false
	}
	switch p.item.SourceKind {
	case "evidence":
		if !p.evidence.eligible {
			return nil, true
		}
		expected := p.item
		expected.SourceVersion = 1
		expected.ProjectionFormat = defaultProjectionFormat("evidence")
		expected.ProjectionGenerationID = ""
		expected.DocumentText = strings.TrimSpace(p.evidence.content)
		expected.DocumentHash = searchDocumentHash(expected.DocumentText)
		expected.SpaceID = strings.TrimSpace(p.evidence.spaceID)
		expected.SpaceGeneration = p.evidence.spaceGeneration
		return &expected, true
	case "relationship":
		if !p.relationship.eligible {
			return nil, true
		}
		relationship := &RelationshipRecord{
			TeamID: p.item.TeamID, RelationshipID: p.item.SourceID,
			SpaceID: p.relationship.spaceID, SpaceGeneration: p.relationship.spaceGeneration,
			SubjectEntityID: p.relationship.subjectEntityID,
			PredicateKey:    p.relationship.predicateKey, PredicateVersion: p.relationship.predicateVersion,
			ObjectEntityID: p.relationship.objectEntityID, ObjectValueID: p.relationship.objectValueID,
			RelationshipKind: p.relationship.relationshipKind, CurrentCardinality: p.relationship.currentCardinality,
			Status: p.relationship.status, Polarity: p.relationship.polarity,
			ScopeKey: p.relationship.scopeKey, ValidFrom: p.relationship.validFrom, ValidTo: p.relationship.validTo,
			IdentityAliasOfID: p.relationship.identityAliasOfID,
			SupportCount:      p.relationship.supportCount, SourceGroupCount: p.relationship.sourceGroupCount,
			Version: p.relationship.version,
		}
		text := relationshipProjectionText(relationship, p.relationship.names)
		expected := p.item
		expected.SourceVersion = int64(relationship.Version)
		expected.ProjectionFormat = 2
		expected.ProjectionGenerationID = p.relationship.foregroundGeneration
		expected.DocumentText = strings.TrimSpace(text)
		expected.DocumentHash = searchDocumentHash(expected.DocumentText)
		expected.SpaceID = relationship.SpaceID
		expected.SpaceGeneration = relationship.SpaceGeneration
		return &expected, true
	default:
		return nil, false
	}
}

func addMissingCanonicalSearchStats(
	ctx context.Context,
	tx *gorm.DB,
	contract *ActiveSearchContract,
	result *canonicalSearchConvergence,
	classes map[string]int64,
	teams map[string]struct{},
	now time.Time,
) error {
	rows, err := tx.WithContext(ctx).Raw(`
		WITH canonical_sources AS (
			SELECT fragment.team_id, fragment.fragment_id AS source_id,
			       'evidence'::text AS source_kind, fragment.created_at
			FROM evidence_fragments AS fragment
			JOIN teams AS team
			  ON team.id = fragment.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE NOT EXISTS (
			          SELECT 1
			          FROM evidence_quarantines AS quarantine
			          WHERE quarantine.team_id = fragment.team_id
			            AND quarantine.fragment_id = fragment.fragment_id
			            AND quarantine.status = 'active'
			      )
			  AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'
			  AND NOT EXISTS (
			          SELECT 1
			          FROM evidence_lifecycle_events AS lifecycle
			          WHERE lifecycle.team_id = fragment.team_id
			            AND lifecycle.target_fragment_id = fragment.fragment_id
			      )
			UNION ALL
			SELECT relationship.team_id, relationship.relationship_id AS source_id,
			       'relationship'::text AS source_kind, relationship.created_at
			FROM relationship_records AS relationship
			JOIN teams AS team
			  ON team.id = relationship.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			WHERE relationship.status = 'active'
			  AND relationship.support_count > 0
			  AND relationship.identity_alias_of_relationship_id IS NULL
		)
		SELECT source.team_id::text, count(*),
		       COALESCE(EXTRACT(EPOCH FROM (clock_timestamp() - min(source.created_at))), 0)
		FROM canonical_sources AS source
		WHERE NOT EXISTS (
		          SELECT 1
		          FROM search_documents AS document
		          WHERE document.team_id = source.team_id
		            AND document.source_kind = source.source_kind
		            AND document.source_id = source.source_id
		            AND document.embedding_contract_id = ?::uuid
		      )
		GROUP BY source.team_id
	`, contract.EmbeddingContractID).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var teamID string
		var count int64
		var oldestSeconds float64
		if err := rows.Scan(&teamID, &count, &oldestSeconds); err != nil {
			return err
		}
		result.ExpectedDocuments += count
		result.DriftedDocuments += count
		classes["canonical_document_missing"] += count
		teams[teamID] = struct{}{}
		age := time.Duration(oldestSeconds * float64(time.Second))
		if age > result.OldestDriftAge {
			result.OldestDriftAge = age
		}
	}
	return rows.Err()
}

func (r *SearchRepositoryImpl) latestSearchReconciliationRun(ctx context.Context) (*SearchReconciliationRun, error) {
	var run SearchReconciliationRun
	err := r.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.WithContext(ctx).Raw(`
			SELECT reconciliation_run_id::text, local_run_date, status,
			       selected_count, embedded_count, updated_count, drifted_count, last_error,
			       started_at, completed_at, updated_at
			FROM search_reconciliation_runs
			ORDER BY updated_at DESC, reconciliation_run_id DESC
			LIMIT 1
		`).Row().Scan(
			&run.RunID, &run.LocalRunDate, &run.Status,
			&run.SelectedCount, &run.EmbeddedCount, &run.UpdatedCount, &run.DriftedCount, &run.LastError,
			&run.StartedAt, &run.CompletedAt, &run.UpdatedAt,
		)
	})
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("search: latest reconciliation run: %w", err)
	}
	return &run, nil
}

func searchTimePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}

func normalizeSearchConvergenceInput(input SearchConvergenceInput) SearchConvergenceInput {
	input.EmbeddingContractID = strings.TrimSpace(input.EmbeddingContractID)
	return input
}

func validateSearchConvergenceInput(input SearchConvergenceInput) error {
	if input.EmbeddingContractID != "" {
		if _, err := uuid.Parse(input.EmbeddingContractID); err != nil {
			return fmt.Errorf("embedding_contract_id is invalid: %w", err)
		}
	}
	if input.EmbeddingDimensions < 0 {
		return errors.New("embedding_dimensions cannot be negative")
	}
	return nil
}

func refreshRelationshipProjectionGeneration(ctx context.Context, tx *gorm.DB, teamID string, projectionGenerationID string) error {
	return tx.WithContext(ctx).Exec("UPDATE search_projection_generations SET drifted_count = 0 WHERE team_id = ?::uuid AND projection_generation_id = ?::uuid", teamID, projectionGenerationID).Error
}
