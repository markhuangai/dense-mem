package ontology

import (
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
