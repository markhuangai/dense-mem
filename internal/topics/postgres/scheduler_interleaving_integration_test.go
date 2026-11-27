//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
	searchpostgres "github.com/markhuangai/dense-mem/internal/search/postgres"
	organization "github.com/markhuangai/dense-mem/internal/topics"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"

	"github.com/stretchr/testify/require"
)

type interleavingEmbeddings struct{}

func (interleavingEmbeddings) EmbedBatch(_ context.Context, input []string) ([][]float32, string, error) {
	vectors := make([][]float32, len(input))
	for i := range vectors {
		vectors[i] = []float32{1, 0, 0}
	}
	return vectors, "fixture-embedding", nil
}
func (interleavingEmbeddings) ModelName() string { return "fixture-embedding" }
func (interleavingEmbeddings) Dimensions() int   { return 3 }
func (interleavingEmbeddings) IsAvailable() bool { return true }

type interleavingDecisions struct{}

func (interleavingDecisions) DecisionRuntimeConfig(context.Context) (domain.DecisionRuntimeConfig, error) {
	return domain.DecisionRuntimeConfig{Revision: "interleaving", Provider: domain.DecisionProviderStatus{MaxInputTokens: 128000, MaxQuestionInputTokens: 128000},
		Effective: domain.DecisionTasks{TopicAssignment: true, CommunityPlacement: true}}, nil
}

func (interleavingDecisions) Decide(_ context.Context, request modelprovider.DecisionRequest) (modelprovider.DecisionResult, error) {
	answers := make(map[string]modelprovider.DecisionAnswer)
	for _, group := range request.Groups {
		for _, question := range group.Questions {
			if question.Type == modelprovider.DecisionChoice {
				probabilities := make(map[string]float64, len(question.Choices))
				for _, choice := range question.Choices {
					probabilities[choice.Value] = 0
				}
				probabilities["standalone"] = 1
				confidence := 1.0
				answers[question.Name] = modelprovider.DecisionAnswer{Type: modelprovider.DecisionChoice, Choice: "standalone", Confidence: &confidence, Probabilities: probabilities}
				continue
			}
			probability := 0.1
			if request.Workload == "topic_assignment" && strings.Contains(question.Name, "match_") {
				probability = 0.9
			}
			answers[question.Name] = modelprovider.DecisionAnswer{Type: modelprovider.DecisionPredicate, Probability: &probability}
		}
	}
	return modelprovider.DecisionResult{Answers: answers}, nil
}

func TestTopicMaintenanceFiveStageInterleaving(t *testing.T) {
	f := newOrganizationFixture(t)
	ctx := context.Background()
	_, err := searchpostgres.NewStore(f.admin, f.rls).EnsureActiveSearchContract(ctx, searchmaintenance.EnsureActiveSearchContractInput{Model: "fixture-embedding", Dimensions: 3})
	require.NoError(t, err)
	first, second := testTopic("scheduler-database"), testTopic("scheduler-storage")
	revalidationPublish(t, f, topic.Change{Record: first}, topic.Change{Record: second})
	for i := range topic.MaxOrganizationSources + 1 {
		text := fmt.Sprintf("Scheduler database observation %d.", i)
		source := f.organizationEvidence(t, 0, text, nil)
		document, err := f.knowledge.UpsertSearchDocument(ctx, knowledge.UpsertSearchDocumentInput{TeamID: f.team, OwnerProfileID: f.owners[0],
			SourceKind: "evidence", SourceID: source.ID, SourceVersion: 1, DocumentText: text,
			SpaceID: f.space, SpaceGeneration: f.generation, SpaceKind: "team_shared"})
		require.NoError(t, err)
		loaded, err := f.knowledge.LoadSearchDocumentsForEmbedding(ctx, knowledge.LoadSearchDocumentsForEmbeddingInput{TeamID: f.team,
			OwnerProfileID: f.owners[0], SearchDocumentIDs: []string{document.SearchDocumentID}})
		require.NoError(t, err)
		require.Len(t, loaded, 1)
		item := loaded[0]
		err = f.knowledge.CompleteSearchDocumentsWithEmbeddings(ctx, knowledge.CompleteSearchDocumentsWithEmbeddingsInput{TeamID: f.team,
			OwnerProfileID: f.owners[0], Documents: []knowledge.SearchDocumentEmbedding{{SearchDocumentID: item.SearchDocumentID,
				DocumentHash: item.DocumentHash, SourceVersion: item.SourceVersion, DocumentVersion: item.DocumentVersion,
				ProjectionFormat: item.ProjectionFormat, ProjectionGenerationID: item.ProjectionGenerationID,
				EmbeddingContractID: item.EmbeddingContractID, EmbeddingDimensions: item.EmbeddingDimensions,
				Embedding: []float32{1, 0, 0}, SpaceID: item.SpaceID, SpaceGeneration: item.SpaceGeneration}}})
		require.NoError(t, err)
	}
	config := maintenanceSettings(t, f)
	window := maintenanceWindow(t, f, config)
	limits := maintenanceFixtureLimits()
	transport, _ := cohortTopicTransport(t, window.Policy.Model)
	decisions := interleavingDecisions{}
	service := organization.NewMaintenanceService(organization.MaintenanceDependencies{
		Repository: f.store, Config: config, DefaultModel: window.Policy.Model, ProviderTimeout: time.Minute,
		Organizer: func(model string, contextWindow int64, accounting assessment.AttemptAccounting) *organization.Service {
			organizer, _ := organizationFixtureServiceWithAccounting(t, f, nil, nil, model, limits, accounting, int(contextWindow))
			return organizer
		},
	})
	assigner := organization.NewAssignmentService(f.store, config, transport, decisions, limits, window.Policy.Model, nil)
	service.WithTopicProcessing(assigner, f.store, f.store, organization.CommunityAssessor{Transport: transport, Limits: limits, Decisions: decisions, Settings: decisions}, decisions)
	service.WithTopicEmbeddings(f.store, interleavingEmbeddings{}, time.Minute)
	for turn := range 5 {
		progress, err := service.RunTurn(ctx)
		require.NoError(t, err, "stage turn %d", turn)
		require.True(t, progress)
		if turn == 0 {
			status, err := service.Status(ctx)
			require.NoError(t, err)
			require.EqualValues(t, 1, status.Counts.Pending, "assignment work must remain while later stages run")
		}
		if turn == 1 {
			var count int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_embeddings WHERE team_id=?::uuid`, f.team).Row().Scan(&count))
			require.Equal(t, 2, count)
		}
	}
	var kinds []string
	require.NoError(t, f.admin.Raw(`SELECT batch_kind FROM topic_maintenance_batches WHERE team_id=?::uuid ORDER BY created_at,batch_id`, f.team).Scan(&kinds).Error)
	require.Equal(t, []string{"assignment", "consolidation", "community", "equivalence"}, kinds)
	status, err := service.Status(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Counts.Pending)
	require.EqualValues(t, 20, status.Equivalence.Completed)
}

func TestOntologyMaintenanceParallelSchedulerFillsAvailableSlots(t *testing.T) {
	for _, limits := range [][2]int{{1, 1}, {4, 1}, {4, 4}, {8, 8}} {
		t.Run(fmt.Sprintf("global-%d-team-%d", limits[0], limits[1]), func(t *testing.T) {
			f := newOrganizationFixture(t)
			for i := range limits[1]*topic.MaxOrganizationSources + 1 {
				f.organizationEvidence(t, i%len(f.owners), fmt.Sprintf("Slot fill source %d.", i), nil)
			}
			config := schedulerConfig(t, f, limits[0], limits[1])
			m := &schedulerMeasurement{started: time.Now(), MaxTeamAdmitted: map[string]int{}}
			gate, firstGate, entered := make(chan struct{}), make(chan struct{}), make(chan struct{}, 100)
			var once, firstOnce sync.Once
			releaseFirst := func() { firstOnce.Do(func() { close(firstGate) }) }
			release := func() { releaseFirst(); once.Do(func() { close(gate) }) }
			firstProvider := schedulerProvider(m, f, entered, firstGate)
			otherProvider := schedulerProvider(m, f, entered, gate)
			var dispatched atomic.Int32
			service, _ := schedulerFixtureService(t, f, config, func(request assessment.Request, response *assessment.Response) {
				if dispatched.Add(1) == 1 {
					firstProvider(request, response)
				} else {
					otherProvider(request, response)
				}
			})
			canonical := schedulerCanonical(t, f)
			_, stop := startScheduler(t, service, release, 1)
			defer stop()
			for range limits[1] {
				select {
				case <-entered:
				case <-time.After(30 * time.Second):
					t.Fatal("refused preparation did not retry after lease transfer")
				}
			}
			m.mu.Lock()
			overlap, maxAdmitted := m.MaxProviderOverlap, m.MaxAdmitted
			errors := append([]string(nil), m.Errors...)
			m.mu.Unlock()
			require.Equal(t, limits[1], overlap)
			require.LessOrEqual(t, maxAdmitted, limits[0])
			require.Empty(t, errors)
			if limits[1] > 1 {
				releaseFirst()
				select {
				case <-entered:
				case <-time.After(30 * time.Second):
					t.Fatal("released slot was not refilled while other providers remained blocked")
				}
				select {
				case <-gate:
					t.Fatal("the independent provider blockers were released before refill")
				default:
				}
				m.mu.Lock()
				providers, maxAdmitted := m.providers, m.MaxAdmitted
				errors := append([]string(nil), m.Errors...)
				m.mu.Unlock()
				require.Equal(t, limits[1], providers)
				require.LessOrEqual(t, maxAdmitted, limits[0])
				require.Empty(t, errors)
			}
			stop()
			schedulerAssertDrained(t, f)
			require.Equal(t, canonical, schedulerCanonical(t, f))
		})
	}
}

func TestOntologyMaintenanceParallelSchedulerDrainControls(t *testing.T) {
	for _, action := range []string{"pause", "disable", "cancel"} {
		t.Run(action, func(t *testing.T) {
			f := newOrganizationFixture(t)
			concurrencySources(t, f)
			config := schedulerConfig(t, f, 4, 4)
			gate, entered := make(chan struct{}), make(chan struct{})
			var releaseOnce, entryOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(gate) }) }
			service, _ := schedulerFixtureService(t, f, config, func(_ assessment.Request, _ *assessment.Response) { entryOnce.Do(func() { close(entered) }); <-gate })
			canonical := schedulerCanonical(t, f)
			ctx, stop := startScheduler(t, service, release, 1)
			defer stop()
			select {
			case <-entered:
			case <-time.After(30 * time.Second):
				t.Fatal("provider did not enter before control")
			}
			if action == "pause" {
				_, err := f.store.MaintenanceCommand(context.Background(), domain.TopicMaintenanceCommand{OperationKey: "evidence-pause", Action: "pause"}, time.Now(), nil)
				require.NoError(t, err)
			} else if action == "disable" {
				_, err := config.UpdateTopicMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigTopicMaintenanceEnabled: "false"}, "control", "", "")
				require.NoError(t, err)
			}
			var before int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_maintenance_attempts`).Row().Scan(&before))
			if action == "cancel" {
				joined := make(chan struct{})
				go func() { stop(); close(joined) }()
				<-ctx.Done()
				select {
				case <-joined:
				case <-time.After(10 * time.Second):
					t.Fatal("scheduler did not join cancelled work")
				}
			} else {
				release()
				require.Eventually(t, func() bool {
					var active int
					err := f.admin.Raw(`SELECT count(*) FROM topic_maintenance_batches WHERE status='running'`).Row().Scan(&active)
					return err == nil && active == 0
				}, 10*time.Second, 10*time.Millisecond)
				stop()
				var after int
				require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_maintenance_attempts`).Row().Scan(&after))
				require.Equal(t, before, after, "stopped admission cannot create a new provider attempt")
			}
			schedulerAssertDrained(t, f)
			require.Equal(t, canonical, schedulerCanonical(t, f))
		})
	}
}
