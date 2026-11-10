package memorypack

import (
	"context"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestEvidenceOnlyUnitMemoryPackCanonicalizationExcludesTheArtifactHash(t *testing.T) {
	ctx, _, service, id := exportFixture()
	result, err := service.Export(ctx, ExportRequest{Name: "pack", EvidenceIDs: []string{id}})
	require.NoError(t, err)
	artifact := result.Artifact
	artifact.ContentSHA256 = "stale"
	artifact.Name = "  pack  "
	canonical, hash, err := canonicalMemoryPackArtifact(artifact)
	require.NoError(t, err)
	require.Equal(t, result.SHA256, hash)
	require.NotContains(t, string(canonical), "stale")
	encoded, err := marshalMemoryPackArtifact(artifact)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"content_sha256":"stale"`)
	artifact.Evidence[0].Content = "changed"
	_, _, err = canonicalMemoryPackArtifact(artifact)
	require.ErrorIs(t, err, ErrInvalidArtifact)
	artifact = result.Artifact
	artifact.Name = strings.Repeat("x", maxMemoryPackNameBytes+1)
	_, _, err = canonicalMemoryPackArtifact(artifact)
	require.ErrorIs(t, err, ErrInvalidArtifact)
	_, err = memoryPackActor(context.Background())
	require.ErrorIs(t, err, ErrMemoryPackAuthContext)
}
