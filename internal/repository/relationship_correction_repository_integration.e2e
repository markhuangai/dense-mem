package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestRelationshipCorrectionReplacesOwnedRelationshipAndPreservesSupport(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-replace", 3, "exact", "")
	ctx := context.Background()
	teamA := createLedgerTeam(t, adminDB, rls, "relationship-correction-team-a")
	ownerA := createLedgerProfile(t, adminDB, rls, teamA, "owner-a")
	ownerB := createLedgerProfile(t, adminDB, rls, teamA, "owner-b")
	teamC := createLedgerTeam(t, adminDB, rls, "relationship-correction-team-c")
	ownerC := createLedgerProfile(t, adminDB, rls, teamC, "owner-c")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamA, ownerA, "person", "Mark Huang")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamA, ownerA, "project", "Wrong Project")
	correctObject := createSemanticEntity(t, ctx, semantic, teamA, ownerA, "project", "Dense-Mem")
	retiredObject := createSemanticEntity(t, ctx, semantic, teamA, ownerA, "project", "Retired Project")
	crossTeamObject := createSemanticEntity(t, ctx, semantic, teamC, ownerC, "project", "Cross-Team Project")
	ingest := createSemanticIngest(t, ctx, ledger, teamA, ownerA, "relationship-correction-source", "Mark Huang works on Dense-Mem.")
	fragmentID := ingest.Evidence[0].FragmentID
	spanEnd := len("Mark Huang works on Dense-Mem.")
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamA, OwnerProfileID: ownerA, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: fragmentID, SourceGroupKey: "conversation:correction", SpanStart: 0, SpanEnd: spanEnd, Authority: "primary"},
	}).Relationship
	require.NotNil(t, original)

	request := CorrectRelationshipInput{
		TeamID: teamA, OwnerProfileID: ownerA, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: fragmentID, Start: 0, End: spanEnd}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "replace-owned-relationship",
	}
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamA, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE entity_records SET status = 'retired', updated_at = now()
			WHERE team_id = ?::uuid AND entity_id = ?::uuid
		`, teamA, retiredObject.EntityID)
		require.Equal(t, int64(1), result.RowsAffected)
		return result.Error
	}))
	for index, targetID := range []string{uuid.NewString(), retiredObject.EntityID, crossTeamObject.EntityID} {
		unavailable := request
		unavailable.Patch.ObjectEntity = &RelationshipCorrectionEntityPatch{EntityID: targetID}
		unavailable.IdempotencyKey = "reject-unavailable-entity-" + string(rune('a'+index))
		rejected, err := semantic.CorrectRelationship(ctx, unavailable)
		require.NoError(t, err)
		require.Equal(t, "rejected", rejected.ProcessingState, "target index %d id %s", index, targetID)
		require.Equal(t, "entity_not_found", rejected.ErrorCode, "target index %d id %s", index, targetID)
	}

	nonOwner := request
	nonOwner.OwnerProfileID = ownerB
	nonOwner.IdempotencyKey = "replace-other-owner-relationship"
	_, err := semantic.CorrectRelationship(ctx, nonOwner)
	require.ErrorIs(t, err, ErrSemanticOwnerMismatch)

	crossTeam := request
	crossTeam.TeamID = teamC
	crossTeam.OwnerProfileID = ownerC
	crossTeam.IdempotencyKey = "replace-cross-team-relationship"
	_, err = semantic.CorrectRelationship(ctx, crossTeam)
	require.ErrorIs(t, err, ErrSemanticOwnerMismatch)

	missingEmbedding := request
	missingEmbedding.IdempotencyKey = "replace-owned-relationship-missing-embedding"
	_, err = semantic.CorrectRelationship(ctx, missingEmbedding)
	require.ErrorIs(t, err, ErrSearchEmbeddingRequired)
	var originalStateAfterFailure string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamA, ownerA, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT status FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamA, original.RelationshipID).Row().Scan(&originalStateAfterFailure)
	}))
	require.Equal(t, "active", originalStateAfterFailure)

	result, err := correctRelationshipWithTestEmbeddings(ctx, semantic, request)
	require.NoError(t, err)
	require.Equal(t, "completed", result.ProcessingState)
	require.Equal(t, "current", result.SearchState)
	require.NotNil(t, result.Correction)
	require.False(t, result.Correction.ReusedSuccessor)
	require.NotEqual(t, original.RelationshipID, result.Correction.SuccessorRelationshipID)

	replayed, err := semantic.CorrectRelationship(ctx, request)
	require.NoError(t, err)
	require.Equal(t, result.SubmissionID, replayed.SubmissionID)
	require.Equal(t, result.Correction.SuccessorRelationshipID, replayed.Correction.SuccessorRelationshipID)

	status, err := semantic.GetRelationshipCorrection(ctx, GetRelationshipCorrectionInput{
		TeamID: teamA, OwnerProfileID: ownerA, SubmissionID: result.SubmissionID,
	})
	require.NoError(t, err)
	require.Equal(t, "completed", status.ProcessingState)
	require.Equal(t, "current", status.SearchState)

	err = rls.WithTeamProfileTx(ctx, appDB, teamA, ownerA, func(tx *gorm.DB) error {
		var originalStatus, successorStatus string
		var originalSupportCount, successorSupportCount int
		if err := tx.Raw(`
			SELECT status, support_count FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamA, original.RelationshipID).Row().Scan(&originalStatus, &originalSupportCount); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT status, support_count FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamA, result.Correction.SuccessorRelationshipID).Row().Scan(&successorStatus, &successorSupportCount); err != nil {
			return err
		}
		assert.Equal(t, "superseded", originalStatus)
		assert.Equal(t, 1, originalSupportCount)
		assert.Equal(t, "active", successorStatus)
		assert.Equal(t, 1, successorSupportCount)
		var searchState string
		if err := tx.Raw(`
			SELECT search_state FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamA, result.Correction.SuccessorRelationshipID).Row().Scan(&searchState); err != nil {
			return err
		}
		assert.Equal(t, searchState, status.SearchState)
		var crossReferences, correctionEvents int64
		if err := tx.Raw(`
			SELECT COUNT(*) FROM relationship_cross_references
			WHERE team_id = ?::uuid AND source_relationship_id = ?::uuid
			  AND target_relationship_id = ?::uuid AND kind = 'corrects'
		`, teamA, result.Correction.SuccessorRelationshipID, original.RelationshipID).Scan(&crossReferences).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM relationship_correction_events
			WHERE team_id = ?::uuid AND submission_id = ?::uuid
		`, teamA, result.SubmissionID).Scan(&correctionEvents).Error; err != nil {
			return err
		}
		assert.Equal(t, int64(1), crossReferences)
		assert.Equal(t, int64(1), correctionEvents)
		return nil
	})
	require.NoError(t, err)

	freshGraph, err := semantic.SemanticGraph(ctx, SemanticGraphQuery{TeamID: teamA, Limit: 181})
	require.NoError(t, err)
	assert.Contains(t, semanticGraphEdgeIDs(freshGraph.Edges), result.Correction.SuccessorRelationshipID)
	assert.NotContains(t, semanticGraphEdgeIDs(freshGraph.Edges), original.RelationshipID)
	assertGraphHasNode(t, freshGraph.Nodes, "entity", correctObject.EntityID, "Dense-Mem")
	assert.NotContains(t, semanticGraphNodeIDs(freshGraph.Nodes), wrongObject.EntityID)

	otherSubject := createSemanticEntity(t, ctx, semantic, teamA, ownerB, "person", "Other Owner")
	otherContent := "Other Owner works on Wrong Project."
	otherIngest := createSemanticIngest(t, ctx, ledger, teamA, ownerB, "relationship-correction-shared-endpoint", otherContent)
	shared := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamA, OwnerProfileID: ownerB, IngestID: otherIngest.IngestID,
		SubjectEntityID: otherSubject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{
			FragmentID: otherIngest.Evidence[0].FragmentID, SourceGroupKey: "conversation:correction:shared",
			SpanStart: 0, SpanEnd: len(otherContent), Authority: "primary",
		},
	}).Relationship
	require.NotNil(t, shared)

	sharedGraph, err := semantic.SemanticGraph(ctx, SemanticGraphQuery{TeamID: teamA, Limit: 181})
	require.NoError(t, err)
	assert.Contains(t, semanticGraphEdgeIDs(sharedGraph.Edges), shared.RelationshipID)
	assert.NotContains(t, semanticGraphEdgeIDs(sharedGraph.Edges), original.RelationshipID)
	assertGraphHasNode(t, sharedGraph.Nodes, "entity", wrongObject.EntityID, "Wrong Project")

	_, err = semantic.GetRelationshipCorrection(ctx, GetRelationshipCorrectionInput{
		TeamID: teamA, OwnerProfileID: ownerB, SubmissionID: result.SubmissionID,
	})
	require.ErrorIs(t, err, ErrRelationshipCorrectionNotFound)
}

func TestRelationshipCorrectionAmbiguityRequiresOneOwnerConfirmation(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-ambiguity", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-ambiguity")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Mark Huang")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Wrong Project")
	firstAtlas := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Atlas")
	secondAtlas := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Atlas")
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-ambiguous-source", "Mark Huang works on Atlas.")
	fragmentID := ingest.Evidence[0].FragmentID
	spanEnd := len("Mark Huang works on Atlas.")
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: fragmentID, SourceGroupKey: "conversation:ambiguous", SpanStart: 0, SpanEnd: spanEnd, Authority: "primary"},
	}).Relationship

	submitted, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{Name: "Atlas", EntityKind: "project"}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: fragmentID, Start: 0, End: spanEnd}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "ambiguous-submit",
	})
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", submitted.ProcessingState)
	require.NotNil(t, submitted.Confirmation)
	require.Len(t, submitted.Confirmation.Candidates, 2)
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		canceledCtx, cancel := context.WithCancel(ctx)
		cancel()
		validationErr := validateSelectedCorrectionEntities(canceledCtx, tx, teamID, submitted.Confirmation.Candidates, RelationshipCorrectionSelection{ObjectEntityID: firstAtlas.EntityID})
		require.Error(t, validationErr)
		require.NotErrorIs(t, validationErr, errRelationshipCorrectionSelectionUnavailable)
		return nil
	}))

	var originalStatus string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT status FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, original.RelationshipID).Scan(&originalStatus).Error
	}))
	require.Equal(t, "active", originalStatus)

	_, err = semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: submitted.SubmissionID, ConfirmationToken: uuid.NewString(),
		Selection: RelationshipCorrectionSelection{ObjectEntityID: firstAtlas.EntityID}, IdempotencyKey: "invalid-token-confirm",
	})
	require.ErrorIs(t, err, ErrRelationshipCorrectionConfirmation)
	invalidToken, err := semantic.GetRelationshipCorrection(ctx, GetRelationshipCorrectionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: submitted.SubmissionID,
	})
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", invalidToken.ProcessingState)
	require.NotNil(t, invalidToken.Confirmation)
	require.Equal(t, submitted.Confirmation.Token, invalidToken.Confirmation.Token)

	_, err = semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: submitted.SubmissionID, ConfirmationToken: submitted.Confirmation.Token,
		Selection: RelationshipCorrectionSelection{ObjectEntityID: uuid.NewString()}, IdempotencyKey: "invalid-selection-confirm",
	})
	require.ErrorIs(t, err, ErrRelationshipCorrectionConfirmation)
	invalidSelection, err := semantic.GetRelationshipCorrection(ctx, GetRelationshipCorrectionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: submitted.SubmissionID,
	})
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", invalidSelection.ProcessingState)
	require.NotNil(t, invalidSelection.Confirmation)
	require.Equal(t, submitted.Confirmation.Token, invalidSelection.Confirmation.Token)

	confirmed, err := correctRelationshipWithTestEmbeddings(ctx, semantic, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: submitted.SubmissionID, ConfirmationToken: submitted.Confirmation.Token,
		Selection:      RelationshipCorrectionSelection{ObjectEntityID: strings.ToUpper(firstAtlas.EntityID)},
		IdempotencyKey: "ambiguous-confirm",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", confirmed.ProcessingState)
	require.Equal(t, firstAtlas.EntityID, loadRelationshipObjectEntity(t, ctx, appDB, rls, teamID, ownerID, confirmed.Correction.SuccessorRelationshipID))

	_, err = semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: submitted.SubmissionID, ConfirmationToken: submitted.Confirmation.Token,
		Selection:      RelationshipCorrectionSelection{ObjectEntityID: firstAtlas.EntityID},
		IdempotencyKey: "second-confirmation-round",
	})
	require.True(t, errors.Is(err, ErrRelationshipCorrectionConfirmation), "err=%v", err)

	expiredSubject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Expired Confirmation Owner")
	expiredWrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Expired Wrong Project")
	expiredIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-expired-source", "Expired Confirmation Owner works on Atlas.")
	expiredSpan := len("Expired Confirmation Owner works on Atlas.")
	expiredOriginal := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: expiredIngest.IngestID,
		SubjectEntityID: expiredSubject.EntityID, PredicateKey: "works_on", ObjectEntityID: expiredWrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: expiredIngest.Evidence[0].FragmentID, SourceGroupKey: "conversation:expired", SpanStart: 0, SpanEnd: expiredSpan, Authority: "primary"},
	}).Relationship
	expired, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: expiredOriginal.RelationshipID, ExpectedVersion: expiredOriginal.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{Name: "Atlas", EntityKind: "project"}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: expiredIngest.Evidence[0].FragmentID, Start: 0, End: expiredSpan}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "expired-submit",
	})
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", expired.ProcessingState)
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_correction_submissions
			SET confirmation_expires_at = ?
			WHERE team_id = ?::uuid AND submission_id = ?::uuid
		`, time.Now().UTC().Add(-time.Minute), teamID, expired.SubmissionID).Error
	}))
	_, err = semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: expired.SubmissionID, ConfirmationToken: expired.Confirmation.Token,
		Selection: RelationshipCorrectionSelection{ObjectEntityID: firstAtlas.EntityID}, IdempotencyKey: "expired-confirm",
	})
	require.ErrorIs(t, err, ErrRelationshipCorrectionConfirmationExpired)
	expiredStatus, err := semantic.GetRelationshipCorrection(ctx, GetRelationshipCorrectionInput{
		TeamID: teamID, OwnerProfileID: ownerID, SubmissionID: expired.SubmissionID,
	})
	require.NoError(t, err)
	require.Equal(t, "rejected", expiredStatus.ProcessingState)
	require.Equal(t, "confirmation_expired", expiredStatus.ErrorCode)
	_, err = semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: expired.SubmissionID, ConfirmationToken: expired.Confirmation.Token,
		Selection: RelationshipCorrectionSelection{ObjectEntityID: firstAtlas.EntityID}, IdempotencyKey: "expired-confirm",
	})
	require.ErrorIs(t, err, ErrRelationshipCorrectionConfirmationExpired)

	unavailableSubject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Unavailable Selection Owner")
	unavailableWrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Unavailable Wrong Project")
	unavailableIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-unavailable-source", "Unavailable Selection Owner works on Atlas.")
	unavailableSpan := len("Unavailable Selection Owner works on Atlas.")
	unavailableOriginal := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: unavailableIngest.IngestID,
		SubjectEntityID: unavailableSubject.EntityID, PredicateKey: "works_on", ObjectEntityID: unavailableWrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: unavailableIngest.Evidence[0].FragmentID, SourceGroupKey: "conversation:unavailable", SpanStart: 0, SpanEnd: unavailableSpan, Authority: "primary"},
	}).Relationship
	unavailable, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: unavailableOriginal.RelationshipID, ExpectedVersion: unavailableOriginal.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{Name: "Atlas", EntityKind: "project"}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: unavailableIngest.Evidence[0].FragmentID, Start: 0, End: unavailableSpan}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "unavailable-submit",
	})
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", unavailable.ProcessingState)
	require.NoError(t, rls.WithTeamTx(ctx, appDB, teamID, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE entity_records SET status = 'retired', updated_at = now()
			WHERE team_id = ?::uuid AND entity_id = ?::uuid
		`, teamID, secondAtlas.EntityID)
		require.Equal(t, int64(1), result.RowsAffected)
		return result.Error
	}))
	unavailableResult, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: unavailable.SubmissionID, ConfirmationToken: unavailable.Confirmation.Token,
		Selection: RelationshipCorrectionSelection{ObjectEntityID: secondAtlas.EntityID}, IdempotencyKey: "unavailable-confirm",
	})
	require.NoError(t, err)
	require.Equal(t, "rejected", unavailableResult.ProcessingState)
	require.Equal(t, "persistent_ambiguity", unavailableResult.ErrorCode)
}

func TestPrivateMemoryCorrectionStatusHidesSealedGeneration(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()

	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "private-correction-status-generation"))
	identityID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	target := createOwnedCredential(t, credentialRepo, teamID, identityID, "private-correction-status", domain.CredentialBindingCredentialPrivate)
	semantic := NewSemanticRepository(appDB, rls)
	semanticCtx := requestctx.WithActor(ctx, requestctx.Actor{
		TeamID:  teamID,
		OwnerID: target.ID,
		AllowedSpaces: []domain.MemorySpaceAccess{
			{ID: target.TeamSharedSpaceID, Kind: domain.MemorySpaceTeamShared},
			{ID: target.MemorySpaceID, Kind: domain.MemorySpaceCredentialPrivate},
		},
	})

	subjectID, wrongObjectID := uuid.New(), uuid.New()
	firstCandidateID, secondCandidateID := uuid.New(), uuid.New()
	ingestID, fragmentID := uuid.New(), uuid.New()
	content := "Private correction status evidence."
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := seedTeamPredicateDefinitions(ctx, tx, teamID.String()); err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO entity_records (team_id, entity_id, entity_kind, space_id)
			VALUES
			  (?, ?, 'person', ?),
			  (?, ?, 'project', ?),
			  (?, ?, 'project', ?),
			  (?, ?, 'project', ?)
		`, teamID, subjectID, target.MemorySpaceID,
			teamID, wrongObjectID, target.MemorySpaceID,
			teamID, firstCandidateID, target.MemorySpaceID,
			teamID, secondCandidateID, target.MemorySpaceID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO entity_names (
			    team_id, entity_id, owner_profile_id, display_name,
			    normalized_name, name_kind, space_id
			) VALUES
			  (?, ?, ?, 'Atlas', 'atlas', 'canonical', ?),
			  (?, ?, ?, 'Atlas', 'atlas', 'canonical', ?)
		`, teamID, firstCandidateID, target.ID, target.MemorySpaceID,
			teamID, secondCandidateID, target.ID, target.MemorySpaceID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`
			INSERT INTO knowledge_ingests (
			    team_id, ingest_id, owner_profile_id, idempotency_key, request_hash,
			    source_summary, status, proposal, metadata, space_id, space_generation
			) VALUES (?, ?, ?, ?, ?, ?, 'queued', '{}'::jsonb, '{}'::jsonb, ?, ?)
		`, teamID, ingestID, target.ID, "private-correction-status-"+ingestID.String(),
			"hash-"+ingestID.String(), content, target.MemorySpaceID, target.MemorySpaceGeneration).Error; err != nil {
			return err
		}
		return tx.Exec(`
			INSERT INTO evidence_fragments (
			    team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
			    content, content_hash, source_type, authority, labels, metadata,
			    space_id, space_generation
			) VALUES (?, ?, ?, ?, 0, ?, ?, 'conversation', 'primary', ARRAY[]::text[], '{}'::jsonb, ?, ?)
		`, teamID, fragmentID, ingestID, target.ID, content, "hash-"+fragmentID.String(),
			target.MemorySpaceID, target.MemorySpaceGeneration).Error
	}))

	decision, err := semantic.ApplyRelationshipDecision(semanticCtx, ApplyRelationshipDecisionInput{
		TeamID:            teamID.String(),
		OwnerProfileID:    target.ID.String(),
		IngestID:          ingestID.String(),
		SubjectEntityID:   subjectID.String(),
		PredicateKey:      "works_on",
		PredicateVersion:  1,
		OriginalPredicate: "works_on",
		SubjectRef:        "Private subject",
		ObjectRef:         "Wrong project",
		ObjectEntityID:    wrongObjectID.String(),
		Polarity:          "+",
		EvidenceVerdict:   string(domain.VerificationEntailed),
		Rationale:         "private correction status generation regression",
		Model:             "test",
		ResponseHash:      "hash-private-correction-status",
		Support: &EvidenceSupportInput{
			FragmentID:     fragmentID.String(),
			SourceGroupKey: "private-correction-status-generation",
			SpanStart:      0,
			SpanEnd:        len(content),
			Quote:          content,
			Authority:      "primary",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, decision.Relationship)

	submitted, err := semantic.CorrectRelationship(semanticCtx, CorrectRelationshipInput{
		TeamID: teamID.String(), OwnerProfileID: target.ID.String(), Action: "submit",
		RelationshipID: decision.Relationship.RelationshipID, ExpectedVersion: decision.Relationship.Version,
		Patch: RelationshipCorrectionPatch{
			ObjectEntity: &RelationshipCorrectionEntityPatch{Name: "Atlas", EntityKind: "project"},
		},
		Supports:       []RelationshipCorrectionSupport{{EvidenceID: fragmentID.String(), Start: 0, End: len(content)}},
		Reason:         "the object Entity was resolved incorrectly",
		IdempotencyKey: "private-correction-status-submit",
	})
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", submitted.ProcessingState)
	require.NotNil(t, submitted.Confirmation)
	require.Len(t, submitted.Confirmation.Candidates, 2)

	status, err := semantic.GetRelationshipCorrection(semanticCtx, GetRelationshipCorrectionInput{
		TeamID: teamID.String(), OwnerProfileID: target.ID.String(), SubmissionID: submitted.SubmissionID,
	})
	require.NoError(t, err)
	require.Equal(t, submitted.Confirmation.Token, status.Confirmation.Token)

	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		result := tx.Exec(`
			UPDATE memory_spaces
			SET generation = generation + 1, lifecycle_state = 'sealed', sealed_at = now()
			WHERE id = ? AND team_id = ? AND lifecycle_state = 'active'
		`, target.MemorySpaceID, teamID)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("seal private correction status space: updated %d rows", result.RowsAffected)
		}
		var retained int64
		if err := tx.Raw(`
			SELECT COUNT(*)
			FROM relationship_correction_submissions
			WHERE team_id = ? AND submission_id = ? AND space_id = ?
		`, teamID, submitted.SubmissionID, target.MemorySpaceID).Row().Scan(&retained); err != nil {
			return err
		}
		if retained != 1 {
			return fmt.Errorf("expected retained private correction submission, got %d", retained)
		}
		return nil
	}))

	_, err = semantic.GetRelationshipCorrection(semanticCtx, GetRelationshipCorrectionInput{
		TeamID: teamID.String(), OwnerProfileID: target.ID.String(), SubmissionID: submitted.SubmissionID,
	})
	require.ErrorIs(t, err, ErrRelationshipCorrectionNotFound)
}

func TestRelationshipCorrectionReusesActiveCollisionAndRejectsNoOp(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-collision", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-collision")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Mark Huang")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Wrong Project")
	targetObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Dense-Mem")
	sourceIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "collision-source", "Mark works on the wrong project.")
	targetIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "collision-target", "Mark works on Dense-Mem.")
	sourceSpan := len("Mark works on the wrong project.")
	targetSpan := len("Mark works on Dense-Mem.")
	source := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: sourceIngest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: sourceIngest.Evidence[0].FragmentID, SourceGroupKey: "collision:source", SpanStart: 0, SpanEnd: sourceSpan, Authority: "primary"},
	}).Relationship
	target := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: targetIngest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: targetObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: targetIngest.Evidence[0].FragmentID, SourceGroupKey: "collision:target", SpanStart: 0, SpanEnd: targetSpan, Authority: "primary"},
	}).Relationship

	reused, err := correctRelationshipWithTestEmbeddings(ctx, semantic, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: source.RelationshipID, ExpectedVersion: source.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: targetObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: sourceIngest.Evidence[0].FragmentID, Start: 0, End: sourceSpan}},
		Reason:   "replace with the already active Relationship", IdempotencyKey: "reuse-active-collision",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", reused.ProcessingState)
	require.True(t, reused.Correction.ReusedSuccessor)
	require.Equal(t, target.RelationshipID, reused.Correction.SuccessorRelationshipID)

	var supportCount int
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT support_count FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, target.RelationshipID).Scan(&supportCount).Error
	}))
	require.Equal(t, 2, supportCount)

	noOp, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: target.RelationshipID, ExpectedVersion: reused.Correction.SuccessorVersion,
		Patch: RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: targetObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{
			{EvidenceID: sourceIngest.Evidence[0].FragmentID, Start: 0, End: sourceSpan},
			{EvidenceID: targetIngest.Evidence[0].FragmentID, Start: 0, End: targetSpan},
		},
		Reason: "this should be rejected as a no-op", IdempotencyKey: "reject-no-op",
	})
	require.NoError(t, err)
	require.Equal(t, "rejected", noOp.ProcessingState)
	require.Equal(t, "no_change", noOp.ErrorCode)

	inactiveSubject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Inactive Collision Owner")
	inactiveWrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Inactive Wrong Project")
	inactiveTargetObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Inactive Correct Project")
	inactiveTargetIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "inactive-collision-target", "Inactive Collision Owner works on Inactive Correct Project.")
	inactiveSourceIngest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "inactive-collision-source", "Inactive Collision Owner works on Inactive Wrong Project.")
	inactiveTargetSpan := len("Inactive Collision Owner works on Inactive Correct Project.")
	inactiveSourceSpan := len("Inactive Collision Owner works on Inactive Wrong Project.")
	inactiveTarget := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: inactiveTargetIngest.IngestID,
		SubjectEntityID: inactiveSubject.EntityID, PredicateKey: "works_on", ObjectEntityID: inactiveTargetObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: inactiveTargetIngest.Evidence[0].FragmentID, SourceGroupKey: "inactive-collision:target", SpanStart: 0, SpanEnd: inactiveTargetSpan, Authority: "primary"},
	}).Relationship
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE relationship_records SET status = 'superseded', updated_at = now()
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, inactiveTarget.RelationshipID).Error
	}))
	inactiveSource := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: inactiveSourceIngest.IngestID,
		SubjectEntityID: inactiveSubject.EntityID, PredicateKey: "works_on", ObjectEntityID: inactiveWrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: inactiveSourceIngest.Evidence[0].FragmentID, SourceGroupKey: "inactive-collision:source", SpanStart: 0, SpanEnd: inactiveSourceSpan, Authority: "primary"},
	}).Relationship

	inactiveCollision, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: inactiveSource.RelationshipID, ExpectedVersion: inactiveSource.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: inactiveTargetObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: inactiveSourceIngest.Evidence[0].FragmentID, Start: 0, End: inactiveSourceSpan}},
		Reason:   "inactive history must not be revived", IdempotencyKey: "reject-inactive-collision",
	})
	require.NoError(t, err)
	require.Equal(t, "rejected", inactiveCollision.ProcessingState)
	require.Equal(t, "inactive_relationship_collision", inactiveCollision.ErrorCode)
}

func TestRelationshipCorrectionPlannerKeepsStoredResolvedSelection(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-mixed-selection", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-mixed-selection")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	wrongSubject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Wrong Subject")
	resolvedSubject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Resolved Subject")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Wrong Project")
	firstAtlas := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Atlas")
	createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Atlas")
	content := "Resolved Subject works on Atlas."
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-mixed-selection", content)
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: wrongSubject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "mixed-selection", SpanStart: 0, SpanEnd: len(content), Authority: "primary"},
	}).Relationship

	submitted, err := semantic.CorrectRelationship(ctx, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch: RelationshipCorrectionPatch{
			SubjectEntity: &RelationshipCorrectionEntityPatch{Name: "Resolved Subject", EntityKind: "person"},
			ObjectEntity:  &RelationshipCorrectionEntityPatch{Name: "Atlas", EntityKind: "project"},
		},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(content)}},
		Reason:   "both endpoints were resolved incorrectly", IdempotencyKey: "mixed-selection-submit",
	})
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", submitted.ProcessingState)
	require.NotNil(t, submitted.Confirmation)
	require.Len(t, submitted.Confirmation.Candidates, 2)

	confirmed, err := correctRelationshipWithTestEmbeddings(ctx, semantic, CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "confirm",
		SubmissionID: submitted.SubmissionID, ConfirmationToken: submitted.Confirmation.Token,
		Selection: RelationshipCorrectionSelection{ObjectEntityID: firstAtlas.EntityID}, IdempotencyKey: "mixed-selection-confirm",
	})
	require.NoError(t, err)
	require.Equal(t, "completed", confirmed.ProcessingState)
	require.Equal(t, firstAtlas.EntityID, loadRelationshipObjectEntity(t, ctx, appDB, rls, teamID, ownerID, confirmed.Correction.SuccessorRelationshipID))

	var successorSubjectID string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT subject_entity_id::text FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, confirmed.Correction.SuccessorRelationshipID).Scan(&successorSubjectID).Error
	}))
	require.Equal(t, resolvedSubject.EntityID, successorSubjectID)
}

func TestRelationshipCorrectionRejectsSearchContractRotationAfterPlan(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-rotation", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-rotation")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Rotation Owner")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Rotation Wrong")
	correctObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Rotation Correct")
	content := "Rotation Owner works on Rotation Correct."
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-rotation", content)
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "rotation", SpanStart: 0, SpanEnd: len(content), Authority: "primary"},
	}).Relationship
	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(content)}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "rotation-submit",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Documents)
	sourceDocument, err := NewSearchRepository(appDB, rls).UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: original.RelationshipID,
		SourceVersion: int64(original.Version), ProjectionFormat: 2, DocumentText: plan.Documents[0].DocumentText,
		SpaceID: original.SpaceID, SpaceGeneration: original.SpaceGeneration,
	})
	require.NoError(t, err)
	require.Equal(t, "pending", sourceDocument.SearchState)

	var beforeSearchState string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT search_state FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, original.RelationshipID).Scan(&beforeSearchState).Error; err != nil {
			return err
		}
		return nil
	}))

	insertSearchTestContract(t, adminDB, rls, "relationship-correction-rotation-next", 3, "exact", "")
	_, err = semantic.CorrectRelationshipWithEmbeddings(ctx, input, relationshipCorrectionTestEmbeddings(plan))
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	var originalStatus, afterSearchState string
	var activeRelationships int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT status FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, original.RelationshipID).Scan(&originalStatus).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT COUNT(*) FROM relationship_records
			WHERE team_id = ?::uuid AND status = 'active'
		`, teamID).Scan(&activeRelationships).Error; err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT search_state FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, original.RelationshipID).Scan(&afterSearchState).Error; err != nil {
			return err
		}
		return nil
	}))
	require.Equal(t, "active", originalStatus)
	require.Equal(t, int64(1), activeRelationships)
	require.Equal(t, beforeSearchState, afterSearchState)
}

func TestRelationshipCorrectionRejectsInvalidEmbeddingAtomically(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-invalid-embedding", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-invalid-embedding")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Invalid Embedding Owner")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Invalid Embedding Wrong")
	correctObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Invalid Embedding Correct")
	content := "Invalid Embedding Owner works on Invalid Embedding Correct."
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-invalid-embedding", content)
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "invalid-embedding", SpanStart: 0, SpanEnd: len(content), Authority: "primary"},
	}).Relationship
	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(content)}},
		Reason:   "the object Entity was resolved incorrectly", IdempotencyKey: "invalid-embedding-submit",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Documents)
	sourceDocument, err := NewSearchRepository(appDB, rls).UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: original.RelationshipID,
		SourceVersion: int64(original.Version), ProjectionFormat: 2, DocumentText: plan.Documents[0].DocumentText,
		SpaceID: original.SpaceID, SpaceGeneration: original.SpaceGeneration,
	})
	require.NoError(t, err)
	require.Equal(t, "pending", sourceDocument.SearchState)

	embeddings := relationshipCorrectionTestEmbeddings(plan)
	successorHash := plan.Documents[len(plan.Documents)-1].DocumentHash
	invalidated := false
	for index := range embeddings {
		if embeddings[index].DocumentHash == successorHash {
			embeddings[index].Embedding = []float32{0}
			invalidated = true
		}
	}
	require.True(t, invalidated)

	var beforeSearchState, beforeDocumentHash string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT search_state, document_hash FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, original.RelationshipID).Row().Scan(&beforeSearchState, &beforeDocumentHash); err != nil {
			return err
		}
		return nil
	}))

	_, err = semantic.CorrectRelationshipWithEmbeddings(ctx, input, embeddings)
	require.ErrorIs(t, err, ErrSearchContractMismatch)

	var afterSearchState, afterDocumentHash, originalStatus string
	var relationshipCount int64
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT search_state, document_hash FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, original.RelationshipID).Row().Scan(&afterSearchState, &afterDocumentHash); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT COUNT(*) FROM relationship_records WHERE team_id = ?::uuid`, teamID).Scan(&relationshipCount).Error; err != nil {
			return err
		}
		return tx.Raw(`SELECT status FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, original.RelationshipID).Scan(&originalStatus).Error
	}))
	require.Equal(t, "active", originalStatus)
	require.Equal(t, beforeSearchState, afterSearchState)
	require.Equal(t, beforeDocumentHash, afterDocumentHash)
	require.Equal(t, int64(1), relationshipCount)
}

func TestRelationshipCorrectionEqualHashUsesOneEmbeddingForBothDocumentStates(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	insertSearchTestContract(t, adminDB, rls, "relationship-correction-equal-hash", 3, "exact", "")
	ctx := context.Background()
	teamID := createLedgerTeam(t, adminDB, rls, "relationship-correction-equal-hash")
	ownerID := createLedgerProfile(t, adminDB, rls, teamID, "owner")
	ledger := NewLedgerRepository(appDB, rls)
	semantic := NewSemanticRepository(appDB, rls)

	subject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "person", "Equal Hash Owner")
	wrongObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Atlas")
	correctObject := createSemanticEntity(t, ctx, semantic, teamID, ownerID, "project", "Atlas")
	content := "Equal Hash Owner works on Atlas."
	ingest := createSemanticIngest(t, ctx, ledger, teamID, ownerID, "relationship-correction-equal-hash", content)
	original := applySemanticDecision(t, ctx, semantic, ApplyRelationshipDecisionInput{
		TeamID: teamID, OwnerProfileID: ownerID, IngestID: ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "equal-hash", SpanStart: 0, SpanEnd: len(content), Authority: "primary"},
	}).Relationship
	input := CorrectRelationshipInput{
		TeamID: teamID, OwnerProfileID: ownerID, Action: "submit",
		RelationshipID: original.RelationshipID, ExpectedVersion: original.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: correctObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(content)}},
		Reason:   "the object Entity identity was resolved incorrectly", IdempotencyKey: "equal-hash-submit",
	}
	plan, err := semantic.PlanRelationshipCorrectionEmbeddings(ctx, input)
	require.NoError(t, err)
	require.Len(t, plan.Documents, 1)
	sourceDocument, err := NewSearchRepository(appDB, rls).UpsertSearchDocument(ctx, UpsertSearchDocumentInput{
		TeamID: teamID, OwnerProfileID: ownerID, SourceKind: "relationship", SourceID: original.RelationshipID,
		SourceVersion: int64(original.Version), ProjectionFormat: 2, DocumentText: plan.Documents[0].DocumentText,
		SpaceID: original.SpaceID, SpaceGeneration: original.SpaceGeneration,
	})
	require.NoError(t, err)
	require.Equal(t, "pending", sourceDocument.SearchState)

	result, err := semantic.CorrectRelationshipWithEmbeddings(ctx, input, relationshipCorrectionTestEmbeddings(plan))
	require.NoError(t, err)
	require.Equal(t, "completed", result.ProcessingState)

	var originalSearchState, successorSearchState, originalHash, successorHash string
	require.NoError(t, rls.WithTeamProfileTx(ctx, appDB, teamID, ownerID, func(tx *gorm.DB) error {
		if err := tx.Raw(`
			SELECT search_state, document_hash FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, original.RelationshipID).Row().Scan(&originalSearchState, &originalHash); err != nil {
			return err
		}
		if err := tx.Raw(`
			SELECT search_state, document_hash FROM search_documents
			WHERE team_id = ?::uuid AND source_kind = 'relationship' AND source_id = ?::uuid
			ORDER BY updated_at DESC LIMIT 1
		`, teamID, result.Correction.SuccessorRelationshipID).Row().Scan(&successorSearchState, &successorHash); err != nil {
			return err
		}
		return nil
	}))
	require.Equal(t, "not_required", originalSearchState)
	require.Equal(t, "current", successorSearchState)
	require.Equal(t, plan.Documents[0].DocumentHash, originalHash)
	require.Equal(t, plan.Documents[0].DocumentHash, successorHash)
}

func loadRelationshipObjectEntity(
	t *testing.T,
	ctx context.Context,
	db *gorm.DB,
	rls interface {
		WithTeamProfileTx(context.Context, *gorm.DB, string, string, func(*gorm.DB) error) error
	},
	teamID, ownerID, relationshipID string,
) string {
	t.Helper()
	var entityID string
	require.NoError(t, rls.WithTeamProfileTx(ctx, db, teamID, ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`
			SELECT object_entity_id::text FROM relationship_records
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, teamID, relationshipID).Scan(&entityID).Error
	}))
	return entityID
}

func TestCorrectRelationshipValidationRejectsInvalidConfirmation(t *testing.T) {
	input := normalizeCorrectRelationshipInput(CorrectRelationshipInput{
		TeamID: uuid.NewString(), OwnerProfileID: uuid.NewString(), Action: "confirm",
		SubmissionID: uuid.NewString(), ConfirmationToken: uuid.NewString(), IdempotencyKey: "confirm",
	})
	err := validateCorrectRelationshipInput(input)
	require.ErrorContains(t, err, "selection")
}

func TestNormalizeCorrectRelationshipInputCanonicalizesWithoutMutatingCaller(t *testing.T) {
	teamID := uuid.NewString()
	profileID := uuid.NewString()
	relationshipID := uuid.NewString()
	entityID := uuid.NewString()
	firstEvidenceID := uuid.NewString()
	secondEvidenceID := uuid.NewString()
	callerSupports := []RelationshipCorrectionSupport{
		{EvidenceID: strings.ToUpper(secondEvidenceID), Start: 2, End: 3},
		{EvidenceID: strings.ToUpper(firstEvidenceID), Start: 0, End: 1},
	}
	callerPatch := &RelationshipCorrectionEntityPatch{EntityID: " " + strings.ToUpper(entityID) + " "}
	input := CorrectRelationshipInput{
		TeamID: strings.ToUpper(teamID), OwnerProfileID: strings.ToUpper(profileID), Action: " submit ",
		RelationshipID: strings.ToUpper(relationshipID), Patch: RelationshipCorrectionPatch{ObjectEntity: callerPatch},
		Supports: callerSupports,
	}

	normalized := normalizeCorrectRelationshipInput(input)

	require.Equal(t, teamID, normalized.TeamID)
	require.Equal(t, profileID, normalized.OwnerProfileID)
	require.Equal(t, relationshipID, normalized.RelationshipID)
	require.Equal(t, entityID, normalized.Patch.ObjectEntity.EntityID)
	require.ElementsMatch(t, []string{firstEvidenceID, secondEvidenceID}, []string{normalized.Supports[0].EvidenceID, normalized.Supports[1].EvidenceID})
	require.Equal(t, strings.ToUpper(secondEvidenceID), callerSupports[0].EvidenceID)
	require.Equal(t, " "+strings.ToUpper(entityID)+" ", callerPatch.EntityID)
}
