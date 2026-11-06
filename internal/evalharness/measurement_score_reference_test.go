package evalharness

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMeasurementScoreReference(t *testing.T) {
	var examples []struct {
		ID       string             `json:"id"`
		K        int                `json:"k"`
		QRel     QRel               `json:"qrel"`
		Trace    RecallTrace        `json:"trace"`
		Expected map[string]float64 `json:"expected"`
	}
	require.NoError(t, readJSONFile("../../tests/eval/fixtures/measurement_score_cases.json", &examples))
	scores := map[string]map[string]float64{}
	for _, example := range examples {
		score := ScoreTrace(example.ID, example.K, example.QRel, example.Trace, newKnowledgeMapping())
		got := map[string]float64{"recall_at_k": score.RecallAtK, "mrr": score.MRR, "ndcg_at_k": score.NDCGAtK}
		for metric, expected := range example.Expected {
			require.InDelta(t, expected, got[metric], 1e-12, "%s/%s", example.ID, metric)
		}
		scores[example.ID] = got
	}
	encoded, err := json.Marshal(scores)
	require.NoError(t, err)
	fmt.Printf("measurement_reference_scores=%s\n", encoded)
}
