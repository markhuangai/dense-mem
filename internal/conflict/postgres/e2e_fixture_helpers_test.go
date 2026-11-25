//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"

	"encoding/hex"
	"encoding/json"

	"fmt"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	privacypostgres "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	recallpostgres "github.com/markhuangai/dense-mem/internal/recall/postgres"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type resolvedEvidenceConflictCitation struct {
	CanonicalEvidenceID string
	ContentHash         string
}

func evidenceConflictPositionKey(citation resolvedEvidenceConflictCitation, start, end int) string {
	return sha256LengthDelimited(citation.CanonicalEvidenceID, citation.ContentHash, fmt.Sprintf("%d", start), fmt.Sprintf("%d", end))
}

func evidenceConflictCaseKey(teamID, spaceID string, generation int64, positionKeys []string) string {
	keys := append([]string(nil), positionKeys...)
	sort.Strings(keys)
	parts := append([]string{teamID, spaceID, fmt.Sprintf("%d", generation)}, keys...)
	return sha256LengthDelimited(parts...)
}

func sha256LengthDelimited(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		var length [8]byte
		for i := range length {
			length[7-i] = byte(len(part) >> (8 * i))
		}
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

type conflictStore = Store

type conflictKnowledgeFixtureStore struct {
	*knowledgepostgres.Store
	*conflictStore
}

func newConflictKnowledgeFixtureStore(db *gorm.DB, rls storagepostgres.RLSHelper) *conflictKnowledgeFixtureStore {
	knowledge := knowledgepostgres.NewStore(db, rls)
	return &conflictKnowledgeFixtureStore{
		Store:         knowledge,
		conflictStore: NewStore(db, rls, knowledge),
	}
}

func (s *conflictKnowledgeFixtureStore) ListEvidenceConflicts(ctx context.Context, input EvidenceConflictListInput) (*EvidenceConflictListResult, error) {
	return s.conflictStore.ListEvidenceConflicts(ctx, input)
}

func (s *conflictKnowledgeFixtureStore) GetEvidenceConflict(ctx context.Context, input EvidenceConflictGetInput) (*EvidenceConflictGetResult, error) {
	return s.conflictStore.GetEvidenceConflict(ctx, input)
}

func (s *conflictKnowledgeFixtureStore) ResolveEvidenceConflict(ctx context.Context, input EvidenceConflictResolutionInput) (*EvidenceConflictCaseRecord, error) {
	return s.conflictStore.ResolveEvidenceConflict(ctx, input)
}

func duplicateTeamSharedSpace(t *testing.T, db *gorm.DB, rls storagepostgres.RLSHelper, teamID string) (string, int64) {
	t.Helper()
	var spaceID string
	var generation int64
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT id::text, generation FROM memory_spaces WHERE team_id = ?::uuid AND kind = 'team_shared' LIMIT 1`, teamID).Row().Scan(&spaceID, &generation)
	}))
	return spaceID, generation
}

func createLedgerSSOIdentity(t *testing.T, db *gorm.DB, rls storagepostgres.RLSHelper, teamID uuid.UUID) uuid.UUID {
	t.Helper()
	providerID, identityID, membershipID := uuid.New(), uuid.New(), uuid.New()
	now := time.Now().UTC()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO sso_providers (id, name, kind, issuer_url, client_id) VALUES (?, ?, 'generic_oidc', 'https://issuer.example.test', ?)`, providerID, "provider-"+providerID.String(), "client-"+providerID.String()).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO sso_identities (id, provider_id, subject, email, display_name) VALUES (?, ?, ?, ?, ?)`, identityID, providerID, "subject-"+identityID.String(), "user-"+identityID.String()+"@example.test", "SSO test user").Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO actor_identities (id, kind, team_id, provider, subject, display_name, active, created_at, updated_at) VALUES (?, 'human', NULL, ?, ?, 'SSO test user', true, ?, ?)`, identityID, providerID.String(), "subject-"+identityID.String(), now, now).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO team_memberships (id, actor_identity_id, team_id, status, team_admin, maximum_grants, sso_provider_id, sso_group_id, sso_entitlement_status, created_at, updated_at) VALUES (?, ?, ?, 'active', false, ARRAY['read','write']::text[], ?, 'test-group', 'active', ?, ?)`, membershipID, identityID, teamID, providerID, now, now).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO sso_group_mappings(provider_id,team_id,group_id,scopes) VALUES(?,?,'test-group',ARRAY['read','write'])`, providerID, teamID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`INSERT INTO sso_entitlement_cache(provider_id,subject,groups,status,checked_at,expires_at) VALUES(?,?,ARRAY['test-group'],'active',now(),now()+interval '1 hour')`, providerID, "subject-"+identityID.String()).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO membership_grants (membership_id, grant_name, source) VALUES (?, 'read', 'explicit'), (?, 'write', 'explicit')`, membershipID, membershipID).Error
	}))
	return identityID
}

func createOwnedCredential(t *testing.T, repo *accesspostgres.CredentialRepositoryImpl, teamID, ownerID uuid.UUID, name string) *domain.Credential {
	t.Helper()
	id := uuid.New()
	prefix := "dm_" + strings.ReplaceAll(id.String(), "-", "")[:20]
	credential := &domain.Credential{ID: id, TeamID: teamID, Name: name, KeyHash: "hash-" + id.String(), KeyPrefix: prefix, KeySuffix: "suffix", Scopes: []string{"read", "write"}, RateLimit: 60, OwnerIdentityID: &ownerID}
	require.NoError(t, repo.CreateCredential(context.Background(), credential))
	return credential
}

func privateMemoryHash(parts ...string) string {
	return privacypostgres.Hash(parts...)
}

var conflictSearchTestContractSequence atomic.Int32

func insertSearchTestContract(t testing.TB, db *gorm.DB, rls *storagepostgres.RLS, prefix string, dimensions int, strategy string, indexName string) string {
	t.Helper()
	sequence := int(conflictSearchTestContractSequence.Add(1))
	contractID, generationID := uuid.NewString(), uuid.NewString()
	contractKey := fmt.Sprintf("%s-%s", prefix, strings.ReplaceAll(uuid.NewString(), "-", "")[:8])
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO embedding_contracts (embedding_contract_id, contract_key, version, provider, model, dimensions, distance_metric, vector_normalization, document_format_version, query_format_version, lifecycle_state) VALUES (?::uuid, ?, ?, 'test', 'test-model', ?, 'cosine', 'provider', 1, 1, 'active')`, contractID, contractKey, sequence, dimensions).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO search_index_generations (search_index_generation_id, generation, embedding_contract_id, embedding_dimensions, ann_strategy, operator_class, indexed_expression, physical_index_name, exact_max_rows, allow_exact_fallback, activation_state, activated_at) VALUES (?::uuid, ?, ?::uuid, ?, ?, '', '', ?, 10000, false, 'active', now())`, generationID, sequence, contractID, dimensions, strategy, indexName).Error
	}))
	return contractID
}

func evidenceOnlyRememberInput(teamID, ownerID, label string) SynchronousRememberCommitInput {
	fragmentID, ingestID, assessmentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	content := "Dense-Mem stores this evidence without a semantic Relationship. [" + label + "]"
	return SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID, IdempotencyKey: label, RequestHash: sha256Hex(label), SourceSummary: label,
		Evidence:     []EvidenceInput{{FragmentID: fragmentID, Content: content, ContentHash: sha256Hex(content), SourceType: "manual", Authority: "primary"}},
		AssessmentID: assessmentID, AssessmentJSON: json.RawMessage(`{"request_id":"` + label + `"}`), ProviderTurns: 1,
		EvidenceSecurityResults: []EvidenceSecurityResult{{FragmentID: fragmentID, EvidenceID: "evidence:0", EvidenceIndex: 0, Decision: "pass", Safe: true}},
		Commit:                  CommitSubmissionAssessmentInput{AssessmentID: assessmentID, Items: []SubmissionAssessmentItemInput{{FragmentID: fragmentID}}, Payload: map[string]any{"response_hash": sha256Hex(label), "model": "test-model", "tokenizer": "o200k_base", "candidate_context_tokens": 0, "candidate_context_truncated": false}},
	}
}

func duplicateRememberInput(teamID, ownerID, label, content string, forceInsert bool) SynchronousRememberCommitInput {
	input := evidenceOnlyRememberInput(teamID, ownerID, label)
	input.Evidence[0].Content = content
	input.Evidence[0].ContentHash = sha256Hex(content)
	input.Evidence[0].ForceInsert = forceInsert
	input.Commit.Items[0].FragmentID = input.Evidence[0].FragmentID
	input.Commit.Payload["response_hash"] = sha256Hex(label + "\x00" + content)
	return input
}

func duplicateCandidateInput(input SynchronousRememberCommitInput) RememberDuplicateCandidateInput {
	return RememberDuplicateCandidateInput{TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration, Evidence: append([]EvidenceInput(nil), input.Evidence...)}
}

func duplicatePlanEmbeddings(plan *RememberDuplicateEmbeddingPlan) []InlineEmbeddingResult {
	results := make([]InlineEmbeddingResult, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		vector := make([]float32, plan.EmbeddingDimensions)
		if len(vector) > 0 {
			vector[0] = 1
		}
		results = append(results, InlineEmbeddingResult{DocumentHash: document.DocumentHash, Embedding: vector, EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions, EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration})
	}
	return results
}

func duplicatePlanToInline(plan *InlineEmbeddingPlan) []InlineEmbeddingResult {
	results := make([]InlineEmbeddingResult, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		vector := make([]float32, plan.EmbeddingDimensions)
		if len(vector) > 0 {
			vector[0] = 1
		}
		results = append(results, InlineEmbeddingResult{DocumentHash: document.DocumentHash, Embedding: vector, EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions, EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration})
	}
	return results
}

func commitDuplicateFixture(t *testing.T, ctx context.Context, repo rememberEmbeddingCommitter, input SynchronousRememberCommitInput) *SynchronousRememberCommitResult {
	t.Helper()
	plan, err := repo.PlanRememberEmbeddings(ctx, input)
	require.NoError(t, err)
	result, err := repo.CommitRememberWithEmbeddings(ctx, input, duplicatePlanToInline(plan))
	require.NoError(t, err)
	return result
}

func duplicateCount(t *testing.T, db *gorm.DB, rls storagepostgres.RLSHelper, query string, args ...any) int64 {
	t.Helper()
	var count int64
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error { return tx.Raw(query, args...).Row().Scan(&count) }))
	return count
}

func loadRecallEvidenceConflictCase(ctx context.Context, tx *gorm.DB, teamID, conflictID string, knownAt *time.Time) (*recallpostgres.EvidenceConflictCaseRecord, error) {
	return recallpostgres.LoadRecallEvidenceConflictCase(ctx, tx, teamID, conflictID, knownAt)
}
