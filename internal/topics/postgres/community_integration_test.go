//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/provider/decision"
	"github.com/markhuangai/dense-mem/internal/topics"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	assignment "github.com/markhuangai/dense-mem/internal/topics/assignment"
	"github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestCommunityPlacementChecksRevisionsAndAccountsForProviderWork(t *testing.T) {
	ctx := t.Context()
	f := newOntologyFixture(t)
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
	child := testTopic("postgresql-backup")
	child.ID = "10000000-0000-4000-8000-000000000001"
	child.Definition.Label = "PostgreSQL backup"
	parent := testTopic("postgresql")
	parent.ID = "10000000-0000-4000-8000-000000000002"
	parent.Definition.Label = "PostgreSQL"
	_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("community-seed", 0,
		contract.Change{Record: child}, contract.Change{Record: parent}))
	require.NoError(t, err)

	claim := func() (contract.CommunityInput, contract.MaintenanceClaim) {
		t.Helper()
		turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
		require.NoError(t, err)
		require.NotNil(t, turn)
		input, batch, err := f.store.ClaimCommunity(ctx, *turn, window.ID, time.Now())
		require.NoError(t, err)
		require.NotNil(t, input)
		require.NotNil(t, batch)
		return *input, *batch
	}

	input, batch := claim()
	require.Equal(t, child.ID, input.Topic.ID)
	require.Len(t, input.Candidates, 1)
	require.Equal(t, parent.ID, input.Candidates[0].ID)
	attempt := uuid.NewString()
	require.NoError(t, f.store.AdmitMaintenanceAttempt(ctx, batch, attempt, 1, 100, 200, time.Now()))
	require.NoError(t, f.store.ReconcileMaintenanceAttempt(ctx, batch, attempt, contract.AssessmentAttempt{Number: 1}))
	require.NoError(t, f.store.CompleteCommunity(ctx, batch, input, contract.CommunityOutcome{
		InputHash: "sha256:placement", Method: "generation", Status: "placed", ParentID: parent.ID,
	}, "", time.Now()))
	visible, err := f.store.GetRecord(f.actor(1, "member"), f.team, child.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "placed", visible.CommunityPlacement)
	require.Equal(t, parent.ID, visible.CommunityParentID)

	view, err := f.store.GetRecord(f.actor(0, "manager"), f.team, parent.ID, 0)
	require.NoError(t, err)
	view.Definition.Description = "Database administration and operations."
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("change-parent", 1,
		contract.Change{Record: view.Record, ExpectedVersion: 1}))
	require.NoError(t, err)
	input, batch = claim()
	require.Equal(t, child.ID, input.Topic.ID)
	require.Len(t, input.Candidates, 1)
	view, err = f.store.GetRecord(f.actor(0, "manager"), f.team, parent.ID, 0)
	require.NoError(t, err)
	view.Retired = true
	_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("retire-parent", 2,
		contract.Change{Record: view.Record, ExpectedVersion: 2}))
	require.NoError(t, err)
	err = f.store.CompleteCommunity(ctx, batch, input, contract.CommunityOutcome{
		InputHash: "sha256:stale-placement", Method: "decision", Status: "placed", ParentID: parent.ID,
	}, "", time.Now())
	require.ErrorIs(t, err, contract.ErrSourceStale)
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, batch, contract.OrganizationResult{}, "source_stale", time.Now()))
	input, batch = claim()
	require.Equal(t, child.ID, input.Topic.ID)
	require.Empty(t, input.Candidates)
	require.NoError(t, f.store.CompleteCommunity(ctx, batch, input, contract.CommunityOutcome{
		InputHash: "sha256:standalone", Method: "deterministic", Status: "standalone",
	}, "", time.Now()))
	status, err := f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Communities.Standalone)
	require.Zero(t, status.ActiveWork)
	require.Zero(t, status.Usage.UnreconciledAttempts)
	require.EqualValues(t, 1, status.Usage.UnavailableUsageAttempts)

	t.Run("manager publication rolls back when a later edge closes a mixed cycle", testCommunityMixedHierarchyPublicationRollback)
	t.Run("parent metadata revision preserves effective edges", testCommunityParentRevision)
	t.Run("placement revalidates after a concurrent assignment refresh", testCommunityAssignmentRefresh)
	t.Run("rejected hierarchy placement is deferred", testCommunityHierarchyRejectionDeferred)
	t.Run("admitted decision outlives the preparation lease", testCommunityDecisionRenewsLease)
}

func TestCommunityBatchEligibilityAndPerItemFences(t *testing.T) {
	ctx := t.Context()
	f := newOrganizationFixture(t)
	manager := f.actor(0, "manager")
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
	first, second, third, automatic := testTopic("batch-first"), testTopic("batch-second"), testTopic("batch-third"), testTopic("batch-automatic")
	publication, err := f.store.PublishManager(manager, f.team, testPublication("community-batch-manager", 0,
		contract.Change{Record: first}, contract.Change{Record: second}, contract.Change{Record: third}))
	require.NoError(t, err)
	_, err = f.store.PublishAutomatic(ctx, f.team, testPublication("community-batch-auto", publication.Revision, contract.Change{Record: automatic}))
	require.NoError(t, err)
	firstMember := f.organizationEvidence(t, 0, "First automatic Topic member.", nil)
	secondMember := f.organizationEvidence(t, 1, "Second automatic Topic member.", nil)
	publishMember := func(source contract.SourceHandle, text string) {
		t.Helper()
		input := assignment.Scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: uuid.NewString()}
		item := assignment.Publication{EvidenceID: source.ID, Outcome: assignment.Outcome{Text: text, Method: "decision", InputHash: testHash(text), Matches: []assignment.Match{{ID: automatic.ID, Version: 1}}}}
		require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
			return f.store.PublishAssignmentsTx(ctx, tx, input, []assignment.Publication{item})
		}))
	}
	publishMember(firstMember, "First automatic Topic member.")
	status, err := f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 3, status.Communities.Pending)

	turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), 3*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	inputs, claim, err := f.store.ClaimCommunities(ctx, *turn, window.ID, time.Now())
	require.NoError(t, err)
	require.NotNil(t, claim)
	require.Len(t, inputs, 3)
	var leased int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_maintenance_community_work WHERE lease_batch_id=?::uuid`, claim.ID).Row().Scan(&leased))
	require.Equal(t, 3, leased)
	second.Definition.Description = "Edited during the placement assessment."
	revalidationPublish(t, f, contract.Change{Record: second, ExpectedVersion: 1})
	stale, committed := 0, 0
	for _, input := range inputs {
		err := f.store.CompleteCommunityItem(ctx, *claim, input, contract.CommunityOutcome{InputHash: testHash(input.Topic.ID), Method: "decision", Status: "standalone"}, "", time.Now())
		if input.Topic.ID == second.ID {
			require.ErrorIs(t, err, contract.ErrSourceStale)
			stale++
		} else {
			require.NoError(t, err)
			committed++
		}
	}
	require.Equal(t, 1, stale)
	require.Equal(t, 2, committed)
	require.NoError(t, f.store.CompleteMaintenanceBatch(ctx, *claim, contract.OrganizationResult{}, "stale_input", time.Now()))
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_maintenance_community_work WHERE lease_batch_id=?::uuid`, claim.ID).Row().Scan(&leased))
	require.Zero(t, leased)
	status, err = f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 2, status.Communities.Standalone)
	require.EqualValues(t, 1, status.Communities.Pending)
	publishMember(secondMember, "Second automatic Topic member.")
	status, err = f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 2, status.Communities.Pending, "a second active member makes the automatic Topic eligible")
	turn, err = f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), 3*time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	inputs, expired, err := f.store.ClaimCommunities(ctx, *turn, window.ID, time.Now())
	require.NoError(t, err)
	require.NotNil(t, expired)
	require.True(t, slices.ContainsFunc(inputs, func(input contract.CommunityInput) bool { return input.Topic.ID == automatic.ID }))
	require.NoError(t, f.store.withMaintenanceSystem(ctx, func(tx *gorm.DB) error {
		_, err := expireMaintenanceBatches(tx, time.Now().Add(4*time.Minute))
		return err
	}))
	require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_maintenance_community_work WHERE lease_batch_id=?::uuid`, expired.ID).Row().Scan(&leased))
	require.Zero(t, leased, "expired community leases must be released")
}

type communityDecisionSettings struct{}

func (communityDecisionSettings) DecisionRuntimeConfig(context.Context) (domain.DecisionRuntimeConfig, error) {
	return domain.DecisionRuntimeConfig{Revision: "lease-regression", Effective: domain.DecisionTasks{CommunityPlacement: true}}, nil
}

func testCommunityDecisionRenewsLease(t *testing.T) {
	ctx := t.Context()
	f := newOntologyFixture(t)
	manager := f.actor(0, "manager")
	child, parent := testTopic("postgresql-backup"), testTopic("postgresql")
	child.ID, parent.ID = "10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002"
	child.Definition.Label, parent.Definition.Label = "PostgreSQL backup", "PostgreSQL"
	_, err := f.store.PublishManager(manager, f.team, testPublication("community-decision-lease", 0,
		contract.Change{Record: child}, contract.Change{Record: parent}))
	require.NoError(t, err)

	const providerTimeout = time.Second
	const decisionTimeout = 30 * time.Second
	const preparationLease = 3*providerTimeout + 15*time.Second
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Input     string                           `json:"input"`
			Questions []modelprovider.DecisionQuestion `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var groups map[string]contract.CommunityInput
		if err := json.Unmarshal([]byte(request.Input), &groups); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		timer := time.NewTimer(preparationLease + time.Second)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return
		case <-timer.C:
		}
		w.Header().Set("Content-Type", "application/json")
		answers := make([]map[string]any, 0, len(request.Questions))
		for _, question := range request.Questions {
			group := groups["community_"+strings.TrimPrefix(question.Name, "parent_")]
			selected := "standalone"
			if group.Topic.ID == child.ID {
				selected = parent.ID
			}
			probabilities := make([]map[string]any, 0, len(question.Choices))
			for _, option := range question.Choices {
				probability := 0
				if option.Value == selected {
					probability = 1
				}
				probabilities = append(probabilities, map[string]any{"value": option.Value, "probability": probability})
			}
			answers = append(answers, map[string]any{"name": question.Name, "type": "choice", "choice": selected, "confidence": 1, "probabilities": probabilities})
		}
		if err := json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "answers": answers, "usage": map[string]int{"input_tokens": 8, "output_tokens": 2, "total_tokens": 10}}); err != nil && r.Context().Err() == nil {
			t.Error(err)
		}
	}))
	defer upstream.Close()
	limits := maintenanceFixtureLimits()
	protector := observability.NewCredentialProtector("decision-lease-fixture")
	logger := observability.NewConsoleWithProtector(slog.LevelDebug, protector, io.Discard)
	provider, err := decisionprovider.New(config.DecisionProviderConfig{
		Provider: "openai", APIURL: upstream.URL, APIKey: "decision-lease-fixture", Model: "fixture",
		MaxInputTokens: 64000, MaxQuestionInputTokens: 64000, TimeoutSeconds: int(decisionTimeout.Seconds()), MaxConcurrency: 1,
	}, limits.Tokenizer, upstream.Client(), logger, protector, nil)
	require.NoError(t, err)
	service := topics.NewMaintenanceService(topics.MaintenanceDependencies{
		Repository: f.store, Config: maintenanceSettings(t, f), Communities: f.store,
		ProviderTimeout: providerTimeout, DecisionTimeout: decisionTimeout,
		Placement: topics.CommunityAssessor{Decisions: provider, Settings: communityDecisionSettings{}, Limits: limits},
		Organizer: func(model string, capacity int64, _ assessment.AttemptAccounting) *topics.Service {
			return topics.NewService(nil, assessment.NewProvider(nil, model, limits, int(capacity)))
		},
	})
	progress, err := service.RunTurn(ctx)
	require.NoError(t, err)
	require.True(t, progress)
	view, err := f.store.GetRecord(manager, f.team, child.ID, 0)
	require.NoError(t, err)
	require.Equal(t, parent.ID, view.CommunityParentID)
	status, err := f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Communities.Placed)
	require.EqualValues(t, 1, status.Usage.Attempts)
	require.EqualValues(t, 10, status.Usage.ReportedTotalTokens)
	require.Zero(t, status.Usage.UnreconciledAttempts)
	require.Zero(t, status.ActiveWork)
}

type communityPlacementTransport func(context.Context, modelprovider.StructuredRequest) (modelprovider.StructuredResult, error)

func (transport communityPlacementTransport) Complete(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
	return transport(ctx, request)
}

func testCommunityHierarchyRejectionDeferred(t *testing.T) {
	ctx := t.Context()
	f := newOntologyFixture(t)
	manager := f.actor(0, "manager")
	parent, child := testTopic("database"), testTopic("database-backup")
	parent.ID, child.ID = "10000000-0000-4000-8000-000000000001", "10000000-0000-4000-8000-000000000002"
	parent.Definition.Label, child.Definition.Label = "Database", "Database backup"
	child.Definition.ParentID = parent.ID
	_, err := f.store.PublishManager(manager, f.team, testPublication("hierarchy-rejection-seed", 0,
		contract.Change{Record: parent}, contract.Change{Record: child}))
	require.NoError(t, err)

	var calls atomic.Int32
	transport := communityPlacementTransport(func(ctx context.Context, request modelprovider.StructuredRequest) (modelprovider.StructuredResult, error) {
		if err := modelprovider.NotifyAdmission(ctx); err != nil {
			return modelprovider.StructuredResult{}, err
		}
		if request.SchemaName != "community_placement_v1" {
			return modelprovider.StructuredResult{}, fmt.Errorf("unexpected schema %s", request.SchemaName)
		}
		var input contract.CommunityInput
		if err := json.Unmarshal([]byte(request.Messages[1].Content), &input); err != nil {
			return modelprovider.StructuredResult{}, err
		}
		selected := parent.ID
		if input.Topic.ID == parent.ID {
			selected = child.ID
		}
		calls.Add(1)
		return modelprovider.StructuredResult{Content: `{"parent_id":"` + selected + `"}`}, nil
	})
	limits := maintenanceFixtureLimits()
	service := topics.NewMaintenanceService(topics.MaintenanceDependencies{
		Repository: f.store, Config: maintenanceSettings(t, f), Communities: f.store,
		Placement: topics.CommunityAssessor{Transport: transport, Limits: limits},
		Organizer: func(model string, capacity int64, _ assessment.AttemptAccounting) *topics.Service {
			return topics.NewService(nil, assessment.NewProvider(nil, model, limits, int(capacity)))
		},
	})
	progress, err := service.RunTurn(ctx)
	require.True(t, progress)
	require.ErrorIs(t, err, contract.ErrConflict)
	require.EqualValues(t, 2, calls.Load())
	var disposition, failure string
	var deferred bool
	require.NoError(t, f.admin.Raw(`SELECT outcome.status,batch.failure_code,work.ready_at>statement_timestamp()
		FROM community_placement_outcomes AS outcome
		JOIN topic_maintenance_batches AS batch ON batch.batch_id=outcome.operation_id
		JOIN topic_maintenance_community_work AS work
		  ON (work.team_id,work.shared_space_id,work.space_generation,work.topic_id)=(outcome.team_id,outcome.shared_space_id,outcome.space_generation,outcome.topic_id)
		WHERE work.team_id=?::uuid AND work.topic_id=?::uuid`, f.team, parent.ID).
		Row().Scan(&disposition, &failure, &deferred))
	require.Equal(t, "failed", disposition)
	require.Equal(t, "conflict", failure)
	require.True(t, deferred)
	failed, err := f.store.GetRecord(f.actor(1, "member"), f.team, parent.ID, 0)
	require.NoError(t, err)
	require.Equal(t, "failed", failed.CommunityPlacement)
	require.Empty(t, failed.Definition.ParentID)

	view, err := f.store.GetRecord(manager, f.team, child.ID, 0)
	require.NoError(t, err)
	require.Equal(t, parent.ID, view.CommunityParentID)
	require.Equal(t, parent.ID, view.Definition.ParentID)
	status, err := f.store.MaintenanceStatus(ctx, time.Now())
	require.NoError(t, err)
	require.EqualValues(t, 1, status.Communities.Failed)
	require.Zero(t, status.ActiveWork)
	require.EqualValues(t, 2, status.Usage.Attempts)
	require.Zero(t, status.Usage.UnreconciledAttempts)

	failed.Definition.Description = "Updated database vocabulary"
	_, err = managementService(f).SaveEdits(manager, contract.SaveEditsInput{
		OperationKey: "revise-after-placement-failure", Reason: "Update database vocabulary",
		Edits: []contract.ManagementEdit{{Action: "define", RecordID: parent.ID, Kind: contract.Topic, ExpectedVersion: failed.EditVersion, Definition: failed.Definition}},
	})
	require.NoError(t, err)
	current, err := f.store.GetRecord(manager, f.team, parent.ID, 0)
	require.NoError(t, err)
	require.Empty(t, current.CommunityPlacement, "the failure belongs to the previous Topic revision")
}

func testCommunityMixedHierarchyPublicationRollback(t *testing.T) {
	ctx := t.Context()
	f := newOntologyFixture(t)
	manager := f.actor(0, "manager")
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
	placed, placedParent, cycleChild := testTopic("postgresql"), testTopic("database-storage"), testTopic("infrastructure")
	placed.ID = "00000000-0000-4000-8000-000000000001"
	placedParent.Definition.Description = "PostgreSQL database storage"
	placedParent.Definition.ParentID = cycleChild.ID
	legalChild, legalParent := testTopic("log-retention"), testTopic("archiving")
	seed, err := f.store.PublishManager(manager, f.team, testPublication("mixed-hierarchy-seed", 0,
		contract.Change{Record: placed}, contract.Change{Record: cycleChild}, contract.Change{Record: placedParent},
		contract.Change{Record: legalChild}, contract.Change{Record: legalParent}))
	require.NoError(t, err)

	turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	input, batch, err := f.store.ClaimCommunity(ctx, *turn, window.ID, time.Now())
	require.NoError(t, err)
	require.NotNil(t, input)
	require.NotNil(t, batch)
	require.Equal(t, placed.ID, input.Topic.ID)
	require.Len(t, input.Candidates, 1)
	require.Equal(t, placedParent.ID, input.Candidates[0].ID)
	require.NoError(t, f.store.CompleteCommunity(ctx, *batch, *input, contract.CommunityOutcome{
		InputHash: testHash("mixed-hierarchy-placement"), Method: "generation", Status: "placed", ParentID: placedParent.ID,
	}, "", time.Now()))
	placedView, err := f.store.GetRecord(manager, f.team, placed.ID, 0)
	require.NoError(t, err)
	require.Equal(t, placedParent.ID, placedView.CommunityParentID)
	require.Equal(t, placedParent.ID, placedView.Definition.ParentID)
	original, err := f.store.GetRecord(manager, f.team, placed.ID, 1)
	require.NoError(t, err)
	require.Empty(t, original.Definition.ParentID)

	before, err := f.store.ListRecords(manager, f.team, contract.Topic, "", 10)
	require.NoError(t, err)
	legalChild.Definition.ParentID = legalParent.ID
	cycleChild.Definition.ParentID = placed.ID
	legal := contract.Change{Record: legalChild, ExpectedVersion: 1}
	cycle := contract.Change{Record: cycleChild, ExpectedVersion: 1}
	const operation = "edit-mixed-hierarchy"
	_, err = f.store.PublishManager(manager, f.team, testPublication(operation, seed.Revision, legal, cycle))
	require.ErrorIs(t, err, contract.ErrConflict)
	var rejection *contract.PublicationRejection
	require.ErrorAs(t, err, &rejection)
	require.Equal(t, "community_hierarchy_conflict", rejection.Reason)
	require.Equal(t, cycleChild.ID, rejection.RecordID)
	require.Equal(t, placed.ID, rejection.ParentID)

	after, err := f.store.ListRecords(manager, f.team, contract.Topic, "", 10)
	require.NoError(t, err)
	require.Equal(t, before, after)
	accepted, err := f.store.PublishManager(manager, f.team, testPublication(operation, seed.Revision, legal))
	require.NoError(t, err)
	require.Greater(t, accepted.Revision, seed.Revision)
	require.Equal(t, []contract.RevisionRef{{ID: legalChild.ID, Version: 2}}, accepted.Records)
}

func testCommunityParentRevision(t *testing.T) {
	ctx := t.Context()
	f := newOntologyFixture(t)
	manager := f.actor(0, "manager")
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
	child, definitionParent, communityParent := testTopic("postgresql-backups"), testTopic("postgresql-reliability"), testTopic("postgresql-operations")
	child.ID = "10000000-0000-4000-8000-000000000001"
	definitionParent.ID = "10000000-0000-4000-8000-000000000002"
	communityParent.ID = "10000000-0000-4000-8000-000000000003"
	child.Definition.Label, definitionParent.Definition.Label, communityParent.Definition.Label = "PostgreSQL backups", "PostgreSQL reliability", "PostgreSQL operations"
	child.Definition.ParentID = definitionParent.ID
	seed, err := f.store.PublishManager(manager, f.team, testPublication("fallback-hierarchy-seed", 0,
		contract.Change{Record: child}, contract.Change{Record: definitionParent}, contract.Change{Record: communityParent}))
	require.NoError(t, err)

	place := func(childID, parentID string) {
		t.Helper()
		turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
		require.NoError(t, err)
		require.NotNil(t, turn)
		input, batch, err := f.store.ClaimCommunity(ctx, *turn, window.ID, time.Now())
		require.NoError(t, err)
		require.NotNil(t, input)
		require.NotNil(t, batch)
		require.Equal(t, childID, input.Topic.ID)
		require.NoError(t, f.store.CompleteCommunity(ctx, *batch, *input, contract.CommunityOutcome{
			InputHash: testHash(childID), Method: "generation", Status: "placed", ParentID: parentID,
		}, "", time.Now()))
	}
	place(child.ID, communityParent.ID)
	place(definitionParent.ID, child.ID)

	const operation = "edit-parent-description"
	communityParent.Definition.Description = "PostgreSQL operations and administration."
	result, err := f.store.PublishManager(manager, f.team, testPublication(operation, seed.Revision,
		contract.Change{Record: communityParent, ExpectedVersion: 1}))
	require.NoError(t, err)
	require.Greater(t, result.Revision, seed.Revision)
	view, err := f.store.GetRecord(manager, f.team, child.ID, 0)
	require.NoError(t, err)
	require.Equal(t, communityParent.ID, view.CommunityParentID)
	require.Equal(t, communityParent.ID, view.Definition.ParentID)
	view, err = f.store.GetRecord(manager, f.team, definitionParent.ID, 0)
	require.NoError(t, err)
	require.Equal(t, child.ID, view.Definition.ParentID)
}

func testCommunityAssignmentRefresh(t *testing.T) {
	for _, variant := range []struct {
		name           string
		declaredParent bool
		waitStatement  string
	}{
		{name: "restored declared parent", declaredParent: true, waitStatement: "%pg_advisory_xact_lock%"},
		{name: "root endpoint revision", waitStatement: "%FOR NO KEY UPDATE%"},
	} {
		t.Run(variant.name, func(t *testing.T) {
			f := newOntologyFixture(t)
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			window := maintenanceWindow(t, f, maintenanceSettings(t, f))
			oldSource := f.organizationEvidence(t, 0, "PostgreSQL backup guidance.", nil)
			const text = "PostgreSQL backup retention can be configured."
			newSource := f.organizationEvidence(t, 0, text, nil)
			parent, child := testTopic("postgresql-backup"), testTopic("postgresql")
			parent.ID = "10000000-0000-4000-8000-000000000001"
			child.ID = "10000000-0000-4000-8000-000000000002"
			parent.Definition.Label = "PostgreSQL backup"
			child.Definition.Label = "PostgreSQL"
			parent.Sources = []contract.SourceDependency{f.source(t, oldSource)}
			if variant.declaredParent {
				parent.Definition.ParentID = child.ID
			}
			_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("placement-refresh-fixture", 0,
				contract.Change{Record: parent}, contract.Change{Record: child}))
			require.NoError(t, err)

			claim := func(topicID string) (contract.CommunityInput, contract.MaintenanceClaim) {
				t.Helper()
				turn, err := f.store.ClaimMaintenanceTurn(ctx, window.ID, time.Now(), time.Minute)
				require.NoError(t, err)
				require.NotNil(t, turn)
				input, batch, err := f.store.ClaimCommunity(ctx, *turn, window.ID, time.Now())
				require.NoError(t, err)
				require.NotNil(t, input)
				require.NotNil(t, batch)
				require.Equal(t, topicID, input.Topic.ID)
				return *input, *batch
			}
			input, batch := claim(parent.ID)
			require.NoError(t, f.store.CompleteCommunity(ctx, batch, input, contract.CommunityOutcome{
				InputHash: testHash("standalone parent"), Method: "generation", Status: "standalone",
			}, "", time.Now()))
			input, batch = claim(child.ID)
			require.Len(t, input.Candidates, 1)
			require.Equal(t, parent.ID, input.Candidates[0].ID)

			require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
				return appendMaintenanceOccurrence(tx, f, oldSource)
			}))
			before, err := f.store.GetRecord(f.actor(0, "manager"), f.team, parent.ID, 0)
			require.NoError(t, err)
			require.Equal(t, "source_changed", before.StaleReason)
			require.Equal(t, "standalone", before.CommunityPlacement)

			assignmentScope := assignment.Scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: uuid.NewString()}
			item := assignment.Publication{EvidenceID: newSource.ID, Outcome: assignment.Outcome{
				Text: text, InputHash: testHash("refresh parent definition"), Method: "generation",
				Proposals: []assignment.Proposal{{Label: parent.Definition.Label, Quote: parent.Definition.Label, Description: "PostgreSQL backup guidance"}},
			}}
			publish := func(tx *gorm.DB) error {
				return f.store.PublishAssignmentsTx(ctx, tx, assignmentScope, []assignment.Publication{item})
			}
			if variant.declaredParent {
				require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
					fence := scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation}
					if err := lockOrganizationCatalog(tx, fence); err != nil {
						return err
					}
					require.ErrorIs(t, f.rls.WithSystemTx(ctx, f.app, publish), assignment.ErrStale)
					return nil
				}))
			}

			finished := make(chan error, 1)
			require.NoError(t, f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
				if err := publish(tx); err != nil {
					return err
				}
				var writerPID int
				if err := tx.Raw(`SELECT pg_backend_pid()`).Row().Scan(&writerPID); err != nil {
					return err
				}
				go func() {
					finished <- f.store.CompleteCommunity(ctx, batch, input, contract.CommunityOutcome{
						InputHash: testHash("place under refreshed parent"), Method: "generation", Status: "placed", ParentID: parent.ID,
					}, "", time.Now())
				}()
				require.Eventually(t, func() bool {
					var blocked bool
					err := f.admin.Raw(`SELECT EXISTS(SELECT 1 FROM pg_stat_activity
                        WHERE wait_event_type='Lock' AND query LIKE ? AND ?=ANY(pg_blocking_pids(pid)))`,
						variant.waitStatement, writerPID).Row().Scan(&blocked)
					return err == nil && blocked
				}, 5*time.Second, 20*time.Millisecond)
				return nil
			}))
			require.ErrorIs(t, <-finished, contract.ErrSourceStale)
			current, err := f.store.GetRecord(f.actor(0, "manager"), f.team, parent.ID, 0)
			require.NoError(t, err)
			require.EqualValues(t, 2, current.Version)
			require.Equal(t, parent.Definition.ParentID, current.Definition.ParentID)
			current, err = f.store.GetRecord(f.actor(0, "manager"), f.team, child.ID, 0)
			require.NoError(t, err)
			require.Empty(t, current.Definition.ParentID)
			var placementCount int
			require.NoError(t, f.admin.Raw(`SELECT count(*) FROM community_placement_outcomes WHERE operation_id=?::uuid`, batch.ID).Row().Scan(&placementCount))
			require.Zero(t, placementCount)
		})
	}
}

func TestConcurrentTopicProposalsReuseScopedName(t *testing.T) {
	ctx := t.Context()
	f := newOntologyFixture(t)
	const label = "PostgreSQL backups"
	propose := func(contents ...string) []string {
		t.Helper()
		evidenceIDs := make([]string, len(contents))
		for i, content := range contents {
			evidenceIDs[i] = f.organizationEvidence(t, i, content, nil).ID
		}
		start := make(chan struct{})
		result := make(chan error, len(contents))
		for i, content := range contents {
			go func() {
				<-start
				outcome := assignment.Outcome{
					Text: content, InputHash: "sha256:proposal", Method: "generation",
					Proposals: []assignment.Proposal{{Label: label, Description: "Backing up a PostgreSQL database", Quote: label}},
				}
				result <- f.rls.WithSystemTx(ctx, f.app, func(tx *gorm.DB) error {
					return f.store.PublishAssignmentsTx(ctx, tx, assignment.Scope{
						TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: uuid.NewString(),
					}, []assignment.Publication{{EvidenceID: evidenceIDs[i], Outcome: outcome}})
				})
			}()
		}
		close(start)
		for range contents {
			require.NoError(t, <-result)
		}
		return evidenceIDs
	}
	propose("PostgreSQL backups retain seven daily restore points.", "Test PostgreSQL backups after changing storage providers.")
	var topics, members, distinct int
	require.NoError(t, f.admin.Raw(`SELECT count(*) FILTER(WHERE kind='topic'),
		count(*) FILTER(WHERE kind='assignment'),count(DISTINCT topic_id)
		FROM topic_record_heads WHERE team_id=?::uuid AND NOT retired`, f.team).Row().Scan(&topics, &members, &distinct))
	require.Equal(t, 1, topics)
	require.Equal(t, 2, members)
	require.Equal(t, 1, distinct)

	manager := f.actor(0, "manager")
	before, err := f.store.ListRecords(manager, f.team, contract.Topic, "", 10)
	require.NoError(t, err)
	require.Len(t, before.Records, 1)
	original := before.Records[0]
	require.Equal(t, contract.OrganizationRecordID(f.team, contract.Topic, contract.NormalizeName(label)), original.ID)
	renamed := original.Record
	definition := *renamed.Definition
	definition.Key, definition.Label = "snapshot archives", "Snapshot archives"
	renamed.Definition = &definition
	_, err = f.store.PublishManager(manager, f.team, testPublication("rename-backup-topic", before.Revision,
		contract.Change{Record: renamed, ExpectedVersion: original.Version}))
	require.NoError(t, err)

	evidenceIDs := propose("PostgreSQL backups use archive compression.", "Verify PostgreSQL backups every month.")
	after, err := f.store.ListRecords(manager, f.team, contract.Topic, "", 10)
	require.NoError(t, err)
	require.Len(t, after.Records, 2)
	for _, record := range after.Records {
		if record.ID == original.ID {
			require.Equal(t, original.Version+1, record.Version)
			require.Equal(t, renamed.Definition, record.Definition)
			continue
		}
		require.Equal(t, label, record.Definition.Label)
		require.EqualValues(t, 1, record.Version)
		for _, evidenceID := range evidenceIDs {
			var topicID string
			require.NoError(t, f.admin.Raw(`SELECT topic_id::text FROM topic_record_heads
				WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?
				AND kind='assignment' AND NOT retired AND evidence_id=?::uuid`,
				f.team, f.space, f.generation, evidenceID).Row().Scan(&topicID))
			require.Equal(t, record.ID, topicID)
		}
	}
	historical, err := f.store.GetRecord(manager, f.team, original.ID, original.Version)
	require.NoError(t, err)
	require.Equal(t, original.Record, historical.Record)
}
