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

func newDependencyCheckFlightRegistry() *dependencyCheckFlightRegistry {
	return &dependencyCheckFlightRegistry{active: make(map[string]*dependencyCheckFlight)}
}

func (r *dependencyCheckFlightRegistry) acquire(key string) (*dependencyCheckFlight, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		r.active = make(map[string]*dependencyCheckFlight)
	}
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

func runDependencyChecks(ctx context.Context, registry *dependencyCheckFlightRegistry, checks []HealthCheck) []dependencyCheckResult {
	if registry == nil {
		registry = newDependencyCheckFlightRegistry()
	}
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
				results[index] = runDependencyCheck(ctx, registry, checks[index])
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

func runDependencyCheck(ctx context.Context, registry *dependencyCheckFlightRegistry, check HealthCheck) dependencyCheckResult {
	started := time.Now()
	checkCtx, cancel := context.WithTimeout(ctx, dependencyCheckTimeout)
	defer cancel()
	if check.Check == nil {
		return dependencyCheckResult{Check: check, Latency: time.Since(started)}
	}
	key := dependencyCheckKey(check)
	flight, owner := registry.acquire(key)
	if !owner {
		return waitForDependencyCheck(checkCtx, check, started, flight)
	}
	sharedCtx, sharedCancel := context.WithTimeout(context.WithoutCancel(ctx), dependencyCheckTimeout)
	go func() {
		defer sharedCancel()
		err := check.Check(sharedCtx)
		registry.release(key, flight, dependencyCheckOutcome{
			Err:      err,
			TimedOut: err != nil && errors.Is(sharedCtx.Err(), context.DeadlineExceeded),
		})
	}()
	return waitForDependencyCheck(checkCtx, check, started, flight)
}

func waitForDependencyCheck(checkCtx context.Context, check HealthCheck, started time.Time, flight *dependencyCheckFlight) dependencyCheckResult {
	select {
	case <-flight.done:
		err := flight.outcome.Err
		timedOut := flight.outcome.TimedOut
		if timedOut && err == nil {
			err = context.DeadlineExceeded
		}
		if errors.Is(checkCtx.Err(), context.DeadlineExceeded) {
			timedOut = true
			if err == nil {
				err = checkCtx.Err()
			}
		}
		return dependencyCheckResult{
			Check:    check,
			Err:      err,
			Latency:  time.Since(started),
			TimedOut: timedOut,
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
