package repository

import "strings"

// searchDocumentHash remains a package-local compatibility utility used by
// search-reconciliation fixtures. Inline embedding validation and completion
// are implemented by internal/knowledge/postgres.
func searchDocumentHash(text string) string {
	return strings.TrimPrefix(sha256Hex(strings.TrimSpace(text)), "sha256:")
}
