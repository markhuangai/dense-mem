package jsonstrict

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeAcceptsOneBoundedClosedSchemaValue(t *testing.T) {
	var destination struct {
		Name string `json:"name"`
	}

	require.NoError(t, Decode(strings.NewReader(`{"name":"entra"}`), &destination, 64))
	require.Equal(t, "entra", destination.Name)
}

func TestDecodeRejectsUnknownDuplicateTrailingAndOversizedInput(t *testing.T) {
	for name, input := range map[string]string{
		"unknown":   `{"name":"entra","extra":true}`,
		"duplicate": `{"name":"entra","name":"pingone"}`,
		"trailing":  `{"name":"entra"} {"name":"pingone"}`,
		"oversized": `{"name":"` + strings.Repeat("x", 64) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			var destination struct {
				Name string `json:"name"`
			}
			err := Decode(strings.NewReader(input), &destination, 48)
			require.Error(t, err)
		})
	}
}

func TestDecodePropagatesReadErrorsAndRejectsInvalidLimit(t *testing.T) {
	var destination any
	require.ErrorContains(t, Decode(strictErrorReader{}, &destination, 32), "read failed")
	require.Error(t, Decode(strings.NewReader(`null`), &destination, 0))
}

func TestRejectDuplicateFieldsCoversNestedObjectsAndMalformedJSON(t *testing.T) {
	require.ErrorContains(t, RejectDuplicateFields([]byte(`{"items":[{"value":1,"value":2}]}`)), `duplicate JSON field "value"`)
	require.Error(t, RejectDuplicateFields([]byte(`{"value":`)))
	require.Error(t, RejectDuplicateFields([]byte(`{"value":1} {"value":2}`)))
	require.NoError(t, RejectDuplicateFields([]byte(`{"items":[{"value":1},{"value":2}]}`)))
}

type strictErrorReader struct{}

func (strictErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}
