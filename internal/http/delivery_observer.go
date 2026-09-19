package http

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"

	"github.com/labstack/echo/v4"
)

const deliveryObserverContextKey = "dense-mem.delivery-observer"

// DeliveryObservation records local response delivery without retaining the
// response body or asserting that the caller received it.
type DeliveryObservation struct {
	response     http.ResponseWriter
	status       int
	writeBytes   int64
	writeStarted bool
	writeErr     error
	flushErr     error
}

func NewDeliveryResponseWriter(response http.ResponseWriter) (http.ResponseWriter, *DeliveryObservation) {
	observation := &DeliveryObservation{response: response}
	capabilities := 0
	if _, ok := response.(interface{ FlushError() error }); ok {
		capabilities |= 1
	} else if _, ok := response.(http.Flusher); ok {
		capabilities |= 1
	}
	if _, ok := response.(http.Hijacker); ok {
		capabilities |= 2
	}
	if _, ok := response.(http.Pusher); ok {
		capabilities |= 4
	}
	if _, ok := response.(io.ReaderFrom); ok {
		capabilities |= 8
	}
	return deliveryCapabilityWriter(observation, capabilities), observation
}

func (o *DeliveryObservation) WriteHeader(status int) {
	if o.status != 0 {
		return
	}
	o.status = status
	o.response.WriteHeader(status)
}

func (o *DeliveryObservation) Write(value []byte) (int, error) {
	o.writeStarted = true
	if o.status == 0 {
		o.status = http.StatusOK
	}
	n, err := o.response.Write(value)
	o.writeBytes += int64(n)
	if err != nil && o.writeErr == nil {
		o.writeErr = err
	}
	return n, err
}

func (o *DeliveryObservation) flush() error {
	o.writeStarted = true
	if o.status == 0 {
		o.status = http.StatusOK
	}
	var err error
	if flusher, ok := o.response.(interface{ FlushError() error }); ok {
		err = flusher.FlushError()
	} else if flusher, ok := o.response.(http.Flusher); ok {
		flusher.Flush()
	} else {
		err = http.ErrNotSupported
	}
	if err != nil && !errors.Is(err, http.ErrNotSupported) && o.flushErr == nil {
		o.flushErr = err
	}
	return err
}

func (o *DeliveryObservation) hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := o.response.(http.Hijacker)
	if !ok {
		return nil, nil, http.ErrNotSupported
	}
	return hijacker.Hijack()
}

func (o *DeliveryObservation) push(target string, options *http.PushOptions) error {
	pusher, ok := o.response.(http.Pusher)
	if !ok {
		return http.ErrNotSupported
	}
	return pusher.Push(target, options)
}

func (o *DeliveryObservation) readFrom(reader io.Reader) (int64, error) {
	if source, ok := o.response.(io.ReaderFrom); ok {
		o.writeStarted = true
		if o.status == 0 {
			o.status = http.StatusOK
		}
		n, err := source.ReadFrom(reader)
		o.writeBytes += n
		if err != nil && o.writeErr == nil {
			o.writeErr = err
		}
		return n, err
	}
	return io.Copy(deliveryBaseWriter{o}, reader)
}

func (o *DeliveryObservation) Unwrap() http.ResponseWriter { return o.response }
func (o *DeliveryObservation) Status() int                 { return o.status }
func (o *DeliveryObservation) WriteBytes() int64           { return o.writeBytes }

func (o *DeliveryObservation) Stage(ctx context.Context, handlerErr error) string {
	if ctx != nil && (errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return "disconnect_observed"
	}
	if errors.Is(handlerErr, context.Canceled) || errors.Is(handlerErr, context.DeadlineExceeded) {
		return "disconnect_observed"
	}
	if o.flushErr != nil {
		return "flush_error_observed"
	}
	if o.writeErr != nil {
		if o.writeBytes > 0 {
			return "partial_write_observed"
		}
		return "write_error_observed"
	}
	if o.writeStarted || o.status != 0 {
		return "write_observed"
	}
	return "prepared"
}

type deliveryBaseWriter struct{ observation *DeliveryObservation }

func (w deliveryBaseWriter) Header() http.Header { return w.observation.response.Header() }
func (w deliveryBaseWriter) Write(value []byte) (int, error) {
	return w.observation.Write(value)
}
func (w deliveryBaseWriter) WriteHeader(status int)      { w.observation.WriteHeader(status) }
func (w deliveryBaseWriter) Unwrap() http.ResponseWriter { return w.observation.response }

type deliveryFlushMethods struct{ observation *DeliveryObservation }

func (w deliveryFlushMethods) Flush()            { _ = w.observation.flush() }
func (w deliveryFlushMethods) FlushError() error { return w.observation.flush() }

type deliveryHijackMethods struct{ observation *DeliveryObservation }

func (w deliveryHijackMethods) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.observation.hijack()
}

type deliveryPushMethods struct{ observation *DeliveryObservation }

func (w deliveryPushMethods) Push(target string, options *http.PushOptions) error {
	return w.observation.push(target, options)
}

type deliveryReadMethods struct{ observation *DeliveryObservation }

func (w deliveryReadMethods) ReadFrom(reader io.Reader) (int64, error) {
	return w.observation.readFrom(reader)
}

type deliveryF struct {
	deliveryBaseWriter
	deliveryFlushMethods
}
type deliveryH struct {
	deliveryBaseWriter
	deliveryHijackMethods
}
type deliveryP struct {
	deliveryBaseWriter
	deliveryPushMethods
}
type deliveryR struct {
	deliveryBaseWriter
	deliveryReadMethods
}
type deliveryFH struct {
	deliveryBaseWriter
	deliveryFlushMethods
	deliveryHijackMethods
}
type deliveryFP struct {
	deliveryBaseWriter
	deliveryFlushMethods
	deliveryPushMethods
}
type deliveryFR struct {
	deliveryBaseWriter
	deliveryFlushMethods
	deliveryReadMethods
}
type deliveryHP struct {
	deliveryBaseWriter
	deliveryHijackMethods
	deliveryPushMethods
}
type deliveryHR struct {
	deliveryBaseWriter
	deliveryHijackMethods
	deliveryReadMethods
}
type deliveryPR struct {
	deliveryBaseWriter
	deliveryPushMethods
	deliveryReadMethods
}
type deliveryFHP struct {
	deliveryBaseWriter
	deliveryFlushMethods
	deliveryHijackMethods
	deliveryPushMethods
}
type deliveryFHR struct {
	deliveryBaseWriter
	deliveryFlushMethods
	deliveryHijackMethods
	deliveryReadMethods
}
type deliveryFPR struct {
	deliveryBaseWriter
	deliveryFlushMethods
	deliveryPushMethods
	deliveryReadMethods
}
type deliveryHPR struct {
	deliveryBaseWriter
	deliveryHijackMethods
	deliveryPushMethods
	deliveryReadMethods
}
type deliveryFHPR struct {
	deliveryBaseWriter
	deliveryFlushMethods
	deliveryHijackMethods
	deliveryPushMethods
	deliveryReadMethods
}

func deliveryCapabilityWriter(o *DeliveryObservation, capabilities int) http.ResponseWriter {
	base := deliveryBaseWriter{observation: o}
	switch capabilities {
	case 1:
		return deliveryF{base, deliveryFlushMethods{o}}
	case 2:
		return deliveryH{base, deliveryHijackMethods{o}}
	case 3:
		return deliveryFH{base, deliveryFlushMethods{o}, deliveryHijackMethods{o}}
	case 4:
		return deliveryP{base, deliveryPushMethods{o}}
	case 5:
		return deliveryFP{base, deliveryFlushMethods{o}, deliveryPushMethods{o}}
	case 6:
		return deliveryHP{base, deliveryHijackMethods{o}, deliveryPushMethods{o}}
	case 7:
		return deliveryFHP{base, deliveryFlushMethods{o}, deliveryHijackMethods{o}, deliveryPushMethods{o}}
	case 8:
		return deliveryR{base, deliveryReadMethods{o}}
	case 9:
		return deliveryFR{base, deliveryFlushMethods{o}, deliveryReadMethods{o}}
	case 10:
		return deliveryHR{base, deliveryHijackMethods{o}, deliveryReadMethods{o}}
	case 11:
		return deliveryFHR{base, deliveryFlushMethods{o}, deliveryHijackMethods{o}, deliveryReadMethods{o}}
	case 12:
		return deliveryPR{base, deliveryPushMethods{o}, deliveryReadMethods{o}}
	case 13:
		return deliveryFPR{base, deliveryFlushMethods{o}, deliveryPushMethods{o}, deliveryReadMethods{o}}
	case 14:
		return deliveryHPR{base, deliveryHijackMethods{o}, deliveryPushMethods{o}, deliveryReadMethods{o}}
	case 15:
		return deliveryFHPR{base, deliveryFlushMethods{o}, deliveryHijackMethods{o}, deliveryPushMethods{o}, deliveryReadMethods{o}}
	default:
		return base
	}
}

func observeDelivery(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		response := c.Response()
		original := response.Writer
		writer, observation := NewDeliveryResponseWriter(original)
		response.Writer = writer
		c.Set(deliveryObserverContextKey, observation)
		defer func() { response.Writer = original }()
		return next(c)
	}
}

// DeliveryObservationMiddleware wraps a listener response writer without
// buffering its body and preserves the underlying optional capabilities.
func DeliveryObservationMiddleware(next echo.HandlerFunc) echo.HandlerFunc {
	return observeDelivery(next)
}

func deliveryObserverFromContext(c echo.Context) *DeliveryObservation {
	if c == nil {
		return nil
	}
	observation, _ := c.Get(deliveryObserverContextKey).(*DeliveryObservation)
	return observation
}
