//go:build integration

package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	evalharness "github.com/markhuangai/dense-mem/internal/testfixture/evaluation"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
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
		{"traversal bound remains visible", topic.MaxDependencyRecords, 1, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newOrganizationFixture(t)
			current := seedStaleVocabulary(t, f, c.stale, c.current)
			source := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL relational storage.", nil)
			before := f.canonicalSnapshot(t)
			input := []topic.SourceHandle{source}
			contextData, err := f.store.ReadOrganization(context.Background(), f.team, input)
			if c.bounded {
				require.ErrorIs(t, err, topic.ErrContextBound)
				return
			}
			require.NoError(t, err)
			require.Len(t, contextData.Candidates, min(c.current, topic.MaxVocabularyCandidates))
			for index, view := range contextData.Candidates {
				require.True(t, view.Current)
				require.Equal(t, current[index].ID, view.ID)
			}
			repeated, err := f.store.ReadOrganization(context.Background(), f.team, input)
			require.NoError(t, err)
			require.Equal(t, contextData.Candidates, repeated.Candidates)
			service, calls := organizationFixtureService(t, f, nil, nil)
			result, err := service.Organize(context.Background(), f.team, topic.OrganizationInput{OperationKey: "reuse-current", Sources: input})
			require.NoError(t, err)
			require.NotNil(t, result.Publication)
			assignments, err := f.store.ListRecords(context.Background(), f.team, topic.AssignmentKind, "", 20)
			require.NoError(t, err)
			require.Len(t, assignments.Records, 1)
			require.Equal(t, current[0].ID, assignments.Records[0].Assignment.DefinitionID)
			count := calls.Load()
			replay, err := service.Organize(context.Background(), f.team, topic.OrganizationInput{OperationKey: "reuse-current-again", Sources: input})
			require.NoError(t, err)
			require.True(t, replay.Existing)
			require.Equal(t, count, calls.Load())
			if c.current > topic.MaxVocabularyCandidates {
				override := topic.Record{ID: uuid.NewString(), Kind: topic.OverrideKind, Override: &topic.Override{Action: topic.SetClassification, DefinitionID: current[len(current)-1].ID, Members: input}, Sources: []topic.SourceDependency{f.source(t, source)}}
				_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("retired-classification", result.Publication.Revision, topic.Change{Record: override}))
				require.ErrorIs(t, err, topic.ErrOverride)
				retained, err := f.store.ReadOrganization(context.Background(), f.team, input)
				require.NoError(t, err)
				require.Equal(t, result.Publication.Revision, retained.Revision)
				require.Len(t, retained.Candidates, 1)
				require.Equal(t, current[0].ID, retained.Candidates[0].ID)
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
	_, err := other.store.PublishManager(other.actor(0, "manager"), other.team, testPublication("foreign", 0, topic.Change{Record: foreign}))
	require.NoError(t, err)
	source := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL relational storage.", nil)
	contextData, err := f.store.ReadOrganization(context.Background(), f.team, []topic.SourceHandle{source})
	require.NoError(t, err)
	require.Len(t, contextData.Candidates, 1)
	require.Equal(t, current.ID, contextData.Candidates[0].ID)
	reuse := testTopic("reused-concept")
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("name-reuse-target", contextData.Revision, topic.Change{Record: reuse}))
	require.NoError(t, err)
	service, _ := organizationFixtureService(t, f, nil, func(_ assessment.Request, response *assessment.Response) {
		response.Definitions = []assessment.Definition{{Ref: "same-normalized-name", Kind: topic.Topic, Key: " " + strings.ToUpper(reuse.Definition.Key) + " ", Label: strings.ToUpper(reuse.Definition.Label), Aliases: []string{}}}
		for i := range response.Items {
			response.Items[i].DefinitionRef = "same-normalized-name"
		}
	})
	before := f.canonicalSnapshot(t)
	_, err = service.Organize(context.Background(), f.team, topic.OrganizationInput{OperationKey: "normalized-name", Sources: []topic.SourceHandle{source}})
	require.NoError(t, err)
	assignments, err := f.store.ListRecords(context.Background(), f.team, topic.AssignmentKind, "", 20)
	require.NoError(t, err)
	require.Len(t, assignments.Records, 1)
	require.Equal(t, reuse.ID, assignments.Records[0].Assignment.DefinitionID)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyFollowupCohort(t *testing.T) {
	testOntologyCohort(t, true, evalharness.OntologyOrganizationFollowupCohort())
}
