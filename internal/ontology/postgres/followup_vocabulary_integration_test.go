//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestOntologyVocabularyUsesCurrentCandidateWindow(t *testing.T) {
	for _, c := range []struct {
		name           string
		stale, current int
		bounded        bool
	}{
		{"stale heads do not consume slots", 20, 1, false},
		{"twenty current candidates retain stable rank", 20, 21, false},
		{"traversal bound remains visible", ontology.MaxDependencyRecords, 1, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newOrganizationFixture(t)
			current := seedStaleVocabulary(t, f, c.stale, c.current)
			source := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL relational storage.", nil)
			before := f.canonicalSnapshot(t)
			input := []ontology.SourceHandle{source}
			contextData, err := f.store.ReadOrganization(context.Background(), f.team, input)
			if c.bounded {
				require.ErrorIs(t, err, ontology.ErrContextBound)
				return
			}
			require.NoError(t, err)
			require.Len(t, contextData.Candidates, min(c.current, ontology.MaxVocabularyCandidates))
			for index, view := range contextData.Candidates {
				require.True(t, view.Current)
				require.Equal(t, current[index].ID, view.ID)
			}
			repeated, err := f.store.ReadOrganization(context.Background(), f.team, input)
			require.NoError(t, err)
			require.Equal(t, contextData.Candidates, repeated.Candidates)
			service, calls := organizationFixtureService(t, f, nil, nil)
			result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "reuse-current", Sources: input})
			require.NoError(t, err)
			require.NotNil(t, result.Publication)
			assignments, err := f.store.ListRecords(context.Background(), f.team, ontology.AssignmentKind, "", 20)
			require.NoError(t, err)
			require.Len(t, assignments.Records, 1)
			require.Equal(t, current[0].ID, assignments.Records[0].Assignment.DefinitionID)
			count := calls.Load()
			replay, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "reuse-current-again", Sources: input})
			require.NoError(t, err)
			require.True(t, replay.Existing)
			require.Equal(t, count, calls.Load())
			if c.current > ontology.MaxVocabularyCandidates {
				override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: current[len(current)-1].ID, Members: input}, Sources: []ontology.SourceDependency{f.source(t, source)}}
				_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("manager-precedence", result.Publication.Revision, ontology.Change{Record: override}))
				require.NoError(t, err)
				forced, err := f.store.ReadOrganization(context.Background(), f.team, input)
				require.NoError(t, err)
				require.Len(t, forced.Candidates, 20)
				require.Equal(t, current[len(current)-1].ID, forced.Candidates[0].ID)
			}
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
}

func TestOntologyVocabularyWindowPreservesTeamIsolationAndNameReuse(t *testing.T) {
	f := newOrganizationFixture(t)
	current := seedStaleVocabulary(t, f, 20, 1)[0]
	other := maintenanceOtherTeam(t, f)
	foreign := testTopic("foreign-postgresql")
	foreign.Definition.Description = "PostgreSQL relational storage"
	_, err := other.store.PublishManager(other.actor(0, "manager"), other.team, testPublication("foreign", 0, ontology.Change{Record: foreign}))
	require.NoError(t, err)
	source := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL relational storage.", nil)
	contextData, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{source})
	require.NoError(t, err)
	require.Len(t, contextData.Candidates, 1)
	require.Equal(t, current.ID, contextData.Candidates[0].ID)
	reuse := testTopic("reused-concept")
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("name-reuse-target", contextData.Revision, ontology.Change{Record: reuse}))
	require.NoError(t, err)
	service, _ := organizationFixtureService(t, f, nil, func(_ assessment.Request, response *assessment.Response) {
		response.Definitions = []assessment.Definition{{Ref: "same-normalized-name", Kind: ontology.Topic, Key: " " + strings.ToUpper(reuse.Definition.Key) + " ", Label: strings.ToUpper(reuse.Definition.Label), Aliases: []string{}}}
		for i := range response.Items {
			response.Items[i].DefinitionRef = "same-normalized-name"
		}
	})
	before := f.canonicalSnapshot(t)
	_, err = service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "normalized-name", Sources: []ontology.SourceHandle{source}})
	require.NoError(t, err)
	assignments, err := f.store.ListRecords(context.Background(), f.team, ontology.AssignmentKind, "", 20)
	require.NoError(t, err)
	require.Len(t, assignments.Records, 1)
	require.Equal(t, reuse.ID, assignments.Records[0].Assignment.DefinitionID)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyFollowupCohort(t *testing.T) {
	testOntologyCohort(t, true, evalharness.OntologyOrganizationFollowupCohort())
}
