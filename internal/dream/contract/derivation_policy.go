package contract

import (
	"fmt"
	"strings"
)

// GraphSourceStatusEligible excludes non-source lifecycle states before the
// repository checks current endpoints, provenance, and evidence.
func GraphSourceStatusEligible(status string) bool {
	return status == "active" || status == "pending_evidence"
}

func GraphSourceValidationRequired(input UpsertHypothesisInput) bool {
	return input.GeneratorKind != "evaluation_seed"
}

func GraphSourceCountValid(input UpsertHypothesisInput) bool {
	return len(input.SourceVersions) == 2
}

func GraphDerivationMatchesSourceVersion(derivation DreamDerivationSource, sourceVersions map[string]int) bool {
	wantVersion, ok := sourceVersions[derivation.RelationshipID]
	return ok && wantVersion == derivation.RelationshipVersion
}

func GraphDerivationsCoverSources(derivations []DreamDerivationSource, sourceVersions map[string]int) bool {
	covered := make(map[string]struct{}, len(sourceVersions))
	for _, derivation := range derivations {
		covered[derivation.RelationshipID] = struct{}{}
	}
	return len(covered) == len(sourceVersions)
}

func GraphDerivationMatchesEvidence(derivation DreamDerivationSource, available []DreamEvidence) bool {
	for _, excerpt := range available {
		if strings.TrimSpace(derivation.SupportID) != strings.TrimSpace(excerpt.SupportID) ||
			strings.TrimSpace(derivation.ObservationID) != strings.TrimSpace(excerpt.ObservationID) ||
			strings.TrimSpace(derivation.FragmentID) != strings.TrimSpace(excerpt.FragmentID) ||
			strings.TrimSpace(derivation.SourceID) != strings.TrimSpace(excerpt.SourceID) ||
			strings.TrimSpace(derivation.SourceRevisionID) != strings.TrimSpace(excerpt.SourceRevisionID) ||
			strings.TrimSpace(derivation.SourceGroupKey) != strings.TrimSpace(excerpt.SourceGroupKey) ||
			derivation.SpanStart != excerpt.SpanStart ||
			derivation.SpanEnd != excerpt.SpanEnd ||
			derivation.Quote != excerpt.Content ||
			strings.TrimSpace(derivation.Authority) != strings.TrimSpace(excerpt.Authority) {
			continue
		}
		return true
	}
	return false
}

// DuplicateEvidenceDerivationSpan preserves the persisted span identity check.
func DuplicateEvidenceDerivationSpan(prior []EvidenceDerivationSource, current EvidenceDerivationSource) bool {
	key := fmt.Sprintf("%s:%d:%d", current.EvidenceID, current.SpanStart, current.SpanEnd)
	for _, derivation := range prior {
		if fmt.Sprintf("%s:%d:%d", derivation.EvidenceID, derivation.SpanStart, derivation.SpanEnd) == key {
			return true
		}
	}
	return false
}

func EvidenceDerivationsCiteTarget(derivations []EvidenceDerivationSource, targetEvidenceID string) bool {
	for _, derivation := range derivations {
		if derivation.EvidenceID == targetEvidenceID {
			return true
		}
	}
	return false
}

func EvidenceDerivationMetadataMatches(derivation EvidenceDerivationSource, evidence EvidenceContext) bool {
	return derivation.SourceID == evidence.SourceID &&
		derivation.SourceRevisionID == evidence.SourceRevisionID &&
		derivation.SourceGroupKey == evidence.SourceGroupKey &&
		derivation.Authority == evidence.Authority
}

func EvidenceDerivationSpanMatches(derivation EvidenceDerivationSource, content string) bool {
	runes := []rune(content)
	return derivation.SpanStart >= 0 && derivation.SpanEnd <= len(runes) &&
		derivation.SpanEnd > derivation.SpanStart &&
		string(runes[derivation.SpanStart:derivation.SpanEnd]) == derivation.Quote
}
