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

type dependencyCheckFlightRegistry struct {
	mu     sync.Mutex
	active map[string]struct{}
}

func (r *dependencyCheckFlightRegistry) acquire(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.active[key]; exists {
		return false
	}
	r.active[key] = struct{}{}
	return true
}

func (r *dependencyCheckFlightRegistry) release(key string) {
	r.mu.Lock()
	delete(r.active, key)
	r.mu.Unlock()
}

var dependencyCheckFlights = dependencyCheckFlightRegistry{active: make(map[string]struct{})}

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
	if !dependencyCheckFlights.acquire(key) {
		err := context.DeadlineExceeded
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return dependencyCheckResult{Check: check, Err: err, Latency: time.Since(started), TimedOut: errors.Is(err, context.DeadlineExceeded)}
	}
	result := make(chan error, 1)
	go func() {
		defer dependencyCheckFlights.release(key)
		result <- check.Check(checkCtx)
	}()
	select {
	case err := <-result:
		return dependencyCheckResult{
			Check:    check,
			Err:      err,
			Latency:  time.Since(started),
			TimedOut: errors.Is(checkCtx.Err(), context.DeadlineExceeded),
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
