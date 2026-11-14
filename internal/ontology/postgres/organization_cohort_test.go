package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"

	"github.com/google/uuid"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	evalharness "github.com/markhuangai/dense-mem/internal/testfixture/evaluation"
	"github.com/stretchr/testify/require"
)

func testOntologyOrganizationCohort(t *testing.T) { testOntologyCohort(t, false) }

func testOntologyCohort(t *testing.T, maintenance bool, supplements ...[]evalharness.OrganizationFollowupCase) {
	testOntologyCohortWithMatching(t, maintenance, nil, supplements...)
}

func testOntologyCohortWithMatching(t *testing.T, maintenance bool, matching []evalharness.OrganizationMatchingCase, supplements ...[]evalharness.OrganizationFollowupCase) {
	f := newOrganizationFixture(t)
	cohort := evalharness.OntologyOrganizationCohort()
	lockPath := "../../testfixture/evaluation/testdata/organization_cohort.json"
	followups := map[string]evalharness.OrganizationFollowupCase{}
	var judgments any = cohort
	matchingCases := map[string]evalharness.OrganizationMatchingCase{}
	if len(matching) > 0 {
		cohort, judgments = nil, matching
		lockPath = "../../testfixture/evaluation/testdata/ontology_matching_v1.json"
		for _, item := range matching {
			cohort = append(cohort, item.Case)
			matchingCases[item.Case.ID] = item
		}
	}
	if len(supplements) > 0 {
		cohort = nil
		judgments = supplements[0]
		lockPath = "../../testfixture/evaluation/testdata/ontology_followups_v1.json"
		for _, supplement := range supplements[0] {
			cohort = append(cohort, supplement.Case)
			followups[supplement.Case.ID] = supplement
		}
	}
	encoded, err := json.Marshal(judgments)
	require.NoError(t, err)
	digest := sha256.Sum256(encoded)
	var lock struct {
		CohortSHA256    string `json:"cohort_sha256"`
		GeneratorSHA256 string `json:"generator_sha256"`
	}
	baselineBytes, err := os.ReadFile(lockPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(baselineBytes, &lock))
	require.Equal(t, lock.CohortSHA256, "sha256:"+hex.EncodeToString(digest[:]))
	if len(followups) > 0 || len(matching) > 0 {
		generatorPath := "../../testfixture/evaluation/organization_followup_cohort.go"
		if len(matching) > 0 {
			generatorPath = "../../testfixture/evaluation/organization_matching_cohort.go"
		}
		generator, err := os.ReadFile(generatorPath)
		require.NoError(t, err)
		generatorDigest := sha256.Sum256(generator)
		require.Equal(t, lock.GeneratorSHA256, "sha256:"+hex.EncodeToString(generatorDigest[:]))
	}
	scores := map[string]evalharness.OrganizationScore{}
	reports := map[string]ontology.OrganizationResult{}
	measurements := map[string]map[string]any{}
	model := "fixture-model"
	for _, testCase := range cohort {
		t.Run(testCase.ID, func(t *testing.T) {
			if maintenance {
				f = newOrganizationFixture(t)
			}
			var maintenanceLog bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&maintenanceLog, nil))
			originals := map[string]evalharness.OrganizationSource{}
			byText := map[string]string{}
			handles := []ontology.SourceHandle{}
			for i, source := range testCase.Sources {
				owner := i % len(f.owners)
				if testCase.Override == "group_together" {
					owner = 0
				}
				var handle ontology.SourceHandle
				if supplement, ok := followups[testCase.ID]; ok {
					handle = f.organizationEvidenceAt(t, owner, source.Text, supplement.CreatedAt[i], supplement.Timezone)
				} else {
					handle = f.organizationEvidence(t, owner, source.Text, map[string]any{"actor": source.Actor, "polarity": source.Polarity, "time": source.Time, "qualification": source.Qualification})
				}
				handles = append(handles, handle)
				originals[handle.ID] = source
				byText[source.Text] = source.EquivalenceKey
			}
			var vocabularyID string
			if matchingCase, ok := matchingCases[testCase.ID]; ok {
				measurements[testCase.ID] = seedMatchingCohortVocabulary(t, f, matchingCase, handles)
			}
			if followups[testCase.ID].StaleVocabulary {
				definitions := seedStaleVocabulary(t, f, 20, 1)
				vocabularyID = definitions[0].ID
				contextData, err := f.store.ReadOrganization(context.Background(), f.team, handles)
				require.NoError(t, err)
				require.Len(t, contextData.Candidates, 1)
				require.Equal(t, vocabularyID, contextData.Candidates[0].ID)
			}
			if testCase.Override != "" {
				record := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.OverrideAction(testCase.Override), Members: handles}}
				for _, handle := range handles {
					record.Sources = append(record.Sources, f.source(t, handle))
				}
				page, err := f.store.ListRecords(context.Background(), f.team, "", "", 1)
				require.NoError(t, err)
				_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("override-"+testCase.ID, page.Revision, ontology.Change{Record: record}))
				require.NoError(t, err)
			}
			before := f.canonicalSnapshot(t)
			var service *organization.Service
			var maintained *organization.MaintenanceService
			var callCount func() int32
			ctx := context.Background()
			fixture, calls := organizationFixtureService(t, f, func(a, b assessment.Item) bool { return byText[a.Text] == byText[b.Text] }, nil)
			service = fixture
			callCount = calls.Load
			if maintenance {
				var counters []*atomic.Int32
				maintained = organization.NewMaintenanceService(organization.MaintenanceDependencies{Repository: f.store, Config: maintenanceSettings(t, f), DefaultModel: model, Logger: logger, ProviderTimeout: time.Minute, Organizer: func(model string, contextWindowTokens int64, accounting assessment.AttemptAccounting) *organization.Service {
					service, count := organizationFixtureServiceWithAccounting(t, f, func(a, b assessment.Item) bool { return byText[a.Text] == byText[b.Text] }, nil, model, assessor.DefaultSemanticAssessmentLimits(), accounting, int(contextWindowTokens))
					counters = append(counters, count)
					return service
				}})
				callCount = func() int32 {
					var total int32
					for _, count := range counters {
						total += count.Load()
					}
					return total
				}
			}
			input := ontology.OrganizationInput{OperationKey: "cohort-" + testCase.ID, Sources: handles}
			var result ontology.OrganizationResult
			started := time.Now()
			if maintenance {
				for range 100 {
					progress, err := maintained.RunTurn(ctx)
					require.NoError(t, err)
					if !progress {
						break
					}
				}
				status, err := maintained.Status(ctx)
				require.NoError(t, err)
				require.True(t, status.DiscoveryComplete)
				require.Zero(t, status.Counts.Pending)
				require.Zero(t, status.Counts.Failed)
				rows, err := f.admin.Raw(`SELECT body FROM ontology_assessments WHERE team_id=?::uuid ORDER BY created_at`, f.team).Rows()
				require.NoError(t, err)
				for rows.Next() {
					var body []byte
					require.NoError(t, rows.Scan(&body))
					var receipt ontology.OrganizationReceipt
					require.NoError(t, json.Unmarshal(body, &receipt))
					result.Attempts = append(result.Attempts, receipt.Result.Attempts...)
				}
				require.NoError(t, rows.Err())
				require.NoError(t, rows.Close())
			} else {
				result, err = service.Organize(ctx, f.team, input)
			}
			reports[testCase.ID] = result
			measurement := measurements[testCase.ID]
			if measurement == nil {
				measurement = map[string]any{}
			}
			measurement["processing_ms"] = time.Since(started).Milliseconds()
			if callCount != nil {
				measurement["provider_calls"] = callCount()
			}
			if maintenance {
				var discovery []map[string]any
				for _, line := range bytes.Split(bytes.TrimSpace(maintenanceLog.Bytes()), []byte("\n")) {
					var event map[string]any
					require.NoError(t, json.Unmarshal(line, &event))
					if event["msg"] == "ontology maintenance discovery" {
						discovery = append(discovery, event)
					}
				}
				require.NotEmpty(t, discovery)
				measurement["discovery"] = discovery
			}
			measurements[testCase.ID] = measurement
			require.NoError(t, err)
			require.Empty(t, result.FailureCode)
			groups, err := f.store.ListRecords(context.Background(), f.team, ontology.EvidenceGroup, "", ontology.MaxPageSize)
			require.NoError(t, err)
			items := []evalharness.OrganizationItem{}
			grouped := map[string]bool{}
			for _, view := range groups.Records {
				if !view.Current {
					continue
				}
				item := evalharness.OrganizationItem{ID: view.ID}
				for _, member := range view.Group.Members {
					source, ok := originals[member.ID]
					if ok {
						item.SourceIDs = append(item.SourceIDs, source.ID)
						grouped[member.ID] = true
					}
				}
				if len(item.SourceIDs) > 0 {
					require.Len(t, item.SourceIDs, len(view.Group.Members), "group crossed its audited cohort")
					items = append(items, item)
				}
			}
			for id, source := range originals {
				if !grouped[id] {
					items = append(items, evalharness.OrganizationItem{ID: id, SourceIDs: []string{source.ID}})
				}
			}
			score, err := evalharness.ScoreOrganization(testCase, items)
			require.NoError(t, err)
			scores[testCase.ID] = score
			require.Zero(t, score.FalseConsolidations)
			require.Equal(t, 1.0, score.DistinctFactCoverage)
			require.Equal(t, 1.0, score.SourcePreservation)
			require.Zero(t, score.RepeatedSlots)
			if vocabularyID != "" {
				assignments, err := f.store.ListRecords(context.Background(), f.team, ontology.AssignmentKind, "", ontology.MaxPageSize)
				require.NoError(t, err)
				for _, assignment := range assignments.Records {
					if _, ok := originals[assignment.Assignment.Source.ID]; ok {
						require.True(t, assignment.Current)
						require.Equal(t, vocabularyID, assignment.Assignment.DefinitionID)
					}
				}
			}
			require.Equal(t, before, f.canonicalSnapshot(t))
			priorCalls := int32(0)
			if callCount != nil {
				priorCalls = callCount()
			}
			input.OperationKey += "-replay"
			if maintenance {
				for range 8 {
					_, err := maintained.RunTurn(ctx)
					require.NoError(t, err)
				}
			} else {
				replay, err := service.Organize(ctx, f.team, input)
				require.NoError(t, err)
				require.True(t, replay.Existing)
				require.Equal(t, result.AssessmentID, replay.AssessmentID)
			}
			if callCount != nil {
				require.Equal(t, priorCalls, callCount())
			}
		})
	}
}
