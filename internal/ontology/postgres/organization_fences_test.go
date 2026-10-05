package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
)

func testOntologyOrganizationVocabularyAndSourceFences(t *testing.T) {
	t.Run("bounded vocabulary reuse", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		changes := []ontology.Change{}
		for i := 0; i < 24; i++ {
			changes = append(changes, ontology.Change{Record: testTopic(fmt.Sprintf("PostgreSQL memory %d", i))})
		}
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("vocabulary", 0, changes...))
		require.NoError(t, err)
		contextData, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{source})
		require.NoError(t, err)
		require.Len(t, contextData.Candidates, 20)
		service, _ := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
			require.Len(t, request.Definitions, 20)
			require.Empty(t, response.Definitions)
		})
		_, err = service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "reuse", Sources: []ontology.SourceHandle{source}})
		require.NoError(t, err)
		page, err := f.store.ListRecords(context.Background(), f.team, ontology.Topic, "", 200)
		require.NoError(t, err)
		require.Len(t, page.Records, 24)
	})
	t.Run("private and old generation", func(t *testing.T) {
		f := newOrganizationFixture(t)
		private := &domain.Credential{ID: uuid.New(), TeamID: uuid.MustParse(f.team), Name: "private", KeyHash: "synthetic-private", KeyPrefix: uuid.NewString()[:24], KeySuffix: "test", Scopes: []string{"read", "write"}, MemoryBinding: domain.CredentialBindingCredentialPrivate}
		require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(context.Background(), private))
		privateCtx := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{{ID: private.MemorySpaceID, Kind: domain.MemorySpaceCredentialPrivate, Generation: private.MemorySpaceGeneration}})
		stored, err := f.knowledge.CreateIngestForTest(privateCtx, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: private.ID.String(), SpaceID: private.MemorySpaceID.String(), SpaceGeneration: private.MemorySpaceGeneration, Evidence: []knowledge.EvidenceInput{{Content: "Private source"}}})
		require.NoError(t, err)
		service, calls := organizationFixtureService(t, f, nil, nil)
		privateHandle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: stored.Evidence[0].FragmentID, Version: 1}
		result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "private", Sources: []ontology.SourceHandle{privateHandle}})
		require.NoError(t, err)
		require.Equal(t, "unavailable", result.Outcomes[0].Status)
		require.Zero(t, calls.Load())
		source := f.organizationEvidence(t, 0, "Shared old generation", nil)
		require.NoError(t, f.admin.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND kind='team_shared'`, f.team).Error)
		result, err = service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "old-generation", Sources: []ontology.SourceHandle{source}})
		require.NoError(t, err)
		require.Equal(t, "unavailable", result.Outcomes[0].Status)
		require.Zero(t, calls.Load())
	})
	t.Run("override arrives during assessment", func(t *testing.T) {
		f := newOrganizationFixture(t)
		a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		b := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
		before := f.canonicalSnapshot(t)
		service, _ := organizationFixtureService(t, f, func(assessment.Item, assessment.Item) bool { return true }, func(request assessment.Request, response *assessment.Response) {
			separation := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.KeepSeparate, Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
			_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("concurrent-separation", 0, ontology.Change{Record: separation}))
			require.NoError(t, err)
		})
		result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "stale-override", Sources: []ontology.SourceHandle{a, b}})
		require.Error(t, err)
		require.Equal(t, "stale_input", result.FailureCode)
		page, err := f.store.ListRecords(context.Background(), f.team, ontology.EvidenceGroup, "", 20)
		require.NoError(t, err)
		require.Empty(t, page.Records)
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
}
