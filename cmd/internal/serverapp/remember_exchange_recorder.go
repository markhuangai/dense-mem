package serverapp

import (
	"context"
	"regexp"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

const (
	rememberDiagnosticMaxBodyBytes    = 16 << 20
	rememberDiagnosticMaxAttemptBytes = 64 << 20
)

var rememberDiagnosticSecretPattern = regexp.MustCompile(`(?i)("(?:authorization|api[_-]?key|password|token|secret|stack|database_error)"\s*:\s*)"[^"]*"`)

func boundedRememberDiagnosticBody(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return nil, false
	}
	truncated := false
	if len(body) > rememberDiagnosticMaxBodyBytes {
		body = body[:rememberDiagnosticMaxBodyBytes]
		truncated = true
	}
	sanitized := rememberDiagnosticSecretPattern.ReplaceAll(body, []byte(`${1}"[REDACTED]"`))
	return append([]byte(nil), sanitized...), truncated
}

func sanitizeRememberDiagnosticContent(body []byte) []byte {
	sanitized, _ := boundedRememberDiagnosticBody(body)
	return sanitized
}

func rememberFailureDiagnostics(
	input rememberapp.RememberProcessRequest,
	publicResult map[string]any,
	exchanges []modelprovider.ProviderExchange,
	callerResponse []byte,
	_ string,
	_ string,
) []repository.RememberAttemptDiagnosticInput {
	items := make([]repository.RememberAttemptDiagnosticInput, 0, len(exchanges)+2)
	if len(input.OriginalRequest) > 0 {
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: 1, Kind: "original_request", Component: "remember",
			RequestBody:        sanitizeRememberDiagnosticContent(input.OriginalRequest),
			RequestContentType: "application/json", Outcome: "captured",
		})
	} else {
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: 1, Kind: "original_request", Component: "remember", Outcome: "not_captured",
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
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "provider_exchange", Component: exchange.Component,
			Model: exchange.Model, RequestBody: sanitizeRememberDiagnosticContent(exchange.RequestBody),
			ResponseBody:       sanitizeRememberDiagnosticContent(exchange.ResponseBody),
			RequestContentType: exchange.RequestContentType, ResponseContentType: exchange.ResponseContentType,
			StatusCode: exchange.StatusCode, Outcome: outcome,
			CapturedAt: capturedAt, ExpiresAt: capturedAt.Add(7 * 24 * time.Hour),
		})
		sequence++
	}
	if len(exchanges) == 0 {
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "provider_exchange", Component: "provider", Outcome: "provider_not_called",
		})
		sequence++
	}
	if len(callerResponse) > 0 {
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "caller_response", Component: "mcp", ResponseBody: sanitizeRememberDiagnosticContent(callerResponse), ResponseContentType: "application/json", Outcome: "captured",
		})
	} else {
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "caller_response", Component: "mcp", Outcome: "not_captured",
		})
	}
	boundRememberDiagnosticItems(items)
	return items
}

func boundRememberDiagnosticItems(items []repository.RememberAttemptDiagnosticInput) {
	remaining := rememberDiagnosticMaxAttemptBytes
	for index := range items {
		item := &items[index]
		request, requestTruncated := boundedRememberDiagnosticBody(item.RequestBody)
		response, responseTruncated := boundedRememberDiagnosticBody(item.ResponseBody)
		if len(request) > remaining {
			request = request[:remaining]
			response = nil
			responseTruncated = len(item.ResponseBody) > 0
		} else if len(request)+len(response) > remaining {
			limit := remaining - len(request)
			response = response[:limit]
			responseTruncated = true
		}
		item.RequestBody, item.ResponseBody = request, response
		if requestTruncated || responseTruncated {
			item.Outcome = "truncated"
		}
		remaining -= len(request) + len(response)
		if remaining <= 0 {
			for next := index + 1; next < len(items); next++ {
				items[next].RequestBody = nil
				items[next].ResponseBody = nil
				items[next].Outcome = "truncated"
			}
			return
		}
	}
}

type rememberExchangeRecorder struct {
	mu        sync.Mutex
	exchanges []modelprovider.ProviderExchange
}

func (r *rememberExchangeRecorder) RecordProviderExchange(_ context.Context, exchange modelprovider.ProviderExchange) {
	if r == nil {
		return
	}
	var requestTruncated, responseTruncated bool
	exchange.RequestBody, requestTruncated = boundedRememberDiagnosticBody(exchange.RequestBody)
	exchange.ResponseBody, responseTruncated = boundedRememberDiagnosticBody(exchange.ResponseBody)
	if requestTruncated || responseTruncated {
		exchange.Outcome = "truncated"
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
		result[index] = exchange
		request, requestTruncated := boundedRememberDiagnosticBody(exchange.RequestBody)
		response, responseTruncated := boundedRememberDiagnosticBody(exchange.ResponseBody)
		if len(request)+len(response) > remaining {
			if remaining < len(request) {
				request = request[:remaining]
				response = nil
			} else {
				responseLimit := remaining - len(request)
				if responseLimit < len(response) {
					response = response[:responseLimit]
				}
			}
			result[index].Outcome = "truncated"
		}
		if requestTruncated || responseTruncated {
			result[index].Outcome = "truncated"
		}
		result[index].RequestBody = request
		result[index].ResponseBody = response
		remaining -= len(request) + len(response)
		if remaining <= 0 {
			return result[:index+1]
		}
	}
	return result
}
