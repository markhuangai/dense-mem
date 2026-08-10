package http

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
)

const (
	dependencyCheckTimeout     = 2 * time.Second
	dependencyCheckConcurrency = 3
)

type dependencyCheckResult struct {
	Check    HealthCheck
	Err      error
	Latency  time.Duration
	TimedOut bool
}

type dependencyCheckOutcome struct {
	Err      error
	TimedOut bool
}

type dependencyCheckFlight struct {
	done    chan struct{}
	outcome dependencyCheckOutcome
}

type dependencyCheckFlightRegistry struct {
	mu     sync.Mutex
	active map[string]*dependencyCheckFlight
}

func (r *dependencyCheckFlightRegistry) acquire(key string) (*dependencyCheckFlight, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if flight, exists := r.active[key]; exists {
		return flight, false
	}
	flight := &dependencyCheckFlight{done: make(chan struct{})}
	r.active[key] = flight
	return flight, true
}

func (r *dependencyCheckFlightRegistry) release(key string, flight *dependencyCheckFlight, outcome dependencyCheckOutcome) {
	flight.outcome = outcome
	close(flight.done)
	r.mu.Lock()
	if r.active[key] == flight {
		delete(r.active, key)
	}
	r.mu.Unlock()
}

var dependencyCheckFlights = dependencyCheckFlightRegistry{active: make(map[string]*dependencyCheckFlight)}

func runDependencyChecks(ctx context.Context, checks []HealthCheck) []dependencyCheckResult {
	results := make([]dependencyCheckResult, len(checks))
	jobs := make(chan int)
	workerCount := dependencyCheckConcurrency
	if len(checks) < workerCount {
		workerCount = len(checks)
	}
	if workerCount == 0 {
		return results
	}

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for worker := 0; worker < workerCount; worker++ {
		go func() {
			defer workers.Done()
			for index := range jobs {
				results[index] = runDependencyCheck(ctx, checks[index])
			}
		}()
	}
	for index := range checks {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
	return results
}

func runDependencyCheck(ctx context.Context, check HealthCheck) dependencyCheckResult {
	started := time.Now()
	checkCtx, cancel := context.WithTimeout(ctx, dependencyCheckTimeout)
	defer cancel()
	if check.Check == nil {
		return dependencyCheckResult{Check: check, Latency: time.Since(started)}
	}
	key := dependencyCheckKey(check)
	flight, owner := dependencyCheckFlights.acquire(key)
	if !owner {
		return waitForDependencyCheck(check, checkCtx, started, flight)
	}
	go func() {
		err := check.Check(checkCtx)
		dependencyCheckFlights.release(key, flight, dependencyCheckOutcome{
			Err:      err,
			TimedOut: errors.Is(checkCtx.Err(), context.DeadlineExceeded),
		})
	}()
	return waitForDependencyCheck(check, checkCtx, started, flight)
}

func waitForDependencyCheck(check HealthCheck, checkCtx context.Context, started time.Time, flight *dependencyCheckFlight) dependencyCheckResult {
	select {
	case <-flight.done:
		return dependencyCheckResult{
			Check:    check,
			Err:      flight.outcome.Err,
			Latency:  time.Since(started),
			TimedOut: flight.outcome.TimedOut || errors.Is(checkCtx.Err(), context.DeadlineExceeded),
		}
	case <-checkCtx.Done():
		return dependencyCheckResult{
			Check:    check,
			Err:      checkCtx.Err(),
			Latency:  time.Since(started),
			TimedOut: errors.Is(checkCtx.Err(), context.DeadlineExceeded),
		}
	}
}

func dependencyCheckKey(check HealthCheck) string {
	if check.Name != "" {
		return check.Name
	}
	return fmt.Sprintf("anonymous:%x", reflect.ValueOf(check.Check).Pointer())
}
