package modelprovider

import "context"

// ConcurrencyGate is the process-wide limit for outbound model requests.
// Providers share one gate when several capability adapters use one model
// endpoint and configuration budget.
type ConcurrencyGate = chan struct{}

// NewConcurrencyGate creates a bounded outbound request gate.
func NewConcurrencyGate(limit int) ConcurrencyGate {
	if limit <= 0 {
		limit = 1
	}
	return make(chan struct{}, limit)
}

// AcquireConcurrency reserves one request slot or returns the context error
// when the caller is canceled while waiting.
func AcquireConcurrency(ctx context.Context, gate ConcurrencyGate) error {
	if gate == nil {
		return nil
	}
	select {
	case gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReleaseConcurrency returns one previously acquired request slot.
func ReleaseConcurrency(gate ConcurrencyGate) {
	if gate != nil {
		<-gate
	}
}
