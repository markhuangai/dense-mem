package domain

import "strings"

// RememberRequestHashMatches is the single owner of equivalent Remember
// request identity across current and migrated terminal attempts.
func RememberRequestHashMatches(storedHash, storedContract, requestHash, migratedHash string) bool {
	if strings.TrimSpace(storedHash) == strings.TrimSpace(requestHash) {
		return true
	}
	return strings.TrimSpace(storedContract) == MigratedRememberRequestHashVersion &&
		strings.TrimSpace(migratedHash) != "" &&
		strings.TrimSpace(storedHash) == strings.TrimSpace(migratedHash)
}
