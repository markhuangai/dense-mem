package evalharness

import (
	"errors"

	"github.com/google/uuid"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

func ValidateMappings(corpus []CorpusItem, mapping KnowledgeMapping, canonical map[string]CanonicalEvidence) error {
	if len(mapping.Partitions) != len(corpus) {
		return errors.New("source mapping denominator is incomplete")
	}
	bySource := make(map[string]CorpusItem, len(corpus))
	for _, item := range corpus {
		if _, exists := bySource[item.SourceDocID]; exists || item.SourceDocID == "" {
			return errors.New("source identity is empty or duplicated")
		}
		bySource[item.SourceDocID] = item
	}
	occurrences, submitted, referenced := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for source, item := range bySource {
		parts := mapping.Partitions[source]
		if len(parts) == 0 {
			return errors.New("source has no complete partition mapping")
		}
		runes, start, sourceItem := []rune(item.Content), 0, ""
		for index, part := range parts {
			for _, id := range []string{part.EvidenceID, part.OccurrenceID, part.SourceItemID} {
				parsed, err := uuid.Parse(id)
				if err != nil || parsed == uuid.Nil {
					return errors.New("invalid partition identity")
				}
			}
			if part.PartitionIndex != index || part.SpanStart != start || part.SpanEnd <= start || part.SpanEnd > len(runes) || occurrences[part.OccurrenceID] {
				return errors.New("partition spans or occurrence identities are inconsistent")
			}
			if index == 0 {
				if submitted[part.SourceItemID] {
					return errors.New("submitted item identity crosses original sources")
				}
				sourceItem, submitted[part.SourceItemID] = part.SourceItemID, true
			}
			if part.SourceItemID != sourceItem || part.ContentHash != knowledge.EvidenceContentHash(string(runes[start:part.SpanEnd])) {
				return errors.New("partition receipt differs from the exact published source")
			}
			if _, exists := canonical[part.EvidenceID]; !exists {
				return errors.New("partition maps to missing canonical Evidence")
			}
			occurrences[part.OccurrenceID], referenced[part.EvidenceID], start = true, true, part.SpanEnd
		}
		if start != len(runes) {
			return errors.New("partition mapping omits published source text")
		}
	}
	for id, evidence := range canonical {
		item, exists := bySource[evidence.SourceID]
		if !exists || !referenced[id] || evidence.Content == "" {
			return errors.New("canonical Evidence has no mapped original source")
		}
		exact := false
		for _, part := range mapping.Partitions[evidence.SourceID] {
			if part.EvidenceID == id && string([]rune(item.Content)[part.SpanStart:part.SpanEnd]) == evidence.Content {
				exact = true
			}
		}
		if !exact {
			return errors.New("canonical Evidence is not an exact original source partition")
		}
	}
	return nil
}
