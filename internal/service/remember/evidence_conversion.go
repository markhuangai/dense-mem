package remember

import (
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
