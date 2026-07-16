package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type SemanticRepository interface {
	StoreRemember(ctx context.Context, input SemanticRememberInput) (*SemanticRememberResult, error)
	LoadSemanticVerifierContext(ctx context.Context, teamID string, relationships []SemanticRelationshipInput) (SemanticVerifierContext, error)
	SearchRecallLexicalCandidates(ctx context.Context, scope domain.SemanticRecallSearchScope) (domain.SemanticRecallCandidateBatch, error)
	SearchRecallVectorCandidates(ctx context.Context, scope domain.SemanticRecallSearchScope) (domain.SemanticRecallCandidateBatch, error)
	SearchRecallAdjacencyCandidates(ctx context.Context, scope domain.SemanticRecallSearchScope, seeds []domain.SemanticRecallEntitySeed) ([]domain.SemanticRecallCandidate, error)
	HydrateRecallEvidence(ctx context.Context, scope domain.SemanticRecallSearchScope, evidenceIDs, preferredRelationshipIDs []string) ([]domain.SemanticRecallResult, error)
	TraceRelationship(ctx context.Context, teamID string, relationshipID string) (*domain.SemanticTraceResult, error)
}

type SemanticRememberInput struct {
	TeamID           string
	TeamName         string
	OwnerProfileID   string
	OwnerProfileName string
	Evidence         []SemanticEvidenceInput
	Relationships    []SemanticRelationshipInput
}

type SemanticEvidenceInput struct {
	Content        string
	Source         string
	SourceDocID    string
	SourceGroup    string
	SourceType     domain.SourceType
	Authority      domain.Authority
	Labels         []string
	Metadata       map[string]any
	IdempotencyKey string
}

type SemanticRelationshipInput struct {
	ObservationOnly          bool
	SubjectName              string
	SubjectKind              domain.SemanticEntityKind
	Predicate                string
	Polarity                 domain.ClaimPolarity
	ObjectName               string
	ObjectKind               domain.SemanticEntityKind
	ObjectValue              string
	Tier                     domain.SemanticRelationshipTier
	Status                   domain.SemanticRelationshipStatus
	Confidence               float64
	EvidenceIndex            int
	SpanStart                int
	SpanEnd                  int
	SourceGroup              string
	Quote                    string
	ExtractionModel          string
	ExtractionRawJSON        string
	VerifierModel            string
	EvidenceVerdict          string
	KnowledgeAlignment       string
	VerificationRationale    string
	VerificationRawJSON      string
	PlacementOutcomeCategory string
	PlacementReviewMessage   string
}

type SemanticRememberResult struct {
	Evidence                 []domain.SemanticEvidenceFragment
	Entities                 []domain.SemanticEntity
	Relationships            []domain.SemanticRelationship
	RelationshipInputIndexes []int
	Supports                 []domain.SemanticRelationshipSupport
}

type SemanticVerifierContext struct {
	EntityCandidates       map[string][]SemanticEntityCandidate
	RelationshipCandidates map[int][]SemanticRelationshipCandidate
}

type SemanticEntityCandidate struct {
	EntityID      string
	CanonicalName string
	Kind          domain.SemanticEntityKind
}

type SemanticRelationshipCandidate struct {
	RelationshipID string
	SubjectName    string
	Predicate      string
	ObjectName     string
	ObjectValue    string
	Tier           domain.SemanticRelationshipTier
	Status         domain.SemanticRelationshipStatus
}

type SemanticRepositoryImpl struct {
	db  *gorm.DB
	rls postgres.RLSHelper
}

var _ SemanticRepository = (*SemanticRepositoryImpl)(nil)

func NewSemanticRepository(db *gorm.DB, rls postgres.RLSHelper) *SemanticRepositoryImpl {
	return &SemanticRepositoryImpl{db: db, rls: rls}
}

func (r *SemanticRepositoryImpl) StoreRemember(ctx context.Context, input SemanticRememberInput) (*SemanticRememberResult, error) {
	input = normalizeSemanticRememberInput(input)
	if err := validateSemanticRememberInput(input); err != nil {
		return nil, err
	}

	var result SemanticRememberResult
	err := r.withTeamTx(ctx, input.TeamID, func(tx *gorm.DB) error {
		if err := upsertSemanticRefs(ctx, tx, input); err != nil {
			return err
		}

		evidence := make([]domain.SemanticEvidenceFragment, 0, len(input.Evidence))
		for _, item := range input.Evidence {
			stored, err := insertSemanticEvidence(ctx, tx, input, item)
			if err != nil {
				return err
			}
			evidence = append(evidence, stored)
		}
		result.Evidence = evidence
		for _, item := range evidence {
			if err := upsertSemanticSearchDocument(ctx, tx, semanticSearchDocumentInput{
				TeamID:         input.TeamID,
				OwnerProfileID: input.OwnerProfileID,
				SourceType:     "evidence",
				SourceID:       item.FragmentID,
				DocumentText:   item.Content,
				SourceVersion:  1,
			}); err != nil {
				return err
			}
		}

		entityByKey := map[string]domain.SemanticEntity{}
		for inputIndex, item := range input.Relationships {
			storedEvidence := evidence[item.EvidenceIndex]
			item.SourceGroup = storedEvidence.SourceGroup
			observationID, err := insertSemanticRelationshipObservation(ctx, tx, input, storedEvidence.FragmentID, item)
			if err != nil {
				return err
			}
			if item.ObservationOnly {
				if err := insertSemanticVerificationEvent(ctx, tx, input.TeamID, "", observationID, item); err != nil {
					return err
				}
				continue
			}

			subject, err := upsertSemanticEntity(ctx, tx, input, item.SubjectName, item.SubjectKind)
			if err != nil {
				return err
			}
			entityByKey[semanticEntityKey(subject.CanonicalName, subject.Kind)] = subject
			if err := upsertSemanticSearchDocument(ctx, tx, semanticSearchDocumentInput{
				TeamID:         input.TeamID,
				OwnerProfileID: input.OwnerProfileID,
				SourceType:     "entity",
				SourceID:       subject.EntityID,
				DocumentText:   semanticEntitySearchText(subject),
				SourceVersion:  1,
			}); err != nil {
				return err
			}

			objectEntityID := ""
			objectEntityName := ""
			if strings.TrimSpace(item.ObjectName) != "" {
				object, err := upsertSemanticEntity(ctx, tx, input, item.ObjectName, item.ObjectKind)
				if err != nil {
					return err
				}
				entityByKey[semanticEntityKey(object.CanonicalName, object.Kind)] = object
				objectEntityID = object.EntityID
				objectEntityName = object.CanonicalName
				if err := upsertSemanticSearchDocument(ctx, tx, semanticSearchDocumentInput{
					TeamID:         input.TeamID,
					OwnerProfileID: input.OwnerProfileID,
					SourceType:     "entity",
					SourceID:       object.EntityID,
					DocumentText:   semanticEntitySearchText(object),
					SourceVersion:  1,
				}); err != nil {
					return err
				}
			}

			relationship, err := upsertSemanticRelationship(ctx, tx, input, item, subject.EntityID, objectEntityID)
			if err != nil {
				return err
			}
			result.Relationships = append(result.Relationships, relationship)
			result.RelationshipInputIndexes = append(result.RelationshipInputIndexes, inputIndex)
			if err := insertSemanticVerificationEvent(ctx, tx, input.TeamID, relationship.RelationshipID, observationID, item); err != nil {
				return err
			}

			if semanticRelationshipSearchEligible(relationship) && item.EvidenceVerdict == "entailed" {
				support, err := insertSemanticRelationshipSupport(ctx, tx, input.TeamID, relationship.RelationshipID, storedEvidence.FragmentID, item)
				if err != nil {
					return err
				}
				result.Supports = append(result.Supports, support)
				if err := refreshSemanticRelationshipSupportCounts(ctx, tx, input.TeamID, relationship.RelationshipID); err != nil {
					return err
				}
				objectText := firstNonEmpty(objectEntityName, item.ObjectValue)
				if err := upsertSemanticSearchDocument(ctx, tx, semanticSearchDocumentInput{
					TeamID:         input.TeamID,
					OwnerProfileID: input.OwnerProfileID,
					SourceType:     "relationship",
					SourceID:       relationship.RelationshipID,
					DocumentText:   semanticRelationshipSearchText(subject.CanonicalName, item.Predicate, item.Polarity, objectText),
					SourceVersion:  relationship.Version,
				}); err != nil {
					return err
				}
			}
		}
		entityKeys := make([]string, 0, len(entityByKey))
		for key := range entityByKey {
			entityKeys = append(entityKeys, key)
		}
		sort.Strings(entityKeys)
		for _, key := range entityKeys {
			result.Entities = append(result.Entities, entityByKey[key])
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("semantic remember: %w", err)
	}
	return &result, nil
}

func (r *SemanticRepositoryImpl) LoadSemanticVerifierContext(ctx context.Context, teamID string, relationships []SemanticRelationshipInput) (SemanticVerifierContext, error) {
	teamID = strings.TrimSpace(teamID)
	out := SemanticVerifierContext{
		EntityCandidates:       map[string][]SemanticEntityCandidate{},
		RelationshipCandidates: map[int][]SemanticRelationshipCandidate{},
	}
	if teamID == "" || len(relationships) == 0 {
		return out, nil
	}
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		for i, relationship := range relationships {
			if candidates, err := loadSemanticEntityCandidates(ctx, tx, teamID, relationship.SubjectName, relationship.SubjectKind); err != nil {
				return err
			} else {
				out.EntityCandidates[SemanticVerifierEntityCandidateKey(relationship.SubjectName, relationship.SubjectKind)] = candidates
			}
			if strings.TrimSpace(relationship.ObjectName) != "" {
				if candidates, err := loadSemanticEntityCandidates(ctx, tx, teamID, relationship.ObjectName, relationship.ObjectKind); err != nil {
					return err
				} else {
					out.EntityCandidates[SemanticVerifierEntityCandidateKey(relationship.ObjectName, relationship.ObjectKind)] = candidates
				}
			}
			candidates, err := loadSemanticRelationshipCandidates(ctx, tx, teamID, relationship)
			if err != nil {
				return err
			}
			out.RelationshipCandidates[i] = candidates
		}
		return nil
	})
	if err != nil {
		return SemanticVerifierContext{}, fmt.Errorf("semantic verifier context: %w", err)
	}
	return out, nil
}

func (r *SemanticRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if r.rls != nil {
		return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
	}
	return r.db.WithContext(ctx).Transaction(fn)
}

func (r *SemanticRepositoryImpl) withSystemTx(ctx context.Context, fn func(tx *gorm.DB) error) error {
	if r.rls != nil {
		return r.rls.WithSystemTx(ctx, r.db, fn)
	}
	return r.db.WithContext(ctx).Transaction(fn)
}

func normalizeSemanticRememberInput(input SemanticRememberInput) SemanticRememberInput {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.TeamName = strings.TrimSpace(input.TeamName)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	input.OwnerProfileName = strings.TrimSpace(input.OwnerProfileName)
	for i := range input.Evidence {
		input.Evidence[i].Content = strings.TrimSpace(input.Evidence[i].Content)
		input.Evidence[i].Source = strings.TrimSpace(input.Evidence[i].Source)
		input.Evidence[i].SourceDocID = strings.TrimSpace(input.Evidence[i].SourceDocID)
		input.Evidence[i].SourceGroup = strings.TrimSpace(input.Evidence[i].SourceGroup)
		input.Evidence[i].IdempotencyKey = strings.TrimSpace(input.Evidence[i].IdempotencyKey)
		if input.Evidence[i].Labels == nil {
			input.Evidence[i].Labels = []string{}
		}
		if input.Evidence[i].SourceType == "" {
			input.Evidence[i].SourceType = domain.SourceTypeConversation
		}
		if input.Evidence[i].Authority == "" {
			input.Evidence[i].Authority = domain.AuthorityPrimary
		}
	}
	for i := range input.Relationships {
		input.Relationships[i].SubjectName = strings.TrimSpace(input.Relationships[i].SubjectName)
		input.Relationships[i].Predicate = strings.TrimSpace(input.Relationships[i].Predicate)
		input.Relationships[i].ObjectName = strings.TrimSpace(input.Relationships[i].ObjectName)
		input.Relationships[i].ObjectValue = strings.TrimSpace(input.Relationships[i].ObjectValue)
		input.Relationships[i].Quote = strings.TrimSpace(input.Relationships[i].Quote)
		input.Relationships[i].ExtractionModel = strings.TrimSpace(input.Relationships[i].ExtractionModel)
		input.Relationships[i].ExtractionRawJSON = strings.TrimSpace(input.Relationships[i].ExtractionRawJSON)
		input.Relationships[i].VerifierModel = strings.TrimSpace(input.Relationships[i].VerifierModel)
		input.Relationships[i].EvidenceVerdict = strings.TrimSpace(input.Relationships[i].EvidenceVerdict)
		input.Relationships[i].KnowledgeAlignment = strings.TrimSpace(input.Relationships[i].KnowledgeAlignment)
		input.Relationships[i].VerificationRationale = strings.TrimSpace(input.Relationships[i].VerificationRationale)
		input.Relationships[i].VerificationRawJSON = strings.TrimSpace(input.Relationships[i].VerificationRawJSON)
	}
	return input
}

func validateSemanticRememberInput(input SemanticRememberInput) error {
	if _, err := uuid.Parse(input.TeamID); err != nil {
		return fmt.Errorf("team_id is required: %w", err)
	}
	if _, err := uuid.Parse(input.OwnerProfileID); err != nil {
		return fmt.Errorf("owner_profile_id is required: %w", err)
	}
	if len(input.Evidence) == 0 {
		return errors.New("evidence is required")
	}
	for i, evidence := range input.Evidence {
		if evidence.Content == "" {
			return fmt.Errorf("evidence[%d].content is required", i)
		}
		if !evidence.SourceType.IsValid() {
			return fmt.Errorf("evidence[%d].source_type is invalid", i)
		}
	}
	for i, relationship := range input.Relationships {
		if relationship.SubjectName == "" {
			return fmt.Errorf("relationships[%d].subject_name is required", i)
		}
		if relationship.Predicate == "" {
			return fmt.Errorf("relationships[%d].predicate is required", i)
		}
		if (relationship.ObjectName == "") == (relationship.ObjectValue == "") {
			return fmt.Errorf("relationships[%d] must set exactly one object form", i)
		}
		if !relationship.SubjectKind.IsValid() {
			return fmt.Errorf("relationships[%d].subject_kind is invalid", i)
		}
		if !relationship.ObjectKind.IsValid() {
			return fmt.Errorf("relationships[%d].object_kind is invalid", i)
		}
		if !relationship.Tier.IsValid() {
			return fmt.Errorf("relationships[%d].tier is invalid", i)
		}
		if !relationship.Polarity.IsValid() {
			return fmt.Errorf("relationships[%d].polarity is invalid", i)
		}
		if !relationship.Status.IsValid() {
			return fmt.Errorf("relationships[%d].status is invalid", i)
		}
		if relationship.Confidence < 0 || relationship.Confidence > 1 {
			return fmt.Errorf("relationships[%d].confidence is out of range", i)
		}
		if relationship.Tier == domain.SemanticTierCandidate && relationship.Status == domain.SemanticStatusActive {
			return fmt.Errorf("relationships[%d] candidate tier cannot be active", i)
		}
		if relationship.Status == domain.SemanticStatusActive &&
			relationship.Tier != domain.SemanticTierValidatedClaim && relationship.Tier != domain.SemanticTierFact {
			return fmt.Errorf("relationships[%d] active status requires validated_claim or fact tier", i)
		}
		if !semanticOneOf(relationship.EvidenceVerdict, "entailed", "contradicted", "insufficient") {
			return fmt.Errorf("relationships[%d].evidence_verdict is invalid", i)
		}
		if !semanticOneOf(relationship.KnowledgeAlignment, "novel", "duplicate", "corroborates", "conflicts", "updates_existing", "ambiguous") {
			return fmt.Errorf("relationships[%d].knowledge_alignment is invalid", i)
		}
		if relationship.EvidenceIndex < 0 || relationship.EvidenceIndex >= len(input.Evidence) {
			return fmt.Errorf("relationships[%d].evidence_index is out of range", i)
		}
	}
	return nil
}

func upsertSemanticRefs(ctx context.Context, tx *gorm.DB, input SemanticRememberInput) error {
	if err := tx.WithContext(ctx).Exec(`
		INSERT INTO semantic_team_refs(team_id, name, created_at, updated_at)
		VALUES (?, ?, now(), now())
		ON CONFLICT (team_id) DO UPDATE SET
		    name = COALESCE(NULLIF(EXCLUDED.name, ''), semantic_team_refs.name),
		    updated_at = now()
	`, input.TeamID, input.TeamName).Error; err != nil {
		return err
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO semantic_profile_refs(team_id, profile_id, name, created_at, updated_at)
		VALUES (?, ?, ?, now(), now())
		ON CONFLICT (team_id, profile_id) DO UPDATE SET
		    name = COALESCE(NULLIF(EXCLUDED.name, ''), semantic_profile_refs.name),
		    updated_at = now()
	`, input.TeamID, input.OwnerProfileID, input.OwnerProfileName).Error
}

func SemanticVerifierEntityCandidateKey(name string, kind domain.SemanticEntityKind) string {
	return strings.ToLower(strings.TrimSpace(name)) + "|" + string(kind)
}

func loadSemanticEntityCandidates(ctx context.Context, tx *gorm.DB, teamID, name string, kind domain.SemanticEntityKind) ([]SemanticEntityCandidate, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT entity_id::text, canonical_name, kind
		FROM semantic_entities
		WHERE team_id = ?
		  AND status = 'active'
		  AND lower(canonical_name) = lower(?)
		  AND kind = ?
		ORDER BY updated_at DESC, entity_id ASC
		LIMIT 5
	`, teamID, name, string(kind)).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []SemanticEntityCandidate{}
	for rows.Next() {
		var candidate SemanticEntityCandidate
		var candidateKind string
		if err := rows.Scan(&candidate.EntityID, &candidate.CanonicalName, &candidateKind); err != nil {
			return nil, err
		}
		candidate.Kind = domain.SemanticEntityKind(candidateKind)
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func loadSemanticRelationshipCandidates(ctx context.Context, tx *gorm.DB, teamID string, relationship SemanticRelationshipInput) ([]SemanticRelationshipCandidate, error) {
	subjectName := strings.TrimSpace(relationship.SubjectName)
	objectName := strings.TrimSpace(relationship.ObjectName)
	objectValue := strings.TrimSpace(relationship.ObjectValue)
	if subjectName == "" && objectName == "" && objectValue == "" {
		return nil, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		SELECT r.relationship_id::text,
		       subject.canonical_name,
		       r.predicate,
		       COALESCE(object.canonical_name, '') AS object_name,
		       r.object_value,
		       r.tier,
		       r.status
		FROM semantic_relationship_records r
		JOIN semantic_entities subject
		  ON subject.team_id = r.team_id
		 AND subject.entity_id = r.subject_entity_id
		LEFT JOIN semantic_entities object
		  ON object.team_id = r.team_id
		 AND object.entity_id = r.object_entity_id
		WHERE r.team_id = ?
		  AND r.status NOT IN ('rejected', 'retracted', 'superseded')
		  AND (
		    lower(subject.canonical_name) = lower(?)
		    OR (? <> '' AND lower(COALESCE(object.canonical_name, '')) = lower(?))
		    OR (? <> '' AND lower(r.object_value) = lower(?))
		  )
		ORDER BY CASE
		    WHEN lower(subject.canonical_name) = lower(?)
		     AND r.predicate = ?
		     AND (
		       (? <> '' AND lower(COALESCE(object.canonical_name, '')) = lower(?))
		       OR (? <> '' AND lower(r.object_value) = lower(?))
		     ) THEN 0
		    WHEN lower(subject.canonical_name) = lower(?) THEN 1
		    ELSE 2
		  END,
		  r.updated_at DESC,
		  r.relationship_id ASC
		LIMIT 64
	`, teamID, subjectName, objectName, objectName, objectValue, objectValue,
		subjectName, strings.TrimSpace(relationship.Predicate), objectName, objectName, objectValue, objectValue,
		subjectName).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates := []SemanticRelationshipCandidate{}
	for rows.Next() {
		var candidate SemanticRelationshipCandidate
		var tier, status string
		if err := rows.Scan(
			&candidate.RelationshipID,
			&candidate.SubjectName,
			&candidate.Predicate,
			&candidate.ObjectName,
			&candidate.ObjectValue,
			&tier,
			&status,
		); err != nil {
			return nil, err
		}
		candidate.Tier = domain.SemanticRelationshipTier(tier)
		candidate.Status = domain.SemanticRelationshipStatus(status)
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

func insertSemanticEvidence(ctx context.Context, tx *gorm.DB, input SemanticRememberInput, evidence SemanticEvidenceInput) (domain.SemanticEvidenceFragment, error) {
	metadata, err := marshalMap(evidence.Metadata)
	if err != nil {
		return domain.SemanticEvidenceFragment{}, err
	}
	contentHash := semanticContentHash(evidence.Content)
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO semantic_evidence_fragments (
		    team_id, owner_profile_id, content, source, source_doc_id, source_group, source_type,
		    authority, labels, metadata, content_hash, idempotency_key,
		    search_state, created_at, updated_at
		) VALUES (
		    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, ?, ?, 'pending', now(), now()
		)
		ON CONFLICT (team_id, idempotency_key) WHERE idempotency_key <> ''
		DO UPDATE SET updated_at = now()
		RETURNING team_id::text, fragment_id::text, owner_profile_id::text,
		          content, source, source_doc_id, source_group, source_type, authority, labels,
		          metadata::text, content_hash, idempotency_key, '' AS embedding_model,
		          embedding_contract_id, created_at
	`, input.TeamID, input.OwnerProfileID, evidence.Content, evidence.Source,
		evidence.SourceDocID, evidence.SourceGroup, string(evidence.SourceType), string(evidence.Authority),
		pq.Array(evidence.Labels), string(metadata), contentHash, evidence.IdempotencyKey).Rows()
	if err != nil {
		return domain.SemanticEvidenceFragment{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return domain.SemanticEvidenceFragment{}, sql.ErrNoRows
	}
	out, err := scanSemanticEvidence(rows)
	if err != nil {
		return domain.SemanticEvidenceFragment{}, err
	}
	return out, rows.Err()
}

func upsertSemanticEntity(ctx context.Context, tx *gorm.DB, input SemanticRememberInput, name string, kind domain.SemanticEntityKind) (domain.SemanticEntity, error) {
	if kind == "" {
		kind = domain.SemanticEntityUnknown
	}
	var existing domain.SemanticEntity
	found, err := scanOptionalSemanticEntity(tx.WithContext(ctx).Raw(`
		SELECT team_id::text, entity_id::text, owner_profile_id::text,
		       kind, canonical_name, metadata::text, created_at, updated_at
		FROM semantic_entities
		WHERE team_id = ?
		  AND lower(canonical_name) = lower(?)
		  AND kind = ?
		  AND status = 'active'
		LIMIT 1
	`, input.TeamID, name, string(kind)).Rows())
	if err != nil {
		return domain.SemanticEntity{}, err
	}
	if found != nil {
		if err := upsertSemanticEntityName(ctx, tx, input.TeamID, found.EntityID, name); err != nil {
			return domain.SemanticEntity{}, err
		}
		return *found, nil
	}

	metadata := []byte("{}")
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO semantic_entities (
		    team_id, owner_profile_id, kind, canonical_name, metadata, status,
		    created_at, updated_at
		) VALUES (
		    ?, ?, ?, ?, ?::jsonb, 'active', now(), now()
		)
		ON CONFLICT DO NOTHING
		RETURNING team_id::text, entity_id::text, owner_profile_id::text,
		          kind, canonical_name, metadata::text, created_at, updated_at
	`, input.TeamID, input.OwnerProfileID, string(kind), name, string(metadata)).Rows()
	if err != nil {
		return domain.SemanticEntity{}, err
	}
	if rows.Next() {
		if err := rows.Scan(
			&existing.TeamID,
			&existing.EntityID,
			&existing.OwnerProfileID,
			(*stringEntityKind)(&existing.Kind),
			&existing.CanonicalName,
			(*jsonMapScanner)(&existing.Metadata),
			&existing.CreatedAt,
			&existing.UpdatedAt,
		); err != nil {
			_ = rows.Close()
			return domain.SemanticEntity{}, err
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return domain.SemanticEntity{}, err
		}
		if err := rows.Close(); err != nil {
			return domain.SemanticEntity{}, err
		}
		if err := upsertSemanticEntityName(ctx, tx, input.TeamID, existing.EntityID, name); err != nil {
			return domain.SemanticEntity{}, err
		}
		return existing, nil
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return domain.SemanticEntity{}, err
	}
	if err := rows.Close(); err != nil {
		return domain.SemanticEntity{}, err
	}
	found, err = scanOptionalSemanticEntity(tx.WithContext(ctx).Raw(`
		SELECT team_id::text, entity_id::text, owner_profile_id::text,
		       kind, canonical_name, metadata::text, created_at, updated_at
		FROM semantic_entities
		WHERE team_id = ?
		  AND lower(canonical_name) = lower(?)
		  AND kind = ?
		  AND status = 'active'
		LIMIT 1
	`, input.TeamID, name, string(kind)).Rows())
	if err != nil {
		return domain.SemanticEntity{}, err
	}
	if found == nil {
		return domain.SemanticEntity{}, sql.ErrNoRows
	}
	if err := upsertSemanticEntityName(ctx, tx, input.TeamID, found.EntityID, name); err != nil {
		return domain.SemanticEntity{}, err
	}
	return *found, nil
}

func upsertSemanticEntityName(ctx context.Context, tx *gorm.DB, teamID, entityID, name string) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO semantic_entity_names (team_id, entity_id, name, created_at)
		VALUES (?, ?, ?, now())
		ON CONFLICT (team_id, entity_id, lower(name)) DO NOTHING
	`, teamID, entityID, name).Error
}

func upsertSemanticRelationship(ctx context.Context, tx *gorm.DB, input SemanticRememberInput, item SemanticRelationshipInput, subjectEntityID, objectEntityID string) (domain.SemanticRelationship, error) {
	semanticGroupKey := semanticRelationshipGroupKey(subjectEntityID, item.Predicate, objectEntityID, item.ObjectValue, item.Polarity)
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO semantic_relationship_records (
		    team_id, owner_profile_id, subject_entity_id, predicate,
		    polarity, object_entity_id, object_value, object_kind, tier, status,
		    confidence, semantic_group_key, search_state, recorded_at, created_at, updated_at
		) VALUES (
		    ?, ?, ?, ?, ?, nullif(?, '')::uuid, ?, ?, ?, ?, ?, ?, ?, now(), now(), now()
		)
		ON CONFLICT DO NOTHING
		RETURNING `+semanticRelationshipColumns()+`
	`, input.TeamID, input.OwnerProfileID, subjectEntityID, item.Predicate, string(item.Polarity),
		objectEntityID, item.ObjectValue, string(item.ObjectKind), string(item.Tier),
		string(item.Status), item.Confidence, semanticGroupKey, semanticRelationshipSearchState(item)).Rows()
	if err != nil {
		return domain.SemanticRelationship{}, err
	}
	var inserted domain.SemanticRelationship
	insertedRelationship := false
	if rows.Next() {
		rel, err := scanSemanticRelationship(rows)
		if err != nil {
			_ = rows.Close()
			return domain.SemanticRelationship{}, err
		}
		inserted = rel
		insertedRelationship = true
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return domain.SemanticRelationship{}, err
	}
	if err := rows.Close(); err != nil {
		return domain.SemanticRelationship{}, err
	}
	if insertedRelationship {
		if err := insertSemanticRelationshipEvent(ctx, tx, input, inserted, "created"); err != nil {
			return domain.SemanticRelationship{}, err
		}
		return inserted, nil
	}

	foundRows, err := tx.WithContext(ctx).Raw(`
		SELECT `+semanticRelationshipColumns()+`
		FROM semantic_relationship_records
		WHERE team_id = ?
		  AND owner_profile_id = ?
		  AND subject_entity_id = ?
		  AND predicate = ?
		  AND polarity = ?
		  AND COALESCE(object_entity_id, '00000000-0000-0000-0000-000000000000'::uuid) = COALESCE(nullif(?, '')::uuid, '00000000-0000-0000-0000-000000000000'::uuid)
		  AND object_value = ?
		LIMIT 1
	`, input.TeamID, input.OwnerProfileID, subjectEntityID, item.Predicate, string(item.Polarity), objectEntityID, item.ObjectValue).Rows()
	if err != nil {
		return domain.SemanticRelationship{}, err
	}
	if !foundRows.Next() {
		_ = foundRows.Close()
		return domain.SemanticRelationship{}, sql.ErrNoRows
	}
	existing, err := scanSemanticRelationship(foundRows)
	if err != nil {
		_ = foundRows.Close()
		return domain.SemanticRelationship{}, err
	}
	if err := foundRows.Close(); err != nil {
		return domain.SemanticRelationship{}, err
	}
	return updateSemanticRelationshipLifecycle(ctx, tx, input, item, existing)
}

func updateSemanticRelationshipLifecycle(ctx context.Context, tx *gorm.DB, input SemanticRememberInput, item SemanticRelationshipInput, existing domain.SemanticRelationship) (domain.SemanticRelationship, error) {
	tier := existing.Tier
	status := existing.Status
	confidence := existing.Confidence
	searchState := "not_required"
	if semanticRelationshipSearchEligible(existing) {
		searchState = "pending"
	}
	if item.Status == domain.SemanticStatusActive {
		status = domain.SemanticStatusActive
		if tier != domain.SemanticTierFact {
			tier = item.Tier
		}
		confidence = item.Confidence
		searchState = "pending"
	} else if existing.Status != domain.SemanticStatusActive {
		tier = item.Tier
		status = item.Status
		confidence = item.Confidence
		searchState = "not_required"
	}
	if tier == existing.Tier && status == existing.Status && confidence == existing.Confidence {
		return existing, nil
	}
	rows, err := tx.WithContext(ctx).Raw(`
		UPDATE semantic_relationship_records
		SET tier = ?, status = ?, confidence = ?, search_state = ?,
		    version = version + 1, updated_at = now()
		WHERE team_id = ? AND relationship_id = ?
		RETURNING `+semanticRelationshipColumns()+`
	`, string(tier), string(status), confidence, searchState, input.TeamID, existing.RelationshipID).Rows()
	if err != nil {
		return domain.SemanticRelationship{}, err
	}
	if !rows.Next() {
		_ = rows.Close()
		return domain.SemanticRelationship{}, sql.ErrNoRows
	}
	updated, err := scanSemanticRelationship(rows)
	if err != nil {
		_ = rows.Close()
		return domain.SemanticRelationship{}, err
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return domain.SemanticRelationship{}, err
	}
	if err := rows.Close(); err != nil {
		return domain.SemanticRelationship{}, err
	}
	if err := insertSemanticRelationshipEvent(ctx, tx, input, updated, "lifecycle_updated"); err != nil {
		return domain.SemanticRelationship{}, err
	}
	return updated, nil
}

func semanticRelationshipSearchState(item SemanticRelationshipInput) string {
	if item.Status == domain.SemanticStatusActive &&
		(item.Tier == domain.SemanticTierValidatedClaim || item.Tier == domain.SemanticTierFact) {
		return "pending"
	}
	return "not_required"
}

func insertSemanticRelationshipObservation(ctx context.Context, tx *gorm.DB, input SemanticRememberInput, fragmentID string, item SemanticRelationshipInput) (string, error) {
	raw, err := semanticJSONPayload(item.ExtractionRawJSON)
	if err != nil {
		return "", fmt.Errorf("semantic observation extraction_raw: %w", err)
	}
	spanStart, spanEnd := normalizedSemanticSpan(item.SpanStart, item.SpanEnd)
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO semantic_relationship_observations (
		    team_id, owner_profile_id, fragment_id,
		    subject_text, predicate_text, object_text,
		    evidence_index, span_start, span_end,
		    extraction_model, extraction_raw, created_at
		) VALUES (
		    ?, ?, ?::uuid, ?, ?, ?, ?, ?, ?, ?, ?::jsonb, now()
		)
		RETURNING observation_id::text
	`, input.TeamID, input.OwnerProfileID, fragmentID, item.SubjectName, item.Predicate,
		firstNonEmpty(item.ObjectName, item.ObjectValue), item.EvidenceIndex, spanStart, spanEnd,
		item.ExtractionModel, raw).Rows()
	if err != nil {
		return "", err
	}
	defer rows.Close()
	if !rows.Next() {
		return "", sql.ErrNoRows
	}
	var observationID string
	if err := rows.Scan(&observationID); err != nil {
		return "", err
	}
	return observationID, rows.Err()
}

func insertSemanticVerificationEvent(ctx context.Context, tx *gorm.DB, teamID, relationshipID, observationID string, item SemanticRelationshipInput) error {
	raw, err := semanticJSONPayload(item.VerificationRawJSON)
	if err != nil {
		return fmt.Errorf("semantic verification raw_response: %w", err)
	}
	return tx.WithContext(ctx).Exec(`
		INSERT INTO semantic_verification_events (
		    team_id, relationship_id, observation_id,
		    verifier_model, evidence_verdict, knowledge_alignment,
		    confidence, rationale, raw_response, created_at
		) VALUES (
		    ?, nullif(?, '')::uuid, nullif(?, '')::uuid,
		    ?, ?, ?, ?, ?, ?::jsonb, now()
		)
	`, teamID, relationshipID, observationID, item.VerifierModel, item.EvidenceVerdict,
		item.KnowledgeAlignment, item.Confidence, item.VerificationRationale, raw).Error
}

func insertSemanticRelationshipSupport(ctx context.Context, tx *gorm.DB, teamID, relationshipID, fragmentID string, item SemanticRelationshipInput) (domain.SemanticRelationshipSupport, error) {
	spanStart, spanEnd := normalizedSemanticSpan(item.SpanStart, item.SpanEnd)
	rows, err := tx.WithContext(ctx).Raw(`
		INSERT INTO semantic_relationship_supports (
		    team_id, relationship_id, fragment_id, evidence_index, span_start, span_end, source_group, quote, created_at
		) VALUES (
		    ?, ?, ?, ?, ?, ?, ?, ?, now()
		)
		ON CONFLICT (team_id, relationship_id, fragment_id, span_start, span_end)
		DO UPDATE SET quote = EXCLUDED.quote
		RETURNING team_id::text, relationship_id::text, fragment_id::text,
		          evidence_index, quote, created_at
	`, teamID, relationshipID, fragmentID, item.EvidenceIndex, spanStart, spanEnd, item.SourceGroup, item.Quote).Rows()
	if err != nil {
		return domain.SemanticRelationshipSupport{}, err
	}
	defer rows.Close()
	if !rows.Next() {
		return domain.SemanticRelationshipSupport{}, sql.ErrNoRows
	}
	var support domain.SemanticRelationshipSupport
	if err := rows.Scan(&support.TeamID, &support.RelationshipID, &support.FragmentID, &support.EvidenceIndex, &support.Quote, &support.CreatedAt); err != nil {
		return domain.SemanticRelationshipSupport{}, err
	}
	return support, rows.Err()
}

func insertSemanticRelationshipEvent(ctx context.Context, tx *gorm.DB, input SemanticRememberInput, rel domain.SemanticRelationship, eventType string) error {
	return tx.WithContext(ctx).Exec(`
		INSERT INTO semantic_relationship_events (
		    team_id, relationship_id, event_type, to_tier, to_status,
		    actor_profile_id, occurred_at
		) VALUES (
		    ?, ?, ?, ?, ?, ?, now()
		)
	`, input.TeamID, rel.RelationshipID, eventType, string(rel.Tier), string(rel.Status), input.OwnerProfileID).Error
}
