package repository

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestLedgerPlacementClaimSkipsSealedPrivateGeneration(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "placement-claim-private-generation"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	privateCredential := createOwnedCredential(t, credentialRepo, teamID, ownerID, "private-claim", domain.CredentialBindingCredentialPrivate)
	repo := NewLedgerRepository(appDB, rls)

	privateIngest, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:          teamID.String(),
		OwnerProfileID:  privateCredential.ID.String(),
		SpaceID:         privateCredential.MemorySpaceID.String(),
		SpaceGeneration: privateCredential.MemorySpaceGeneration,
		Evidence:        []EvidenceInput{{Content: "A sealed private placement must not be claimed."}},
	})
	require.NoError(t, err)
	privateClaimed, err := repo.ClaimNextPlacementRun(ctx, teamID.String(), "private-generation-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, privateClaimed)
	assert.Equal(t, privateIngest.PlacementRunID, privateClaimed.PlacementRunID)
	_, err = repo.FinishPlacementRun(ctx, teamID.String(), privateClaimed.PlacementRunID, "private-generation-worker", string(domain.PlacementRunCompleted), "")
	require.NoError(t, err)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		return tx.Exec(`
			UPDATE memory_spaces
			SET lifecycle_state = 'sealed', generation = generation + 1, sealed_at = now(), updated_at = now()
			WHERE id = ?::uuid AND team_id = ?::uuid AND lifecycle_state = 'active'
		`, privateCredential.MemorySpaceID, teamID).Error
	}))

	shared, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:         teamID.String(),
		OwnerProfileID: privateCredential.ID.String(),
		Evidence:       []EvidenceInput{{Content: "A current shared placement remains claimable."}},
	})
	require.NoError(t, err)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID.String(), "private-generation-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	assert.Equal(t, shared.PlacementRunID, claimed.PlacementRunID)
}

func TestLedgerPlacementOutcomesInheritRunMemorySpace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "placement-outcome-private-space"))
	ownerID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	privateCredential := createOwnedCredential(t, credentialRepo, teamID, ownerID, "private-outcome", domain.CredentialBindingCredentialPrivate)
	repo := NewLedgerRepository(appDB, rls)
	created, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:          teamID.String(),
		OwnerProfileID:  privateCredential.ID.String(),
		SpaceID:         privateCredential.MemorySpaceID.String(),
		SpaceGeneration: privateCredential.MemorySpaceGeneration,
		Evidence:        []EvidenceInput{{Content: "Placement outcomes inherit the referenced run space."}},
	})
	require.NoError(t, err)

	outcomeID, err := repo.AppendPlacementOutcome(ctx, PlacementOutcomeInput{
		TeamID:          teamID.String(),
		OwnerProfileID:  privateCredential.ID.String(),
		PlacementRunID:  created.PlacementRunID,
		PlacementItemID: created.Items[0].PlacementItemID,
		OutcomeKind:     "private_space_regression",
		Status:          "recorded",
		Payload:         map[string]any{"verified": true},
	})
	require.NoError(t, err)
	var outcomeSpaceID string
	var outcomeGeneration int64
	require.NoError(t, adminDB.Raw(`
		SELECT space_id::text, space_generation
		FROM placement_outcomes
		WHERE team_id = ?::uuid AND outcome_id = ?::uuid
	`, teamID, outcomeID).Row().Scan(&outcomeSpaceID, &outcomeGeneration))
	assert.Equal(t, privateCredential.MemorySpaceID.String(), outcomeSpaceID)
	assert.Equal(t, privateCredential.MemorySpaceGeneration, outcomeGeneration)

	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID.String(), "private-outcome-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	_, err = repo.FinishPlacementRun(ctx, teamID.String(), claimed.PlacementRunID, "private-outcome-worker", "completed", "")
	require.NoError(t, err)
	var markerSpaceID string
	var markerGeneration int64
	require.NoError(t, adminDB.Raw(`
		SELECT space_id::text, space_generation
		FROM placement_outcomes
		WHERE team_id = ?::uuid AND placement_run_id = ?::uuid
		  AND outcome_kind = 'telemetry_first_disposition'
	`, teamID, created.PlacementRunID).Row().Scan(&markerSpaceID, &markerGeneration))
	assert.Equal(t, privateCredential.MemorySpaceID.String(), markerSpaceID)
	assert.Equal(t, privateCredential.MemorySpaceGeneration, markerGeneration)
}

func TestPrivatePlacementDerivedWritersInheritMemorySpace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	insertSearchTestContract(t, adminDB, rls, "placement-derived-private-space", 3, "exact", "")
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "placement-derived-private-space"))
	identityID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	privateCredential := createOwnedCredential(t, credentialRepo, teamID, identityID, "private-derived", domain.CredentialBindingCredentialPrivate)
	repo := NewLedgerRepository(appDB, rls)
	created, err := repo.CreateIngest(ctx, CreateIngestInput{
		TeamID:          teamID.String(),
		OwnerProfileID:  privateCredential.ID.String(),
		SpaceID:         privateCredential.MemorySpaceID.String(),
		SpaceGeneration: privateCredential.MemorySpaceGeneration,
		Evidence: []EvidenceInput{{
			Content:                   "Private subject uses a private value.",
			SourceKey:                 "private://derived",
			SourceRevisionToken:       "rev-1",
			SourceRevisionContentHash: "sha256:private-derived",
		}},
	})
	require.NoError(t, err)
	claimed, err := repo.ClaimNextPlacementRun(ctx, teamID.String(), "private-derived-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claimed)

	committed, err := commitAcceptedSubmissionFixture(t, ctx, repo, CommitPlacementSemanticInput{
		TeamID:           teamID.String(),
		OwnerProfileID:   privateCredential.ID.String(),
		IngestID:         created.IngestID,
		PlacementRunID:   created.PlacementRunID,
		PlacementItemID:  created.Items[0].PlacementItemID,
		WorkerID:         "private-derived-worker",
		ExpectedAttempts: claimed.Attempts,
		Status:           string(domain.SemanticReviewAccepted),
		EntityResolutions: []PlacementEntityResolutionInput{
			{MentionRef: "subject", Action: "create", EntityKind: "project", CanonicalName: "Private subject"},
		},
		RelationshipObservations: []PlacementRelationshipDecisionInput{{
			Ref:          "private-derived-relation",
			SubjectRef:   "subject",
			PredicateKey: "released",
			ObjectValue: &PlacementValueInput{
				Ref: "private-value", ValueType: "date", CanonicalValue: "2026-08-21", Display: "2026-08-21",
			},
			Support: &EvidenceSupportInput{
				FragmentID: created.Evidence[0].FragmentID, SourceGroupKey: "private-derived",
				SpanStart: 0, SpanEnd: len("Private subject uses a private value."),
				Quote: "Private subject uses a private value.", Authority: "primary",
			},
		}},
	})
	require.NoError(t, err)
	require.NotEmpty(t, committed.EntityResolutionIDs)

	var sourceSpaceID, revisionSpaceID, entitySpaceID, nameSpaceID, valueSpaceID, resolutionSpaceID, relationshipSpaceID string
	var sourceGeneration, revisionGeneration, entityGeneration, nameGeneration, valueGeneration, resolutionGeneration, relationshipGeneration int64
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Raw(`SELECT space_id::text, space_generation FROM evidence_sources WHERE team_id = ?::uuid AND source_key = 'private://derived'`, teamID).Row().Scan(&sourceSpaceID, &sourceGeneration); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT space_id::text, space_generation FROM evidence_source_revisions WHERE team_id = ?::uuid AND content_hash = 'sha256:private-derived'`, teamID).Row().Scan(&revisionSpaceID, &revisionGeneration); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT space_id::text, space_generation FROM entity_records WHERE team_id = ?::uuid AND identity_context->>'mention_ref' = 'subject'`, teamID).Row().Scan(&entitySpaceID, &entityGeneration); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT names.space_id::text, names.space_generation FROM entity_names AS names JOIN entity_records AS entity ON entity.team_id = names.team_id AND entity.entity_id = names.entity_id WHERE names.team_id = ?::uuid AND entity.identity_context->>'mention_ref' = 'subject'`, teamID).Row().Scan(&nameSpaceID, &nameGeneration); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT space_id::text, space_generation FROM value_records WHERE team_id = ?::uuid AND canonical_value = '2026-08-21'`, teamID).Row().Scan(&valueSpaceID, &valueGeneration); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT space_id::text, space_generation FROM entity_resolution_events WHERE team_id = ?::uuid AND placement_item_id = ?::uuid`, teamID, created.Items[0].PlacementItemID).Row().Scan(&resolutionSpaceID, &resolutionGeneration); err != nil {
			return err
		}
		return tx.Raw(`SELECT space_id::text, space_generation FROM relationship_records WHERE team_id = ?::uuid AND relationship_id = ?::uuid`, teamID, committed.RelationshipResults[0].Relationship.RelationshipID).Row().Scan(&relationshipSpaceID, &relationshipGeneration)
	}))
	for _, got := range []string{sourceSpaceID, revisionSpaceID, entitySpaceID, nameSpaceID, valueSpaceID, resolutionSpaceID, relationshipSpaceID} {
		assert.Equal(t, privateCredential.MemorySpaceID.String(), got)
	}
	for _, got := range []int64{sourceGeneration, revisionGeneration, entityGeneration, nameGeneration, valueGeneration, resolutionGeneration, relationshipGeneration} {
		assert.Equal(t, privateCredential.MemorySpaceGeneration, got)
	}
}
