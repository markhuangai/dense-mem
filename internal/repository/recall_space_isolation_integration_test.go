package repository

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

func TestRecallEvidenceIsolatesCredentialPrivateSpacesWithinTeam(t *testing.T) {
	adminDB, appDB, rls, cleanup := setupLedgerRepositoryDB(t)
	defer cleanup()
	ctx := context.Background()
	teamID := uuid.MustParse(createLedgerTeam(t, adminDB, rls, "recall-credential-private-isolation-team"))
	insertSearchTestContract(t, adminDB, rls, "recall-credential-private-isolation", 3, "exact", "")

	credentialRepo := NewCredentialRepository(adminDB, rls)
	credentials := make([]*domain.Credential, 2)
	for i, name := range []string{"credential-private-a", "credential-private-b"} {
		keyPrefix := "dm_" + uuid.NewString()[:20]
		credential := &domain.Credential{
			ID:            uuid.New(),
			TeamID:        teamID,
			Name:          name,
			KeyHash:       "hash-" + name,
			KeyPrefix:     keyPrefix,
			KeySuffix:     "suffix",
			Scopes:        []string{"read", "write"},
			MemoryBinding: domain.CredentialBindingCredentialPrivate,
		}
		require.NoError(t, credentialRepo.CreateCredential(ctx, credential))
		credentials[i] = credential
	}

	shared, err := NewMemorySpaceRepository(appDB, rls).GetTeamShared(ctx, teamID)
	require.NoError(t, err)
	require.NotNil(t, shared)
	ledgerRepo := NewLedgerRepository(appDB, rls)
	searchRepo := NewSearchRepository(appDB, rls)
	const query = "credential private isolation sentinel"

	for i, credential := range credentials {
		privateSpace := credential.MemorySpaceID
		actorCtx := requestctx.WithActor(ctx, requestctx.Actor{
			TeamID:        teamID,
			IdentityID:    credential.ActorIdentityID,
			MembershipID:  credential.MembershipID,
			OwnerID:       credential.OwnerID,
			CredentialID:  &credential.ID,
			AuthMethod:    "api_key",
			AllowedSpaces: []domain.MemorySpaceAccess{{ID: shared.ID, Kind: domain.MemorySpaceTeamShared}, {ID: privateSpace, Kind: domain.MemorySpaceCredentialPrivate}},
		})
		content := fmt.Sprintf("credential %c %s", rune('a'+i), query)
		ingest, err := ledgerRepo.CreateIngest(actorCtx, CreateIngestInput{
			TeamID:         teamID.String(),
			OwnerProfileID: credential.OwnerID.String(),
			IdempotencyKey: "private-isolation-" + credential.ID.String(),
			RequestHash:    sha256Hex(content),
			Evidence: []EvidenceInput{{
				Content:    "shared setup evidence",
				SourceType: "document",
			}},
		})
		require.NoError(t, err)

		fragmentID := uuid.New()
		require.NoError(t, rls.WithTeamProfileTx(actorCtx, appDB, teamID.String(), credential.OwnerID.String(), func(tx *gorm.DB) error {
			return tx.Exec(`
				INSERT INTO evidence_fragments (
				    team_id, fragment_id, ingest_id, owner_profile_id, evidence_index,
				    content, content_hash, source_type, authority, source_ref, labels, metadata, space_id
				) VALUES (?, ?, ?, ?, 1, ?, ?, 'document', 'primary', '', ARRAY[]::text[], '{}'::jsonb, ?)
			`, teamID, fragmentID, ingest.IngestID, credential.OwnerID, content, sha256Hex(content), privateSpace).Error
		}))

		doc, err := searchRepo.UpsertSearchDocument(actorCtx, UpsertSearchDocumentInput{
			TeamID:         teamID.String(),
			OwnerProfileID: credential.OwnerID.String(),
			SourceKind:     "evidence",
			SourceID:       fragmentID.String(),
			SourceVersion:  1,
			SpaceID:        privateSpace.String(),
			DocumentText:   content,
		})
		require.NoError(t, err)
		require.Equal(t, privateSpace.String(), doc.SpaceID)

		recall, err := searchRepo.RecallEvidence(actorCtx, RecallEvidenceInput{
			TeamID:    teamID.String(),
			Query:     query,
			Limit:     10,
			SpaceID:   privateSpace.String(),
			SpaceKind: string(domain.MemorySpaceCredentialPrivate),
		})
		require.NoError(t, err)
		require.Len(t, recall.Results, 1)
		require.Equal(t, fragmentID.String(), recall.Results[0].EvidenceID)
		require.Contains(t, recall.Results[0].Context, content)
	}
}
