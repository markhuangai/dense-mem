package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	organization "github.com/markhuangai/dense-mem/internal/ontology"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
	"github.com/stretchr/testify/require"
)

func testOntologyOrganizationCohort(t *testing.T) {
	f := newOrganizationFixture(t)
	cohort := evalharness.OntologyOrganizationCohort()
	encoded, err := json.Marshal(cohort)
	require.NoError(t, err)
	digest := sha256.Sum256(encoded)
	var lock struct {
		CohortSHA256 string `json:"cohort_sha256"`
	}
	baselineBytes, err := os.ReadFile("../../../tests/eval/baselines/ontology_organization_v1.json")
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(baselineBytes, &lock))
	require.Equal(t, lock.CohortSHA256, "sha256:"+hex.EncodeToString(digest[:]))
	scores := map[string]evalharness.OrganizationScore{}
	reports := map[string]ontology.OrganizationResult{}
	live := os.Getenv("DENSE_MEM_ONTOLOGY_LIVE_EVAL") == "1"
	diagnostic := os.Getenv("DENSE_MEM_ONTOLOGY_DIAGNOSTIC") == "1"
	directory := os.Getenv("DENSE_MEM_ORGANIZATION_REPORT_DIR")
	if diagnostic {
		require.True(t, live, "diagnostics require live-provider mode")
		require.NotEmpty(t, directory, "diagnostics require an isolated report directory")
	}
	if directory != "" {
		require.NoError(t, os.MkdirAll(directory, 0700))
	}
	model := "fixture-model"
	identity := ""
	for _, testCase := range cohort {
		t.Run(testCase.ID, func(t *testing.T) {
			defer func() {
				if directory != "" {
					writeOrganizationDiagnostic(t, filepath.Join(directory, testCase.ID+"-result.json"), map[string]any{"case_id": testCase.ID, "diagnostic_only": diagnostic, "ineligible_for_quality_gate": diagnostic, "failed": t.Failed(), "result": reports[testCase.ID], "score": scores[testCase.ID], "cohort_sha256": lock.CohortSHA256, "model": model, "provider_identity": identity})
				}
			}()
			originals := map[string]evalharness.OrganizationSource{}
			byText := map[string]string{}
			handles := []ontology.SourceHandle{}
			for i, source := range testCase.Sources {
				owner := i % len(f.owners)
				if testCase.Override == "group_together" {
					owner = 0
				}
				handle := f.organizationEvidence(t, owner, source.Text, map[string]any{"actor": source.Actor, "polarity": source.Polarity, "time": source.Time, "qualification": source.Qualification})
				handles = append(handles, handle)
				originals[handle.ID] = source
				byText[source.Text] = source.EquivalenceKey
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
			var callCount func() int32
			ctx := context.Background()
			if live {
				cfg, err := config.Load()
				require.NoError(t, err)
				model = cfg.GetAIVerifierModel()
				limits := assessorprovider.SemanticAssessmentLimitsForConfig(&cfg)
				transport := assessorprovider.NewOpenAIAssessorWithAssessmentLimits(&cfg, nil, limits)
				provider := assessment.NewProvider(transport, model, limits)
				identity = provider.Identity()
				service = organization.NewService(f.store, provider)
				if directory != "" {
					recorder := newOrganizationDiagnosticRecorder(t, directory, testCase.ID, cfg)
					ctx = modelprovider.WithExchangeRecorder(ctx, recorder)
					callCount = func() int32 { return int32(recorder.sequence) }
				}
			} else {
				fixture, calls := organizationFixtureService(t, f, func(a, b assessment.Item) bool { return byText[a.Text] == byText[b.Text] }, nil)
				service = fixture
				callCount = calls.Load
			}
			input := ontology.OrganizationInput{OperationKey: "cohort-" + testCase.ID, Sources: handles}
			result, err := service.Organize(ctx, f.team, input)
			reports[testCase.ID] = result
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
			require.Equal(t, before, f.canonicalSnapshot(t))
			priorCalls := int32(0)
			if callCount != nil {
				priorCalls = callCount()
			}
			input.OperationKey += "-replay"
			replay, err := service.Organize(ctx, f.team, input)
			require.NoError(t, err)
			require.True(t, replay.Existing)
			require.Equal(t, result.AssessmentID, replay.AssessmentID)
			if callCount != nil {
				require.Equal(t, priorCalls, callCount())
			}
		})
	}
	if directory != "" {
		require.NoError(t, os.MkdirAll(directory, 0700))
		report := map[string]any{"schema_version": "dense-mem.ontology.organization_run.v1", "cohort_sha256": lock.CohortSHA256, "live": live, "model": model, "provider_identity": identity, "scores": scores, "results": reports, "failed": t.Failed(), "diagnostic_only": diagnostic, "ineligible_for_quality_gate": diagnostic}
		if !t.Failed() {
			report["false_consolidations"], report["source_preservation"], report["distinct_fact_coverage"], report["repeated_slots"] = 0, 1, 1, 0
		}
		output, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "organization-"+uuid.NewString()+".json"), output, 0600))
	}
}
