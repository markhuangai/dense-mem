package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	graphpostgres "github.com/markhuangai/dense-mem/internal/graph/postgres"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
	tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"
)

var searchTestContractSequence atomic.Int32

type privacyTraceFixtureStore struct{ trace *tracepostgres.Store }

func newPrivacyTraceFixtureStore(db *gorm.DB, rls storagepostgres.RLSHelper) *privacyTraceFixtureStore {
	trace := tracepostgres.New(db, rls, func(ctx context.Context, tx *gorm.DB, input graphcontract.Query, spaceID string) (*graphcontract.Snapshot, error) {
		rows, err := graphpostgres.LoadLocalRows(ctx, tx, input, spaceID)
		if err != nil {
			return nil, err
		}
		return graphpostgres.Snapshot(input, rows), nil
	}, nil)
	return &privacyTraceFixtureStore{trace: trace}
}

func (s *privacyTraceFixtureStore) TraceRelationship(ctx context.Context, input tracepostgres.TraceRelationshipInput) (*tracepostgres.RelationshipTraceResult, error) {
	return s.trace.TraceRelationship(ctx, input)
}

func createLedgerSSOIdentity(t *testing.T, db *gorm.DB, rls storagepostgres.RLSHelper, teamID uuid.UUID) uuid.UUID {
	t.Helper()
	providerID := uuid.New()
	identityID := uuid.New()
	membershipID := uuid.New()
	now := time.Now().UTC()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO sso_providers (id, name, kind, issuer_url, client_id)
			VALUES (?, ?, 'generic_oidc', 'https://issuer.example.test', ?)
		`, providerID, "provider-"+providerID.String(), "client-"+providerID.String()).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO sso_identities (id, provider_id, subject, email, display_name)
			VALUES (?, ?, ?, ?, ?)
		`, identityID, providerID, "subject-"+identityID.String(), "user-"+identityID.String()+"@example.test", "SSO test user").Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO actor_identities (id, kind, team_id, provider, subject, display_name, active, created_at, updated_at)
			VALUES (?, 'human', NULL, ?, ?, 'SSO test user', true, ?, ?)
		`, identityID, providerID.String(), "subject-"+identityID.String(), now, now).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO team_memberships (
				id, actor_identity_id, team_id, status, team_admin, maximum_grants,
				sso_provider_id, sso_group_id, sso_entitlement_status, created_at, updated_at
			) VALUES (?, ?, ?, 'active', false, ARRAY['read','write']::text[], ?, 'test-group', 'active', ?, ?)
		`, membershipID, identityID, teamID, providerID, now, now).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO membership_grants (membership_id, grant_name, source)
			VALUES (?, 'read', 'explicit'), (?, 'write', 'explicit')
		`, membershipID, membershipID).Error
	}))
	return identityID
}

func createOwnedCredential(t *testing.T, repo *accesspostgres.CredentialRepositoryImpl, teamID, ownerID uuid.UUID, name string, binding domain.CredentialMemoryBinding) *domain.Credential {
	t.Helper()
	id := uuid.New()
	prefix := "dm_" + strings.ReplaceAll(id.String(), "-", "")[:20]
	credential := &domain.Credential{
		ID:              id,
		TeamID:          teamID,
		Name:            name,
		KeyHash:         "hash-" + id.String(),
		KeyPrefix:       prefix,
		KeySuffix:       "suffix",
		Scopes:          []string{"read", "write"},
		RateLimit:       60,
		OwnerIdentityID: &ownerID,
		MemoryBinding:   binding,
	}
	require.NoError(t, repo.CreateCredential(context.Background(), credential))
	return credential
}

func insertSearchTestContract(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, prefix string, dimensions int, strategy string, indexName string) string {
	t.Helper()
	sequence := int(searchTestContractSequence.Add(1))
	contractKey := fmt.Sprintf("%s-%s", prefix, strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
	contractID, generationID := uuid.NewString(), uuid.NewString()
	operatorClass, indexedExpression := "", ""
	switch strategy {
	case "vector_hnsw":
		operatorClass, indexedExpression = "vector_cosine_ops", fmt.Sprintf("embedding::vector(%d)", dimensions)
	case "halfvec_hnsw":
		operatorClass, indexedExpression = "halfvec_cosine_ops", fmt.Sprintf("embedding::halfvec(%d)", dimensions)
	case "binary_hnsw":
		operatorClass, indexedExpression = "bit_hamming_ops", fmt.Sprintf("binary_quantize(embedding)::bit(%d)", dimensions)
	}
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO embedding_contracts (embedding_contract_id, contract_key, version, provider, model, dimensions, distance_metric, vector_normalization, document_format_version, query_format_version, lifecycle_state) VALUES (?::uuid, ?, ?, 'test', ?, ?, 'cosine', 'provider', 1, 1, 'active')`, contractID, contractKey, sequence, "test-model", dimensions).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO search_index_generations (search_index_generation_id, generation, embedding_contract_id, embedding_dimensions, ann_strategy, operator_class, indexed_expression, physical_index_name, exact_max_rows, allow_exact_fallback, activation_state, activated_at) VALUES (?::uuid, ?, ?::uuid, ?, ?, ?, ?, ?, 10000, false, 'active', now())`, generationID, sequence, contractID, dimensions, strategy, operatorClass, indexedExpression, indexName).Error
	}))
	return contractID
}

func seedTeamPredicateDefinitions(ctx context.Context, tx *gorm.DB, teamID string) error {
	return storagepostgres.SeedTeamPredicateDefinitions(ctx, tx, teamID)
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func correctRelationshipWithTestEmbeddings(ctx context.Context, semantic *knowledgepostgres.Store, input knowledgepostgres.CorrectRelationshipInput) (*knowledgepostgres.CorrectRelationshipResult, error) {
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	if err != nil {
		return nil, err
	}
	embeddings := make([]knowledgepostgres.RelationshipCorrectionEmbedding, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		embeddings = append(embeddings, knowledgepostgres.RelationshipCorrectionEmbedding{
			DocumentHash: document.DocumentHash, Embedding: make([]float32, plan.EmbeddingDimensions),
			EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions,
			EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID,
			IndexGeneration: plan.IndexGeneration,
		})
	}
	return semantic.CorrectRelationshipWithEmbeddings(ctx, input, embeddings)
}

func seedPrivateMemoryEvidenceConflict(t *testing.T, db *gorm.DB, rls storagepostgres.RLSHelper, teamID uuid.UUID, target *domain.Credential, ingestID uuid.UUID) {
	t.Helper()
	conflictID, firstPositionID, secondPositionID, fragmentID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		var generation int64
		if err := tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?`, target.MemorySpaceID).Row().Scan(&generation); err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE knowledge_ingests SET status = 'completed' WHERE team_id = ? AND ingest_id = ?`, teamID, ingestID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
				content, content_hash, source_type, authority, labels, metadata,
				space_id, space_generation
			) VALUES (?, ?, ?, ?, 0, 'private conflict citation', ?, 'manual', 'primary', ARRAY[]::text[], '{}'::jsonb, ?, ?)
		`, teamID, fragmentID, ingestID, target.ID, sha256Hex("private conflict citation"), target.MemorySpaceID, generation).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO evidence_conflict_cases (team_id, conflict_id, space_id, space_generation, case_key, status, version)
			VALUES (?, ?, ?, ?, ?, 'open', 1)
		`, teamID, conflictID, target.MemorySpaceID, generation, "private-erasure-conflict").Error; err != nil {
			return err
		}
		for _, position := range []struct {
			id        uuid.UUID
			key       string
			submitted bool
		}{{firstPositionID, "private-erasure-position-a", true}, {secondPositionID, "private-erasure-position-b", false}} {
			if err := tx.Exec(`
				INSERT INTO evidence_conflict_positions (
					team_id, conflict_id, space_id, space_generation, position_id, position_key,
					canonical_evidence_id, canonical_owner_profile_id, occurrence_id,
					occurrence_owner_profile_id, quote, span_start, span_end, authority, submitted
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'private', 0, 7, 'primary', ?)
			`, teamID, conflictID, target.MemorySpaceID, generation, position.id, position.key, fragmentID, target.ID, fragmentID, target.ID, position.submitted).Error; err != nil {
				return err
			}
		}
		return tx.Exec(`
			INSERT INTO evidence_conflict_events (
				team_id, conflict_event_id, conflict_id, space_id, space_generation, ordinal,
				action, status_after, case_version, actor_kind, actor_id, citation_snapshot
			) VALUES (?, ?, ?, ?, ?, 1, 'opened', 'open', 1, 'profile', ?, '[]'::jsonb)
		`, teamID, uuid.New(), conflictID, target.MemorySpaceID, generation, target.ID).Error
	}))
}
