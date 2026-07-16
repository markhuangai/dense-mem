package domain

type EvidenceSecurityDecision string

const (
	EvidenceSecurityPass       EvidenceSecurityDecision = "pass"
	EvidenceSecurityGuarded    EvidenceSecurityDecision = "guarded"
	EvidenceSecurityQuarantine EvidenceSecurityDecision = "quarantine"
)

func (d EvidenceSecurityDecision) IsValid() bool {
	switch d {
	case EvidenceSecurityPass, EvidenceSecurityGuarded, EvidenceSecurityQuarantine:
		return true
	default:
		return false
	}
}

type EvidenceSecurityEventKind string

const (
	EvidenceSecurityEventDeterministicScan EvidenceSecurityEventKind = "deterministic_scan"
	EvidenceSecurityEventReviewerSignal    EvidenceSecurityEventKind = "reviewer_signal"
	EvidenceSecurityEventVerifierSignal    EvidenceSecurityEventKind = "verifier_signal"
	EvidenceSecurityEventQuarantineRelease EvidenceSecurityEventKind = "quarantine_release"
)

func (k EvidenceSecurityEventKind) IsValid() bool {
	switch k {
	case EvidenceSecurityEventDeterministicScan, EvidenceSecurityEventReviewerSignal,
		EvidenceSecurityEventVerifierSignal, EvidenceSecurityEventQuarantineRelease:
		return true
	default:
		return false
	}
}

type EvidenceSecuritySignalKind string

const (
	EvidenceSignalRoleControlSpoofing    EvidenceSecuritySignalKind = "role_control_spoofing"
	EvidenceSignalInstructionOverride    EvidenceSecuritySignalKind = "instruction_override"
	EvidenceSignalPromptSecretExtraction EvidenceSecuritySignalKind = "prompt_secret_extraction"
	EvidenceSignalToolExfiltration       EvidenceSecuritySignalKind = "tool_exfiltration"
	EvidenceSignalObfuscatedInstruction  EvidenceSecuritySignalKind = "obfuscated_instruction"
	EvidenceSignalHiddenControlMarkup    EvidenceSecuritySignalKind = "hidden_control_markup"
)

func (k EvidenceSecuritySignalKind) IsValid() bool {
	switch k {
	case EvidenceSignalRoleControlSpoofing, EvidenceSignalInstructionOverride,
		EvidenceSignalPromptSecretExtraction, EvidenceSignalToolExfiltration,
		EvidenceSignalObfuscatedInstruction, EvidenceSignalHiddenControlMarkup:
		return true
	default:
		return false
	}
}

type EvidenceSecuritySeverity string

const (
	EvidenceSecuritySeverityLow      EvidenceSecuritySeverity = "low"
	EvidenceSecuritySeverityMedium   EvidenceSecuritySeverity = "medium"
	EvidenceSecuritySeverityHigh     EvidenceSecuritySeverity = "high"
	EvidenceSecuritySeverityCritical EvidenceSecuritySeverity = "critical"
)

func (s EvidenceSecuritySeverity) IsValid() bool {
	switch s {
	case EvidenceSecuritySeverityLow, EvidenceSecuritySeverityMedium,
		EvidenceSecuritySeverityHigh, EvidenceSecuritySeverityCritical:
		return true
	default:
		return false
	}
}

type EvidenceSecuritySignal struct {
	Kind      EvidenceSecuritySignalKind `json:"kind"`
	Severity  EvidenceSecuritySeverity   `json:"severity"`
	SpanStart int                        `json:"span_start"`
	SpanEnd   int                        `json:"span_end"`
	Quote     string                     `json:"quote,omitempty"`
}

type EvidenceSecurityAssessment struct {
	Decision       EvidenceSecurityDecision  `json:"decision"`
	EventKind      EvidenceSecurityEventKind `json:"event_kind"`
	ScanPolicyHash string                    `json:"scan_policy_hash,omitempty"`
	Reason         string                    `json:"reason,omitempty"`
	Signals        []EvidenceSecuritySignal  `json:"signals,omitempty"`
}
