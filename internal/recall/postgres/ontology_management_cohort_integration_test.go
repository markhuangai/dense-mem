//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	"github.com/markhuangai/dense-mem/internal/settings"
	settingspg "github.com/markhuangai/dense-mem/internal/settings/postgres"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpg "github.com/markhuangai/dense-mem/internal/audit/postgres"
	application "github.com/markhuangai/dense-mem/internal/ontology"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestRecallOntologyManagementLockedCohort(t *testing.T) {
	data, err := os.ReadFile("../../../tests/eval/source_locks/ontology_management_v1.json")
	require.NoError(t, err)
	var lock struct {
		Cases []struct {
			ID            string   `json:"id"`
			Texts         []string `json:"texts"`
			Action        string   `json:"action"`
			SameOwner     bool     `json:"same_owner"`
			Reject        bool     `json:"reject"`
			ExpectedSlots int      `json:"expected_slots"`
			RollbackSlots int      `json:"rollback_slots"`
		}
	}
	require.NoError(t, json.Unmarshal(data, &lock))
	require.Len(t, lock.Cases, 3)
	digest := sha256.Sum256(data)
	for _, testCase := range lock.Cases {
		t.Run(testCase.ID, func(t *testing.T) {
			f := newRecallOntologyFixture(t)
			handles := []ontology.SourceHandle{}
			for i, text := range testCase.Texts {
				owner := i
				if testCase.SameOwner {
					owner = 0
				}
				handles = append(handles, f.evidence(t, owner, text, nil))
			}
			gold := map[string]bool{}
			for _, handle := range handles {
				gold[handle.ID] = true
			}
			groups := map[string]string{}
			for _, text := range testCase.Texts {
				if testCase.Reject {
					groups[text] = text
				} else {
					groups[text] = "same"
				}
			}
			forceDistinct := testCase.Action == "group_together"
			organizer, calls := f.organizerWithAccounting(t, groups, forceDistinct, nil)
			_, err := organizer.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "initial", Sources: handles})
			require.NoError(t, err)
			manager := application.NewManagementService(f.ontology, auditapp.New(auditpg.NewStore(f.app, f.rls)))
			actor := f.actor(0, "manager")
			page, err := manager.Records(actor, "", "", 1)
			require.NoError(t, err)
			priorCalls := calls.Load()
			draft, err := manager.SaveDraft(actor, "", ontology.SaveDraftInput{OperationKey: uuid.NewString(), ExpectedRevision: page.Revision, Reason: "Locked manager correction", Edits: []ontology.ManagementEdit{{Action: testCase.Action, Members: handles}}})
			check := func(expected int) {
				result, err := f.search.RecallEvidence(f.actor(2, "member"), RecallEvidenceInput{TeamID: f.team, SpaceID: f.space, Query: "Atlas PostgreSQL MongoDB", QueryEmbedding: []float32{1, 0, 0}, Limit: 10, OrganizationEnabled: true})
				require.NoError(t, err)
				require.Len(t, result.Results, expected)
				seen := map[string]bool{}
				for _, hit := range result.Results {
					for _, id := range append([]string{hit.EvidenceID}, hit.EquivalentEvidenceIDs...) {
						require.True(t, gold[id], "Bad@K increased")
						seen[id] = true
					}
				}
				require.Equal(t, gold, seen, "source or distinct-fact loss")
			}
			if !testCase.Reject {
				if forceDistinct {
					check(2)
				} else {
					check(1)
				}
			}
			if testCase.Reject {
				require.ErrorIs(t, err, ontology.ErrInvalid)
				check(testCase.ExpectedSlots)
				return
			}
			require.NoError(t, err)
			_, err = manager.Preview(actor, draft.ID, draft.Version)
			require.NoError(t, err)
			pub, err := manager.CommandDraft(actor, draft.ID, "publish", ontology.DraftCommand{OperationKey: uuid.NewString(), ExpectedVersion: draft.Version, ExpectedRevision: draft.BaseRevision, Reason: draft.Reason})
			require.NoError(t, err)
			require.Equal(t, priorCalls, calls.Load(), "manager operation called provider")
			config := settings.NewAppConfigService(settingspg.NewAppConfigRepository(f.app, f.rls), nil)
			_, err = config.UpdateOntologyMaintenanceSettings(context.Background(), map[string]string{domain.AppConfigOntologyEnabled: "true", domain.AppConfigOntologyModel: "fixture-model", domain.AppConfigOntologyStartTime: time.Now().UTC().Format("15:04")}, "control", "", "")
			require.NoError(t, err)
			_, err = config.UpdateGeneralSettings(context.Background(), map[string]string{domain.AppConfigTimezone: "UTC"}, "control", "", "")
			require.NoError(t, err)
			worker := application.NewMaintenanceService(application.MaintenanceDependencies{Repository: f.ontology, Config: config, Organizer: func(_ string, accounting assessment.AttemptAccounting) *application.Service {
				provider, _ := f.organizerWithAccounting(t, groups, forceDistinct, accounting)
				return provider
			}})
			drain := func() {
				for i := 0; i < 40; i++ {
					progress, runErr := worker.RunTurn(context.Background())
					require.NoError(t, runErr)
					if !progress {
						return
					}
				}
				t.Fatal("manager cohort maintenance did not quiesce")
			}
			drain()
			status, err := manager.Status(actor)
			require.NoError(t, err)
			require.True(t, status.CoverageComplete)

			check(testCase.ExpectedSlots)
			page, err = manager.Records(actor, "", "", 1)
			require.NoError(t, err)
			_, err = manager.Rollback(actor, pub.Publication.ID, ontology.DraftCommand{OperationKey: uuid.NewString(), ExpectedRevision: page.Revision, Reason: "Reverse locked correction"})
			require.NoError(t, err)
			drain()
			check(testCase.RollbackSlots)
		})
	}
	if directory := os.Getenv("DENSE_MEM_ORGANIZATION_REPORT_DIR"); directory != "" {
		report := map[string]any{"schema_version": "dense-mem.ontology.management_comparison.v1", "issue": 529, "source_lock_sha256": "sha256:" + hex.EncodeToString(digest[:]), "cases": 3, "sources": 6, "source_preservation": 1, "distinct_fact_preservation": 1, "false_consolidations": 0, "bad_at_k": 0, "manager_provider_calls": 0, "verified": !t.Failed()}
		body, err := json.MarshalIndent(report, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(directory, 0700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, "ontology-management-comparison.json"), append(body, '\n'), 0600))
	}
}
