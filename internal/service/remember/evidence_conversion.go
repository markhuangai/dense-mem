package remember

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// repositoryEvidenceInputs is the one boundary conversion used by remember
// and owner-authorized lifecycle replacements. Keeping it shared prevents the
// two entry points from drifting on source revisions, supersession, or audit
// metadata.
func repositoryEvidenceInputs(evidence []RememberEvidenceInput) []EvidenceInput {
	if len(evidence) == 0 {
		return nil
	}
	out := make([]EvidenceInput, 0, len(evidence))
	sourceRevisionHashes := sourceRevisionContentHashes(evidence)
	for _, item := range evidence {
		event := submissionSecurityPassEvent()
		authority, metadata := ledgerAuthorityAndMetadata(item.Authority, item.Metadata)
		metadata = evidenceProcessingIntentMetadata(metadata, item)
		out = append(out, EvidenceInput{
			Content:                       item.Content,
			ForceInsert:                   item.ForceInsert,
			ContentHash:                   evidenceContentHash(item.Content),
			SourceType:                    evidenceSourceType(item.SourceType),
			Authority:                     authority,
			SourceRef:                     strings.TrimSpace(item.Source),
			SourceKey:                     strings.TrimSpace(item.SourceKey),
			SourceRevisionToken:           strings.TrimSpace(item.SourceRevision),
			ExpectedPreviousRevisionToken: strings.TrimSpace(item.PreviousSourceRevision),
			SourceRevisionContentHash:     sourceRevisionHashes[sourceRevisionBatchKey(item)],
			SourceRevisionEnvelope:        sourceRevisionEnvelope(item),
			SupersedesEvidenceIDs:         append([]string(nil), item.SupersedesEvidenceIDs...),
			Labels:                        append([]string(nil), item.Labels...),
			Metadata:                      metadata,
			InitialEvent:                  &event,
		})
	}
	return out
}

func evidenceContentHash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}
