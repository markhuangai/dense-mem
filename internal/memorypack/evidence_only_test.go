package memorypack

import (
	"crypto/sha256"
	"encoding/hex"
	tracecontract "github.com/markhuangai/dense-mem/internal/trace/contract"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestEvidenceOnlyUnitMemoryPackCanonicalHashesPreserveExactContentAndProvenance(t *testing.T) {
	content := "  Exact é🧭 Evidence.\n"
	digest := sha256.Sum256([]byte(content))
	fragment := tracecontract.TraceEvidenceFragment{FragmentID: "00000000-0000-4000-8000-000000000001", Content: content, ContentHash: "sha256:" + hex.EncodeToString(digest[:]), Metadata: map[string]any{"original": "source"}}
	artifact := MemoryPackArtifact{Format: MemoryPackFormat, PackID: "pack", Name: "Evidence export", CreatedAt: time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano), Evidence: []MemoryPackEvidence{{TraceEvidenceFragment: fragment, Occurrences: []tracecontract.TraceEvidenceFragment{{FragmentID: fragment.FragmentID, OccurrenceID: "second", Content: content, ContentHash: fragment.ContentHash}, {FragmentID: fragment.FragmentID, OccurrenceID: "first", Content: content, ContentHash: fragment.ContentHash}}}}, Omissions: []string{}}
	data, hash, err := canonicalMemoryPackArtifact(artifact)
	require.NoError(t, err)
	require.Contains(t, string(data), "dense-mem.memory-pack.v3.0")
	artifact.Evidence[0].Occurrences[0], artifact.Evidence[0].Occurrences[1] = artifact.Evidence[0].Occurrences[1], artifact.Evidence[0].Occurrences[0]
	_, same, err := canonicalMemoryPackArtifact(artifact)
	require.NoError(t, err)
	require.Equal(t, hash, same)
	artifact.Evidence[0].Content = "Changed content"
	_, _, err = canonicalMemoryPackArtifact(artifact)
	require.ErrorIs(t, err, ErrInvalidArtifact)
	artifact.Evidence[0].Content = content
	artifact.Evidence[0].Metadata = map[string]any{"original": "different source"}
	_, changed, err := canonicalMemoryPackArtifact(artifact)
	require.NoError(t, err)
	require.NotEqual(t, hash, changed)
	artifact.Evidence = append(artifact.Evidence, artifact.Evidence[0])
	_, _, err = canonicalMemoryPackArtifact(artifact)
	require.ErrorIs(t, err, ErrInvalidArtifact)
}
