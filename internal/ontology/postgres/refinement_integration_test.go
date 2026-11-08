//go:build integration

package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type refinementFixture struct {
	f                         *ontologyFixture
	source                    ontology.SourceHandle
	parent, child, assignment ontology.Record
}

func newRefinementFixture(t *testing.T, predicate, legacy bool) refinementFixture {
	t.Helper()
	f := newOrganizationFixture(t)
	x := refinementFixture{f: f}
	kind, key, base := ontology.EntityClass, "project", "project"
	if predicate {
		relationship := maintenanceRelationship(t, f)
		sources, err := f.store.ReadSources(context.Background(), f.team, []ontology.SourceHandle{relationship})
		require.NoError(t, err)
		version, err := strconv.ParseInt(sources[0].State["predicate_version"], 10, 64)
		require.NoError(t, err)
		x.source = ontology.SourceHandle{Kind: ontology.PredicateSource, ID: sources[0].State["predicate"], Version: version}
		kind, key, base = ontology.PredicateConcept, "uses", ""
	} else {
		x.source = maintenanceEntity(t, f, "Atlas explicitly identified as a software project")
	}
	x.parent = ontology.Record{ID: uuid.NewString(), Kind: kind, Definition: &ontology.Definition{Key: key, Label: key, BaseEntityKind: base}}
	x.child = ontology.Record{ID: uuid.NewString(), Kind: kind, Definition: &ontology.Definition{Key: "specific-" + key, Label: "Specific " + key, Description: "Explicit " + key + " source context", Aliases: []string{"refined " + key}, BaseEntityKind: base}}
	definitionID := x.parent.ID
	if legacy {
		x.child.Definition.ParentID = x.parent.ID
		definitionID = x.child.ID
	}
	x.assignment = ontology.Record{ID: uuid.NewString(), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: x.source, DefinitionID: definitionID}, Sources: []ontology.SourceDependency{f.source(t, x.source)}}
	revalidationPublish(t, f, ontology.Change{Record: x.parent}, ontology.Change{Record: x.child}, ontology.Change{Record: x.assignment})
	return x
}

func (x refinementFixture) propose(request assessment.Request, response *assessment.Response) {
	parent, child := "", ""
	for _, definition := range request.Definitions {
		if definition.Key == x.parent.Definition.Key {
			parent = definition.Ref
		}
		if definition.Key == x.child.Definition.Key {
			child = definition.Ref
		}
	}
	for i, item := range request.Items {
		if item.Kind != x.source.Kind {
			continue
		}
		response.Items[i] = assessment.Decision{Ref: item.Ref, Status: "classified", DefinitionRef: child, Reason: "The explicit supplied context justifies this more specific meaning."}
	}
	relation := ontology.SubclassOf
	if x.source.Kind == ontology.PredicateSource {
		relation = ontology.SubpropertyOf
	}
	response.HierarchyUpdates = []assessment.HierarchyUpdate{{DefinitionRef: child, ParentRef: parent, ParentRelation: relation, Reason: "The supplied meaning is a specialization of the preferred definition."}}
}

func TestOntologyAutomaticRefinement(t *testing.T) {
	for _, predicate := range []bool{false, true} {
		t.Run(strconv.FormatBool(predicate), func(t *testing.T) {
			x := newRefinementFixture(t, predicate, false)
			f := x.f
			before := f.canonicalSnapshot(t)
			service, calls := organizationFixtureService(t, f, nil, x.propose)
			input := ontology.OrganizationInput{OperationKey: "refine", Sources: []ontology.SourceHandle{x.source}}
			result, err := service.Organize(context.Background(), f.team, input)
			require.NoError(t, err)
			require.True(t, result.Current)
			require.EqualValues(t, 1, calls.Load())
			child, err := f.store.GetRecord(f.actor(1, "member"), f.team, x.child.ID, 0)
			require.NoError(t, err)
			require.True(t, child.Current)
			require.EqualValues(t, 2, child.Version)
			require.Equal(t, x.parent.ID, child.Definition.ParentID)
			require.NotEmpty(t, child.Definition.ParentRelation)
			require.Equal(t, x.child.Definition.Description, child.Definition.Description)
			require.Equal(t, x.child.Definition.Aliases, child.Definition.Aliases)
			assignment, err := f.store.GetRecord(f.actor(1, "member"), f.team, x.assignment.ID, 0)
			require.NoError(t, err)
			require.True(t, assignment.Current)
			require.Equal(t, x.child.ID, assignment.Assignment.DefinitionID)
			require.Contains(t, assignment.Dependencies, ontology.RevisionRef{ID: child.ID, Version: child.Version})
			old, err := f.store.GetRecord(context.Background(), f.team, x.child.ID, 1)
			require.NoError(t, err)
			require.Empty(t, old.Definition.ParentRelation)
			f.store = NewStore(f.app, f.rls)
			restarted, restartedCalls := organizationFixtureService(t, f, nil, x.propose)
			for _, key := range []string{"refine", "same-v2-work-after-restart"} {
				input.OperationKey = key
				replay, err := restarted.Organize(context.Background(), f.team, input)
				require.NoError(t, err)
				require.True(t, replay.Existing)
				require.True(t, replay.Current)
				require.Equal(t, result.AssessmentID, replay.AssessmentID)
			}
			require.Zero(t, restartedCalls.Load())
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
}

func TestOntologyRefinementPreservesLegacyAndAmbiguity(t *testing.T) {
	for _, mode := range []string{"reuse", "ambiguous", "pinned", "manager_lock", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			x := newRefinementFixture(t, false, true)
			f := x.f
			if mode == "pinned" {
				revalidationPublish(t, f, ontology.Change{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.PinDefinition, TargetID: x.child.ID}}})
			}
			if mode == "manager_lock" {
				revalidationPublish(t, f, ontology.Change{Record: ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, Members: []ontology.SourceHandle{x.source}, DefinitionID: x.parent.ID}, Sources: []ontology.SourceDependency{f.source(t, x.source)}}})
			}
			before := f.canonicalSnapshot(t)
			service, calls := organizationFixtureService(t, f, nil, func(q assessment.Request, r *assessment.Response) {
				switch mode {
				case "ambiguous":
					ambiguousClassification(q, r)
				case "pinned", "cycle":
					x.propose(q, r)
					if mode == "cycle" {
						r.HierarchyUpdates[0].ParentRef = r.HierarchyUpdates[0].DefinitionRef
					}
				}
			})
			result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: mode, Sources: []ontology.SourceHandle{x.source}})
			if mode == "pinned" || mode == "cycle" {
				require.Error(t, err)
				require.Equal(t, "provider_response_invalid", result.FailureCode)
				require.Nil(t, result.Publication)
				require.EqualValues(t, 3, calls.Load())
			} else {
				require.NoError(t, err)
			}
			child, err := f.store.GetRecord(f.actor(1, "member"), f.team, x.child.ID, 0)
			require.NoError(t, err)
			require.EqualValues(t, 1, child.Version)
			require.Equal(t, x.parent.ID, child.Definition.ParentID)
			require.Empty(t, child.Definition.ParentRelation)
			assignment, err := f.store.GetRecord(context.Background(), f.team, x.assignment.ID, 0)
			require.NoError(t, err)
			require.True(t, assignment.Current)
			if mode == "ambiguous" {
				require.Equal(t, "ambiguous", result.Outcomes[0].Status)
				require.Contains(t, result.Outcomes[0].RecordIDs, assignment.ID)
			}
			if mode == "manager_lock" {
				require.Zero(t, calls.Load())
				require.Equal(t, x.parent.ID, assignment.Assignment.DefinitionID)
			} else {
				require.Equal(t, x.child.ID, assignment.Assignment.DefinitionID)
			}
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
}

func TestOntologyRefinementPublicationRaces(t *testing.T) {
	for _, race := range []string{"pin", "definition", "source", "override"} {
		t.Run(race, func(t *testing.T) {
			x := newRefinementFixture(t, false, false)
			f := x.f
			var raced bool
			var mutationErr error
			service, calls := organizationFixtureService(t, f, nil, func(q assessment.Request, r *assessment.Response) {
				x.propose(q, r)
				if raced {
					return
				}
				raced = true
				if race == "source" {
					_, mutationErr = f.knowledge.AddEntityName(context.Background(), knowledge.AddEntityNameInput{TeamID: f.team, OwnerProfileID: f.owners[0], EntityID: x.source.ID, DisplayName: "new relevant alias", NameKind: "alias"})
					return
				}
				record := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.PinDefinition, TargetID: x.child.ID}}
				expected := int64(0)
				if race == "definition" {
					record = x.child
					definition := *x.child.Definition
					definition.Description = "Manager corrected the assessed meaning"
					record.Definition = &definition
					expected = 1
				}
				if race == "override" {
					record.Override = &ontology.Override{Action: ontology.SetClassification, Members: []ontology.SourceHandle{x.source}, DefinitionID: x.parent.ID}
					record.Sources = []ontology.SourceDependency{f.source(t, x.source)}
				}
				_, mutationErr = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("racing-manager", 1, ontology.Change{ExpectedVersion: expected, Record: record}))
			})
			result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: race, Sources: []ontology.SourceHandle{x.source}})
			require.NoError(t, mutationErr)
			require.ErrorIs(t, err, ontology.ErrSourceStale)
			require.Equal(t, "stale_input", result.FailureCode)
			require.NotNil(t, result.FreshnessRejection)
			require.Nil(t, result.Publication)
			require.EqualValues(t, 1, calls.Load())
			child, err := f.store.GetRecord(context.Background(), f.team, x.child.ID, 0)
			require.NoError(t, err)
			require.Empty(t, child.Definition.ParentID)
			require.Empty(t, child.Definition.ParentRelation)
			assignment, err := f.store.GetRecord(context.Background(), f.team, x.assignment.ID, 0)
			require.NoError(t, err)
			require.EqualValues(t, 1, assignment.Version)
			require.Equal(t, x.parent.ID, assignment.Assignment.DefinitionID)
		})
	}
}

func TestOntologyRefinementLeaseRecovery(t *testing.T) {
	x := newRefinementFixture(t, false, false)
	f := x.f
	window := maintenanceWindow(t, f, maintenanceSettings(t, f))
	_, claim := maintenanceClaim(t, f, window)
	before := f.canonicalSnapshot(t)
	var mutationErr error
	service, calls := organizationFixtureServiceWithAccounting(t, f, nil, func(q assessment.Request, r *assessment.Response) {
		x.propose(q, r)
		mutationErr = f.admin.Exec(`UPDATE ontology_maintenance_batches SET lease_until=clock_timestamp()-interval '1 second' WHERE batch_id=?::uuid`, claim.ID).Error
	}, "fixture-model", assessor.DefaultSemanticAssessmentLimits(), finalizationAccounting{f.store, *claim}, 300000)
	result, err := service.Organize(ontology.WithMaintenanceClaim(context.Background(), *claim), f.team, ontology.OrganizationInput{OperationKey: "expired-refinement", Sources: claim.Sources})
	require.NoError(t, mutationErr)
	require.ErrorIs(t, err, ontology.ErrLeaseLost)
	require.Equal(t, "lease_lost", result.FailureCode)
	require.Nil(t, result.Publication)
	require.EqualValues(t, 1, calls.Load())
	f.store = NewStore(f.app, f.rls)
	turn, err := f.store.ClaimMaintenanceTurn(context.Background(), window.ID, time.Now().UTC(), time.Minute)
	require.NoError(t, err)
	require.NotNil(t, turn)
	require.NoError(t, f.store.ReleaseMaintenanceTurn(context.Background(), *turn))
	child, err := f.store.GetRecord(context.Background(), f.team, x.child.ID, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, child.Version)
	require.Empty(t, child.Definition.ParentID)
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyRefinementIsolationAndAncestryBound(t *testing.T) {
	x := newRefinementFixture(t, false, false)
	f := x.f
	managementOwnerC(t, f)
	foreign := maintenanceOtherTeam(t, f)
	before := f.canonicalSnapshot(t)
	service, calls := organizationFixtureService(t, f, nil, x.propose)
	for _, owner := range []int{0, 1, 2} {
		view, err := f.store.GetRecord(f.actor(owner, "member"), f.team, x.assignment.ID, 0)
		require.NoError(t, err)
		require.True(t, view.Current)
		_, err = service.Organize(f.actor(owner, "manager"), f.team, ontology.OrganizationInput{OperationKey: uuid.NewString(), Sources: []ontology.SourceHandle{x.source}})
		require.ErrorIs(t, err, ontology.ErrUnauthorized)
	}
	result, err := service.Organize(context.Background(), foreign.team, ontology.OrganizationInput{OperationKey: "foreign-refinement", Sources: []ontology.SourceHandle{x.source}})
	require.NoError(t, err)
	require.Equal(t, "unavailable", result.Outcomes[0].Status)
	require.Zero(t, calls.Load())
	_, err = f.store.GetRecord(foreign.actor(0, "manager"), foreign.team, x.child.ID, 0)
	require.ErrorIs(t, err, ontology.ErrNotFound)
	require.Equal(t, before, f.canonicalSnapshot(t))
	parentID := x.parent.ID
	for index := 0; index < ontology.MaxVocabularyCandidates; index++ {
		definition := ontology.Record{ID: uuid.NewString(), Kind: ontology.EntityClass, Definition: &ontology.Definition{Key: fmt.Sprintf("bounded-project-%02d", index), Label: fmt.Sprintf("Bounded project %02d", index), BaseEntityKind: "project", ParentID: parentID, ParentRelation: ontology.SubclassOf}}
		revalidationPublish(t, f, ontology.Change{Record: definition})
		parentID = definition.ID
	}
	assignment := x.assignment
	assignment.Assignment = &ontology.Assignment{Source: x.source, DefinitionID: parentID}
	revalidationPublish(t, f, ontology.Change{ExpectedVersion: 1, Record: assignment})
	result, err = service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "bounded-refinement", Sources: []ontology.SourceHandle{x.source}})
	require.ErrorIs(t, err, ontology.ErrContextBound)
	require.Equal(t, "organization_context_bound", result.FailureCode)
	require.Nil(t, result.Publication)
	require.Zero(t, calls.Load())
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyRefinementKeepsHistoricalReceipts(t *testing.T) {
	x := newRefinementFixture(t, false, false)
	f := x.f
	ctx := context.Background()
	contextData, err := f.store.ReadOrganization(ctx, f.team, []ontology.SourceHandle{x.source})
	require.NoError(t, err)
	contextHash, err := ontology.OrganizationContextHash(contextData)
	require.NoError(t, err)
	dependencies, err := ontology.OrganizationDependencies(contextData)
	require.NoError(t, err)
	id := uuid.NewString()
	legacy := ontology.OrganizationReceipt{ID: id, OperationKey: "historical-v1", ProviderIdentity: testHash("ontology_organization_v1"), InputHash: testHash("ontology-organization-v1-input"), BatchHash: testHash("ontology-organization-v1-batch"), ContextHash: contextHash, Dependencies: dependencies,
		Sources: []ontology.SourceDependency{f.source(t, x.source)}, Result: ontology.OrganizationResult{AssessmentID: id, Current: true, Outcomes: []ontology.OrganizationOutcome{{Source: x.source, Status: "unchanged", RecordIDs: []string{x.assignment.ID}}}, Attempts: []ontology.AssessmentAttempt{}}}
	require.NoError(t, f.store.withScope(ctx, f.team, false, func(tx *gorm.DB, fence scope) error { return insertOrganization(tx, fence, legacy) }))
	service, calls := organizationFixtureService(t, f, nil, nil)
	result, err := service.Organize(ctx, f.team, ontology.OrganizationInput{OperationKey: "current-v2", Sources: []ontology.SourceHandle{x.source}})
	require.NoError(t, err)
	require.NotEqual(t, legacy.ID, result.AssessmentID)
	require.EqualValues(t, 1, calls.Load())
	require.NoError(t, f.store.withScope(ctx, f.team, true, func(tx *gorm.DB, fence scope) error {
		stored, found, err := organizationByKey(tx, fence, legacy.OperationKey)
		require.True(t, found)
		require.Equal(t, legacy, stored)
		return err
	}))
}

func TestOntologyRefinementRebuildsStoredAncestry(t *testing.T) {
	for _, predicate := range []bool{false, true} {
		for _, semantic := range []bool{false, true} {
			t.Run(fmt.Sprintf("predicate=%t/semantic=%t", predicate, semantic), func(t *testing.T) {
				f := newOrganizationFixture(t)
				kind, base, relation := ontology.EntityClass, "project", ontology.SubclassOf
				var source ontology.SourceHandle
				if predicate {
					relationship := maintenanceRelationship(t, f)
					sources, err := f.store.ReadSources(context.Background(), f.team, []ontology.SourceHandle{relationship})
					require.NoError(t, err)
					version, err := strconv.ParseInt(sources[0].State["predicate_version"], 10, 64)
					require.NoError(t, err)
					source = ontology.SourceHandle{Kind: ontology.PredicateSource, ID: sources[0].State["predicate"], Version: version}
					kind, base, relation = ontology.PredicateConcept, "", ontology.SubpropertyOf
				} else {
					source = maintenanceEntity(t, f, "Explicit software project")
				}
				if !semantic {
					relation = ""
				}
				parent := ontology.Record{ID: uuid.NewString(), Kind: kind, Definition: &ontology.Definition{Key: "opaqueancestor", Label: "Opaque ancestor", BaseEntityKind: base}}
				support := f.organizationEvidence(t, 0, "Historical definition support", nil)
				child := ontology.Record{ID: uuid.NewString(), Kind: kind, Definition: &ontology.Definition{Key: "reconstructedmeaning", Label: "Reconstructed meaning", BaseEntityKind: base, ParentID: parent.ID, ParentRelation: relation}, Sources: []ontology.SourceDependency{f.source(t, support)}}
				revalidationPublish(t, f, ontology.Change{Record: parent}, ontology.Change{Record: child})
				_, err := f.knowledge.RetractEvidence(f.actor(0, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[0], EvidenceIDs: []string{support.ID}, Reason: "withdraw stale definition support", IdempotencyKey: uuid.NewString(), RequestHash: testHash("stale definition support")})
				require.NoError(t, err)
				contextData, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{source})
				require.NoError(t, err)
				require.Empty(t, contextData.Candidates, "stored ancestry must not depend on lexical provider context")
				before := f.canonicalSnapshot(t)
				service, calls := organizationFixtureService(t, f, nil, func(_ assessment.Request, response *assessment.Response) {
					response.Definitions[0].Key, response.Definitions[0].Label = child.Definition.Key, child.Definition.Label
				})
				input := ontology.OrganizationInput{OperationKey: "rebuild-stored-ancestry", Sources: []ontology.SourceHandle{source}}
				result, err := service.Organize(context.Background(), f.team, input)
				require.NoError(t, err)
				require.True(t, result.Current)
				require.NotNil(t, result.Publication)
				require.Len(t, result.Publication.Records, 2)
				rebuilt, err := f.store.GetRecord(context.Background(), f.team, child.ID, 0)
				require.NoError(t, err)
				require.True(t, rebuilt.Current)
				require.EqualValues(t, 2, rebuilt.Version)
				require.Equal(t, parent.ID, rebuilt.Definition.ParentID)
				require.Equal(t, relation, rebuilt.Definition.ParentRelation)
				assignments, err := f.store.ListRecords(context.Background(), f.team, ontology.AssignmentKind, "", 20)
				require.NoError(t, err)
				require.Len(t, assignments.Records, 1)
				assignment := assignments.Records[0]
				require.True(t, assignment.Current)
				require.Contains(t, assignment.Dependencies, ontology.RevisionRef{ID: child.ID, Version: 2})
				require.Contains(t, assignment.Dependencies, ontology.RevisionRef{ID: parent.ID, Version: 1})
				input.OperationKey = "reuse-rebuilt-stored-ancestry"
				replay, err := service.Organize(context.Background(), f.team, input)
				require.NoError(t, err)
				require.True(t, replay.Existing)
				require.Equal(t, result.AssessmentID, replay.AssessmentID)
				require.EqualValues(t, 1, calls.Load())
				require.Equal(t, before, f.canonicalSnapshot(t))
			})
		}
	}
}

func TestOntologyRefinementCoarseAncestryBound(t *testing.T) {
	f := newOrganizationFixture(t)
	source := maintenanceEntity(t, f, "Atlas")
	parentID := ""
	var changes []ontology.Change
	for index := 0; index <= ontology.MaxVocabularyCandidates; index++ {
		key := fmt.Sprintf("opaqueancestor-%02d", index)
		if index == ontology.MaxVocabularyCandidates {
			key = "project"
		}
		definition := ontology.Record{ID: uuid.NewString(), Kind: ontology.EntityClass, Definition: &ontology.Definition{Key: key, Label: key, BaseEntityKind: "project", ParentID: parentID}}
		parentID = definition.ID
		changes = append(changes, ontology.Change{Record: definition})
	}
	revalidationPublish(t, f, changes...)
	service, calls := organizationFixtureService(t, f, nil, nil)
	result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "coarse-ancestry-bound", Sources: []ontology.SourceHandle{source}})
	require.ErrorIs(t, err, ontology.ErrContextBound)
	require.Equal(t, "organization_context_bound", result.FailureCode)
	require.Nil(t, result.Publication)
	require.Zero(t, calls.Load())
}

func TestOntologyRefinementDefinitionAliasRace(t *testing.T) {
	x := newRefinementFixture(t, false, false)
	f := x.f
	before := f.canonicalSnapshot(t)
	var mutationErr error
	var observations []assessment.Diagnostic
	service, calls := organizationFixtureService(t, f, nil, func(q assessment.Request, r *assessment.Response) {
		proposed := assessment.Definition{Ref: "proposed-refinement", Kind: ontology.EntityClass, Key: "explicit-software-project", Label: "Explicit software project", Aliases: []string{}, BaseEntityKind: "project"}
		r.Definitions = []assessment.Definition{proposed}
		r.Items[0] = assessment.Decision{Ref: q.Items[0].Ref, Status: "classified", DefinitionRef: proposed.Ref, Reason: "Explicit supplied software project context."}
		changed := x.child
		definition := *x.child.Definition
		definition.Aliases = append(append([]string(nil), definition.Aliases...), proposed.Key)
		changed.Definition = &definition
		_, mutationErr = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("alias-during-provider-work", 1, ontology.Change{ExpectedVersion: 1, Record: changed}))
	})
	service.WithDiagnostics(func(_ context.Context, diagnostic assessment.Diagnostic) {
		observations = append(observations, diagnostic)
	})
	result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "definition-alias-race", Sources: []ontology.SourceHandle{x.source}})
	require.NoError(t, mutationErr)
	require.ErrorIs(t, err, ontology.ErrSourceStale)
	require.Equal(t, "stale_input", result.FailureCode)
	require.Nil(t, result.Publication)
	require.Equal(t, &ontology.FreshnessRejection{Check: "dependency_version", RecordID: x.child.ID, Expected: "1", Observed: "2"}, result.FreshnessRejection)
	require.True(t, result.FreshnessRejection.Valid())
	require.EqualValues(t, 1, calls.Load())
	require.NotEmpty(t, observations)
	terminal := observations[len(observations)-1]
	require.Equal(t, "completed", terminal.Stage)
	require.ErrorIs(t, terminal.Error, ontology.ErrSourceStale)
	require.Equal(t, result.FreshnessRejection, terminal.Result.FreshnessRejection)
	assignment, err := f.store.GetRecord(context.Background(), f.team, x.assignment.ID, 0)
	require.NoError(t, err)
	require.EqualValues(t, 1, assignment.Version)
	require.Equal(t, x.parent.ID, assignment.Assignment.DefinitionID)
	history, err := f.store.History(context.Background(), f.team, 0, 20)
	require.NoError(t, err)
	require.Len(t, history, 2, "only initial setup and manager alias publication may commit")
	require.Equal(t, before, f.canonicalSnapshot(t))
}

func TestOntologyRefinementReusesBoundedCandidate(t *testing.T) {
	for _, relation := range []ontology.ParentRelation{"", ontology.SubclassOf} {
		t.Run(string(relation), func(t *testing.T) {
			f := newOrganizationFixture(t)
			oldSource := maintenanceEntity(t, f, "Original software project")
			source := maintenanceEntity(t, f, "Atlas explicitly identified as a software project")
			parent := ontology.Record{ID: uuid.NewString(), Kind: ontology.EntityClass, Definition: &ontology.Definition{Key: "project", Label: "Project", BaseEntityKind: "project"}}
			accepted := ontology.Record{ID: ontology.OrganizationRecordID(f.team, ontology.EntityClass, "software-project"), Kind: ontology.EntityClass,
				Definition: &ontology.Definition{Key: "software-project", Label: "Software project", Description: strings.Repeat("meaning ", 100), Aliases: []string{"software initiative"}, BaseEntityKind: "project", ParentID: parent.ID, ParentRelation: relation},
				Sources:    []ontology.SourceDependency{f.source(t, oldSource)}}
			revalidationPublish(t, f, ontology.Change{Record: parent}, ontology.Change{Record: accepted})
			before, err := f.store.GetRecord(context.Background(), f.team, accepted.ID, 0)
			require.NoError(t, err)
			snapshot, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{source})
			require.NoError(t, err)
			var candidateFound bool
			for _, view := range snapshot.Candidates {
				candidateFound = candidateFound || view.ID == accepted.ID
			}
			require.True(t, candidateFound)
			for _, view := range snapshot.Records {
				require.NotEqual(t, accepted.ID, view.ID)
			}
			canonical := f.canonicalSnapshot(t)
			limits := assessor.DefaultSemanticAssessmentLimits()
			limits.MaxCandidateContextTokens = 128
			service, calls := organizationFixtureServiceWithIdentity(t, f, nil, func(q assessment.Request, r *assessment.Response) {
				for _, definition := range q.Definitions {
					if definition.Key == accepted.Definition.Key {
						t.Error("bounded request retained the optional candidate")
					}
				}
				r.Definitions = []assessment.Definition{{Ref: "fresh", Kind: accepted.Kind, Key: accepted.Definition.Key, Label: "Replacement metadata", Aliases: []string{}, BaseEntityKind: "project"}}
				r.Items[0] = assessment.Decision{Ref: q.Items[0].Ref, Status: "classified", DefinitionRef: "fresh"}
			}, "bounded-candidate-fixture", limits)
			input := ontology.OrganizationInput{OperationKey: "bounded-candidate-reuse", Sources: []ontology.SourceHandle{source}}
			result, err := service.Organize(context.Background(), f.team, input)
			require.NoError(t, err)
			require.True(t, result.Current)
			require.EqualValues(t, 1, calls.Load())
			require.NotNil(t, result.Publication)
			require.Len(t, result.Publication.Records, 1)
			assignment, err := f.store.GetRecord(context.Background(), f.team, result.Publication.Records[0].ID, 0)
			require.NoError(t, err)
			require.Equal(t, accepted.ID, assignment.Assignment.DefinitionID)
			after, err := f.store.GetRecord(context.Background(), f.team, accepted.ID, 0)
			require.NoError(t, err)
			require.Equal(t, before, after)
			input.OperationKey = "bounded-candidate-replay"
			replay, err := service.Organize(context.Background(), f.team, input)
			require.NoError(t, err)
			require.True(t, replay.Existing)
			require.Equal(t, result.AssessmentID, replay.AssessmentID)
			require.EqualValues(t, 1, calls.Load())
			require.Equal(t, canonical, f.canonicalSnapshot(t))
		})
	}
}
