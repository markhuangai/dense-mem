package contract

import (
	"fmt"
	"reflect"
	"sort"
)

func PreparePublication(current map[string]Record, snapshots map[string]SourceSnapshot, input Publication, automatic bool) ([]Record, error) {
	if err := ValidatePublication(input); err != nil {
		return nil, err
	}
	catalog := make(map[string]Record, len(current)+len(input.Changes))
	for id, record := range current {
		catalog[id] = record
	}
	for _, change := range input.Changes {
		previous, exists := current[change.Record.ID]
		if !exists && change.Record.Retired {
			return nil, fmt.Errorf("%w: cannot retire a nonexistent record", ErrInvalid)
		}
		if change.Record.Retired {
			comparison := previous
			comparison.Retired = true
			comparison.Version = change.Record.Version
			comparison.Fingerprint = change.Record.Fingerprint
			if !reflect.DeepEqual(comparison, change.Record) {
				return nil, fmt.Errorf("%w: retirement must retain accepted record contents", ErrInvalid)
			}
		}
		if (exists && (previous.Version != change.ExpectedVersion || previous.Kind != change.Record.Kind)) || (!exists && change.ExpectedVersion != 0) {
			return nil, fmt.Errorf("%w: record expected version or kind changed", ErrConflict)
		}
		if automatic && change.Record.Kind == OverrideKind {
			return nil, ErrOverride
		}
		record := change.Record
		record.Version = change.ExpectedVersion + 1
		catalog[record.ID] = record
	}
	if err := ValidateDefinitions(catalog); err != nil {
		return nil, err
	}
	prepared := make([]Record, 0, len(input.Changes))
	for _, change := range input.Changes {
		record := catalog[change.Record.ID]
		if err := validateRecordReferences(record, catalog); err != nil {
			return nil, err
		}
		if err := validateRecordSources(record, snapshots, catalog); err != nil {
			return nil, err
		}
		if automatic {
			if err := CheckOverrides(record, current); err != nil {
				return nil, err
			}
		}
		for _, ref := range record.Dependencies {
			if record.Retired {
				continue
			}
			if dependency, ok := catalog[ref.ID]; !ok || dependency.Retired || dependency.Version != ref.Version {
				return nil, fmt.Errorf("%w: referenced revision changed", ErrConflict)
			}
		}
		if !record.Retired {
			record.Dependencies = dependenciesFor(record, catalog)
		}
		fingerprint, err := RecordFingerprint(record, snapshots, catalog)
		if err != nil {
			return nil, err
		}
		record.Fingerprint = fingerprint
		catalog[record.ID] = record
		prepared = append(prepared, record)
	}
	if err := CheckDependencies(prepared, snapshots, catalog); err != nil {
		return nil, err
	}
	enriched := input
	enriched.Changes = make([]Change, len(prepared))
	for i, record := range prepared {
		enriched.Changes[i] = Change{ExpectedVersion: input.Changes[i].ExpectedVersion, Record: record}
	}
	if err := ValidatePublication(enriched); err != nil {
		return nil, err
	}
	return prepared, nil
}

func ValidateDefinitions(catalog map[string]Record) error {
	names := map[string]string{}
	for _, record := range catalog {
		if record.Retired || record.Definition == nil {
			continue
		}
		for _, name := range DefinitionNames(record) {
			key := string(record.Kind) + "\x00" + name
			if previous, exists := names[key]; exists && previous != record.ID {
				return fmt.Errorf("%w: definition name or alias collision", ErrInvalid)
			}
			names[key] = record.ID
		}
		seen := map[string]bool{record.ID: true}
		parentID := record.Definition.ParentID
		for depth := 0; parentID != ""; depth++ {
			parent, exists := catalog[parentID]
			if depth >= MaxParentDepth || seen[parentID] || !exists || parent.Retired || parent.Definition == nil || parent.Kind != record.Kind {
				return fmt.Errorf("%w: missing parent, hierarchy cycle or depth bound", ErrInvalid)
			}
			if record.Kind == EntityClass && parent.Definition.BaseEntityKind != record.Definition.BaseEntityKind {
				return fmt.Errorf("%w: parent class has a different entity kind", ErrInvalid)
			}
			seen[parentID] = true
			parentID = parent.Definition.ParentID
		}
	}
	return nil
}

func DefinitionNames(record Record) []string {
	if record.Definition == nil || record.Retired {
		return nil
	}
	seen := map[string]bool{}
	for _, value := range append([]string{record.Definition.Key, record.Definition.Label}, record.Definition.Aliases...) {
		seen[NormalizeName(value)] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func RequiredSources(record Record) []SourceHandle {
	var sources []SourceHandle
	if record.Assignment != nil {
		sources = append(sources, record.Assignment.Source)
	}
	if record.Group != nil {
		sources = append(sources, record.Group.Members...)
	}
	if record.Override != nil {
		sources = append(sources, record.Override.Members...)
	}
	return sources
}

func ReferenceIDs(record Record) []string {
	seen := map[string]bool{}
	if record.Definition != nil && record.Definition.ParentID != "" {
		seen[record.Definition.ParentID] = true
	}
	if record.Assignment != nil {
		seen[record.Assignment.DefinitionID] = true
	}
	if record.Override != nil {
		if record.Override.TargetID != "" {
			seen[record.Override.TargetID] = true
		}
		if record.Override.DefinitionID != "" {
			seen[record.Override.DefinitionID] = true
		}
	}
	for _, ref := range record.Dependencies {
		seen[ref.ID] = true
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func validateRecordReferences(record Record, catalog map[string]Record) error {
	if record.Retired {
		return nil
	}
	for _, id := range ReferenceIDs(record) {
		reference, exists := catalog[id]
		if !exists || reference.Retired {
			return fmt.Errorf("%w: reference is missing or retired", ErrInvalid)
		}
	}
	if record.Assignment != nil {
		return compatibleAssignment(record.Assignment.Source, catalog[record.Assignment.DefinitionID])
	}
	if record.Override != nil {
		switch record.Override.Action {
		case PinDefinition:
			if catalog[record.Override.TargetID].Definition == nil {
				return fmt.Errorf("%w: pin target must be a definition", ErrInvalid)
			}
		case SetClassification:
			return compatibleAssignment(record.Override.Members[0], catalog[record.Override.DefinitionID])
		}
	}
	return nil
}

func compatibleAssignment(source SourceHandle, definition Record) error {
	if definition.Definition == nil {
		return fmt.Errorf("%w: assignment target must be a definition", ErrInvalid)
	}
	valid := (definition.Kind == EntityClass && source.Kind == EntitySource) || (definition.Kind == PredicateConcept && source.Kind == PredicateSource) ||
		(definition.Kind == Topic && (source.Kind == EvidenceSource || source.Kind == RelationshipSource))
	if !valid {
		return fmt.Errorf("%w: incompatible assignment source kind", ErrInvalid)
	}
	return nil
}

func validateRecordSources(record Record, snapshots map[string]SourceSnapshot, catalog map[string]Record) error {
	if record.Retired {
		return nil
	}
	declared := map[string]SourceHandle{}
	for _, dependency := range record.Sources {
		snapshot, exists := snapshots[SourceKey(dependency.SourceHandle)]
		if !exists || !snapshot.Eligible || snapshot.SourceHandle != dependency.SourceHandle {
			return ErrSourceStale
		}
		fingerprint, err := SourceFingerprint(snapshot)
		if err != nil {
			return err
		}
		if fingerprint != dependency.Fingerprint {
			return ErrSourceStale
		}
		declared[SourceKey(dependency.SourceHandle)] = dependency.SourceHandle
	}
	for _, source := range RequiredSources(record) {
		if declared[SourceKey(source)] != source {
			return fmt.Errorf("%w: required source dependency missing", ErrInvalid)
		}
	}
	if record.Assignment != nil {
		if err := compatibleClass(record.Assignment.Source, catalog[record.Assignment.DefinitionID], snapshots); err != nil {
			return err
		}
	}
	if record.Override != nil && record.Override.Action == SetClassification {
		if err := compatibleClass(record.Override.Members[0], catalog[record.Override.DefinitionID], snapshots); err != nil {
			return err
		}
	}
	if record.Group != nil {
		if record.Group.AssessmentID != "" {
			return AssessedGroupCompatible(record, snapshots)
		}
		return GroupCompatible(record.Kind, record.Group.Members, snapshots)
	}
	if record.Override != nil && (record.Override.Action == GroupTogether || record.Override.Action == KeepSeparate) {
		kind := EvidenceGroup
		if record.Override.Members[0].Kind == RelationshipSource {
			kind = RelationshipGroup
		}
		if record.Override.Action == GroupTogether {
			return GroupCompatible(kind, record.Override.Members, snapshots)
		}
		for _, member := range record.Override.Members {
			if member.Kind != EvidenceSource && member.Kind != RelationshipSource {
				return fmt.Errorf("%w: only evidence or Relationships can be separated", ErrInvalid)
			}
		}
	}
	return nil
}

func compatibleClass(source SourceHandle, definition Record, snapshots map[string]SourceSnapshot) error {
	if definition.Kind == EntityClass && snapshots[SourceKey(source)].EntityKind != definition.Definition.BaseEntityKind {
		return fmt.Errorf("%w: entity kind is incompatible with class", ErrInvalid)
	}
	return nil
}

func GroupCompatible(kind Kind, members []SourceHandle, snapshots map[string]SourceSnapshot) error {
	expectedKind := EvidenceSource
	if kind == RelationshipGroup {
		expectedKind = RelationshipSource
	}
	meaning := ""
	for _, member := range members {
		snapshot := snapshots[SourceKey(member)]
		if member.Kind != expectedKind || !snapshot.Eligible || snapshot.MeaningKey == "" {
			return fmt.Errorf("%w: incompatible group member", ErrInvalid)
		}
		if meaning != "" && meaning != snapshot.MeaningKey {
			return fmt.Errorf("%w: group members have distinct semantic context", ErrInvalid)
		}
		meaning = snapshot.MeaningKey
	}
	return nil
}
