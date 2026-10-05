package ontology

import (
	"reflect"
	"sort"

	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

func buildPublication(teamID string, context contract.OrganizationContext, request assessment.Request, response assessment.Response, binding requestBinding, receipt *contract.OrganizationReceipt) (contract.Publication, error) {
	publication := contract.Publication{OperationKey: receipt.OperationKey, ExpectedRevision: context.Revision, Reason: "validated existing-memory organization"}
	current := map[string]contract.RecordView{}
	for _, record := range context.Records {
		current[record.ID] = record
	}
	changes := map[string]contract.Record{}
	definitions := map[string]contract.Record{}
	for ref, record := range binding.definitions {
		definitions[ref] = record
	}
	for _, definition := range response.Definitions {
		definitions[definition.Ref] = contract.Record{ID: contract.OrganizationRecordID(teamID, definition.Kind, contract.NormalizeName(definition.Key)), Kind: definition.Kind,
			Definition: &contract.Definition{Key: definition.Key, Label: definition.Label, Description: definition.Description, Aliases: definition.Aliases, BaseEntityKind: definition.BaseEntityKind}}
	}
	for _, definition := range response.Definitions {
		record := definitions[definition.Ref]
		if previous, ok := current[record.ID]; ok && previous.Current && previous.Definition != nil && previous.Kind == record.Kind && contract.NormalizeName(previous.Definition.Key) == contract.NormalizeName(record.Definition.Key) {
			definitions[definition.Ref] = previous.Record
			continue
		}
		if definition.ParentRef != "" {
			record.Definition.ParentID = definitions[definition.ParentRef].ID
		}
		for _, decision := range response.Items {
			if decision.DefinitionRef == definition.Ref {
				record.Sources = append(record.Sources, dependencyFor(binding.sources[decision.Ref], receipt.Sources))
			}
		}
		record.Sources = orderedDependencies(record.Sources)
		changes[record.ID] = record
		definitions[definition.Ref] = record
	}
	add := func(record contract.Record) {
		previous, ok := current[record.ID]
		if ok && previous.Current && sameOrganizationRecord(previous.Record, record) {
			return
		}
		changes[record.ID] = record
	}
	for _, decision := range response.Items {
		outcome := &receipt.Result.Outcomes[binding.outcomeIndices[decision.Ref]]
		if decision.Status == "ambiguous" {
			outcome.Status = "ambiguous"
			outcome.Reason = "classification_ambiguous"
			continue
		}
		source := binding.sources[decision.Ref]
		definition := definitions[decision.DefinitionRef]
		id := contract.OrganizationRecordID(teamID, contract.AssignmentKind, contract.SourceKey(source.SourceHandle))
		for _, view := range context.Records {
			if view.Assignment != nil && contract.SourceKey(view.Assignment.Source) == contract.SourceKey(source.SourceHandle) {
				id = view.ID
				break
			}
		}
		record := contract.Record{ID: id, Kind: contract.AssignmentKind, Assignment: &contract.Assignment{Source: source.SourceHandle, DefinitionID: definition.ID}, Sources: []contract.SourceDependency{dependencyFor(source, receipt.Sources)}}
		add(record)
		outcome.RecordIDs = append(outcome.RecordIDs, id)
		if _, changed := changes[id]; changed {
			outcome.Status = "organized"
		}
	}
	components := equivalenceComponents(request, response)
	pairs := map[string]assessment.Pair{}
	for _, pair := range request.Pairs {
		pairs[pair.Ref] = pair
	}
	for _, decision := range response.Equivalence {
		if decision.Relation == "ambiguous" {
			pair := pairs[decision.Ref]
			left, right := binding.sources[pair.LeftRef].SourceHandle, binding.sources[pair.RightRef].SourceHandle
			if contract.SourceKey(left) > contract.SourceKey(right) {
				left, right = right, left
			}
			receipt.Result.AmbiguousComparisons = append(receipt.Result.AmbiguousComparisons, contract.AmbiguousComparison{Left: left, Right: right, Reason: "equivalence_ambiguous"})
		}
	}
	usedGroups := map[string]bool{}
	for _, component := range components {
		if len(component) < 2 {
			continue
		}
		members := []contract.SourceHandle{}
		sources := []contract.SourceDependency{}
		keys := map[string]bool{}
		for _, ref := range component {
			source := binding.sources[ref]
			members = append(members, source.SourceHandle)
			sources = append(sources, dependencyFor(source, receipt.Sources))
			keys[contract.SourceKey(source.SourceHandle)] = true
		}
		sort.Slice(members, func(i, j int) bool { return contract.SourceKey(members[i]) < contract.SourceKey(members[j]) })
		kind := contract.EvidenceGroup
		if members[0].Kind == contract.RelationshipSource {
			kind = contract.RelationshipGroup
		}
		id := contract.OrganizationRecordID(teamID, kind, contract.SourceKey(members[0]))
		var previousIDs []string
		for _, view := range context.Records {
			if view.Group != nil {
				for _, member := range view.Group.Members {
					if keys[contract.SourceKey(member)] {
						previousIDs = append(previousIDs, view.ID)
						break
					}
				}
			}
		}
		sort.Strings(previousIDs)
		if len(previousIDs) > 0 {
			id = previousIDs[0]
		}
		if usedGroups[id] {
			return publication, contract.ErrConflict
		}
		record := contract.Record{ID: id, Kind: kind, Group: &contract.Group{Members: members, AssessmentID: receipt.ID}, Sources: orderedDependencies(sources)}
		if previous, ok := current[id]; ok && previous.Current && reflect.DeepEqual(previous.Group.Members, members) {
			record.Group.AssessmentID = previous.Group.AssessmentID
		}
		add(record)
		usedGroups[id] = true
		for _, ref := range component {
			outcome := &receipt.Result.Outcomes[binding.outcomeIndices[ref]]
			outcome.RecordIDs = append(outcome.RecordIDs, id)
			if _, changed := changes[id]; changed && outcome.Status != "ambiguous" {
				outcome.Status = "organized"
			}
		}
	}
	admitted := map[string]bool{}
	for _, source := range binding.sources {
		admitted[contract.SourceKey(source.SourceHandle)] = true
	}
	for _, view := range context.Records {
		if view.Group == nil || usedGroups[view.ID] {
			continue
		}
		complete := true
		for _, member := range view.Group.Members {
			complete = complete && admitted[contract.SourceKey(member)]
		}
		if complete {
			retired := view.Record
			retired.Retired = true
			changes[view.ID] = retired
		}
	}
	for _, record := range sortedRecords(changes) {
		expected := int64(0)
		if previous, ok := current[record.ID]; ok {
			expected = previous.Version
		}
		publication.Changes = append(publication.Changes, contract.Change{ExpectedVersion: expected, Record: record})
	}
	if len(publication.Changes) > 0 {
		if err := contract.ValidatePublication(publication); err != nil {
			return publication, err
		}
	}
	return publication, nil
}

func sameOrganizationRecord(left, right contract.Record) bool {
	left.Version = 0
	right.Version = 0
	left.Fingerprint = ""
	right.Fingerprint = ""
	left.Dependencies = nil
	right.Dependencies = nil
	left.Sources = orderedDependencies(left.Sources)
	right.Sources = orderedDependencies(right.Sources)
	return reflect.DeepEqual(left, right)
}

func orderedDependencies(sources []contract.SourceDependency) []contract.SourceDependency {
	copy := append([]contract.SourceDependency(nil), sources...)
	sort.Slice(copy, func(i, j int) bool {
		return contract.SourceKey(copy[i].SourceHandle) < contract.SourceKey(copy[j].SourceHandle)
	})
	return copy
}

func dependencyFor(source contract.SourceSnapshot, dependencies []contract.SourceDependency) contract.SourceDependency {
	for _, dependency := range dependencies {
		if dependency.SourceHandle == source.SourceHandle {
			return dependency
		}
	}
	return contract.SourceDependency{SourceHandle: source.SourceHandle}
}

func equivalenceComponents(request assessment.Request, response assessment.Response) [][]string {
	parent := map[string]string{}
	for _, item := range request.Items {
		parent[item.Ref] = item.Ref
	}
	var root func(string) string
	root = func(ref string) string {
		if parent[ref] != ref {
			parent[ref] = root(parent[ref])
		}
		return parent[ref]
	}
	pairs := map[string]assessment.Pair{}
	for _, pair := range request.Pairs {
		pairs[pair.Ref] = pair
	}
	for _, decision := range response.Equivalence {
		if decision.Relation == "equivalent" {
			pair := pairs[decision.Ref]
			a, b := root(pair.LeftRef), root(pair.RightRef)
			if a > b {
				a, b = b, a
			}
			parent[b] = a
		}
	}
	groups := map[string][]string{}
	for _, item := range request.Items {
		ref := root(item.Ref)
		groups[ref] = append(groups[ref], item.Ref)
	}
	keys := []string{}
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := [][]string{}
	for _, key := range keys {
		sort.Strings(groups[key])
		result = append(result, groups[key])
	}
	return result
}
