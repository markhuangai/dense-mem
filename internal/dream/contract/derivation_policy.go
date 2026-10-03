package contract

import (
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
