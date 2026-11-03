package assessment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/assessor"
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
	if len(raw) > ontology.MaxPublicationBytes {
		return Response{}, validationResult([]assessor.SemanticValidationError{validationIssue("response", "strict_json", "JSON input exceeds %d bytes", ontology.MaxPublicationBytes)}, nil)
	}
	fields, errs := assessor.ValidateResponseObject([]byte(raw), "", []string{"request_id", "definitions", "items", "equivalence"}, nil, nil)
	for _, section := range []struct {
		name   string
		fields []string
	}{
		{"definitions", []string{"ref", "kind", "key", "label", "description", "aliases", "parent_ref", "base_entity_kind"}},
		{"items", []string{"ref", "status", "definition_ref", "reason"}},
		{"equivalence", []string{"ref", "relation"}},
	} {
		if value, ok := fields[section.name]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			errs = append(errs, assessor.ValidateResponseArrayObjects(value, section.name, section.fields, nil, nil)...)
		}
	}
	for i := range errs {
		if errs[i].Field == "" {
			errs[i].Field = "response"
		}
		switch errs[i].Message {
		case "is required", "must not be null":
			errs[i].Code = "missing_field"
		case "must be an array":
			errs[i].Code = "array_decode"
		default:
			errs[i].Code = "strict_json"
		}
	}
	if len(errs) > 0 {
		return Response{}, validationResult(errs, nil)
	}
	var response Response
	decode := func(raw json.RawMessage, path string, target any) {
		if err := json.Unmarshal(raw, target); err != nil {
			issue := assessor.ResponseJSONValidationError(err, path)
			issue.Code = "strict_json"
			issue.Message += fmt.Sprintf("; %v; offending item: %s", err, raw)
			errs = append(errs, issue)
		}
	}
	decode(fields["request_id"], "request_id", &response.RequestID)
	for _, section := range []string{"definitions", "items", "equivalence"} {
		var entries []json.RawMessage
		_ = json.Unmarshal(fields[section], &entries)
		switch section {
		case "definitions":
			response.Definitions = make([]Definition, len(entries))
		case "items":
			response.Items = make([]Decision, len(entries))
		case "equivalence":
			response.Equivalence = make([]Equivalence, len(entries))
		}
		for i, entry := range entries {
			path := fmt.Sprintf("%s[%d]", section, i)
			switch section {
			case "definitions":
				decode(entry, path, &response.Definitions[i])
			case "items":
				decode(entry, path, &response.Items[i])
			case "equivalence":
				decode(entry, path, &response.Equivalence[i])
			}
		}
	}
	if len(errs) > 0 {
		return Response{}, validationResult(errs, nil)
	}
	return response, nil
}

func Validate(request Request, response Response) error {
	var errs []assessor.SemanticValidationError
	var cause error
	if response.RequestID != request.RequestID {
		errs = append(errs, validationIssue("request_id", "request_id_mismatch", "must equal %q; received %q", request.RequestID, response.RequestID))
	}
	for _, section := range []struct {
		name    string
		missing bool
	}{{"definitions", response.Definitions == nil}, {"items", response.Items == nil}, {"equivalence", response.Equivalence == nil}} {
		if section.missing {
			errs = append(errs, validationIssue(section.name, "missing_array", "must be an array, including when empty"))
		}
	}
	if len(response.Definitions) > ontology.MaxVocabularyCandidates {
		errs = append(errs, validationIssue("definitions", "definition_limit", "must contain at most %d definitions; received %d", ontology.MaxVocabularyCandidates, len(response.Definitions)))
	}
	if len(response.Items) != len(request.Items) {
		errs = append(errs, validationIssue("items", "item_count_mismatch", "must contain exactly %d decisions, one per supplied item ref; received %d", len(request.Items), len(response.Items)))
	}
	if len(response.Definitions) > ontology.MaxVocabularyCandidates || len(response.Items) > 20 || len(response.Equivalence) > 190 {
		if len(response.Equivalence) > 190 {
			errs = append(errs, validationIssue("equivalence", "equivalence_coverage", "must contain exactly one decision per supplied pair ref; received %d", len(response.Equivalence)))
		}
		return validationResult(errs, nil)
	}
	definitions := map[string]Definition{}
	paths := map[string]string{}
	definitionOrder := []string{}
	for i, d := range request.Definitions {
		definitions[d.Ref] = d
		paths[d.Ref] = fmt.Sprintf("request.definitions[%d]", i)
		definitionOrder = append(definitionOrder, d.Ref)
	}
	newRefs := []string{}
	for i, d := range response.Definitions {
		path := fmt.Sprintf("definitions[%d]", i)
		if !validRef(d.Ref) {
			errs = append(errs, validationIssue(path+".ref", "definition_ref_invalid", "definition ref %q must be nonempty, trimmed, at most 64 bytes and contain no NUL", d.Ref))
			continue
		}
		if _, exists := definitions[d.Ref]; exists {
			code := "definition_ref_duplicate"
			message := "definition ref %q is duplicated; use a fresh ref for each new definition"
			if strings.HasPrefix(paths[d.Ref], "request.") {
				code = "definition_ref_supplied"
				message = "response.definitions is only for new definitions; omit supplied ref %q and reuse it in items[].definition_ref"
			}
			errs = append(errs, validationIssue(path+".ref", code, message, d.Ref))
			continue
		}
		definitions[d.Ref] = d
		paths[d.Ref] = path
		definitionOrder = append(definitionOrder, d.Ref)
		newRefs = append(newRefs, d.Ref)
	}
	catalog := map[string]ontology.Record{}
	catalogValid := true
	for _, ref := range definitionOrder {
		d := definitions[ref]
		parentID := ""
		if d.ParentRef != "" {
			if _, ok := definitions[d.ParentRef]; !ok {
				errs = append(errs, validationIssue(paths[ref]+".parent_ref", "definition_parent_ref", "definition %q parent_ref %q is outside vocabulary allowlist", ref, d.ParentRef))
				catalogValid = false
			}
			parentID = referenceID(d.ParentRef)
		}
		record := ontology.Record{ID: referenceID(ref), Kind: d.Kind, Definition: &ontology.Definition{Key: d.Key, Label: d.Label, Description: d.Description, Aliases: d.Aliases, ParentID: parentID, BaseEntityKind: d.BaseEntityKind}}
		if err := ontology.ValidateRecord(record); err != nil {
			encoded, _ := json.Marshal(d)
			errs = append(errs, validationIssue(paths[ref], "definition_record", "invalid definition %s: %v", encoded, err))
			cause = ontology.ErrInvalid
			catalogValid = false
		}
		catalog[record.ID] = record
	}
	if catalogValid {
		if err := ontology.ValidateDefinitions(catalog); err != nil {
			encoded, _ := json.Marshal(response.Definitions)
			errs = append(errs, validationIssue("definitions", "definition_catalog", "%v; proposed definitions: %s", err, encoded))
			cause = ontology.ErrInvalid
		}
	}
	items := map[string]Item{}
	for _, item := range request.Items {
		items[item.Ref] = item
	}
	seen := map[string]bool{}
	used := map[string]bool{}
	for i, decision := range response.Items {
		path := fmt.Sprintf("items[%d]", i)
		item, ok := items[decision.Ref]
		if !ok {
			errs = append(errs, validationIssue(path+".ref", "item_ref_unknown", "item ref %q is outside the supplied item allowlist", decision.Ref))
			continue
		}
		if seen[decision.Ref] {
			errs = append(errs, validationIssue(path+".ref", "item_ref_duplicate", "item ref %q is duplicated; return it exactly once", decision.Ref))
		}
		seen[decision.Ref] = true
		if len(decision.Reason) > 128 {
			errs = append(errs, validationIssue(path+".reason", "item_reason_limit", "item %q reason must be at most 128 bytes; received %d bytes: %q", decision.Ref, len(decision.Reason), decision.Reason))
		}
		switch decision.Status {
		case "ambiguous":
			if decision.DefinitionRef != "" || decision.Reason == "" || item.LockedDefinitionRef != "" {
				errs = append(errs, validationIssue(path, "item_ambiguous_classification", "item %q: ambiguous requires empty definition_ref and a nonempty reason, and cannot replace locked_definition_ref %q; received definition_ref %q and reason %q", decision.Ref, item.LockedDefinitionRef, decision.DefinitionRef, decision.Reason))
			}
		case "classified":
			if item.LockedDefinitionRef != "" && decision.DefinitionRef != item.LockedDefinitionRef {
				errs = append(errs, validationIssue(path+".definition_ref", "item_locked_classification", "item %q classification changes deterministic reuse: must equal locked_definition_ref %q; received %q", decision.Ref, item.LockedDefinitionRef, decision.DefinitionRef))
			}
			d, ok := definitions[decision.DefinitionRef]
			if !ok {
				errs = append(errs, validationIssue(path+".definition_ref", "item_definition_ref", "item %q definition_ref %q is outside allowlist; include a new definition in response.definitions or reuse a supplied ref", decision.Ref, decision.DefinitionRef))
				continue
			}
			if !compatibleDefinition(item, d) {
				expected := string(ontology.Topic)
				if item.Kind == ontology.EntitySource {
					expected = fmt.Sprintf("%s with base_entity_kind %q", ontology.EntityClass, item.EntityKind)
				}
				if item.Kind == ontology.PredicateSource {
					expected = string(ontology.PredicateConcept)
				}
				errs = append(errs, validationIssue(path+".definition_ref", "item_source_kind", "item %q has incompatible source kind %q: expected %s; definition_ref %q selects kind %q with base_entity_kind %q", decision.Ref, item.Kind, expected, decision.DefinitionRef, d.Kind, d.BaseEntityKind))
			}
			used[decision.DefinitionRef] = true
		default:
			errs = append(errs, validationIssue(path+".status", "item_status", "item %q status must be classified or ambiguous; received %q", decision.Ref, decision.Status))
		}
	}
	for _, item := range request.Items {
		if !seen[item.Ref] {
			errs = append(errs, validationIssue("items", "item_count_mismatch", "missing decision for supplied item ref %q; return each supplied item exactly once", item.Ref))
		}
	}
	for _, ref := range newRefs {
		if !used[ref] {
			errs = append(errs, validationIssue(paths[ref], "definition_unused", "new definition %q has no source assignment; omit it or assign an appropriate source", ref))
		}
	}
	groups := []assessor.SemanticAssessmentEvidenceEquivalenceCandidateGroup{}
	choices := []assessor.SemanticAssessmentEvidenceEquivalenceResult{}
	pairs := map[string]Pair{}
	pairRefs := []string{}
	for _, pair := range request.Pairs {
		pairs[pair.Ref] = pair
		pairRefs = append(pairRefs, pair.Ref)
		groups = append(groups, assessor.SemanticAssessmentEvidenceEquivalenceCandidateGroup{EvidenceID: pair.Ref, Candidates: []assessor.SemanticAssessmentEvidenceEquivalenceCandidate{{EvidenceID: "equivalent", Content: "complete meaning equivalence"}}})
	}
	equivalent := map[string]bool{}
	for i, decision := range response.Equivalence {
		path := fmt.Sprintf("equivalence[%d]", i)
		pair, ok := pairs[decision.Ref]
		if !ok {
			errs = append(errs, validationIssue(path+".ref", "equivalence_ref", "pair ref %q is outside allowlist %q", decision.Ref, pairRefs))
		}
		choice := assessor.SemanticAssessmentEvidenceEquivalenceResult{EvidenceID: decision.Ref, Action: "new"}
		switch decision.Relation {
		case "equivalent":
			value := "equivalent"
			choice.Action = "reuse"
			choice.CandidateEvidenceID = &value
			if ok {
				equivalent[pairKey(pair.LeftRef, pair.RightRef)] = true
			}
		case "distinct", "ambiguous":
		default:
			errs = append(errs, validationIssue(path+".relation", "equivalence_relation", "pair %q relation must be equivalent, distinct or ambiguous; received %q", decision.Ref, decision.Relation))
		}
		if ok && pair.RequiredRelation != "" && decision.Relation != pair.RequiredRelation {
			errs = append(errs, validationIssue(path+".relation", "equivalence_required_relation", "pair %q contradicts deterministic context or manager rule: must equal required_relation %q; received %q", decision.Ref, pair.RequiredRelation, decision.Relation))
		}
		choices = append(choices, choice)
	}
	coverageErrors := assessor.ValidateEvidenceEquivalenceResults(groups, choices)
	if len(coverageErrors) > 0 {
		received, _ := json.Marshal(response.Equivalence)
		for _, issue := range coverageErrors {
			field := strings.ReplaceAll(strings.ReplaceAll(issue.Field, "evidence_equivalence_results", "equivalence"), ".evidence_id", ".ref")
			errs = append(errs, validationIssue(field, "equivalence_coverage", "%s; return exactly one decision per supplied pair ref %q, including distinct and ambiguous pairs; duplicate pair refs are not allowed; received %s", issue.Message, pairRefs, received))
		}
	} else {
	transitivity:
		for _, a := range request.Items {
			for _, b := range request.Items {
				for _, c := range request.Items {
					if a.Ref != c.Ref && equivalent[pairKey(a.Ref, b.Ref)] && equivalent[pairKey(b.Ref, c.Ref)] && !equivalent[pairKey(a.Ref, c.Ref)] {
						errs = append(errs, validationIssue("equivalence", "equivalence_transitivity", "equivalence is not transitive: items %q and %q, and %q and %q, are equivalent but %q and %q are not", a.Ref, b.Ref, b.Ref, c.Ref, a.Ref, c.Ref))
						break transitivity
					}
				}
			}
		}
	}
	return validationResult(errs, cause)
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

func ResponseSchema(requestID string) map[string]any {
	text := map[string]any{"type": "string"}
	ref := map[string]any{"type": "string", "maxLength": 64}
	definition := closedObject(map[string]any{"ref": ref, "kind": map[string]any{"type": "string", "enum": []string{"entity_class", "predicate_concept", "topic"}}, "key": map[string]any{"type": "string", "minLength": 1, "maxLength": 128}, "label": map[string]any{"type": "string", "maxLength": 128}, "description": map[string]any{"type": "string", "maxLength": 1000}, "aliases": map[string]any{"type": "array", "maxItems": 20, "items": map[string]any{"type": "string", "maxLength": 128}}, "parent_ref": ref, "base_entity_kind": text})
	item := closedObject(map[string]any{"ref": ref, "status": map[string]any{"type": "string", "enum": []string{"classified", "ambiguous"}}, "definition_ref": ref, "reason": map[string]any{"type": "string", "maxLength": 128}})
	pair := closedObject(map[string]any{"ref": ref, "relation": map[string]any{"type": "string", "enum": []string{"equivalent", "distinct", "ambiguous"}}})
	return closedObject(map[string]any{"request_id": map[string]any{"type": "string", "enum": []string{requestID}}, "definitions": map[string]any{"type": "array", "maxItems": 20, "items": definition}, "items": map[string]any{"type": "array", "maxItems": 20, "items": item}, "equivalence": map[string]any{"type": "array", "maxItems": 190, "items": pair}})
}

func closedObject(properties map[string]any) map[string]any {
	required := []string{}
	for key := range properties {
		required = append(required, key)
	}
	sort.Strings(required)
	return map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
}
