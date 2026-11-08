package ontology

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestOrganizationRefinementPublicationAndAmbiguity(t *testing.T) {
	for _, ambiguous := range []bool{false, true} {
		for _, semantic := range []bool{false, true} {
			source := unitSource(uuid.NewString(), "Atlas is an explicitly named software project.")
			source.Kind, source.EntityKind = contract.EntitySource, "project"
			parent := contract.Record{ID: uuid.NewString(), Version: 1, Kind: contract.EntityClass, Definition: &contract.Definition{Key: "project", Label: "Project", BaseEntityKind: "project"}}
			child := contract.Record{ID: uuid.NewString(), Version: 1, Kind: contract.EntityClass, Definition: &contract.Definition{Key: "software-project", Label: "Software project", Description: "Retained description", Aliases: []string{"software initiative"}, BaseEntityKind: "project", ParentID: parent.ID}}
			assignment := contract.Record{ID: uuid.NewString(), Version: 1, Kind: contract.AssignmentKind, Assignment: &contract.Assignment{Source: source.SourceHandle, DefinitionID: child.ID}, Sources: unitReceipt(t, []contract.SourceSnapshot{source}).Sources}
			ctx := contract.OrganizationContext{Sources: []contract.SourceSnapshot{source}, Records: []contract.RecordView{{Record: assignment, Current: true}}, Candidates: []contract.RecordView{{Record: child, Current: true}, {Record: parent, Current: true}}}
			receipt := unitReceipt(t, ctx.Sources)
			service := NewService(nil, assessment.NewProvider(nil, "model", assessor.DefaultSemanticAssessmentLimits(), 300000))
			request, binding, err := service.request(ctx, &receipt)
			require.NoError(t, err)
			require.Empty(t, request.Items[0].LockedDefinitionRef)
			require.Equal(t, "d0", request.Items[0].PreferredDefinitionRef)
			require.ElementsMatch(t, []string{"d0", "d1"}, request.HierarchyUpdateRefs)
			response := assessment.Response{RequestID: request.RequestID, Items: []assessment.Decision{{Ref: "s0", Status: "classified", DefinitionRef: "d0"}}, Definitions: []assessment.Definition{}, Equivalence: []assessment.Equivalence{}, HierarchyUpdates: []assessment.HierarchyUpdate{}}
			if ambiguous {
				response.Items[0] = assessment.Decision{Ref: "s0", Status: "ambiguous", Reason: "Context does not justify refinement."}
			}
			if semantic && !ambiguous {
				response.HierarchyUpdates = append(response.HierarchyUpdates, assessment.HierarchyUpdate{DefinitionRef: "d0", ParentRef: "d1", ParentRelation: contract.SubclassOf, Reason: "The explicit software project is a project."})
			}
			require.NoError(t, assessment.Validate(request, response))
			publication, err := buildPublication(source.TeamID, ctx, request, response, binding, &receipt)
			require.NoError(t, err)
			if semantic && !ambiguous {
				require.Len(t, publication.Changes, 2)
				for _, change := range publication.Changes {
					require.EqualValues(t, 1, change.ExpectedVersion)
					if change.Record.Definition != nil {
						require.Equal(t, contract.SubclassOf, change.Record.Definition.ParentRelation)
						require.Equal(t, child.Definition.Description, change.Record.Definition.Description)
						require.Equal(t, child.Definition.Aliases, change.Record.Definition.Aliases)
					} else {
						require.Equal(t, assignment.ID, change.Record.ID)
						require.Empty(t, change.Record.Dependencies)
					}
				}
			} else {
				require.Empty(t, publication.Changes, "without an explicit update accepted parent meaning must remain unchanged")
			}
			if ambiguous {
				require.Equal(t, "ambiguous", receipt.Result.Outcomes[0].Status)
			}
			require.Equal(t, []string{assignment.ID}, receipt.Result.Outcomes[0].RecordIDs)
		}
	}
}

func TestOrganizationRequestPreservesSemanticAncestryAndPins(t *testing.T) {
	source := unitSource("ancestry", "Explicit project context")
	source.Kind, source.EntityKind = contract.EntitySource, "project"
	parent := contract.Record{ID: uuid.NewString(), Version: 1, Kind: contract.EntityClass, Definition: &contract.Definition{Key: "project", Label: "Project", BaseEntityKind: "project"}}
	child := contract.Record{ID: uuid.NewString(), Version: 1, Kind: contract.EntityClass, Definition: &contract.Definition{Key: "software-project", Label: "Software project", BaseEntityKind: "project", ParentID: parent.ID, ParentRelation: contract.SubclassOf}}
	assignment := contract.Record{ID: uuid.NewString(), Kind: contract.AssignmentKind, Assignment: &contract.Assignment{Source: source.SourceHandle, DefinitionID: child.ID}}
	pin := contract.Record{ID: uuid.NewString(), Kind: contract.OverrideKind, Override: &contract.Override{Action: contract.PinDefinition, TargetID: child.ID}}
	ctx := contract.OrganizationContext{Sources: []contract.SourceSnapshot{source}, Records: []contract.RecordView{{Record: assignment, Current: true}, {Record: pin, Current: true}}, Candidates: []contract.RecordView{{Record: child, Current: true}, {Record: parent, Current: true}}}
	service := NewService(nil, assessment.NewProvider(nil, "model", assessor.DefaultSemanticAssessmentLimits(), 300000))
	receipt := unitReceipt(t, ctx.Sources)
	request, _, err := service.request(ctx, &receipt)
	require.NoError(t, err)
	require.Equal(t, "d1", request.Definitions[0].ParentRef)
	require.Equal(t, contract.SubclassOf, request.Definitions[0].ParentRelation)
	require.Equal(t, []string{"d1"}, request.HierarchyUpdateRefs)
	ctx.Candidates = ctx.Candidates[:1]
	receipt = unitReceipt(t, ctx.Sources)
	request, _, err = service.request(ctx, &receipt)
	require.NoError(t, err)
	require.Empty(t, request.Items)
	require.Equal(t, "preferred_classification_context_bound", receipt.Result.Outcomes[0].Reason)
}

func TestOrganizationReproposedCandidatePreservesAcceptedDefinition(t *testing.T) {
	for _, kind := range []contract.Kind{contract.EntityClass, contract.PredicateConcept} {
		t.Run(string(kind), func(t *testing.T) {
			source := unitSource("reproposed", "Atlas is a software project.")
			source.Kind, source.EntityKind = contract.EntitySource, "project"
			relation, base, parentKey := contract.SubclassOf, "project", "project"
			if kind == contract.PredicateConcept {
				source.Kind, source.ID, source.EntityKind = contract.PredicateSource, "uses", ""
				relation, base, parentKey = contract.SubpropertyOf, "", "uses"
			}
			parent := contract.Record{ID: uuid.NewString(), Version: 1, Kind: kind, Definition: &contract.Definition{Key: parentKey, Label: "Parent", BaseEntityKind: base}}
			accepted := contract.Record{ID: contract.OrganizationRecordID(source.TeamID, kind, "specific"), Version: 3, Kind: kind,
				Definition: &contract.Definition{Key: "specific", Label: "Accepted meaning", Description: strings.Repeat("meaning ", 100), Aliases: []string{"accepted alias"}, BaseEntityKind: base, ParentID: parent.ID, ParentRelation: relation},
				Sources:    unitReceipt(t, []contract.SourceSnapshot{unitSource("other-source", "Accepted provenance")}).Sources}
			ctx := contract.OrganizationContext{Sources: []contract.SourceSnapshot{source}, Candidates: []contract.RecordView{{Record: parent, Current: true}, {Record: accepted, Current: true}}}
			receipt := unitReceipt(t, ctx.Sources)
			limits := assessor.DefaultSemanticAssessmentLimits()
			limits.MaxCandidateContextTokens = 128
			request, binding, err := NewService(nil, assessment.NewProvider(nil, "model", limits, 300000)).request(ctx, &receipt)
			require.NoError(t, err)
			require.Len(t, request.Definitions, 1)
			require.Equal(t, parent.Definition.Key, request.Definitions[0].Key)
			require.Equal(t, request.Definitions[0].Ref, request.Items[0].PreferredDefinitionRef)
			response := assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{{Ref: "fresh", Kind: kind, Key: "specific", Label: "Replacement metadata", Aliases: []string{}, BaseEntityKind: base}},
				Items: []assessment.Decision{{Ref: "s0", Status: "classified", DefinitionRef: "fresh", Reason: "The supplied context supports the more specific classification."}}, Equivalence: []assessment.Equivalence{}, HierarchyUpdates: []assessment.HierarchyUpdate{}}
			require.NoError(t, assessment.Validate(request, response))
			publication, err := buildPublication(source.TeamID, ctx, request, response, binding, &receipt)
			require.NoError(t, err)
			require.Len(t, publication.Changes, 1)
			require.Equal(t, contract.AssignmentKind, publication.Changes[0].Record.Kind)
			require.Equal(t, accepted.ID, publication.Changes[0].Record.Assignment.DefinitionID)
			require.Equal(t, accepted, ctx.Candidates[1].Record)
		})
	}
}

func TestOrganizationAmbiguityRetainsEligiblePredicateVersion(t *testing.T) {
	old := unitSource("predicate-version", "An active project dependency.")
	old.Kind, old.ID = contract.PredicateSource, "dependency"
	latest := old
	latest.Version++
	other := old
	other.ID = "utilizes"
	parent := contract.Record{ID: uuid.NewString(), Version: 1, Kind: contract.PredicateConcept, Definition: &contract.Definition{Key: "utilizes", Label: "Utilizes"}}
	var err error
	parent.Fingerprint, err = contract.RecordFingerprint(parent, nil, nil)
	require.NoError(t, err)
	child := contract.Record{ID: uuid.NewString(), Version: 1, Kind: contract.PredicateConcept, Definition: &contract.Definition{Key: "project-dependency-use", Label: "Project dependency use"}}
	assignment := contract.Record{ID: uuid.NewString(), Version: 1, Kind: contract.AssignmentKind, Assignment: &contract.Assignment{Source: old.SourceHandle, DefinitionID: child.ID}, Sources: unitReceipt(t, []contract.SourceSnapshot{old}).Sources, Dependencies: []contract.RevisionRef{{ID: child.ID, Version: 1}}}
	ctx := contract.OrganizationContext{Revision: 1, Sources: []contract.SourceSnapshot{latest, other}, Records: []contract.RecordView{{Record: assignment, Current: true}}, Candidates: []contract.RecordView{{Record: child, Current: true}, {Record: parent, Current: true}}}
	receipt := unitReceipt(t, ctx.Sources)
	request, binding, err := NewService(nil, assessment.NewProvider(nil, "model", assessor.DefaultSemanticAssessmentLimits(), 300000)).request(ctx, &receipt)
	require.NoError(t, err)
	childRef, parentRef := "", ""
	for _, definition := range request.Definitions {
		if definition.Key == child.Definition.Key {
			childRef = definition.Ref
		} else if definition.Key == parent.Definition.Key {
			parentRef = definition.Ref
		}
	}
	response := assessment.Response{RequestID: request.RequestID, Definitions: []assessment.Definition{}, Equivalence: []assessment.Equivalence{}, HierarchyUpdates: []assessment.HierarchyUpdate{{DefinitionRef: childRef, ParentRef: parentRef, ParentRelation: contract.SubpropertyOf, Reason: "The supplied project dependency use specializes utilizes."}}}
	for _, item := range request.Items {
		decision := assessment.Decision{Ref: item.Ref, Status: "classified", DefinitionRef: childRef, Reason: "The supplied context describes project dependency use."}
		if item.Context["predicate"] == latest.ID {
			decision = assessment.Decision{Ref: item.Ref, Status: "ambiguous", Reason: "The new version's context does not justify refinement."}
		}
		response.Items = append(response.Items, decision)
	}
	require.NoError(t, assessment.Validate(request, response))
	publication, err := buildPublication(old.TeamID, ctx, request, response, binding, &receipt)
	require.NoError(t, err)
	require.Equal(t, "ambiguous", receipt.Result.Outcomes[0].Status)
	require.Equal(t, []string{assignment.ID}, receipt.Result.Outcomes[0].RecordIDs)
	snapshots := map[string]contract.SourceSnapshot{contract.SourceKey(old.SourceHandle): old, contract.SourceKey(other.SourceHandle): other}
	for _, change := range publication.Changes {
		require.NoError(t, contract.CheckSourceDependencies(change.Record, snapshots), "%+v", change.Record)
	}
	prepared, err := contract.PreparePublication(map[string]contract.Record{parent.ID: parent, child.ID: child, assignment.ID: assignment}, snapshots, publication, true)
	require.NoError(t, err)
	require.Len(t, prepared, 3)
	for _, record := range prepared {
		if record.ID == assignment.ID {
			require.Equal(t, assignment.Assignment, record.Assignment)
			require.Equal(t, assignment.Sources, record.Sources)
			require.EqualValues(t, 2, record.Version)
			require.Contains(t, record.Dependencies, contract.RevisionRef{ID: child.ID, Version: 2})
		}
	}
}
