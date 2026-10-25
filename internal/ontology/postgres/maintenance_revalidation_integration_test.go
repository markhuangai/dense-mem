//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMaintenanceRevalidationChurn(t *testing.T) {
	t.Run("historical broad contexts remain immutable", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		first, alternative := testTopic("postgresql"), testTopic("atlas-alternative")
		alternative.Definition.Description = "PostgreSQL storage for Atlas"
		revalidationPublish(t, f, ontology.Change{Record: first}, ontology.Change{Record: alternative})
		service, calls := organizationFixtureService(t, f, nil, nil)
		input := ontology.OrganizationInput{OperationKey: "current-context", Sources: []ontology.SourceHandle{source}}
		_, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		var legacy ontology.OrganizationReceipt
		require.NoError(t, f.store.withScope(context.Background(), f.team, false, func(tx *gorm.DB, fence scope) error {
			var err error
			legacy, _, err = organizationByKey(tx, fence, input.OperationKey)
			if err != nil {
				return err
			}
			contextData, err := f.store.organizationContext(tx, fence, input.Sources)
			if err != nil {
				return err
			}
			require.Len(t, contextData.Candidates, 1)
			contextData.Candidates, err = f.store.organizationVocabulary(tx, fence, contextData.Sources, []string{contextData.Candidates[0].ID})
			if err != nil {
				return err
			}
			require.Len(t, contextData.Candidates, 2)
			legacy.OperationKey = "legacy-context"
			legacy.Dependencies, err = ontology.OrganizationDependencies(contextData)
			if err != nil {
				return err
			}
			legacy.ContextHash, err = ontology.OrganizationContextHash(contextData)
			if err != nil {
				return err
			}
			return insertOrganization(tx, fence, legacy)
		}))
		before := f.canonicalSnapshot(t)
		input.OperationKey = legacy.OperationKey
		replay, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, replay.Existing)
		require.False(t, replay.Current)
		input.OperationKey = "fresh-context"
		fresh, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, fresh.Current)
		require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
			stored, found, err := organizationByKey(tx, fence, legacy.OperationKey)
			require.True(t, found)
			require.Equal(t, legacy, stored)
			return err
		}))
		require.Equal(t, int32(1), calls.Load())
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("fixed assignments discard unused alternatives", func(t *testing.T) {
		f := newOrganizationFixture(t)
		started := time.Now()
		t.Cleanup(func() { revalidationReport(t, f, started) })
		source := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL relational storage.", nil)
		first, alternative := testTopic("postgresql"), testTopic("atlas-alternative")
		alternative.Definition.Description = "Atlas PostgreSQL relational storage"
		revalidationPublish(t, f, ontology.Change{Record: first}, ontology.Change{Record: alternative})
		before := f.canonicalSnapshot(t)
		service, calls := organizationFixtureService(t, f, nil, nil)
		input := ontology.OrganizationInput{OperationKey: "fixed-original", Sources: []ontology.SourceHandle{source}}
		original, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		contextData, err := f.store.ReadOrganization(context.Background(), f.team, input.Sources)
		require.NoError(t, err)
		require.Len(t, contextData.Candidates, 1, "fixed choices must not retain unused vocabulary")
		selected := contextData.Candidates[0].ID
		unused := first
		if unused.ID == selected {
			unused = alternative
		}
		unused.Definition.Description += " revised unrelated description"
		revalidationPublish(t, f, ontology.Change{ExpectedVersion: 1, Record: unused})
		added := testTopic("atlas-postgresql-addition")
		revalidationPublish(t, f, ontology.Change{Record: added})
		for _, key := range []string{input.OperationKey, "fixed-after-vocabulary"} {
			input.OperationKey = key
			replay, err := service.Organize(context.Background(), f.team, input)
			require.NoError(t, err)
			require.True(t, replay.Current)
			require.True(t, replay.Existing)
			require.Equal(t, original.AssessmentID, replay.AssessmentID)
		}
		require.Equal(t, int32(1), calls.Load())
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("native discovery drains unrelated vocabulary without reassessment", func(t *testing.T) {
		f := newOrganizationFixture(t)
		started := time.Now()
		t.Cleanup(func() { revalidationReport(t, f, started) })
		sources := []ontology.SourceHandle{f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil), f.organizationEvidence(t, 1, "Atlas relies on PostgreSQL.", nil)}
		before := f.canonicalSnapshot(t)
		config := maintenanceSettings(t, f)
		service, calls := maintenanceServiceFixture(t, f, config, nil)
		drainMaintenance(t, service)
		originalCalls := calls()
		original := map[string]string{}
		for _, source := range sources {
			state := revalidationQueue(t, f, source)
			require.Equal(t, "organized", state.Status)
			original[source.ID] = state.AssessmentID
		}
		for index := range 3 {
			topic := testTopic(fmt.Sprintf("atlas-vocabulary-%d", index))
			topic.Definition.Description = "PostgreSQL database storage for Atlas"
			revalidationPublish(t, f, ontology.Change{Record: topic})
			drainMaintenance(t, service)
			for _, source := range sources {
				state := revalidationQueue(t, f, source)
				require.Equal(t, "organized", state.Status)
				assert.Equal(t, original[source.ID], state.AssessmentID, "unused catalog additions must not prepare a new assessment")
			}
		}
		status, err := service.Status(context.Background())
		require.NoError(t, err)
		require.True(t, status.CoverageComplete)
		require.EqualValues(t, len(sources), status.Counts.Organized)
		var markers int
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_markers WHERE team_id=?::uuid`, f.team).Row().Scan(&markers))
		require.Zero(t, markers)
		require.Equal(t, before, f.canonicalSnapshot(t))
		require.Equal(t, originalCalls, calls())
	})
	t.Run("unresolved and mixed choices retain relevant candidates", func(t *testing.T) {
		f := newOrganizationFixture(t)
		fixed := f.organizationEvidence(t, 0, "Atlas PostgreSQL.", nil)
		free := f.organizationEvidence(t, 1, "Vega Swift.", nil)
		service, calls := organizationFixtureService(t, f, nil, nil)
		_, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "fixed", Sources: []ontology.SourceHandle{fixed}})
		require.NoError(t, err)
		ambiguous, ambiguousCalls := organizationFixtureService(t, f, nil, ambiguousClassification)
		input := ontology.OrganizationInput{OperationKey: "unresolved", Sources: []ontology.SourceHandle{free}}
		original, err := ambiguous.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		both := []ontology.SourceHandle{fixed, free}
		read, err := f.store.ReadOrganization(context.Background(), f.team, both)
		require.NoError(t, err)
		hash, err := ontology.OrganizationContextHash(read)
		require.NoError(t, err)
		revalidationPublish(t, f, ontology.Change{Record: testTopic("atlas-postgresql-added")})
		unchanged, err := f.store.ReadOrganization(context.Background(), f.team, both)
		require.NoError(t, err)
		same, err := ontology.OrganizationContextHash(unchanged)
		require.NoError(t, err)
		require.Equal(t, hash, same, "fixed-source words must not add candidates to a mixed batch")
		added := testTopic("vega-swift-added")
		revalidationPublish(t, f, ontology.Change{Record: added})
		relevant, err := f.store.ReadOrganization(context.Background(), f.team, both)
		require.NoError(t, err)
		relevantHash, err := ontology.OrganizationContextHash(relevant)
		require.NoError(t, err)
		require.NotEqual(t, hash, relevantHash, "unresolved-source vocabulary must change a mixed batch context")
		candidateIDs := []string{}
		for _, candidate := range relevant.Candidates {
			candidateIDs = append(candidateIDs, candidate.ID)
		}
		require.Contains(t, candidateIDs, added.ID)
		require.Equal(t, read.Records, relevant.Records, "the fixed classification must remain intact")
		stale, err := ambiguous.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.False(t, stale.Current)
		require.Equal(t, original.AssessmentID, stale.AssessmentID)
		input.OperationKey = "reassess-relevant-addition"
		fresh, err := ambiguous.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, fresh.Current)
		require.NotEqual(t, original.AssessmentID, fresh.AssessmentID)
		require.Equal(t, int32(2), ambiguousCalls.Load())
		require.Equal(t, int32(1), calls.Load())
	})
	t.Run("manager choices remain fixed but relevant edits invalidate", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 1, "Atlas PostgreSQL.", nil)
		topic := testTopic("manager-storage")
		override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: topic.ID, Members: []ontology.SourceHandle{source}}, Sources: []ontology.SourceDependency{f.source(t, source)}}
		revalidationPublish(t, f, ontology.Change{Record: topic}, ontology.Change{Record: override})
		before := f.canonicalSnapshot(t)
		service, calls := organizationFixtureService(t, f, nil, nil)
		input := ontology.OrganizationInput{OperationKey: "manager-choice", Sources: []ontology.SourceHandle{source}}
		original, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		revalidationPublish(t, f, ontology.Change{Record: testTopic("atlas-postgresql-added")})
		input.OperationKey = "manager-choice-replay"
		replay, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.True(t, replay.Current)
		require.Equal(t, original.AssessmentID, replay.AssessmentID)
		require.Zero(t, calls.Load())
		topic.Definition.Description = "Changed meaning of the required definition"
		revalidationPublish(t, f, ontology.Change{ExpectedVersion: 1, Record: topic})
		stale, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.False(t, stale.Current)
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("vocabulary changes during dispatched provider work", func(t *testing.T) {
		f := newOrganizationFixture(t)
		a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		b := f.organizationEvidence(t, 1, "Atlas relies on PostgreSQL.", nil)
		before := f.canonicalSnapshot(t)
		initial, _ := organizationFixtureService(t, f, nil, nil)
		for _, source := range []ontology.SourceHandle{a, b} {
			_, err := initial.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: source.ID, Sources: []ontology.SourceHandle{source}})
			require.NoError(t, err)
		}
		config := concurrencyConfig(t, f, 2, 2)
		window := maintenanceWindow(t, f, config)
		_, claim := maintenanceClaim(t, f, window)
		arrived, release := make(chan struct{}, 1), make(chan struct{})
		var releaseOnce sync.Once
		releaseProvider := func() { releaseOnce.Do(func() { close(release) }) }
		defer releaseProvider()
		service, calls := organizationFixtureService(t, f, nil, func(_ assessment.Request, _ *assessment.Response) { arrived <- struct{}{}; <-release })
		type answer struct {
			result ontology.OrganizationResult
			err    error
		}
		finished := make(chan answer, 1)
		go func() {
			result, err := service.Organize(ontology.WithMaintenanceClaim(context.Background(), *claim), f.team, ontology.OrganizationInput{OperationKey: "maintenance:" + claim.ID, Sources: claim.Sources})
			finished <- answer{result, err}
		}()
		select {
		case <-arrived:
		case <-time.After(10 * time.Second):
			releaseProvider()
			t.Fatal("provider did not start")
		}
		revalidationPublish(t, f, ontology.Change{Record: testTopic("atlas-postgresql-added")})
		revalidationDiscover(t, f, window)
		releaseProvider()
		received := <-finished
		require.NoError(t, received.err)
		require.True(t, received.result.Current)
		for _, source := range claim.Sources {
			require.Greater(t, revalidationQueue(t, f, source).Revision, claim.Revisions[ontology.SourceKey(source)])
		}
		require.NoError(t, f.store.CompleteMaintenanceBatch(context.Background(), *claim, received.result, "", time.Now().UTC()))
		for _, source := range claim.Sources {
			state := revalidationQueue(t, f, source)
			require.Equal(t, "organized", state.Status)
			require.Equal(t, received.result.AssessmentID, state.AssessmentID)
		}
		require.Equal(t, int32(1), calls.Load())
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
}
