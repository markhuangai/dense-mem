package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

type semanticSearchDocumentInput struct {
	TeamID         string
	OwnerProfileID string
	SourceType     string
	SourceID       string
	DocumentText   string
	SourceVersion  int64
}

func upsertSemanticSearchDocument(ctx context.Context, tx *gorm.DB, input semanticSearchDocumentInput) error {
	input.DocumentText = strings.TrimSpace(input.DocumentText)
	if input.DocumentText == "" {
		return nil
	}
	if input.SourceVersion <= 0 {
		input.SourceVersion = 1
	}
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO semantic_search_documents (
		    team_id, owner_profile_id, source_type, source_id, document_text,
		    source_version, document_version, search_state, created_at, updated_at
		) VALUES (
		    ?, ?, ?, ?::uuid, ?, ?, 1, 'pending', now(), now()
		)
		ON CONFLICT (team_id, source_type, source_id, document_version)
		DO UPDATE SET
		    owner_profile_id = EXCLUDED.owner_profile_id,
		    document_text = EXCLUDED.document_text,
		    source_version = EXCLUDED.source_version,
		    embedding = CASE
		        WHEN semantic_search_documents.document_text IS DISTINCT FROM EXCLUDED.document_text
		          OR semantic_search_documents.source_version IS DISTINCT FROM EXCLUDED.source_version
		        THEN NULL
		        ELSE semantic_search_documents.embedding
		    END,
		    embedding_model = CASE
		        WHEN semantic_search_documents.document_text IS DISTINCT FROM EXCLUDED.document_text
		          OR semantic_search_documents.source_version IS DISTINCT FROM EXCLUDED.source_version
		        THEN ''
		        ELSE semantic_search_documents.embedding_model
		    END,
		    embedding_contract_id = CASE
		        WHEN semantic_search_documents.document_text IS DISTINCT FROM EXCLUDED.document_text
		          OR semantic_search_documents.source_version IS DISTINCT FROM EXCLUDED.source_version
		        THEN ''
		        ELSE semantic_search_documents.embedding_contract_id
		    END,
		    search_state = CASE
		        WHEN semantic_search_documents.document_text IS DISTINCT FROM EXCLUDED.document_text
		          OR semantic_search_documents.source_version IS DISTINCT FROM EXCLUDED.source_version
		        THEN 'pending'
		        ELSE semantic_search_documents.search_state
		    END,
		    last_error = CASE
		        WHEN semantic_search_documents.document_text IS DISTINCT FROM EXCLUDED.document_text
		          OR semantic_search_documents.source_version IS DISTINCT FROM EXCLUDED.source_version
		        THEN ''
		        ELSE semantic_search_documents.last_error
		    END,
		    updated_at = CASE
		        WHEN semantic_search_documents.document_text IS DISTINCT FROM EXCLUDED.document_text
		          OR semantic_search_documents.source_version IS DISTINCT FROM EXCLUDED.source_version
		          OR semantic_search_documents.owner_profile_id IS DISTINCT FROM EXCLUDED.owner_profile_id
		        THEN now()
		        ELSE semantic_search_documents.updated_at
		    END
		RETURNING search_document_id::text, document_version, search_state
	`, input.TeamID, input.OwnerProfileID, input.SourceType, input.SourceID, input.DocumentText, input.SourceVersion).Rows()
	if err != nil {
		return err
	}
	if !rows.Next() {
		_ = rows.Close()
		return sql.ErrNoRows
	}
	var searchDocumentID string
	var documentVersion int64
	var searchState string
	if err := rows.Scan(&searchDocumentID, &documentVersion, &searchState); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if searchState != "pending" {
		return nil
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO semantic_embedding_jobs (
		    team_id, search_document_id, source_type, source_id, document_version,
		    status, available_at, created_at, updated_at
		) VALUES (
		    ?, ?::uuid, ?, ?::uuid, ?, 'queued', now(), now(), now()
		)
		ON CONFLICT (team_id, search_document_id, document_version)
		WHERE status IN ('queued', 'processing')
		DO NOTHING
	`, input.TeamID, searchDocumentID, input.SourceType, input.SourceID, documentVersion).Error
}

func semanticEntitySearchText(entity domain.SemanticEntity) string {
	return strings.TrimSpace(entity.CanonicalName + " " + string(entity.Kind))
}

func semanticValueSearchText(value domain.SemanticValue) string {
	return strings.TrimSpace(value.DisplayValue + " " + string(value.ValueType))
}

func valueVersion(value domain.SemanticValue) int64 {
	if value.UpdatedAt.IsZero() {
		return 1
	}
	return value.UpdatedAt.UnixNano()
}

func semanticRelationshipSearchText(subject, predicate string, polarity domain.ClaimPolarity, object string) string {
	relation := strings.ReplaceAll(predicate, "_", " ")
	if polarity == domain.PolarityMinus {
		relation = "does not " + relation
	}
	return strings.TrimSpace(subject + " " + relation + " " + object)
}

func semanticRelationshipSearchEligible(relationship domain.SemanticRelationship) bool {
	return relationship.Status == domain.SemanticStatusActive &&
		(relationship.Tier == domain.SemanticTierValidatedClaim || relationship.Tier == domain.SemanticTierFact)
}

func refreshSemanticRelationshipSupportCounts(ctx context.Context, tx *gorm.DB, teamID, relationshipID string) error {
	return tx.WithContext(ctx).Exec(`
		UPDATE semantic_relationship_records r
		SET support_count = counts.support_count,
		    source_group_count = counts.source_group_count,
		    updated_at = now()
		FROM (
			SELECT team_id, relationship_id,
			       COUNT(*)::int AS support_count,
			       COUNT(DISTINCT NULLIF(source_group, ''))::int AS source_group_count
			FROM semantic_relationship_supports
			WHERE team_id = ? AND relationship_id = ?
			GROUP BY team_id, relationship_id
		) counts
		WHERE r.team_id = counts.team_id
		  AND r.relationship_id = counts.relationship_id
	`, teamID, relationshipID).Error
}

func listSemanticRelationshipSupports(ctx context.Context, tx *gorm.DB, teamID string, relationshipIDs []string) (map[string][]domain.SemanticRelationshipSupport, error) {
	supportsByRelationshipID := make(map[string][]domain.SemanticRelationshipSupport, len(relationshipIDs))
	if len(relationshipIDs) == 0 {
		return supportsByRelationshipID, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT team_id::text, relationship_id::text, fragment_id::text,
		       evidence_index, quote, created_at
		FROM semantic_relationship_supports
		WHERE team_id = ? AND relationship_id = ANY(?::uuid[])
		ORDER BY relationship_id ASC, evidence_index ASC, span_start ASC, span_end ASC, fragment_id ASC
	`, teamID, pq.Array(relationshipIDs)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var support domain.SemanticRelationshipSupport
		if err := rows.Scan(&support.TeamID, &support.RelationshipID, &support.FragmentID, &support.EvidenceIndex, &support.Quote, &support.CreatedAt); err != nil {
			return nil, err
		}
		supportsByRelationshipID[support.RelationshipID] = append(supportsByRelationshipID[support.RelationshipID], support)
	}
	return supportsByRelationshipID, rows.Err()
}

func semanticContentHash(content string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(content)))
	return hex.EncodeToString(sum[:])
}

func semanticEntityKey(name string, kind domain.SemanticEntityKind) string {
	return strings.ToLower(strings.TrimSpace(name)) + "\x00" + string(kind)
}

func semanticRelationshipGroupKey(subjectEntityID, predicate, objectEntityID, objectValue string, polarity domain.ClaimPolarity) string {
	canonical := strings.Join([]string{
		strings.TrimSpace(subjectEntityID),
		strings.ToLower(strings.TrimSpace(predicate)),
		string(polarity),
		strings.TrimSpace(objectEntityID),
		strings.ToLower(strings.TrimSpace(objectValue)),
	}, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])
}

func normalizeSemanticLimit(limit int) int {
	if limit <= 0 {
		return 10
	}
	if limit > 50 {
		return 50
	}
	return limit
}

func semanticOverfetch(limit int) int {
	limit = normalizeSemanticLimit(limit)
	overfetch := limit * 10
	if overfetch < 50 {
		overfetch = 50
	}
	if overfetch > 500 {
		overfetch = 500
	}
	return overfetch
}

func semanticVectorLiteral(vec []float32) string {
	if len(vec) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, value := range vec {
		if i > 0 {
			b.WriteByte(',')
		}
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			b.WriteString("0")
			continue
		}
		b.WriteString(strconv.FormatFloat(float64(value), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func normalizedSemanticSpan(start, end int) (int, int) {
	if start < 0 {
		start = 0
	}
	if end < start {
		end = start
	}
	return start, end
}

func semanticJSONPayload(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}", nil
	}
	if !json.Valid([]byte(raw)) {
		return "", errors.New("invalid json")
	}
	return raw, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func semanticOneOf(value string, allowed ...string) bool {
	for _, item := range allowed {
		if value == item {
			return true
		}
	}
	return false
}
