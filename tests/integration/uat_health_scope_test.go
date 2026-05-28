//go:build integration

package integration

import (
	"context"
	"testing"
)

// TestUATHealthAndScope is a UAT scaffold for health endpoint and scope headers.
// Skipped: depends on testcontainers fixture that is not yet wired (TestEnv.Setup
// is a placeholder). Tracked separately from the knowledge-pipeline build.
func TestUATHealthAndScope(t *testing.T) {
	t.Helper()
	t.Skip("UAT scaffold: TestEnv.Setup is a placeholder; testcontainers wiring pending")
	ctx := context.Background()

	// Setup test environment
	env, cleanup := SetupTestEnv(t, ctx)
	defer cleanup()

	_ = env
}
