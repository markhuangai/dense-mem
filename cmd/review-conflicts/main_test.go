package main

import (
	"io"
	"strings"
	"testing"
)

func TestParseCLIRejectsInvalidTimezone(t *testing.T) {
	_, err := parseCLI([]string{
		"--team-id", "00000000-0000-0000-0000-000000000001",
		"--timezone", "Mars/Base",
	}, io.Discard)

	if err == nil || !strings.Contains(err.Error(), "--timezone is invalid") {
		t.Fatalf("parseCLI error = %v, want invalid timezone", err)
	}
}

func TestParseCLIDefaultsTimezoneFromAppTimezone(t *testing.T) {
	t.Setenv("APP_TIMEZONE", "America/New_York")

	cfg, err := parseCLI([]string{
		"--team-id", "00000000-0000-0000-0000-000000000001",
	}, io.Discard)
	if err != nil {
		t.Fatalf("parseCLI returned error: %v", err)
	}
	if cfg.timezone != "America/New_York" {
		t.Fatalf("timezone = %q, want America/New_York", cfg.timezone)
	}
}
