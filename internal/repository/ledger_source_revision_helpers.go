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
	return nil
}
