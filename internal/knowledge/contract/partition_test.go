package contract

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRememberPartitionCommitReconstruction(t *testing.T) {
	text := "  Exact.\n\n第二。 "
	items := []RememberSubmittedItem{{ID: "source", Index: 0, Content: text, ContentHash: EvidenceContentHash(text), PolicyVersion: RememberPartitionPolicyVersion}}
	first := "  Exact.\n\n"
	evidence := []EvidenceInput{{Content: first, ContentHash: EvidenceContentHash(first), Partition: &EvidencePartition{SourceItemID: "source", Index: 0, Start: 0, End: len([]rune(first))}}, {Content: "第二。 ", ContentHash: EvidenceContentHash("第二。 "), Partition: &EvidencePartition{SourceItemID: "source", Index: 1, Start: len([]rune(first)), End: len([]rune(text))}}}
	require.NoError(t, ValidateRememberPartitions(items, evidence))
	for _, field := range []string{"source", "start", "end", "index", "text", "hash", "missing", "lifecycle", "source revision"} {
		t.Run(field, func(t *testing.T) {
			copyEvidence := append([]EvidenceInput{}, evidence...)
			p := *copyEvidence[1].Partition
			copyEvidence[1].Partition = &p
			switch field {
			case "source":
				p.SourceItemID = "foreign"
			case "start":
				p.Start++
			case "end":
				p.End--
			case "index":
				p.Index++
			case "text":
				copyEvidence[1].Content = "rewritten"
			case "hash":
				copyEvidence[1].ContentHash = "forged"
			case "missing":
				copyEvidence = copyEvidence[:1]
			case "lifecycle":
				copyEvidence[1].SupersedesEvidenceIDs = []string{"foreign"}
			case "source revision":
				copyEvidence[1].SourceRevisionToken = "changed"
			}
			require.Error(t, ValidateRememberPartitions(items, copyEvidence))
		})
	}
}
