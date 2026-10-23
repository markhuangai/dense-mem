package contract

import (
	"context"
	"errors"
	"time"
)

type MaintenanceFinalizationError struct {
	BatchID  string
	RunID    string
	Phase    string
	Duration time.Duration
	Cause    error
}

func (e *MaintenanceFinalizationError) Error() string {
	return "ontology maintenance finalization failed"
}
func (e *MaintenanceFinalizationError) Unwrap() error { return e.Cause }

func (e *MaintenanceFinalizationError) FailureClass() string {
	switch {
	case errors.Is(e.Cause, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(e.Cause, context.Canceled):
		return "canceled"
	case errors.Is(e.Cause, ErrLeaseLost):
		return "lease_lost"
	case e.SQLState() != "":
		return "postgres"
	default:
		return "internal_failure"
	}
}

func (e *MaintenanceFinalizationError) SQLState() string {
	var state interface{ SQLState() string }
	if !errors.As(e.Cause, &state) {
		return ""
	}
	code := state.SQLState()
	if len(code) != 5 {
		return ""
	}
	for _, c := range code {
		if (c < '0' || c > '9') && (c < 'A' || c > 'Z') {
			return ""
		}
	}
	return code
}
