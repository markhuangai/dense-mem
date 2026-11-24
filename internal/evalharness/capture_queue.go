package evalharness

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"
)

type captureQueue struct {
	events            chan Event
	bytes             *semaphore.Weighted
	done              chan struct{}
	ctx               context.Context
	stop              context.CancelFunc
	preserveOnce      sync.Once
	preserve          context.Context
	cancelPreserve    context.CancelFunc
	waitNS            atomic.Int64
	sinkNS            atomic.Int64
	httpNS            atomic.Int64
	scoringFinishedNS atomic.Int64
}

func (c *Capture) startQueue(cancelWork context.CancelFunc) {
	ctx, stop := context.WithCancel(context.Background())
	q := &captureQueue{events: make(chan Event, 128), bytes: semaphore.NewWeighted(64 << 20), done: make(chan struct{}), ctx: ctx, stop: stop}
	c.queue = q
	go func() {
		defer close(q.done)
		defer q.stop()
		for event := range q.events {
			if q.ctx.Err() != nil {
				q.bytes.Release(int64(len(event.Payload)))
				continue
			}
			started := time.Now()
			err := c.emit(q.ctx, event.phase, event.Kind, event.Payload)
			q.sinkNS.Add(time.Since(started).Nanoseconds())
			q.bytes.Release(int64(len(event.Payload)))
			if err == nil && event.Kind == "condition_finished" {
				q.scoringFinishedNS.Store(time.Now().UnixNano())
			}
			if err != nil {
				c.failed.Store(true)
				q.stop()
				cancelWork()
			}
		}
	}()
}

func (q *captureQueue) enqueue(ctx context.Context, event Event) error {
	started := time.Now()
	defer func() { q.waitNS.Add(time.Since(started).Nanoseconds()) }()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(q.ctx, cancel)
	defer cancel()
	defer stop()
	weight := int64(len(event.Payload))
	if weight > captureLimit {
		return errors.New("benchmark capture payload exceeds limit")
	}
	if err := q.bytes.Acquire(ctx, weight); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		q.bytes.Release(weight)
		return errors.New("benchmark capture queue unavailable")
	case q.events <- event:
		return nil
	}
}

func (q *captureQueue) preservation(ctx context.Context) context.Context {
	q.preserveOnce.Do(func() {
		q.preserve, q.cancelPreserve = context.WithTimeout(context.WithoutCancel(ctx), 20*time.Minute)
	})
	return q.preserve
}

func (c *Capture) finishQueue(ctx context.Context) error {
	close(c.queue.events)
	select {
	case <-c.queue.done:
		if !c.incomplete() {
			return nil
		}
	case <-ctx.Done():
		c.recordFailure(CaptureFailure{Phase: c.currentPhase(), Kind: "queue_drain", Reason: "capture_deadline"})
		c.lost.Store(true)
		c.queue.stop()
	}
	return errors.New("benchmark console capture did not drain")
}

func (c *Capture) recordHTTP(started time.Time) {
	if c != nil && c.queue != nil {
		c.queue.httpNS.Add(time.Since(started).Nanoseconds())
	}
}
