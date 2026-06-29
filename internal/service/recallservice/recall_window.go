package recallservice

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/tools/keywordsearch"
	"github.com/markhuangai/dense-mem/internal/tools/semanticsearch"
)

func factMatchesRecallWindow(f *domain.Fact, validAt, knownAt *time.Time) bool {
	if f == nil {
		return false
	}
	if validAt != nil {
		if f.ValidFrom != nil && f.ValidFrom.After(*validAt) {
			return false
		}
		if f.ValidTo != nil && !f.ValidTo.After(*validAt) {
			return false
		}
	}
	if knownAt != nil {
		if f.RecordedAt.After(*knownAt) {
			return false
		}
		if f.RecordedTo != nil && !f.RecordedTo.After(*knownAt) {
			return false
		}
	}
	return true
}

func claimMatchesRecallWindow(c *domain.Claim, validAt, knownAt *time.Time) bool {
	if c == nil {
		return false
	}
	if validAt != nil {
		if c.ValidFrom != nil && c.ValidFrom.After(*validAt) {
			return false
		}
		if c.ValidTo != nil && !c.ValidTo.After(*validAt) {
			return false
		}
	}
	if knownAt != nil {
		if c.RecordedAt.After(*knownAt) {
			return false
		}
		if c.RecordedTo != nil && !c.RecordedTo.After(*knownAt) {
			return false
		}
	}
	return true
}

func factCandidateMatchesRecallWindow(f FactRecallResult, validAt, knownAt *time.Time) bool {
	if validAt != nil {
		if f.ValidFrom != nil && f.ValidFrom.After(*validAt) {
			return false
		}
		if f.ValidTo != nil && !f.ValidTo.After(*validAt) {
			return false
		}
	}
	if knownAt != nil {
		if f.RecordedAt.After(*knownAt) {
			return false
		}
		if f.RecordedTo != nil && !f.RecordedTo.After(*knownAt) {
			return false
		}
	}
	return true
}

func claimCandidateMatchesRecallWindow(c ClaimRecallResult, validAt, knownAt *time.Time) bool {
	if validAt != nil {
		if c.ValidFrom != nil && c.ValidFrom.After(*validAt) {
			return false
		}
		if c.ValidTo != nil && !c.ValidTo.After(*validAt) {
			return false
		}
	}
	if knownAt != nil {
		if c.RecordedAt.After(*knownAt) {
			return false
		}
		if c.RecordedTo != nil && !c.RecordedTo.After(*knownAt) {
			return false
		}
	}
	return true
}

func filterSemanticFragmentsByWindow(hits []semanticsearch.SearchHit, validAt, knownAt *time.Time) []semanticsearch.SearchHit {
	if validAt == nil && knownAt == nil {
		return hits
	}
	out := hits[:0]
	for _, hit := range hits {
		if fragmentMetadataMatchesRecallWindow(hit.Metadata, validAt, knownAt) {
			out = append(out, hit)
		}
	}
	return out
}

func filterKeywordFragmentsByWindow(hits []keywordsearch.FragmentSearchResult, validAt, knownAt *time.Time) []keywordsearch.FragmentSearchResult {
	if validAt == nil && knownAt == nil {
		return hits
	}
	out := hits[:0]
	for _, hit := range hits {
		if fragmentMetadataMatchesRecallWindow(hit.Metadata, validAt, knownAt) {
			out = append(out, hit)
		}
	}
	return out
}

func fragmentMetadataMatchesRecallWindow(metadata map[string]any, validAt, knownAt *time.Time) bool {
	if validAt != nil {
		if validFrom, ok := metadataTime(metadata, "valid_from", "validFrom"); ok && validFrom.After(*validAt) {
			return false
		}
		if validTo, ok := metadataTime(metadata, "valid_to", "validTo"); ok && !validTo.After(*validAt) {
			return false
		}
	}
	if knownAt != nil {
		if recordedAt, ok := metadataTime(metadata, "recorded_at", "recordedAt"); ok && recordedAt.After(*knownAt) {
			return false
		}
		if recordedTo, ok := metadataTime(metadata, "recorded_to", "recordedTo"); ok && !recordedTo.After(*knownAt) {
			return false
		}
	}
	return true
}

func metadataTime(metadata map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		value, ok := metadata[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case time.Time:
			return typed.UTC(), true
		case string:
			parsed, err := time.Parse(time.RFC3339Nano, typed)
			if err != nil {
				return time.Time{}, false
			}
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}
