package repository

import "testing"

func TestRememberAttemptHashMatchesCurrentHash(t *testing.T) {
	if !rememberAttemptHashMatches("current", "any", "current", "") {
		t.Fatal("current request hash must match")
	}
	if rememberAttemptHashMatches("stored", "any", "current", "") {
		t.Fatal("different current request hash must not match")
	}
}

func TestRememberAttemptHashMatchesOnlyRecognizedMigrationContract(t *testing.T) {
	if !rememberAttemptHashMatches("migrated", MigratedRememberRequestHashVersion, "current", "migrated") {
		t.Fatal("recognized migration hash must match")
	}
	if rememberAttemptHashMatches("migrated", "unrecognized", "current", "migrated") {
		t.Fatal("unrecognized migration contract must not match")
	}
}
