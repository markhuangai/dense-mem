package modelprovider

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// AdmissionCallback is invoked after a provider transport has acquired its
// outbound-request admission slot and immediately before dispatching work.
// The callback may return an error to stop dispatch before the request is sent.
type AdmissionCallback func(context.Context) error

type admissionCallbackContextKey struct{}

// WithAdmissionCallback attaches a transport admission callback to ctx.
// Capability services use this to record durable dispatch state only after
// shared provider admission succeeds.
func WithAdmissionCallback(ctx context.Context, callback AdmissionCallback) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if callback == nil {
		return ctx
	}
	return context.WithValue(ctx, admissionCallbackContextKey{}, callback)
}

// NotifyAdmission invokes the callback attached to ctx, if any.
func NotifyAdmission(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	callback, _ := ctx.Value(admissionCallbackContextKey{}).(AdmissionCallback)
	if callback == nil {
		return nil
	}
	return callback(ctx)
}

type rateLimitObserverKey struct{}

func WithRateLimitObserver(ctx context.Context, observe func(time.Time)) context.Context {
	return context.WithValue(ctx, rateLimitObserverKey{}, observe)
}

func ObserveRateLimit(ctx context.Context, header string, now time.Time) {
	observe, _ := ctx.Value(rateLimitObserverKey{}).(func(time.Time))
	if observe == nil {
		return
	}
	header = strings.TrimSpace(header)
	if seconds, err := strconv.ParseUint(header, 10, 63); err == nil && seconds > 0 {
		// A far-future header must not wrap around and reopen admission.
		now = now.Add(time.Duration(min(seconds, uint64((1<<63-1)/time.Second))) * time.Second)
	} else if date, err := http.ParseTime(header); err == nil && date.After(now) {
		now = date
	}
	observe(now)
}
