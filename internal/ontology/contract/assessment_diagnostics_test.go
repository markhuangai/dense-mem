package contract

import (
	"encoding/json"
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
