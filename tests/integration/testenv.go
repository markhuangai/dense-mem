package integration

import (
	"context"
	"testing"
)

// TestEnvProvider is the companion interface for TestEnv to enable mockability
type TestEnvProvider interface {
	Setup(ctx context.Context) error
	Teardown(ctx context.Context) error
	GetPostgresDSN() string
	GetRedisAddr() string
	GetServerBaseURL() string
}

// TestEnv is a shared integration fixture that manages test containers
// and in-process server lifecycle for UAT tests
type TestEnv struct {
	postgresDSN string
	redisAddr   string

	// Server
	serverBaseURL string
}

// Ensure TestEnv implements TestEnvProvider
var _ TestEnvProvider = (*TestEnv)(nil)

// Setup initializes all test containers and starts the in-process server
func (te *TestEnv) Setup(ctx context.Context) error {
	// Placeholder: container initialization will be implemented in subsequent units
	return nil
}

// Teardown stops all containers and cleans up resources
func (te *TestEnv) Teardown(ctx context.Context) error {
	// Placeholder: container cleanup will be implemented in subsequent units
	return nil
}

// GetPostgresDSN returns the connection string for the test Postgres container
func (te *TestEnv) GetPostgresDSN() string {
	return te.postgresDSN
}

// GetRedisAddr returns the address for the test Redis container
func (te *TestEnv) GetRedisAddr() string {
	return te.redisAddr
}

// GetServerBaseURL returns the base URL for the in-process test server
func (te *TestEnv) GetServerBaseURL() string {
	return te.serverBaseURL
}

// NewTestEnv creates a new TestEnv instance
func NewTestEnv() *TestEnv {
	return &TestEnv{}
}

// SetupTestEnv is a helper function that sets up the test environment
// and returns a cleanup function
func SetupTestEnv(t *testing.T, ctx context.Context) (*TestEnv, func()) {
	t.Helper()
	env := NewTestEnv()
	if err := env.Setup(ctx); err != nil {
		t.Fatalf("failed to setup test environment: %v", err)
	}
	cleanup := func() {
		if err := env.Teardown(ctx); err != nil {
			t.Logf("warning: failed to teardown test environment: %v", err)
		}
	}
	return env, cleanup
}
