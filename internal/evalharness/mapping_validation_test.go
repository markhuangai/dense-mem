package evalharness

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMappingValidationRejectsReplaySpanHashAndOriginalSourceInflation(t *testing.T) {
	_, _, corpus, original, canonical := scoringFixture()
	rows := []CorpusItem{}
	for _, item := range corpus {
		rows = append(rows, item)
	}
	require.NoError(t, ValidateMappings(rows, original, canonical))
	for name, corrupt := range map[string]func(*KnowledgeMapping, map[string]CanonicalEvidence){
		"hash": func(m *KnowledgeMapping, _ map[string]CanonicalEvidence) {
			m.Partitions["source-a"][0].ContentHash = "invalid"
		},
		"gap":            func(m *KnowledgeMapping, _ map[string]CanonicalEvidence) { m.Partitions["source-a"][1].SpanStart++ },
		"missing source": func(m *KnowledgeMapping, _ map[string]CanonicalEvidence) { delete(m.Partitions, "source-b") },
		"duplicate occurrence": func(m *KnowledgeMapping, _ map[string]CanonicalEvidence) {
			m.Partitions["source-a"][1].OccurrenceID = m.Partitions["source-a"][0].OccurrenceID
		},
		"submitted identity": func(m *KnowledgeMapping, _ map[string]CanonicalEvidence) {
			m.Partitions["source-a"][1].SourceItemID = uuid.NewString()
		},
		"canonical text": func(_ *KnowledgeMapping, c map[string]CanonicalEvidence) {
			value := c[testEvidenceA1]
			value.Content = "different"
			c[testEvidenceA1] = value
		},
		"canonical source": func(_ *KnowledgeMapping, c map[string]CanonicalEvidence) {
			value := c[testEvidenceA1]
			value.SourceID = "source-b"
			c[testEvidenceA1] = value
		},
		"unmapped canonical": func(_ *KnowledgeMapping, c map[string]CanonicalEvidence) { c[uuid.NewString()] = c[testEvidenceA1] },
	} {
		t.Run(name, func(t *testing.T) {
			var mapping KnowledgeMapping
			var copied map[string]CanonicalEvidence
			body, err := json.Marshal(original)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &mapping))
			body, err = json.Marshal(canonical)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(body, &copied))
			corrupt(&mapping, copied)
			require.Error(t, ValidateMappings(rows, mapping, copied))
		})
	}
}
