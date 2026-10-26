package config

import "github.com/jackc/pgx/v5/pgconn"

// OperationalSecrets supplies the same resolved credentials to server logging and operator diagnostics.
func (cfg Config) OperationalSecrets() ([]string, error) {
	postgresConfig, err := pgconn.ParseConfig(cfg.PostgresDSN)
	if err != nil {
		return nil, &ValidationError{Field: "POSTGRES_DSN", Message: "invalid connection configuration"}
	}
	secrets := []string{
		cfg.PostgresDSN,
		postgresConfig.Password,
		cfg.RedisPassword,
		cfg.AIAPIKey,
		cfg.AIVerifierAPIKey,
		cfg.ControlPortalToken,
		cfg.TelemetryScrapeToken,
	}
	return append(secrets, cfg.ExportHeaderSecrets()...), nil
}
