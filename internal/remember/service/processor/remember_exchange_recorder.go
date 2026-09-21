package processor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	rememberapp "github.com/markhuangai/dense-mem/internal/remember/service"
)

const (
	rememberDiagnosticMaxBodyBytes    = modelprovider.MaxProviderDiagnosticBodyBytes
	rememberDiagnosticMaxAttemptBytes = 64 << 20
)

func boundedRememberDiagnosticBody(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return nil, false
	}
	sanitized := body
	truncated := false
	if len(sanitized) > rememberDiagnosticMaxBodyBytes {
		sanitized = sanitized[:rememberDiagnosticMaxBodyBytes]
		truncated = true
	}
	return append([]byte(nil), sanitized...), truncated
}

type rememberDiagnosticCapture struct {
	body   []byte
	state  string
	reason string
}

func captureRememberDiagnosticBody(body []byte, protector observability.DiagnosticProtector, authenticatedSecrets ...string) rememberDiagnosticCapture {
	if len(body) == 0 {
		return rememberDiagnosticCapture{state: "not_captured"}
	}
	if protector == nil {
		return rememberDiagnosticCapture{
			state:  "unavailable",
			reason: "credential_protection_" + strconv.Itoa(int(observability.CredentialProtectionUnsupported)),
		}
	}
	// The extra bytes cover the root's JSON string envelope.
	protected, reason := protector.ProtectDiagnosticBytes(body, rememberDiagnosticMaxBodyBytes+2, authenticatedSecrets...)
	if reason != observability.CredentialProtectionAvailable {
		return rememberDiagnosticCapture{
			state:  "unavailable",
			reason: "credential_protection_" + strconv.Itoa(int(reason)),
		}
	}
	captured, protectedTruncated := boundedRememberDiagnosticBody(protected)
	state := "captured"
	if len(body) > rememberDiagnosticMaxBodyBytes || protectedTruncated {
		state = "truncated"
	}
	return rememberDiagnosticCapture{body: captured, state: state}
}

func combineRememberDiagnosticCapture(explicitState, explicitReason string, captures ...rememberDiagnosticCapture) (string, string) {
	state := strings.TrimSpace(explicitState)
	reason := strings.TrimSpace(explicitReason)
	for _, capture := range captures {
		if capture.state == "unavailable" {
			return "unavailable", capture.reason
		}
	}
	for _, capture := range captures {
		if capture.state == "truncated" {
			return "truncated", capture.reason
		}
	}
	if state == "" {
		state = "captured"
	}
	return state, reason
}

func protectedRememberDiagnosticBody(body []byte, protector observability.DiagnosticProtector, authenticatedSecrets ...string) ([]byte, bool) {
	capture := captureRememberDiagnosticBody(body, protector, authenticatedSecrets...)
	return capture.body, capture.state == "truncated" || capture.state == "unavailable"
}

func rememberDiagnosticCaptureStateForExchange(exchange modelprovider.ProviderExchange) string {
	responseBytes := exchange.ResponseBodySize
	if responseBytes == 0 {
		responseBytes = len(exchange.ResponseBody)
	}
	state := strings.TrimSpace(exchange.CaptureState)
	if state == "captured" {
		state = ""
	}
	return repository.DiagnosticCaptureState(state, exchange.Outcome, len(exchange.RequestBody), responseBytes)
}

func rememberFailureDiagnostics(
	input rememberapp.RememberProcessRequest,
	publicResult map[string]any,
	exchanges []modelprovider.ProviderExchange,
	callerResponse []byte,
	callerResponseDelivered bool,
	_ string,
	protectors ...observability.DiagnosticProtector,
) []repository.RememberAttemptDiagnosticInput {
	return rememberFailureDiagnosticsWithCapture(input, publicResult, exchanges, callerResponse, callerResponseDelivered, len(callerResponse) > 0, protectors...)
}

func rememberFailureDiagnosticsWithCapture(
	input rememberapp.RememberProcessRequest,
	publicResult map[string]any,
	exchanges []modelprovider.ProviderExchange,
	callerResponse []byte,
	callerResponseDelivered bool,
	callerResponseCaptureAvailable bool,
	protectors ...observability.DiagnosticProtector,
) []repository.RememberAttemptDiagnosticInput {
	return rememberFailureDiagnosticsWithAuthenticationSecrets(
		input, publicResult, exchanges, callerResponse, callerResponseDelivered,
		callerResponseCaptureAvailable, nil, protectors...,
	)
}

func rememberFailureDiagnosticsWithAuthenticationSecrets(
	input rememberapp.RememberProcessRequest,
	publicResult map[string]any,
	exchanges []modelprovider.ProviderExchange,
	callerResponse []byte,
	callerResponseDelivered bool,
	callerResponseCaptureAvailable bool,
	authenticatedSecrets []string,
	protectors ...observability.DiagnosticProtector,
) []repository.RememberAttemptDiagnosticInput {
	var protector observability.DiagnosticProtector
	if len(protectors) > 0 {
		protector = protectors[0]
	}
	if !callerResponseCaptureAvailable {
		callerResponse = nil
	} else if !callerResponseDelivered {
		callerResponse = nil
	}
	items := make([]repository.RememberAttemptDiagnosticInput, 0, len(exchanges)+2)
	if len(input.OriginalRequest) > 0 {
		if input.SecurityRejected || input.InitialSecurityRejected {
			requestHash := input.RequestHash
			if requestHash == "" {
				digest := sha256.Sum256(input.OriginalRequest)
				requestHash = "sha256:" + hex.EncodeToString(digest[:])
			}
			requestBody, _ := json.Marshal(map[string]any{
				"capture":        "hash_only",
				"evidence_count": len(input.Evidence),
				"original_bytes": len(input.OriginalRequest),
				"request_sha256": requestHash,
			})
			items = append(items, repository.RememberAttemptDiagnosticInput{
				SequenceNo: 1, Kind: "original_request", Component: "remember",
				RequestBody: requestBody, RequestContentType: "application/json", Outcome: "hash_only", CaptureState: "hash_only",
			})
		} else {
			capture := captureRememberDiagnosticBody(input.OriginalRequest, protector, authenticatedSecrets...)
			items = append(items, repository.RememberAttemptDiagnosticInput{
				SequenceNo: 1, Kind: "original_request", Component: "remember",
				RequestBody: capture.body, RequestContentType: "application/json", Outcome: "captured", CaptureState: capture.state, CaptureReason: capture.reason,
			})
		}
	} else {
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: 1, Kind: "original_request", Component: "remember", Outcome: "not_captured", CaptureState: "not_captured",
		})
	}
	sequence := len(items) + 1
	for _, exchange := range exchanges {
		outcome := exchange.Outcome
		if outcome == "" {
			outcome = "captured"
		}
		capturedAt := exchange.StartedAt
		if capturedAt.IsZero() {
			capturedAt = time.Now().UTC()
		}
		requestCapture := captureRememberDiagnosticBody(exchange.RequestBody, protector, authenticatedSecrets...)
		responseCapture := captureRememberDiagnosticBody(exchange.ResponseBody, protector, authenticatedSecrets...)
		captureState, captureReason := combineRememberDiagnosticCapture(
			rememberDiagnosticCaptureStateForExchange(exchange), exchange.CaptureReason,
			requestCapture, responseCapture,
		)
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "provider_exchange", Component: exchange.Component,
			Model: exchange.Model, RequestBody: requestCapture.body,
			ResponseBody:       responseCapture.body,
			RequestContentType: exchange.RequestContentType, ResponseContentType: exchange.ResponseContentType,
			StatusCode: exchange.StatusCode, Outcome: outcome, CaptureState: captureState, CaptureReason: captureReason,
			CapturedAt: capturedAt, ExpiresAt: capturedAt.Add(7 * 24 * time.Hour),
		})
		sequence++
	}
	if len(exchanges) == 0 {
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "provider_exchange", Component: "provider", Outcome: "provider_not_called", CaptureState: "provider_not_called",
		})
		sequence++
	}
	if !callerResponseCaptureAvailable {
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "caller_response", Component: "mcp", Outcome: "not_captured", CaptureState: "not_captured",
		})
	} else if !callerResponseDelivered {
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "caller_response", Component: "mcp", Outcome: "not_delivered", CaptureState: "not_delivered",
		})
	} else if len(callerResponse) > 0 {
		capture := captureRememberDiagnosticBody(callerResponse, protector, authenticatedSecrets...)
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "caller_response", Component: "mcp", ResponseBody: capture.body, ResponseContentType: "application/json", Outcome: "captured", CaptureState: capture.state, CaptureReason: capture.reason,
		})
	} else {
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "caller_response", Component: "mcp", Outcome: "not_captured", CaptureState: "not_captured",
		})
	}
	boundRememberDiagnosticItems(items)
	return items
}

func rememberCallerResponseDelivered(ctx context.Context, _ error) bool {
	if requestCtx := rememberapp.CallerResponseRequestContextFromContext(ctx); requestCtx != nil {
		return requestCtx.Err() == nil
	}
	return ctx == nil || ctx.Err() == nil
}

func boundRememberDiagnosticItems(items []repository.RememberAttemptDiagnosticInput) {
	remaining := rememberDiagnosticMaxAttemptBytes
	for index := range items {
		item := &items[index]
		request, requestTruncated := boundedRememberDiagnosticBody(item.RequestBody)
		response, responseTruncated := boundedRememberDiagnosticBody(item.ResponseBody)
		if remaining <= 0 {
			request, response = nil, nil
			requestTruncated = len(item.RequestBody) > 0
			responseTruncated = len(item.ResponseBody) > 0
		} else if len(request) > remaining {
			request = request[:remaining]
			response = nil
			requestTruncated = true
			responseTruncated = len(item.ResponseBody) > 0
		} else if len(request)+len(response) > remaining {
			limit := remaining - len(request)
			response = response[:limit]
			responseTruncated = true
		}
		item.RequestBody, item.ResponseBody = request, response
		if (requestTruncated || responseTruncated) && item.CaptureState != "unavailable" {
			item.CaptureState = "truncated"
		}
		remaining -= len(request) + len(response)
		if remaining <= 0 {
			for next := index + 1; next < len(items); next++ {
				items[next].RequestBody = nil
				items[next].ResponseBody = nil
				if items[next].CaptureState != "unavailable" {
					items[next].CaptureState = "truncated"
				}
			}
			return
		}
	}
}

type rememberExchangeRecorder struct {
	mu        sync.Mutex
	exchanges []modelprovider.ProviderExchange
	protector observability.DiagnosticProtector
}

func (r *rememberExchangeRecorder) RecordProviderExchange(ctx context.Context, exchange modelprovider.ProviderExchange) {
	if r == nil {
		return
	}
	originalRequestBytes, originalResponseBytes := len(exchange.RequestBody), exchange.ResponseBodySize
	if originalResponseBytes == 0 {
		originalResponseBytes = len(exchange.ResponseBody)
	}
	authenticatedSecrets := observability.AuthenticationSecretsFromContext(ctx)
	requestCapture := captureRememberDiagnosticBody(exchange.RequestBody, r.protector, authenticatedSecrets...)
	exchange.RequestBody = requestCapture.body
	responseBody := exchange.ResponseBody
	if len(exchange.ResponseBodyProjection) > 0 {
		responseBody = exchange.ResponseBodyProjection
	}
	responseCapture := captureRememberDiagnosticBody(responseBody, r.protector, authenticatedSecrets...)
	exchange.ResponseBody = responseCapture.body
	exchange.CaptureState, exchange.CaptureReason = combineRememberDiagnosticCapture(
		rememberDiagnosticCaptureStateForExchange(exchange), exchange.CaptureReason, requestCapture, responseCapture,
	)
	var requestTruncated, responseTruncated bool
	exchange.RequestBody, requestTruncated = boundedRememberDiagnosticBody(exchange.RequestBody)
	exchange.ResponseBody, responseTruncated = boundedRememberDiagnosticBody(exchange.ResponseBody)
	if exchange.CaptureState == "" {
		exchange.CaptureState = rememberDiagnosticCaptureStateForExchange(exchange)
	}
	if (requestTruncated || responseTruncated || originalRequestBytes > rememberDiagnosticMaxBodyBytes || originalResponseBytes > rememberDiagnosticMaxBodyBytes) && exchange.CaptureState != "unavailable" {
		exchange.CaptureState = "truncated"
	}
	r.mu.Lock()
	r.exchanges = append(r.exchanges, exchange)
	r.mu.Unlock()
}

func (r *rememberExchangeRecorder) Snapshot() []modelprovider.ProviderExchange {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]modelprovider.ProviderExchange, len(r.exchanges))
	remaining := rememberDiagnosticMaxAttemptBytes
	for index, exchange := range r.exchanges {
		bounded := exchange
		request, requestTruncated := boundedRememberDiagnosticBody(exchange.RequestBody)
		response, responseTruncated := boundedRememberDiagnosticBody(exchange.ResponseBody)
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
			bounded.CaptureState = rememberDiagnosticCaptureStateForExchange(bounded)
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
