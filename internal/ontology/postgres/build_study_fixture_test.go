//go:build integration && ontology_study

package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingsdb "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"github.com/stretchr/testify/require"
	gormpg "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type buildStudyShape struct {
	Dataset                                                                                  string
	Eligible, Organized, DefinitionMarkers, ClaimEntries, DistinctClaimSources, TypicalBatch int
}

var buildStudyShapes = []buildStudyShape{
	{"hotpotqa", 13810, 4228, 4339, 1503, 356, 13},
	{"musique", 16870, 4033, 4128, 1619, 400, 14},
	{"2wikimultihopqa", 8829, 3757, 1088, 818, 251, 8},
	{"nq_rear", 20166, 5167, 4332, 1629, 721, 19},
	{"popqa", 26315, 7029, 4110, 1818, 855, 18},
}

type buildStudySeed struct {
	fixture                  *ontologyFixture
	shape                    buildStudyShape
	canonical                string
	sources                  []ontology.SourceHandle
	classByID                map[string]string
	correctionTopic          ontology.Record
	override                 ontology.Record
	classes, startingMarkers int
	backgroundTopics         int
}

func studyShape(t *testing.T) buildStudyShape {
	t.Helper()
	for _, shape := range buildStudyShapes {
		if shape.Dataset == os.Getenv("ONTOLOGY_STUDY_DATASET") {
			return shape
		}
	}
	t.Fatal("select a registered study dataset")
	return buildStudyShape{}
}

func studyClass(class int) string {
	return fmt.Sprintf("studyfacet%02d", class)
}

func (s *buildStudySeed) addEvidence(t *testing.T, count int) {
	t.Helper()
	f := s.fixture
	for range count {
		index := len(s.sources)
		class := studyClass(index % s.classes)
		text := fmt.Sprintf("%s specimen%05d belongs to collection%02d.", class, index, index%s.classes)
		source := f.organizationEvidence(t, index%len(f.owners), text, nil)
		s.sources = append(s.sources, source)
		s.classByID[source.ID] = class
	}
}

// The response uses only the actual provider request, including its current symbol bindings.
func studyClassification(request assessment.Request, response *assessment.Response) {
	definitions := make(map[string]string)
	for _, def := range request.Definitions {
		definitions[def.Key] = def.Ref
	}
	items := make(map[string]assessment.Item)
	for _, item := range request.Items {
		items[item.Ref] = item
	}
	response.Definitions = []assessment.Definition{}
	for i, decision := range response.Items {
		item := items[decision.Ref]
		class := strings.Fields(item.Text)[0]
		ref := definitions[class]
		if ref == "" {
			ref = "new-" + class
			definitions[class] = ref
			response.Definitions = append(response.Definitions, assessment.Definition{
				Ref: ref, Kind: ontology.Topic, Key: class, Label: class, Aliases: []string{},
			})
		}
		response.Items[i].DefinitionRef = ref
	}
}

func buildStudySettings(f *ontologyFixture) *settings.AppConfigServiceImpl {
	return settings.NewAppConfigService(settingsdb.NewAppConfigRepository(f.app, f.rls), nil)
}

func buildStudyTemplate(t *testing.T, shape buildStudyShape) *buildStudySeed {
	t.Helper()
	f := newOrganizationFixture(t)
	config := concurrencyConfig(t, f, 8, 8)
	_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{
		domain.AppConfigOntologyContextWindow: "500000",
	}, "control", "", "")
	require.NoError(t, err)
	seed := &buildStudySeed{fixture: f, shape: shape, classByID: map[string]string{},
		classes: max(3, shape.TypicalBatch/2), backgroundTopics: max(256, shape.DefinitionMarkers/4)}
	var topics []ontology.Change
	for i := range seed.classes {
		topic := testTopic(studyClass(i))
		topics = append(topics, ontology.Change{Record: topic})
	}
	for i := range seed.backgroundTopics {
		topic := testTopic(fmt.Sprintf("archivedstudy%05d", i))
		topic.Definition.Description = fmt.Sprintf("Separated catalogue family historical%05d", i)
		topics = append(topics, ontology.Change{Record: topic})
	}
	for group := range slices.Chunk(topics, ontology.MaxChanges) {
		revalidationPublish(t, f, group...)
	}
	seed.addEvidence(t, max(8, shape.Organized/64))
	service, _ := maintenanceServiceFixture(t, f, config, studyClassification)
	for turns := 0; ; turns++ {
		status, err := f.store.MaintenanceStatus(context.Background(), time.Now())
		require.NoError(t, err)
		if status.CoverageComplete && int(status.Counts.Organized) == len(seed.sources) {
			break
		}
		require.Less(t, turns, shape.Eligible, "template must converge")
		_, err = service.RunTurn(context.Background())
		require.NoError(t, err)
	}
	seed.addEvidence(t, shape.Eligible/64-len(seed.sources))
	window := maintenanceWindow(t, f, config)
	turn, err := f.store.ClaimMaintenanceTurn(context.Background(), window.ID, time.Now(), 30*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	for count := 0; ; count++ {
		status, err := f.store.MaintenanceStatus(context.Background(), time.Now())
		require.NoError(t, err)
		if status.DiscoveryComplete && int(status.Counts.Eligible) == len(seed.sources) {
			break
		}
		require.Less(t, count, len(seed.sources))
		_, err = f.store.DiscoverMaintenance(context.Background(), *turn, ontology.MaintenancePageSize)
		require.NoError(t, err)
	}
	require.NoError(t, f.store.ReleaseMaintenanceTurn(context.Background(), *turn))
	seed.startingMarkers = max(4, shape.DefinitionMarkers/64)
	markers := make([]ontology.Change, 0, seed.startingMarkers)
	for i := range seed.startingMarkers {
		topic := testTopic(fmt.Sprintf("studyupdate%05d", i))
		// Preserve the observed repeated-to-distinct claim ratio as a synthetic overlap parameter.
		width := 1
		if i%2 == 0 {
			width = min(seed.classes, (shape.ClaimEntries+shape.DistinctClaimSources-1)/shape.DistinctClaimSources)
		}
		var words []string
		for j := range width {
			words = append(words, studyClass((i+j)%seed.classes))
		}
		topic.Definition.Description = strings.Join(words, " ")
		markers = append(markers, ontology.Change{Record: topic})
	}
	for group := range slices.Chunk(markers, ontology.MaxChanges) {
		revalidationPublish(t, f, group...)
	}
	seed.correctionTopic = testTopic("correctedstudyfacet")
	target := seed.sources[0]
	seed.override = ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind,
		Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: seed.correctionTopic.ID, Members: []ontology.SourceHandle{target}},
		Sources:  []ontology.SourceDependency{f.source(t, target)}}
	require.NoError(t, f.admin.Exec("ANALYZE").Error)
	seed.canonical = schedulerCanonical(t, f)
	return seed
}

type buildStudyCloner struct {
	controller       *sql.DB
	adminURL, appURL *url.URL
	seed             *buildStudySeed
	next             int
}

func newBuildStudyCloner(t *testing.T, seed *buildStudySeed) *buildStudyCloner {
	t.Helper()
	c := &buildStudyCloner{seed: seed}
	for _, part := range []struct {
		db   *gorm.DB
		dest **url.URL
	}{
		{seed.fixture.admin, &c.adminURL}, {seed.fixture.app, &c.appURL},
	} {
		connection, err := url.Parse(part.db.Dialector.(*gormpg.Dialector).DSN)
		require.NoError(t, err)
		*part.dest = connection
		pool, err := part.db.DB()
		require.NoError(t, err)
		require.NoError(t, pool.Close())
	}
	require.Equal(t, "/ontology_test", c.adminURL.Path)
	maintenance := *c.adminURL
	maintenance.Path = "/postgres"
	db, err := sql.Open("pgx", maintenance.String())
	require.NoError(t, err)
	c.controller = db
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return c
}

func (c *buildStudyCloner) clone(t *testing.T) *ontologyFixture {
	t.Helper()
	c.next++
	name := fmt.Sprintf("ontology_study_%d", c.next)
	_, err := c.controller.Exec("CREATE DATABASE " + name + " TEMPLATE ontology_test")
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := c.controller.Exec("DROP DATABASE " + name)
		require.NoError(t, err)
	})
	open := func(original *url.URL) *gorm.DB {
		connection := *original
		connection.Path = "/" + name
		db, err := gorm.Open(gormpg.Open(connection.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Error)})
		require.NoError(t, err)
		raw, err := db.DB()
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, raw.Close()) })
		return db
	}
	f := *c.seed.fixture
	f.admin, f.app = open(c.adminURL), open(c.appURL)
	f.store, f.knowledge = NewStore(f.app, f.rls), knowledge.NewStore(f.app, f.rls)
	require.Equal(t, c.seed.canonical, schedulerCanonical(t, &f))
	return &f
}
