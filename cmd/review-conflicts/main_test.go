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
