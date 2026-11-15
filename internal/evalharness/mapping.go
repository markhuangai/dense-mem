package evalharness

import (
	"strings"
)

func newKnowledgeMapping() KnowledgeMapping {
	return KnowledgeMapping{
		BySourceDocID:        map[string]Ref{},
		BySourceDocIDAndType: map[string]map[string][]Ref{},
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
	for id, parts := range src.Partitions {
		if dst.Partitions == nil {
			dst.Partitions = map[string][]ImportedPartition{}
		}
		dst.Partitions[id] = append([]ImportedPartition(nil), parts...)
	}
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

}
