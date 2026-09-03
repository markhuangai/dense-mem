package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

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
	return RememberDuplicateCandidateInput{
		TeamID: input.TeamID, OwnerProfileID: input.OwnerProfileID,
		SpaceID: input.SpaceID, SpaceGeneration: input.SpaceGeneration,
		Evidence: append([]EvidenceInput(nil), input.Evidence...),
	}
}

func duplicatePlanEmbeddings(plan *RememberDuplicateEmbeddingPlan) []InlineEmbeddingResult {
	results := make([]InlineEmbeddingResult, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		vector := make([]float32, plan.EmbeddingDimensions)
		if len(vector) > 0 {
			vector[0] = 1
		}
		results = append(results, InlineEmbeddingResult{
			DocumentHash: document.DocumentHash, Embedding: vector,
			EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions,
			EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID,
			IndexGeneration: plan.IndexGeneration,
		})
	}
	return results
}

func commitDuplicateFixture(t *testing.T, ctx context.Context, repo *LedgerRepositoryImpl, input SynchronousRememberCommitInput) *SynchronousRememberCommitResult {
	t.Helper()
	plan, err := repo.PlanRememberEmbeddings(ctx, input)
	require.NoError(t, err)
	result, err := repo.CommitRememberWithEmbeddings(ctx, input, duplicatePlanToInline(plan))
	require.NoError(t, err)
	return result
}

func duplicatePlanToInline(plan *InlineEmbeddingPlan) []InlineEmbeddingResult {
	results := make([]InlineEmbeddingResult, 0, len(plan.Documents))
	for _, document := range plan.Documents {
		vector := make([]float32, plan.EmbeddingDimensions)
		if len(vector) > 0 {
			vector[0] = 1
		}
		results = append(results, InlineEmbeddingResult{
			DocumentHash: document.DocumentHash, Embedding: vector,
			EmbeddingContractID: plan.EmbeddingContractID, EmbeddingDimensions: plan.EmbeddingDimensions,
			EmbeddingModel: plan.EmbeddingModel, SearchIndexGenerationID: plan.SearchIndexGenerationID,
			IndexGeneration: plan.IndexGeneration,
		})
	}
	return results
}

func duplicateTeamSharedSpace(t *testing.T, db *gorm.DB, rls rLSHelper, teamID string) (string, int64) {
	t.Helper()
	var spaceID string
	var generation int64
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT id::text, generation
			FROM memory_spaces
			WHERE team_id = ?::uuid AND kind = 'team_shared'
			LIMIT 1
		`, teamID).Row().Scan(&spaceID, &generation)
	}))
	return spaceID, generation
}

func duplicatePrivateSpace(t *testing.T, db *gorm.DB, rls rLSHelper, teamID, ownerID string) (string, int64) {
	t.Helper()
	spaceID := uuid.NewString()
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO memory_spaces (id, team_id, kind, owner_profile_id)
			VALUES (?::uuid, ?::uuid, 'profile_private', ?::uuid)
		`, spaceID, teamID, ownerID).Error
	}))
	var generation int64
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE team_id = ?::uuid AND id = ?::uuid`, teamID, spaceID).Row().Scan(&generation)
	}))
	return spaceID, generation
}

func duplicateCount(t *testing.T, db *gorm.DB, rls rLSHelper, query string, args ...any) int64 {
	t.Helper()
	var count int64
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(query, args...).Row().Scan(&count)
	}))
	return count
}

func TestRememberDuplicateExactReuseCreatesOneCanonicalAndTwoOccurrences(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-exact", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-exact-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-exact-owner")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)

	content := "The exact byte sequence is retained."
	first := duplicateRememberInput(teamID, ownerID, "duplicate-exact-first", content, false)
	first.SpaceID, first.SpaceGeneration = spaceID, generation
	commitDuplicateFixture(t, ctx, repo, first)
	canonicalID := first.Evidence[0].FragmentID

	second := duplicateRememberInput(teamID, ownerID, "duplicate-exact-second", content, true)
	second.SpaceID, second.SpaceGeneration = spaceID, generation
	duplicateInput := duplicateCandidateInput(second)
	plan, err := repo.PlanRememberDuplicateEmbeddings(ctx, duplicateInput)
	require.NoError(t, err)
	require.Empty(t, plan.Documents, "exact reuse must not request a provider vector")
	resolved, err := repo.ResolveRememberDuplicateCandidates(ctx, duplicateInput, nil)
	require.NoError(t, err)
	require.Len(t, resolved.Exact, 1)
	require.True(t, resolved.Exact[0].Exact)
	require.Equal(t, canonicalID, resolved.Exact[0].CandidateFragmentID)
	second.DuplicateResolutions = resolved.Exact
	result, err := repo.CommitRememberWithEmbeddings(ctx, second, nil)
	require.NoError(t, err)
	require.Equal(t, "completed", result.Outcome)
	evidenceResult, ok := result.PublicResult["evidence"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, evidenceResult, 1)
	require.Equal(t, "stored", evidenceResult[0]["disposition"])

	require.EqualValues(t, 1, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_fragments WHERE team_id = ?::uuid`, teamID))
	require.EqualValues(t, 2, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_occurrences WHERE team_id = ?::uuid`, teamID))
	var canonicalOwner, occurrenceOwner string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT canonical_owner_profile_id::text, owner_profile_id::text
			FROM evidence_occurrences
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, second.IngestID).Row().Scan(&canonicalOwner, &occurrenceOwner)
	}))
	require.Equal(t, ownerID, canonicalOwner)
	require.Equal(t, ownerID, occurrenceOwner)
	var sameTeamVisible int64
	require.NoError(t, rls.WithTeamProfileReadOnlyRepeatableTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM evidence_occurrences WHERE team_id = ?::uuid`, teamID).Row().Scan(&sameTeamVisible)
	}))
	require.EqualValues(t, 2, sameTeamVisible)
	otherTeam := createLedgerTeam(t, adminDB, rls, "duplicate-exact-other-team")
	otherOwner := createLedgerProfile(t, adminDB, rls, otherTeam, "duplicate-exact-other-owner")
	var crossTeamVisible int64
	require.NoError(t, rls.WithTeamProfileReadOnlyRepeatableTx(ctx, appDB, otherTeam, otherOwner, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT count(*) FROM evidence_occurrences WHERE team_id = ?::uuid`, teamID).Row().Scan(&crossTeamVisible)
	}))
	require.Zero(t, crossTeamVisible, "occurrences must not cross team RLS")
}

func TestRememberDuplicateConcurrentSameContentConverges(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-concurrent", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-concurrent-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-concurrent-owner")
	content := "concurrent exact evidence"
	left := duplicateRememberInput(teamID, ownerID, "duplicate-concurrent-left", content, false)
	right := duplicateRememberInput(teamID, ownerID, "duplicate-concurrent-right", content, false)
	repo := NewLedgerRepository(appDB, rls)
	leftPlan, err := repo.PlanRememberEmbeddings(ctx, left)
	require.NoError(t, err)
	rightPlan, err := repo.PlanRememberEmbeddings(ctx, right)
	require.NoError(t, err)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, item := range []struct {
		input SynchronousRememberCommitInput
		plan  *InlineEmbeddingPlan
	}{{left, leftPlan}, {right, rightPlan}} {
		item := item
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, commitErr := repo.CommitRememberWithEmbeddings(ctx, item.input, duplicatePlanToInline(item.plan))
			results <- commitErr
		}()
	}
	wg.Wait()
	close(results)
	for err := range results {
		require.NoError(t, err)
	}
	fragmentCount := duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_fragments WHERE team_id = ?::uuid`, teamID)
	require.EqualValues(t, 1, fragmentCount)
	require.EqualValues(t, 2, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_occurrences WHERE team_id = ?::uuid`, teamID))
}

func TestRememberDuplicateExactMatchRequiresByteEqualityOnHashCollision(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-byte-collision-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-byte-collision-owner")
	collisionHash := "sha256:deliberate-collision"
	canonicalID, ingestID := uuid.NewString(), uuid.NewString()
	item := EvidenceInput{FragmentID: canonicalID, Content: "bytes A", ContentHash: collisionHash, SourceType: "manual", Authority: "primary"}
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		input := CreateIngestInput{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingestID,
			IdempotencyKey: "duplicate-byte-collision-canonical", RequestHash: sha256Hex("duplicate-byte-collision-canonical"),
			Status: "completed", Evidence: []EvidenceInput{item},
		}
		if _, _, err := insertKnowledgeIngest(ctx, tx, input); err != nil {
			return err
		}
		_, err := insertEvidenceFragment(ctx, tx, input, ingestID, 0, item, nil)
		return err
	}))
	var found string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		id, ok, err := resolveRememberExactEvidenceInTx(ctx, tx, RememberDuplicateCandidateInput{
			TeamID: teamID, OwnerProfileID: ownerID,
			Evidence: []EvidenceInput{{FragmentID: uuid.NewString(), Content: "bytes B", ContentHash: collisionHash}},
		}, EvidenceInput{Content: "bytes B", ContentHash: collisionHash})
		if err != nil {
			return err
		}
		if ok {
			found = id
		}
		return nil
	}))
	require.Empty(t, found, "matching hash with different bytes must not reuse evidence")
}

func TestRememberDuplicateForceAndLifecycleBearingEvidenceStayNew(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-controls", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-controls-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-controls-owner")
	repo := NewLedgerRepository(appDB, rls)
	candidate := duplicateRememberInput(teamID, ownerID, "duplicate-controls-candidate", "canonical semantic candidate", false)
	commitDuplicateFixture(t, ctx, repo, candidate)

	forced := duplicateRememberInput(teamID, ownerID, "duplicate-controls-forced", "semantically similar forced evidence", true)
	forcedPlan, err := repo.PlanRememberDuplicateEmbeddings(ctx, duplicateCandidateInput(forced))
	require.NoError(t, err)
	require.Empty(t, forcedPlan.Documents)
	forcedResolved, err := repo.ResolveRememberDuplicateCandidates(ctx, duplicateCandidateInput(forced), nil)
	require.NoError(t, err)
	require.Empty(t, forcedResolved.Candidates)
	require.Equal(t, "new", forcedResolved.Exact[0].Disposition)
	forced.DuplicateResolutions = forcedResolved.Exact
	commitDuplicateWithNormalPlan(t, ctx, repo, forced)

	lifecycle := duplicateRememberInput(teamID, ownerID, "duplicate-controls-source", "source-backed semantic candidate", false)
	lifecycle.Evidence[0].SourceKey = "doc://duplicate-controls"
	lifecycle.Evidence[0].SourceRevisionToken = "rev-1"
	lifecycle.Evidence[0].SourceRevisionContentHash = lifecycle.Evidence[0].ContentHash
	lifecyclePlan, err := repo.PlanRememberDuplicateEmbeddings(ctx, duplicateCandidateInput(lifecycle))
	require.NoError(t, err)
	require.Empty(t, lifecyclePlan.Documents, "source-bearing evidence must not be semantically pre-assessed")
	lifecycleResolved, err := repo.ResolveRememberDuplicateCandidates(ctx, duplicateCandidateInput(lifecycle), nil)
	require.NoError(t, err)
	require.Empty(t, lifecycleResolved.Candidates)
	require.Equal(t, "new", lifecycleResolved.Exact[0].Disposition)
	lifecycle.DuplicateResolutions = lifecycleResolved.Exact
	commitDuplicateWithNormalPlan(t, ctx, repo, lifecycle)

	require.EqualValues(t, 3, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_fragments WHERE team_id = ?::uuid`, teamID))
	require.EqualValues(t, 1, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_sources WHERE team_id = ?::uuid`, teamID))
}

func commitDuplicateWithNormalPlan(t *testing.T, ctx context.Context, repo *LedgerRepositoryImpl, input SynchronousRememberCommitInput) {
	t.Helper()
	plan, err := repo.PlanRememberEmbeddings(ctx, input)
	require.NoError(t, err)
	_, err = repo.CommitRememberWithEmbeddings(ctx, input, duplicatePlanToInline(plan))
	require.NoError(t, err)
}

func TestRememberDuplicateCandidateIsolationAndBound(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-isolation", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-isolation-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-isolation-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-isolation-b")
	otherTeam := createLedgerTeam(t, adminDB, rls, "duplicate-isolation-other-team")
	otherOwner := createLedgerProfile(t, adminDB, rls, otherTeam, "duplicate-isolation-other-owner")
	sharedSpaceID, sharedGeneration := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)

	shared := duplicateRememberInput(teamID, ownerB, "duplicate-isolation-shared", "shared semantic candidate", false)
	shared.SpaceID, shared.SpaceGeneration = sharedSpaceID, sharedGeneration
	commitDuplicateFixture(t, ctx, repo, shared)
	privateSpaceID, privateGeneration := duplicatePrivateSpace(t, adminDB, rls, teamID, ownerB)
	private := duplicateRememberInput(teamID, ownerB, "duplicate-isolation-private", "private semantic candidate", false)
	private.SpaceID, private.SpaceGeneration = privateSpaceID, privateGeneration
	commitDuplicateFixture(t, ctx, repo, private)
	foreign := duplicateRememberInput(otherTeam, otherOwner, "duplicate-isolation-foreign", "foreign semantic candidate", false)
	commitDuplicateFixture(t, ctx, repo, foreign)

	for index := 0; index < 11; index++ {
		candidate := duplicateRememberInput(teamID, ownerB, fmt.Sprintf("duplicate-isolation-%02d", index), fmt.Sprintf("bounded semantic candidate %02d", index), false)
		candidate.SpaceID, candidate.SpaceGeneration = sharedSpaceID, sharedGeneration
		commitDuplicateFixture(t, ctx, repo, candidate)
	}
	query := duplicateRememberInput(teamID, ownerA, "duplicate-isolation-query", "query semantically near candidates", false)
	query.SpaceID, query.SpaceGeneration = sharedSpaceID, sharedGeneration
	resolved, err := resolveDuplicateFixture(t, ctx, repo, query)
	require.NoError(t, err)
	require.Len(t, resolved.Candidates, 1)
	require.Len(t, resolved.Candidates[0].Candidates, RememberDuplicateCandidateLimit)
	for _, candidate := range resolved.Candidates[0].Candidates {
		require.Equal(t, teamID, candidateOwnerTeam(t, adminDB, rls, candidate.FragmentID))
		require.NotEqual(t, private.Evidence[0].FragmentID, candidate.FragmentID)
		require.NotEqual(t, foreign.Evidence[0].FragmentID, candidate.FragmentID)
	}

	exactCrossOwner := duplicateRememberInput(teamID, ownerA, "duplicate-isolation-exact-cross-owner", shared.Evidence[0].Content, false)
	exactCrossOwner.SpaceID, exactCrossOwner.SpaceGeneration = sharedSpaceID, sharedGeneration
	exactResolved, err := resolveDuplicateFixture(t, ctx, repo, exactCrossOwner)
	require.NoError(t, err)
	require.False(t, exactResolved.Exact[0].Exact, "exact matching must not cross owners")
	for _, candidate := range exactResolved.Candidates[0].Candidates {
		require.NotEqual(t, private.Evidence[0].FragmentID, candidate.FragmentID)
	}
}

func resolveDuplicateFixture(t *testing.T, ctx context.Context, repo *LedgerRepositoryImpl, input SynchronousRememberCommitInput) (*RememberDuplicateResolutionResult, error) {
	t.Helper()
	duplicateInput := duplicateCandidateInput(input)
	plan, err := repo.PlanRememberDuplicateEmbeddings(ctx, duplicateInput)
	if err != nil {
		return nil, err
	}
	resolved, err := repo.ResolveRememberDuplicateCandidates(ctx, duplicateInput, duplicatePlanEmbeddings(plan))
	if err != nil {
		return nil, err
	}
	return resolved, nil
}

func candidateOwnerTeam(t *testing.T, db *gorm.DB, rls rLSHelper, fragmentID string) string {
	t.Helper()
	var teamID string
	require.NoError(t, rls.WithSystemTx(context.Background(), db, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT team_id::text FROM evidence_fragments WHERE fragment_id = ?::uuid`, fragmentID).Row().Scan(&teamID)
	}))
	return teamID
}

func TestRememberDuplicateCrossOwnerReusePreservesOccurrenceLineage(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-lineage", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-lineage-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-lineage-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-lineage-b")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)
	candidate := duplicateRememberInput(teamID, ownerB, "duplicate-lineage-candidate", "team shared canonical", false)
	candidate.SpaceID, candidate.SpaceGeneration = spaceID, generation
	commitDuplicateFixture(t, ctx, repo, candidate)

	submitted := duplicateRememberInput(teamID, ownerA, "duplicate-lineage-submitted", "team shared semantic paraphrase", false)
	submitted.SpaceID, submitted.SpaceGeneration = spaceID, generation
	submitted.Evidence[0].InitialEvent = &SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass"}
	resolved, err := resolveDuplicateFixture(t, ctx, repo, submitted)
	require.NoError(t, err)
	require.Len(t, resolved.Candidates, 1)
	resolved.Exact[0].Disposition = "reuse"
	resolved.Exact[0].CandidateFragmentID = candidate.Evidence[0].FragmentID
	resolved.Exact[0].CandidateOwnerID = ownerB
	submitted.DuplicateResolutions = resolved.Exact
	result, err := repo.CommitRememberWithEmbeddings(ctx, submitted, duplicatePlanEmbeddingsForInput(t, ctx, repo, submitted))
	require.NoError(t, err)
	require.Equal(t, "completed", result.Outcome)

	var canonicalOwner, occurrenceOwner, eventOccurrence, eventEvidenceOwner string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT canonical_owner_profile_id::text, owner_profile_id::text
			FROM evidence_occurrences
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, submitted.IngestID).Row().Scan(&canonicalOwner, &occurrenceOwner); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT occurrence_id::text, evidence_owner_profile_id::text
			FROM evidence_security_events
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, submitted.IngestID).Row().Scan(&eventOccurrence, &eventEvidenceOwner)
	}))
	require.Equal(t, ownerB, canonicalOwner)
	require.Equal(t, ownerA, occurrenceOwner)
	require.NotEmpty(t, eventOccurrence)
	require.Equal(t, ownerB, eventEvidenceOwner)
}

func TestRememberDuplicateRepeatedCanonicalKeepsRelationshipSupportOccurrences(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-support-lineage", 3, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-support-lineage-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-support-lineage-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-support-lineage-b")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)
	candidate := duplicateRememberInput(teamID, ownerB, "duplicate-support-lineage-candidate", "shared canonical support", false)
	candidate.SpaceID, candidate.SpaceGeneration = spaceID, generation
	commitDuplicateFixture(t, ctx, repo, candidate)

	assessmentID := uuid.NewString()
	first := duplicateRememberInput(teamID, ownerA, "duplicate-support-lineage-submitted", "first submitted paraphrase", false)
	first.SpaceID, first.SpaceGeneration = spaceID, generation
	secondItem := EvidenceInput{FragmentID: uuid.NewString(), Content: "second submitted paraphrase", ContentHash: sha256Hex("second submitted paraphrase"), SourceType: "manual", Authority: "primary"}
	first.Evidence = append(first.Evidence, secondItem)
	first.AssessmentID = assessmentID
	first.AssessmentJSON = json.RawMessage(`{"request_id":"duplicate-support-lineage"}`)
	first.EvidenceSecurityResults = append(first.EvidenceSecurityResults, EvidenceSecurityResult{
		FragmentID: secondItem.FragmentID, EvidenceID: "evidence:1", EvidenceIndex: 1, Decision: "pass", Safe: true,
	})
	first.Commit.AssessmentID = assessmentID
	first.Commit.Items = append(first.Commit.Items, SubmissionAssessmentItemInput{FragmentID: secondItem.FragmentID})
	first.Commit.EntityResolutions = []SubmissionAssessmentEntityResolutionInput{
		{Resolution: SemanticEntityResolutionInput{MentionRef: "subject", Action: string(domain.EntityResolutionCreate), EntityKind: "project", CanonicalName: "Support subject", FragmentID: first.Evidence[0].FragmentID, AssessmentID: assessmentID}},
		{Resolution: SemanticEntityResolutionInput{MentionRef: "object", Action: string(domain.EntityResolutionCreate), EntityKind: "product", CanonicalName: "Support object", FragmentID: first.Evidence[0].FragmentID, AssessmentID: assessmentID}},
	}
	relationshipInputs := []SubmissionAssessmentRelationshipObservationInput{
		{
			RelationshipRef: "support-lineage-relationship-0", SplitIndex: 0,
			Observation: SemanticRelationshipDecisionInput{
				Ref: "support-lineage-observation-0", SubjectRef: "subject", OriginalPredicate: "uses", PredicateKey: "uses", PredicateVersion: 1,
				ObjectRef: "object", Polarity: "+", AssessorAccepted: true, AssessmentID: assessmentID,
				Support: &EvidenceSupportInput{FragmentID: first.Evidence[0].FragmentID, SourceGroupKey: "support-lineage-group-0", SpanStart: 0, SpanEnd: len(first.Evidence[0].Content), Quote: first.Evidence[0].Content, Authority: "primary"},
			},
		},
		{
			RelationshipRef: "support-lineage-relationship-1", SplitIndex: 0,
			Observation: SemanticRelationshipDecisionInput{
				Ref: "support-lineage-observation-1", SubjectRef: "subject", OriginalPredicate: "uses", PredicateKey: "uses", PredicateVersion: 1,
				ObjectRef: "object", Polarity: "+", AssessorAccepted: true, AssessmentID: assessmentID,
				Support: &EvidenceSupportInput{FragmentID: secondItem.FragmentID, SourceGroupKey: "support-lineage-group-1", SpanStart: 0, SpanEnd: len(secondItem.Content), Quote: secondItem.Content, Authority: "primary"},
			},
		},
	}
	first.Commit.RelationshipObservations = relationshipInputs
	first.Commit.RelationshipResults = []SubmissionRelationshipResultInput{
		{RelationshipRef: "support-lineage-relationship-0", Disposition: "stored"},
		{RelationshipRef: "support-lineage-relationship-1", Disposition: "stored"},
	}
	first.DuplicateResolutions = []RememberDuplicateResolution{
		{EvidenceIndex: 0, EvidenceID: "evidence:0", InputFragmentID: first.Evidence[0].FragmentID, Disposition: "reuse", CandidateFragmentID: candidate.Evidence[0].FragmentID, CandidateOwnerID: ownerB},
		{EvidenceIndex: 1, EvidenceID: "evidence:1", InputFragmentID: secondItem.FragmentID, Disposition: "reuse", CandidateFragmentID: candidate.Evidence[0].FragmentID, CandidateOwnerID: ownerB},
	}
	plan, err := repo.PlanRememberEmbeddings(ctx, first)
	require.NoError(t, err)
	require.Len(t, plan.Documents, 1, "identical relationship projections share one provider vector")
	_, err = repo.CommitRememberWithEmbeddings(ctx, first, duplicatePlanToInline(plan))
	require.NoError(t, err)

	occurrences := make([]string, 2)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT occurrence_id::text
			FROM evidence_occurrences
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
			ORDER BY evidence_index
		`, teamID, first.IngestID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		index := 0
		for rows.Next() {
			if index >= len(occurrences) {
				return fmt.Errorf("too many occurrences")
			}
			if err := rows.Scan(&occurrences[index]); err != nil {
				return err
			}
			index++
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if index != len(occurrences) {
			return fmt.Errorf("got %d occurrences", index)
		}
		return nil
	}))
	type supportLineage struct {
		Group, Occurrence, EvidenceOwner, OccurrenceOwner string
	}
	var supports []supportLineage
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT support.source_group_key, support.occurrence_id::text,
			       support.evidence_owner_profile_id::text, support.occurrence_owner_profile_id::text
			FROM relationship_evidence_supports AS support
			JOIN relationship_observations AS observation
			  ON observation.team_id = support.team_id AND observation.observation_id = support.observation_id
			WHERE observation.team_id = ?::uuid AND observation.ingest_id = ?::uuid
			ORDER BY support.source_group_key
		`, teamID, first.IngestID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item supportLineage
			if err := rows.Scan(&item.Group, &item.Occurrence, &item.EvidenceOwner, &item.OccurrenceOwner); err != nil {
				return err
			}
			supports = append(supports, item)
		}
		return rows.Err()
	}))
	require.Len(t, supports, 2)
	require.Equal(t, occurrences[0], supports[0].Occurrence)
	require.Equal(t, occurrences[1], supports[1].Occurrence)
	for _, support := range supports {
		require.Equal(t, ownerB, support.EvidenceOwner)
		require.Equal(t, ownerA, support.OccurrenceOwner)
	}
}

func duplicatePlanEmbeddingsForInput(t *testing.T, ctx context.Context, repo *LedgerRepositoryImpl, input SynchronousRememberCommitInput) []InlineEmbeddingResult {
	t.Helper()
	plan, err := repo.PlanRememberDuplicateEmbeddings(ctx, duplicateCandidateInput(input))
	require.NoError(t, err)
	return duplicatePlanEmbeddings(plan)
}

func TestRememberDuplicateFinalReauthorizationRejectsQuarantinedCandidateAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-reauthorize", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-reauthorize-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-reauthorize-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-reauthorize-b")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)
	candidate := duplicateRememberInput(teamID, ownerB, "duplicate-reauthorize-candidate", "candidate becomes stale", false)
	candidate.SpaceID, candidate.SpaceGeneration = spaceID, generation
	commitDuplicateFixture(t, ctx, repo, candidate)
	submitted := duplicateRememberInput(teamID, ownerA, "duplicate-reauthorize-submitted", "candidate semantic paraphrase", false)
	submitted.SpaceID, submitted.SpaceGeneration = spaceID, generation
	resolved, err := resolveDuplicateFixture(t, ctx, repo, submitted)
	require.NoError(t, err)
	require.Len(t, resolved.Candidates, 1)
	resolved.Exact[0].Disposition = "reuse"
	resolved.Exact[0].CandidateFragmentID = candidate.Evidence[0].FragmentID
	resolved.Exact[0].CandidateOwnerID = ownerB
	submitted.DuplicateResolutions = resolved.Exact
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO evidence_quarantines (team_id, fragment_id, ingest_id, owner_profile_id, reason)
			SELECT team_id, fragment_id, ingest_id, owner_profile_id, 'test stale candidate'
			FROM evidence_fragments
			WHERE team_id = ?::uuid AND fragment_id = ?::uuid
		`, teamID, candidate.Evidence[0].FragmentID).Error
	}))
	embeddings := duplicatePlanEmbeddingsForInput(t, ctx, repo, submitted)
	_, err = repo.CommitRememberWithEmbeddings(ctx, submitted, embeddings)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrRememberDuplicateCandidateStale), "err=%v", err)
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, submitted.IngestID))
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_occurrences WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, submitted.IngestID))
}

func TestRememberDuplicateExactReuseSerializesWithConcurrentQuarantine(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-exact-quarantine-race", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-exact-quarantine-race-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-exact-quarantine-race-owner")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)

	content := "exact candidate held by a lifecycle writer"
	first := duplicateRememberInput(teamID, ownerID, "duplicate-exact-quarantine-race-first", content, false)
	first.SpaceID, first.SpaceGeneration = spaceID, generation
	commitDuplicateFixture(t, ctx, repo, first)
	second := duplicateRememberInput(teamID, ownerID, "duplicate-exact-quarantine-race-second", content, false)
	second.SpaceID, second.SpaceGeneration = spaceID, generation
	resolved, err := resolveDuplicateFixture(t, ctx, repo, second)
	require.NoError(t, err)
	require.True(t, resolved.Exact[0].Exact)
	second.DuplicateResolutions = resolved.Exact

	lockHeld := make(chan struct{})
	release := make(chan struct{})
	quarantineErr := make(chan error, 1)
	go func() {
		quarantineErr <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
			if err := lockEvidenceLifecycleTarget(ctx, tx, teamID, first.Evidence[0].FragmentID); err != nil {
				return err
			}
			if err := insertEvidenceQuarantine(ctx, tx, CreateIngestInput{TeamID: teamID, OwnerProfileID: ownerID}, first.IngestID, first.Evidence[0].FragmentID, "concurrent exact reuse quarantine"); err != nil {
				return err
			}
			close(lockHeld)
			<-release
			return nil
		})
	}()
	<-lockHeld

	commitDone := make(chan error, 1)
	go func() {
		_, commitErr := repo.CommitRememberWithEmbeddings(ctx, second, nil)
		commitDone <- commitErr
	}()
	select {
	case commitErr := <-commitDone:
		close(release)
		require.NoError(t, <-quarantineErr)
		require.FailNow(t, "exact reuse completed before lifecycle lock was released", "error: %v", commitErr)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-quarantineErr)
	require.ErrorIs(t, <-commitDone, ErrRememberDuplicateCandidateStale)
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, second.IngestID))
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_occurrences WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, second.IngestID))
}

func TestRememberDuplicateSemanticReuseSerializesWithConcurrentQuarantine(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-semantic-quarantine-race", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-semantic-quarantine-race-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-semantic-quarantine-race-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-semantic-quarantine-race-b")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)

	candidate := duplicateRememberInput(teamID, ownerB, "duplicate-semantic-quarantine-race-candidate", "semantic candidate held by a lifecycle writer", false)
	candidate.SpaceID, candidate.SpaceGeneration = spaceID, generation
	commitDuplicateFixture(t, ctx, repo, candidate)
	submitted := duplicateRememberInput(teamID, ownerA, "duplicate-semantic-quarantine-race-submitted", "a paraphrase of the candidate", false)
	submitted.SpaceID, submitted.SpaceGeneration = spaceID, generation
	resolved, err := resolveDuplicateFixture(t, ctx, repo, submitted)
	require.NoError(t, err)
	require.Len(t, resolved.Candidates, 1)
	submitted.DuplicateResolutions = resolved.Exact
	submitted.DuplicateResolutions[0].Disposition = "reuse"
	submitted.DuplicateResolutions[0].CandidateFragmentID = candidate.Evidence[0].FragmentID
	submitted.DuplicateResolutions[0].CandidateOwnerID = ownerB

	lockHeld := make(chan struct{})
	release := make(chan struct{})
	quarantineErr := make(chan error, 1)
	go func() {
		quarantineErr <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerB, func(tx *gorm.DB) error {
			if err := lockEvidenceLifecycleTarget(ctx, tx, teamID, candidate.Evidence[0].FragmentID); err != nil {
				return err
			}
			if err := insertEvidenceQuarantine(ctx, tx, CreateIngestInput{TeamID: teamID, OwnerProfileID: ownerB}, candidate.IngestID, candidate.Evidence[0].FragmentID, "concurrent semantic reuse quarantine"); err != nil {
				return err
			}
			close(lockHeld)
			<-release
			return nil
		})
	}()
	<-lockHeld

	commitDone := make(chan error, 1)
	go func() {
		_, commitErr := repo.CommitRememberWithEmbeddings(ctx, submitted, nil)
		commitDone <- commitErr
	}()
	select {
	case commitErr := <-commitDone:
		close(release)
		require.NoError(t, <-quarantineErr)
		require.FailNow(t, "semantic reuse completed before lifecycle lock was released", "error: %v", commitErr)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-quarantineErr)
	require.ErrorIs(t, <-commitDone, ErrRememberDuplicateCandidateStale)
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, submitted.IngestID))
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_occurrences WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, submitted.IngestID))
}

func TestRememberDuplicateExactReuseRejectsConcurrentSourceAdvance(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-exact-source-race", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-exact-source-race-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-exact-source-race-owner")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)

	content := "source-backed exact candidate"
	candidate := duplicateRememberInput(teamID, ownerID, "duplicate-exact-source-race-candidate", content, false)
	candidate.SpaceID, candidate.SpaceGeneration = spaceID, generation
	candidate.Evidence[0].SourceKey = "document://duplicate-exact-source-race"
	candidate.Evidence[0].SourceRevisionToken = "rev-1"
	candidate.Evidence[0].SourceRevisionContentHash = candidate.Evidence[0].ContentHash
	commitDuplicateFixture(t, ctx, repo, candidate)
	submitted := duplicateRememberInput(teamID, ownerID, "duplicate-exact-source-race-submitted", content, false)
	submitted.SpaceID, submitted.SpaceGeneration = spaceID, generation
	resolved, err := resolveDuplicateFixture(t, ctx, repo, submitted)
	require.NoError(t, err)
	require.True(t, resolved.Exact[0].Exact, "resolved=%+v candidates=%+v", resolved.Exact[0], resolved.Candidates)
	submitted.DuplicateResolutions = resolved.Exact

	lockHeld := make(chan struct{})
	release := make(chan struct{})
	advanceErr := make(chan error, 1)
	go func() {
		advanceErr <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
			_, err := advanceSourceRevisionInTx(ctx, tx, AdvanceSourceRevisionInput{
				TeamID: teamID, OwnerProfileID: ownerID, SpaceID: spaceID, SpaceGeneration: generation,
				SourceKey: candidate.Evidence[0].SourceKey, SourceKind: "document", Authority: "primary",
				RevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
				ContentHash: sha256Hex("source-backed exact candidate revision 2"), Envelope: map[string]any{},
			}, nil)
			if err != nil {
				return err
			}
			close(lockHeld)
			<-release
			return nil
		})
	}()
	select {
	case <-lockHeld:
	case err := <-advanceErr:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for source revision lock")
	}

	commitDone := make(chan error, 1)
	go func() {
		_, commitErr := repo.CommitRememberWithEmbeddings(ctx, submitted, nil)
		commitDone <- commitErr
	}()
	var commitErr error
	select {
	case commitErr = <-commitDone:
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-advanceErr)
	if commitErr == nil {
		commitErr = <-commitDone
	}
	require.ErrorIs(t, commitErr, ErrRememberDuplicateCandidateStale)
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, submitted.IngestID))
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_occurrences WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, submitted.IngestID))
}

func TestRememberDuplicateSemanticReuseRejectsConcurrentSourceAdvance(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-semantic-source-race", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-semantic-source-race-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-semantic-source-race-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-semantic-source-race-b")
	spaceID, generation := duplicateTeamSharedSpace(t, adminDB, rls, teamID)
	repo := NewLedgerRepository(appDB, rls)

	candidate := duplicateRememberInput(teamID, ownerB, "duplicate-semantic-source-race-candidate", "source-backed semantic candidate", false)
	candidate.SpaceID, candidate.SpaceGeneration = spaceID, generation
	candidate.Evidence[0].SourceKey = "document://duplicate-semantic-source-race"
	candidate.Evidence[0].SourceRevisionToken = "rev-1"
	candidate.Evidence[0].SourceRevisionContentHash = candidate.Evidence[0].ContentHash
	commitDuplicateFixture(t, ctx, repo, candidate)
	submitted := duplicateRememberInput(teamID, ownerA, "duplicate-semantic-source-race-submitted", "a paraphrase of the source candidate", false)
	submitted.SpaceID, submitted.SpaceGeneration = spaceID, generation
	resolved, err := resolveDuplicateFixture(t, ctx, repo, submitted)
	require.NoError(t, err)
	require.Len(t, resolved.Candidates, 1)
	submitted.DuplicateResolutions = resolved.Exact
	submitted.DuplicateResolutions[0].Disposition = "reuse"
	submitted.DuplicateResolutions[0].CandidateFragmentID = candidate.Evidence[0].FragmentID
	submitted.DuplicateResolutions[0].CandidateOwnerID = ownerB

	lockHeld := make(chan struct{})
	release := make(chan struct{})
	advanceErr := make(chan error, 1)
	go func() {
		advanceErr <- rls.WithTeamProfileTx(ctx, appDB, teamID, ownerB, func(tx *gorm.DB) error {
			_, err := advanceSourceRevisionInTx(ctx, tx, AdvanceSourceRevisionInput{
				TeamID: teamID, OwnerProfileID: ownerB, SpaceID: spaceID, SpaceGeneration: generation,
				SourceKey: candidate.Evidence[0].SourceKey, SourceKind: "document", Authority: "primary",
				RevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
				ContentHash: sha256Hex("source-backed semantic candidate revision 2"), Envelope: map[string]any{},
			}, nil)
			if err != nil {
				return err
			}
			close(lockHeld)
			<-release
			return nil
		})
	}()
	select {
	case <-lockHeld:
	case err := <-advanceErr:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for source revision lock")
	}

	commitDone := make(chan error, 1)
	go func() {
		_, commitErr := repo.CommitRememberWithEmbeddings(ctx, submitted, nil)
		commitDone <- commitErr
	}()
	var commitErr error
	select {
	case commitErr = <-commitDone:
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-advanceErr)
	if commitErr == nil {
		commitErr = <-commitDone
	}
	require.ErrorIs(t, commitErr, ErrRememberDuplicateCandidateStale)
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, submitted.IngestID))
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_occurrences WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, submitted.IngestID))
}

func TestRememberDuplicateExactReuseRejectsConcurrentPrivateSpaceSeal(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-private-space-race", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-private-space-race-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-private-space-race-owner")
	spaceID, generation := duplicatePrivateSpace(t, adminDB, rls, teamID, ownerID)
	privateCtx := requestctx.WithAllowedSpaces(ctx, []domain.MemorySpaceAccess{{ID: uuid.MustParse(spaceID), Kind: domain.MemorySpaceProfilePrivate}})
	repo := NewLedgerRepository(appDB, rls)

	content := "private-space exact candidate"
	candidate := duplicateRememberInput(teamID, ownerID, "duplicate-private-space-race-candidate", content, false)
	candidate.SpaceID, candidate.SpaceGeneration = spaceID, generation
	commitDuplicateFixture(t, privateCtx, repo, candidate)
	submitted := duplicateRememberInput(teamID, ownerID, "duplicate-private-space-race-submitted", content, false)
	submitted.SpaceID, submitted.SpaceGeneration = spaceID, generation
	resolved, err := resolveDuplicateFixture(t, privateCtx, repo, submitted)
	require.NoError(t, err)
	require.True(t, resolved.Exact[0].Exact, "resolved=%+v candidates=%+v", resolved.Exact[0], resolved.Candidates)
	submitted.DuplicateResolutions = resolved.Exact

	lockHeld := make(chan struct{})
	release := make(chan struct{})
	sealErr := make(chan error, 1)
	go func() {
		sealErr <- rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
			result := tx.Exec(`
				UPDATE memory_spaces
				SET lifecycle_state = 'sealed', generation = generation + 1, sealed_at = now(), updated_at = now()
				WHERE team_id = ?::uuid AND id = ?::uuid AND generation = ?
			`, teamID, spaceID, generation)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errors.New("private space seal did not update one row")
			}
			close(lockHeld)
			<-release
			return nil
		})
	}()
	select {
	case <-lockHeld:
	case err := <-sealErr:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for private-space seal")
	}

	commitDone := make(chan error, 1)
	go func() {
		_, commitErr := repo.CommitRememberWithEmbeddings(privateCtx, submitted, nil)
		commitDone <- commitErr
	}()
	var commitErr error
	select {
	case commitErr = <-commitDone:
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	require.NoError(t, <-sealErr)
	if commitErr == nil {
		commitErr = <-commitDone
	}
	require.ErrorIs(t, commitErr, ErrRememberDuplicateCandidateStale)
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, submitted.IngestID))
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM evidence_occurrences WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, submitted.IngestID))
}

func TestRememberDuplicateEmbeddingPlanUsesCanonicalRenderedBytesAndRejectsContractChange(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "duplicate-embedding-fence", 2, "exact", "")
	teamID := createLedgerTeam(t, adminDB, rls, "duplicate-embedding-fence-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "duplicate-embedding-fence-owner")
	repo := NewLedgerRepository(appDB, rls)
	input := duplicateRememberInput(teamID, ownerID, "duplicate-embedding-padding", "  padded rendered evidence  ", false)
	duplicateInput := duplicateCandidateInput(input)
	plan, err := repo.PlanRememberDuplicateEmbeddings(ctx, duplicateInput)
	require.NoError(t, err)
	require.Len(t, plan.Documents, 1)
	require.Equal(t, "padded rendered evidence", plan.Documents[0].DocumentText)
	require.Equal(t, searchDocumentHash(plan.Documents[0].DocumentText), plan.Documents[0].DocumentHash)
	resolved, err := repo.ResolveRememberDuplicateCandidates(ctx, duplicateInput, duplicatePlanEmbeddings(plan))
	require.NoError(t, err)
	input.DuplicateResolutions = resolved.Exact
	_, err = repo.CommitRememberWithEmbeddings(ctx, input, duplicatePlanEmbeddings(plan))
	require.NoError(t, err)

	stale := duplicateRememberInput(teamID, ownerID, "duplicate-embedding-stale", "contract changes after provider work", false)
	stalePlan, err := repo.PlanRememberDuplicateEmbeddings(ctx, duplicateCandidateInput(stale))
	require.NoError(t, err)
	staleResolved, err := repo.ResolveRememberDuplicateCandidates(ctx, duplicateCandidateInput(stale), duplicatePlanEmbeddings(stalePlan))
	require.NoError(t, err)
	stale.DuplicateResolutions = staleResolved.Exact
	oldEmbeddings := duplicatePlanEmbeddings(stalePlan)
	insertSearchTestContract(t, adminDB, rls, "duplicate-embedding-fence-new", 2, "exact", "")
	_, err = repo.CommitRememberWithEmbeddings(ctx, stale, oldEmbeddings)
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSearchContractMismatch), "err=%v", err)
	require.EqualValues(t, 0, duplicateCount(t, adminDB, rls, `SELECT count(*) FROM knowledge_ingests WHERE team_id = ?::uuid AND ingest_id = ?::uuid`, teamID, stale.IngestID))
}
