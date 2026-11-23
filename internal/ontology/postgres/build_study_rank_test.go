//go:build integration && ontology_study

package postgres

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

var studyRankArms = []string{"base", "vector", "rank-first"}

var studyRankOrders = [][]string{
	{"base", "vector", "rank-first"},
	{"rank-first", "vector", "base"},
	{"vector", "rank-first", "base"},
	{"base", "rank-first", "vector"},
}

var studyPendingArms = []string{"base", "base-skip", "vector", "vector-skip"}

var studyPendingOrders = [][]string{
	{"base", "base-skip", "vector-skip", "vector"},
	{"base-skip", "vector", "base", "vector-skip"},
	{"vector", "vector-skip", "base-skip", "base"},
	{"vector-skip", "base", "vector", "base-skip"},
}

type studyRankPlan struct {
	QuerySHA, BindingsSHA string
	Plan                  studyQueryPlan
}

type studyRankReport struct {
	Protocol, Source, RunID, RunAttempt, Runner  string
	PostgresVersion, TemplateSHA, CorrectionSHA  string
	Stage                                        string
	Shape                                        buildStudyShape
	SeedSources, BackgroundTopics, ChangeMarkers int
	Plans                                        map[string]studyRankPlan
	ScheduledInputs                              map[string][]studyScheduledInput
	Runs                                         []studyTiming
}

func studyRankPlanProbe(t *testing.T, f *ontologyFixture, seed *buildStudySeed, arm string) studyRankPlan {
	t.Helper()
	t.Setenv("ONTOLOGY_STUDY_ACTIVE_ARM", arm)
	installStudyVectors(t, f, arm)
	stats := instrumentStudyQuery(t, f, arm)
	input := seed.sources[len(seed.sources)-2:]
	result, err := f.store.ReadOrganization(context.Background(), f.team, input)
	require.NoError(t, err)
	require.NotEmpty(t, result.Candidates)
	require.Positive(t, stats.calls.Load())
	bound := make([]any, len(stats.exampleArgs))
	for i, argument := range stats.exampleArgs {
		if valuer, ok := argument.(driver.Valuer); ok {
			value, err := valuer.Value()
			require.NoError(t, err)
			argument = value
		}
		bound[i] = argument
	}
	plan := explainStudyQuery(t, f, stats)
	if arm == "rank-first" {
		require.True(t, plan.PageBeforePayload, "actual plan must rank and limit before reading revision payloads")
		require.Positive(t, plan.RankedPageRows)
		require.Positive(t, plan.PayloadRowsVisited)
	}
	return studyRankPlan{studyBoundLookupSHA(stats.exampleSQL), studyHash(t, bound), plan}
}

func runRankFirstComparison(t *testing.T, shape buildStudyShape) {
	t.Helper()
	runMatchedLookupComparison(t, shape, "ontology-build-rank-first-v1", studyRankArms, studyRankOrders)
}

func runPendingRefreshComparison(t *testing.T, shape buildStudyShape) {
	t.Helper()
	runMatchedLookupComparison(t, shape, "ontology-build-pending-refresh-v1", studyPendingArms, studyPendingOrders)
}

func runMatchedLookupComparison(t *testing.T, shape buildStudyShape, protocol string, arms []string, orders [][]string) {
	t.Helper()
	require.Equal(t, "github-hosted", os.Getenv("RUNNER_ENVIRONMENT"))
	t.Setenv("ONTOLOGY_STUDY_ACTIVE_ARM", "base")
	seed := buildStudyTemplate(t, shape)
	report := studyRankReport{
		Protocol: protocol,
		Source:   os.Getenv("GITHUB_SHA"), RunID: os.Getenv("GITHUB_RUN_ID"),
		RunAttempt: os.Getenv("GITHUB_RUN_ATTEMPT"), Runner: os.Getenv("RUNNER_NAME"),
		TemplateSHA:   studyTemplateSHA(t, seed),
		CorrectionSHA: studyHash(t, []ontology.Record{seed.correctionTopic, seed.override}),
		Shape:         shape, SeedSources: len(seed.sources), BackgroundTopics: seed.backgroundTopics,
		ChangeMarkers: seed.startingMarkers, Plans: make(map[string]studyRankPlan),
		ScheduledInputs: make(map[string][]studyScheduledInput), Runs: []studyTiming{},
	}
	require.NoError(t, seed.fixture.admin.Raw("SHOW server_version").Row().Scan(&report.PostgresVersion))
	dir := os.Getenv("ONTOLOGY_STUDY_REPORT_DIR")
	require.NotEmpty(t, dir)
	require.NoError(t, os.MkdirAll(dir, 0700))
	path := filepath.Join(dir, shape.Dataset+".json")
	save := func() {
		body, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(path+".tmp", body, 0600))
		require.NoError(t, os.Rename(path+".tmp", path))
	}
	stage := func(name string) {
		report.Stage = name
		t.Logf("study dataset=%s stage=%s template=%s", shape.Dataset, name, report.TemplateSHA)
		save()
	}
	cloner := newBuildStudyCloner(t, seed)
	stage("queue-transitions")
	if !t.Run("queue-transitions", func(t *testing.T) { proveStudyQueueTransitions(t, cloner.clone(t), seed) }) {
		return
	}
	for _, arm := range arms {
		stage("explain-" + arm)
		if !t.Run(report.Stage, func(t *testing.T) { report.Plans[arm] = studyRankPlanProbe(t, cloner.clone(t), seed, arm) }) {
			return
		}
		require.Equal(t, report.Plans["base"].BindingsSHA, report.Plans[arm].BindingsSHA)
		stage("fixed-schedule-" + arm)
		if !t.Run(report.Stage, func(t *testing.T) {
			report.ScheduledInputs[arm] = studyScheduledInputs(t, cloner.clone(t), seed, arm)
		}) {
			return
		}
		save()
		for i, input := range report.ScheduledInputs[arm] {
			control := report.ScheduledInputs["base"][i]
			require.Equal(t, studyHash(t, control.Sources), studyHash(t, input.Sources), "%s turn %d ordered sources", arm, i+1)
			require.Equal(t, studyHash(t, control.Candidates), studyHash(t, input.Candidates), "%s turn %d ordered candidates", arm, i+1)
			require.Equal(t, studyHash(t, control.HTTPBodies), studyHash(t, input.HTTPBodies), "%s turn %d HTTP requests", arm, i+1)
		}
	}
	for i, order := range orders {
		for _, arm := range order {
			stage(fmt.Sprintf("pair-%d-%s", i+1, arm))
			if !t.Run(report.Stage, func(t *testing.T) {
				m := timeStudyArm(t, cloner.clone(t), seed, arm, i+1)
				report.Runs = append(report.Runs, m)
				save()
				require.Equal(t, report.Plans[arm].QuerySHA, m.LookupSHA)
				require.Equal(t, report.Runs[0].Quality, m.Quality)
			}) {
				return
			}
		}
	}
	stage("complete")
}

func TestOntologyRankFirstVocabulary(t *testing.T) {
	t.Run("forced-lexical-miss-refills-tied-page", func(t *testing.T) {
		f := newOrganizationFixture(t)
		current := seedStaleVocabulary(t, f, 1, 21)
		forced := testTopic("astronomy")
		revalidationPublish(t, f, ontology.Change{Record: forced})
		source := f.organizationEvidence(t, 1, "PostgreSQL relational storage", nil)
		require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
			sources, err := organizationSources(tx, fence, []ontology.SourceHandle{source})
			require.NoError(t, err)
			unforced, err := f.store.organizationVocabulary(tx, fence, sources, []string{})
			require.NoError(t, err)
			require.Len(t, unforced, ontology.MaxVocabularyCandidates)
			for i, candidate := range unforced {
				require.Equal(t, current[i].ID, candidate.ID, "unforced UUID order across page boundary")
			}
			ranked, err := f.store.organizationVocabulary(tx, fence, sources, []string{forced.ID})
			require.NoError(t, err)
			require.Len(t, ranked, ontology.MaxVocabularyCandidates)
			require.Equal(t, forced.ID, ranked[0].ID)
			for i, candidate := range ranked[1:] {
				require.Equal(t, current[i].ID, candidate.ID, "refill the page in UUID order after skipping a stale candidate")
				require.True(t, candidate.Current)
			}
			return nil
		}))
	})

	t.Run("hydrate-only-exact-scope-and-version", func(t *testing.T) {
		f := newOrganizationFixture(t)
		other := maintenanceOtherTeam(t, f)
		old := testTopic("historical-collision")
		foreign := testTopic("foreign-collision")
		foreign.ID = old.ID
		revalidationPublish(t, f, ontology.Change{Record: old})
		revalidationPublish(t, other, ontology.Change{Record: foreign})
		oldActor := f.actor(0, "manager")
		require.NoError(t, f.rls.WithSystemTx(t.Context(), f.admin, func(tx *gorm.DB) error {
			return tx.Exec("UPDATE memory_spaces SET generation=generation+1 WHERE id=?::uuid", f.space).Error
		}))
		f.generation++
		require.ErrorIs(t, f.store.withScope(oldActor, f.team, true, func(*gorm.DB, scope) error {
			t.Error("stale generation reached the vocabulary reader")
			return nil
		}), ontology.ErrUnauthorized)
		f.credentials[0].TeamSharedSpaceGeneration = f.generation
		active := testTopic("active-collision")
		active.ID = old.ID
		revalidationPublish(t, f, ontology.Change{Record: active})
		active.Definition.Description = "exact second revision"
		revalidationPublish(t, f, ontology.Change{Record: active, ExpectedVersion: 1})

		type row struct {
			TeamID          string
			SpaceGeneration int64
			Version         int64
		}
		relations := []string{"ontology_topic_revision_payloads"}
		if hasStudyVectors(os.Getenv("ONTOLOGY_STUDY_ACTIVE_ARM")) {
			relations = append(relations, "ontology_study_topic_vectors")
		}
		for _, table := range relations {
			var total int
			require.NoError(t, f.admin.Raw("SELECT count(*) FROM "+table+" WHERE record_id=?::uuid", old.ID).Row().Scan(&total))
			require.Equal(t, 4, total, "colliding rows must exist")
		}
		require.NoError(t, f.store.withScope(f.actor(0, "manager"), f.team, true, func(tx *gorm.DB, fence scope) error {
			var bypass bool
			require.NoError(t, tx.Raw("SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user").Row().Scan(&bypass))
			require.False(t, bypass)
			for _, table := range relations {
				var forced bool
				require.NoError(t, tx.Raw("SELECT relforcerowsecurity FROM pg_class WHERE oid=?::regclass", table).Row().Scan(&forced))
				require.True(t, forced)
				var visible []row
				require.NoError(t, tx.Raw("SELECT team_id::text,space_generation,version FROM "+table+" WHERE record_id=?::uuid ORDER BY version", old.ID).Scan(&visible).Error)
				require.Equal(t, []row{{f.team, f.generation, 1}, {f.team, f.generation, 2}}, visible)
			}
			ranked, err := f.store.organizationVocabulary(tx, fence, nil, []string{old.ID})
			require.NoError(t, err)
			require.Len(t, ranked, 1)
			require.Equal(t, old.ID, ranked[0].ID)
			require.EqualValues(t, 2, ranked[0].Version)
			require.Equal(t, active.Definition.Key, ranked[0].Definition.Key)
			require.Equal(t, active.Definition.Description, ranked[0].Definition.Description)
			return nil
		}))
	})

	t.Run("publish-topic-and-correction-during-assessment", func(t *testing.T) {
		f := newOrganizationFixture(t)
		initial := testTopic("relational-storage")
		initial.Definition.Description = "PostgreSQL data systems"
		revalidationPublish(t, f, ontology.Change{Record: initial})
		source := f.organizationEvidence(t, 1, "PostgreSQL data systems", nil)
		canonical := f.canonicalSnapshot(t)
		correction := testTopic("corrected-storage")
		correction.Definition.Description = "PostgreSQL data systems"
		override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind,
			Override: &ontology.Override{Action: ontology.SetClassification, DefinitionID: correction.ID, Members: []ontology.SourceHandle{source}},
			Sources:  []ontology.SourceDependency{f.source(t, source)}}
		service, calls := organizationFixtureService(t, f, nil, func(_ assessment.Request, _ *assessment.Response) {
			revalidationPublish(t, f, ontology.Change{Record: correction}, ontology.Change{Record: override})
		})
		input := ontology.OrganizationInput{OperationKey: "concurrent-correction", Sources: []ontology.SourceHandle{source}}
		result, err := service.Organize(t.Context(), f.team, input)
		require.Error(t, err)
		require.Equal(t, "stale_input", result.FailureCode)
		require.Nil(t, result.Publication)
		require.EqualValues(t, 1, calls.Load())
		fresh, freshCalls := organizationFixtureService(t, f, nil, nil)
		input.OperationKey = "after-correction"
		result, err = fresh.Organize(t.Context(), f.team, input)
		require.NoError(t, err)
		require.True(t, result.Current)
		require.Zero(t, freshCalls.Load(), "the current manager decision is locked")
		require.Contains(t, studyRecall(t, f, correction.Definition.Key).Sources, source)
		require.Equal(t, canonical, f.canonicalSnapshot(t))
	})
}
