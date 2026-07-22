package main

import (
	"testing"
	"time"
)

func TestV2ActivePlacementLeaseCoversVerifierAndCommitWindow(t *testing.T) {
	if got := v2ActivePlacementLease(60, 10); got != 5*time.Minute {
		t.Fatalf("lease = %s, want 5m minimum", got)
	}
	if got := v2ActivePlacementLease(120, 20); got != 530*time.Second {
		t.Fatalf("lease = %s, want verifier*4 + commit + buffer", got)
	}
}

func TestV2ActiveWorkerCountUsesBoundedVerifierConcurrency(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{name: "missing", in: 0, want: 1},
		{name: "negative", in: -2, want: 1},
		{name: "configured", in: 5, want: 5},
		{name: "clamped", in: 100, want: 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := v2ActiveWorkerCount(tt.in); got != tt.want {
				t.Fatalf("worker count = %d, want %d", got, tt.want)
			}
		})
	}
}
