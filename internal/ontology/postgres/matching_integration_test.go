//go:build integration

package postgres

import (
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestOntologyMatchingVocabularyAndInvalidation(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	foreign := maintenanceOtherTeam(t, f)
	foreignSource := foreign.organizationEvidence(t, 0, "heliostat about and x 東京", nil)
	foreignDefinition := testTopic("foreigncatalog")
	foreignDefinition.Definition.Description = "heliostat x 東京"
	foreignDefinition.Definition.Aliases = []string{"about", "and"}
	revalidationPublish(t, foreign, ontology.Change{Record: foreignDefinition})
	revalidationRefresh(t, foreign, foreignSource)
	foreignBefore := foreign.canonicalSnapshot(t)
	type matchingCase struct {
		key, description, text string
		aliases                []string
		matched                bool
	}
	cases := []matchingCase{
		{key: "grammarnoise", description: "of and the", text: "of and the"},
		{key: "domaincatalog", description: "heliostat and", text: "heliostat of", matched: true},
		{key: "about", text: "about", matched: true},
		{key: "aliascatalog", aliases: []string{"and"}, text: "and", matched: true},
		{key: "databasecatalog", aliases: []string{"psql"}, text: "psql", matched: true},
		{key: "q", text: "q", matched: true},
		{key: "shortcatalog", description: "x", text: "x", matched: true},
		{key: "internationalcatalog", description: "東京 Über", text: "東京 ÜBER", matched: true},
		{key: "nostemming", description: "databases", text: "database"},
	}
	var definitions []ontology.Record
	for index, testCase := range cases {
		t.Run(testCase.key, func(t *testing.T) {
			source := f.organizationEvidence(t, index%len(f.owners), testCase.text, nil)
			before := f.canonicalSnapshot(t)
			definition := testTopic(testCase.key)
			definition.Definition.Description = testCase.description
			definition.Definition.Aliases = testCase.aliases
			page, err := f.store.ListRecords(ctx, f.team, "", "", 1)
			require.NoError(t, err)
			_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(uuid.NewString(), page.Revision, ontology.Change{Record: definition}))
			require.NoError(t, err)
			definitions = append(definitions, definition)
			contextData, err := f.store.ReadOrganization(ctx, f.team, []ontology.SourceHandle{source})
			require.NoError(t, err)
			require.False(t, slices.ContainsFunc(contextData.Candidates, func(view ontology.RecordView) bool { return view.ID == foreignDefinition.ID }))
			require.Equal(t, testCase.matched, slices.ContainsFunc(contextData.Candidates, func(view ontology.RecordView) bool { return view.ID == definition.ID }))
			require.NoError(t, f.store.withScope(ctx, f.team, false, func(tx *gorm.DB, fence scope) error {
				if err := refreshMaintenanceSource(tx, fence, source, true); err != nil {
					return err
				}
				marker := maintenanceMarker{AnchorKind: "ontology_record_heads", AnchorID: definition.ID, TargetKind: "definition", TargetID: definition.ID}
				individual, err := maintenanceMarkerSources(tx, fence, marker, ontology.MaintenancePageSize)
				if err != nil {
					return err
				}
				grouped, err := maintenanceDefinitionGroupPage(tx, fence, []maintenanceMarker{marker}, ontology.MaintenancePageSize)
				if err != nil {
					return err
				}
				require.ElementsMatch(t, individual, grouped.Sources)
				require.NotContains(t, individual, foreignSource)
				require.Equal(t, testCase.matched, slices.Contains(individual, source), "lookup and lexical invalidation must agree")
				t.Logf("matching case=%s candidates=%d invalidated_sources=%d", testCase.key, len(contextData.Candidates), len(individual))
				return nil
			}))
			var stored pq.StringArray
			require.NoError(t, f.admin.Raw(`SELECT vocabulary_words FROM ontology_maintenance_sources WHERE team_id=?::uuid AND source_kind=? AND source_id=?`, f.team, source.Kind, source.ID).Row().Scan(&stored))
			_, raw := ontology.VocabularyQuery(contextData.Sources)
			require.Equal(t, raw, []string(stored))
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
	require.Equal(t, foreignBefore, foreign.canonicalSnapshot(t))
	t.Run("empty loose query retains forced and exact paths", func(t *testing.T) {
		require.NoError(t, f.store.withScope(ctx, f.team, true, func(tx *gorm.DB, fence scope) error {
			views, err := f.store.organizationVocabulary(tx, fence, []ontology.SourceSnapshot{{State: map[string]string{"content": "about and of"}}}, []string{definitions[0].ID})
			require.NoError(t, err)
			require.Len(t, views, 3)
			require.Equal(t, definitions[0].ID, views[0].ID, "forced references precede exact names")
			require.ElementsMatch(t, []string{definitions[2].ID, definitions[3].ID}, []string{views[1].ID, views[2].ID})
			views, err = f.store.organizationVocabulary(tx, fence, nil, []string{definitions[0].ID})
			require.NoError(t, err)
			require.Len(t, views, 1)
			require.Equal(t, definitions[0].ID, views[0].ID)
			return nil
		}))
	})
	t.Run("stop words cannot fill the bounded candidate window", func(t *testing.T) {
		page, err := f.store.ListRecords(ctx, f.team, "", "", 1)
		require.NoError(t, err)
		var changes []ontology.Change
		for i := range ontology.MaxVocabularyCandidates {
			record := testTopic(fmt.Sprintf("noisewindow%d", i))
			record.Definition.Description = "of of of and and the the"
			changes = append(changes, ontology.Change{Record: record})
		}
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("noise-window", page.Revision, changes...))
		require.NoError(t, err)
		source := f.organizationEvidence(t, 1, "heliostat of the", nil)
		contextData, err := f.store.ReadOrganization(ctx, f.team, []ontology.SourceHandle{source})
		require.NoError(t, err)
		require.Len(t, contextData.Candidates, 1)
		require.Equal(t, definitions[1].ID, contextData.Candidates[0].ID)
	})
}
