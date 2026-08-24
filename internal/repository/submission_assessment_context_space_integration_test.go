package repository

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRememberFencesCorrectionAndConflictContextsToTheIngestSpace(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "remember-preflight-context-space"))
	identityID := createLedgerSSOIdentity(t, adminDB, rls, teamID)
	credentialRepo := NewCredentialRepository(appDB, rls)
	credentialA := createOwnedCredential(t, credentialRepo, teamID, identityID, "remember-context-a", domain.CredentialBindingCredentialPrivate)
	credentialB := createOwnedCredential(t, credentialRepo, teamID, identityID, "remember-context-b", domain.CredentialBindingCredentialPrivate)
	semantic := NewSemanticRepository(appDB, rls)
	ledger := NewLedgerRepository(appDB, rls)
	insertSearchTestContract(t, adminDB, rls, "remember-context-space-search", 3, "exact", "")
	subject := createSemanticEntity(t, ctx, semantic, teamID.String(), credentialA.ID.String(), "project", "Context Subject")
	object := createSemanticEntity(t, ctx, semantic, teamID.String(), credentialA.ID.String(), "product", "Context Object")
	conflictObject := createSemanticEntity(t, ctx, semantic, teamID.String(), credentialB.ID.String(), "product", "Context Conflict Object")
	committed := commitPlacementRelationshipForConflictTest(
		t, ctx, ledger, teamID.String(), credentialA.ID.String(), "remember-context-worker", "remember-context-target",
		"Context Subject uses Context Object.", subject.EntityID, object.EntityID, "remember-context-target-source",
	)
	commitPlacementRelationshipForConflictTest(
		t, ctx, ledger, teamID.String(), credentialB.ID.String(), "remember-context-conflict-worker", "remember-context-conflict",
		"Context Subject uses Context Conflict Object.", subject.EntityID, conflictObject.EntityID, "remember-context-conflict-source",
	)
	targetRelationship := committed.RelationshipResults[0].Relationship
	require.NotNil(t, targetRelationship)
	conflictID, conflictVersion := loadConflictCaseVersionForSubject(
		t, ctx, appDB, rls, teamID.String(), credentialA.ID.String(), subject.EntityID,
	)
	require.NoError(t, rls.WithSystemTx(ctx, adminDB, func(tx *gorm.DB) error {
		if err := tx.Exec(`
			UPDATE relationship_records
			SET space_id = ?, space_generation = ?
			WHERE team_id = ?::uuid AND relationship_id = ?::uuid
		`, credentialB.MemorySpaceID, credentialB.MemorySpaceGeneration, teamID, targetRelationship.RelationshipID).Error; err != nil {
			return err
		}
		return tx.Exec(`
			UPDATE relationship_conflict_cases
			SET space_id = ?, space_generation = ?
			WHERE team_id = ?::uuid AND conflict_id = ?::uuid
		`, credentialB.MemorySpaceID, credentialB.MemorySpaceGeneration, teamID, conflictID).Error
	}))

	proposal := map[string]any{"relationship_hints": []map[string]any{
		{
			"subject":           map[string]any{"name": "Context Subject", "entity_kind": "project"},
			"predicate":         map[string]any{"proposed_key": "uses"},
			"object":            map[string]any{"entity": map[string]any{"name": "Context Object", "entity_kind": "product"}},
			"correction_target": map[string]any{"relationship_id": targetRelationship.RelationshipID, "expected_version": targetRelationship.Version},
			"evidence_indices":  []any{0},
		},
		{
			"subject":          map[string]any{"name": "Context Subject", "entity_kind": "project"},
			"predicate":        map[string]any{"proposed_key": "uses"},
			"object":           map[string]any{"entity": map[string]any{"name": "Context Object", "entity_kind": "product"}},
			"conflict_context": map[string]any{"conflict_id": conflictID, "expected_version": conflictVersion},
			"evidence_indices": []any{0},
		},
	}}
	_, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID.String(), OwnerProfileID: credentialA.ID.String(),
		SpaceID: credentialA.MemorySpaceID.String(), SpaceGeneration: credentialA.MemorySpaceGeneration,
		IdempotencyKey: "remember-context-cross-space-preflight", RequestHash: "remember-context-cross-space-preflight-hash",
		TelemetryRemember: true, Proposal: proposal,
		Evidence: []EvidenceInput{{Content: "Cross-space lifecycle contexts must not stage."}},
	})
	var preflight *RememberPreflightError
	require.True(t, errors.As(err, &preflight), "err=%v", err)
	require.Contains(t, preflight.Issues, RememberPreflightIssue{
		Path: "/relationships/0/correction_target/relationship_id", Code: "unavailable", Message: "correction target is unavailable",
	})
	require.Contains(t, preflight.Issues, RememberPreflightIssue{
		Path: "/relationships/1/conflict_context/conflict_id", Code: "unavailable", Message: "conflict context is unavailable",
	})

	staged, err := ledger.CreateIngest(ctx, CreateIngestInput{
		TeamID: teamID.String(), OwnerProfileID: credentialA.ID.String(),
		SpaceID: credentialA.MemorySpaceID.String(), SpaceGeneration: credentialA.MemorySpaceGeneration,
		IdempotencyKey: "remember-context-cross-space-commit", RequestHash: "remember-context-cross-space-commit-hash",
		TelemetryRemember: true,
		Proposal: map[string]any{"relationship_hints": []map[string]any{{
			"subject":          map[string]any{"name": "Context Subject", "entity_kind": "project"},
			"predicate":        map[string]any{"proposed_key": "uses"},
			"object":           map[string]any{"entity": map[string]any{"name": "Context Object", "entity_kind": "product"}},
			"evidence_indices": []any{0},
		}}},
		Evidence: []EvidenceInput{{Content: "Commit-time lifecycle context fence."}},
	})
	require.NoError(t, err)
	scope := SubmissionAssessmentRunScope{TeamID: teamID.String(), OwnerProfileID: credentialA.ID.String(), IngestID: staged.IngestID, PlacementRunID: staged.PlacementRunID}
	require.ErrorIs(t, rls.WithTeamProfileTx(ctx, appDB, teamID.String(), credentialA.ID.String(), func(tx *gorm.DB) error {
		return validateSubmissionAssessmentContextSpaces(ctx, tx, CommitSubmissionAssessmentInput{
			SubmissionAssessmentRunScope: scope,
			RelationshipObservations: []SubmissionAssessmentRelationshipObservationInput{{
				Observation: PlacementRelationshipDecisionInput{CorrectionTarget: &PlacementCorrectionTargetInput{
					RelationshipID: targetRelationship.RelationshipID, ExpectedVersion: targetRelationship.Version,
				}},
			}},
		})
	}), ErrCorrectionTargetStale)
	require.ErrorIs(t, rls.WithTeamProfileTx(ctx, appDB, teamID.String(), credentialA.ID.String(), func(tx *gorm.DB) error {
		return validateSubmissionAssessmentContextSpaces(ctx, tx, CommitSubmissionAssessmentInput{
			SubmissionAssessmentRunScope: scope,
			RelationshipObservations: []SubmissionAssessmentRelationshipObservationInput{{
				Observation: PlacementRelationshipDecisionInput{ConflictContext: &PlacementConflictContextInput{
					ConflictID: conflictID, ExpectedVersion: conflictVersion,
				}},
			}},
		})
	}), ErrConflictContextStale)
}
