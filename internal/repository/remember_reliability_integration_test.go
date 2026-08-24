package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestRememberPreflightAggregatesExactReferenceIssuesBeforeStaging(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-preflight-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-preflight-owner")
	ledger := NewLedgerRepository(appDB, rls)
	missingSubjectID := uuid.NewString()
	missingObjectID := uuid.NewString()
	missingCorrectionID := uuid.NewString()
	missingConflictID := uuid.NewString()
	missingEvidenceID := uuid.NewString()

	_, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID:            teamID,
		OwnerProfileID:    ownerID,
		IdempotencyKey:    "remember-preflight-invalid",
		RequestHash:       "remember-preflight-invalid-hash",
		TelemetryRemember: true,
		Proposal: map[string]any{"relationship_hints": []map[string]any{{
			"subject":   map[string]any{"known_entity_id": missingSubjectID},
			"predicate": map[string]any{"known_predicate_key": "missing_predicate"},
			"object":    map[string]any{"entity": map[string]any{"known_entity_id": missingObjectID}},
			"correction_target": map[string]any{
				"relationship_id": missingCorrectionID, "expected_version": 1,
			},
			"conflict_context": map[string]any{
				"conflict_id": missingConflictID, "expected_version": 1,
			},
		}}},
		Evidence: []EvidenceInput{{
			Content:                       "Invalid exact source references must not stage.",
			SourceKey:                     "doc://remember-preflight-missing",
			SourceRevisionToken:           "rev-2",
			ExpectedPreviousRevisionToken: "rev-1",
			SourceRevisionContentHash:     "sha256:remember-preflight-missing",
		}, {
			Content: "Invalid supersession references must not stage.", SupersedesEvidenceIDs: []string{missingEvidenceID},
		}},
	})
	require.Error(t, err)
	var preflight *RememberPreflightError
	require.True(t, errors.As(err, &preflight), "err=%v", err)
	paths := make([]string, 0, len(preflight.Issues))
	for _, issue := range preflight.Issues {
		paths = append(paths, issue.Path)
	}
	for _, path := range []string{
		"/evidence/0/previous_source_revision",
		"/evidence/1/supersedes_evidence_ids/0",
		"/relationships/0/conflict_context/conflict_id",
		"/relationships/0/correction_target/relationship_id",
		"/relationships/0/object/entity/known_entity_id",
		"/relationships/0/predicate/known_predicate_key",
		"/relationships/0/subject/known_entity_id",
	} {
		require.Contains(t, paths, path)
	}

	var staged int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*) FROM knowledge_ingests
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND idempotency_key = 'remember-preflight-invalid'
		`, teamID, ownerID).Scan(&staged).Error
	}))
	assert.Zero(t, staged)
}

func TestRememberPreflightRejectsStaleSourceAndCrossProfileSupersession(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-preflight-source-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "remember-preflight-source-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "remember-preflight-source-owner-b")
	ledger := NewLedgerRepository(appDB, rls)
	first, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerA, IdempotencyKey: "remember-preflight-source-first",
		RequestHash: "remember-preflight-source-first-hash",
		Evidence: []EvidenceInput{{
			Content: "Current source revision.", SourceKey: "doc://remember-preflight-source",
			SourceRevisionToken: "rev-1", SourceRevisionContentHash: "sha256:remember-preflight-source-rev-1",
		}},
	})
	require.NoError(t, err)
	require.Len(t, first.Evidence, 1)

	_, err = ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerA, IdempotencyKey: "remember-preflight-source-stale",
		RequestHash: "remember-preflight-source-stale-hash", TelemetryRemember: true,
		Evidence: []EvidenceInput{{
			Content: "Stale source revision.", SourceKey: "doc://remember-preflight-source",
			SourceRevisionToken: "rev-2", ExpectedPreviousRevisionToken: "wrong",
			SourceRevisionContentHash: "sha256:remember-preflight-source-rev-2",
		}},
	})
	var sourcePreflight *RememberPreflightError
	require.True(t, errors.As(err, &sourcePreflight), "err=%v", err)
	require.Contains(t, sourcePreflight.Issues, RememberPreflightIssue{
		Path: "/evidence/0/previous_source_revision", Code: "stale", Message: "source revision is stale",
	})

	_, err = ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerB, IdempotencyKey: "remember-preflight-cross-profile-supersession",
		RequestHash: "remember-preflight-cross-profile-supersession-hash", TelemetryRemember: true,
		Evidence: []EvidenceInput{{
			Content: "Cross-profile replacement.", SupersedesEvidenceIDs: []string{first.Evidence[0].FragmentID},
		}},
	})
	var supersessionPreflight *RememberPreflightError
	require.True(t, errors.As(err, &supersessionPreflight), "err=%v", err)
	require.Contains(t, supersessionPreflight.Issues, RememberPreflightIssue{
		Path: "/evidence/0/supersedes_evidence_ids/0", Code: "unavailable", Message: "supersession target is unavailable",
	})

	staged, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerA, IdempotencyKey: "remember-preflight-source-current",
		RequestHash: "remember-preflight-source-current-hash", TelemetryRemember: true,
		Evidence: []EvidenceInput{{
			Content: "Next source revision.", SourceKey: "doc://remember-preflight-source",
			SourceRevisionToken: "rev-2", ExpectedPreviousRevisionToken: "rev-1",
			SourceRevisionContentHash: "sha256:remember-preflight-source-rev-2",
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, staged)
	var currentToken string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT current_revision_token FROM evidence_sources
			WHERE team_id = ?::uuid AND owner_profile_id = ?::uuid AND source_key = 'doc://remember-preflight-source'
		`, teamID, ownerA).Row().Scan(&currentToken)
	}))
	assert.Equal(t, "rev-1", currentToken)
}

func TestRememberPreflightAppliesTeamVisibilityAndOwnerMutationAuthorityABC(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-preflight-abc-team")
	ownerA := createLedgerProfile(t, adminDB, rls, teamID, "remember-preflight-abc-owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamID, "remember-preflight-abc-owner-b")
	ownerC := createLedgerProfile(t, adminDB, rls, teamID, "remember-preflight-abc-owner-c")
	insertSearchTestContract(t, adminDB, rls, "remember-preflight-abc-search", 3, "exact", "")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "project", "Dense-Mem ABC")
	otherSubject := createSemanticEntity(t, ctx, semantic, teamID, ownerC, "project", "Dense-Mem C")
	postgres := createSemanticEntity(t, ctx, semantic, teamID, ownerA, "product", "PostgreSQL ABC")
	graphDB := createSemanticEntity(t, ctx, semantic, teamID, ownerB, "product", "GraphDB ABC")
	redis := createSemanticEntity(t, ctx, semantic, teamID, ownerC, "product", "Redis ABC")

	commitPlacementRelationshipForConflictTest(
		t, ctx, ledger, teamID, ownerA, "remember-preflight-abc-worker-a", "remember-preflight-abc-a",
		"Dense-Mem ABC uses PostgreSQL ABC.", subject.EntityID, postgres.EntityID, "remember-preflight-abc-source-a",
	)
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledger, teamID, ownerB, "remember-preflight-abc-worker-b", "remember-preflight-abc-b",
		"Dense-Mem ABC uses GraphDB ABC.", subject.EntityID, graphDB.EntityID, "remember-preflight-abc-source-b",
	)
	conflictID, conflictVersion := loadConflictCaseVersionForSubject(t, ctx, appDB, rls, teamID, ownerA, subject.EntityID)
	ownerCRelationship := commitPlacementRelationshipForConflictTest(
		t, ctx, ledger, teamID, ownerC, "remember-preflight-abc-worker-c", "remember-preflight-abc-c",
		"Dense-Mem C uses Redis ABC.", otherSubject.EntityID, redis.EntityID, "remember-preflight-abc-source-c",
	).RelationshipResults[0].Relationship
	require.NotNil(t, ownerCRelationship)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO team_predicate_definitions (
				team_id, predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
				relationship_kind, current_cardinality, lifecycle_state, origin, metadata
			) VALUES (?::uuid, 'abc_exact_predicate', 1, ARRAY[]::text[], ARRAY['project']::text[], ARRAY['product']::text[],
			          'state', 'many', 'active', 'built_in', '{}'::jsonb)
		`, teamID).Error
	}))
	valid, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerC, IdempotencyKey: "remember-preflight-abc-valid",
		RequestHash: "remember-preflight-abc-valid-hash", TelemetryRemember: true,
		Proposal: map[string]any{"relationship_hints": []map[string]any{
			{
				"subject":          map[string]any{"known_entity_id": subject.EntityID},
				"predicate":        map[string]any{"known_predicate_key": "abc_exact_predicate"},
				"conflict_context": map[string]any{"conflict_id": conflictID, "expected_version": conflictVersion},
			},
			{
				"correction_target": map[string]any{
					"relationship_id": ownerCRelationship.RelationshipID, "expected_version": ownerCRelationship.Version,
				},
			},
		}},
		Evidence: []EvidenceInput{{Content: "Owner C can read team context and mutate only owner C state."}},
	})
	require.NoError(t, err)
	require.NotNil(t, valid)

	_, err = ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerB, IdempotencyKey: "remember-preflight-abc-non-owner",
		RequestHash: "remember-preflight-abc-non-owner-hash", TelemetryRemember: true,
		Proposal: map[string]any{"relationship_hints": []map[string]any{{
			"correction_target": map[string]any{
				"relationship_id": ownerCRelationship.RelationshipID, "expected_version": ownerCRelationship.Version,
			},
		}}},
		Evidence: []EvidenceInput{{Content: "Owner B cannot mutate owner C state."}},
	})
	var ownerError *RememberPreflightError
	require.True(t, errors.As(err, &ownerError), "err=%v", err)
	require.Contains(t, ownerError.Issues, RememberPreflightIssue{
		Path: "/relationships/0/correction_target/relationship_id", Code: "unavailable", Message: "correction target is unavailable",
	})

	otherTeamID := createLedgerTeam(t, adminDB, rls, "remember-preflight-other-team")
	otherOwner := createLedgerProfile(t, adminDB, rls, otherTeamID, "remember-preflight-other-owner")
	otherSemantic := NewSemanticRepository(appDB, rls)
	otherEntity := createSemanticEntity(t, ctx, otherSemantic, otherTeamID, otherOwner, "project", "Other Team Entity")
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO team_predicate_definitions (
				team_id, predicate_key, version, aliases, allowed_subject_kinds, allowed_object_kinds,
				relationship_kind, current_cardinality, lifecycle_state, origin, metadata
			) VALUES (?::uuid, 'other_team_only', 1, ARRAY[]::text[], ARRAY['project']::text[], ARRAY['product']::text[],
			          'state', 'many', 'active', 'built_in', '{}'::jsonb)
		`, otherTeamID).Error
	}))
	_, err = ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerC, IdempotencyKey: "remember-preflight-abc-cross-team",
		RequestHash: "remember-preflight-abc-cross-team-hash", TelemetryRemember: true,
		Proposal: map[string]any{"relationship_hints": []map[string]any{{
			"subject":   map[string]any{"known_entity_id": otherEntity.EntityID},
			"predicate": map[string]any{"known_predicate_key": "other_team_only"},
		}}},
		Evidence: []EvidenceInput{{Content: "Cross-team exact references are unavailable."}},
	})
	var crossTeamError *RememberPreflightError
	require.True(t, errors.As(err, &crossTeamError), "err=%v", err)
	paths := make([]string, 0, len(crossTeamError.Issues))
	for _, issue := range crossTeamError.Issues {
		paths = append(paths, issue.Path)
	}
	require.Contains(t, paths, "/relationships/0/subject/known_entity_id")
	require.Contains(t, paths, "/relationships/0/predicate/known_predicate_key")
}

func TestRememberPreflightFencesExactEntitiesToTheIngestSpace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "remember-preflight-private-space"))
	identityID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	credentialA := createOwnedCredential(t, credentialRepo, teamID, identityID, "remember-private-a", domain.CredentialBindingCredentialPrivate)
	credentialB := createOwnedCredential(t, credentialRepo, teamID, identityID, "remember-private-b", domain.CredentialBindingCredentialPrivate)
	entityA, entityB := uuid.New(), uuid.New()
	var generationA, generationB int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?`, credentialA.MemorySpaceID).Row().Scan(&generationA); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?`, credentialB.MemorySpaceID).Row().Scan(&generationB); err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO entity_records (team_id, entity_id, entity_kind, space_id, space_generation)
			VALUES (?, ?, 'project', ?, ?), (?, ?, 'project', ?, ?)
		`, teamID, entityA, credentialA.MemorySpaceID, generationA,
			teamID, entityB, credentialB.MemorySpaceID, generationB).Error
	}))
	privateCtx := requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID, OwnerID: credentialA.ID,
		AllowedSpaces: []domain.MemorySpaceAccess{{
			ID: credentialA.MemorySpaceID, Kind: domain.MemorySpaceCredentialPrivate, Generation: generationA,
		}},
	})
	ledger := NewLedgerRepository(appDB, rls)
	valid, err := ledger.CreateIngest(privateCtx, CreateIngestInput{
		TeamID: teamID.String(), OwnerProfileID: credentialA.ID.String(),
		SpaceID: credentialA.MemorySpaceID.String(), SpaceGeneration: generationA,
		IdempotencyKey: "remember-private-same-space", RequestHash: "remember-private-same-space-hash", TelemetryRemember: true,
		Proposal: map[string]any{"relationship_hints": []map[string]any{{
			"subject": map[string]any{"known_entity_id": entityA.String()},
		}}},
		Evidence: []EvidenceInput{{Content: "A same-space exact Entity remains available."}},
	})
	require.NoError(t, err)
	require.NotNil(t, valid)

	_, err = ledger.CreateIngest(privateCtx, CreateIngestInput{
		TeamID: teamID.String(), OwnerProfileID: credentialA.ID.String(),
		SpaceID: credentialA.MemorySpaceID.String(), SpaceGeneration: generationA,
		IdempotencyKey: "remember-private-kind-mismatch", RequestHash: "remember-private-kind-mismatch-hash", TelemetryRemember: true,
		Proposal: map[string]any{"relationship_hints": []map[string]any{{
			"subject": map[string]any{"known_entity_id": entityA.String(), "entity_kind": "product"},
		}}},
		Evidence: []EvidenceInput{{Content: "A conflicting exact Entity kind must be rejected before staging."}},
	})
	var kindPreflight *RememberPreflightError
	require.True(t, errors.As(err, &kindPreflight), "err=%v", err)
	require.Contains(t, kindPreflight.Issues, RememberPreflightIssue{
		Path: "/relationships/0/subject/entity_kind", Code: "conflict", Message: "entity_kind does not match known_entity_id",
	})
	var kindMismatchStaged int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*) FROM knowledge_ingests
			WHERE team_id = ? AND owner_profile_id = ? AND idempotency_key = 'remember-private-kind-mismatch'
		`, teamID, credentialA.ID).Scan(&kindMismatchStaged).Error
	}))
	require.Zero(t, kindMismatchStaged)

	_, err = ledger.CreateIngest(privateCtx, CreateIngestInput{
		TeamID: teamID.String(), OwnerProfileID: credentialA.ID.String(),
		SpaceID: credentialA.MemorySpaceID.String(), SpaceGeneration: generationA,
		IdempotencyKey: "remember-private-cross-space", RequestHash: "remember-private-cross-space-hash", TelemetryRemember: true,
		Proposal: map[string]any{"relationship_hints": []map[string]any{{
			"subject": map[string]any{"known_entity_id": entityB.String()},
		}}},
		Evidence: []EvidenceInput{{Content: "A cross-space exact Entity must remain unavailable."}},
	})
	var preflight *RememberPreflightError
	require.True(t, errors.As(err, &preflight), "err=%v", err)
	require.Contains(t, preflight.Issues, RememberPreflightIssue{
		Path: "/relationships/0/subject/known_entity_id", Code: "unavailable", Message: "known_entity_id is unavailable",
	})
	var staged int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*) FROM knowledge_ingests
			WHERE team_id = ? AND owner_profile_id = ? AND idempotency_key = 'remember-private-cross-space'
		`, teamID, credentialA.ID).Scan(&staged).Error
	}))
	require.Zero(t, staged)
}

func TestRememberSupersessionIntentIsRemovedByPrivateMemoryErasure(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "remember-private-supersession-erasure"))
	identityID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	credential := createOwnedCredential(t, credentialRepo, teamID, identityID, "remember-private-supersession", domain.CredentialBindingCredentialPrivate)
	var generation int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT generation FROM memory_spaces WHERE id = ?`, credential.MemorySpaceID).Row().Scan(&generation)
	}))
	privateCtx := requestctx.WithActor(ctx, requestctx.Actor{
		TeamID: teamID, OwnerID: credential.ID,
		AllowedSpaces: []domain.MemorySpaceAccess{{
			ID: credential.MemorySpaceID, Kind: domain.MemorySpaceCredentialPrivate, Generation: generation,
		}},
	})
	ledger := NewLedgerRepository(appDB, rls)
	target, err := ledger.CreateIngest(privateCtx, CreateIngestInput{
		TeamID: teamID.String(), OwnerProfileID: credential.ID.String(),
		SpaceID: credential.MemorySpaceID.String(), SpaceGeneration: generation,
		IdempotencyKey: "remember-private-supersession-target", RequestHash: "remember-private-supersession-target-hash",
		Evidence: []EvidenceInput{{Content: "Private evidence to replace."}},
	})
	require.NoError(t, err)
	require.Len(t, target.Evidence, 1)
	replacement, err := ledger.CreateIngest(privateCtx, CreateIngestInput{
		TeamID: teamID.String(), OwnerProfileID: credential.ID.String(),
		SpaceID: credential.MemorySpaceID.String(), SpaceGeneration: generation,
		IdempotencyKey: "remember-private-supersession-replacement", RequestHash: "remember-private-supersession-replacement-hash", TelemetryRemember: true,
		Evidence: []EvidenceInput{{
			Content: "Private replacement evidence.", SourceKey: "doc://private-replacement", SourceRevisionToken: "rev-1",
			SupersedesEvidenceIDs: []string{target.Evidence[0].FragmentID},
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, replacement)
	var intentSpaceID string
	var intentGeneration int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT space_id::text, space_generation
			FROM remember_supersession_intents
			WHERE team_id = ? AND ingest_id = ?
		`, teamID, replacement.IngestID).Row().Scan(&intentSpaceID, &intentGeneration)
	}))
	require.Equal(t, credential.MemorySpaceID.String(), intentSpaceID)
	require.Equal(t, generation, intentGeneration)
	assessmentPayload := json.RawMessage(`{"request_id":"private-erasure-assessment","security_signals":[],"entity_results":[],"relationship_results":[]}`)
	assessment, existing, err := ledger.PersistSubmissionAssessment(privateCtx, PersistSubmissionAssessmentInput{
		TeamID: teamID.String(), OwnerProfileID: credential.ID.String(), IngestID: replacement.IngestID,
		PlacementRunID: replacement.PlacementRunID, RequestID: "private-erasure-assessment",
		AssessorContractVersion: domain.ContractVersion, Model: "private-erasure-model", Tokenizer: "o200k_base",
		ProviderTurns: 1, NormalizedResponse: assessmentPayload, ResponseHash: sha256Hex(string(assessmentPayload)),
		ValidatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	require.False(t, existing)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			INSERT INTO submission_assessment_response_revisions (
			    team_id, assessment_id, ingest_id, placement_run_id, owner_profile_id,
			    revision_number, provider_turns, input_tokens, output_tokens, candidate_context_tokens,
			    candidate_context_truncated, normalized_response, response_hash, validated_at,
			    space_id, space_generation
			) VALUES (?, ?, ?, ?, ?, 1, 2, 0, 0, 0, false, ?::jsonb, ?, now(), ?, ?)
		`, teamID, assessment.AssessmentID, replacement.IngestID, replacement.PlacementRunID, credential.ID,
			string(assessmentPayload), sha256Hex("private-erasure-assessment-revision"), credential.MemorySpaceID, generation).Error
	}))
	err = rls.WithTeamProfileTx(privateCtx, appDB, teamID.String(), credential.ID.String(), func(tx *gorm.DB) error {
		return tx.Exec(`DELETE FROM remember_source_revision_intents WHERE team_id = ? AND ingest_id = ?`, teamID, replacement.IngestID).Error
	})
	require.Error(t, err)

	privateRepo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, privateRepo.Prepare(ctx))
	operation, created, err := privateRepo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: credential.ID, CredentialID: credential.ID,
		IdempotencyScopeHash: privateMemoryHash("remember-private-supersession", credential.ID.String()),
		RequestHash:          privateMemoryHash("erase-remember-private-supersession", credential.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)
	claim, err := privateRepo.ClaimNext(ctx, "remember-private-supersession-worker", time.Minute)
	require.NoError(t, err)
	require.Equal(t, operation.ID, claim.ID)
	completed, err := privateRepo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)
	require.Equal(t, int64(1), completed.DeletedCounts["remember_source_revision_intents"])
	require.Equal(t, int64(1), completed.DeletedCounts["remember_supersession_intents"])
	require.Equal(t, int64(1), completed.DeletedCounts["submission_assessment_response_revisions"])
	var remaining int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*) FROM remember_supersession_intents WHERE space_id = ?
		`, credential.MemorySpaceID).Scan(&remaining).Error
	}))
	require.Zero(t, remaining)
}

func TestCompletePlacementRunUsesPlacementRunScope(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-complete-run-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-complete-run-owner")
	ledger := NewLedgerRepository(appDB, rls)
	staged := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "remember-complete-run", "Terminal placement evidence.")

	claimed, err := ledger.ClaimNextPlacementRun(ctx, teamID, "remember-complete-run-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, staged.PlacementRunID, claimed.PlacementRunID)

	_, err = ledger.FinishPlacementRun(
		ctx,
		teamID,
		staged.PlacementRunID,
		"remember-complete-run-worker",
		string(domain.PlacementRunFailed),
		"fixture terminal failure",
	)
	require.NoError(t, err)
}

func TestRememberStagesSourceAndSupersessionIntentsUntilCommit(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-intents-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-intents-owner")
	ledger := NewLedgerRepository(appDB, rls)
	target := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "remember-intents-target", "Old source evidence.")

	staged, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID:            teamID,
		OwnerProfileID:    ownerID,
		IdempotencyKey:    "remember-intents-batch",
		RequestHash:       "remember-intents-request",
		TelemetryRemember: true,
		Evidence: []EvidenceInput{{
			Content:                   "New source evidence.",
			SourceType:                "document",
			SourceKey:                 "doc://remember-intents",
			SourceRevisionToken:       "rev-1",
			SourceRevisionContentHash: "sha256:new-source",
			SupersedesEvidenceIDs:     []string{target.Evidence[0].FragmentID},
		}},
	})
	require.NoError(t, err)
	require.Len(t, staged.Evidence, 1)
	assert.Empty(t, staged.Evidence[0].SourceID)
	assert.Empty(t, staged.Evidence[0].SourceRevisionID)

	var sourceID, sourceRevisionID sql.NullString
	var sourceIntentCount, supersessionIntentCount, lifecycleEventCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT source_id::text, source_revision_id::text
			FROM remember_source_revision_intents
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, staged.IngestID).Row().Scan(&sourceID, &sourceRevisionID); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM remember_source_revision_intents
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, staged.IngestID).Scan(&sourceIntentCount).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COUNT(*) FROM remember_supersession_intents
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, staged.IngestID).Scan(&supersessionIntentCount).Error
	})
	require.NoError(t, err)
	assert.False(t, sourceID.Valid)
	assert.False(t, sourceRevisionID.Valid)
	assert.Equal(t, int64(1), sourceIntentCount)
	assert.Equal(t, int64(1), supersessionIntentCount)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return applyRememberSubmissionIntents(ctx, tx, SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: staged.IngestID,
		})
	})
	require.NoError(t, err)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT source_id::text, source_revision_id::text
			FROM remember_source_revision_intents
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, staged.IngestID).Row().Scan(&sourceID, &sourceRevisionID); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM evidence_lifecycle_events
			WHERE team_id = ?::uuid AND replacement_fragment_id = ?::uuid
		`, teamID, staged.Evidence[0].FragmentID).Scan(&lifecycleEventCount).Error; err != nil {
			return err
		}
		return nil
	})
	require.NoError(t, err)
	assert.True(t, sourceID.Valid)
	assert.True(t, sourceRevisionID.Valid)
	assert.Equal(t, int64(1), lifecycleEventCount)
}

func TestRememberSourceIntentCommitsWithSemanticPlacementAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-source-commit-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-source-commit-owner")
	insertSearchTestContract(t, adminDB, rls, "remember-source-commit-search", 3, "exact", "")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)
	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	object := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "product", "PostgreSQL")
	content := "Dense-Mem uses PostgreSQL."

	staged, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID, OwnerProfileID: ownerID,
		IdempotencyKey: "remember-source-commit", RequestHash: "remember-source-commit-request",
		TelemetryRemember: true,
		Evidence: []EvidenceInput{{
			Content: content, SourceType: "document", SourceKey: "doc://remember-source-commit",
			SourceRevisionToken: "rev-1", SourceRevisionContentHash: sha256Hex(content),
		}},
	})
	require.NoError(t, err)
	claimed, err := ledger.ClaimNextPlacementRun(ctx, teamID, "remember-source-commit-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assessment := persistSubmissionAssessment(t, ctx, ledger, *claimed)

	committed, err := ledger.CommitSubmissionAssessment(ctx, CommitSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: staged.IngestID,
			PlacementRunID: staged.PlacementRunID, WorkerID: "remember-source-commit-worker", ExpectedAttempts: claimed.Attempts,
		},
		AssessmentID: assessment.AssessmentID,
		Items: []SubmissionAssessmentItemInput{{
			PlacementItemID: staged.Items[0].PlacementItemID, FragmentID: staged.Evidence[0].FragmentID,
		}},
		EntityResolutions: []SubmissionAssessmentEntityResolutionInput{
			{PlacementItemID: staged.Items[0].PlacementItemID, Resolution: PlacementEntityResolutionInput{
				MentionRef: "subject", Action: string(domain.EntityResolutionReuse), EntityID: subject.EntityID,
				FragmentID: staged.Evidence[0].FragmentID, AssessmentID: assessment.AssessmentID,
			}},
			{PlacementItemID: staged.Items[0].PlacementItemID, Resolution: PlacementEntityResolutionInput{
				MentionRef: "object", Action: string(domain.EntityResolutionReuse), EntityID: object.EntityID,
				FragmentID: staged.Evidence[0].FragmentID, AssessmentID: assessment.AssessmentID,
			}},
		},
		RelationshipObservations: []SubmissionAssessmentRelationshipObservationInput{{
			PlacementItemID: staged.Items[0].PlacementItemID, RelationshipRef: "uses-postgresql", SplitIndex: 0,
			Observation: PlacementRelationshipDecisionInput{
				Ref: "uses-postgresql", SubjectRef: "subject", OriginalPredicate: "uses",
				PredicateKey: "uses", PredicateVersion: 1, ObjectRef: "object", Polarity: "+",
				AssessorAccepted: true, Model: "remember-source-commit", ResponseHash: "sha256:remember-source-commit",
				AssessmentID: assessment.AssessmentID,
				Support: &EvidenceSupportInput{
					FragmentID: staged.Evidence[0].FragmentID, SourceGroupKey: "remember-source-commit",
					SpanStart: 0, SpanEnd: len(content), Quote: content, Authority: "primary",
				},
			},
		}},
		RelationshipResults: []SubmissionRelationshipResultInput{{
			RelationshipRef: "uses-postgresql", Disposition: "stored",
		}},
		Payload: map[string]any{"assessor_contract": domain.ContractVersion},
	})
	require.NoError(t, err)
	require.NotNil(t, committed)

	var sourceID, sourceRevisionID, fragmentSourceID, fragmentSourceRevisionID sql.NullString
	var resultCount, sourceSupportCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT source_id::text, source_revision_id::text
			FROM remember_source_revision_intents
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, staged.IngestID).Row().Scan(&sourceID, &sourceRevisionID); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT source_id::text, source_revision_id::text
			FROM evidence_fragments
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid AND fragment_id = ?::uuid
		`, teamID, staged.IngestID, staged.Evidence[0].FragmentID).Row().Scan(&fragmentSourceID, &fragmentSourceRevisionID); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT
			  (SELECT COUNT(*) FROM submission_relationship_results
			   WHERE team_id = ?::uuid AND ingest_id = ?::uuid),
			  (SELECT COUNT(*)
			   FROM relationship_evidence_supports AS support
			   JOIN remember_source_revision_intents AS intent
			     ON intent.team_id = support.team_id
			    AND intent.fragment_id = support.fragment_id
			    AND intent.source_id = support.source_id
			    AND intent.source_revision_id = support.source_revision_id
			   WHERE support.team_id = ?::uuid
			     AND intent.ingest_id = ?::uuid)
		`, teamID, staged.IngestID, teamID, staged.IngestID).Row().Scan(&resultCount, &sourceSupportCount)
	})
	require.NoError(t, err)
	assert.True(t, sourceID.Valid)
	assert.True(t, sourceRevisionID.Valid)
	assert.Equal(t, sourceID, fragmentSourceID)
	assert.Equal(t, sourceRevisionID, fragmentSourceRevisionID)
	assert.Equal(t, int64(1), resultCount)
	assert.Equal(t, int64(1), sourceSupportCount)
	for _, update := range []struct {
		name  string
		query string
		args  []any
	}{
		{
			name: "unrelated evidence field",
			query: `UPDATE evidence_fragments SET content = content || ' forbidden'
				WHERE team_id = ?::uuid AND fragment_id = ?::uuid`,
			args: []any{teamID, staged.Evidence[0].FragmentID},
		},
		{
			name: "second source binding",
			query: `UPDATE evidence_fragments SET source_id = ?::uuid, source_revision_id = ?::uuid
				WHERE team_id = ?::uuid AND fragment_id = ?::uuid`,
			args: []any{uuid.NewString(), uuid.NewString(), teamID, staged.Evidence[0].FragmentID},
		},
		{
			name: "source unbinding",
			query: `UPDATE evidence_fragments SET source_id = NULL, source_revision_id = NULL
				WHERE team_id = ?::uuid AND fragment_id = ?::uuid`,
			args: []any{teamID, staged.Evidence[0].FragmentID},
		},
	} {
		t.Run(update.name, func(t *testing.T) {
			err := rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
				result := tx.Exec(update.query, update.args...)
				require.NoError(t, result.Error)
				assert.Zero(t, result.RowsAffected)
				return nil
			})
			require.NoError(t, err)
		})
	}

	var finalContent, finalSourceID, finalSourceRevisionID string
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT content, source_id::text, source_revision_id::text
			FROM evidence_fragments
			WHERE team_id = ?::uuid AND fragment_id = ?::uuid
		`, teamID, staged.Evidence[0].FragmentID).Row().Scan(&finalContent, &finalSourceID, &finalSourceRevisionID)
	})
	require.NoError(t, err)
	assert.Equal(t, content, finalContent)
	assert.Equal(t, sourceID.String, finalSourceID)
	assert.Equal(t, sourceRevisionID.String, finalSourceRevisionID)
}

func TestRememberRejectedSubmissionDoesNotActivateSourceOrSupersession(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-rejected-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-rejected-owner")
	ledger := NewLedgerRepository(appDB, rls)
	target := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "remember-rejected-target", "Old source evidence.")
	terminalizeRememberFixtureIngest(t, ctx, ledger, teamID, ownerID, target, "remember-rejected-target-worker")

	staged, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID:            teamID,
		OwnerProfileID:    ownerID,
		IdempotencyKey:    "remember-rejected-batch",
		RequestHash:       "remember-rejected-request",
		TelemetryRemember: true,
		Evidence: []EvidenceInput{{
			Content:                   "Rejected replacement evidence.",
			SourceType:                "document",
			SourceKey:                 "doc://remember-rejected",
			SourceRevisionToken:       "rev-1",
			SourceRevisionContentHash: "sha256:rejected-source",
			SupersedesEvidenceIDs:     []string{target.Evidence[0].FragmentID},
		}},
	})
	require.NoError(t, err)
	claimed, err := ledger.ClaimNextPlacementRun(ctx, teamID, "remember-rejected-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	_, err = ledger.CompleteSubmissionAssessment(ctx, CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID:           teamID,
			OwnerProfileID:   ownerID,
			IngestID:         staged.IngestID,
			PlacementRunID:   staged.PlacementRunID,
			WorkerID:         "remember-rejected-worker",
			ExpectedAttempts: claimed.Attempts,
		},
		OutcomeKind: "submission_assessment_rejected",
		Status:      string(domain.SemanticReviewRejected),
		Category:    "rejected",
		Payload:     map[string]any{"failure_code": "no_supported_memory"},
	})
	require.NoError(t, err)

	var sourceID, sourceRevisionID sql.NullString
	var lifecycleEventCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT source_id::text, source_revision_id::text
			FROM remember_source_revision_intents
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, staged.IngestID).Row().Scan(&sourceID, &sourceRevisionID); err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COUNT(*) FROM evidence_lifecycle_events
			WHERE team_id = ?::uuid AND replacement_fragment_id = ?::uuid
		`, teamID, staged.Evidence[0].FragmentID).Scan(&lifecycleEventCount).Error
	})
	require.NoError(t, err)
	assert.False(t, sourceID.Valid)
	assert.False(t, sourceRevisionID.Valid)
	assert.Zero(t, lifecycleEventCount)
}

func TestRememberFailedSubmissionDoesNotActivateCanonicalOrDerivedState(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-failed-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-failed-owner")
	ledger := NewLedgerRepository(appDB, rls)
	target := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "remember-failed-target", "Old source evidence.")
	terminalizeRememberFixtureIngest(t, ctx, ledger, teamID, ownerID, target, "remember-failed-target-worker")

	staged, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID:            teamID,
		OwnerProfileID:    ownerID,
		IdempotencyKey:    "remember-failed-batch",
		RequestHash:       "remember-failed-request",
		TelemetryRemember: true,
		Evidence: []EvidenceInput{{
			Content:                   "Failed replacement evidence.",
			SourceType:                "document",
			SourceKey:                 "doc://remember-failed",
			SourceRevisionToken:       "rev-1",
			SourceRevisionContentHash: "sha256:failed-source",
			SupersedesEvidenceIDs:     []string{target.Evidence[0].FragmentID},
		}},
	})
	require.NoError(t, err)
	claimed, err := ledger.ClaimNextPlacementRun(ctx, teamID, "remember-failed-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	_, err = ledger.CompleteSubmissionAssessment(ctx, CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID:           teamID,
			OwnerProfileID:   ownerID,
			IngestID:         staged.IngestID,
			PlacementRunID:   staged.PlacementRunID,
			WorkerID:         "remember-failed-worker",
			ExpectedAttempts: claimed.Attempts,
		},
		OutcomeKind: "submission_assessment_terminal",
		Status:      string(domain.SemanticReviewTerminalFailure),
		Category:    "failed",
		Payload:     map[string]any{"failure_code": "provider_response_invalid"},
	})
	require.NoError(t, err)

	var sourceID, sourceRevisionID sql.NullString
	var lifecycleEventCount, searchDocumentCount, embeddingJobCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT source_id::text, source_revision_id::text
			FROM remember_source_revision_intents
			WHERE team_id = ?::uuid AND ingest_id = ?::uuid
		`, teamID, staged.IngestID).Row().Scan(&sourceID, &sourceRevisionID); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM evidence_lifecycle_events
			WHERE team_id = ?::uuid AND replacement_fragment_id = ?::uuid
		`, teamID, staged.Evidence[0].FragmentID).Scan(&lifecycleEventCount).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM search_documents
			WHERE team_id = ?::uuid AND source_id = ?::uuid
		`, teamID, staged.Evidence[0].FragmentID).Scan(&searchDocumentCount).Error; err != nil {
			return err
		}
		return tx.Raw(`
			SELECT COUNT(*) FROM embedding_jobs
			WHERE team_id = ?::uuid AND source_id = ?::uuid
		`, teamID, staged.Evidence[0].FragmentID).Scan(&embeddingJobCount).Error
	})
	require.NoError(t, err)
	assert.False(t, sourceID.Valid)
	assert.False(t, sourceRevisionID.Valid)
	assert.Zero(t, lifecycleEventCount)
	assert.Zero(t, searchDocumentCount)
	assert.Zero(t, embeddingJobCount)
}

func TestRememberRelationshipResultsAreOwnerScopedAndAppendOnly(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "remember-results-team")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-results-owner")
	otherOwnerID := createLedgerProfile(t, adminDB, rls, teamID, "remember-results-other-owner")
	ledger := NewLedgerRepository(appDB, rls)
	staged, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID:            teamID,
		OwnerProfileID:    ownerID,
		IdempotencyKey:    "remember-results-batch",
		RequestHash:       "remember-results-request",
		TelemetryRemember: true,
		Evidence:          []EvidenceInput{{Content: "A supported statement."}},
	})
	require.NoError(t, err)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return insertSubmissionRelationshipResults(ctx, tx, SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID,
			IngestID: staged.IngestID, PlacementRunID: staged.PlacementRunID,
		}, []SubmissionRelationshipResultInput{
			{RelationshipRef: "unsupported", Disposition: "not_stored", Reason: "not_supported_by_evidence"},
			{RelationshipRef: "stale", Disposition: "not_stored", Reason: "stale_input"},
		}, nil)
	})
	require.NoError(t, err)

	var ownerCount, otherOwnerCount, emptyArrayCount int64
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*), COUNT(*) FILTER (WHERE splits = '[]'::jsonb)
			FROM submission_relationship_results
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, staged.PlacementRunID).Row().Scan(&ownerCount, &emptyArrayCount)
	})
	require.NoError(t, err)
	err = rls.WithTeamProfileTx(ctx, appDB, teamID, otherOwnerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT COUNT(*) FROM submission_relationship_results
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, staged.PlacementRunID).Scan(&otherOwnerCount).Error
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), ownerCount)
	assert.Equal(t, int64(2), emptyArrayCount)
	assert.Zero(t, otherOwnerCount)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE submission_relationship_results
			SET reason = 'rewritten'
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, staged.PlacementRunID).Error
	})
	assert.Error(t, err)

	err = rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Exec(`
			DELETE FROM submission_relationship_results
			WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		`, teamID, staged.PlacementRunID).Error
	})
	assert.Error(t, err)
}

func terminalizeRememberFixtureIngest(
	t *testing.T,
	ctx context.Context,
	ledger *LedgerRepositoryImpl,
	teamID string,
	ownerID string,
	ingest *CreateIngestResult,
	workerID string,
) {
	t.Helper()
	claimed, err := ledger.ClaimNextPlacementRun(ctx, teamID, workerID, time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, ingest.IngestID, claimed.IngestID)
	_, err = ledger.CompleteSubmissionAssessment(ctx, CompleteSubmissionAssessmentInput{
		SubmissionAssessmentRunScope: SubmissionAssessmentRunScope{
			TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
			PlacementRunID: ingest.PlacementRunID, WorkerID: workerID, ExpectedAttempts: claimed.Attempts,
		},
		OutcomeKind: "remember_fixture_terminal",
		Status:      string(domain.SemanticReviewTerminalFailure),
		Category:    "failed",
		Payload:     map[string]any{"failure_code": "fixture_only"},
	})
	require.NoError(t, err)
}
