package domain

import "testing"

func TestRememberRequestHashMatchesCurrentAndMigratedContracts(t *testing.T) {
	tests := []struct {
		name                       string
		storedHash, storedContract string
		requestHash, migratedHash  string
		want                       bool
	}{
		{name: "current", storedHash: "current", storedContract: "any", requestHash: "current", want: true},
		{name: "different current", storedHash: "stored", storedContract: ContractVersion, requestHash: "current", want: false},
		{name: "recognized migration", storedHash: "migrated", storedContract: MigratedRememberRequestHashVersion, requestHash: "current", migratedHash: "migrated", want: true},
		{name: "unrecognized migration", storedHash: "migrated", storedContract: "other", requestHash: "current", migratedHash: "migrated", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RememberRequestHashMatches(test.storedHash, test.storedContract, test.requestHash, test.migratedHash); got != test.want {
				t.Fatalf("RememberRequestHashMatches() = %t, want %t", got, test.want)
			}
		})
	}
}
