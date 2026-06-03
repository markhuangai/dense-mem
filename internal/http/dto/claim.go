package dto

import (
	"time"

	"github.com/go-playground/validator/v10"

	httpvalidation "github.com/markhuangai/dense-mem/internal/http/validation"
)

func init() {
	// Register cross-field date validation: valid_from must not be after valid_to.
	// Security invariant: this struct-level check is the authoritative enforcement
	// point for temporal ordering — do not bypass it in handlers.
	httpvalidation.Validator.RegisterStructValidation(validateClaimRequestDates, CreateClaimRequest{})
}

// validateClaimRequestDates enforces that valid_from <= valid_to when both are present.
func validateClaimRequestDates(sl validator.StructLevel) {
	req := sl.Current().Interface().(CreateClaimRequest)
	if req.ValidFrom != nil && req.ValidTo != nil && req.ValidFrom.After(*req.ValidTo) {
		sl.ReportError(req.ValidTo, "valid_to", "ValidTo", "gtefield", "ValidFrom")
	}
}

// CreateClaimRequest represents a request to create a new claim.
// Validation rules:
//   - SupportedBy: required, min 1 element, each element must be a UUID (any version)
//   - Subject: optional, max 256 characters
//   - Predicate: optional, max 128 characters
//   - Object: optional, max 1024 characters
//   - Modality: optional, oneof assertion question proposal speculation quoted
//   - Polarity: optional, oneof + -
//   - Speaker: optional, max 256 characters
//   - ExtractConf: optional, float in [0,1]
//   - ResolutionConf: optional, float in [0,1]
//   - IdempotencyKey: optional, max 128 characters
//   - ValidFrom / ValidTo: optional; when both present, ValidFrom must not be after ValidTo
type CreateClaimRequest struct {
	SupportedBy    []string   `json:"supported_by" validate:"required,min=1,dive,uuid"`
	Subject        string     `json:"subject,omitempty" validate:"max=256"`
	Predicate      string     `json:"predicate,omitempty" validate:"max=128"`
	Object         string     `json:"object,omitempty" validate:"max=1024"`
	Modality       string     `json:"modality,omitempty" validate:"omitempty,oneof=assertion question proposal speculation quoted"`
	Polarity       string     `json:"polarity,omitempty" validate:"omitempty,oneof=+ -"`
	Speaker        string     `json:"speaker,omitempty" validate:"max=256"`
	ExtractConf    float64    `json:"extract_conf,omitempty" validate:"gte=0,lte=1"`
	ResolutionConf float64    `json:"resolution_conf,omitempty" validate:"gte=0,lte=1"`
	IdempotencyKey string     `json:"idempotency_key,omitempty" validate:"max=128"`
	ValidFrom      *time.Time `json:"valid_from,omitempty"`
	ValidTo        *time.Time `json:"valid_to,omitempty"`
}

// ClaimResponse represents a claim in API responses.
type ClaimResponse struct {
	ClaimID              string         `json:"claim_id"`
	ProfileID            string         `json:"team_id"`
	OwnerProfileID       string         `json:"owner_profile_id,omitempty"`
	OwnerProfileName     string         `json:"owner_profile_name,omitempty"`
	CreatedByProfileID   string         `json:"created_by_profile_id,omitempty"`
	CreatedByProfileName string         `json:"created_by_profile_name,omitempty"`
	Subject              string         `json:"subject"`
	Predicate            string         `json:"predicate"`
	Object               string         `json:"object"`
	Modality             string         `json:"modality"`
	Polarity             string         `json:"polarity"`
	Speaker              string         `json:"speaker,omitempty"`
	SpanStart            int            `json:"span_start"`
	SpanEnd              int            `json:"span_end"`
	ValidFrom            *time.Time     `json:"valid_from,omitempty"`
	ValidTo              *time.Time     `json:"valid_to,omitempty"`
	RecordedAt           time.Time      `json:"recorded_at"`
	RecordedTo           *time.Time     `json:"recorded_to,omitempty"`
	ExtractConf          float64        `json:"extract_conf"`
	ResolutionConf       float64        `json:"resolution_conf"`
	SourceQuality        float64        `json:"source_quality"`
	EntailmentVerdict    string         `json:"entailment_verdict"`
	Status               string         `json:"status"`
	ExtractionModel      string         `json:"extraction_model"`
	ExtractionVersion    string         `json:"extraction_version"`
	VerifierModel        string         `json:"verifier_model,omitempty"`
	PipelineRunID        string         `json:"pipeline_run_id,omitempty"`
	ContentHash          string         `json:"content_hash"`
	IdempotencyKey       string         `json:"idempotency_key,omitempty"`
	Classification       map[string]any `json:"classification,omitempty"`
	SupportedBy          []string       `json:"supported_by,omitempty"`
	Evidence             []Evidence     `json:"evidence,omitempty"`
}

// ListClaimsRequest represents query parameters for listing claims.
// Validation rules:
//   - Limit: optional, 0-100
//   - Cursor: optional, max 256 characters
//   - Status: optional, oneof candidate validated rejected superseded disputed promoted
//   - Modality: optional, oneof assertion question proposal speculation quoted
type ListClaimsRequest struct {
	Limit    int    `query:"limit" validate:"min=0,max=100"`
	Cursor   string `query:"cursor" validate:"max=256"`
	Status   string `query:"status" validate:"omitempty,oneof=candidate validated rejected superseded disputed promoted"`
	Modality string `query:"modality" validate:"omitempty,oneof=assertion question proposal speculation quoted"`
}

// ListClaimsResponse represents a paginated list of claims.
type ListClaimsResponse struct {
	Items      []ClaimResponse `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
	HasMore    bool            `json:"has_more"`
}

// VerifyClaimResponse represents the result of a claim verification operation.
type VerifyClaimResponse struct {
	ClaimID              string     `json:"claim_id"`
	EntailmentVerdict    string     `json:"entailment_verdict"`
	Status               string     `json:"status"`
	LastVerifierResponse string     `json:"last_verifier_response,omitempty"`
	VerifierModel        string     `json:"verifier_model,omitempty"`
	VerifiedAt           *time.Time `json:"verified_at,omitempty"`
}
