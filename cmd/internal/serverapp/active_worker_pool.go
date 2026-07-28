package serverapp

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
)

var (
	errActiveWorkerProfileListFailed = errors.New("active worker profile list failed")
	errSemanticPlacementWorkerFailed = errors.New("semantic placement worker failed")
	errEmbeddingWorkerFailed         = errors.New("embedding worker failed")
)

type activeWorkerProfileLister interface {
	List(context.Context, int, int) ([]*domain.Profile, error)
}

type activeTeamWorkFunc func(context.Context, string, string) (bool, error)

type activeTeamWorkerPoolConfig struct {
	name         string
	baseWorkerID string
	count        int
	pollInterval time.Duration
	profiles     activeWorkerProfileLister
	logger       observability.LogProvider
	workerError  error
	work         activeTeamWorkFunc
}

type activeTeamWorkResult struct {
	teamID string
	worked bool
	err    error
}

func activeWorkerCount(configured int) int {
	if configured <= 0 {
		return 1
	}
	return configured
}

func startActiveTeamWorkerPool(ctx context.Context, cfg activeTeamWorkerPoolConfig) {
	go runActiveTeamWorkerPool(ctx, cfg)
}

func runActiveTeamWorkerPool(ctx context.Context, cfg activeTeamWorkerPoolConfig) {
	const pageSize = 100

	workerCount := activeWorkerCount(cfg.count)
	pollInterval := cfg.pollInterval
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	tasks := make(chan string, workerCount)
	results := make(chan activeTeamWorkResult, workerCount)
	for workerIndex := 0; workerIndex < workerCount; workerIndex++ {
		workerID := fmt.Sprintf("%s-%s-%d", cfg.baseWorkerID, cfg.name, workerIndex+1)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case teamID := <-tasks:
					worked, err := cfg.work(ctx, teamID, workerID)
					select {
					case results <- activeTeamWorkResult{teamID: teamID, worked: worked, err: err}:
					case <-ctx.Done():
						return
					}
				}
			}
		}()
	}

	dispatcher := newActiveTeamDispatcher(pollInterval)
	refresh := func(now time.Time) {
		teamIDs, err := listActiveWorkerTeamIDs(ctx, cfg.profiles, pageSize)
		if err != nil {
			if ctx.Err() == nil {
				cfg.logger.Error(
					"active worker team list failed",
					errActiveWorkerProfileListFailed,
					observability.String("worker_kind", cfg.name),
				)
			}
			return
		}
		dispatcher.replaceTeams(teamIDs, now)
	}
	refresh(time.Now())

	availableWorkers := workerCount
	dispatch := func(now time.Time) {
		for availableWorkers > 0 {
			teamID, ok := dispatcher.next(now)
			if !ok {
				return
			}
			tasks <- teamID
			availableWorkers--
		}
	}
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		dispatch(time.Now())
		select {
		case <-ctx.Done():
			return
		case result := <-results:
			availableWorkers++
			dispatcher.complete(result.teamID, result.worked && result.err == nil, time.Now())
			if result.err != nil && ctx.Err() == nil {
				cfg.logger.Error(
					"active team worker failed",
					cfg.workerError,
					observability.String("worker_kind", cfg.name),
				)
			}
		case now := <-ticker.C:
			refresh(now)
		}
	}
}

func listActiveWorkerTeamIDs(ctx context.Context, profiles activeWorkerProfileLister, pageSize int) ([]string, error) {
	teamIDs := make([]string, 0, pageSize)
	seen := make(map[string]struct{})
	for offset := 0; ; offset += pageSize {
		teams, err := profiles.List(ctx, pageSize, offset)
		if err != nil {
			return nil, err
		}
		for _, team := range teams {
			if team == nil {
				continue
			}
			teamID := team.ID.String()
			if _, exists := seen[teamID]; exists {
				continue
			}
			seen[teamID] = struct{}{}
			teamIDs = append(teamIDs, teamID)
		}
		if len(teams) < pageSize {
			return teamIDs, nil
		}
	}
}

type activeTeamDispatchEntry struct {
	present   bool
	ready     bool
	cooling   bool
	inFlight  int
	nextProbe time.Time
}

type activeTeamDispatcher struct {
	pollInterval time.Duration
	order        []string
	entries      map[string]*activeTeamDispatchEntry
	queue        []string
	queued       map[string]bool
	nextDue      time.Time
}

func newActiveTeamDispatcher(pollInterval time.Duration) *activeTeamDispatcher {
	return &activeTeamDispatcher{
		pollInterval: pollInterval,
		entries:      make(map[string]*activeTeamDispatchEntry),
		queued:       make(map[string]bool),
	}
}

func (d *activeTeamDispatcher) replaceTeams(teamIDs []string, now time.Time) {
	for _, entry := range d.entries {
		entry.present = false
	}
	d.order = append(d.order[:0], teamIDs...)
	for _, teamID := range teamIDs {
		entry := d.entries[teamID]
		if entry == nil {
			entry = &activeTeamDispatchEntry{nextProbe: now}
			d.entries[teamID] = entry
		}
		entry.present = true
	}
	for teamID, entry := range d.entries {
		if !entry.present && entry.inFlight == 0 {
			delete(d.entries, teamID)
		}
	}
	d.pruneQueue()
	d.nextDue = now
	d.enqueueDue(now)
}

func (d *activeTeamDispatcher) next(now time.Time) (string, bool) {
	d.enqueueDue(now)
	for len(d.queue) > 0 {
		teamID := d.queue[0]
		d.queue = d.queue[1:]
		d.queued[teamID] = false
		entry := d.entries[teamID]
		if entry == nil || !entry.present || entry.cooling {
			continue
		}
		entry.inFlight++
		if entry.ready {
			d.enqueue(teamID)
		}
		return teamID, true
	}
	return "", false
}

func (d *activeTeamDispatcher) complete(teamID string, worked bool, now time.Time) {
	entry := d.entries[teamID]
	if entry == nil {
		return
	}
	if entry.inFlight > 0 {
		entry.inFlight--
	}
	if !entry.present {
		if entry.inFlight == 0 {
			delete(d.entries, teamID)
		}
		return
	}
	if !worked {
		entry.ready = false
		entry.cooling = true
		d.removeQueued(teamID)
	}
	if worked && !entry.cooling {
		becameReady := !entry.ready
		entry.ready = true
		if becameReady {
			d.enqueueFront(teamID)
		} else {
			d.enqueue(teamID)
		}
	}
	if entry.cooling && entry.inFlight == 0 {
		entry.cooling = false
		entry.nextProbe = now.Add(d.pollInterval)
		d.scheduleDue(entry.nextProbe)
	}
}

func (d *activeTeamDispatcher) enqueueDue(now time.Time) {
	if d.nextDue.IsZero() || now.Before(d.nextDue) {
		return
	}
	d.nextDue = time.Time{}
	for _, teamID := range d.order {
		entry := d.entries[teamID]
		if entry == nil || !entry.present || entry.ready || entry.cooling || entry.inFlight > 0 {
			continue
		}
		if now.Before(entry.nextProbe) {
			d.scheduleDue(entry.nextProbe)
			continue
		}
		d.enqueue(teamID)
	}
}

func (d *activeTeamDispatcher) scheduleDue(at time.Time) {
	if d.nextDue.IsZero() || at.Before(d.nextDue) {
		d.nextDue = at
	}
}

func (d *activeTeamDispatcher) enqueue(teamID string) {
	if d.queued[teamID] {
		return
	}
	d.queued[teamID] = true
	d.queue = append(d.queue, teamID)
}

func (d *activeTeamDispatcher) enqueueFront(teamID string) {
	if d.queued[teamID] {
		return
	}
	d.queued[teamID] = true
	d.queue = append(d.queue, "")
	copy(d.queue[1:], d.queue[:len(d.queue)-1])
	d.queue[0] = teamID
}

func (d *activeTeamDispatcher) removeQueued(teamID string) {
	if !d.queued[teamID] {
		return
	}
	filtered := d.queue[:0]
	for _, queuedTeamID := range d.queue {
		if queuedTeamID != teamID {
			filtered = append(filtered, queuedTeamID)
		}
	}
	d.queue = filtered
	d.queued[teamID] = false
}

func (d *activeTeamDispatcher) pruneQueue() {
	filtered := d.queue[:0]
	for _, teamID := range d.queue {
		entry := d.entries[teamID]
		if entry != nil && entry.present {
			filtered = append(filtered, teamID)
			continue
		}
		d.queued[teamID] = false
	}
	d.queue = filtered
}
