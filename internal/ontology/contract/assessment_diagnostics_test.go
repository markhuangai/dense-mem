package contract

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/stretchr/testify/require"
)

func TestOrganizationReceiptValidationDiagnostics(t *testing.T) {
	source := sourceDependency(t, policySnapshot("diagnostic-source", EvidenceSource, "source"))
	id := policyID("diagnostic-assessment")
	receipt := OrganizationReceipt{ID: id, OperationKey: "diagnostic", InputHash: source.Fingerprint, BatchHash: source.Fingerprint,
		ContextHash: source.Fingerprint, ProviderIdentity: "provider", Sources: []SourceDependency{source},
		Result: OrganizationResult{AssessmentID: id, Outcomes: []OrganizationOutcome{{Source: source.SourceHandle, Status: "failed"}},
			Attempts: []AssessmentAttempt{{Number: 1, FailureCode: "response_invalid"}}}}
	require.NoError(t, ValidateOrganizationReceipt(receipt))
	historical, err := json.Marshal(receipt)
	require.NoError(t, err)
	require.NotContains(t, string(historical), `"validation"`)
	var decoded OrganizationReceipt
	require.NoError(t, jsonstrict.Decode(strings.NewReader(string(historical)), &decoded, MaxPublicationBytes))
	require.NoError(t, ValidateOrganizationReceipt(decoded))
	require.Nil(t, decoded.Result.Attempts[0].Validation)

	expected, actual := 2, 0
	valid := AssessmentValidation{Stage: "validate", Code: "item_count_mismatch", FieldFamily: "items", ExpectedCount: &expected, ActualCount: &actual}
	receipt.Result.Attempts[0].Validation = &valid
	require.NoError(t, ValidateOrganizationReceipt(receipt))
	encoded, err := json.Marshal(receipt)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"actual_count":0`)
	require.NoError(t, jsonstrict.Decode(strings.NewReader(string(encoded)), &decoded, MaxPublicationBytes))
	require.NoError(t, ValidateOrganizationReceipt(decoded))
	require.Equal(t, receipt.Result.Attempts, decoded.Result.Attempts)
	for name, change := range map[string]func(*AssessmentValidation){
		"unknown stage":     func(d *AssessmentValidation) { d.Stage = "raw-source-canary" },
		"unknown code":      func(d *AssessmentValidation) { d.Code = "synthetic-provider-secret" },
		"unknown field":     func(d *AssessmentValidation) { d.FieldFamily = "provider-field" },
		"wrong stage":       func(d *AssessmentValidation) { d.Stage = "decode" },
		"wrong family":      func(d *AssessmentValidation) { d.FieldFamily = "definitions" },
		"unpaired count":    func(d *AssessmentValidation) { d.ActualCount = nil },
		"missing counts":    func(d *AssessmentValidation) { d.ActualCount, d.ExpectedCount = nil, nil },
		"negative count":    func(d *AssessmentValidation) { negative := -1; d.ActualCount = &negative },
		"unexpected counts": func(d *AssessmentValidation) { d.Code = "item_ref_unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			invalid := valid
			change(&invalid)
			receipt.Result.Attempts[0].Validation = &invalid
			require.ErrorIs(t, ValidateOrganizationReceipt(receipt), ErrInvalid)
		})
	}
	receipt.Result.Attempts[0].Validation = &valid
	receipt.Result.Attempts[0].FailureCode = ""
	require.ErrorIs(t, ValidateOrganizationReceipt(receipt), ErrInvalid)
	receipt.Result.Attempts[0].Validation = nil
	require.NoError(t, ValidateOrganizationReceipt(receipt))
	encoded, err = json.Marshal(receipt.Result.Attempts[0])
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"validation"`)
}

func TestOrganizationContextDiagnosticSnapshotPreservesHash(t *testing.T) {
	id := policyID("context-record")
	context := OrganizationContext{Records: []RecordView{{Record: Record{ID: id, Version: 3}, Current: true}}}
	legacyJSON := fmt.Sprintf(`{"Records":[{"ID":%q,"Version":3,"Current":true}],"Candidates":[]}`, id)
	expected := fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(legacyJSON)))
	actual, err := OrganizationContextHash(context)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	snapshot := SnapshotOrganizationContext(context)
	context.Records[0].Version = 4
	require.Equal(t, int64(3), snapshot.Records[0].Version)
}

func TestOrganizationFreshnessDiagnosticsValidateAndPreserveLegacyReceipts(t *testing.T) {
	source := sourceDependency(t, policySnapshot("freshness-source", EvidenceSource, "source"))
	id := policyID("freshness-assessment")
	receipt := OrganizationReceipt{ID: id, OperationKey: "freshness", InputHash: source.Fingerprint, BatchHash: source.Fingerprint,
		ContextHash: source.Fingerprint, ProviderIdentity: "provider", Sources: []SourceDependency{source},
		Result: OrganizationResult{AssessmentID: id, FailureCode: "stale_input", Outcomes: []OrganizationOutcome{{Source: source.SourceHandle, Status: "failed", Reason: "stale_input"}}}}
	require.NoError(t, ValidateOrganizationReceipt(receipt))
	historical, err := json.Marshal(receipt)
	require.NoError(t, err)
	require.NotContains(t, string(historical), "freshness_rejection")
	receipt.Result.FreshnessRejection = &FreshnessRejection{Check: "source_fingerprint", Source: &source.SourceHandle,
		Expected: source.Fingerprint, Observed: "sha256:" + strings.Repeat("a", 64)}
	require.NoError(t, ValidateOrganizationReceipt(receipt))
	encoded, err := json.Marshal(receipt)
	require.NoError(t, err)
	var decoded OrganizationReceipt
	require.NoError(t, jsonstrict.Decode(strings.NewReader(string(encoded)), &decoded, MaxPublicationBytes))
	require.Equal(t, receipt.Result.FreshnessRejection, decoded.Result.FreshnessRejection)
	for _, change := range []func(*FreshnessRejection){
		func(d *FreshnessRejection) { d.Check = "unknown" },
		func(d *FreshnessRejection) { d.Expected = "invalid fingerprint" },
		func(d *FreshnessRejection) { d.Source = nil },
		func(d *FreshnessRejection) { d.RecordID = "not-an-id" },
		func(d *FreshnessRejection) { d.Observed = strings.Repeat("x", 129) },
	} {
		invalid := *receipt.Result.FreshnessRejection
		change(&invalid)
		require.False(t, invalid.Valid())
	}
	require.ErrorIs(t, &FreshnessError{Rejection: *receipt.Result.FreshnessRejection}, ErrSourceStale)
	receipt.Result.FailureCode = "provider_response_invalid"
	require.ErrorIs(t, ValidateOrganizationReceipt(receipt), ErrInvalid)
}
