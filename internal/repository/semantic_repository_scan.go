package repository

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func semanticRelationshipColumns() string {
	return `team_id::text, relationship_id::text, owner_profile_id::text,
	        subject_entity_id::text, predicate, polarity, COALESCE(object_entity_id::text, ''),
	        object_value, object_kind, tier, status, confidence, support_count,
	        source_group_count, semantic_group_key, version, valid_from, valid_to,
	        recorded_at, recorded_to, created_at, updated_at`
}

func semanticEvidenceColumns(alias string) string {
	prefix := ""
	if alias = strings.TrimSpace(alias); alias != "" {
		prefix = alias + "."
	}
	return prefix + `team_id::text, ` + prefix + `fragment_id::text, ` + prefix + `owner_profile_id::text,
	        ` + prefix + `content, ` + prefix + `source, ` + prefix + `source_doc_id, ` + prefix + `source_group, ` + prefix + `source_type,
	        ` + prefix + `authority, ` + prefix + `labels, ` + prefix + `metadata::text, ` + prefix + `content_hash,
	        ` + prefix + `idempotency_key, '' AS embedding_model, ` + prefix + `embedding_contract_id,
	        ` + prefix + `created_at`
}

func scanSemanticRelationshipScore(rows *sql.Rows) (domain.SemanticRelationship, float64, error) {
	var rel domain.SemanticRelationship
	var tier, status string
	var validFrom, validTo, recordedTo sql.NullTime
	var score float64
	if err := rows.Scan(
		&rel.TeamID,
		&rel.RelationshipID,
		&rel.OwnerProfileID,
		&rel.SubjectEntityID,
		&rel.Predicate,
		&rel.Polarity,
		&rel.ObjectEntityID,
		&rel.ObjectValue,
		&rel.ObjectKind,
		&tier,
		&status,
		&rel.Confidence,
		&rel.SupportCount,
		&rel.SourceGroupCount,
		&rel.SemanticGroupKey,
		&rel.Version,
		&validFrom,
		&validTo,
		&rel.RecordedAt,
		&recordedTo,
		&rel.CreatedAt,
		&rel.UpdatedAt,
		&score,
	); err != nil {
		return domain.SemanticRelationship{}, 0, err
	}
	rel.Tier = domain.SemanticRelationshipTier(tier)
	rel.Status = domain.SemanticRelationshipStatus(status)
	rel.ValidFrom = sqlTimePtr(validFrom)
	rel.ValidTo = sqlTimePtr(validTo)
	rel.RecordedTo = sqlTimePtr(recordedTo)
	return rel, score, nil
}

func scanSemanticRelationship(rows *sql.Rows) (domain.SemanticRelationship, error) {
	var rel domain.SemanticRelationship
	var tier, status string
	var validFrom, validTo, recordedTo sql.NullTime
	if err := rows.Scan(
		&rel.TeamID,
		&rel.RelationshipID,
		&rel.OwnerProfileID,
		&rel.SubjectEntityID,
		&rel.Predicate,
		&rel.Polarity,
		&rel.ObjectEntityID,
		&rel.ObjectValue,
		&rel.ObjectKind,
		&tier,
		&status,
		&rel.Confidence,
		&rel.SupportCount,
		&rel.SourceGroupCount,
		&rel.SemanticGroupKey,
		&rel.Version,
		&validFrom,
		&validTo,
		&rel.RecordedAt,
		&recordedTo,
		&rel.CreatedAt,
		&rel.UpdatedAt,
	); err != nil {
		return domain.SemanticRelationship{}, err
	}
	rel.Tier = domain.SemanticRelationshipTier(tier)
	rel.Status = domain.SemanticRelationshipStatus(status)
	rel.ValidFrom = sqlTimePtr(validFrom)
	rel.ValidTo = sqlTimePtr(validTo)
	rel.RecordedTo = sqlTimePtr(recordedTo)
	return rel, nil
}

func scanHydratedSemanticRelationshipScore(rows *sql.Rows) (domain.SemanticRelationship, []string, float64, error) {
	var rel domain.SemanticRelationship
	var subjectKind, objectEntityKind, tier, status string
	var validFrom, validTo, recordedTo sql.NullTime
	var corroborating, conflicting, exactEntityIDs pq.StringArray
	var score float64
	if err := rows.Scan(
		&rel.TeamID,
		&rel.RelationshipID,
		&rel.OwnerProfileID,
		&rel.OwnerProfileName,
		&rel.SubjectEntityID,
		&rel.SubjectEntityName,
		&subjectKind,
		&rel.Predicate,
		&rel.Polarity,
		&rel.ObjectEntityID,
		&rel.ObjectEntityName,
		&objectEntityKind,
		&rel.ObjectValue,
		&rel.ObjectKind,
		&tier,
		&status,
		&rel.Confidence,
		&rel.SupportCount,
		&rel.SourceGroupCount,
		&rel.SemanticGroupKey,
		&rel.PrimarySourceGroup,
		&rel.Version,
		&validFrom,
		&validTo,
		&rel.RecordedAt,
		&recordedTo,
		&rel.CreatedAt,
		&rel.UpdatedAt,
		&corroborating,
		&conflicting,
		&exactEntityIDs,
		&score,
	); err != nil {
		return domain.SemanticRelationship{}, nil, 0, err
	}
	rel.SubjectEntityKind = domain.SemanticEntityKind(subjectKind)
	rel.ObjectEntityKind = domain.SemanticEntityKind(objectEntityKind)
	rel.Tier = domain.SemanticRelationshipTier(tier)
	rel.Status = domain.SemanticRelationshipStatus(status)
	rel.ValidFrom = sqlTimePtr(validFrom)
	rel.ValidTo = sqlTimePtr(validTo)
	rel.RecordedTo = sqlTimePtr(recordedTo)
	rel.CorroboratingRelationshipIDs = append([]string(nil), corroborating...)
	rel.ConflictingRelationshipIDs = append([]string(nil), conflicting...)
	return rel, append([]string(nil), exactEntityIDs...), score, nil
}

func scanSemanticEvidenceScore(rows *sql.Rows) (domain.SemanticEvidenceFragment, float64, error) {
	var evidence domain.SemanticEvidenceFragment
	var sourceType, authority string
	var labels pq.StringArray
	var metadata jsonMapScanner
	var score float64
	if err := rows.Scan(
		&evidence.TeamID,
		&evidence.FragmentID,
		&evidence.OwnerProfileID,
		&evidence.Content,
		&evidence.Source,
		&evidence.SourceDocID,
		&evidence.SourceGroup,
		&sourceType,
		&authority,
		&labels,
		&metadata,
		&evidence.ContentHash,
		&evidence.IdempotencyKey,
		&evidence.EmbeddingModel,
		&evidence.EmbeddingContract,
		&evidence.CreatedAt,
		&score,
	); err != nil {
		return domain.SemanticEvidenceFragment{}, 0, err
	}
	evidence.SourceType = domain.SourceType(sourceType)
	evidence.Authority = domain.Authority(authority)
	evidence.Labels = []string(labels)
	evidence.Metadata = map[string]any(metadata)
	return evidence, score, nil
}

func scanSemanticEvidence(rows *sql.Rows) (domain.SemanticEvidenceFragment, error) {
	var evidence domain.SemanticEvidenceFragment
	var sourceType, authority string
	var labels pq.StringArray
	var metadata jsonMapScanner
	if err := rows.Scan(
		&evidence.TeamID,
		&evidence.FragmentID,
		&evidence.OwnerProfileID,
		&evidence.Content,
		&evidence.Source,
		&evidence.SourceDocID,
		&evidence.SourceGroup,
		&sourceType,
		&authority,
		&labels,
		&metadata,
		&evidence.ContentHash,
		&evidence.IdempotencyKey,
		&evidence.EmbeddingModel,
		&evidence.EmbeddingContract,
		&evidence.CreatedAt,
	); err != nil {
		return domain.SemanticEvidenceFragment{}, err
	}
	evidence.SourceType = domain.SourceType(sourceType)
	evidence.Authority = domain.Authority(authority)
	evidence.Labels = []string(labels)
	evidence.Metadata = map[string]any(metadata)
	return evidence, nil
}

func scanOptionalSemanticEntity(rows *sql.Rows, err error) (*domain.SemanticEntity, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var entity domain.SemanticEntity
	if err := rows.Scan(
		&entity.TeamID,
		&entity.EntityID,
		&entity.OwnerProfileID,
		(*stringEntityKind)(&entity.Kind),
		&entity.CanonicalName,
		(*jsonMapScanner)(&entity.Metadata),
		&entity.CreatedAt,
		&entity.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &entity, rows.Err()
}

type stringEntityKind domain.SemanticEntityKind

func (k *stringEntityKind) Scan(value any) error {
	switch typed := value.(type) {
	case string:
		*k = stringEntityKind(typed)
	case []byte:
		*k = stringEntityKind(string(typed))
	default:
		return fmt.Errorf("unsupported entity kind scan type %T", value)
	}
	return nil
}

type jsonMapScanner map[string]any

func (m *jsonMapScanner) Scan(value any) error {
	if value == nil {
		*m = map[string]any{}
		return nil
	}
	var raw []byte
	switch typed := value.(type) {
	case string:
		raw = []byte(typed)
	case []byte:
		raw = typed
	default:
		return fmt.Errorf("unsupported json map scan type %T", value)
	}
	if len(raw) == 0 {
		*m = map[string]any{}
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return err
	}
	*m = out
	return nil
}

func marshalMap(value map[string]any) ([]byte, error) {
	if len(value) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(value)
}

func sqlTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time.UTC()
	return &t
}
