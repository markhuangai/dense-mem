package dreamgeneration

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDreamGenerationResponseSchemaIsClosedAndBounded(t *testing.T) {
	schema := DreamGenerationResponseSchema()
	require.Equal(t, "object", schema["type"])
	require.Equal(t, false, schema["additionalProperties"])
	require.NotEmpty(t, schema["required"])
	require.NotEmpty(t, schema["properties"])
	encoded, err := json.Marshal(schema)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"maxItems":50`)
}
