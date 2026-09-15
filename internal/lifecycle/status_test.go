package lifecycle

import (
	"strings"
	"testing"
)

func TestSubmissionStatusExportsAndErrorMapping(t *testing.T) {
	codes := SubmissionErrorCodes()
	if len(codes) == 0 || !strings.Contains(strings.Join(codes, ","), string(SubmissionErrorProviderUnavailable)) {
		t.Fatalf("submission error codes = %#v", codes)
	}
	actions := SubmissionNextActions()
	if len(actions) == 0 || !strings.Contains(strings.Join(actions, ","), string(SubmissionNextActionRetrySameRequest)) {
		t.Fatalf("submission next actions = %#v", actions)
	}
	for _, test := range []struct {
		code  string
		state string
		want  SubmissionErrorCode
	}{
		{string(SubmissionErrorPolicyRejected), "", SubmissionErrorPolicyRejected},
		{"", "rejected", SubmissionErrorPolicyRejected},
		{string(SubmissionErrorProviderUnavailable), "failed", SubmissionErrorProviderUnavailable},
		{"unknown", "failed", SubmissionErrorInternalFailure},
	} {
		got := submissionStatusErrorForCode(test.code, test.state)
		if got.Code != string(test.want) {
			t.Errorf("status error %q/%q = %#v, want %q", test.code, test.state, got, test.want)
		}
	}
	if got := correctionStatusErrorForCode("", "rejected"); got.Code != string(SubmissionErrorPolicyRejected) {
		t.Fatalf("correction rejection = %#v", got)
	}
	if got := correctionStatusErrorForCode(string(SubmissionErrorNoChange), ""); got.Code != string(SubmissionErrorNoChange) {
		t.Fatalf("correction code = %#v", got)
	}
	if err := submissionStatusError(SubmissionErrorRequestCancelled); err.Code != string(SubmissionErrorRequestCancelled) {
		t.Fatalf("status error = %#v", err)
	}
}
