package config

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/observability"
)

func TestOperationalSecretsIncludesConfiguredAndHeaderCredentials(t *testing.T) {
	basic := base64.StdEncoding.EncodeToString([]byte("export-user:export-password"))
	cfg := Config{
		PostgresDSN: "postgres://user:database%20password@localhost/db", RedisPassword: "redis-secret",
		AIAPIKey: "embedding-secret", AIVerifierAPIKey: "verifier-secret",
		Decision:           DecisionProviderConfig{APIKey: "decision-secret"},
		ControlPortalToken: "control-secret", TelemetryScrapeToken: "scrape-secret",
		OTLPTraceHeaders:  map[string]string{"Authorization": "Basic " + basic},
		OTLPMetricHeaders: map[string]string{"Cookie": "session=cookie-secret", "X-API-Key": "export-key"},
	}
	secrets, err := cfg.OperationalSecrets()
	require.NoError(t, err)
	require.ElementsMatch(t, []string{
		cfg.PostgresDSN, "database password", "redis-secret", "embedding-secret", "verifier-secret", "decision-secret", "control-secret", "scrape-secret",
		"Basic " + basic, basic, "export-user:export-password", "export-password", "session=cookie-secret", "cookie-secret", "export-key",
	}, secrets)
	value := map[string]any{"detail": strings.Join(secrets, " | "), "token": "fictional-token", "password": "ordinary user text"}
	protected := observability.NewCredentialProtector(secrets...).Snapshot(value, 8192)
	require.Equal(t, observability.CredentialProtectionAvailable, protected.UnavailableReason)
	encoded, err := json.Marshal(protected.Value)
	require.NoError(t, err)
	for _, secret := range secrets {
		require.NotContains(t, string(encoded), secret)
	}
	require.Contains(t, string(encoded), "fictional-token")
	require.Contains(t, string(encoded), "ordinary user text")
}

func TestOperationalSecretsRejectsInvalidDSNWithoutExposingIt(t *testing.T) {
	secrets, err := (Config{PostgresDSN: "postgres://user:private%zz@localhost/db"}).OperationalSecrets()
	require.Nil(t, secrets)
	var validation *ValidationError
	require.ErrorAs(t, err, &validation)
	require.Equal(t, "POSTGRES_DSN", validation.Field)
	require.NotContains(t, err.Error(), "private")
}
