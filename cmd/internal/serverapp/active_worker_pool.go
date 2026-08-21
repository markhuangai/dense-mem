package serverapp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/embedding"
	"github.com/markhuangai/dense-mem/internal/observability"
	"github.com/markhuangai/dense-mem/internal/service/memoryservice"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

var (
	errActiveWorkerTeamListFailed    = errors.New("active worker team list failed")
	errSemanticPlacementWorkerFailed = errors.New("semantic placement worker failed")
	errEmbeddingWorkerFailed         = errors.New("embedding worker failed")
)

type activeWorkerTeamLister interface {
	List(context.Context, int, int) ([]*domain.Team, error)
}

type activeTeamWorkFunc func(context.Context, string, string) (bool, error)

type activeTeamWorkerPoolConfig struct {
	name         string
	baseWorkerID string
	count        int
	pollInterval time.Duration
	teams        activeWorkerTeamLister
	logger       observability.LogProvider
	workerError  error
	work         activeTeamWorkFunc
}

func activePlacementLease(verifierTimeoutSeconds int, commitTimeoutSeconds int) time.Duration {
	if verifierTimeoutSeconds <= 0 {
		verifierTimeoutSeconds = 60
	}
	if commitTimeoutSeconds <= 0 {
		commitTimeoutSeconds = 10
	}
	providerWindow := verifierTimeoutSeconds * memoryservice.SemanticPlacementMaxAssessorTurns * verifier.RememberNormalizerTransportAttempts
	lease := time.Duration(providerWindow+commitTimeoutSeconds+30) * time.Second
	if lease < 5*time.Minute {
		return 5 * time.Minute
	}
	return lease
}

func activeEmbeddingLease(embeddingTimeoutSeconds int) time.Duration {
	if embeddingTimeoutSeconds <= 0 {
		embeddingTimeoutSeconds = 30
	}
	requestWindow := time.Duration(embeddingTimeoutSeconds*(embedding.DefaultRetryEmbeddingMaxRetries+1)) * time.Second
	retryWindow := time.Duration(embedding.DefaultRetryEmbeddingMaxRetries) * embedding.MaxProviderRetryAfter
	lease := requestWindow + retryWindow + 30*time.Second
	if lease < 5*time.Minute {
		return 5 * time.Minute
	}
	return lease
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
		teamIDs, err := listActiveWorkerTeamIDs(ctx, cfg.teams, pageSize)
		if err != nil {
			if ctx.Err() == nil {
				cfg.logger.Error(
					"active worker team list failed",
					errActiveWorkerTeamListFailed,
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
			dispatcher.complete(result.teamID, result.worked, time.Now())
			if result.err != nil && ctx.Err() == nil && cfg.logger != nil {
				attrs := []observability.LogAttr{
					observability.String("worker_kind", cfg.name),
					observability.String("team_id", result.teamID),
				}
				failure, classified := memoryservice.PlacementWorkerFailureFromError(result.err)
				if classified {
					if failure.SubmissionID != "" {
						attrs = append(attrs, observability.String("submission_id", failure.SubmissionID))
					}
					if failure.Stage != "" {
						attrs = append(attrs, observability.String("failure_stage", failure.Stage))
					}
					if failure.ReasonCode != "" {
						attrs = append(attrs, observability.String("failure_reason_code", failure.ReasonCode))
					}
					if failure.Class != "" {
						attrs = append(attrs, observability.String("failure_class", failure.Class))
					}
				} else {
					attrs = append(attrs,
						observability.String("failure_reason_code", "unclassified_worker_failure"),
						observability.String("failure_class", "internal"),
					)
				}
				cfg.logger.Error("active team worker failed", activeWorkerFailureLogError(cfg.workerError, failure, classified), attrs...)
			}
		case now := <-ticker.C:
			refresh(now)
		}
	}
}

func activeWorkerFailureLogError(base error, failure memoryservice.PlacementWorkerFailure, classified bool) error {
	if base == nil {
		base = errors.New("active team worker failed")
	}
	if !classified {
		return base
	}
	parts := []string{base.Error()}
	if failure.SubmissionID != "" {
		parts = append(parts, "submission_id="+failure.SubmissionID)
	}
	if failure.Stage != "" {
		parts = append(parts, "stage="+failure.Stage)
	}
	if failure.ReasonCode != "" {
		parts = append(parts, "reason="+failure.ReasonCode)
	}
	if failure.Class != "" {
		parts = append(parts, "class="+failure.Class)
	}
	return errors.New(strings.Join(parts, "; "))
}

func listActiveWorkerTeamIDs(ctx context.Context, teams activeWorkerTeamLister, pageSize int) ([]string, error) {
	teamIDs := make([]string, 0, pageSize)
	seen := make(map[string]struct{})
	for offset := 0; ; offset += pageSize {
		page, err := teams.List(ctx, pageSize, offset)
		if err != nil {
			return nil, err
		}
		for _, team := range page {
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
		if len(page) < pageSize {
			return teamIDs, nil
		}
	}
}

type activeTeamDispatchEntry struct {
	present       bool
	ready         bool
	cooling       bool
	workedInBurst bool
	inFlight      int
	nextProbe     time.Time
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
		if entry.inFlight == 0 {
			entry.workedInBurst = false
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
	if worked {
		entry.workedInBurst = true
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
		workedInBurst := entry.workedInBurst
		entry.cooling = false
		entry.workedInBurst = false
		if workedInBurst {
			entry.ready = true
			d.enqueueFront(teamID)
			return
		}
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
