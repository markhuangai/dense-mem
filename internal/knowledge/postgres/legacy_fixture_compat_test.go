package postgres

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	graphcontract "github.com/markhuangai/dense-mem/internal/graph/contract"
	graphpostgres "github.com/markhuangai/dense-mem/internal/graph/postgres"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type SemanticGraphQuery = graphcontract.Query
type SearchReconciliationRun = knowledgecontract.SearchReconciliationRun
type SearchReconciliationSelectionInput = knowledgecontract.SearchReconciliationSelectionInput
type SearchReconciliationRunInput = knowledgecontract.SearchReconciliationRunInput
type FinishSearchReconciliationRunInput = knowledgecontract.FinishSearchReconciliationRunInput
type ApplySearchReconciliationInput = knowledgecontract.ApplySearchReconciliationInput
type SearchConvergenceInput = knowledgecontract.SearchConvergenceInput
type SearchDocumentDriftCount = knowledgecontract.SearchDocumentDriftCount

func (s *Store) SemanticGraph(ctx context.Context, input graphcontract.Query) (*graphcontract.Snapshot, error) {
	return graphpostgres.NewStore(s.db, s.rls).SemanticGraph(ctx, input)
}

func fromKnowledgeRelationshipRecord(input *RelationshipRecord) *RelationshipRecord {
	return input
}

func setRememberAttemptDiagnosticHoldStateTx(ctx context.Context, tx *gorm.DB, spaceID uuid.UUID, retained bool) error {
	return storagepostgres.SetRememberAttemptDiagnosticHoldStateTx(ctx, tx, spaceID, retained)
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

func upsertRecallEvidenceSearchDocumentForTest(
	t *testing.T,
	ctx context.Context,
	repo *searchFixtureStore,
	teamID string,
	ownerID string,
	evidence EvidenceFragment,
) {
	t.Helper()
	doc, err := repo.UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "evidence",
		SourceID: evidence.FragmentID, SourceVersion: 1, DocumentText: evidence.Content,
	})
	require.NoError(t, err)
	require.Equal(t, evidence.FragmentID, doc.SourceID)
	var alias bool
	require.NoError(t, repo.projection.withSystemTx(ctx, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT EXISTS (
				SELECT 1 FROM evidence_exact_aliases
				WHERE team_id = ?::uuid AND alias_fragment_id = ?::uuid
			)
		`, teamID, evidence.FragmentID).Row().Scan(&alias)
	}))
	if alias {
		require.Equal(t, "not_required", doc.SearchState)
	} else {
		require.Equal(t, "pending", doc.SearchState)
	}
}

func (r *Store) ListSemanticReviewEntityCandidates(ctx context.Context, input SemanticReviewEntityCandidateInput) ([]SemanticReviewEntityCandidate, error) {
	result, err := r.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, Entities: []SubmissionAssessmentEntityCatalogTarget{{
			Ref: "legacy-review", Surface: input.Name, EntityKind: input.EntityKind, KnownEntityID: input.KnownEntityID,
		}}, CandidateLimit: input.Limit,
	})
	if err != nil {
		return nil, err
	}
	if len(result.Groups) == 0 {
		return []SemanticReviewEntityCandidate{}, nil
	}
	return result.Groups[0].Candidates, nil
}

func (r *Store) ListSemanticAssessmentKnownEntities(ctx context.Context, input SemanticAssessmentKnownEntityInput) ([]SemanticReviewEntityCandidate, error) {
	if len(input.EntityIDs) == 0 {
		return []SemanticReviewEntityCandidate{}, nil
	}
	targets := make([]SubmissionAssessmentEntityCatalogTarget, 0, len(input.EntityIDs))
	for index, entityID := range input.EntityIDs {
		targets = append(targets, SubmissionAssessmentEntityCatalogTarget{Ref: "known-" + strings.TrimSpace(entityID) + "-" + string(rune(index)), KnownEntityID: entityID})
	}
	result, err := r.ListSubmissionAssessmentEntityCatalog(ctx, SubmissionAssessmentEntityCatalogInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, Entities: targets, CandidateLimit: 1,
	})
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	known := make([]SemanticReviewEntityCandidate, 0, len(result.Groups))
	for _, group := range result.Groups {
		for _, candidate := range group.Candidates {
			if _, exists := seen[candidate.EntityID]; exists {
				continue
			}
			seen[candidate.EntityID] = struct{}{}
			known = append(known, candidate)
		}
	}
	return known, nil
}

func (r *Store) ListSemanticAssessmentEntityMatches(ctx context.Context, input SemanticAssessmentEntityMatchInput) (SemanticAssessmentEntityMatchResult, error) {
	words := strings.Fields(input.EvidenceText)
	seen := make(map[string]struct{})
	result := SemanticAssessmentEntityMatchResult{Matches: []SemanticAssessmentEntityMatch{}}
	for _, word := range words {
		word = strings.Trim(word, " \t\r\n.,;:!?()[]{}\"")
		if word == "" {
			continue
		}
		candidates, err := r.ListSemanticReviewEntityCandidates(ctx, SemanticReviewEntityCandidateInput{
			TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, Name: word, Limit: input.Limit,
		})
		if err != nil {
			return SemanticAssessmentEntityMatchResult{}, err
		}
		for _, candidate := range candidates {
			matched := ""
			if strings.EqualFold(candidate.CanonicalName, word) {
				matched = candidate.CanonicalName
			} else {
				for _, activeName := range candidate.ActiveNames {
					if strings.EqualFold(activeName, word) {
						matched = activeName
						break
					}
				}
			}
			if matched == "" {
				continue
			}
			if _, exists := seen[candidate.EntityID]; exists {
				continue
			}
			seen[candidate.EntityID] = struct{}{}
			result.Matches = append(result.Matches, SemanticAssessmentEntityMatch{Candidate: candidate, MatchedName: matched})
			if input.Limit > 0 && len(result.Matches) >= input.Limit {
				result.Truncated = len(words) > len(result.Matches)
				return result, nil
			}
		}
	}
	return result, nil
}

func (r *Store) ListSemanticReviewPredicateCandidates(ctx context.Context, input SemanticReviewPredicateCandidateInput) ([]SemanticReviewPredicateCandidate, error) {
	var result []SemanticReviewPredicateCandidate
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		rows, err := tx.WithContext(ctx).Raw(`
			WITH latest AS (
				SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
				       relationship_kind, current_cardinality, lifecycle_state,
				       row_number() OVER (PARTITION BY predicate_key ORDER BY version DESC) AS version_rank
				FROM team_predicate_definitions WHERE team_id = ?::uuid
			)
			SELECT predicate_key, version, allowed_subject_kinds, allowed_object_kinds,
			       relationship_kind, current_cardinality, lifecycle_state
			FROM latest
			WHERE version_rank = 1 AND lifecycle_state = 'active'
			  AND (predicate_key = ? OR ? = ANY((SELECT aliases FROM team_predicate_definitions WHERE team_id = ?::uuid AND predicate_key = latest.predicate_key AND version = latest.version)))
			ORDER BY CASE WHEN predicate_key = ? THEN 0 ELSE 1 END, version DESC
			LIMIT ?
		`, input.TeamID, input.Predicate, input.Predicate, input.TeamID, input.Predicate, input.Limit).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var candidate SemanticReviewPredicateCandidate
			var subjectKinds, objectKinds []string
			if err := rows.Scan(&candidate.PredicateKey, &candidate.Version, &subjectKinds, &objectKinds, &candidate.RelationshipKind, &candidate.CurrentCardinality, &candidate.LifecycleState); err != nil {
				return err
			}
			candidate.AllowedSubjectKinds = subjectKinds
			candidate.AllowedObjectKinds = objectKinds
			result = append(result, candidate)
		}
		return rows.Err()
	})
	return result, err
}

func (r *Store) ListSemanticReviewPredicateOptions(ctx context.Context, input SemanticReviewPredicateOptionsInput) ([]string, error) {
	candidates, err := r.ListSemanticAssessmentPredicateOptions(ctx, SemanticAssessmentPredicateOptionsInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID, QueryText: input.QueryText, Limit: input.Limit,
	})
	if err != nil {
		return nil, err
	}
	options := make([]string, 0, len(candidates)*2)
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		for _, value := range append([]string{candidate.PredicateKey}, candidate.Aliases...) {
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			options = append(options, value)
			if input.Limit > 0 && len(options) >= input.Limit {
				return options, nil
			}
		}
	}
	return options, nil
}
