//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	accesspostgres "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledgepostgres "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	tracepostgres "github.com/markhuangai/dense-mem/internal/trace/postgres"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestEvidenceOnlyPostgresPrivateErasureCleansEvidenceAndDiagnosticLineage(t *testing.T) {
	admin, app, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	team := uuid.MustParse(createLedgerTeam(t, admin, rls, "private-evidence-lineage"))
	identity := createLedgerSSOIdentity(t, admin, rls, team)
	credentials := accesspostgres.NewCredentialRepository(app, rls, nil)
	target := createOwnedCredential(t, credentials, team, identity, "Evidence lineage")
	peer := createOwnedCredential(t, credentials, team, identity, "Other private owner")
	actor := func(key *domain.Credential) context.Context {
		return requestctx.WithActor(ctx, requestctx.Actor{
			TeamID: team, OwnerID: key.OwnerID, IdentityID: key.ActorIdentityID, MembershipID: key.MembershipID, CredentialID: &key.ID,
			AuthMethod: "api_key", Grants: key.Scopes, AllowedSpaces: key.MemorySpaces(),
		})
	}
	ledger := knowledgepostgres.NewStore(app, rls)
	peerStored, err := ledger.CreateIngestForTest(actor(peer), knowledge.CreateIngestInput{
		TeamID: team.String(), OwnerProfileID: peer.ID.String(), SpaceID: peer.MemorySpaceID.String(), SpaceGeneration: peer.MemorySpaceGeneration,
		IdempotencyKey: "peer-evidence-source", RequestHash: sha256Hex("peer-evidence-source"),
		Evidence: []knowledge.EvidenceInput{{Content: "Other private Evidence survives.", SourceKey: "doc://private-lineage", SourceRevisionToken: "r1"}},
	})
	require.NoError(t, err)
	input := knowledge.CreateIngestInput{TeamID: team.String(), OwnerProfileID: target.ID.String(),
		SpaceID: target.MemorySpaceID.String(), SpaceGeneration: target.MemorySpaceGeneration,
		IdempotencyKey: "private-evidence-source", RequestHash: sha256Hex("private-evidence-source"),
		Evidence: []knowledge.EvidenceInput{{Content: "Exact private Evidence. Café 🧭", SourceKey: "doc://private-lineage", SourceRevisionToken: "r1", SourceType: "document", Authority: "primary",
			InitialEvent: &knowledge.SecurityEventDraft{EventKind: "deterministic_scan", Decision: "pass", Reason: "Admitted exact Evidence"}}},
	}
	stored, err := ledger.CreateIngestForTest(actor(target), input)
	require.NoError(t, err)
	evidence := stored.Evidence[0].FragmentID
	invocation := uuid.NewString()
	require.NoError(t, ledger.RecordRememberInvocationDiagnostic(ctx, knowledge.RememberInvocationDiagnosticInput{
		TeamID: team.String(), OwnerProfileID: target.ID.String(), InvocationID: invocation,
		SpaceID: target.MemorySpaceID.String(), SpaceGeneration: target.MemorySpaceGeneration,
		Classification: "execution", Outcome: "completed", CreatedAt: time.Now().UTC(), CompletedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(24 * time.Hour),
	}))
	_, err = ledger.RetractEvidence(actor(target), knowledge.RetractEvidenceInput{TeamID: team.String(), OwnerProfileID: target.ID.String(), EvidenceIDs: []string{evidence},
		IdempotencyKey: "retract-private-evidence", RequestHash: sha256Hex("retract-private-evidence"), Reason: "Withdraw private Evidence"})
	require.NoError(t, err)
	traces := tracepostgres.New(app, rls)
	trace, err := traces.TraceEvidence(actor(target), tracecontract.Input{TeamID: team.String(), EvidenceID: evidence})
	require.NoError(t, err)
	require.Equal(t, input.Evidence[0].Content, trace.Evidence.Content)
	require.Len(t, trace.Occurrences, 1)
	require.Len(t, trace.SourceRevisions, 1)
	require.NotEmpty(t, trace.Decisions)
	require.Len(t, trace.Lifecycle, 1)
	_, err = traces.TraceEvidence(actor(peer), tracecontract.Input{TeamID: team.String(), EvidenceID: evidence})
	require.ErrorIs(t, err, tracecontract.ErrEvidenceNotFound)
	repository := NewPrivateMemoryRepository(app, rls)
	require.NoError(t, repository.Prepare(ctx))
	operation, created, err := repository.RequestCredentialErasure(actor(target), PrivateMemoryErasureRequest{
		TeamID: team, OwnerID: target.ID, CredentialID: target.ID, IdempotencyScopeHash: Hash("private-evidence-lineage", target.ID.String()),
		RequestHash: Hash("erase-private-evidence-lineage", target.ID.String()), ReasonCode: "owner_request",
	})
	require.NoError(t, err)
	require.True(t, created)
	claim, err := repository.ClaimNext(ctx, "private-evidence-lineage-worker", time.Minute)
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Equal(t, operation.ID, claim.ID)
	completed, err := repository.ExecuteClaim(ctx, claim.ID, claim.WorkerID, claim.Fence)
	require.NoError(t, err)
	require.Equal(t, domain.PrivateMemoryErasureCompleted, completed.Status)
	require.NoError(t, rls.WithSystemTx(ctx, admin, func(tx *gorm.DB) error {
		for _, table := range []string{"evidence_fragments", "evidence_occurrences", "evidence_sources", "evidence_source_revisions", "evidence_security_events", "evidence_lifecycle_events", "evidence_lifecycle_operations", "remember_invocation_diagnostics"} {
			var count int64
			if err := tx.Raw("SELECT count(*) FROM "+table+" WHERE space_id=?", target.MemorySpaceID).Scan(&count).Error; err != nil {
				return err
			}
			require.Zero(t, count, table)
		}
		return nil
	}))
	_, err = traces.TraceEvidence(actor(target), tracecontract.Input{TeamID: team.String(), EvidenceID: evidence})
	require.ErrorIs(t, err, tracecontract.ErrEvidenceNotFound)
	preserved, err := traces.TraceEvidence(actor(peer), tracecontract.Input{TeamID: team.String(), EvidenceID: peerStored.Evidence[0].FragmentID})
	require.NoError(t, err)
	require.Equal(t, "Other private Evidence survives.", preserved.Evidence.Content)
	require.Len(t, preserved.SourceRevisions, 1)
}
