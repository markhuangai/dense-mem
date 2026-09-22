package dream

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const (
	dreamDiagnosticProviderBodyLimit = 16 << 20
	dreamDiagnosticRunPayloadLimit   = 64 << 20
)

type dreamDiagnosticExchangeRecorder struct {
	protector observability.DiagnosticProtector
	mu        sync.Mutex
	items     []map[string]any
	bytes     int
	attempted bool
	state     string
	reason    string
}

type dreamDiagnosticRecorderContextKey struct{}

func withDreamDiagnosticRecorder(ctx context.Context, recorder *dreamDiagnosticExchangeRecorder) context.Context {
	return context.WithValue(ctx, dreamDiagnosticRecorderContextKey{}, recorder)
}

func dreamDiagnosticRecorderFromContext(ctx context.Context) *dreamDiagnosticExchangeRecorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(dreamDiagnosticRecorderContextKey{}).(*dreamDiagnosticExchangeRecorder)
	return recorder
}

func newDreamDiagnosticExchangeRecorder(protector observability.DiagnosticProtector) *dreamDiagnosticExchangeRecorder {
	return &dreamDiagnosticExchangeRecorder{protector: protector, state: "captured"}
}

func (r *dreamDiagnosticExchangeRecorder) RecordProviderExchange(ctx context.Context, exchange modelprovider.ProviderExchange) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attempted = true
	item := map[string]any{
		"component":     exchange.Component,
		"model":         exchange.Model,
		"outcome":       exchange.Outcome,
		"status_code":   exchange.StatusCode,
		"capture_state": exchange.CaptureState,
	}
	request, requestState := r.protect(ctx, exchange.RequestBody)
	responseBody := exchange.ResponseBody
	if len(exchange.ResponseBodyProjection) > 0 {
		responseBody = exchange.ResponseBodyProjection
	}
	response, responseState := r.protect(ctx, responseBody)
	if requestState == "unavailable" || responseState == "unavailable" {
		r.state = "unavailable"
		r.reason = "credential_protection_unavailable"
	} else if (requestState == "truncated" || responseState == "truncated") && r.state != "unavailable" {
		r.state = "truncated"
		if r.reason == "" {
			r.reason = "provider_body_budget_exceeded"
		}
	}
	if len(request) > 0 {
		item["request_body"] = string(request)
	}
	if len(response) > 0 {
		item["response_body"] = string(response)
	}
	item["request_capture_state"] = requestState
	item["response_capture_state"] = responseState
	encoded, _ := json.Marshal(item)
	if r.bytes+len(encoded) > dreamDiagnosticRunPayloadLimit {
		delete(item, "request_body")
		delete(item, "response_body")
		item["capture_state"] = "truncated"
		item["capture_reason"] = "run_payload_budget_exceeded"
		encoded, _ = json.Marshal(item)
		if r.state != "unavailable" {
			r.state = "truncated"
			r.reason = "run_payload_budget_exceeded"
		}
	}
	if r.bytes+len(encoded) > dreamDiagnosticRunPayloadLimit {
		return
	}
	r.bytes += len(encoded)
	r.items = append(r.items, item)
}

func (r *dreamDiagnosticExchangeRecorder) protect(ctx context.Context, body []byte) ([]byte, string) {
	if len(body) == 0 {
		return nil, "not_captured"
	}
	if r.protector == nil {
		return nil, "unavailable"
	}
	protected, reason := r.protector.ProtectDiagnosticBytes(body, dreamDiagnosticProviderBodyLimit, observability.AuthenticationSecretsFromContext(ctx)...)
	if reason == observability.CredentialProtectionBudgetExceeded {
		if r.state != "unavailable" {
			r.reason = "provider_body_budget_exceeded"
		}
		return nil, "truncated"
	}
	if reason != observability.CredentialProtectionAvailable {
		r.reason = "credential_protection_unavailable"
		return nil, "unavailable"
	}
	return protected, "captured"
}

func (r *dreamDiagnosticExchangeRecorder) Payload() []byte {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.items) == 0 {
		return []byte(`{}`)
	}
	payload, err := json.Marshal(map[string]any{"provider_exchanges": r.items})
	if err != nil || len(payload) > dreamDiagnosticRunPayloadLimit {
		if r.state != "unavailable" {
			r.state = "truncated"
			r.reason = "run_payload_budget_exceeded"
		}
		return []byte(`{"provider_exchanges":[]}`)
	}
	return payload
}

func (r *dreamDiagnosticExchangeRecorder) State() (string, string) {
	if r == nil {
		return "not_captured", "provider_not_called"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.attempted {
		return "not_captured", "provider_not_called"
	}
	return r.state, r.reason
}
