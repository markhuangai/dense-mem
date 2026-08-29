package registry

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolResultErrorCarriesOnlyStructuredResult(t *testing.T) {
	result := map[string]any{"processing_state": "failed"}
	err := NewToolResultError(result)

	structured, ok := ToolResultFromError(err)
	require.True(t, ok)
	require.Equal(t, result, structured.Result)
	require.Equal(t, "tool returned a structured error result", err.Error())

	wrapped := errors.New("unrelated")
	_, ok = ToolResultFromError(wrapped)
	require.False(t, ok)
}

func TestToolResultFromErrorRejectsNilResult(t *testing.T) {
	_, ok := ToolResultFromError(&ToolResultError{})
	require.False(t, ok)
}
