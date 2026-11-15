package evaluation

import (
	"fmt"
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
			if len(mapping.Partitions[sourceDocID]) > 0 && (refType == "fragment" || refType == "evidence") {
				resolved, ok := mapping.BySourceDocID[sourceDocID]
				if ok && resolved.ID != "" {
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

func validateRequiredQRelMappings(qrels map[string]QRel, suite []SuiteCase, mapping KnowledgeMapping) error {
	mapping = scoringMapping(mapping)
	for _, suiteCase := range suite {
		qrel, ok := qrels[suiteCase.CaseID]
		if !ok {
			return fmt.Errorf("suite case %q missing from seed qrels", suiteCase.CaseID)
		}
		for _, refs := range []struct {
			label string
			refs  []Ref
		}{
			{"required", qrel.RequiredRefs},
			{"required evidence", qrel.RequiredEvidenceRefs},
			{"bad", qrel.BadRefs},
			{"bad evidence", qrel.BadEvidenceRefs},
		} {
			for _, ref := range refs.refs {
				if strings.TrimSpace(ref.SourceDocID) == "" {
					continue
				}
				ref.Type = scoringRefType(ref.Type)
				if _, ok := resolveRef(ref, mapping); !ok {
					return fmt.Errorf("%s qrel ref for case %q is unmapped after import: type=%s source_doc_id=%s", refs.label, suiteCase.CaseID, ref.Type, ref.SourceDocID)
				}
			}
		}
	}
	return nil
}
