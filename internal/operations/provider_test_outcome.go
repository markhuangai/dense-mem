package operations

import (
	"context"
	"errors"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
)

func classifyProviderTest(result *ProviderTestResult, contextErr, failure error) {
	result.NetworkState = "not_dispatched"
	for _, exchange := range result.Exchanges {
		result.HTTPStatus = nil
		result.NetworkState = "no_http_response"
		if exchange.StatusCode != 0 {
			status := exchange.StatusCode
			result.HTTPStatus, result.NetworkState = &status, "http_response_received"
		}
		result.FailurePhase = exchange.FailurePhase
		if exchange.CaptureReason != "" || exchange.RequestCaptureState == "unavailable" || exchange.ResponseCaptureState == "unavailable" || exchange.ResponseCaptureState == "interrupted" {
			result.CaptureState = "incomplete"
		}
	}
	result.Outcome, result.Reason, result.Guidance = "succeeded", "sample_validated", "The sampled provider operation completed successfully."
	if failure == nil && contextErr == nil {
		result.FailurePhase = ""
		return
	}
	result.Outcome = "failed"
	if result.FailurePhase == "" && result.Diagnostics != nil {
		result.FailurePhase = result.Diagnostics.FailurePhase
	}
	if result.FailurePhase == "" {
		result.FailurePhase = "before_dispatch"
		if result.HTTPStatus != nil {
			result.FailurePhase = "validation"
		}
	}
	switch {
	case errors.Is(contextErr, context.Canceled) || errors.Is(failure, context.Canceled):
		result.Outcome, result.Reason, result.Guidance = "cancelled", "cancelled", "The request was cancelled; no successful test is inferred."
	case errors.Is(contextErr, context.DeadlineExceeded) || errors.Is(failure, modelprovider.ErrVerifierTimeout):
		result.Reason, result.Guidance = "timeout", "The configured deadline expired. Check the observed phase and timings before diagnosing a network outage."
	case result.HTTPStatus != nil && (*result.HTTPStatus == 401 || *result.HTTPStatus == 403):
		result.Reason, result.FailurePhase, result.Guidance = "authentication_failed", "response_status", "An HTTP response arrived. Check the configured provider credential and model permissions."
	case result.HTTPStatus != nil && *result.HTTPStatus == 429:
		result.Reason, result.FailurePhase, result.Guidance = "rate_limited", "response_status", "An HTTP response arrived. Check provider quota and rate limits before retrying."
	case result.HTTPStatus != nil && *result.HTTPStatus != 200:
		result.Reason, result.FailurePhase, result.Guidance = "provider_http_error", "response_status", "An HTTP response arrived. Inspect the protected exchange and provider or gateway error."
	case result.FailurePhase == "dns":
		result.Reason, result.Guidance = "dns_failed", "Check name resolution for the configured provider from this server."
	case result.FailurePhase == "connect":
		result.Reason, result.Guidance = "connect_failed", "Check the provider address, routing, proxy, firewall, and listening port from this server."
	case result.FailurePhase == "tls":
		result.Reason, result.Guidance = "tls_failed", "Check certificate trust, hostname, and any TLS-intercepting proxy on this server."
	case result.FailurePhase == "response_body":
		result.Reason, result.Guidance = "response_read_failed", "The response was interrupted. Inspect the exchange and connection or gateway failure."
	case result.HTTPStatus != nil:
		result.Reason, result.Guidance = "invalid_response", "An HTTP response arrived, but the existing provider operation rejected its envelope or sample result. Inspect validation diagnostics."
	case len(result.Exchanges) == 0:
		result.Reason, result.Guidance = "not_dispatched", "No outbound exchange was observed. Check provider capacity, configuration, and sample preparation."
	default:
		result.Reason, result.Guidance = "transport_failed", "No HTTP response was observed. Inspect the protected transport error; the network cause is not established."
	}
}
