//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpg "github.com/markhuangai/dense-mem/internal/audit/postgres"
	application "github.com/markhuangai/dense-mem/internal/topics"
	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

func TestRecallOntologyManagementLockedCohort(t *testing.T) {
	data, err := os.ReadFile("../../testfixture/evaluation/testdata/ontology_management_v1.json")
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
			_, err = manager.SaveDraft(actor, "", ontology.SaveDraftInput{OperationKey: uuid.NewString(), ExpectedRevision: page.Revision, Reason: "Locked manager correction", Edits: []ontology.ManagementEdit{{Action: testCase.Action, Members: handles}}})
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
			require.ErrorIs(t, err, ontology.ErrInvalid)
			if testCase.Reject || forceDistinct {
				check(2)
			} else {
				check(1)
			}
			drafts, err := manager.Drafts(actor, "", 20)
			require.NoError(t, err)
			require.Empty(t, drafts.Drafts)
			require.Equal(t, priorCalls, calls.Load())

		})
	}
}
