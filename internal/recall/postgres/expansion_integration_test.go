//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

func testScopedEvidenceExpansion(t *testing.T) {
	f := newRecallOntologyFixture(t)
	seed := f.evidence(t, 0, "Launchcode cytomorphism hyperthermia", nil)
	objection := f.evidence(t, 1, "Cytomorphism disproves the original hypothesis through bridgingtest.", nil)
	f.evidence(t, 1, "Bridgingtest appears only past the first hop.", nil)
	for i := range peersPerBridge + 1 {
		f.evidence(t, 2, fmt.Sprintf("Hyperthermia also occurs in unrelated work %d.", i), nil)
	}
	topicPeer := f.evidence(t, 2, "Independent trial observation.", nil)
	def := topic.Record{ID: uuid.NewString(), Kind: topic.Topic, Definition: &topic.Definition{Key: "clinical trial", Label: "Clinical trial"}}
	f.publication(t, def, f.assignment(t, def.ID, seed), f.assignment(t, def.ID, topicPeer))
	private := &domain.Credential{
		ID: uuid.New(), TeamID: uuid.MustParse(f.team), Name: "private bridge fixture",
		KeyHash: "synthetic-" + uuid.NewString(), KeyPrefix: uuid.NewString()[:24],
		KeySuffix: "test", Scopes: []string{"read", "write"},
	}
	require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(t.Context(), private))
	privateScope := requestctx.WithAllowedSpaces(t.Context(), []domain.MemorySpaceAccess{
		{ID: private.MemorySpaceID, Kind: domain.MemorySpacePrivate, Generation: private.MemorySpaceGeneration},
	})
	forbiddenPrivate := addExpansionDocument(t, privateScope, f, f.team, private.OwnerID.String(),
		private.MemorySpaceID.String(), private.MemorySpaceGeneration, "Cytomorphism is under embargo in Private.")
	foreign := createLedgerTeam(t, f.admin, f.rls, "foreign bridge fixture")
	foreignOwner := createLedgerProfile(t, f.admin, f.rls, foreign, "foreign source owner")
	forbiddenForeign := addExpansionDocument(t, t.Context(), f, foreign, foreignOwner, "", 0, "Cytomorphism in another team.")
	input := RecallEvidenceInput{TeamID: f.team, SpaceKind: "team_shared", SpaceID: f.space, Query: "Launchcode", Limit: 10, OrganizationEnabled: true}
	ctx := f.actor(0, "member")
	ordinary, err := f.search.RecallEvidence(ctx, input)
	require.NoError(t, err)
	require.Len(t, ordinary.Results, 1)
	require.Equal(t, seed.ID, ordinary.Results[0].EvidenceID)

	input.ExpansionEnabled = true
	expanded, err := f.search.RecallEvidence(ctx, input)
	require.NoError(t, err)
	paths := map[string]string{}
	for _, hit := range expanded.Results {
		require.NotContains(t, []string{forbiddenPrivate, forbiddenForeign}, hit.EvidenceID)
		if hit.Bridge != nil {
			require.Equal(t, seed.ID, hit.Bridge.SeedID)
			require.Equal(t, "Launchcode cytomorphism hyperthermia", hit.Bridge.SeedExcerpt)
			paths[hit.EvidenceID] = hit.Bridge.TopicID + hit.Bridge.Term
		}
	}
	require.Equal(t, map[string]string{objection.ID: "cytomorphism", topicPeer.ID: def.ID}, paths)
	require.Len(t, expanded.Results, 3)

	input.KnownEvidenceIDs = []string{objection.ID}
	input.TopicIDs = []string{def.ID}
	filtered, err := f.search.RecallEvidence(ctx, input)
	require.NoError(t, err)
	require.Len(t, filtered.Results, 2)
	require.ElementsMatch(t, []string{seed.ID, topicPeer.ID}, []string{filtered.Results[0].EvidenceID, filtered.Results[1].EvidenceID})

	input.KnownEvidenceIDs, input.TopicIDs = nil, nil
	now := time.Now().UTC()
	input.KnownAt = &now
	historical, err := f.search.RecallEvidence(ctx, input)
	require.NoError(t, err)
	var ids []string
	for _, hit := range historical.Results {
		ids = append(ids, hit.EvidenceID)
	}
	require.ElementsMatch(t, []string{seed.ID, objection.ID}, ids)
}

func addExpansionDocument(t *testing.T, ctx context.Context, f *recallOntologyFixture, team, owner, space string, generation int64, content string) string {
	t.Helper()
	ingest, err := f.knowledge.CreateIngestForTest(ctx, knowledge.CreateIngestInput{TeamID: team, OwnerProfileID: owner, SpaceID: space, SpaceGeneration: generation,
		IdempotencyKey: uuid.NewString(), RequestHash: sha256Hex(content),
		Evidence: []knowledge.EvidenceInput{{Content: content, SourceType: "document", ForceInsert: true}}})
	require.NoError(t, err)
	id := ingest.Evidence[0].FragmentID
	_, err = f.search.UpsertSearchDocument(ctx, knowledge.UpsertSearchDocumentInput{TeamID: team, OwnerProfileID: owner, SpaceID: space, SpaceGeneration: generation,
		SourceKind: "evidence", SourceID: id, SourceVersion: 1, DocumentText: content})
	require.NoError(t, err)
	return id
}
