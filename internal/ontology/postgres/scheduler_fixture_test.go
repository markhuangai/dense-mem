//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	community "github.com/markhuangai/dense-mem/internal/community/contract"
	communitypg "github.com/markhuangai/dense-mem/internal/community/postgres"
	communityapp "github.com/markhuangai/dense-mem/internal/community/service"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/settings"
	"github.com/stretchr/testify/require"
)

type schedulerProjectionStore struct {
	*communitypg.Store
	before func(context.Context, community.TopicProjectionBatch) error
	after  func(error)
}

func (s *schedulerProjectionStore) AppendTopicProjection(ctx context.Context, batch community.TopicProjectionBatch, now time.Time) error {
	if s.before != nil {
		if err := s.before(ctx, batch); err != nil {
			return err
		}
	}
	err := s.Store.AppendTopicProjection(ctx, batch, now)
	if s.after != nil {
		s.after(err)
	}
	return err
}

type schedulerProjectionObserver struct {
	organization.ProjectionRunner
	before func(context.Context)
	mu     sync.Mutex
	turns  []time.Duration
}

func (s *schedulerProjectionObserver) RunProjectionTurn(ctx context.Context) (bool, error) {
	started := time.Now()
	if s.before != nil {
		s.before(ctx)
	}
	progress, err := s.ProjectionRunner.RunProjectionTurn(ctx)
	s.mu.Lock()
	s.turns = append(s.turns, time.Since(started))
	s.mu.Unlock()
	return progress, err
}

type schedulerMeasurement struct {
	mu                 sync.Mutex
	started            time.Time
	Fixture            string         `json:"fixture"`
	Configuration      map[string]int `json:"configuration"`
	FirstDispatchMS    float64        `json:"first_dispatch_ms"`
	FirstProjectionMS  float64        `json:"first_projection_publish_return_ms"`
	CompletionMS       float64        `json:"completion_ms"`
	ProviderCalls      int            `json:"provider_calls"`
	MaxProviderOverlap int            `json:"max_provider_overlap"`
	MixedOverlap       bool           `json:"mixed_overlap"`
	MaxAdmitted        int            `json:"max_admitted"`
	MaxTeamAdmitted    map[string]int `json:"max_team_admitted"`
	ProjectionTurnsMS  []float64      `json:"projection_turns_ms"`
	PreparationTurnsMS []float64      `json:"preparation_lease_ms"`
	TerminalOutcomes   map[string]int `json:"terminal_outcomes"`
	ChargedInput       int64          `json:"charged_input"`
	ChargedOutput      int64          `json:"charged_output"`
	CanonicalBefore    string         `json:"canonical_before"`
	CanonicalAfter     string         `json:"canonical_after"`
	ProjectionCoverage any            `json:"projection_coverage"`
	Errors             []string       `json:"errors,omitempty"`
	providers          int
	projections        int
}

type schedulerPreparationObserver struct {
	ontology.MaintenanceRepository
	mu      sync.Mutex
	started map[string]time.Time
	turns   []float64
}

func (s *schedulerPreparationObserver) ClaimMaintenanceTurn(ctx context.Context, window string, now time.Time, lease time.Duration) (*ontology.MaintenanceTurn, error) {
	started := time.Now()
	turn, err := s.MaintenanceRepository.ClaimMaintenanceTurn(ctx, window, now, lease)
	s.mu.Lock()
	defer s.mu.Unlock()
	if turn != nil {
		s.started[turn.LeaseToken] = started
	} else {
		s.turns = append(s.turns, float64(time.Since(started).Microseconds())/1000)
	}
	return turn, err
}

func (s *schedulerPreparationObserver) ClaimMaintenanceBatch(ctx context.Context, turn ontology.MaintenanceTurn, window string, now time.Time) (*ontology.MaintenanceClaim, error) {
	claim, err := s.MaintenanceRepository.ClaimMaintenanceBatch(ctx, turn, window, now)
	s.finish(turn.LeaseToken)
	return claim, err
}

func (s *schedulerPreparationObserver) ReleaseMaintenanceTurn(ctx context.Context, turn ontology.MaintenanceTurn) error {
	err := s.MaintenanceRepository.ReleaseMaintenanceTurn(ctx, turn)
	s.finish(turn.LeaseToken)
	return err
}

func (s *schedulerPreparationObserver) finish(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if started, ok := s.started[token]; ok {
		s.turns = append(s.turns, float64(time.Since(started).Microseconds())/1000)
		delete(s.started, token)
	}
}

func (m *schedulerMeasurement) sample(f *ontologyFixture) {
	var rows []struct {
		Team  string
		Count int
	}
	err := f.admin.Raw(`SELECT team_id::text AS team,sum(n)::int AS count FROM (
		SELECT team_id,count(*) AS n FROM ontology_maintenance_teams WHERE lease_until>clock_timestamp() GROUP BY team_id
		UNION ALL SELECT team_id,count(*) FROM ontology_maintenance_batches
		WHERE status='running' AND lease_until>clock_timestamp() GROUP BY team_id) AS work GROUP BY team_id`).Scan(&rows).Error
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		m.Errors = append(m.Errors, err.Error())
		return
	}
	total := 0
	for _, row := range rows {
		total += row.Count
		m.MaxTeamAdmitted[row.Team] = max(m.MaxTeamAdmitted[row.Team], row.Count)
	}
	m.MaxAdmitted = max(m.MaxAdmitted, total)
	var duplicate bool
	err = f.admin.Raw(`SELECT EXISTS(SELECT 1 FROM ontology_maintenance_batches AS batch,
		jsonb_array_elements(batch.sources) AS source WHERE batch.status='running' AND batch.lease_until>clock_timestamp()
		GROUP BY batch.team_id,source->>'kind',source->>'id' HAVING count(*)>1)`).Row().Scan(&duplicate)
	if err != nil {
		m.Errors = append(m.Errors, err.Error())
	} else if duplicate {
		m.Errors = append(m.Errors, "a stable source identity belongs to multiple live batches")
	}
}

func schedulerTopic(t *testing.T, f *ontologyFixture) ontology.Record {
	t.Helper()
	ctx := context.Background()
	subject := maintenanceEntity(t, f, "Scheduler project")
	object, err := f.knowledge.CreateEntity(ctx, knowledge.CreateEntityInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityKind: "product", CanonicalName: "PostgreSQL"})
	require.NoError(t, err)
	owner := len(f.owners) - 1
	text := "Scheduler project uses PostgreSQL."
	ingest := f.evidence(t, owner, text)
	decision, err := f.knowledge.ApplyRelationshipDecision(ctx, knowledge.ApplyRelationshipDecisionInput{
		TeamID: f.team, OwnerProfileID: f.owners[owner], IngestID: ingest.IngestID,
		SubjectEntityID: subject.ID, PredicateKey: "uses", ObjectEntityID: object.EntityID,
		Support: &knowledge.EvidenceSupportInput{FragmentID: ingest.Evidence[0].FragmentID, SourceGroupKey: uuid.NewString(), SpanEnd: len(text), Authority: "primary"},
	})
	require.NoError(t, err)
	handle := ontology.SourceHandle{Kind: ontology.RelationshipSource, ID: decision.Relationship.RelationshipID, Version: int64(decision.Relationship.Version)}
	topic := testTopic("scheduler-topic")
	assignment := ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind,
		Assignment: &ontology.Assignment{Source: handle, DefinitionID: topic.ID}, Sources: []ontology.SourceDependency{f.source(t, handle)}}
	_, err = f.store.PublishAutomatic(ctx, f.team, testPublication(uuid.NewString(), 0, ontology.Change{Record: topic}, ontology.Change{Record: assignment}))
	require.NoError(t, err)
	return topic
}

func schedulerConfig(t *testing.T, f *ontologyFixture, global, team int) *settings.AppConfigServiceImpl {
	t.Helper()
	config := concurrencyConfig(t, f, global, team)
	_, err := config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{
		domain.AppConfigOntologyInputTokens: "100000000", domain.AppConfigOntologyOutputTokens: "100000000",
	}, "control", "", "")
	require.NoError(t, err)
	return config
}

func schedulerFixtureService(t *testing.T, f *ontologyFixture, config *settings.AppConfigServiceImpl, edit func(assessment.Request, *assessment.Response), repositories ...ontology.MaintenanceRepository) (*organization.MaintenanceService, func() int32) {
	t.Helper()
	var calls atomic.Int32
	var repository ontology.MaintenanceRepository = f.store
	if len(repositories) > 0 {
		repository = repositories[0]
	}
	service := organization.NewMaintenanceService(organization.MaintenanceDependencies{Repository: repository, Config: config, DefaultModel: "fixture-model", ProviderTimeout: time.Minute,
		Organizer: func(model string, accounting assessment.AttemptAccounting) *organization.Service {
			organizer, _ := organizationFixtureServiceWithAccounting(t, f, nil, func(request assessment.Request, response *assessment.Response) {
				calls.Add(1)
				for i := range response.Definitions {
					response.Definitions[i].Key += "-" + request.RequestID
					response.Definitions[i].Label = response.Definitions[i].Key
				}
				if edit != nil {
					edit(request, response)
				}
			}, model, maintenanceFixtureLimits(), accounting, map[string]int{"prompt_tokens": 40, "completion_tokens": 20, "total_tokens": 60})
			return organizer
		},
	})
	return service, calls.Load
}

func schedulerCanonical(t *testing.T, teams ...*ontologyFixture) string {
	t.Helper()
	var snapshots []map[string]string
	for _, team := range teams {
		snapshots = append(snapshots, team.canonicalSnapshot(t))
	}
	encoded, err := json.Marshal(snapshots)
	require.NoError(t, err)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func startScheduler(t *testing.T, service *organization.MaintenanceService, release func(), instances int) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	for range instances {
		workers.Add(1)
		go func() {
			defer workers.Done()
			organization.NewMaintenanceScheduler(service, nil).Start(ctx)
		}()
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	stop := func() {
		cancel()
		if release != nil {
			release()
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("mixed scheduler failed to join its workers")
		}
	}
	t.Cleanup(stop)
	return ctx, stop
}

func schedulerProjections(f *ontologyFixture, config *settings.AppConfigServiceImpl, before func(context.Context, community.TopicProjectionBatch) error, after func(error)) (*schedulerProjectionObserver, *communitypg.Store) {
	store := communitypg.NewStore(f.app, f.rls).WithTopics(f.store.NewTopicCatalogReader(), f.store.NewTopicMembershipReader(), f.store.NewTopicProjectionAdmission(), f.store.NewTopicProjectionRelease())
	wrapped := &schedulerProjectionStore{Store: store, before: before, after: after}
	service := communityapp.New(communityapp.Dependencies{Store: wrapped, AppConfig: config})
	return &schedulerProjectionObserver{ProjectionRunner: service}, store
}

func schedulerAssertDrained(t *testing.T, f *ontologyFixture) {
	t.Helper()
	var leases, batches, projections int
	require.NoError(t, f.admin.Raw(`SELECT
		(SELECT count(*) FROM ontology_maintenance_teams WHERE lease_until>clock_timestamp()),
		(SELECT count(*) FROM ontology_maintenance_batches WHERE status='running'),
		(SELECT count(*) FROM community_topic_work WHERE lease_until>clock_timestamp())`).Row().Scan(&leases, &batches, &projections))
	require.Zero(t, leases)
	require.Zero(t, batches)
	require.Zero(t, projections)
	status, err := f.store.MaintenanceStatus(context.Background(), time.Now())
	require.NoError(t, err)
	var pending, reservedInput, reservedOutput int64
	require.NoError(t, f.admin.Raw(`SELECT count(*) FILTER (WHERE reconciled_at IS NULL),
		COALESCE(sum(CASE WHEN COALESCE(reported_input,0)=0 THEN reserved_input ELSE 0 END),0),
		COALESCE(sum(CASE WHEN COALESCE(reported_output,0)=0 THEN reserved_output ELSE 0 END),0)
		FROM ontology_maintenance_attempts`).Row().Scan(&pending, &reservedInput, &reservedOutput))
	require.Zero(t, pending)
	require.Equal(t, reservedInput, status.Window.ReservedInput, "unreported usage keeps its conservative reservation")
	require.Equal(t, reservedOutput, status.Window.ReservedOutput)
}

func schedulerProvider(m *schedulerMeasurement, f *ontologyFixture, entered chan<- struct{}, gate <-chan struct{}) func(assessment.Request, *assessment.Response) {
	return func(_ assessment.Request, _ *assessment.Response) {
		m.mu.Lock()
		if m.ProviderCalls == 0 {
			m.FirstDispatchMS = float64(time.Since(m.started).Microseconds()) / 1000
		}
		m.ProviderCalls++
		m.providers++
		m.MaxProviderOverlap = max(m.MaxProviderOverlap, m.providers)
		m.MixedOverlap = m.MixedOverlap || m.projections > 0
		m.mu.Unlock()
		m.sample(f)
		select {
		case entered <- struct{}{}:
		default:
		}
		if gate != nil {
			<-gate
		} else {
			time.Sleep(200 * time.Millisecond)
		}
		m.mu.Lock()
		m.providers--
		m.mu.Unlock()
	}
}
