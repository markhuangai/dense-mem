package observability

import (
	"context"
	"strings"
	"sync"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

// DiagnosticCaptureState derives the bounded state used by both the capture
// recorder and durable repository when a caller did not provide one.
func DiagnosticCaptureState(captureState, outcome string, requestBodyBytes, responseBodyBytes int) string {
	if state := strings.TrimSpace(captureState); state != "" {
		return state
	}
	switch strings.TrimSpace(outcome) {
	case "provider_not_called":
		return "provider_not_called"
	case "no_response":
		return "no_response"
	case "response_read_failed":
		return "interrupted"
	case "response_too_large":
		return "truncated"
	case "not_captured":
		return "not_captured"
	default:
		if requestBodyBytes == 0 && responseBodyBytes == 0 {
			return "not_captured"
		}
		return "captured"
	}
}

const MaxProviderDiagnosticSnapshotBytes = 64 << 20

func BoundProviderDiagnosticBody(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return nil, false
	}
	sanitized := body
	truncated := false
	if len(sanitized) > modelprovider.MaxProviderDiagnosticBodyBytes {
		sanitized = sanitized[:modelprovider.MaxProviderDiagnosticBodyBytes]
		truncated = true
	}
	return append([]byte(nil), sanitized...), truncated
}

type ProviderDiagnosticCapture struct {
	Body   []byte
	State  string
	Reason string
}

func CaptureProviderDiagnosticBody(body []byte, protector DiagnosticProtector, authenticatedSecrets ...string) ProviderDiagnosticCapture {
	if len(body) == 0 {
		return ProviderDiagnosticCapture{State: "not_captured"}
	}
	if protector == nil {
		return ProviderDiagnosticCapture{
			State:  "unavailable",
			Reason: CredentialProtectionUnsupported.CaptureReason(modelprovider.MaxProviderDiagnosticBodyBytes),
		}
	}
	ctx := WithAuthenticationSecrets(context.Background(), authenticatedSecrets...)
	protected, reason := ProtectDiagnosticText(ctx, protector, body, modelprovider.MaxProviderDiagnosticBodyBytes)
	if reason != CredentialProtectionAvailable {
		return ProviderDiagnosticCapture{
			State:  "unavailable",
			Reason: reason.CaptureReason(modelprovider.MaxProviderDiagnosticBodyBytes),
		}
	}
	captured, protectedTruncated := BoundProviderDiagnosticBody(protected)
	state := "captured"
	if len(body) > modelprovider.MaxProviderDiagnosticBodyBytes || protectedTruncated {
		state = "truncated"
	}
	return ProviderDiagnosticCapture{Body: captured, State: state}
}

func CombineProviderDiagnosticCapture(explicitState, explicitReason string, captures ...ProviderDiagnosticCapture) (string, string) {
	state := strings.TrimSpace(explicitState)
	reason := strings.TrimSpace(explicitReason)
	for _, capture := range captures {
		if capture.State == "unavailable" {
			return "unavailable", capture.Reason
		}
	}
	for _, capture := range captures {
		if capture.State == "truncated" {
			return "truncated", capture.Reason
		}
	}
	if state == "" {
		state = "captured"
	}
	return state, reason
}

func ProviderExchangeCaptureState(exchange modelprovider.ProviderExchange) string {
	responseBytes := exchange.ResponseBodySize
	if responseBytes == 0 {
		responseBytes = len(exchange.ResponseBody)
	}
	state := strings.TrimSpace(exchange.CaptureState)
	if state == "captured" {
		state = ""
	}
	return DiagnosticCaptureState(state, exchange.Outcome, len(exchange.RequestBody), responseBytes)
}

type ProviderExchangeRecorder struct {
	mu        sync.Mutex
	exchanges []modelprovider.ProviderExchange
	protector DiagnosticProtector
}

// NewProviderExchangeRecorder protects complete bodies with configured and request-scoped credentials before retaining bounded snapshots.
func NewProviderExchangeRecorder(protector DiagnosticProtector) *ProviderExchangeRecorder {
	return &ProviderExchangeRecorder{protector: protector}
}

func (r *ProviderExchangeRecorder) RecordProviderExchange(ctx context.Context, exchange modelprovider.ProviderExchange) {
	if r == nil {
		return
	}
	originalRequestBytes, originalResponseBytes := len(exchange.RequestBody), exchange.ResponseBodySize
	if originalResponseBytes == 0 {
		originalResponseBytes = len(exchange.ResponseBody)
	}
	authenticatedSecrets := AuthenticationSecretsFromContext(ctx)
	requestCapture := CaptureProviderDiagnosticBody(exchange.RequestBody, r.protector, authenticatedSecrets...)
	exchange.RequestBody = requestCapture.Body
	responseCapture := CaptureProviderDiagnosticBody(exchange.ResponseBody, r.protector, authenticatedSecrets...)
	exchange.ResponseBody = responseCapture.Body
	exchange.ResponseBodyProjection = nil
	exchange.CaptureState, exchange.CaptureReason = CombineProviderDiagnosticCapture(
		ProviderExchangeCaptureState(exchange), exchange.CaptureReason, requestCapture, responseCapture,
	)
	var requestTruncated, responseTruncated bool
	exchange.RequestBody, requestTruncated = BoundProviderDiagnosticBody(exchange.RequestBody)
	exchange.ResponseBody, responseTruncated = BoundProviderDiagnosticBody(exchange.ResponseBody)
	if exchange.CaptureState == "" {
		exchange.CaptureState = ProviderExchangeCaptureState(exchange)
	}
	if (requestTruncated || responseTruncated || originalRequestBytes > modelprovider.MaxProviderDiagnosticBodyBytes || originalResponseBytes > modelprovider.MaxProviderDiagnosticBodyBytes) && exchange.CaptureState != "unavailable" {
		exchange.CaptureState = "truncated"
	}
	r.mu.Lock()
	r.exchanges = append(r.exchanges, exchange)
	r.mu.Unlock()
}

func (r *ProviderExchangeRecorder) Snapshot() []modelprovider.ProviderExchange {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]modelprovider.ProviderExchange, len(r.exchanges))
	remaining := MaxProviderDiagnosticSnapshotBytes
	for index, exchange := range r.exchanges {
		bounded := exchange
		request, requestTruncated := BoundProviderDiagnosticBody(exchange.RequestBody)
		response, responseTruncated := BoundProviderDiagnosticBody(exchange.ResponseBody)
		if remaining <= 0 {
			request, response = nil, nil
			requestTruncated = len(exchange.RequestBody) > 0
			responseTruncated = len(exchange.ResponseBody) > 0
		} else if len(request) > remaining {
			request = request[:remaining]
			response = nil
			requestTruncated = true
			responseTruncated = len(exchange.ResponseBody) > 0
		} else if len(request)+len(response) > remaining {
			response = response[:remaining-len(request)]
			responseTruncated = true
		}
		bounded.RequestBody, bounded.ResponseBody = request, response
		if bounded.CaptureState == "" {
			bounded.CaptureState = ProviderExchangeCaptureState(bounded)
		}
		if (requestTruncated || responseTruncated) && bounded.CaptureState != "unavailable" {
			bounded.CaptureState = "truncated"
		}
		result[index] = bounded
		remaining -= len(request) + len(response)
		if remaining <= 0 {
			for next := index + 1; next < len(result); next++ {
				result[next] = r.exchanges[next]
				result[next].RequestBody = nil
				result[next].ResponseBody = nil
				if result[next].CaptureState != "unavailable" {
					result[next].CaptureState = "truncated"
				}
			}
			return result
		}
	}
	return result
}
