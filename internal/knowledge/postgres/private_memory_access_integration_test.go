//go:build integration

package postgres

import (
	"context"
	privacy "github.com/markhuangai/dense-mem/internal/privacy/postgres"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func privateCredentialContext(key *domain.Credential) context.Context {
	return requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: key.TeamID, IdentityID: key.ActorIdentityID, MembershipID: key.MembershipID, OwnerID: key.OwnerID, CredentialID: &key.ID, AuthMethod: "api_key", Grants: key.Scopes, AllowedSpaces: key.MemorySpaces()})
}

func TestPrivateMemoryReadOnlyCommitRetractionAndLiveSharedRevocation(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	team := createLedgerTeam(t, admin, rls, "private-access")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	other := createLedgerProfile(t, admin, rls, team, "other")
	insertSearchTestContract(t, admin, rls, "private-access", 3, "exact", "")
	keys := access.NewCredentialRepository(app, rls, nil)
	key, err := keys.GetByIDForTeam(ctx, uuid.MustParse(team), uuid.MustParse(owner))
	require.NoError(t, err)
	otherKey, err := keys.GetByIDForTeam(ctx, uuid.MustParse(team), uuid.MustParse(other))
	require.NoError(t, err)
	originalContext := privateCredentialContext(key)
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	shared := evidenceOnlyRememberInput(team, owner, "before-revocation-shared")
	shared.SpaceID, shared.SpaceGeneration = key.TeamSharedSpaceID.String(), key.TeamSharedSpaceGeneration
	shared.Metadata = map[string]any{"target_space": "team_shared"}
	prepared, err := store.PlanRememberEmbeddings(originalContext, shared)
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE credentials SET scopes=ARRAY['read'] WHERE id=?`, key.ID).Error
	}))
	_, err = store.CommitRememberWithEmbeddings(originalContext, shared, rememberTestEmbeddings(prepared, false))
	require.ErrorIs(t, err, domain.ErrMemoryWriteDenied)
	for _, count := range rememberPrimitiveCounts(t, ctx, admin, rls, team, shared.IngestID) {
		require.Zero(t, count)
	}
	private := evidenceOnlyRememberInput(team, owner, "read-only-private")
	private.SpaceID, private.SpaceGeneration = key.MemorySpaceID.String(), key.MemorySpaceGeneration
	private.Metadata = map[string]any{"target_space": "private"}
	key.Scopes = []string{"read"}
	privateContext := privateCredentialContext(key)
	prepared, err = store.PlanRememberEmbeddings(privateContext, private)
	require.NoError(t, err)
	stored, err := store.CommitRememberWithEmbeddings(privateContext, private, rememberTestEmbeddings(prepared, false))
	require.NoError(t, err)
	require.Equal(t, "private", stored.PublicResult["space_kind"])
	visible, err := store.ListSubmissionAssessmentKnownEvidence(privateContext, SubmissionAssessmentKnownEvidenceInput{TeamID: team, OwnerProfileID: owner, SpaceID: private.SpaceID, EvidenceIDs: []string{private.Evidence[0].FragmentID}})
	require.NoError(t, err)
	require.Len(t, visible.Evidence, 1)
	for _, target := range []string{private.SpaceID, otherKey.MemorySpaceID.String()} {
		visible, err = store.ListSubmissionAssessmentKnownEvidence(privateCredentialContext(otherKey), SubmissionAssessmentKnownEvidenceInput{TeamID: team, OwnerProfileID: other, SpaceID: target, EvidenceIDs: []string{private.Evidence[0].FragmentID}})
		require.NoError(t, err)
		require.Empty(t, visible.Evidence, "the production evidence reader must scope sources before returning IDs")
	}
	_, err = store.RetractEvidence(privateContext, RetractEvidenceInput{TeamID: team, OwnerProfileID: owner, EvidenceIDs: []string{private.Evidence[0].FragmentID}, IdempotencyKey: "private-retract", RequestHash: "private-retract-hash", Reason: "automation corrected its memory"})
	require.NoError(t, err)
	erasure := privacy.NewPrivateMemoryRepository(app, rls)
	command := privacy.PrivateMemoryErasureRequest{TeamID: key.TeamID, OwnerID: key.OwnerID, CredentialID: key.ID, IdempotencyScopeHash: strings.Repeat("3", 64), RequestHash: strings.Repeat("4", 64), ReasonCode: "owner_erasure"}
	operation, created, err := erasure.RequestCredentialErasure(privateContext, command)
	require.NoError(t, err)
	require.True(t, created)
	sealedKey, err := keys.GetByIDForTeam(ctx, key.TeamID, key.ID)
	require.NoError(t, err)
	require.Equal(t, uuid.Nil, sealedKey.MemorySpaceID)
	replay, created, err := erasure.RequestCredentialErasure(privateCredentialContext(sealedKey), command)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, operation.ID, replay.ID)
	now := time.Now().UTC()
	for attempt := 0; attempt < privacy.MaximumAttempts; attempt++ {
		erasure.SetNow(func() time.Time { return now })
		claim, err := erasure.ClaimNext(ctx, "private-recovery", time.Minute)
		require.NoError(t, err)
		require.NotNil(t, claim)
		require.NoError(t, erasure.ReleaseClaim(ctx, claim.ID, claim.WorkerID, claim.Fence, "database_error"))
		now = now.Add(time.Minute)
	}
	command.IdempotencyScopeHash = strings.Repeat("5", 64)
	command.RequestHash = strings.Repeat("6", 64)
	_, _, err = erasure.RequestCredentialErasure(privateCredentialContext(otherKey), command)
	require.ErrorIs(t, err, domain.ErrMemoryWriteDenied)
	recovery, created, err := erasure.RequestCredentialErasure(privateCredentialContext(sealedKey), command)
	require.NoError(t, err)
	require.True(t, created)
	require.NotEqual(t, operation.ID, recovery.ID)
	require.Equal(t, operation.TargetGeneration, recovery.TargetGeneration)
	failed, err := erasure.GetOperation(ctx, operation.ID)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureFailed, failed.Status)
	_, err = keys.RevokeForTeam(ctx, key.TeamID, key.ID)
	require.NoError(t, err)
	_, _, err = erasure.RequestCredentialErasure(privateCredentialContext(sealedKey), command)
	require.ErrorIs(t, err, domain.ErrMemoryWriteDenied)

}

func TestPrivateMemorySSOKeySpacesAndMappingRevocation(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	team := uuid.MustParse(createLedgerTeam(t, admin, rls, "private-sso-isolation"))
	issuer := createLedgerSSOIdentity(t, admin, rls, team)
	repo := access.NewCredentialRepository(app, rls, nil)
	a := createOwnedCredential(t, repo, team, issuer, "sso-A")
	b := createOwnedCredential(t, repo, team, issuer, "sso-B")
	var ssoSpace uuid.UUID
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT space.id FROM memory_spaces space JOIN team_memberships membership ON membership.id=space.owner_membership_id AND membership.team_id=space.team_id WHERE membership.team_id=? AND membership.actor_identity_id=? AND space.kind='private'`, team, issuer).Row().Scan(&ssoSpace)
	}))
	require.NotEqual(t, a.MemorySpaceID, b.MemorySpaceID)
	require.NotEqual(t, ssoSpace, a.MemorySpaceID)
	require.NotEqual(t, ssoSpace, b.MemorySpaceID)
	authorize := func(space uuid.UUID) error {
		actorContext := privateCredentialContext(a)
		return rls.WithTeamProfileTx(actorContext, app, team.String(), a.OwnerID.String(), func(tx *gorm.DB) error {
			return storage.AuthorizeMemoryMutationTx(actorContext, tx, team.String(), a.OwnerID.String(), space.String(), 1)
		})
	}
	require.NoError(t, authorize(a.TeamSharedSpaceID))
	require.ErrorIs(t, authorize(b.MemorySpaceID), domain.ErrMemoryWriteDenied)
	require.ErrorIs(t, authorize(ssoSpace), domain.ErrMemoryWriteDenied)
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE sso_group_mappings SET scopes=ARRAY['read'] WHERE provider_id=(SELECT provider_id FROM sso_identities WHERE id=?)`, issuer).Error
	}))
	require.ErrorIs(t, authorize(a.TeamSharedSpaceID), domain.ErrMemoryWriteDenied)
	require.NoError(t, authorize(a.MemorySpaceID))
	listed, err := repo.GetByIDForTeam(ctx, team, a.ID)
	require.NoError(t, err)
	require.True(t, listed.MemoryCapabilities().Private.Write)
	require.False(t, listed.MemoryCapabilities().TeamShared.Write)
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE sso_providers SET enabled=false WHERE id=(SELECT provider_id FROM sso_identities WHERE id=?)`, issuer).Error
	}))
	require.ErrorIs(t, authorize(a.MemorySpaceID), domain.ErrMemoryWriteDenied)
}

func TestSharedLifecycleRequiresLiveWritePermissionForOwnedRecords(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	team := createLedgerTeam(t, admin, rls, "shared-lifecycle-revocation")
	owner := createLedgerProfile(t, admin, rls, team, "owner")
	insertSearchTestContract(t, admin, rls, "shared-lifecycle-revocation", 3, "exact", "")
	store := NewStore(app, rls, ConflictRuntimeConfig{})
	keys := access.NewCredentialRepository(app, rls, nil)
	key, err := keys.GetByIDForTeam(ctx, uuid.MustParse(team), uuid.MustParse(owner))
	require.NoError(t, err)
	actorCtx := privateCredentialContext(key)
	subject := createSemanticEntity(t, ctx, store, team, owner, "person", "Automation")
	wrong := createSemanticEntity(t, ctx, store, team, owner, "project", "Old project")
	first := createSemanticEntity(t, ctx, store, team, owner, "project", "Atlas")
	createSemanticEntity(t, ctx, store, team, owner, "project", "Atlas")
	content := "Automation works on Atlas."
	ingest := createSemanticIngest(t, ctx, store, team, owner, "shared-lifecycle-source", content)
	source := applySemanticDecision(t, ctx, store, ApplyRelationshipDecisionInput{
		TeamID: team, OwnerProfileID: owner, IngestID: ingest.IngestID, SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrong.EntityID,
		Support: &EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: "source", SpanStart: 0, SpanEnd: len(content), Authority: "primary"},
	}).Relationship
	submit := CorrectRelationshipInput{TeamID: team, OwnerProfileID: owner, Action: "submit", RelationshipID: source.RelationshipID, ExpectedVersion: source.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{Name: "Atlas", EntityKind: "project"}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: ingest.Evidence[0].FragmentID, Start: 0, End: len(content)}}, Reason: "resolve project", IdempotencyKey: "before-revocation"}
	pending, err := store.CorrectRelationship(actorCtx, submit)
	require.NoError(t, err)
	require.Equal(t, "awaiting_confirmation", pending.ProcessingState)
	confirm := CorrectRelationshipInput{TeamID: team, OwnerProfileID: owner, Action: "confirm", SubmissionID: pending.SubmissionID, ConfirmationToken: pending.Confirmation.Token,
		Selection: RelationshipCorrectionSelection{ObjectEntityID: first.EntityID}, IdempotencyKey: "confirm-after-revocation"}
	plan, err := store.PlanRelationshipCorrectionEmbeddings(actorCtx, confirm)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Documents)
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		return tx.Exec(`UPDATE credentials SET scopes=ARRAY['read'] WHERE id=?`, key.ID).Error
	}))
	_, err = store.CorrectRelationshipWithEmbeddings(actorCtx, confirm, nil)
	require.ErrorIs(t, err, domain.ErrMemoryWriteDenied, "commit must recheck even after provider planning")
	_, err = store.PlanRelationshipCorrectionEmbeddings(actorCtx, confirm)
	require.ErrorIs(t, err, domain.ErrMemoryWriteDenied, "confirmation must fail before provider work")
	_, err = store.CorrectRelationship(actorCtx, submit)
	require.ErrorIs(t, err, domain.ErrMemoryWriteDenied, "replaying an owned shared submission requires current write permission")
	_, err = store.RetractEvidence(actorCtx, RetractEvidenceInput{TeamID: team, OwnerProfileID: owner, EvidenceIDs: []string{ingest.Evidence[0].FragmentID}, Reason: "revoked writer", IdempotencyKey: "denied", RequestHash: "denied"})
	require.ErrorIs(t, err, domain.ErrMemoryWriteDenied)
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		var state string
		if err := tx.Raw(`SELECT status FROM relationship_records WHERE team_id=? AND relationship_id=?`, team, source.RelationshipID).Row().Scan(&state); err != nil {
			return err
		}
		require.Equal(t, "active", state)
		return nil
	}))
}
