package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/markhuangai/dense-mem/internal/assessor"
	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
	assessorprovider "github.com/markhuangai/dense-mem/internal/provider/assessor"
)

func runAnswerCampaign(ctx context.Context, opts evalharness.RunOptions, command evalharness.PairCommand, policy string) error {
	configuration, err := evalharness.LoadPairConfig(command.ConfigPath)
	if err != nil {
		return err
	}
	if configuration.Campaign != "issue_681_evidence_only_v5" {
		return fmt.Errorf("answer mode requires the frozen v5 campaign")
	}
	if err := evalharness.VerifyPairBinary(configuration.Scorer); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	hash, err := evalharness.PairFileHash(executable)
	if err != nil {
		return err
	}
	if hash != configuration.Scorer.SHA256 {
		return fmt.Errorf("answer executable differs from pinned scorer")
	}
	deadline, cancel := context.WithDeadline(ctx, configuration.Deadline.Add(-30*time.Minute))
	defer cancel()
	opts.APIKey = os.Getenv(configuration.Candidate.Runtime.APIKeyEnv)
	opts.RunID = configuration.RunID
	logger, protector, transport, limits, err := answerModelRuntime(opts.APIKey, opts.RunID, "answers")
	if err != nil {
		return err
	}
	opts.Logger, opts.Protector = logger, protector
	if opts.Mode == "evidence-only-answer-score" {
		return evalharness.RunPairAnswerScoring(deadline, configuration, command.Side, transport, limits, protector)
	}
	if command.Side != "candidate" {
		return fmt.Errorf("retained baseline answers cannot be regenerated")
	}
	if err := evalharness.VerifyBaselineAnswerScoring(configuration); err != nil {
		return err
	}
	opts.SeedManifestPath, opts.SuitePath = configuration.SeedManifest, configuration.Suite
	opts.BaseURL = configuration.Candidate.Runtime.BaseURL
	opts.OutDir = filepath.Join(configuration.OutDir, "answers", "candidate", policy)
	return evalharness.RunEvidenceOnlyHost(deadline, opts, transport, "dense-mem-testing", policy, limits)
}

func runEvidenceOnlyHost(ctx context.Context, opts evalharness.RunOptions, policy string) error {
	logger, protector, transport, limits, err := answerModelRuntime(opts.APIKey, opts.RunID, "host")
	if err != nil {
		return err
	}
	opts.Logger, opts.Protector = logger, protector
	return evalharness.RunEvidenceOnlyHost(ctx, opts, transport, "dense-mem-testing", policy, limits)
}

func answerModelRuntime(apiKey, runID, phase string) (*observability.Logger, *observability.CredentialProtector, modelprovider.StructuredTransport, assessor.SemanticAssessmentLimits, error) {
	fail := func() (*observability.Logger, *observability.CredentialProtector, modelprovider.StructuredTransport, assessor.SemanticAssessmentLimits, error) {
		return nil, nil, nil, assessor.SemanticAssessmentLimits{}, fmt.Errorf("intake model requires valid dense-mem-testing configuration")
	}
	cfg, err := config.Load()
	if err != nil {
		return fail()
	}
	if cfg.AIVerifierModel != "dense-mem-testing" {
		return fail()
	}
	secrets, err := cfg.OperationalSecrets()
	if err != nil {
		return fail()
	}
	secrets = append(secrets, apiKey)
	protector := observability.NewCredentialProtector(secrets...)
	logger := observability.NewWithProtector(cfg.LogLevel, protector)
	client := observability.DiagnosticHTTPClient(&http.Client{Timeout: time.Duration(cfg.GetAIVerifierTimeoutSeconds()) * time.Second}, logger.ContextSlog().With("run_id", runID, "phase", phase), protector)
	transport := assessorprovider.NewOpenAIAssessor(&cfg, client)
	return logger, protector, transport, assessorprovider.SemanticAssessmentLimitsForConfig(&cfg), nil
}
