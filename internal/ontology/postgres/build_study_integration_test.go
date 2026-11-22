//go:build integration && ontology_study

package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpg "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type studyRankQueries struct {
	calls, elapsed, discoveryElapsed atomic.Int64
	exampleOnce                      sync.Once
	exampleSQL                       string
	exampleArgs                      []any
}

func instrumentStudyQuery(t *testing.T, f *ontologyFixture, arm string) *studyRankQueries {
	t.Helper()
	q := &studyRankQueries{}
	vectorPattern := studyVectorPattern()
	before := func(tx *gorm.DB) {
		sql := tx.Statement.SQL.String()
		if !strings.HasPrefix(sql, "WITH ranked AS (") || !strings.Contains(sql, "ts_rank_cd(") {
			return
		}
		const anchor = "AND NOT head.retired AND head.kind IN ('topic')"
		if strings.Count(sql, anchor) != 1 {
			tx.AddError(errors.New("study vocabulary query changed"))
			return
		}
		replacement := anchor
		if arm == "topic" {
			replacement += " AND revision.kind = 'topic'"
		}
		sql = strings.Replace(sql, anchor, replacement, 1)
		if arm == "vector" {
			var err error
			sql, err = studyVectorSQL(sql, vectorPattern)
			if err != nil {
				tx.AddError(err)
				return
			}
		}
		q.exampleOnce.Do(func() {
			q.exampleSQL = sql
			q.exampleArgs = append([]any{}, tx.Statement.Vars...)
		})
		tx.Statement.SQL.Reset()
		tx.Statement.SQL.WriteString(sql)
		tx.Statement.Settings.Store("ontology-study-row-start", time.Now())
	}
	after := func(tx *gorm.DB) {
		if start, ok := tx.Statement.Settings.LoadAndDelete("ontology-study-row-start"); ok {
			ns := time.Since(start.(time.Time)).Nanoseconds()
			q.elapsed.Add(ns)
			if discoveryStats(tx.Statement.Context) != nil {
				q.discoveryElapsed.Add(ns)
			}
			q.calls.Add(1)
		}
	}
	require.NoError(t, f.app.Callback().Row().Before("gorm:row").Register("ontology_study_before", before))
	require.NoError(t, f.app.Callback().Row().After("gorm:row").Register("ontology_study_after", after))
	return q
}

type studyQueueKey struct{}

type studyQueueChanges struct {
	Enqueued, Requeued, RefreshedPending int
}

// PostgreSQL 18 returns the old and new status from the exact UPSERT without a second snapshot.
func observeStudyQueue(t *testing.T, f *ontologyFixture) {
	t.Helper()
	callbacks := f.app.Callback().Raw()
	original := callbacks.Get("gorm:raw")
	require.NotNil(t, original)
	require.NoError(t, callbacks.Replace("gorm:raw", func(db *gorm.DB) {
		changes, tagged := db.Statement.Context.Value(studyQueueKey{}).(*studyQueueChanges)
		query := db.Statement.SQL.String()
		if !tagged || db.Error != nil || db.DryRun || !strings.HasPrefix(strings.TrimSpace(query), "INSERT INTO ontology_maintenance_sources(") {
			original(db)
			return
		}
		rows, err := db.Statement.ConnPool.QueryContext(db.Statement.Context, query+" RETURNING old.status, new.status", db.Statement.Vars...)
		if err != nil {
			db.AddError(err)
			return
		}
		var affected int64
		for rows.Next() {
			var old sql.NullString
			var current string
			if err = rows.Scan(&old, &current); err != nil {
				break
			}
			affected++
			if current == "pending" {
				switch {
				case !old.Valid:
					changes.Enqueued++
				case old.String == "pending":
					changes.RefreshedPending++
				default:
					changes.Requeued++
				}
			}
		}
		if err = errors.Join(err, rows.Err(), rows.Close()); err != nil {
			db.AddError(err)
			return
		}
		db.RowsAffected = affected
		if db.Statement.Result != nil {
			db.Statement.Result.RowsAffected = affected
			db.Statement.Result.Result = driver.RowsAffected(affected)
		}
	}))
}

func proveStudyQueueTransitions(t *testing.T, f *ontologyFixture, seed *buildStudySeed) {
	t.Helper()
	pending, organized := seed.sources[len(seed.sources)-1], seed.sources[0]
	require.Equal(t, "pending", revalidationQueue(t, f, pending).Status)
	require.Equal(t, "organized", revalidationQueue(t, f, organized).Status)
	revalidationPublish(t, f, ontology.Change{Record: seed.correctionTopic}, ontology.Change{Record: seed.override})
	observeStudyQueue(t, f)
	for _, part := range []struct {
		source ontology.SourceHandle
		want   studyQueueChanges
	}{
		{pending, studyQueueChanges{RefreshedPending: 1}}, {organized, studyQueueChanges{Requeued: 1}},
	} {
		var actual studyQueueChanges
		ctx := context.WithValue(context.Background(), studyQueueKey{}, &actual)
		require.NoError(t, f.store.withScope(ctx, f.team, false, func(tx *gorm.DB, fence scope) error {
			return refreshSelectedMaintenanceSource(tx, fence, part.source, true)
		}))
		require.Equal(t, part.want, actual)
		require.Equal(t, "pending", revalidationQueue(t, f, part.source).Status)
	}
}

func proveStudyInitialPage(t *testing.T, f *ontologyFixture) {
	t.Helper()
	t.Setenv("ONTOLOGY_STUDY_ACTIVE_ARM", "pages")
	ctx := context.Background()
	window := maintenanceWindow(t, f, buildStudySettings(f))
	turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), 5*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	require.NoError(t, f.store.withScope(ctx, f.team, false, func(tx *gorm.DB, fence scope) error {
		updated := tx.Exec(`UPDATE ontology_maintenance_teams SET discovery_kind=0,discovery_cursor=''
			WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND lease_token=?::uuid`,
			fence.TeamID, fence.SpaceID, fence.Generation, turn.LeaseToken)
		if updated.Error == nil && updated.RowsAffected != 1 {
			return errors.New("could not restore initial enumeration cursor on study clone")
		}
		return updated.Error
	}))
	var observed int
	ctx = context.WithValue(ctx, maintenanceDiscoveryObservationKey{}, maintenanceDiscoveryObserver(func(event maintenanceDiscoveryEvent) {
		if event.Phase == "study_page_limit" {
			observed = event.Sources
		}
	}))
	result, err := f.store.DiscoverMaintenance(ctx, *turn, ontology.MaintenancePageSize)
	require.NoError(t, err)
	require.True(t, result.Committed)
	require.Equal(t, ontology.MaintenancePageSize, observed)
	require.Equal(t, observed, result.Counts.InitialSources)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(ctx, *turn))
}

type studyPage struct {
	Changes                studyQueueChanges
	Milliseconds           float64
	Counts                 ontology.MaintenanceDiscoveryCounts
	ValidationMilliseconds float64
	Limit                  int
	Lease                  string
	BatchClaimed           time.Time
}

type studyRepository struct {
	ontology.MaintenanceRepository
	first, resume chan struct{}
	once          sync.Once
	mu            sync.Mutex
	pages         []studyPage
	refreshed     map[ontology.SourceHandle]bool
	receipts      map[string]bool
}

func (r *studyRepository) DiscoverMaintenance(ctx context.Context, turn ontology.MaintenanceTurn, limit int) (ontology.MaintenanceDiscoveryResult, error) {
	var elapsed time.Duration
	var observedLimit int
	selected := make(map[ontology.SourceHandle]bool)
	receipts := make(map[string]bool)
	observe := func(e maintenanceDiscoveryEvent) {
		switch e.Phase {
		case "study_page_limit":
			observedLimit = e.Sources
		case "source_finished":
			if e.Err == nil {
				selected[e.Source] = true
			}
		case "receipt_finished":
			elapsed += e.Duration
			receipts[e.ReceiptID] = true
		}
	}
	var changes studyQueueChanges
	ctx = context.WithValue(ctx, studyQueueKey{}, &changes)
	ctx = context.WithValue(ctx, maintenanceDiscoveryObservationKey{}, maintenanceDiscoveryObserver(observe))
	result, err := r.MaintenanceRepository.DiscoverMaintenance(ctx, turn, limit)
	if err != nil || !result.Committed {
		return result, err
	}
	r.mu.Lock()
	r.pages = append(r.pages, studyPage{Changes: changes, Milliseconds: float64(result.Duration.Microseconds()) / 1000,
		Counts: result.Counts, ValidationMilliseconds: float64(elapsed.Microseconds()) / 1000, Limit: observedLimit, Lease: turn.LeaseToken})
	for source := range selected {
		r.refreshed[source] = true
	}
	for id := range receipts {
		r.receipts[id] = true
	}
	r.mu.Unlock()
	r.once.Do(func() {
		close(r.first)
		select {
		case <-r.resume:
		case <-ctx.Done():
		}
	})
	return result, nil
}

func (r *studyRepository) ClaimMaintenanceBatch(ctx context.Context, turn ontology.MaintenanceTurn, window string, now time.Time) (*ontology.MaintenanceClaim, error) {
	r.mu.Lock()
	page := len(r.pages) - 1
	observed := page >= 0 && r.pages[page].Lease == turn.LeaseToken
	r.mu.Unlock()
	if !observed {
		return nil, errors.New("batch claim without its committed discovery page")
	}
	claim, err := r.MaintenanceRepository.ClaimMaintenanceBatch(ctx, turn, window, now)
	if err == nil && claim != nil {
		r.mu.Lock()
		r.pages[page].BatchClaimed = time.Now()
		r.mu.Unlock()
	}
	return claim, err
}

func newStudyRepository(f *ontologyFixture) *studyRepository {
	return &studyRepository{MaintenanceRepository: f.store, first: make(chan struct{}), resume: make(chan struct{}),
		refreshed: map[ontology.SourceHandle]bool{}, receipts: map[string]bool{}}
}

func studyService(t *testing.T, f *ontologyFixture, repo *studyRepository, classify func(assessment.Request, *assessment.Response), observer assessment.Observer) *organization.MaintenanceService {
	return organization.NewMaintenanceService(organization.MaintenanceDependencies{
		Repository: repo, Config: buildStudySettings(f), DefaultModel: "fixture-model", ProviderTimeout: time.Minute,
		Audit: auditapp.New(auditpg.NewStore(f.app, f.rls)),
		Organizer: func(model string, tokens int64, a assessment.AttemptAccounting) *organization.Service {
			if a == nil {
				return organization.NewService(nil, assessment.NewProvider(nil, model, maintenanceFixtureLimits(), int(tokens)))
			}
			service, _ := organizationFixtureServiceWithAccounting(t, f, nil, classify, model, maintenanceFixtureLimits(), a, int(tokens), map[string]int{"prompt_tokens": 40, "completion_tokens": 20, "total_tokens": 60})
			return service.WithDiagnostics(observer)
		},
	})
}

func readStudyRecall(f *ontologyFixture, term string, limit int) (ontology.RecallOrganization, error) {
	var found ontology.RecallOrganization
	ctx := f.actor(0, "manager")
	err := f.rls.WithTeamReadOnlyRepeatableTx(ctx, f.app, f.team, func(tx *gorm.DB) error {
		var err error
		found, err = f.store.ReadRecall(ctx, tx, f.team, ontology.RecallReadInput{SpaceID: f.space, Query: term, Limit: limit})
		return err
	})
	return found, err
}

func studyRecall(t *testing.T, f *ontologyFixture, term string) ontology.RecallOrganization {
	t.Helper()
	found, err := readStudyRecall(f, term, recallcontract.MaxRecallCandidateCount)
	require.NoError(t, err)
	require.Empty(t, found.Degradation)
	return found
}

func studyHash(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return testHash(string(b))
}

type studyQuality struct{ Canonical, Semantics, Recall string }

func studyFinalQuality(t *testing.T, f *ontologyFixture, seed *buildStudySeed) studyQuality {
	t.Helper()
	var rows []string
	require.NoError(t, f.admin.Raw(`SELECT (CASE head.kind
		WHEN 'topic' THEN revision.body->'definition'
		WHEN 'assignment' THEN revision.body->'assignment'
		WHEN 'override' THEN revision.body->'override'
		WHEN 'evidence_group' THEN (revision.body->'group')-'assessment_id' END)::text AS value
		FROM ontology_record_heads head JOIN ontology_revision_records revision USING
		(team_id,shared_space_id,space_generation,record_id,version)
		WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=? AND NOT head.retired
		ORDER BY head.kind,value`, f.team, f.space, f.generation).Scan(&rows).Error)
	var assignments []struct{ Source, Class string }
	require.NoError(t, f.admin.Raw(`SELECT record.body->'assignment'->'source'->>'id' AS source,topic.body->'definition'->>'key' AS class
		FROM ontology_record_heads head JOIN ontology_revision_records record USING(team_id,shared_space_id,space_generation,record_id,version)
		JOIN ontology_record_heads definition ON (definition.team_id,definition.shared_space_id,definition.space_generation,definition.record_id) =
		(head.team_id,head.shared_space_id,head.space_generation,(record.body->'assignment'->>'definition_id')::uuid)
		JOIN ontology_revision_records topic ON (topic.team_id,topic.shared_space_id,topic.space_generation,topic.record_id,topic.version) =
		(definition.team_id,definition.shared_space_id,definition.space_generation,definition.record_id,definition.version)
		WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
		AND head.kind='assignment' AND record.kind='assignment' AND topic.kind='topic' AND NOT head.retired AND NOT definition.retired`, f.team, f.space, f.generation).Scan(&assignments).Error)
	actual := map[string]string{}
	for _, v := range assignments {
		require.NotContains(t, actual, v.Source)
		actual[v.Source] = v.Class
	}
	require.Len(t, actual, len(seed.sources))
	for id, key := range seed.classByID {
		if id == seed.sources[0].ID {
			data, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{seed.sources[0]})
			require.NoError(t, err)
			locked, _ := ontology.OrganizationClassification(data.Sources[0], data.Records, data.Candidates)
			require.Equal(t, seed.correctionTopic.ID, locked, "effective manager classification")
			continue
		}
		require.Equal(t, key, actual[id], "direct topic for Evidence %s", id)
	}
	var recalls []ontology.RecallOrganization
	for i := range seed.classes {
		term := studyClass(i)
		var want []string
		for _, source := range seed.sources {
			if seed.classByID[source.ID] == term && source.ID != seed.sources[0].ID {
				want = append(want, source.ID)
			}
		}
		slices.Sort(want)
		const narrowBudget = 20
		if len(want) > narrowBudget {
			_, err := readStudyRecall(f, term, narrowBudget)
			require.ErrorIs(t, err, ontology.ErrContextBound, "bounded reader must expose an incomplete candidate pool")
		}
		result := studyRecall(t, f, term)
		var got []string
		for _, source := range result.Sources {
			got = append(got, source.ID)
		}
		require.NotEmpty(t, want)
		require.Equal(t, want, got, "all ordered direct members for %s", term)
		recalls = append(recalls, result)
	}
	corrected := studyRecall(t, f, seed.correctionTopic.Definition.Key)
	require.Contains(t, corrected.Sources, seed.sources[0])
	recalls = append(recalls, corrected)
	return studyQuality{schedulerCanonical(t, f), studyHash(t, rows), studyHash(t, recalls)}
}

func studyFixedProbe(t *testing.T, f *ontologyFixture, seed *buildStudySeed, arm string) string {
	t.Helper()
	installStudyVectors(t, f, arm)
	stats := instrumentStudyQuery(t, f, arm)
	var inputs []assessment.Request
	service, calls := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
		request.RequestID = ""
		inputs = append(inputs, request)
		studyClassification(request, response)
	})
	var candidates [][]ontology.RecordView
	for i := range 3 {
		start := len(seed.sources) - 2*(i+1)
		handles := seed.sources[start : start+2]
		data, err := f.store.ReadOrganization(context.Background(), f.team, handles)
		require.NoError(t, err)
		candidates = append(candidates, data.Candidates)
		result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: fmt.Sprintf("study-parity-%d", i), Sources: handles})
		require.NoError(t, err)
		require.True(t, result.Current)
	}
	require.Positive(t, calls.Load())
	require.Positive(t, stats.calls.Load())
	return studyHash(t, struct {
		Candidates [][]ontology.RecordView
		Requests   []assessment.Request
	}{candidates, inputs})
}

type studyTiming struct {
	SmallPages, FullPages, LastPageSize, PagesBeforeReadyBatch                                 int
	FirstBatchMS, DiscoveryBeforeReadyBatchMS, ReadyPageP95MS                                  float64
	BackfillAndVerificationMS, CorrectionPublicationMS, DiscoveryMS, DiscoveryQueryReturnMS    float64
	ExampleVocabularyPlan                                                                      studyQueryPlan
	NewPending, RefreshedPending, DiscoveryRefreshWrites                                       int
	Arm                                                                                        string
	Pair                                                                                       int
	Coverage                                                                                   ontology.MaintenanceCounts
	ElapsedMS, CorrectionMS, PageMedianMS, PageP95MS, ValidationMS, QueryReturnMS              float64
	InitialOldestMarkerAgeMS, MaxOldestMarkerAgeMS                                             float64
	InitialMarkers, MaximumMarkers, RemainingMarkers, Pages, UniqueRefreshed, DistinctReceipts int
	Requeues, FreshnessChecks, MarkerMatches, MarkersCompleted                                 int
	QueryCount, ReportedInput, ReportedOutput, UnknownUsage, ProviderAttempts                  int64
	Quality                                                                                    studyQuality
}

type studyScheduledInput struct {
	PageLimit  int
	Sources    []ontology.SourceHandle
	Candidates []ontology.RecordView
	Requests   []assessment.Request
}

func studyScheduledInputs(t *testing.T, f *ontologyFixture, seed *buildStudySeed, arm string) []studyScheduledInput {
	t.Helper()
	t.Setenv("ONTOLOGY_STUDY_ACTIVE_ARM", arm)
	repo := newStudyRepository(f)
	close(repo.resume)
	var turns []studyScheduledInput
	totalRequests := 0
	for i := range 3 {
		if i == 1 {
			revalidationPublish(t, f, ontology.Change{Record: seed.correctionTopic}, ontology.Change{Record: seed.override})
		}
		input := studyScheduledInput{Requests: []assessment.Request{}}
		var mu sync.Mutex
		classify := func(request assessment.Request, response *assessment.Response) {
			mu.Lock()
			input.Requests = append(input.Requests, request)
			mu.Unlock()
			studyClassification(request, response)
		}
		observe := func(_ context.Context, d assessment.Diagnostic) {
			if d.Stage == "completed" && d.SourceContext != nil && d.Input != nil {
				input.Sources = d.Input.Sources
				input.Candidates = d.SourceContext.Candidates
			}
		}
		service := studyService(t, f, repo, classify, observe)
		progress, err := service.RunTurn(context.Background())
		require.NoError(t, err)
		require.True(t, progress)
		require.Len(t, repo.pages, i+1)
		require.False(t, repo.pages[i].BatchClaimed.IsZero())
		input.PageLimit = repo.pages[i].Limit
		require.NotEmpty(t, input.Sources)
		require.NotEmpty(t, input.Candidates)
		mu.Lock()
		totalRequests += len(input.Requests)
		turns = append(turns, input)
		mu.Unlock()
	}
	require.Positive(t, totalRequests, "record real provider HTTP requests from native maintenance turns")
	if arm == "pages" {
		require.Equal(t, 25, turns[0].PageLimit)
	} else {
		require.Equal(t, ontology.MaintenancePageSize, turns[0].PageLimit)
	}
	return turns
}

func studyMarkerInfo(t *testing.T, f *ontologyFixture) (int, float64) {
	t.Helper()
	var count int
	var age float64
	require.NoError(t, f.admin.Raw(`SELECT count(*),COALESCE(extract(epoch FROM clock_timestamp()-min(first_pending_at))*1000,0)
		FROM ontology_maintenance_markers WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, f.team, f.space, f.generation).Row().Scan(&count, &age))
	return count, age
}

func timeStudyArm(t *testing.T, f *ontologyFixture, seed *buildStudySeed, arm string, pair int) studyTiming {
	t.Helper()
	t.Setenv("ONTOLOGY_STUDY_ACTIVE_ARM", arm)
	ctx := context.Background()
	stats := instrumentStudyQuery(t, f, arm)
	observeStudyQueue(t, f)
	repo := newStudyRepository(f)
	service := studyService(t, f, repo, studyClassification, nil)
	before, err := f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.Positive(t, before.Counts.Pending)
	require.NotNil(t, before.Window)
	require.True(t, before.Window.Policy.MaxConcurrency == 8 && before.Window.Policy.TeamConcurrency(f.team) == 8 && before.Window.Policy.ContextWindowTokens == 500000)
	initial, age := studyMarkerInfo(t, f)
	require.Equal(t, seed.startingMarkers, initial)
	m := studyTiming{Arm: arm, Pair: pair, InitialMarkers: initial, MaximumMarkers: initial, InitialOldestMarkerAgeMS: age, MaxOldestMarkerAgeMS: age}
	start := time.Now()
	m.BackfillAndVerificationMS = installStudyVectors(t, f, arm)
	_, stop := startScheduler(t, service, nil, 1)
	var unlock sync.Once
	release := func() { unlock.Do(func() { close(repo.resume) }) }
	defer func() { release(); stop() }()
	select {
	case <-repo.first:
	case <-time.After(2 * time.Minute):
		t.Fatal("first committed discovery page unavailable")
	}
	correctionStart := time.Now()
	revalidationPublish(t, f, ontology.Change{Record: seed.correctionTopic}, ontology.Change{Record: seed.override})
	m.CorrectionPublicationMS = float64(time.Since(correctionStart).Microseconds()) / 1000
	release()
	var status ontology.MaintenanceStatus
	for {
		status, err = f.store.MaintenanceStatus(ctx, time.Now())
		require.NoError(t, err)
		if m.CorrectionMS == 0 && slices.Contains(studyRecall(t, f, seed.correctionTopic.Definition.Key).Sources, seed.sources[0]) {
			m.CorrectionMS = float64(time.Since(correctionStart).Microseconds()) / 1000
		}
		count, age := studyMarkerInfo(t, f)
		m.MaximumMarkers = max(m.MaximumMarkers, count)
		m.MaxOldestMarkerAgeMS = max(m.MaxOldestMarkerAgeMS, age)
		if status.DiscoveryComplete && status.Counts.Pending == 0 && status.ActiveWork == 0 {
			m.RemainingMarkers = count
			break
		}
		if time.Since(start) > 15*time.Minute {
			t.Fatalf("did not drain, counts=%+v markers=%d active=%d", status.Counts, count, status.ActiveWork)
		}
		time.Sleep(250 * time.Millisecond)
	}
	stop()
	_, err = service.Command(ctx, domain.OntologyMaintenanceCommand{Action: "pause", OperationKey: uuid.NewString()}, "", "")
	require.NoError(t, err)
	schedulerAssertDrained(t, f)
	status, err = f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	if arm == "vector" {
		verifyStudyVectors(t, f)
	}
	m.ElapsedMS = float64(time.Since(start).Microseconds()) / 1000
	m.Coverage = status.Counts
	require.True(t, status.Paused && status.DiscoveryComplete && status.CoverageComplete, "status=%+v", status.Counts)
	require.Positive(t, m.Coverage.Eligible)
	require.Equal(t, m.Coverage.Eligible, m.Coverage.Organized)
	require.Zero(t, m.RemainingMarkers)
	require.Positive(t, m.CorrectionMS)
	m.ReportedInput = status.Usage.ReportedInputTokens - before.Usage.ReportedInputTokens
	m.ReportedOutput = status.Usage.ReportedOutputTokens - before.Usage.ReportedOutputTokens
	m.UnknownUsage = status.Usage.UnavailableUsageAttempts - before.Usage.UnavailableUsageAttempts
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_maintenance_attempts WHERE admitted_at >= ?`, start).Row().Scan(&m.ProviderAttempts))
	require.Positive(t, m.ProviderAttempts)
	m.QueryCount = stats.calls.Load()
	m.QueryReturnMS = float64(stats.elapsed.Load()) / 1e6
	m.DiscoveryQueryReturnMS = float64(stats.discoveryElapsed.Load()) / 1e6
	m.ExampleVocabularyPlan = explainStudyQuery(t, f, stats)
	m.UniqueRefreshed = len(repo.refreshed)
	m.DistinctReceipts = len(repo.receipts)
	var timings, readyTimings []float64
	for _, p := range repo.pages {
		m.LastPageSize = p.Limit
		if p.Limit == 25 {
			m.SmallPages++
		} else if p.Limit == ontology.MaintenancePageSize {
			m.FullPages++
		}
		if !p.BatchClaimed.IsZero() {
			if m.FirstBatchMS == 0 {
				m.FirstBatchMS = float64(p.BatchClaimed.Sub(start).Microseconds()) / 1000
			}
			m.DiscoveryBeforeReadyBatchMS += p.Milliseconds
			readyTimings = append(readyTimings, p.Milliseconds)
		}
		m.DiscoveryMS += p.Milliseconds
		m.MarkersCompleted += p.Counts.CompletedMarkers
		m.MarkerMatches += p.Counts.EnumeratedMatches
		require.Equal(t, p.Counts.Requeues, p.Changes.Enqueued+p.Changes.Requeued+p.Changes.RefreshedPending, "account for each committed native queue refresh")
		m.Requeues += p.Changes.Requeued
		m.NewPending += p.Changes.Enqueued
		m.RefreshedPending += p.Changes.RefreshedPending
		m.DiscoveryRefreshWrites += p.Counts.Requeues
		m.FreshnessChecks += p.Counts.FreshnessInvocations
		m.ValidationMS += p.ValidationMilliseconds
		timings = append(timings, p.Milliseconds)
	}
	slices.Sort(timings)
	m.Pages = len(timings)
	require.NotEmpty(t, timings)
	require.NotEmpty(t, readyTimings)
	slices.Sort(readyTimings)
	m.PagesBeforeReadyBatch = len(readyTimings)
	m.ReadyPageP95MS = readyTimings[(95*len(readyTimings)+99)/100-1]
	if os.Getenv("ONTOLOGY_STUDY_VARIANT") == "pages" {
		require.Equal(t, m.Pages, m.SmallPages+m.FullPages, "observe the effective limit inside every committed discovery transaction")
		require.Equal(t, ontology.MaintenancePageSize, m.LastPageSize, "finish with a full-size page")
		if arm == "pages" {
			require.Positive(t, m.SmallPages)
		} else {
			require.Zero(t, m.SmallPages)
		}
	}
	m.PageMedianMS = timings[len(timings)/2]
	m.PageP95MS = timings[(95*len(timings)+99)/100-1]
	require.Positive(t, m.QueryCount)
	require.Positive(t, m.FreshnessChecks)
	m.Quality = studyFinalQuality(t, f, seed)
	require.Equal(t, seed.canonical, m.Quality.Canonical)
	return m
}

func TestMatureOntologyBuildComparison(t *testing.T) {
	shape := studyShape(t)
	candidate := os.Getenv("ONTOLOGY_STUDY_VARIANT")
	require.Contains(t, []string{"topic", "vector", "pages"}, candidate)
	seed := buildStudyTemplate(t, shape)
	cloner := newBuildStudyCloner(t, seed)
	if !t.Run("queue-transitions", func(t *testing.T) { proveStudyQueueTransitions(t, cloner.clone(t), seed) }) {
		return
	}
	if candidate == "pages" && !t.Run("initial-page", func(t *testing.T) { proveStudyInitialPage(t, cloner.clone(t)) }) {
		return
	}
	inputHashes := map[string]string{}
	scheduled := map[string][]studyScheduledInput{}
	for _, arm := range []string{"base", candidate} {
		if !t.Run("fixed-input-"+arm, func(t *testing.T) { inputHashes[arm] = studyFixedProbe(t, cloner.clone(t), seed, arm) }) {
			return
		}
		if candidate == "pages" && !t.Run("scheduled-input-"+arm, func(t *testing.T) {
			scheduled[arm] = studyScheduledInputs(t, cloner.clone(t), seed, arm)
		}) {
			return
		}
	}
	require.Equal(t, inputHashes["base"], inputHashes[candidate], "ordered vocabulary and HTTP model inputs")
	var runs []studyTiming
	for pair := range 4 {
		arms := []string{"base", candidate}
		if pair%2 != 0 {
			slices.Reverse(arms)
		}
		for _, arm := range arms {
			if !t.Run(fmt.Sprintf("pair-%d-%s", pair+1, arm), func(t *testing.T) {
				m := timeStudyArm(t, cloner.clone(t), seed, arm, pair+1)
				body, err := json.Marshal(m)
				require.NoError(t, err)
				t.Logf("ontology_build_arm %s", body)
				runs = append(runs, m)
				require.Equal(t, runs[0].Quality, m.Quality, "same memberships, group semantics, overrides and Recall order")
			}) {
				return
			}
		}
	}
	report := struct {
		Protocol, Candidate                          string
		Shape                                        buildStudyShape
		SeedSources, BackgroundTopics, ChangeMarkers int
		FixedInputSHA                                string
		ScheduledInputs                              map[string][]studyScheduledInput
		Runs                                         []studyTiming
	}{"ontology-build-drain-v1", candidate, shape, len(seed.sources), seed.backgroundTopics, seed.startingMarkers, inputHashes["base"], scheduled, runs}
	b, err := json.MarshalIndent(report, "", "  ")
	require.NoError(t, err)
	dir := os.Getenv("ONTOLOGY_STUDY_REPORT_DIR")
	require.NotEmpty(t, dir)
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, shape.Dataset+".json"), b, 0600))
}
