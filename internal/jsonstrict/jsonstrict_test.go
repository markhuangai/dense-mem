package jsonstrict

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDecodeRejectsDuplicateNestedFields(t *testing.T) {
	var destination struct {
		Outer struct {
			Value string `json:"value"`
		} `json:"outer"`
	}
	err := Decode(strings.NewReader(`{"outer":{"value":"first","value":"second"}}`), &destination)
	require.ErrorContains(t, err, `duplicate JSON field "value"`)
}

func TestDecodeRejectsUnknownAndTrailingValues(t *testing.T) {
	var destination struct {
		Value string `json:"value"`
	}
	require.Error(t, Decode(strings.NewReader(`{"value":"ok","extra":true}`), &destination))
	require.Error(t, Decode(strings.NewReader(`{"value":"ok"} {"value":"other"}`), &destination))
}

func TestRejectDuplicateFieldsAcceptsNestedArrays(t *testing.T) {
	require.NoError(t, RejectDuplicateFields([]byte(`{"items":[{"value":1},{"value":2}]}`)))
}

func TestDecodeAcceptsOneClosedSchemaValueAndPropagatesReadErrors(t *testing.T) {
	var destination struct {
		Value string `json:"value"`
	}
	require.NoError(t, Decode(strings.NewReader(`{"value":"ok"}`), &destination))
	require.Equal(t, "ok", destination.Value)
	require.ErrorContains(t, Decode(jsonStrictErrorReader{}, &destination), "read failed")
}

func TestRejectDuplicateFieldsRejectsMalformedAndTrailingValues(t *testing.T) {
	for _, raw := range []string{
		`{"value":`,
		`{"value":1} {"value":2}`,
		`[1,`,
	} {
		require.Error(t, RejectDuplicateFields([]byte(raw)))
	}
	require.NoError(t, RejectDuplicateFields([]byte(`null`)))
}

type jsonStrictErrorReader struct{}

func (jsonStrictErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}
