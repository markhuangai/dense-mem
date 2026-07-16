package eval_test

import (
	"os"
	"strings"
	"testing"
)

func TestMonitorIdentityRecordsReviewerAndVerifierModels(t *testing.T) {
	contents, err := os.ReadFile("scripts/run_full_public_rag_eval_until_done.sh")
	if err != nil {
		t.Fatalf("ReadFile monitor script: %v", err)
	}
	script := string(contents)
	for _, want := range []string{
		`--arg reviewer_model "${AI_REVIEWER_MODEL:-}"`,
		`reviewer_model: $reviewer_model`,
		`--arg verifier_model "${AI_VERIFIER_MODEL:-}"`,
		`verifier_model: $verifier_model`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("monitor identity missing %q", want)
		}
	}
}
