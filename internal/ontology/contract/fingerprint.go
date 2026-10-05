package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

func SourceKey(source SourceHandle) string { return string(source.Kind) + ":" + source.ID }

func SourceFingerprint(snapshot SourceSnapshot) (string, error) {
	return hashJSON(struct {
		Version string
		Source  SourceSnapshot
	}{FingerprintVersion, snapshot})
}

func RequestHash(input Publication, origin, actorID string) (string, error) {
	return hashJSON(struct {
		Origin  string
		ActorID string
		Input   Publication
	}{origin, actorID, input})
}

func RecordFingerprint(record Record, snapshots map[string]SourceSnapshot, catalog map[string]Record) (string, error) {
	if record.Retired {
		record.Fingerprint = ""
		return hashJSON(struct {
			Version string
			Record  Record
		}{FingerprintVersion, record})
	}
	sources := make([]string, 0, len(record.Sources))
	for _, dependency := range record.Sources {
		snapshot, exists := snapshots[SourceKey(dependency.SourceHandle)]
		if !exists || !snapshot.Eligible || snapshot.SourceHandle != dependency.SourceHandle {
			return "", ErrSourceStale
		}
		fingerprint, err := SourceFingerprint(snapshot)
		if err != nil {
			return "", err
		}
		sources = append(sources, SourceKey(snapshot.SourceHandle)+":"+fingerprint)
	}
	sort.Strings(sources)
	dependencies := dependenciesFor(record, catalog)
	for _, reference := range record.Dependencies {
		current, exists := catalog[reference.ID]
		if !exists || current.Retired {
			return "", ErrSourceStale
		}
	}
	definition := record.Definition
	if definition != nil {
		copy := *definition
		copy.Aliases = append([]string(nil), definition.Aliases...)
		sort.Strings(copy.Aliases)
		definition = &copy
	}
	group := record.Group
	if group != nil {
		copy := *group
		copy.Members = sortedSources(group.Members)
		group = &copy
	}
	override := record.Override
	if override != nil {
		copy := *override
		copy.Members = sortedSources(override.Members)
		override = &copy
	}
	return hashJSON(struct {
		Version      string
		ID           string
		Kind         Kind
		Retired      bool
		Definition   *Definition
		Assignment   *Assignment
		Group        *Group
		Override     *Override
		Sources      []string
		Dependencies []RevisionRef
	}{FingerprintVersion, record.ID, record.Kind, record.Retired, definition, record.Assignment, group, override, sources, dependencies})
}

func dependenciesFor(record Record, catalog map[string]Record) []RevisionRef {
	seen := map[string]bool{}
	pending := ReferenceIDs(record)
	for len(pending) > 0 {
		id := pending[0]
		pending = pending[1:]
		if seen[id] || id == record.ID {
			continue
		}
		seen[id] = true
		if dependency, exists := catalog[id]; exists && dependency.Definition != nil && dependency.Definition.ParentID != "" {
			pending = append(pending, dependency.Definition.ParentID)
		}
	}
	if record.Definition == nil {
		for _, override := range ApplicableOverrides(record, catalog) {
			seen[override.ID] = true
		}
	}
	refs := make([]RevisionRef, 0, len(seen))
	for id := range seen {
		refs = append(refs, RevisionRef{ID: id, Version: catalog[id].Version})
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	return refs
}

func sortedSources(sources []SourceHandle) []SourceHandle {
	result := append([]SourceHandle(nil), sources...)
	sort.Slice(result, func(i, j int) bool {
		left, right := SourceKey(result[i]), SourceKey(result[j])
		if left != right {
			return left < right
		}
		return result[i].Version < result[j].Version
	})
	return result
}

func hashJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("ontology fingerprint: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
