package contract

import "fmt"

func ApplicableOverrides(record Record, catalog map[string]Record) []Record {
	var overrides []Record
	if record.Kind == OverrideKind {
		return overrides
	}
	members := map[string]bool{}
	for _, source := range RequiredSources(record) {
		members[SourceKey(source)] = true
	}
	for _, candidate := range catalog {
		if candidate.Retired || candidate.Override == nil {
			continue
		}
		switch candidate.Override.Action {
		case PinDefinition:
			if record.Definition == nil {
				continue
			}
		case SetClassification:
			if record.Assignment == nil {
				continue
			}
		case GroupTogether, KeepSeparate:
			if record.Group == nil || len(candidate.Override.Members) == 0 ||
				(record.Kind == EvidenceGroup && candidate.Override.Members[0].Kind != EvidenceSource) ||
				(record.Kind == RelationshipGroup && candidate.Override.Members[0].Kind != RelationshipSource) {
				continue
			}
		default:
			continue
		}
		applies := candidate.Override.TargetID == record.ID
		for _, member := range candidate.Override.Members {
			applies = applies || members[SourceKey(member)]
		}
		if applies {
			overrides = append(overrides, candidate)
		}
	}
	return overrides
}

func CheckOverrides(record Record, catalog map[string]Record) error {
	for _, overrideRecord := range ApplicableOverrides(record, catalog) {
		override := overrideRecord.Override
		switch override.Action {
		case PinDefinition:
			if record.Definition != nil {
				return fmt.Errorf("%w: definition is pinned", ErrOverride)
			}
		case SetClassification:
			if record.Assignment != nil && (record.Retired || record.Assignment.DefinitionID != override.DefinitionID) {
				return fmt.Errorf("%w: classification is pinned", ErrOverride)
			}
		case GroupTogether, KeepSeparate:
			if record.Group == nil {
				continue
			}
			members := map[string]bool{}
			for _, source := range record.Group.Members {
				members[SourceKey(source)] = true
			}
			matched := 0
			for _, source := range override.Members {
				if members[SourceKey(source)] {
					matched++
				}
			}
			if override.Action == KeepSeparate && !record.Retired && matched > 1 {
				return fmt.Errorf("%w: manager separation rule", ErrOverride)
			}
			if override.Action == GroupTogether && matched > 0 && (record.Retired || matched != len(override.Members)) {
				return fmt.Errorf("%w: manager grouping rule", ErrOverride)
			}
		}
	}
	return nil
}
