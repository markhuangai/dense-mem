package ontology

import (
	"fmt"
	"sort"

	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

type requestBinding struct {
	sources        map[string]contract.SourceSnapshot
	definitions    map[string]contract.Record
	outcomeIndices map[string]int
}

func (s *Service) request(context contract.OrganizationContext, receipt *contract.OrganizationReceipt) (assessment.Request, requestBinding, error) {
	request := assessment.Request{RequestID: receipt.ID, Items: []assessment.Item{}, Definitions: []assessment.Definition{}, Pairs: []assessment.Pair{}}
	binding := requestBinding{sources: map[string]contract.SourceSnapshot{}, definitions: map[string]contract.Record{}, outcomeIndices: map[string]int{}}
	definitionRefs := map[string]string{}
	for i, view := range context.Candidates {
		definitionRefs[view.ID] = fmt.Sprintf("d%d", i)
	}
	for _, view := range context.Candidates {
		d := view.Definition
		ref := definitionRefs[view.ID]
		request.Definitions = append(request.Definitions, assessment.Definition{Ref: ref, Kind: view.Kind, Key: d.Key, Label: d.Label, Description: d.Description, Aliases: append([]string{}, d.Aliases...), ParentRef: definitionRefs[d.ParentID], BaseEntityKind: d.BaseEntityKind})
		binding.definitions[ref] = view.Record
	}
	for len(request.Definitions) > 0 {
		fits, err := s.provider.VocabularyFits(request.Definitions)
		if err != nil {
			return request, binding, err
		}
		if fits {
			break
		}
		last := request.Definitions[len(request.Definitions)-1]
		delete(definitionRefs, binding.definitions[last.Ref].ID)
		delete(binding.definitions, last.Ref)
		request.Definitions = request.Definitions[:len(request.Definitions)-1]
	}
	for i := range request.Definitions {
		if _, retained := binding.definitions[request.Definitions[i].ParentRef]; !retained {
			request.Definitions[i].ParentRef = ""
		}
	}
	keys := map[string]bool{}
	for _, source := range context.Sources {
		keys[contract.SourceKey(source.SourceHandle)] = true
	}
	owners := map[string]string{}
	for i, source := range context.Sources {
		outcome := &receipt.Result.Outcomes[i]
		if !source.Eligible {
			outcome.Status = "unavailable"
			outcome.Reason = "source_unavailable"
			continue
		}
		if incompleteGroup(source, context.Records, keys) {
			outcome.Status = "ambiguous"
			outcome.Reason = "resubmit_complete_group"
			continue
		}
		if owners[source.OwnerID] == "" {
			owners[source.OwnerID] = fmt.Sprintf("owner%d", len(owners))
		}
		ref := fmt.Sprintf("s%d", i)
		item := assessment.Item{Ref: ref, Kind: source.Kind, Text: contract.SourceDisplayText(source), OwnerRef: owners[source.OwnerID], EntityKind: source.EntityKind, Context: contract.SourceDisplayContext(source)}
		locked, required := classificationFor(source, context.Records, context.Candidates)
		item.LockedDefinitionRef = definitionRefs[locked]
		if required && item.LockedDefinitionRef == "" {
			outcome.Status = "ambiguous"
			outcome.Reason = "required_classification_unavailable"
			continue
		}
		request.Items = append(request.Items, item)
		request.Pairs = organizationPairs(request.Items, binding.sources, source, context.Records)
		measurement, err := s.provider.Measure(request)
		if err != nil {
			return request, binding, err
		}
		if measurement > s.provider.MaxInitialInputTokens() {
			request.Items = request.Items[:len(request.Items)-1]
			request.Pairs = organizationPairs(request.Items, binding.sources, contract.SourceSnapshot{}, context.Records)
			outcome.Status = "oversized"
			outcome.Reason = "organization_input_budget"
			continue
		}
		binding.sources[ref] = source
		binding.outcomeIndices[ref] = i
	}
	return request, binding, nil
}

func incompleteGroup(source contract.SourceSnapshot, records []contract.RecordView, keys map[string]bool) bool {
	key := contract.SourceKey(source.SourceHandle)
	for _, view := range records {
		var members []contract.SourceHandle
		if view.Group != nil {
			members = view.Group.Members
		}
		if view.Override != nil && view.Override.Action == contract.GroupTogether {
			members = view.Override.Members
		}
		contains := false
		for _, member := range members {
			contains = contains || contract.SourceKey(member) == key
		}
		if contains {
			for _, member := range members {
				if !keys[contract.SourceKey(member)] {
					return true
				}
			}
		}
	}
	return false
}

func classificationFor(source contract.SourceSnapshot, records, candidates []contract.RecordView) (string, bool) {
	key := contract.SourceKey(source.SourceHandle)
	for _, view := range records {
		if view.Override != nil && view.Override.Action == contract.SetClassification && contract.SourceKey(view.Override.Members[0]) == key {
			return view.Override.DefinitionID, true
		}
	}
	for _, view := range records {
		if view.Current && view.Assignment != nil && contract.SourceKey(view.Assignment.Source) == key {
			return view.Assignment.DefinitionID, true
		}
	}
	name := ""
	if source.Kind == contract.PredicateSource {
		name = contract.NormalizeName(source.ID)
	}
	if source.Kind == contract.EntitySource {
		name = contract.NormalizeName(source.EntityKind)
	}
	if name != "" {
		for _, view := range candidates {
			for _, candidate := range contract.DefinitionNames(view.Record) {
				if candidate == name && ((source.Kind == contract.EntitySource && view.Kind == contract.EntityClass && view.Definition.BaseEntityKind == source.EntityKind) || (source.Kind == contract.PredicateSource && view.Kind == contract.PredicateConcept)) {
					return view.ID, true
				}
			}
		}
	}
	return "", false
}

func organizationPairs(items []assessment.Item, sources map[string]contract.SourceSnapshot, extra contract.SourceSnapshot, records []contract.RecordView) []assessment.Pair {
	lookup := map[string]contract.SourceSnapshot{}
	for ref, source := range sources {
		lookup[ref] = source
	}
	for _, item := range items {
		if _, ok := lookup[item.Ref]; !ok {
			lookup[item.Ref] = extra
		}
	}
	pairs := []assessment.Pair{}
	for i, left := range items {
		for _, right := range items[i+1:] {
			if left.Kind != right.Kind || (left.Kind != contract.EvidenceSource && left.Kind != contract.RelationshipSource) {
				continue
			}
			a, b := lookup[left.Ref], lookup[right.Ref]
			required := ""
			if !contract.CompatibleMeaningContext(a, b) {
				required = "distinct"
			} else if contract.ExactMeaning(a, b) {
				required = "equivalent"
			}
			for _, record := range records {
				members := []contract.SourceHandle{}
				if record.Current && record.Group != nil {
					members = record.Group.Members
				}
				if record.Override != nil && (record.Override.Action == contract.GroupTogether || record.Override.Action == contract.KeepSeparate) {
					members = record.Override.Members
				}
				matched := 0
				for _, member := range members {
					if contract.SourceKey(member) == contract.SourceKey(a.SourceHandle) || contract.SourceKey(member) == contract.SourceKey(b.SourceHandle) {
						matched++
					}
				}
				if matched != 2 {
					continue
				}
				if record.Override != nil && record.Override.Action == contract.KeepSeparate {
					required = "distinct"
					break
				}
				if contract.CompatibleMeaningContext(a, b) {
					required = "equivalent"
				}
			}
			pairs = append(pairs, assessment.Pair{Ref: fmt.Sprintf("p%d", len(pairs)), LeftRef: left.Ref, RightRef: right.Ref, RequiredRelation: required})
		}
	}
	return pairs
}

func sortedRecords(records map[string]contract.Record) []contract.Record {
	ids := []string{}
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := []contract.Record{}
	for _, id := range ids {
		result = append(result, records[id])
	}
	return result
}
