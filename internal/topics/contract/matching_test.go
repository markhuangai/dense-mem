package contract

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVocabularyStopWordsArePinned(t *testing.T) {
	require.Equal(t, "b3f772a000465cb76e23adb03b47073c591c156fad8f7af09c8b8e80d6bd8eac", fmt.Sprintf("%x", sha256.Sum256([]byte(vocabularyStopWords))))
	words := VocabularyStopWords()
	require.Len(t, words, 127)
	require.Empty(t, LooseVocabularyQuery(words))
	words[0] = "postgresql"
	require.Equal(t, "i", VocabularyStopWords()[0])
	require.Equal(t, "postgresql", LooseVocabularyQuery([]string{"postgresql"}))
}

func TestLooseVocabularyPreservesRawTermsAndBound(t *testing.T) {
	sources := []SourceSnapshot{{State: map[string]string{"content": "ABOUT and x C++ 東京 Über PostgreSQL"}}}
	text, words := VocabularyQuery(sources)
	require.Equal(t, "about and x c 東京 über postgresql", text)
	require.Equal(t, "x OR c OR 東京 OR über OR postgresql", LooseVocabularyQuery(words))
	require.Equal(t, strings.Fields(text), words)
	sources[0].State["content"] = strings.Join(VocabularyStopWords()[:64], " ") + " postgresql"
	_, words = VocabularyQuery(sources)
	require.Len(t, words, 64)
	require.Empty(t, LooseVocabularyQuery(words), "filtering must not refill the raw 64-word window")
	require.Empty(t, LooseVocabularyQuery(nil))
}
