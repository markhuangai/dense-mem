package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	repository "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

func rememberFailureDiagnostics(
	input RememberProcessRequest,
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
	input RememberProcessRequest,
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
	input RememberProcessRequest,
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
			capture := observability.CaptureProviderDiagnosticBody(input.OriginalRequest, protector, authenticatedSecrets...)
			items = append(items, repository.RememberAttemptDiagnosticInput{
				SequenceNo: 1, Kind: "original_request", Component: "remember",
				RequestBody: capture.Body, RequestContentType: "application/json", Outcome: "captured", CaptureState: capture.State, CaptureReason: capture.Reason,
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
		requestCapture := observability.CaptureProviderDiagnosticBody(exchange.RequestBody, protector, authenticatedSecrets...)
		responseCapture := observability.CaptureProviderDiagnosticBody(exchange.ResponseBody, protector, authenticatedSecrets...)
		captureState, captureReason := observability.CombineProviderDiagnosticCapture(
			observability.ProviderExchangeCaptureState(exchange), exchange.CaptureReason,
			requestCapture, responseCapture,
		)
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "provider_exchange", Component: exchange.Component,
			Model: exchange.Model, RequestBody: requestCapture.Body,
			ResponseBody:       responseCapture.Body,
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
		capture := observability.CaptureProviderDiagnosticBody(callerResponse, protector, authenticatedSecrets...)
		items = append(items, repository.RememberAttemptDiagnosticInput{
			SequenceNo: sequence, Kind: "caller_response", Component: "mcp", ResponseBody: capture.Body, ResponseContentType: "application/json", Outcome: "captured", CaptureState: capture.State, CaptureReason: capture.Reason,
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
	if requestCtx := CallerResponseRequestContextFromContext(ctx); requestCtx != nil {
		return requestCtx.Err() == nil
	}
	return ctx == nil || ctx.Err() == nil
}

func boundRememberDiagnosticItems(items []repository.RememberAttemptDiagnosticInput) {
	remaining := observability.MaxProviderDiagnosticSnapshotBytes
	for index := range items {
		item := &items[index]
		request, requestTruncated := observability.BoundProviderDiagnosticBody(item.RequestBody)
		response, responseTruncated := observability.BoundProviderDiagnosticBody(item.ResponseBody)
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
