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

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-semantic-lineage"))
	identityID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, identityID, "semantic-lineage", domain.CredentialBindingCredentialPrivate)
	ownerID := target.ID
	sharedSpaceID, err := credentialRepo.GetTeamSharedSpaceID(ctx, teamID)
	require.NoError(t, err)

	privateSubjectID, privateObjectID := uuid.New(), uuid.New()
	relationshipID, ingestID, fragmentID := uuid.New(), uuid.New(), uuid.New()
	content := "Private semantic lineage evidence."
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, teamID.String()); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO entity_records (team_id, entity_id, entity_kind, space_id)
			VALUES (?, ?, 'project', ?), (?, ?, 'product', ?)
		`, teamID, privateSubjectID, target.MemorySpaceID, teamID, privateObjectID, target.MemorySpaceID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO relationship_records (
				team_id, relationship_id, owner_profile_id, semantic_group_key,
				subject_entity_id, predicate_key, predicate_version, object_entity_id,
				relationship_kind, current_cardinality, status, support_count,
				source_group_count, space_id
			) VALUES (?, ?, ?, 'private-semantic-lineage', ?, 'uses', 1, ?,
				'state', 'many', 'active', 0, 0, ?)
		`, teamID, relationshipID, ownerID, privateSubjectID, privateObjectID, target.MemorySpaceID).Error; err != nil {
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
	require.Equal(t, relationshipID.String(), decision.Relationship.RelationshipID)
	require.Equal(t, target.MemorySpaceID.String(), decision.Relationship.SpaceID)

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
		TeamID: teamID.String(), RelationshipID: relationshipID.String(), MaxEvents: 20,
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
			"relationship_records", "relationship_observations", "verification_events",
			"relationship_evidence_supports", "relationship_support_decision_events",
			"relationship_transition_events",
		} {
			var count int64
			if err := tx.Raw("SELECT COUNT(*) FROM "+table+" WHERE space_id = ?", target.MemorySpaceID).Scan(&count).Error; err != nil {
				return err
			}
			require.Zero(t, count, table+" rows remain after private erasure")
		}
		return nil
	}))
}
