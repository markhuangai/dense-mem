package assessment

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

const SchemaName = "ontology_organization_v1"

type Item struct {
	Ref                 string              `json:"ref"`
	Kind                ontology.SourceKind `json:"kind"`
	Text                string              `json:"text"`
	OwnerRef            string              `json:"owner_ref"`
	EntityKind          string              `json:"entity_kind"`
	Context             map[string]string   `json:"context"`
	LockedDefinitionRef string              `json:"locked_definition_ref"`
}

type Definition struct {
	Ref            string        `json:"ref"`
	Kind           ontology.Kind `json:"kind"`
	Key            string        `json:"key"`
	Label          string        `json:"label"`
	Description    string        `json:"description"`
	Aliases        []string      `json:"aliases"`
	ParentRef      string        `json:"parent_ref"`
	BaseEntityKind string        `json:"base_entity_kind"`
}

type Pair struct {
	Ref              string `json:"ref"`
	LeftRef          string `json:"left_ref"`
	RightRef         string `json:"right_ref"`
	RequiredRelation string `json:"required_relation"`
}

type Request struct {
	RequestID   string       `json:"request_id"`
	Items       []Item       `json:"items"`
	Definitions []Definition `json:"definitions"`
	Pairs       []Pair       `json:"pairs"`
}

type Decision struct {
	Ref           string `json:"ref"`
	Status        string `json:"status"`
	DefinitionRef string `json:"definition_ref"`
	Reason        string `json:"reason"`
}

type Equivalence struct {
	Ref      string `json:"ref"`
	Relation string `json:"relation"`
}

type Response struct {
	RequestID   string        `json:"request_id"`
	Definitions []Definition  `json:"definitions"`
	Items       []Decision    `json:"items"`
	Equivalence []Equivalence `json:"equivalence"`
}

func Decode(raw string) (Response, error) {
	var response Response
	if err := jsonstrict.Decode(strings.NewReader(raw), &response, ontology.MaxPublicationBytes); err != nil {
		return response, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return response, err
	}
	for _, field := range []string{"request_id", "definitions", "items", "equivalence"} {
		if value, ok := fields[field]; !ok || string(value) == "null" {
			return response, fmt.Errorf("missing %s", field)
		}
	}
	for _, section := range []struct {
		name   string
		fields []string
	}{
		{"definitions", []string{"ref", "kind", "key", "label", "description", "aliases", "parent_ref", "base_entity_kind"}},
		{"items", []string{"ref", "status", "definition_ref", "reason"}},
		{"equivalence", []string{"ref", "relation"}},
	} {
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(fields[section.name], &entries); err != nil {
			return response, err
		}
		for _, entry := range entries {
			for _, field := range section.fields {
				if value, ok := entry[field]; !ok || string(value) == "null" {
					return response, fmt.Errorf("missing %s.%s", section.name, field)
				}
			}
		}
	}
	return response, nil
}

func Validate(request Request, response Response) error {
	if response.RequestID != request.RequestID || response.Definitions == nil || response.Items == nil || response.Equivalence == nil ||
		len(response.Definitions) > ontology.MaxVocabularyCandidates || len(response.Items) != len(request.Items) {
		return fmt.Errorf("response identity, arrays, or coverage is invalid")
	}
	definitions := map[string]Definition{}
	catalog := map[string]ontology.Record{}
	for _, d := range request.Definitions {
		definitions[d.Ref] = d
	}
	newRefs := map[string]bool{}
	for _, d := range response.Definitions {
		if !validRef(d.Ref) || newRefs[d.Ref] {
			return fmt.Errorf("new definition ref is invalid or duplicated")
		}
		if definitions[d.Ref].Ref != "" {
			return fmt.Errorf("response.definitions is only for new definitions; omit supplied ref %q and reuse it in items[].definition_ref", d.Ref)
		}
		definitions[d.Ref] = d
		newRefs[d.Ref] = true
	}
	for ref, d := range definitions {
		parentID := ""
		if d.ParentRef != "" {
			if _, ok := definitions[d.ParentRef]; !ok {
				return fmt.Errorf("parent ref is outside vocabulary allowlist")
			}
			parentID = referenceID(d.ParentRef)
		}
		record := ontology.Record{ID: referenceID(ref), Kind: d.Kind, Definition: &ontology.Definition{Key: d.Key, Label: d.Label, Description: d.Description, Aliases: d.Aliases, ParentID: parentID, BaseEntityKind: d.BaseEntityKind}}
		if err := ontology.ValidateRecord(record); err != nil {
			return fmt.Errorf("invalid definition: %w", err)
		}
		catalog[record.ID] = record
	}
	if err := ontology.ValidateDefinitions(catalog); err != nil {
		return err
	}
	items := map[string]Item{}
	for _, item := range request.Items {
		items[item.Ref] = item
	}
	seen := map[string]bool{}
	used := map[string]bool{}
	for _, decision := range response.Items {
		item, ok := items[decision.Ref]
		if !ok || seen[decision.Ref] || len(decision.Reason) > 128 {
			return fmt.Errorf("item ref, reason, or coverage is invalid")
		}
		seen[decision.Ref] = true
		switch decision.Status {
		case "ambiguous":
			if decision.DefinitionRef != "" || decision.Reason == "" || item.LockedDefinitionRef != "" {
				return fmt.Errorf("ambiguous item must remain unclassified and cannot replace a locked classification")
			}
		case "classified":
			d, ok := definitions[decision.DefinitionRef]
			if !ok || (item.LockedDefinitionRef != "" && decision.DefinitionRef != item.LockedDefinitionRef) {
				return fmt.Errorf("classification is outside allowlist or changes deterministic reuse")
			}
			if !compatibleDefinition(item, d) {
				return fmt.Errorf("classification has incompatible source kind")
			}
			used[decision.DefinitionRef] = true
		default:
			return fmt.Errorf("unknown item status")
		}
	}
	for ref := range newRefs {
		if !used[ref] {
			return fmt.Errorf("new definition has no source assignment")
		}
	}
	groups := []assessor.SemanticAssessmentEvidenceEquivalenceCandidateGroup{}
	choices := []assessor.SemanticAssessmentEvidenceEquivalenceResult{}
	pairs := map[string]Pair{}
	for _, pair := range request.Pairs {
		pairs[pair.Ref] = pair
		groups = append(groups, assessor.SemanticAssessmentEvidenceEquivalenceCandidateGroup{EvidenceID: pair.Ref, Candidates: []assessor.SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "equivalent", Content: "complete meaning equivalence"}}})
	}
	equivalent := map[string]bool{}
	for _, decision := range response.Equivalence {
		pair, ok := pairs[decision.Ref]
		if !ok {
			return fmt.Errorf("equivalence ref is outside allowlist")
		}
		choice := assessor.SemanticAssessmentEvidenceEquivalenceResult{EvidenceID: decision.Ref, Action: "new"}
		switch decision.Relation {
		case "equivalent":
			value := "equivalent"
			choice.Action = "reuse"
			choice.CandidateEvidenceID = &value
			equivalent[pairKey(pair.LeftRef, pair.RightRef)] = true
		case "distinct", "ambiguous":
		default:
			return fmt.Errorf("unknown equivalence relation")
		}
		if pair.RequiredRelation != "" && decision.Relation != pair.RequiredRelation {
			return fmt.Errorf("equivalence contradicts deterministic context or manager rule")
		}
		choices = append(choices, choice)
	}
	if errors := assessor.ValidateEvidenceEquivalenceResults(groups, choices); len(errors) > 0 {
		return fmt.Errorf("invalid equivalence coverage: return exactly one decision per supplied pair ref, including distinct and ambiguous pairs; duplicate pair refs are not allowed")
	}
	for _, a := range request.Items {
		for _, b := range request.Items {
			for _, c := range request.Items {
				if a.Ref != c.Ref && equivalent[pairKey(a.Ref, b.Ref)] && equivalent[pairKey(b.Ref, c.Ref)] && !equivalent[pairKey(a.Ref, c.Ref)] {
					return fmt.Errorf("equivalence is not transitive across complete comparisons")
				}
			}
		}
	}
	return nil
}

func compatibleDefinition(item Item, d Definition) bool {
	switch item.Kind {
	case ontology.EntitySource:
		return d.Kind == ontology.EntityClass && d.BaseEntityKind == item.EntityKind
	case ontology.PredicateSource:
		return d.Kind == ontology.PredicateConcept
	case ontology.EvidenceSource, ontology.RelationshipSource:
		return d.Kind == ontology.Topic
	}
	return false
}

func referenceID(ref string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("ontology-response:"+ref)).String()
}
func validRef(ref string) bool {
	return strings.TrimSpace(ref) == ref && len(ref) > 0 && len(ref) <= 64 && !strings.ContainsRune(ref, 0)
}
func pairKey(left, right string) string {
	if left > right {
		left, right = right, left
	}
	return left + ":" + right
}

func ResponseSchema() map[string]any {
	text := map[string]any{"type": "string"}
	ref := map[string]any{"type": "string", "maxLength": 64}
	definition := closedObject(map[string]any{"ref": ref, "kind": map[string]any{"type": "string", "enum": []string{"entity_class", "predicate_concept", "topic"}}, "key": map[string]any{"type": "string", "maxLength": 128}, "label": map[string]any{"type": "string", "maxLength": 128}, "description": map[string]any{"type": "string", "maxLength": 1000}, "aliases": map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "string", "maxLength": 128}}, "parent_ref": ref, "base_entity_kind": text})
	item := closedObject(map[string]any{"ref": ref, "status": map[string]any{"type": "string", "enum": []string{"classified", "ambiguous"}}, "definition_ref": ref, "reason": map[string]any{"type": "string", "maxLength": 128}})
	pair := closedObject(map[string]any{"ref": ref, "relation": map[string]any{"type": "string", "enum": []string{"equivalent", "distinct", "ambiguous"}}})
	return closedObject(map[string]any{"request_id": text, "definitions": map[string]any{"type": "array", "maxItems": 20, "items": definition}, "items": map[string]any{"type": "array", "maxItems": 20, "items": item}, "equivalence": map[string]any{"type": "array", "maxItems": 190, "items": pair}})
}

func closedObject(properties map[string]any) map[string]any {
	required := []string{}
	for key := range properties {
		required = append(required, key)
	}
	sort.Strings(required)
	return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
}
