package fulltextquery

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPlainText(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "slash paths and URLs",
			input: "git-vibe GitVibe /git-vibe github.com/markhuangai/git-vibe",
			want:  "git vibe GitVibe git vibe github com markhuangai git vibe",
		},
		{
			name:  "bare slash",
			input: "GitVibe workflows / git-vibe command",
			want:  "GitVibe workflows git vibe command",
		},
		{
			name:  "lucene operators and grouping",
			input: `context: "project decisions" +(memory) -draft`,
			want:  "context project decisions memory draft",
		},
		{
			name:  "punctuation only",
			input: `/ \ + - && || ! ( ) { } [ ] ^ " ~ * ? :`,
			want:  "",
		},
		{
			name:  "unicode text",
			input: "记忆/检索 dense-mem",
			want:  "记忆 检索 dense mem",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, PlainText(tt.input))
		})
	}
}

func TestPlainTextRemovesLuceneControlCharacters(t *testing.T) {
	got := PlainText(`a/b +c -d (e) [f] {g} h:i "j" k~ l* m? n^ && ||`)

	for _, token := range []string{"/", "+", "-", "(", ")", "[", "]", "{", "}", ":", `"`, "~", "*", "?", "^", "&", "|"} {
		require.NotContains(t, got, token)
	}
	require.Equal(t, strings.Join(strings.Fields(got), " "), got)
}
