package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestPrivateMemoryErasureCleansPrivateSemanticDecisionLineage(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "private-semantic-lineage", 3, "exact", "")

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-semantic-lineage"))
	identityID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, identityID, "semantic-lineage", domain.CredentialBindingCredentialPrivate)
	ownerID := target.ID
	sharedSpaceID, err := credentialRepo.GetTeamSharedSpaceID(ctx, teamID)
	require.NoError(t, err)

	privateSubjectID, privateObjectID, correctedObjectID := uuid.New(), uuid.New(), uuid.New()
	ingestID, fragmentID := uuid.New(), uuid.New()
	content := "Private semantic lineage evidence."
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, teamID.String()); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO entity_records (team_id, entity_id, entity_kind, space_id)
			VALUES (?, ?, 'project', ?), (?, ?, 'product', ?), (?, ?, 'product', ?)
		`, teamID, privateSubjectID, target.MemorySpaceID, teamID, privateObjectID, target.MemorySpaceID, teamID, correctedObjectID, target.MemorySpaceID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO knowledge_ingests (
				team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
				source_summary, status, proposal, metadata, space_id
			) VALUES (?, ?, ?, ?, ?, ?, 'queued', '{}'::jsonb, '{}'::jsonb, ?)
		`, teamID, ingestID, ownerID, "private-semantic-lineage-"+ingestID.String(), "hash-"+ingestID.String(), content, target.MemorySpaceID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO evidence_fragments (
				team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
				content, content_hash, source_type, authority, labels, metadata, space_id
			) VALUES (?, ?, ?, ?, 0, ?, ?, 'conversation', 'primary', ARRAY[]::text[], '{}'::jsonb, ?)
		`, teamID, fragmentID, ingestID, ownerID, content, "hash-"+fragmentID.String(), target.MemorySpaceID).Error
	}))
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			INSERT INTO remember_attempts (
				team_id, attempt_id, owner_profile_id, space_id, space_generation,
				idempotency_key, request_hash, contract_version, submission_kind,
				outcome, public_result, completed_at
			) VALUES (
				?, ?, ?, ?, (SELECT generation FROM memory_spaces WHERE id = ?),
				'private-semantic-attempt', 'private-semantic-request', 'dense-mem.v2.6.1', 'remember',
				'completed', '{}'::jsonb, now()
			)
		`, teamID, ingestID, ownerID, target.MemorySpaceID, target.MemorySpaceID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO semantic_assessments (
				team_id, attempt_id, owner_profile_id, response_history,
				accepted_revision, provider_turns, model, response_hash, validated_at
			) VALUES (?, ?, ?, '[]'::jsonb, 1, 1, 'test-model', 'sha256:private-assessment', now())
		`, teamID, ingestID, ownerID).Error
	}))

	semantic := NewSemanticRepository(appDB, rls)
	semanticCtx := requestctx.WithActor(ctx, requestctx.Actor{
		TeamID:  teamID,
		OwnerID: ownerID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: sharedSpaceID, Kind: domain.MemorySpaceTeamShared},
			{ID: target.MemorySpaceID, Kind: domain.MemorySpaceCredentialPrivate},
		},
	})
	decision, err := semantic.ApplyRelationshipDecision(semanticCtx, ApplyRelationshipDecisionInput{
		TeamID:            teamID.String(),
		OwnerProfileID:    ownerID.String(),
		IngestID:          ingestID.String(),
		SubjectEntityID:   privateSubjectID.String(),
		PredicateKey:      "uses",
		PredicateVersion:  1,
		OriginalPredicate: "uses",
		SubjectRef:        "Private subject",
		ObjectRef:         "Private object",
		ObjectEntityID:    privateObjectID.String(),
		Polarity:          "+",
		EvidenceVerdict:   string(domain.VerificationEntailed),
		Rationale:         "private semantic lineage regression",
		Model:             "test",
		ResponseHash:      "hash-private-semantic-lineage",
		Support: &EvidenceSupportInput{
			FragmentID:     fragmentID.String(),
			SourceGroupKey: "private-semantic-lineage",
			SpanStart:      0,
			SpanEnd:        len(content),
			Quote:          content,
			Authority:      "primary",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, decision.Relationship)
	relationshipID := decision.Relationship.RelationshipID
	require.Equal(t, target.MemorySpaceID.String(), decision.Relationship.SpaceID)

	correctionInput := CorrectRelationshipInput{
		TeamID:          teamID.String(),
		OwnerProfileID:  ownerID.String(),
		Action:          "submit",
		RelationshipID:  relationshipID,
		ExpectedVersion: decision.Relationship.Version,
		Patch: RelationshipCorrectionPatch{
			ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctedObjectID.String()},
		},
		Supports:       []RelationshipCorrectionSupport{{EvidenceID: fragmentID.String(), Start: 0, End: len(content)}},
		Reason:         "private correction lineage regression",
		IdempotencyKey: "private-correction-lineage",
	}
	correctionPlan, err := semantic.PlanRelationshipCorrectionEmbeddings(semanticCtx, correctionInput)
	require.NoError(t, err)
	require.Len(t, correctionPlan.Documents, 2)
	correction, err := semantic.CorrectRelationshipWithEmbeddings(semanticCtx, correctionInput, []InlineEmbeddingResult{{
		DocumentHash: correctionPlan.Documents[0].DocumentHash, Embedding: []float32{1, 0, 0},
		EmbeddingContractID: correctionPlan.EmbeddingContractID, EmbeddingDimensions: correctionPlan.EmbeddingDimensions,
		EmbeddingModel: correctionPlan.EmbeddingModel, SearchIndexGenerationID: correctionPlan.SearchIndexGenerationID,
		IndexGeneration: correctionPlan.IndexGeneration,
	}, {
		DocumentHash: correctionPlan.Documents[1].DocumentHash, Embedding: []float32{0, 1, 0},
		EmbeddingContractID: correctionPlan.EmbeddingContractID, EmbeddingDimensions: correctionPlan.EmbeddingDimensions,
		EmbeddingModel: correctionPlan.EmbeddingModel, SearchIndexGenerationID: correctionPlan.SearchIndexGenerationID,
		IndexGeneration: correctionPlan.IndexGeneration,
	}})
	require.NoError(t, err)
	require.Equal(t, "completed", correction.ProcessingState)
	require.NotNil(t, correction.Correction)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		var submissionSpace, eventSpace, crossReferenceSpace string
		if err := tx.Raw(`SELECT space_id::text FROM relationship_correction_submissions WHERE submission_id = ?`, correction.SubmissionID).Row().Scan(&submissionSpace); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT space_id::text FROM relationship_correction_events WHERE submission_id = ?`, correction.SubmissionID).Row().Scan(&eventSpace); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT space_id::text FROM relationship_cross_references WHERE source_relationship_id = ?`, correction.Correction.SuccessorRelationshipID).Row().Scan(&crossReferenceSpace); err != nil {
			return err
		}
		require.Equal(t, target.MemorySpaceID.String(), submissionSpace)
		require.Equal(t, target.MemorySpaceID.String(), eventSpace)
		require.Equal(t, target.MemorySpaceID.String(), crossReferenceSpace)
		return nil
	}))

	var dependentSpaces []string
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		rows, err := tx.Raw(`
			SELECT space_id::text FROM relationship_observations WHERE observation_id = ?
			UNION ALL
			SELECT space_id::text FROM verification_events WHERE verification_event_id = ?
			UNION ALL
			SELECT space_id::text FROM relationship_evidence_supports WHERE relationship_id = ?
			UNION ALL
			SELECT space_id::text FROM relationship_support_decision_events WHERE relationship_id = ?
			UNION ALL
			SELECT space_id::text FROM relationship_transition_events WHERE relationship_id = ?
		`, decision.ObservationID, decision.VerificationEventID, relationshipID, relationshipID, relationshipID).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var spaceID string
			if err := rows.Scan(&spaceID); err != nil {
				return err
			}
			dependentSpaces = append(dependentSpaces, spaceID)
		}
		return rows.Err()
	}))
	require.NotEmpty(t, dependentSpaces)
	for _, spaceID := range dependentSpaces {
		require.Equal(t, target.MemorySpaceID.String(), spaceID)
	}

	trace, err := semantic.TraceRelationship(semanticCtx, TraceRelationshipInput{
		TeamID: teamID.String(), RelationshipID: relationshipID, MaxEvents: 20,
	})
	require.NoError(t, err)
	require.NotEmpty(t, trace.Observations)
	require.NotEmpty(t, trace.EvidenceSupports)
	require.NotEmpty(t, trace.VerificationEvents)

	repo := NewPrivateMemoryRepository(appDB, rls)
	require.NoError(t, repo.Prepare(ctx))
	operation, created, err := repo.RequestCredentialErasure(ctx, PrivateMemoryErasureRequest{
		TeamID: teamID, OwnerID: target.ID, CredentialID: target.ID,
		IdempotencyScopeHash: privateMemoryHash("private-semantic-lineage", target.ID.String()),
		RequestHash:          privateMemoryHash("erase-private-semantic-lineage", target.ID.String()),
		ReasonCode:           "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)
	claim, err := repo.ClaimNext(ctx, "private-semantic-lineage-worker", time.Minute)
	require.NoError(t, err)
	require.Equal(t, operation.ID, claim.ID)
	completed, err := repo.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		for _, table := range []string{
			"remember_attempts", "remember_attempt_events", "remember_failure_artifacts", "semantic_assessments",
			"relationship_records", "relationship_observations", "verification_events",
			"relationship_evidence_supports", "relationship_support_decision_events",
			"relationship_transition_events", "relationship_correction_submissions",
			"relationship_correction_events", "relationship_cross_references",
		} {
			var count int64
			query := "SELECT COUNT(*) FROM " + table + " WHERE space_id = ?"
			if table == "remember_attempt_events" || table == "remember_failure_artifacts" || table == "semantic_assessments" {
				query = "SELECT COUNT(*) FROM " + table + " AS child JOIN remember_attempts AS attempt ON child.team_id = attempt.team_id AND child.attempt_id = attempt.attempt_id AND child.owner_profile_id = attempt.owner_profile_id WHERE attempt.space_id = ?"
			}
			if err := tx.Raw(query, target.MemorySpaceID).Scan(&count).Error; err != nil {
				return err
			}
			require.Zero(t, count, table+" rows remain after private erasure")
		}
		return nil
	}))
}
