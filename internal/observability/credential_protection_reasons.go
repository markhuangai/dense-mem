package observability

import "strconv"

func (r CredentialProtectionUnavailableReason) CaptureReason(maxBytes int) string {
	reason := "credential_protection_" + strconv.Itoa(int(r))
	if r >= CredentialProtectionProcessingBudgetExceeded && r <= CredentialProtectionBodyBudgetExceeded {
		unit, limit := r.diagnosticLimit(maxBytes)
		reason += ":" + r.Code() + ":" + unit + "=" + strconv.Itoa(limit)
	}
	return reason
}

func (r CredentialProtectionUnavailableReason) Code() string {
	switch r {
	case CredentialProtectionAvailable:
		return "available"
	case CredentialProtectionInvalidBudget:
		return "invalid_budget"
	case CredentialProtectionBudgetExceeded:
		return "budget_exceeded_unspecified"
	case CredentialProtectionDepthExceeded:
		return "depth_exceeded"
	case CredentialProtectionCycleDetected:
		return "cycle_detected"
	case CredentialProtectionUnsupported:
		return "unsupported"
	case CredentialProtectionInvalidEncoding:
		return "invalid_encoding"
	case CredentialProtectionFormattingFailed:
		return "formatting_failed"
	case CredentialProtectionEncodingLimitExceeded:
		return "encoding_layers_exceeded"
	case CredentialProtectionSecretTooLong:
		return "secret_too_long"
	case CredentialProtectionProcessingBudgetExceeded:
		return "processing_bytes_exceeded"
	case CredentialProtectionEncodedBudgetExceeded:
		return "encoded_bytes_exceeded"
	case CredentialProtectionMatchingBudgetExceeded:
		return "matching_work_exceeded"
	case CredentialProtectionBodyBudgetExceeded:
		return "body_bytes_exceeded"
	}
	return "unknown"
}

func (r CredentialProtectionUnavailableReason) diagnosticLimit(maxBytes int) (string, int) {
	switch r {
	case CredentialProtectionProcessingBudgetExceeded:
		return "processing_bytes", max(0, maxBytes+2)
	case CredentialProtectionEncodedBudgetExceeded:
		return "encoded_bytes", max(0, maxBytes+2)
	case CredentialProtectionMatchingBudgetExceeded:
		return "matching_work", credentialMatchWorkLimit(maxBytes + 2)
	case CredentialProtectionBodyBudgetExceeded:
		return "body_bytes", max(0, maxBytes)
	}
	return "", 0
}
