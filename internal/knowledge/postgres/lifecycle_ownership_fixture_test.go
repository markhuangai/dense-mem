//go:build integration

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	storagepostgres "github.com/markhuangai/dense-mem/internal/storage/postgres"
)

type lifecycleOwnershipFixture struct {
	adminDB, appDB  *gorm.DB
	rls             *storagepostgres.RLS
	store           *Store
	teamID, ownerID string
	ingest          *EvidenceIngestResult
	decision        *RelationshipDecisionResult
	correctObject   *EntityRecord
}

const lifecycleOwnershipContent = "Jamie works on Dense-Mem."

func newLifecycleOwnershipFixture(t testing.TB) *lifecycleOwnershipFixture {
	t.Helper()
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	t.Cleanup(cleanup)
	f := &lifecycleOwnershipFixture{adminDB: adminDB, appDB: appDB, rls: rls, store: NewStore(appDB, rls, ConflictRuntimeConfig{})}
	f.seed(t)
	return f
}

func (f *lifecycleOwnershipFixture) reset(t testing.TB) {
	t.Helper()
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.adminDB, func(tx *gorm.DB) error {
		return tx.Exec("TRUNCATE teams, actor_identities, embedding_contracts CASCADE").Error
	}))
	f.seed(t)
}

func (f *lifecycleOwnershipFixture) seed(t testing.TB) {
	t.Helper()
	ctx := context.Background()
	searchTestContractSequence.Store(0)
	insertSearchTestContract(t, f.adminDB, f.rls, "lifecycle-ownership", 3, "exact", "")
	f.teamID = createLedgerTeam(t, f.adminDB, f.rls, "lifecycle-ownership")
	f.ownerID = createLedgerProfile(t, f.adminDB, f.rls, f.teamID, "lifecycle-owner")
	subject := createSemanticEntity(t, ctx, f.store, f.teamID, f.ownerID, "person", "Jamie")
	wrongObject := createSemanticEntity(t, ctx, f.store, f.teamID, f.ownerID, "project", "Wrong Project")
	f.correctObject = createSemanticEntity(t, ctx, f.store, f.teamID, f.ownerID, "project", "Dense-Mem")
	var err error
	f.ingest, err = f.store.CreateIngestForTest(ctx, CreateIngestInput{
		TeamID: f.teamID, OwnerProfileID: f.ownerID, IdempotencyKey: "lifecycle-ingest",
		RequestHash: sha256Hex(lifecycleOwnershipContent), Evidence: []EvidenceInput{{Content: lifecycleOwnershipContent}},
	})
	require.NoError(t, err)
	require.Len(t, f.ingest.Evidence, 1)
	f.decision, err = f.store.ApplyRelationshipDecision(ctx, ApplyRelationshipDecisionInput{
		TeamID: f.teamID, OwnerProfileID: f.ownerID, IngestID: f.ingest.IngestID,
		SubjectEntityID: subject.EntityID, PredicateKey: "works_on", ObjectEntityID: wrongObject.EntityID,
		Support: &EvidenceSupportInput{FragmentID: f.ingest.Evidence[0].FragmentID, SourceGroupKey: "lifecycle-source", SpanEnd: len(lifecycleOwnershipContent), Authority: "primary"},
	})
	require.NoError(t, err)
	require.NotNil(t, f.decision.Relationship)
	require.Equal(t, "active", f.decision.Relationship.Status)
	require.Equal(t, 1, f.decision.Relationship.SupportCount)
}

func (f *lifecycleOwnershipFixture) supportInput() ApplyRelationshipSupportDecisionInput {
	return ApplyRelationshipSupportDecisionInput{
		TeamID: f.teamID, OwnerProfileID: f.ownerID, RelationshipID: f.decision.Relationship.RelationshipID,
		SupportID: f.decision.SupportID, Decision: "revoke", Reason: "support withdrawn", IdempotencyKey: "lifecycle-support-revoke",
	}
}

func (f *lifecycleOwnershipFixture) retractInput() RetractEvidenceInput {
	return RetractEvidenceInput{
		TeamID: f.teamID, OwnerProfileID: f.ownerID, EvidenceIDs: []string{f.ingest.Evidence[0].FragmentID},
		Reason: "evidence withdrawn", IdempotencyKey: "lifecycle-retraction", RequestHash: sha256Hex("lifecycle-retraction"),
	}
}

func (f *lifecycleOwnershipFixture) correctionInput() CorrectRelationshipInput {
	return CorrectRelationshipInput{
		TeamID: f.teamID, OwnerProfileID: f.ownerID, Action: "submit",
		RelationshipID: f.decision.Relationship.RelationshipID, ExpectedVersion: f.decision.Relationship.Version,
		Patch:    RelationshipCorrectionPatch{ObjectEntity: &RelationshipCorrectionEntityPatch{EntityID: f.correctObject.EntityID}},
		Supports: []RelationshipCorrectionSupport{{EvidenceID: f.ingest.Evidence[0].FragmentID, End: len(lifecycleOwnershipContent)}},
		Reason:   "object resolved incorrectly", IdempotencyKey: "lifecycle-correction",
	}
}

type lifecycleOwnershipState struct {
	Status                        string
	SupportCount, Version         int
	SupportDecisions, Transitions int
	LifecycleEvents               int
}

func (f *lifecycleOwnershipFixture) state(t testing.TB) lifecycleOwnershipState {
	t.Helper()
	var state lifecycleOwnershipState
	require.NoError(t, f.rls.WithTeamProfileTx(context.Background(), f.appDB, f.teamID, f.ownerID, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT relationship.status, relationship.support_count, relationship.version,
			(SELECT COUNT(*) FROM relationship_support_decision_events WHERE team_id = relationship.team_id AND relationship_id = relationship.relationship_id) AS support_decisions,
			(SELECT COUNT(*) FROM relationship_transition_events WHERE team_id = relationship.team_id AND relationship_id = relationship.relationship_id) AS transitions,
			(SELECT COUNT(*) FROM evidence_lifecycle_events WHERE team_id = relationship.team_id) AS lifecycle_events
			FROM relationship_records AS relationship WHERE team_id = ?::uuid AND relationship_id = ?::uuid`,
			f.teamID, f.decision.Relationship.RelationshipID).Scan(&state).Error
	}))
	return state
}
