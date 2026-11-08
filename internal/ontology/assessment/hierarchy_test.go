package assessment

import (
	"encoding/json"
	"strings"
	"testing"

	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func hierarchyFixture() (Request, Response) {
	request := Request{RequestID: "hierarchy", Items: []Item{{Ref: "source", Kind: ontology.EntitySource, EntityKind: "person", Text: "Ada is explicitly described as an engineer.", PreferredDefinitionRef: "person"}},
		Definitions: []Definition{{Ref: "person", Kind: ontology.EntityClass, Key: "person", Label: "Person", Aliases: []string{}, BaseEntityKind: "person"}, {Ref: "engineer", Kind: ontology.EntityClass, Key: "engineer", Label: "Engineer", Aliases: []string{}, BaseEntityKind: "person"}}, Pairs: []Pair{}, HierarchyUpdateRefs: []string{"person", "engineer"}}
	response := Response{RequestID: request.RequestID, Definitions: []Definition{}, Items: []Decision{{Ref: "source", Status: "classified", DefinitionRef: "engineer", Reason: "The supplied source explicitly identifies an engineer."}}, Equivalence: []Equivalence{}, HierarchyUpdates: []HierarchyUpdate{{DefinitionRef: "engineer", ParentRef: "person", ParentRelation: ontology.SubclassOf, Reason: "An engineer is a person, as stated in the source context."}}}
	return request, response
}

func TestOrganizationV2RefinementAndClosedHierarchyUpdates(t *testing.T) {
	request, response := hierarchyFixture()
	require.NoError(t, Validate(request, response))
	raw, err := json.Marshal(response)
	require.NoError(t, err)
	decoded, err := Decode(string(raw))
	require.NoError(t, err)
	require.Equal(t, response, decoded)
	for _, mutation := range []func(map[string]any){
		func(body map[string]any) { delete(body, "hierarchy_updates") },
		func(body map[string]any) { body["hierarchy_updates"] = nil },
		func(body map[string]any) {
			body["hierarchy_updates"].([]any)[0].(map[string]any)["label"] = "rewritten"
		},
	} {
		var body map[string]any
		require.NoError(t, json.Unmarshal(raw, &body))
		mutation(body)
		invalid, err := json.Marshal(body)
		require.NoError(t, err)
		_, err = Decode(string(invalid))
		require.Error(t, err)
	}
	require.Empty(t, request.Definitions[1].ParentRelation, "validation must not mutate the supplied request")
}

func TestOrganizationV2RejectsInvalidHierarchyAsACompleteResponse(t *testing.T) {
	cases := map[string]func(*Request, *Response){
		"pinned":                  func(q *Request, _ *Response) { q.HierarchyUpdateRefs = []string{"person"} },
		"unknown target":          func(_ *Request, r *Response) { r.HierarchyUpdates[0].DefinitionRef = "foreign" },
		"unknown parent":          func(_ *Request, r *Response) { r.HierarchyUpdates[0].ParentRef = "foreign" },
		"duplicate target":        func(_ *Request, r *Response) { r.HierarchyUpdates = append(r.HierarchyUpdates, r.HierarchyUpdates[0]) },
		"missing justification":   func(_ *Request, r *Response) { r.HierarchyUpdates[0].Reason = " " },
		"oversized justification": func(_ *Request, r *Response) { r.HierarchyUpdates[0].Reason = strings.Repeat("x", 129) },
		"unrelated target":        func(_ *Request, r *Response) { r.Items[0].DefinitionRef = "person" },
		"ambiguous source": func(_ *Request, r *Response) {
			r.Items[0] = Decision{Ref: "source", Status: "ambiguous", Reason: "insufficient source context"}
		},
		"unknown relation":        func(_ *Request, r *Response) { r.HierarchyUpdates[0].ParentRelation = "related_to" },
		"missing semantic parent": func(_ *Request, r *Response) { r.HierarchyUpdates[0].ParentRef = "" },
		"wrong semantic kind":     func(_ *Request, r *Response) { r.HierarchyUpdates[0].ParentRelation = ontology.SubpropertyOf },
		"wrong parent kind": func(q *Request, _ *Response) {
			q.Definitions[0].Kind = ontology.PredicateConcept
			q.Definitions[0].BaseEntityKind = ""
		},
		"wrong EntityKind":       func(q *Request, _ *Response) { q.Definitions[0].BaseEntityKind = "organization" },
		"cycle":                  func(_ *Request, r *Response) { r.HierarchyUpdates[0].ParentRef = "engineer" },
		"locked classification":  func(q *Request, _ *Response) { q.Items[0].LockedDefinitionRef = "person" },
		"unjustified refinement": func(_ *Request, r *Response) { r.Items[0].Reason = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			request, response := hierarchyFixture()
			mutate(&request, &response)
			err := Validate(request, response)
			require.Error(t, err)
			diagnostic := validationDiagnostic("validate", err, request, response)
			require.NotNil(t, diagnostic)
			require.True(t, diagnostic.Valid(), "%+v", diagnostic)
		})
	}
}

func TestOrganizationV2ReusesPreferencesAndPreservesAmbiguity(t *testing.T) {
	request, response := hierarchyFixture()
	response.HierarchyUpdates = []HierarchyUpdate{}
	response.Items[0].DefinitionRef, response.Items[0].Reason = "person", ""
	require.NoError(t, Validate(request, response))
	response.Items[0] = Decision{Ref: "source", Status: "ambiguous", Reason: "A name alone does not establish a personal role."}
	require.NoError(t, Validate(request, response))
	request.Items[0].LockedDefinitionRef = "person"
	require.Error(t, Validate(request, response))
	request, response = hierarchyFixture()
	request.Items[0].Kind, request.Items[0].EntityKind = ontology.PredicateSource, ""
	for i := range request.Definitions {
		request.Definitions[i].Kind, request.Definitions[i].BaseEntityKind = ontology.PredicateConcept, ""
	}
	response.HierarchyUpdates[0].ParentRelation = ontology.SubpropertyOf
	require.NoError(t, Validate(request, response))
}

func TestOrganizationV2ValidatesNewSemanticDefinitions(t *testing.T) {
	request, response := hierarchyFixture()
	request.Definitions = request.Definitions[:1]
	request.HierarchyUpdateRefs = nil
	response.Definitions = []Definition{{Ref: "engineer", Kind: ontology.EntityClass, Key: "engineer", Label: "Engineer", Aliases: []string{}, ParentRef: "person", ParentRelation: ontology.SubclassOf, BaseEntityKind: "person"}}
	response.HierarchyUpdates = []HierarchyUpdate{}
	require.NoError(t, Validate(request, response))
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	_, err = Decode(strings.Replace(string(encoded), `"parent_relation":"subclass_of",`, "", 1))
	require.Error(t, err)
	response.Definitions[0].ParentRelation = ontology.SubpropertyOf
	require.Error(t, Validate(request, response))
}
