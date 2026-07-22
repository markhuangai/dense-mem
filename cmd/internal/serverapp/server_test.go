package serverapp

import (
	"testing"
	"time"
)

func TestActivePlacementLeaseCoversVerifierAndCommitWindow(t *testing.T) {
	if got := activePlacementLease(60, 10); got != 640*time.Second {
		t.Fatalf("lease = %s, want verifier*10 + commit + buffer", got)
	}
	if got := activePlacementLease(120, 20); got != 1250*time.Second {
		t.Fatalf("lease = %s, want verifier*10 + commit + buffer", got)
	}
	if got := activePlacementLease(1, 1); got != 5*time.Minute {
		t.Fatalf("lease = %s, want 5m minimum", got)
	}
}

func TestActiveWorkerCountUsesBoundedVerifierConcurrency(t *testing.T) {
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
			if got := activeWorkerCount(tt.in); got != tt.want {
				t.Fatalf("worker count = %d, want %d", got, tt.want)
			}
		})
	}
}
