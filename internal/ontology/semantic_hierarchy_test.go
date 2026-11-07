package ontology

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestOrganizationSemanticDefinitionRefreshPreservesDeclaredParent(t *testing.T) {
	for _, kind := range []contract.Kind{contract.EntityClass, contract.PredicateConcept} {
		for _, current := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/current=%t", kind, current), func(t *testing.T) {
				relation := contract.SubclassOf
				base := "person"
				if kind == contract.PredicateConcept {
					relation, base = contract.SubpropertyOf, ""
				}
				parentID := uuid.NewString()
				stored := contract.Record{ID: uuid.NewString(), Version: 2, Kind: kind, Definition: &contract.Definition{Key: "people", Label: "People", Description: "Accepted meaning", ParentID: parentID, ParentRelation: relation, BaseEntityKind: base}}
				other := contract.Record{ID: uuid.NewString(), Version: 1, Kind: kind, Definition: &contract.Definition{Key: "other", Label: "Other", BaseEntityKind: base}}
				ctx := contract.OrganizationContext{Revision: 3, Records: []contract.RecordView{{Record: stored, Current: current}}}
				binding := requestBinding{definitions: map[string]contract.Record{"other": other}}
				response := assessment.Response{Definitions: []assessment.Definition{{Ref: "refresh", Kind: kind, Key: "people", Label: "People", Description: "Refreshed source", ParentRef: "other", BaseEntityKind: base}}}
				receipt := unitReceipt(t, nil)
				publication, err := buildPublication(uuid.NewString(), ctx, assessment.Request{}, response, binding, &receipt)
				require.NoError(t, err)
				if current {
					require.Empty(t, publication.Changes)
				} else {
					require.Len(t, publication.Changes, 1)
					require.Equal(t, parentID, publication.Changes[0].Record.Definition.ParentID)
					require.Equal(t, relation, publication.Changes[0].Record.Definition.ParentRelation)
				}
				require.Equal(t, parentID, stored.Definition.ParentID)
			})
		}
	}
}
