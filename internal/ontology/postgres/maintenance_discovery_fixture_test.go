//go:build integration

package postgres

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type discoveryFixture struct {
	f       *ontologyFixture
	turn    ontology.MaintenanceTurn
	topic   ontology.Record
	sources []ontology.SourceHandle
	support ontology.SourceHandle
	calls   []*atomic.Int32
}

func discoveryEvidence(t *testing.T, f *ontologyFixture, index int) ontology.SourceHandle {
	t.Helper()
	id := fmt.Sprintf("00000000-0000-4000-8000-%012d", index)
	text := fmt.Sprintf("PostgreSQL discovery measurement source %04d.", index)
	result, err := f.knowledge.CreateIngestForTest(context.Background(), knowledge.CreateIngestInput{
		TeamID: f.team, OwnerProfileID: f.owners[index%len(f.owners)], IdempotencyKey: "discovery-" + id,
		RequestHash: testHash(text), Evidence: []knowledge.EvidenceInput{{FragmentID: id, Content: text, ForceInsert: true}}})
	require.NoError(t, err)
	return ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: result.Evidence[0].FragmentID, Version: 1}
}

func newDiscoveryFixture(t *testing.T, groups []int, outcomes []string, supported bool) *discoveryFixture {
	t.Helper()
	return newDiscoveryFixtureWithSetup(t, groups, outcomes, supported, false)
}

func newDiscoveryFixtureWithSetup(t *testing.T, groups []int, outcomes []string, supported, prepareSources bool) *discoveryFixture {
	t.Helper()
	x := &discoveryFixture{f: newOrganizationFixture(t), topic: testTopic("postgresql")}
	f := x.f
	if supported {
		x.support = discoveryEvidence(t, f, 9999)
		x.topic.Sources = []ontology.SourceDependency{f.source(t, x.support)}
	}
	revalidationPublish(t, f, ontology.Change{Record: x.topic})
	services := map[string]*organization.Service{}
	for _, mode := range []string{"organized", "ambiguous", "failed"} {
		var edit func(assessment.Request, *assessment.Response)
		if mode == "ambiguous" {
			edit = ambiguousClassification
		} else if mode == "failed" {
			edit = func(_ assessment.Request, response *assessment.Response) { response.RequestID = "invalid" }
		}
		service, calls := organizationFixtureServiceWithAccounting(t, f, nil, edit, "fixture-model", maintenanceFixtureLimits(), nil, 300000,
			map[string]int{"prompt_tokens": 17, "completion_tokens": 9, "total_tokens": 26})
		services[mode], x.calls = service, append(x.calls, calls)
	}
	var prepared [][]ontology.SourceHandle
	if prepareSources {
		for _, count := range groups {
			prepared = append(prepared, x.addSources(t, count))
		}
		x.claimTurn(t)
		x.drain(t)
	}
	for group, count := range groups {
		mode := "organized"
		if len(outcomes) != 0 {
			mode = outcomes[group]
		}
		var handles []ontology.SourceHandle
		if prepareSources {
			handles = prepared[group]
		} else {
			handles = x.addSources(t, count)
		}
		result, err := services[mode].Organize(context.Background(), f.team,
			ontology.OrganizationInput{OperationKey: fmt.Sprintf("discovery-receipt-%03d", group), Sources: handles})
		if mode == "failed" {
			require.Error(t, err)
			require.Equal(t, "provider_response_invalid", result.FailureCode)
		} else {
			require.NoError(t, err)
			require.True(t, result.Current)
		}
		require.NotEmpty(t, result.AssessmentID)
		bySource := map[string]ontology.OrganizationOutcome{}
		for _, outcome := range result.Outcomes {
			bySource[ontology.SourceKey(outcome.Source)] = outcome
		}
		// Seed queue outcomes from real durable assessments; this fixture measures discovery, not batch completion.
		require.NoError(t, f.store.withScope(context.Background(), f.team, false, func(tx *gorm.DB, fence scope) error {
			for _, handle := range handles {
				outcome, found := bySource[ontology.SourceKey(handle)]
				if !found {
					require.Equal(t, "failed", mode)
					outcome = ontology.OrganizationOutcome{Source: handle, Status: "failed", Reason: result.FailureCode}
				}
				status := ontology.MaintenanceOutcomeState(outcome, result, result.FailureCode)
				require.Equal(t, mode, status)
				if err := refreshMaintenanceSource(tx, fence, handle, false); err != nil {
					return err
				}
				if err := tx.Exec(`UPDATE ontology_maintenance_sources SET status=?,reason=?,assessment_id=?::uuid
				 WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND source_kind=? AND source_id=?`,
					status, outcome.Reason, result.AssessmentID, fence.TeamID, fence.SpaceID, fence.Generation, handle.Kind, handle.ID).Error; err != nil {
					return err
				}
			}
			return nil
		}))
	}
	if prepareSources {
		require.NoError(t, f.admin.Exec(`ANALYZE`).Error)
	} else {
		x.claimTurn(t)
	}
	x.drain(t)
	for i, handle := range x.sources {
		require.NotEmpty(t, revalidationQueue(t, f, handle).AssessmentID, "source %d", i)
	}
	return x
}

func (x *discoveryFixture) addSources(t *testing.T, count int) []ontology.SourceHandle {
	t.Helper()
	var handles []ontology.SourceHandle
	for range count {
		handle := discoveryEvidence(t, x.f, len(x.sources)+1)
		x.sources, handles = append(x.sources, handle), append(handles, handle)
	}
	return handles
}

func (x *discoveryFixture) claimTurn(t *testing.T) {
	t.Helper()
	window := maintenanceWindow(t, x.f, maintenanceSettings(t, x.f))
	turn, err := x.f.store.ClaimMaintenanceTurn(context.Background(), window.ID, time.Now().UTC(), 30*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	x.turn = *turn
	t.Cleanup(func() { require.NoError(t, x.f.store.ReleaseMaintenanceTurn(context.Background(), x.turn)) })
}

func (x *discoveryFixture) drain(t *testing.T) {
	t.Helper()
	for range 300 {
		require.NoError(t, x.f.store.discoverMaintenanceError(context.Background(), x.turn, ontology.MaintenancePageSize))
		var complete bool
		require.NoError(t, x.f.admin.Raw(`SELECT discovery_kind=4 AND NOT EXISTS(SELECT 1 FROM ontology_maintenance_markers WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?)
		 FROM ontology_maintenance_teams WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, x.f.team, x.f.space, x.f.generation, x.f.team, x.f.space, x.f.generation).Row().Scan(&complete))
		if complete {
			return
		}
	}
	t.Fatal("discovery fixture did not drain its setup markers")
}

func (x *discoveryFixture) marker(t *testing.T) {
	t.Helper()
	require.NoError(t, x.f.rls.WithTeamTx(context.Background(), x.f.app, x.f.team, func(tx *gorm.DB) error {
		return tx.Exec(`INSERT INTO ontology_maintenance_markers(team_id,shared_space_id,space_generation,anchor_kind,anchor_id,target_kind,target_id)
		 VALUES(?::uuid,?::uuid,?,'ontology_record_heads',?,'definition',?)`, x.f.team, x.f.space, x.f.generation, x.topic.ID, x.topic.ID).Error
	}))
}

func (x *discoveryFixture) providerCalls() int32 {
	var count int32
	for _, calls := range x.calls {
		count += calls.Load()
	}
	return count
}

func (x *discoveryFixture) queue(t *testing.T) []revalidationQueueState {
	t.Helper()
	var states []revalidationQueueState
	for _, source := range x.sources {
		states = append(states, revalidationQueue(t, x.f, source))
	}
	return states
}

type discoveryMeasurementKey struct{}

type discoveryRecorder struct {
	logger.Interface
	mu                      sync.Mutex
	events                  []maintenanceDiscoveryEvent
	queries, receiptQueries int
	sqlTime, receiptSQLTime time.Duration
	active                  bool
	after                   maintenanceDiscoveryObserver
}

func (r *discoveryRecorder) observe(event maintenanceDiscoveryEvent) {
	r.mu.Lock()
	r.events = append(r.events, event)
	if event.Phase == "receipt_started" {
		r.active = true
	} else if event.Phase == "receipt_finished" {
		r.active = false
	}
	r.mu.Unlock()
	if r.after != nil {
		r.after(event)
	}
}

func (r *discoveryRecorder) Trace(ctx context.Context, started time.Time, statement func() (string, int64), err error) {
	if ctx.Value(discoveryMeasurementKey{}) == r {
		duration := time.Since(started)
		r.mu.Lock()
		r.queries++
		r.sqlTime += duration
		if r.active {
			r.receiptQueries++
			r.receiptSQLTime += duration
		}
		r.mu.Unlock()
	}
	r.Interface.Trace(ctx, started, statement, err)
}

func (x *discoveryFixture) recorder(ctx context.Context, observe bool) (*Store, context.Context, *discoveryRecorder) {
	r := &discoveryRecorder{Interface: x.f.app.Logger}
	ctx = context.WithValue(ctx, discoveryMeasurementKey{}, r)
	if observe {
		ctx = context.WithValue(ctx, maintenanceDiscoveryObservationKey{}, maintenanceDiscoveryObserver(r.observe))
	}
	return NewStore(x.f.app.Session(&gorm.Session{Logger: r}), x.f.rls), ctx, r
}

func (r *discoveryRecorder) completed() []maintenanceDiscoveryEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var result []maintenanceDiscoveryEvent
	for _, event := range r.events {
		if event.Phase == "receipt_finished" {
			result = append(result, event)
		}
	}
	return result
}

func discoverySortedSources(sources []ontology.SourceHandle) []ontology.SourceHandle {
	result := append([]ontology.SourceHandle(nil), sources...)
	sort.Slice(result, func(i, j int) bool { return ontology.SourceKey(result[i]) < ontology.SourceKey(result[j]) })
	return result
}
