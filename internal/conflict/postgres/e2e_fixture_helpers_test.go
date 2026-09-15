package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	knowledge := knowledgepostgres.NewStore(db, rls, knowledgepostgres.ConflictRuntimeConfig{})
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

func seedTeamPredicateDefinitions(ctx context.Context, tx *gorm.DB, teamID string) error {
	return storagepostgres.SeedTeamPredicateDefinitions(ctx, tx, teamID)
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
		return tx.Exec(`INSERT INTO membership_grants (membership_id, grant_name, source) VALUES (?, 'read', 'explicit'), (?, 'write', 'explicit')`, membershipID, membershipID).Error
	}))
	return identityID
}

func createOwnedCredential(t *testing.T, repo *accesspostgres.CredentialRepositoryImpl, teamID, ownerID uuid.UUID, name string, binding domain.CredentialMemoryBinding) *domain.Credential {
	t.Helper()
	id := uuid.New()
	prefix := "dm_" + strings.ReplaceAll(id.String(), "-", "")[:20]
	credential := &domain.Credential{ID: id, TeamID: teamID, Name: name, KeyHash: "hash-" + id.String(), KeyPrefix: prefix, KeySuffix: "suffix", Scopes: []string{"read", "write"}, RateLimit: 60, OwnerIdentityID: &ownerID, MemoryBinding: binding}
	require.NoError(t, repo.CreateCredential(context.Background(), credential))
	return credential
}

func privateMemoryHash(parts ...string) string {
	return privacypostgres.Hash(parts...)
}

func ensureConflictSystemProfile(ctx context.Context, tx *gorm.DB, teamID string) (string, error) {
	var profileID string
	err := tx.WithContext(ctx).Raw(`SELECT id::text FROM actor_identities WHERE team_id = ?::uuid AND kind = 'system' LIMIT 1`, teamID).Row().Scan(&profileID)
	if err == nil {
		return profileID, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	profileID = uuid.NewString()
	if err := tx.WithContext(ctx).Exec(`INSERT INTO actor_identities (id, kind, team_id, display_name, active, created_at, updated_at) VALUES (?::uuid, 'system', ?::uuid, ?, false, now(), now())`, profileID, teamID, "__dense_mem_conflict_system__:"+profileID).Error; err != nil {
		return "", err
	}
	if err := tx.WithContext(ctx).Exec(`INSERT INTO team_memberships (actor_identity_id, team_id, status, team_admin, maximum_grants) VALUES (?::uuid, ?::uuid, 'revoked', false, ARRAY[]::text[])`, profileID, teamID).Error; err != nil {
		return "", err
	}
	if err := tx.WithContext(ctx).Exec(`INSERT INTO ownership_aliases (team_id, legacy_owner_id, canonical_identity_id, credential_id, reason) VALUES (?::uuid, ?::uuid, ?::uuid, NULL, 'system')`, teamID, profileID, profileID).Error; err != nil {
		return "", err
	}
	return profileID, nil
}

func enqueueConflictDerivedEvidenceTasks(ctx context.Context, tx *gorm.DB, resolutionPlanID string, targets []ConflictDerivedEvidenceTarget) ([]ConflictDerivedEvidenceTarget, error) {
	result := make([]ConflictDerivedEvidenceTarget, 0, len(targets))
	for _, target := range targets {
		var taskID, spaceID string
		var spaceGeneration int64
		err := tx.WithContext(ctx).Raw(`
			INSERT INTO relationship_conflict_derived_evidence_tasks (
				team_id, space_id, space_generation, resolution_plan_id, conflict_id,
				target_fragment_id, target_owner_profile_id, selected_position_id,
				system_profile_id, source_group_key, origin_evidence_index
			)
			SELECT ?::uuid, conflict.space_id, conflict.space_generation, ?::uuid, ?::uuid,
				   ?::uuid, ?::uuid, ?::uuid, ?::uuid, ?, ?
			FROM relationship_conflict_cases AS conflict
			WHERE conflict.team_id = ?::uuid AND conflict.conflict_id = ?::uuid
			ON CONFLICT (team_id, conflict_id, target_fragment_id) DO NOTHING
			RETURNING derived_evidence_task_id::text, space_id::text, space_generation
		`, target.TeamID, resolutionPlanID, target.ConflictID, target.TargetFragmentID, target.TargetOwnerProfileID, target.SelectedPositionID, target.SystemProfileID, target.SourceGroupKey, target.EvidenceIndex, target.TeamID, target.ConflictID).Row().Scan(&taskID, &spaceID, &spaceGeneration)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if taskID == "" {
			if err := tx.WithContext(ctx).Raw(`SELECT derived_evidence_task_id::text, space_id::text, space_generation FROM relationship_conflict_derived_evidence_tasks WHERE team_id = ?::uuid AND conflict_id = ?::uuid AND target_fragment_id = ?::uuid`, target.TeamID, target.ConflictID, target.TargetFragmentID).Row().Scan(&taskID, &spaceID, &spaceGeneration); err != nil {
				return nil, err
			}
		}
		target.TaskID, target.SpaceID, target.SpaceGeneration = taskID, spaceID, spaceGeneration
		result = append(result, target)
	}
	return result, nil
}

var conflictSearchTestContractSequence atomic.Int32

func insertSearchTestContract(t *testing.T, db *gorm.DB, rls *storagepostgres.RLS, prefix string, dimensions int, strategy string, indexName string) string {
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

func commitConflictRememberFixture(t *testing.T, ctx context.Context, repo *knowledgepostgres.Store, teamID, ownerID, subjectID, objectID, content, key string) *SynchronousRememberCommitResult {
	return commitConflictRememberFixtureWithSupports(t, ctx, repo, teamID, ownerID, subjectID, objectID, content, key, nil)
}

func commitConflictRememberFixtureWithSupports(t *testing.T, ctx context.Context, repo *knowledgepostgres.Store, teamID, ownerID, subjectID, objectID, content, key string, supports []EvidenceSupportInput) *SynchronousRememberCommitResult {
	t.Helper()
	input := conflictRememberFixtureInput(teamID, ownerID, subjectID, objectID, content, key, supports)
	if len(supports) > 0 {
		knownIDs := make([]string, 0, len(supports))
		for _, support := range supports {
			if support.EvidenceOwnerProfileID != "" {
				knownIDs = append(knownIDs, support.FragmentID)
			}
		}
		if len(knownIDs) > 0 {
			known, err := repo.ListSubmissionAssessmentKnownEvidence(ctx, SubmissionAssessmentKnownEvidenceInput{TeamID: teamID, OwnerProfileID: ownerID, EvidenceIDs: knownIDs})
			require.NoError(t, err)
			input.Commit.KnownEvidenceSnapshot = known.Evidence
		}
	}
	plan, err := repo.PlanRememberEmbeddings(ctx, input)
	require.NoError(t, err)
	vectors := make([]InlineEmbeddingResult, 0, len(plan.Documents))
	for index, document := range plan.Documents {
		vector := []float32{1, 0, 0}
		if index%2 == 1 {
			vector = []float32{0, 1, 0}
		}
		vectors = append(vectors, InlineEmbeddingResult{DocumentHash: document.DocumentHash, Embedding: vector, EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions, EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID, IndexGeneration: plan.IndexGeneration})
	}
	result, err := repo.CommitRememberWithEmbeddings(ctx, input, vectors)
	require.NoError(t, err)
	return result
}

func conflictRememberFixtureInput(teamID, ownerID, subjectID, objectID, content, key string, supports []EvidenceSupportInput) SynchronousRememberCommitInput {
	fragmentID, ingestID, assessmentID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	relationshipRef := key + ":relationship"
	return SynchronousRememberCommitInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID, IdempotencyKey: key, RequestHash: sha256Hex(content), SourceSummary: key,
		Evidence:                []EvidenceInput{{FragmentID: fragmentID, Content: content, ContentHash: sha256Hex(content), SourceType: "conversation", Authority: "primary"}},
		EvidenceSecurityResults: []EvidenceSecurityResult{{FragmentID: fragmentID, EvidenceIndex: 0, Decision: "pass", Safe: true}},
		AssessmentID:            assessmentID, AssessmentJSON: json.RawMessage(`{"request_id":"` + key + `"}`), ProviderTurns: 1,
		Commit: CommitSubmissionAssessmentInput{
			AssessmentID: assessmentID, Items: []SubmissionAssessmentItemInput{{FragmentID: fragmentID}},
			EntityResolutions: []SubmissionAssessmentEntityResolutionInput{
				{Resolution: SemanticEntityResolutionInput{MentionRef: "subject", Action: string(domain.EntityResolutionReuse), EntityID: subjectID, ExactEntityID: subjectID, FragmentID: fragmentID, AssessmentID: assessmentID}},
				{Resolution: SemanticEntityResolutionInput{MentionRef: "object", Action: string(domain.EntityResolutionReuse), EntityID: objectID, ExactEntityID: objectID, FragmentID: fragmentID, AssessmentID: assessmentID}},
			},
			RelationshipObservations: []SubmissionAssessmentRelationshipObservationInput{{RelationshipRef: relationshipRef, Observation: SemanticRelationshipDecisionInput{Ref: relationshipRef, SubjectRef: "subject", OriginalPredicate: "primary_database", PredicateKey: "primary_database", PredicateVersion: 1, ObjectRef: "object", Polarity: "+", AssessorAccepted: true, AssessmentID: assessmentID, Support: &EvidenceSupportInput{FragmentID: fragmentID, SourceGroupKey: key, SpanStart: 0, SpanEnd: len(content), Quote: content, Authority: "primary"}, Supports: supports}}},
			RelationshipResults:      []SubmissionRelationshipResultInput{{RelationshipRef: relationshipRef, Disposition: "stored"}},
			Payload:                  map[string]any{"response_hash": sha256Hex(key), "model": "test-model", "tokenizer": "o200k_base", "candidate_context_tokens": 0, "candidate_context_truncated": false},
		},
	}
}
