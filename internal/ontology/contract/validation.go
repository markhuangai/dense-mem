package contract

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
)

func DecodePublication(reader io.Reader) (Publication, error) {
	var input Publication
	if err := jsonstrict.Decode(reader, &input, MaxPublicationBytes); err != nil {
		return input, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return input, ValidatePublication(input)
}

func ValidatePublication(input Publication) error {
	encoded, err := json.Marshal(input)
	if err != nil || len(encoded) > MaxPublicationBytes {
		return fmt.Errorf("%w: publication byte bound", ErrInvalid)
	}
	if err := ValidateOperation(input.OperationKey, input.ExpectedRevision, input.Reason); err != nil {
		return err
	}
	if len(input.Changes) == 0 || len(input.Changes) > MaxChanges {
		return fmt.Errorf("%w: publication must contain 1..%d changes", ErrInvalid, MaxChanges)
	}
	if input.RollbackOf != "" && !validID(input.RollbackOf) {
		return fmt.Errorf("%w: rollback publication ID", ErrInvalid)
	}
	seen := map[string]bool{}
	dependencies := 0
	for _, change := range input.Changes {
		dependencies += len(change.Record.Sources) + len(change.Record.Dependencies)
		if dependencies > MaxPublicationDependencies {
			return fmt.Errorf("%w: publication dependency bound", ErrInvalid)
		}
		if change.ExpectedVersion < 0 || seen[change.Record.ID] {
			return fmt.Errorf("%w: duplicate record or invalid expected version", ErrInvalid)
		}
		seen[change.Record.ID] = true
		if err := ValidateRecord(change.Record); err != nil {
			return err
		}
	}
	return nil
}

func ValidateOperation(key string, revision int64, reason string) error {
	if !boundedText(key, 128) || revision < 0 || !boundedText(reason, 512) {
		return fmt.Errorf("%w: operation key, expected revision and reason are required", ErrInvalid)
	}
	return nil
}

func ValidateRecord(record Record) error {
	if !validID(record.ID) || record.Version < 0 || len(record.Sources) > MaxMembers || len(record.Dependencies) > MaxMembers {
		return fmt.Errorf("%w: record identity, version or dependency bounds", ErrInvalid)
	}
	bodies := 0
	for _, present := range []bool{record.Definition != nil, record.Assignment != nil, record.Group != nil, record.Override != nil} {
		if present {
			bodies++
		}
	}
	if bodies != 1 {
		return fmt.Errorf("%w: exactly one typed record body is required", ErrInvalid)
	}
	var err error
	switch record.Kind {
	case EntityClass, PredicateConcept, Topic:
		if record.Definition == nil {
			return fmt.Errorf("%w: definition body required", ErrInvalid)
		}
		err = validateDefinition(record.Kind, *record.Definition)
	case AssignmentKind:
		if record.Assignment == nil || !validID(record.Assignment.DefinitionID) {
			return fmt.Errorf("%w: assignment body required", ErrInvalid)
		}
		err = ValidateSourceHandle(record.Assignment.Source)
	case EvidenceGroup, RelationshipGroup:
		if record.Group == nil {
			return fmt.Errorf("%w: group body required", ErrInvalid)
		}
		err = validateMembers(record.Group.Members, 2)
		if record.Group.AssessmentID != "" && !validID(record.Group.AssessmentID) {
			return fmt.Errorf("%w: group assessment ID", ErrInvalid)
		}
	case OverrideKind:
		if record.Override == nil {
			return fmt.Errorf("%w: override body required", ErrInvalid)
		}
		err = validateOverride(*record.Override)
	default:
		return fmt.Errorf("%w: unknown record kind", ErrInvalid)
	}
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, source := range record.Sources {
		if err := ValidateSourceHandle(source.SourceHandle); err != nil {
			return err
		}
		if !validFingerprint(source.Fingerprint) || seen[SourceKey(source.SourceHandle)] {
			return fmt.Errorf("%w: source fingerprint or duplicate dependency", ErrInvalid)
		}
		seen[SourceKey(source.SourceHandle)] = true
	}
	seen = map[string]bool{}
	for _, ref := range record.Dependencies {
		if !validID(ref.ID) || ref.Version < 1 || ref.ID == record.ID || seen[ref.ID] {
			return fmt.Errorf("%w: record revision dependency", ErrInvalid)
		}
		seen[ref.ID] = true
	}
	return nil
}

func validateDefinition(kind Kind, definition Definition) error {
	if !boundedText(definition.Key, 128) || !boundedText(definition.Label, 128) || utf8.RuneCountInString(definition.Description) > 1000 || len(definition.Aliases) > MaxAliases {
		return fmt.Errorf("%w: definition text or alias bounds", ErrInvalid)
	}
	if definition.ParentID != "" && !validID(definition.ParentID) {
		return fmt.Errorf("%w: parent ID", ErrInvalid)
	}
	if kind == EntityClass {
		if !slices.Contains(domain.EntityKinds(), definition.BaseEntityKind) {
			return fmt.Errorf("%w: class requires an existing entity kind", ErrInvalid)
		}
	} else if definition.BaseEntityKind != "" {
		return fmt.Errorf("%w: only classes carry an entity kind", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, alias := range definition.Aliases {
		name := NormalizeName(alias)
		if !boundedText(alias, 128) || seen[name] {
			return fmt.Errorf("%w: empty or duplicate alias", ErrInvalid)
		}
		seen[name] = true
	}
	return nil
}

func validateOverride(override Override) error {
	switch override.Action {
	case PinDefinition:
		if !validID(override.TargetID) || override.DefinitionID != "" || len(override.Members) != 0 {
			return fmt.Errorf("%w: definition pin shape", ErrInvalid)
		}
	case SetClassification:
		if override.TargetID != "" || !validID(override.DefinitionID) || len(override.Members) != 1 {
			return fmt.Errorf("%w: classification override shape", ErrInvalid)
		}
		return validateMembers(override.Members, 1)
	case GroupTogether, KeepSeparate:
		if override.TargetID != "" || override.DefinitionID != "" {
			return fmt.Errorf("%w: grouping override shape", ErrInvalid)
		}
		return validateMembers(override.Members, 2)
	default:
		return fmt.Errorf("%w: unknown override action", ErrInvalid)
	}
	return nil
}

func validateMembers(members []SourceHandle, minimum int) error {
	if len(members) < minimum || len(members) > MaxMembers {
		return fmt.Errorf("%w: member bounds", ErrInvalid)
	}
	seen := map[string]bool{}
	for _, member := range members {
		if err := ValidateSourceHandle(member); err != nil {
			return err
		}
		if seen[SourceKey(member)] {
			return fmt.Errorf("%w: duplicate member", ErrInvalid)
		}
		seen[SourceKey(member)] = true
	}
	return nil
}

func ValidateSourceHandle(handle SourceHandle) error {
	if handle.Version < 1 {
		return fmt.Errorf("%w: source version required", ErrInvalid)
	}
	switch handle.Kind {
	case PredicateSource:
		if !boundedText(handle.ID, 128) {
			return fmt.Errorf("%w: predicate key required", ErrInvalid)
		}
	case EntitySource, EvidenceSource, RelationshipSource:
		if !validID(handle.ID) {
			return fmt.Errorf("%w: source UUID required", ErrInvalid)
		}
	default:
		return fmt.Errorf("%w: unknown source kind", ErrInvalid)
	}
	return nil
}

func NormalizeName(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}
func boundedText(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && utf8.RuneCountInString(value) <= limit && !strings.ContainsRune(value, 0)
}
func validFingerprint(value string) bool {
	return len(value) == 71 && strings.HasPrefix(value, "sha256:") && strings.Trim(value[7:], "0123456789abcdef") == ""
}
