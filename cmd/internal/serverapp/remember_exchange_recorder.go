package serverapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
	"github.com/markhuangai/dense-mem/internal/tools/registry"
)

const (
	rememberDiagnosticMaxBodyBytes    = modelprovider.MaxProviderDiagnosticBodyBytes
	rememberDiagnosticMaxAttemptBytes = 64 << 20
)

var (
	rememberDiagnosticJSONSecretPattern       = regexp.MustCompile(`(?is)("(?:authorization|proxy-authorization|api[_-]?key|client[_-]?secret|password|token|access[_-]?token|refresh[_-]?token|secret|stack(?:[_-]?trace)?|traceback|backtrace|database[_ -]?error|db[_ -]?error|sqlstate)"\s*:\s*)(?:"(?:\\.|[^"\\])*"|null|true|false|-?[0-9]+(?:\.[0-9]+)?)`)
	rememberDiagnosticAuthorizationPattern    = regexp.MustCompile(`(?i)(\b(?:authorization|proxy-authorization)\s*:\s*(?:bearer|basic)\s+)[^\s,}\]]+`)
	rememberDiagnosticBearerPattern           = regexp.MustCompile(`(?i)\b(?:bearer|basic)\s+[A-Za-z0-9._~+/-]{8,}`)
	rememberDiagnosticProviderSecretPattern   = regexp.MustCompile(`(?i)\b(?:sk|rk|pk|api[_-]?key|token)[_-][A-Za-z0-9][A-Za-z0-9_-]{8,}\b`)
	rememberDiagnosticAssignmentSecretPattern = regexp.MustCompile(`(?i)(\b(?:api[_-]?key|client[_-]?secret|password|token|access[_-]?token|refresh[_-]?token|secret)\s*[:=]\s*)(?:"(?:\\.|[^"\\])*"|'[^'\r\n]*'|[^\s,;}\]]+)`)
	rememberDiagnosticStackPattern            = regexp.MustCompile(`(?im)(\b(?:stack(?:[_ -]?trace)?|traceback|backtrace|panic|goroutine)\b\s*[:=]?\s*)[^\r\n]+`)
	rememberDiagnosticMultilineStackPattern   = regexp.MustCompile(`(?im)(^|\n)[ \t]*(?:stack(?:[_ -]?trace)?|traceback|backtrace|panic|goroutine)\b[^\r\n]*(?:\r?\n[^\r\n]*){0,8}`)
	rememberDiagnosticStackFramePattern       = regexp.MustCompile(`(?im)(^|\n)[ \t]*(?:[A-Za-z_][A-Za-z0-9_./]*\.[A-Za-z0-9_]+\([^\r\n]*\)|(?:/|[A-Za-z]:\\)[^\r\n]*:\d+(?:\s+\+0x[0-9a-f]+)?)\s*$`)
	rememberDiagnosticDatabasePattern         = regexp.MustCompile(`(?im)(\b(?:database|db|sql)\s*(?:error|exception|failure)\b\s*[:=]?\s*)[^\r\n]+`)
	rememberDiagnosticDriverErrorPattern      = regexp.MustCompile(`(?im)(\b(?:pq|pgx|postgres(?:ql)?|lib/pq)(?:\s+(?:driver\s+)?error)?\s*:\s*)[^\r\n]+`)
	rememberDiagnosticSQLStatePattern         = regexp.MustCompile(`(?im)(\b(?:sqlstate|database/sql|driver\s+error)\s*[:=]\s*)[^\r\n]+`)
	rememberDiagnosticSQLStateLinePattern     = regexp.MustCompile(`(?im)(^|\n)[^\r\n]*\bSQLSTATE\s+[0-9A-Z]{5}\b[^\r\n]*`)
	rememberDiagnosticPostgresErrorPattern    = regexp.MustCompile(`(?im)(\b(?:fatal|error)\s*:\s*)(?:password authentication failed|no pg_hba\.conf entry|database [^\r\n]*|role [^\r\n]*|connection [^\r\n]*)[^\r\n]*`)
)

func redactRememberDiagnosticContent(body []byte) []byte {
	body = rememberDiagnosticJSONSecretPattern.ReplaceAll(body, []byte(`${1}"[REDACTED]"`))
	body = rememberDiagnosticAuthorizationPattern.ReplaceAll(body, []byte(`${1}[REDACTED]`))
	body = rememberDiagnosticBearerPattern.ReplaceAll(body, []byte(`[REDACTED]`))
	body = rememberDiagnosticProviderSecretPattern.ReplaceAll(body, []byte(`[REDACTED]`))
	body = rememberDiagnosticAssignmentSecretPattern.ReplaceAll(body, []byte(`${1}[REDACTED]`))
	body = rememberDiagnosticMultilineStackPattern.ReplaceAll(body, []byte(`${1}[REDACTED]`))
	body = rememberDiagnosticStackPattern.ReplaceAll(body, []byte(`${1}[REDACTED]`))
	body = rememberDiagnosticStackFramePattern.ReplaceAll(body, []byte(`${1}[REDACTED]`))
	body = rememberDiagnosticDatabasePattern.ReplaceAll(body, []byte(`${1}[REDACTED]`))
	body = rememberDiagnosticDriverErrorPattern.ReplaceAll(body, []byte(`${1}[REDACTED]`))
	body = rememberDiagnosticSQLStatePattern.ReplaceAll(body, []byte(`${1}[REDACTED]`))
	body = rememberDiagnosticSQLStateLinePattern.ReplaceAll(body, []byte(`${1}[REDACTED]`))
	body = rememberDiagnosticPostgresErrorPattern.ReplaceAll(body, []byte(`${1}[REDACTED]`))
	return body
}

func boundedRememberDiagnosticBody(body []byte) ([]byte, bool) {
	if len(body) == 0 {
		return nil, false
	}
	sanitized := redactRememberDiagnosticContent(body)
	truncated := false
	if len(sanitized) > rememberDiagnosticMaxBodyBytes {
		sanitized = sanitized[:rememberDiagnosticMaxBodyBytes]
		truncated = true
	}
	return append([]byte(nil), sanitized...), truncated
}

func rememberDiagnosticBodyState(body []byte) ([]byte, bool) {
	return boundedRememberDiagnosticBody(body)
}

func rememberDiagnosticCaptureStateForExchange(exchange modelprovider.ProviderExchange) string {
	if exchange.CaptureState != "" {
		return exchange.CaptureState
	}
	switch exchange.Outcome {
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
		if len(exchange.RequestBody) == 0 && len(exchange.ResponseBody) == 0 {
			return "not_captured"
		}
		return "captured"
	}
}

func rememberFailureDiagnostics(
	input rememberapp.RememberProcessRequest,
	publicResult map[string]any,
	exchanges []modelprovider.ProviderExchange,
	callerResponse []byte,
	callerResponseDelivered bool,
	_ string,
) []repository.RememberAttemptDiagnosticInput {
	if !callerResponseDelivered {
		callerResponse = nil
	} else if len(callerResponse) == 0 && publicResult != nil {
		if encoded, err := json.Marshal(registry.ToolCallerResponse(publicResult, true)); err == nil {
			callerResponse = encoded
		}
	}
	items := make([]repository.RememberAttemptDiagnosticInput, 0, len(exchanges)+2)
	if len(input.OriginalRequest) > 0 {
		if input.SecurityRejected {
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
			requestBody, truncated := rememberDiagnosticBodyState(input.OriginalRequest)
			captureState := "captured"
			if truncated {
				captureState = "truncated"
			}
			items = append(items, repository.RememberAttemptDiagnosticInput{
				SequenceNo: 1, Kind: "original_request", Component: "remember",
				RequestBody: requestBody, RequestContentType: "application/json", Outcome: "captured", CaptureState: captureState,
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
		requestBody, requestTruncated := rememberDiagnosticBodyState(exchange.RequestBody)
		responseBody, responseTruncated := rememberDiagnosticBodyState(exchange.ResponseBody)
		captureState := rememberDiagnosticCaptureStateForExchange(exchange)
		if requestTruncated || responseTruncated {
			captureState = "truncated"
		}
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "provider_exchange", Component: exchange.Component,
			Model: exchange.Model, RequestBody: requestBody,
			ResponseBody:       responseBody,
			RequestContentType: exchange.RequestContentType, ResponseContentType: exchange.ResponseContentType,
			StatusCode: exchange.StatusCode, Outcome: outcome, CaptureState: captureState,
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
	if !callerResponseDelivered {
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "caller_response", Component: "mcp", Outcome: "not_delivered", CaptureState: "not_delivered",
		})
	} else if len(callerResponse) > 0 {
		responseBody, truncated := rememberDiagnosticBodyState(callerResponse)
		captureState := "captured"
		if truncated {
			captureState = "truncated"
		}
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "caller_response", Component: "mcp", ResponseBody: responseBody, ResponseContentType: "application/json", Outcome: "captured", CaptureState: captureState,
		})
	} else {
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "caller_response", Component: "mcp", Outcome: "not_captured", CaptureState: "not_captured",
		})
	}
	boundRememberDiagnosticItems(items)
	return items
}

func rememberCallerResponseDelivered(ctx context.Context, failure error) bool {
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	return !errors.Is(failure, context.Canceled) &&
		!errors.Is(failure, context.DeadlineExceeded)
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
		if requestTruncated || responseTruncated {
			item.CaptureState = "truncated"
		}
		remaining -= len(request) + len(response)
		if remaining <= 0 {
			for next := index + 1; next < len(items); next++ {
				items[next].RequestBody = nil
				items[next].ResponseBody = nil
				items[next].CaptureState = "truncated"
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
	originalRequestBytes, originalResponseBytes := len(exchange.RequestBody), len(exchange.ResponseBody)
	exchange.RequestBody, exchange.ResponseBody = modelprovider.ProjectProviderExchangeBodies(exchange.Component, exchange.RequestBody, exchange.ResponseBody)
	var requestTruncated, responseTruncated bool
	exchange.RequestBody, requestTruncated = boundedRememberDiagnosticBody(exchange.RequestBody)
	exchange.ResponseBody, responseTruncated = boundedRememberDiagnosticBody(exchange.ResponseBody)
	if exchange.CaptureState == "" {
		exchange.CaptureState = rememberDiagnosticCaptureStateForExchange(exchange)
	}
	if requestTruncated || responseTruncated || originalRequestBytes > rememberDiagnosticMaxBodyBytes || originalResponseBytes > rememberDiagnosticMaxBodyBytes {
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
		if requestTruncated || responseTruncated {
			bounded.CaptureState = "truncated"
		}
		result[index] = bounded
		remaining -= len(request) + len(response)
		if remaining <= 0 {
			for next := index + 1; next < len(result); next++ {
				result[next] = r.exchanges[next]
				result[next].RequestBody = nil
				result[next].ResponseBody = nil
				result[next].CaptureState = "truncated"
			}
			return result
		}
	}
	return result
}
