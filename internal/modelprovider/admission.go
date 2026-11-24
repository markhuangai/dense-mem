package modelprovider

import "context"

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
