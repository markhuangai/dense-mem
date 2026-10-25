//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceCurrentReceiptCompletion(t *testing.T) {
	for _, testCase := range []struct{ name, change, expected string }{
		{"unchanged assessed inputs", "", "organized"},
		{"ambiguous comparison remains ambiguous", "ambiguous", "ambiguous"},
		{"source withdrawn after receipt", "source", "unavailable"},
		{"definition support withdrawn after receipt", "support", "pending"},
		{"required definition revised after receipt", "definition", "pending"},
		{"required definition retired after receipt", "retired", "pending"},
		{"classification override after receipt", "override", "pending"},
		{"grouping override after receipt", "grouping", "pending"},
		{"expired worker cannot complete", "expired", "pending"},
		{"different receipt identity cannot complete", "identity", "pending"},
		{"receipt from another operation cannot complete", "operation", "pending"},
		{"failed result cannot replace pending work", "failure", "pending"},
		{"historical generation cannot complete", "generation", "pending"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			f := newOrganizationFixture(t)
			started := time.Now()
			t.Cleanup(func() { revalidationReport(t, f, started) })
			a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL storage.", nil)
			b := f.organizationEvidence(t, 1, "Atlas relies on PostgreSQL storage.", nil)
			config := concurrencyConfig(t, f, 2, 2)
			window := maintenanceWindow(t, f, config)
			_, claim := maintenanceClaim(t, f, window)
			require.ElementsMatch(t, []ontology.SourceHandle{a, b}, claim.Sources)
			topic := testTopic("postgresql")
			var support ontology.SourceHandle
			if testCase.change == "support" {
				support = f.organizationEvidence(t, 0, "PostgreSQL definition support.", nil)
				topic.Sources = []ontology.SourceDependency{f.source(t, support)}
			}
			revalidationPublish(t, f, ontology.Change{Record: topic})
			revalidationDiscover(t, f, window)
			for _, source := range claim.Sources {
				require.Greater(t, revalidationQueue(t, f, source).Revision, claim.Revisions[ontology.SourceKey(source)], "definition discovery must overlap the dispatched claim")
			}
			service, calls := organizationFixtureService(t, f, nil, func(_ assessment.Request, response *assessment.Response) {
				if testCase.change == "ambiguous" {
					response.Equivalence[0].Relation = "ambiguous"
				}
			})
			key := "maintenance:" + claim.ID
			if testCase.change == "operation" {
				key = "different-operation"
			}
			ctx := context.Background()
			result, err := service.Organize(ontology.WithMaintenanceClaim(ctx, *claim), f.team, ontology.OrganizationInput{OperationKey: key, Sources: claim.Sources})
			require.NoError(t, err)
			require.True(t, result.Current)
			require.Equal(t, int32(1), calls.Load())
			if testCase.change == "ambiguous" {
				result.AmbiguousComparisons = nil
				for i := range result.Outcomes {
					result.Outcomes[i].Status = "organized"
				}
			}
			switch testCase.change {
			case "source":
				_, err := f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{a.ID}, Reason: "newer source withdrawal", IdempotencyKey: uuid.NewString(), RequestHash: testHash("newer source withdrawal")})
				require.NoError(t, err)
			case "support":
				_, err := f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{support.ID}, Reason: "withdraw definition support", IdempotencyKey: uuid.NewString(), RequestHash: testHash("withdraw definition support")})
				require.NoError(t, err)
			case "definition":
				topic.Definition.Description = "A changed required storage definition"
				revalidationPublish(t, f, ontology.Change{ExpectedVersion: 1, Record: topic})
			case "retired":
				view, err := f.store.GetRecord(ctx, f.team, topic.ID, 0)
				require.NoError(t, err)
				view.Record.Retired = true
				revalidationPublish(t, f, ontology.Change{ExpectedVersion: view.Version, Record: view.Record})
			case "override":
				other := testTopic("manager-replacement")
				override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: other.ID, Members: []ontology.SourceHandle{a}}, Sources: []ontology.SourceDependency{f.source(t, a)}}
				revalidationPublish(t, f, ontology.Change{Record: other}, ontology.Change{Record: override})
			case "grouping":
				override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.KeepSeparate, Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
				revalidationPublish(t, f, ontology.Change{Record: override})
			case "expired":
				require.NoError(t, f.admin.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second' WHERE batch_id=?::uuid`, claim.ID).Error)
			case "identity":
				result.AssessmentID = uuid.NewString()
			case "failure":
				result.Current = false
				result.FailureCode = "provider_response_invalid"
			case "generation":
				require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
					return tx.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE id=?::uuid AND team_id=?::uuid`, f.space, f.team).Error
				}))
			}
			if testCase.change == "source" || testCase.change == "support" || testCase.change == "definition" || testCase.change == "retired" || testCase.change == "override" || testCase.change == "grouping" {
				for _, source := range claim.Sources {
					revalidationRefresh(t, f, source)
				}
			}
			beforeQueue := revalidationQueue(t, f, a)
			beforeCanonical := f.canonicalSnapshot(t)
			beforeReceipts := revalidationReceiptHistory(t, f)
			err = f.store.CompleteMaintenanceBatch(ctx, *claim, result, "", time.Now().UTC())
			if testCase.change == "expired" {
				require.ErrorIs(t, err, ontology.ErrLeaseLost)
			} else {
				require.NoError(t, err)
			}
			after := revalidationQueue(t, f, a)
			require.Equal(t, testCase.expected, after.Status)
			require.Equal(t, beforeQueue.Revision, after.Revision, "completion must not rewrite a newer revision")
			if testCase.expected == "organized" || testCase.expected == "ambiguous" {
				require.Equal(t, result.AssessmentID, after.AssessmentID)
				require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, result, "", time.Now().UTC()), "completion is idempotent")
			} else {
				require.Equal(t, beforeQueue, after, "newer work must remain untouched")
			}
			require.Equal(t, int32(1), calls.Load())
			require.Equal(t, beforeCanonical, f.canonicalSnapshot(t))
			require.Equal(t, beforeReceipts, revalidationReceiptHistory(t, f))
		})
	}
	t.Run("intervening requeue preserves the observed revision fence", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas PostgreSQL storage.", nil)
		config := concurrencyConfig(t, f, 2, 2)
		window := maintenanceWindow(t, f, config)
		_, claim := maintenanceClaim(t, f, window)
		topic := testTopic("postgresql")
		revalidationPublish(t, f, ontology.Change{Record: topic})
		revalidationDiscover(t, f, window)
		service, calls := organizationFixtureService(t, f, nil, nil)
		result, err := service.Organize(ontology.WithMaintenanceClaim(context.Background(), *claim), f.team, ontology.OrganizationInput{OperationKey: "maintenance:" + claim.ID, Sources: claim.Sources})
		require.NoError(t, err)
		before := revalidationQueue(t, f, source)
		finished := make(chan error, 1)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, f.team, func(tx *gorm.DB) error {
			if err := lockOrganizationCatalog(tx, scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation}); err != nil {
				return err
			}
			go func() { finished <- f.store.CompleteMaintenanceBatch(ctx, *claim, result, "", time.Now().UTC()) }()
			require.Eventually(t, func() bool {
				var blocked bool
				err := f.admin.Raw(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%SELECT revision FROM ontology_catalog_heads%' AND pid<>pg_backend_pid())`).Row().Scan(&blocked)
				return err == nil && blocked
			}, 3*time.Second, 10*time.Millisecond)
			revalidationRefresh(t, f, source)
			return nil
		}))
		require.NoError(t, <-finished)
		after := revalidationQueue(t, f, source)
		require.Greater(t, after.Revision, before.Revision)
		require.Equal(t, "pending", after.Status)
		require.Equal(t, before.AssessmentID, after.AssessmentID)
		require.Equal(t, int32(1), calls.Load())
	})
	t.Run("source scope and foreign claims remain isolated", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 1, "Atlas PostgreSQL shared storage.", nil)
		config := concurrencyConfig(t, f, 3, 2)
		window := maintenanceWindow(t, f, config)
		_, claim := maintenanceClaim(t, f, window)
		revalidationPublish(t, f, ontology.Change{Record: testTopic("postgresql")})
		revalidationDiscover(t, f, window)
		service, _ := organizationFixtureService(t, f, nil, nil)
		result, err := service.Organize(ontology.WithMaintenanceClaim(context.Background(), *claim), f.team, ontology.OrganizationInput{OperationKey: "maintenance:" + claim.ID, Sources: claim.Sources})
		require.NoError(t, err)
		other := maintenanceOtherTeam(t, f)
		foreign := other.organizationEvidence(t, 0, "Atlas PostgreSQL foreign storage.", nil)
		private := f.credentials[0]
		privateContext := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{{ID: private.MemorySpaceID, Kind: domain.MemorySpacePrivate, Generation: private.MemorySpaceGeneration}})
		ingest, err := f.knowledge.CreateIngestForTest(privateContext, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: private.ID.String(), SpaceID: private.MemorySpaceID.String(), SpaceGeneration: private.MemorySpaceGeneration, IdempotencyKey: uuid.NewString(), RequestHash: testHash("private-revalidation"), Evidence: []knowledge.EvidenceInput{{Content: "Atlas PostgreSQL private storage."}}})
		require.NoError(t, err)
		privateSource := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: ingest.Evidence[0].FragmentID, Version: 1}
		read, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{foreign, privateSource})
		require.NoError(t, err)
		for _, snapshot := range read.Sources {
			require.False(t, snapshot.Eligible)
			require.Empty(t, snapshot.State)
		}
		var visible int
		require.NoError(t, f.rls.WithTeamTx(context.Background(), f.app, other.team, func(tx *gorm.DB) error {
			return tx.Raw(`SELECT count(*) FROM ontology_maintenance_sources WHERE team_id=?::uuid`, f.team).Row().Scan(&visible)
		}))
		require.Zero(t, visible)
		before := revalidationQueue(t, f, source)
		for _, mutate := range []func(*ontology.MaintenanceClaim){
			func(c *ontology.MaintenanceClaim) { c.TeamID = other.team },
			func(c *ontology.MaintenanceClaim) { c.SpaceID = other.space },
			func(c *ontology.MaintenanceClaim) { c.Generation++ },
			func(c *ontology.MaintenanceClaim) { c.LeaseToken = uuid.NewString() },
		} {
			wrong := *claim
			mutate(&wrong)
			require.ErrorIs(t, f.store.CompleteMaintenanceBatch(context.Background(), wrong, result, "", time.Now().UTC()), ontology.ErrLeaseLost)
			require.Equal(t, before, revalidationQueue(t, f, source))
		}
		require.NoError(t, f.store.CompleteMaintenanceBatch(context.Background(), *claim, result, "", time.Now().UTC()))
		require.Equal(t, "organized", revalidationQueue(t, f, source).Status)
	})
}

func revalidationReceiptHistory(t *testing.T, f *ontologyFixture) string {
	t.Helper()
	var history string
	require.NoError(t, f.admin.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(receipt) ORDER BY operation_key)::text,'[]') FROM ontology_assessments AS receipt WHERE team_id=?::uuid`, f.team).Row().Scan(&history))
	return history
}
