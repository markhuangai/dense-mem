package contract

import (
	"fmt"
	"sort"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func MaintenanceCompletion(unresolved bool, failure string) (string, string) {
	if unresolved {
		return "incomplete", failure
	}
	return "completed", ""
}

func MaintenanceWindowBounds(policy domain.OntologyMaintenanceConfig, now time.Time, previous *MaintenanceWindow) (time.Time, time.Time, error) {
	if (policy.CadenceHours != 12 && policy.CadenceHours != 24) || policy.MaxConcurrency < 1 || policy.MaxConcurrency > 8 || policy.InputTokens < 1 || policy.OutputTokens < 1 {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: invalid maintenance policy", ErrInvalid)
	}
	clock, err := time.Parse("15:04", policy.StartTimeLocal)
	if err != nil || clock.Format("15:04") != policy.StartTimeLocal {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: invalid maintenance start time", ErrInvalid)
	}
	location, err := time.LoadLocation(policy.Timezone)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: invalid maintenance timezone", ErrInvalid)
	}
	local := now.In(location)
	var slots []time.Time
	for day := -2; day <= 2; day++ {
		for hour := 0; hour < 24; hour += policy.CadenceHours {
			slot := time.Date(local.Year(), local.Month(), local.Day()+day, clock.Hour()+hour, clock.Minute(), 0, 0, location).UTC()
			slots = append(slots, slot)
		}
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].Before(slots[j]) })
	var start, end time.Time
	for _, slot := range slots {
		if !slot.After(now) && (previous == nil || !slot.Before(previous.EndsAt)) {
			start = slot
		}
	}
	if start.IsZero() {
		return start, end, nil
	}
	for _, slot := range slots {
		if slot.After(start) {
			end = slot
			break
		}
	}
	if end.IsZero() {
		return start, end, fmt.Errorf("%w: maintenance window end unavailable", ErrInvalid)
	}
	return start, end, nil
}

func PrepareMaintenanceCommand(input domain.OntologyMaintenanceCommand) (domain.OntologyMaintenanceCommand, error) {
	if !boundedText(input.OperationKey, 128) {
		return input, fmt.Errorf("%w: operation key required", ErrInvalid)
	}
	switch input.Action {
	case "run", "retry":
		if input.MaxBatches == 0 {
			input.MaxBatches = 1
		}
		if input.MaxBatches < 1 || input.MaxBatches > MaxManualBatches {
			return input, fmt.Errorf("%w: max_batches must be 1..100", ErrInvalid)
		}
		if (input.Action == "retry") != (input.RetryRunID != "") || (input.RetryRunID != "" && !validID(input.RetryRunID)) {
			return input, ErrInvalid
		}
	case "pause", "resume":
		if input.MaxBatches != 0 || input.RetryRunID != "" {
			return input, ErrInvalid
		}
	default:
		return input, fmt.Errorf("%w: unknown maintenance action", ErrInvalid)
	}
	return input, nil
}

func MaintenanceCommandHash(input domain.OntologyMaintenanceCommand) (string, error) {
	return hashJSON(input)
}
