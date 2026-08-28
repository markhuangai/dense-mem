package repository

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestRememberAttemptHashMatchesCurrentHash(t *testing.T) {
	if !domain.RememberRequestHashMatches("current", "any", "current", "") {
		t.Fatal("current request hash must match")
	}
	if domain.RememberRequestHashMatches("stored", "any", "current", "") {
		t.Fatal("different current request hash must not match")
	}
}

func TestRememberAttemptHashMatchesOnlyRecognizedMigrationContract(t *testing.T) {
	if !domain.RememberRequestHashMatches("migrated", domain.MigratedRememberRequestHashVersion, "current", "migrated") {
		t.Fatal("recognized migration hash must match")
	}
	if domain.RememberRequestHashMatches("migrated", "unrecognized", "current", "migrated") {
		t.Fatal("unrecognized migration contract must not match")
	}
}
