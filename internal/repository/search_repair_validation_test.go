package repository

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

func TestNormalizeFinishSearchRepairRunInputTruncatesLastErrorByRune(t *testing.T) {
	input := FinishSearchRepairRunInput{
		LastError: strings.Repeat("a", 127) + "界" + "tail",
	}

	normalized := normalizeFinishSearchRepairRunInput(input)

	require.True(t, utf8.ValidString(normalized.LastError))
	require.Equal(t, 128, utf8.RuneCountInString(normalized.LastError))
	require.Equal(t, strings.Repeat("a", 127)+"界", normalized.LastError)
}

func TestNormalizeFinishSearchRepairRunInputRepairsInvalidUTF8(t *testing.T) {
	input := FinishSearchRepairRunInput{LastError: string([]byte{'a', 0xff, 'b'})}

	normalized := normalizeFinishSearchRepairRunInput(input)

	require.True(t, utf8.ValidString(normalized.LastError))
	require.Equal(t, "a�b", normalized.LastError)
}
