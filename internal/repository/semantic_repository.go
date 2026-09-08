package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	dreampostgres "github.com/markhuangai/dense-mem/internal/dream/postgres"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
)

var ErrSemanticOwnerMismatch = knowledgecontract.ErrSemanticOwnerMismatch
var ErrSemanticIdempotencyConflict = knowledgecontract.ErrSemanticIdempotencyConflict
var ErrSemanticIdentityAlias = knowledgecontract.ErrSemanticIdentityAlias

type SemanticRepositoryImpl struct {
	db             *gorm.DB
	rls            rLSHelper
	knowledgeOwner *knowledgepostgres.Store
	dreamAdapter   *dreampostgres.Store
}

var _ SemanticRepository = (*SemanticRepositoryImpl)(nil)

func NewSemanticRepository(db *gorm.DB, rls *postgres.RLS) *SemanticRepositoryImpl {
	return &SemanticRepositoryImpl{
		db: db, rls: rls,
		knowledgeOwner: knowledgepostgres.NewStore(db, rls, knowledgecontract.ConflictRuntimeConfig{}),
		dreamAdapter:   dreampostgres.NewStore(db, rls),
	}
}

// DreamDatabase and DreamRLS expose the narrow construction seam used by the
// Dream-owned composition boundary. They do not expose a repository handle or
// permit callers to choose transaction mode.
func (r *SemanticRepositoryImpl) DreamDatabase() *gorm.DB {
	if r == nil {
		return nil
	}
	return r.db
}

func (r *SemanticRepositoryImpl) DreamRLS() postgres.RLSHelper {
	if r == nil {
		return nil
	}
	return r.rls
}

func (r *SemanticRepositoryImpl) dreamOwner() *dreampostgres.Store {
	if r == nil {
		return nil
	}
	if r.dreamAdapter == nil {
		r.dreamAdapter = dreampostgres.NewStore(r.db, r.rls)
	}
	return r.dreamAdapter
}

func (r *SemanticRepositoryImpl) CreateEntity(ctx context.Context, input CreateEntityInput) (*EntityRecord, error) {
	owner, err := r.semanticWriteOwner()
	if err != nil {
		return nil, err
	}
	result, err := owner.CreateEntity(ctx, toKnowledgeCreateEntityInput(input))
	return fromKnowledgeEntityRecord(result), err
}

func (r *SemanticRepositoryImpl) AddEntityName(ctx context.Context, input AddEntityNameInput) (string, error) {
	owner, err := r.semanticWriteOwner()
	if err != nil {
		return "", err
	}
	return owner.AddEntityName(ctx, toKnowledgeAddEntityNameInput(input))
}

func (r *SemanticRepositoryImpl) UpsertValue(ctx context.Context, input UpsertValueInput) (*ValueRecord, error) {
	owner, err := r.semanticWriteOwner()
	if err != nil {
		return nil, err
	}
	result, err := owner.UpsertValue(ctx, toKnowledgeUpsertValueInput(input))
	return fromKnowledgeValueRecord(result), err
}

func (r *SemanticRepositoryImpl) ApplyRelationshipDecision(ctx context.Context, input ApplyRelationshipDecisionInput) (*RelationshipDecisionResult, error) {
	owner, err := r.semanticWriteOwner()
	if err != nil {
		return nil, err
	}
	result, err := owner.ApplyRelationshipDecision(ctx, toKnowledgeApplyRelationshipDecisionInput(input))
	return fromKnowledgeRelationshipDecisionResult(result), err
}

func (r *SemanticRepositoryImpl) RetractRelationship(ctx context.Context, input RetractRelationshipInput) (*RelationshipTransitionResult, error) {
	owner, err := r.semanticWriteOwner()
	if err != nil {
		return nil, err
	}
	result, err := owner.RetractRelationship(ctx, toKnowledgeRetractRelationshipInput(input))
	return fromKnowledgeRelationshipTransitionResult(result), err
}

func (r *SemanticRepositoryImpl) ApplyRelationshipSupportDecision(ctx context.Context, input ApplyRelationshipSupportDecisionInput) (*RelationshipSupportDecisionResult, error) {
	owner, err := r.semanticWriteOwner()
	if err != nil {
		return nil, err
	}
	result, err := owner.ApplyRelationshipSupportDecision(ctx, toKnowledgeApplyRelationshipSupportDecisionInput(input))
	return fromKnowledgeRelationshipSupportDecisionResult(result), err
}

func (r *SemanticRepositoryImpl) AppendCrossReference(ctx context.Context, input AppendCrossReferenceInput) (string, error) {
	owner, err := r.semanticWriteOwner()
	if err != nil {
		return "", err
	}
	return owner.AppendCrossReference(ctx, toKnowledgeAppendCrossReferenceInput(input))
}

func (r *SemanticRepositoryImpl) semanticWriteOwner() (*knowledgepostgres.Store, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("semantic: database is required")
	}
	owner := r.knowledgeWriteOwner()
	if owner == nil {
		return nil, errors.New("semantic: knowledge write owner is required")
	}
	return owner, nil
}

// CreateHypothesis remains owned by the Dream capability until its cutover.
func (r *SemanticRepositoryImpl) CreateHypothesis(ctx context.Context, input CreateHypothesisInput) (string, error) {
	input = normalizeCreateHypothesisInput(input)
	if err := validateCreateHypothesisInput(input); err != nil {
		return "", err
	}
	var hypothesisID string
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, input.TeamID); err != nil {
			return err
		}
		payload, err := marshalJSON(input.Payload)
		if err != nil {
			return err
		}
		rows, err := tx.WithContext(ctx).Raw(`
			INSERT INTO hypotheses (team_id, created_by_profile_id, status, payload)
			VALUES (?::uuid, ?::uuid, ?, ?::jsonb)
			RETURNING hypothesis_id::text
		`, input.TeamID, input.OwnerProfileID, input.Status, string(payload)).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		if !rows.Next() {
			return rows.Err()
		}
		return rows.Scan(&hypothesisID)
	})
	if err != nil {
		return "", fmt.Errorf("semantic: create hypothesis: %w", err)
	}
	return hypothesisID, nil
}

// ListSemanticEdges remains the graph read owner until the Graph cutover.
func (r *SemanticRepositoryImpl) ListSemanticEdges(ctx context.Context, teamID string, limit int) ([]SemanticEdge, error) {
	teamID = strings.TrimSpace(teamID)
	if _, err := uuid.Parse(teamID); err != nil {
		return nil, fmt.Errorf("team_id is required: %w", err)
	}
	if limit <= 0 || limit > 500 {
		return nil, errors.New("limit must be between 1 and 500")
	}
	var edges []SemanticEdge
	err := r.withTeamTx(ctx, teamID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			SELECT team_id::text, relationship_id::text, owner_profile_id::text,
			       semantic_group_key, subject_entity_id::text, predicate_key,
			       predicate_version, COALESCE(object_entity_id::text, ''),
			       COALESCE(object_value_id::text, ''), relationship_kind,
			       current_cardinality, polarity, COALESCE(scope_key, ''),
			       support_count, source_group_count, version
			FROM semantic_edges
			WHERE team_id = ?::uuid
			ORDER BY relationship_id
			LIMIT ?
		`, teamID, limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			edge := SemanticEdge{}
			if err := rows.Scan(&edge.TeamID, &edge.RelationshipID, &edge.OwnerProfileID,
				&edge.SemanticGroupKey, &edge.SubjectEntityID, &edge.PredicateKey,
				&edge.PredicateVersion, &edge.ObjectEntityID, &edge.ObjectValueID,
				&edge.RelationshipKind, &edge.CurrentCardinality, &edge.Polarity,
				&edge.ScopeKey, &edge.SupportCount, &edge.SourceGroupCount, &edge.Version); err != nil {
				return err
			}
			edges = append(edges, edge)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: list semantic edges: %w", err)
	}
	return edges, nil
}

func (r *SemanticRepositoryImpl) withTeamProfileTx(ctx context.Context, teamID, profileID string, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("semantic: database is required")
	}
	if r.rls == nil {
		return errors.New("semantic: rls helper is required")
	}
	return r.rls.WithTeamProfileTx(ctx, r.db, teamID, profileID, func(tx *gorm.DB) error {
		if err := ensureActiveTeamForMutation(ctx, tx, teamID); err != nil {
			return err
		}
		return fn(tx)
	})
}

func (r *SemanticRepositoryImpl) withTeamTx(ctx context.Context, teamID string, fn func(tx *gorm.DB) error) error {
	if r == nil || r.db == nil {
		return errors.New("semantic: database is required")
	}
	if r.rls == nil {
		return errors.New("semantic: rls helper is required")
	}
	return r.rls.WithTeamTx(ctx, r.db, teamID, fn)
}
