package postgres

import (
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRememberPartitionRecallReturnsExactUnicodeSlice(t *testing.T) {
	text := "  " + strings.Repeat("界", knowledge.MaxRememberItemRunes-5) + " 🧭\n"
	require.Equal(t, knowledge.MaxRememberItemRunes, utf8.RuneCountInString(text))
	require.Equal(t, text, truncateRecallContext(text))
	require.Equal(t, text, truncateRecallContext(text+"omitted"))
	require.True(t, utf8.ValidString(truncateRecallContext(text+"omitted")))
}
