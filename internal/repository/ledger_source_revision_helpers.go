package repository

import (
	"fmt"
	"strings"
)

type sourceRevisionBatch struct {
	RevisionToken                 string
	ExpectedPreviousRevisionToken string
	SourceRevisionContentHash     string
}

func sourceRevisionBatchKey(item EvidenceInput) string {
	if item.SourceKey == "" {
		return ""
	}
	current := sourceRevisionBatch{
		RevisionToken:                 item.SourceRevisionToken,
		ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken,
		SourceRevisionContentHash:     item.SourceRevisionContentHash,
	}
	return item.SourceKey + "\x00" + current.RevisionToken + "\x00" + current.ExpectedPreviousRevisionToken + "\x00" + current.SourceRevisionContentHash
}

func sourceRevisionSupersedesByBatch(evidence []EvidenceInput) map[string][]string {
	out := map[string][]string{}
	seen := map[string]map[string]struct{}{}
	for _, item := range evidence {
		key := sourceRevisionBatchKey(item)
		if key == "" {
			continue
		}
		if seen[key] == nil {
			seen[key] = map[string]struct{}{}
		}
		for _, id := range item.SupersedesFragmentIDs {
			if _, ok := seen[key][id]; ok {
				continue
			}
			seen[key][id] = struct{}{}
			out[key] = append(out[key], id)
		}
	}
	return out
}

func normalizeUUIDStringList(values []string) []string {
	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func validateSourceRevisionBatch(evidence []EvidenceInput) error {
	seen := make(map[string]sourceRevisionBatch)
	supersedes := sourceRevisionSupersedesByBatch(evidence)
	for i, item := range evidence {
		if item.SourceKey == "" {
			continue
		}
		current := sourceRevisionBatch{
			RevisionToken:                 item.SourceRevisionToken,
			ExpectedPreviousRevisionToken: item.ExpectedPreviousRevisionToken,
			SourceRevisionContentHash:     item.SourceRevisionContentHash,
		}
		previous, ok := seen[item.SourceKey]
		if !ok {
			seen[item.SourceKey] = current
			continue
		}
		if previous != current {
			return fmt.Errorf("evidence[%d].source_key %q revision fields must match earlier item in request", i, item.SourceKey)
		}
	}
	for key, ids := range supersedes {
		if len(ids) > 50 {
			parts := strings.Split(key, "\x00")
			sourceKey := key
			if len(parts) > 0 {
				sourceKey = parts[0]
			}
			return fmt.Errorf("source_key %q supersedes_fragment_ids exceeds maximum 50", sourceKey)
		}
	}
	return nil
}
