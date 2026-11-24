package contract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecallQueryNamesAndDefinitionMatching(t *testing.T) {
	names, bounded := RecallQueryNames("Where does DATA_STORE store durable data?")
	require.False(t, bounded)
	require.Contains(t, names, "data_store")
	require.Contains(t, names, "durable data")
	require.NotContains(t, names, "data_stor")
	views := []RecordView{
		{Record: Record{ID: "storage", Definition: &Definition{Key: "data_store", Aliases: []string{"durable data", "db"}}}, Current: true},
		{Record: Record{ID: "other", Definition: &Definition{Key: "other", Aliases: []string{"db"}}}, Current: true},
		{Record: Record{ID: "stale", Definition: &Definition{Key: "where"}}},
	}
	require.Equal(t, []string{"storage"}, MatchRecallDefinitions(names, views))
	require.Empty(t, MatchRecallDefinitions([]string{"db"}, views))
	require.Empty(t, MatchRecallDefinitions([]string{"storage"}, views))
	require.Empty(t, MatchRecallDefinitions([]string{"where"}, views))
	views[1].Definition.Key = "durable data"
	require.Equal(t, []string{"other"}, MatchRecallDefinitions([]string{"durable data"}, views))
}

func TestRecallQueryNamesBoundsCompleteMatching(t *testing.T) {
	words := []string{}
	for i := range 400 {
		words = append(words, fmt.Sprintf("term%d", i))
	}
	names, bounded := RecallQueryNames(strings.Join(words, " "))
	require.True(t, bounded)
	require.Nil(t, names)
}

func TestRecallQueryNamesPreservesPublishedPunctuation(t *testing.T) {
	for _, name := range []string{"C++", "C#", "Node.js", "S3://bucket", "two  words"} {
		t.Run(name, func(t *testing.T) {
			names, bounded := RecallQueryNames("Where is (" + name + ")?")
			require.False(t, bounded)
			require.Contains(t, names, NormalizeName(name))
			views := []RecordView{{Record: Record{ID: "topic", Definition: &Definition{Key: name}}, Current: true}}
			require.Equal(t, []string{"topic"}, MatchRecallDefinitions(names, views))
			views[0].Definition = &Definition{Key: "other", Aliases: []string{name}}
			require.Equal(t, []string{"topic"}, MatchRecallDefinitions(names, views))
		})
	}
	names, bounded := RecallQueryNames("Node.jsx")
	require.False(t, bounded)
	require.NotContains(t, names, "node.js")
}

func TestRecallQueryNamesKeepsCSharpBeforeSentencePunctuation(t *testing.T) {
	names, bounded := RecallQueryNames("What is C#?")
	require.False(t, bounded)
	require.Contains(t, names, "c#")
	for _, definition := range []*Definition{{Key: "C#"}, {Key: "dotnet", Aliases: []string{"C#"}}} {
		require.Equal(t, []string{"topic"}, MatchRecallDefinitions(names, []RecordView{{Record: Record{ID: "topic", Definition: definition}, Current: true}}))
	}
}
