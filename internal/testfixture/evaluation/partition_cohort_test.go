package evaluation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/stretchr/testify/require"
)

func TestPartitionFixtureExactTextAndOffsets(t *testing.T) {
	for _, example := range PartitionCases() {
		t.Run(example.Name, func(t *testing.T) {
			request := assessor.PreparePartitionRequest("fixture", []string{example.Text})
			item := request.Items[0]
			expected := []string{example.Text}
			if example.Parts == 2 {
				expected = strings.SplitAfter(example.Text, "\n\n")
			}
			ends := []string{}
			offset := 0
			for _, part := range expected {
				offset += len([]rune(part))
				found := ""
				for ref, boundary := range item.Refs {
					if boundary == offset {
						found = ref
					}
				}
				require.NotEmpty(t, found, "expected exact fixture boundary must be available")
				ends = append(ends, found)
			}
			encoded, err := json.Marshal(assessor.PartitionResponse{RequestID: request.RequestID, Items: []assessor.PartitionResult{{ItemID: item.ItemID, EndRefs: ends}}})
			require.NoError(t, err)
			spans, err := assessor.ValidatePartitionResponse(request, encoded)
			require.NoError(t, err)
			require.Len(t, spans[0], example.Parts)
			parts := []string{}
			for _, span := range spans[0] {
				parts = append(parts, string([]rune(example.Text)[span[0]:span[1]]))
			}
			require.Equal(t, expected, parts)
			require.Equal(t, example.Text, strings.Join(parts, ""))
		})
	}
}
