package modelprovider

import (
	"context"
	"time"
)

// ProviderExchange is a bounded, operator-only record of one outbound model
// request and the response observed by the transport. It intentionally omits
// headers so credentials and cookies cannot enter diagnostics.
type ProviderExchange struct {
	Component           string
	Model               string
	RequestBody         []byte
	ResponseBody        []byte
	RequestContentType  string
	ResponseContentType string
	StatusCode          int
	Outcome             string
	CaptureState        string
	StartedAt           time.Time
	CompletedAt         time.Time
}

// ExchangeRecorder receives provider exchanges for the current Remember
// operation. Implementations must copy the byte slices before retaining them.
type ExchangeRecorder interface {
	RecordProviderExchange(context.Context, ProviderExchange)
}

type exchangeRecorderContextKey struct{}

func WithExchangeRecorder(ctx context.Context, recorder ExchangeRecorder) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if recorder == nil {
		return ctx
	}
	return context.WithValue(ctx, exchangeRecorderContextKey{}, recorder)
}

func ExchangeRecorderFromContext(ctx context.Context) ExchangeRecorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(exchangeRecorderContextKey{}).(ExchangeRecorder)
	return recorder
}
