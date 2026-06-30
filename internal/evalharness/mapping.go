package evalharness

import (
	"sort"
	"strings"
)

func newKnowledgeMapping() KnowledgeMapping {
	return KnowledgeMapping{
		BySourceDocID:        map[string]Ref{},
		BySourceDocIDAndType: map[string]map[string][]Ref{},
		DreamSourceRefsByID:  map[string][]Ref{},
	}
}

func addSourceMapping(mapping *KnowledgeMapping, ref Ref, defaultForSource bool) {
	sourceDocID := strings.TrimSpace(ref.SourceDocID)
	refType := strings.TrimSpace(ref.Type)
	refID := strings.TrimSpace(ref.ID)
	if sourceDocID == "" || refType == "" || refID == "" {
		return
	}
	if mapping.BySourceDocID == nil {
		mapping.BySourceDocID = map[string]Ref{}
	}
	if mapping.BySourceDocIDAndType == nil {
		mapping.BySourceDocIDAndType = map[string]map[string][]Ref{}
	}
	ref.SourceDocID = sourceDocID
	ref.Type = refType
	ref.ID = refID
	if mapping.BySourceDocIDAndType[sourceDocID] == nil {
		mapping.BySourceDocIDAndType[sourceDocID] = map[string][]Ref{}
	}
	if !hasMappedRef(mapping.BySourceDocIDAndType[sourceDocID][refType], ref) {
		mapping.BySourceDocIDAndType[sourceDocID][refType] = append(mapping.BySourceDocIDAndType[sourceDocID][refType], ref)
	}
	if defaultForSource || mapping.BySourceDocID[sourceDocID].ID == "" {
		mapping.BySourceDocID[sourceDocID] = ref
	}
}

func addDreamSourceRefs(mapping *KnowledgeMapping, dreamID string, refs []Ref) {
	dreamID = strings.TrimSpace(dreamID)
	if dreamID == "" || len(refs) == 0 {
		return
	}
	if mapping.DreamSourceRefsByID == nil {
		mapping.DreamSourceRefsByID = map[string][]Ref{}
	}
	out := make([]Ref, 0, len(refs))
	for _, ref := range refs {
		ref.Type = strings.TrimSpace(ref.Type)
		ref.ID = strings.TrimSpace(ref.ID)
		if ref.Type == "" || ref.ID == "" {
			continue
		}
		out = append(out, Ref{Type: ref.Type, ID: ref.ID})
	}
	if len(out) > 0 {
		mapping.DreamSourceRefsByID[dreamID] = out
	}
}

func hasMappedRef(refs []Ref, want Ref) bool {
	for _, ref := range refs {
		if strings.TrimSpace(ref.Type) == strings.TrimSpace(want.Type) &&
			strings.TrimSpace(ref.ID) == strings.TrimSpace(want.ID) {
			return true
		}
	}
	return false
}

func mergeKnowledgeMapping(dst *KnowledgeMapping, src KnowledgeMapping) {
	for _, ref := range src.BySourceDocID {
		addSourceMapping(dst, ref, true)
	}
	for sourceDocID, byType := range src.BySourceDocIDAndType {
		for refType, refs := range byType {
			for _, ref := range refs {
				if ref.SourceDocID == "" {
					ref.SourceDocID = sourceDocID
				}
				if ref.Type == "" {
					ref.Type = refType
				}
				addSourceMapping(dst, ref, false)
			}
		}
	}
	for dreamID, refs := range src.DreamSourceRefsByID {
		addDreamSourceRefs(dst, dreamID, refs)
	}
}

func resolveSourceMapping(mapping KnowledgeMapping, sourceDocID, refType string) (Ref, bool) {
	sourceDocID = strings.TrimSpace(sourceDocID)
	refType = strings.TrimSpace(refType)
	if refType == "source_doc" {
		refType = ""
	}
	if sourceDocID == "" {
		return Ref{}, false
	}
	if refType != "" && mapping.BySourceDocIDAndType != nil {
		if byType := mapping.BySourceDocIDAndType[sourceDocID]; byType != nil {
			refs := byType[refType]
			switch len(refs) {
			case 0:
				return Ref{}, false
			case 1:
				resolved := refs[0]
				if strings.TrimSpace(resolved.ID) != "" {
					return resolved, true
				}
			}
			return Ref{}, false
		}
	}
	resolved, ok := mapping.BySourceDocID[sourceDocID]
	if !ok || strings.TrimSpace(resolved.ID) == "" {
		return Ref{}, false
	}
	if refType != "" && strings.TrimSpace(resolved.Type) != "" && resolved.Type != refType {
		return Ref{}, false
	}
	if resolved.Type == "" {
		resolved.Type = refType
	}
	return resolved, true
}

func mapExpectedDreams(mapping *KnowledgeMapping, expected []ExpectedDream) {
	if mapping == nil || len(expected) == 0 || len(mapping.DreamSourceRefsByID) == 0 {
		return
	}
	dreamIDs := make([]string, 0, len(mapping.DreamSourceRefsByID))
	for dreamID := range mapping.DreamSourceRefsByID {
		dreamIDs = append(dreamIDs, dreamID)
	}
	sort.Strings(dreamIDs)
	for _, dream := range expected {
		sourceDocID := strings.TrimSpace(dream.SourceDocID)
		if sourceDocID == "" {
			continue
		}
		want, ok := resolveExpectedDreamSourceRefs(dream.SourceRefs, *mapping)
		if !ok {
			continue
		}
		for _, dreamID := range dreamIDs {
			got := mapping.DreamSourceRefsByID[dreamID]
			if sameRefSet(want, got) {
				addSourceMapping(mapping, Ref{Type: "dream", ID: dreamID, SourceDocID: sourceDocID}, false)
				break
			}
		}
	}
}

func resolveExpectedDreamSourceRefs(refs []Ref, mapping KnowledgeMapping) ([]Ref, bool) {
	out := make([]Ref, 0, len(refs))
	for _, ref := range refs {
		resolved, ok := resolveRef(ref, mapping)
		if !ok {
			return nil, false
		}
		out = append(out, Ref{Type: resolved.Type, ID: resolved.ID})
	}
	return out, len(out) > 0
}

func sameRefSet(a, b []Ref) bool {
	if len(a) != len(b) {
		return false
	}
	remaining := map[string]int{}
	for _, ref := range a {
		remaining[mappingRefKey(ref)]++
	}
	for _, ref := range b {
		key := mappingRefKey(ref)
		if remaining[key] == 0 {
			return false
		}
		remaining[key]--
	}
	return true
}

func mappingRefKey(ref Ref) string {
	return strings.TrimSpace(ref.Type) + "\x00" + strings.TrimSpace(ref.ID)
}
