package service

import (
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRememberPartitionTerminalMappingContract(t *testing.T) {
	valid := func() *TerminalRememberResult {
		result := validTerminalResultForTest()
		result.Evidence = result.Evidence[:1]
		source := uuid.NewString()
		for index, span := range [][2]int{{0, 20}, {20, 16384}} {
			entry := result.Evidence[0]
			entry.PartitionIndex = new(index)
			entry.SpanStart = new(span[0])
			entry.SpanEnd = new(span[1])
			entry.SourceItemID = source
			entry.OccurrenceID = uuid.NewString()
			if index == 0 {
				result.Evidence[0] = entry
			} else {
				result.Evidence = append(result.Evidence, entry)
			}
		}
		return result
	}
	require.NoError(t, ValidateTerminalRememberResult(valid(), 1))
	for name, mutate := range map[string]func(*TerminalRememberResult){
		"source changed":        func(r *TerminalRememberResult) { r.Evidence[1].SourceItemID = uuid.NewString() },
		"occurrence duplicated": func(r *TerminalRememberResult) { r.Evidence[1].OccurrenceID = r.Evidence[0].OccurrenceID },
		"gap":                   func(r *TerminalRememberResult) { r.Evidence[1].SpanStart = new(21) },
		"missing first item": func(r *TerminalRememberResult) {
			for i := range r.Evidence {
				r.Evidence[i].EvidenceIndex = 1
			}
		},
		"partial metadata": func(r *TerminalRememberResult) { r.Evidence[1].PartitionIndex = nil },
		"overflow":         func(r *TerminalRememberResult) { r.Evidence[1].SpanEnd = new(16385) },
	} {
		t.Run(name, func(t *testing.T) { r := valid(); mutate(r); require.Error(t, ValidateTerminalRememberResult(r, 1)) })
	}
	require.Error(t, ValidateTerminalRememberResult(valid(), 2))
}
