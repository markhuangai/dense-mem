package domain

import "testing"

func TestEvidenceSecurityEnumsValidate(t *testing.T) {
	cases := []struct {
		name  string
		valid bool
	}{
		{string(EvidenceSecurityPass), EvidenceSecurityDecision(EvidenceSecurityPass).IsValid()},
		{string(EvidenceSecurityGuarded), EvidenceSecurityDecision(EvidenceSecurityGuarded).IsValid()},
		{string(EvidenceSecurityQuarantine), EvidenceSecurityDecision(EvidenceSecurityQuarantine).IsValid()},
		{"invalid decision", EvidenceSecurityDecision("invalid").IsValid()},
		{string(EvidenceSecurityEventDeterministicScan), EvidenceSecurityEventKind(EvidenceSecurityEventDeterministicScan).IsValid()},
		{string(EvidenceSecurityEventReviewerSignal), EvidenceSecurityEventKind(EvidenceSecurityEventReviewerSignal).IsValid()},
		{string(EvidenceSecurityEventVerifierSignal), EvidenceSecurityEventKind(EvidenceSecurityEventVerifierSignal).IsValid()},
		{string(EvidenceSecurityEventQuarantineRelease), EvidenceSecurityEventKind(EvidenceSecurityEventQuarantineRelease).IsValid()},
		{"invalid event", EvidenceSecurityEventKind("invalid").IsValid()},
		{string(EvidenceSignalRoleControlSpoofing), EvidenceSecuritySignalKind(EvidenceSignalRoleControlSpoofing).IsValid()},
		{string(EvidenceSignalInstructionOverride), EvidenceSecuritySignalKind(EvidenceSignalInstructionOverride).IsValid()},
		{string(EvidenceSignalPromptSecretExtraction), EvidenceSecuritySignalKind(EvidenceSignalPromptSecretExtraction).IsValid()},
		{string(EvidenceSignalToolExfiltration), EvidenceSecuritySignalKind(EvidenceSignalToolExfiltration).IsValid()},
		{string(EvidenceSignalObfuscatedInstruction), EvidenceSecuritySignalKind(EvidenceSignalObfuscatedInstruction).IsValid()},
		{string(EvidenceSignalHiddenControlMarkup), EvidenceSecuritySignalKind(EvidenceSignalHiddenControlMarkup).IsValid()},
		{"invalid signal", EvidenceSecuritySignalKind("invalid").IsValid()},
		{string(EvidenceSecuritySeverityLow), EvidenceSecuritySeverity(EvidenceSecuritySeverityLow).IsValid()},
		{string(EvidenceSecuritySeverityMedium), EvidenceSecuritySeverity(EvidenceSecuritySeverityMedium).IsValid()},
		{string(EvidenceSecuritySeverityHigh), EvidenceSecuritySeverity(EvidenceSecuritySeverityHigh).IsValid()},
		{string(EvidenceSecuritySeverityCritical), EvidenceSecuritySeverity(EvidenceSecuritySeverityCritical).IsValid()},
		{"invalid severity", EvidenceSecuritySeverity("invalid").IsValid()},
	}

	for _, tc := range cases {
		want := tc.name != "invalid decision" && tc.name != "invalid event" &&
			tc.name != "invalid signal" && tc.name != "invalid severity"
		if tc.valid != want {
			t.Fatalf("%s valid = %v, want %v", tc.name, tc.valid, want)
		}
	}
}
