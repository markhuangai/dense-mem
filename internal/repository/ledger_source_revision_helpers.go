package repository

import (
	"fmt"
	"sort"
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
	for _, item := range evidence {
		key := sourceRevisionBatchKey(item)
		if key == "" {
			continue
		}
		if _, ok := out[key]; ok {
			continue
		}
		out[key] = append([]string(nil), item.SupersedesFragmentIDs...)
	}
	return out
}

func sourceRevisionSupersedesKey(values []string) string {
	normalized := normalizeUUIDStringList(values)
	sort.Strings(normalized)
	return strings.Join(normalized, "\x00")
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
	supersedesSeen := make(map[string]string)
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
		key := sourceRevisionBatchKey(item)
		supersedesKey := sourceRevisionSupersedesKey(item.SupersedesFragmentIDs)
		previous, ok := seen[item.SourceKey]
		if !ok {
			seen[item.SourceKey] = current
			supersedesSeen[key] = supersedesKey
			continue
		}
		if previous != current {
			return fmt.Errorf("evidence[%d].source_key %q revision fields must match earlier item in request", i, item.SourceKey)
		}
		if previousSupersedes, ok := supersedesSeen[key]; ok && previousSupersedes != supersedesKey {
			return fmt.Errorf("evidence[%d].source_key %q supersedes_fragment_ids must match earlier item in source revision batch", i, item.SourceKey)
		}
		supersedesSeen[key] = supersedesKey
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
