package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/operatorcli"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/conflictreview"
	"github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

type cliConfig struct {
	teamID       string
	workerID     string
	now          string
	timezone     string
	batchSize    int
	leaseSeconds int
	maxAttempts  int
	timeoutSecs  int
}

type reviewOutput struct {
	TeamID        string             `json:"team_id"`
	ReviewRunID   string             `json:"review_run_id,omitempty"`
	Status        string             `json:"status"`
	Claimed       bool               `json:"claimed"`
	ClaimedCases  int                `json:"claimed_cases"`
	ResolvedCases int                `json:"resolved_cases"`
	OverdueCases  int                `json:"overdue_cases"`
	NoOpCases     int                `json:"no_op_cases"`
	FailedCases   int                `json:"failed_cases"`
	Results       []reviewCaseResult `json:"results,omitempty"`
}

type reviewCaseResult struct {
	ConflictID           string   `json:"conflict_id"`
	Outcome              string   `json:"outcome"`
	Stage                string   `json:"stage"`
	PreferredPositionID  string   `json:"preferred_position_id,omitempty"`
	UpdatedRelationships []string `json:"updated_relationships,omitempty"`
	RetractedEvidenceIDs []string `json:"retracted_evidence_ids,omitempty"`
	AssessmentAttemptID  string   `json:"assessment_attempt_id,omitempty"`
	ResolutionMethod     string   `json:"resolution_method,omitempty"`
	ResolutionPending    bool     `json:"resolution_pending,omitempty"`
}

type postgresConfig struct {
	dsn string
}

const (
	reviewConflictMinBatchSize    = 1
	reviewConflictMaxBatchSize    = 500
	reviewConflictMinLeaseSeconds = 30
	reviewConflictMaxLeaseSeconds = 1800
	reviewConflictMinAttempts     = 1
	reviewConflictMaxAttempts     = 20
	reviewConflictMinTimeoutSecs  = 1
	reviewConflictMaxTimeoutSecs  = 86400
	reviewConflictDefaultTimeout  = 360
)

func (c postgresConfig) GetPostgresDSN() string {
	return c.dsn
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	cfg, err := parseCLI(args, stderr)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if cfg.now != "" {
		parsed, err := time.Parse(time.RFC3339, cfg.now)
		if err != nil {
			return fmt.Errorf("invalid --now: must be RFC3339: %w", err)
		}
		now = parsed.UTC()
	}
	dsn, err := operatorcli.ResolvePostgresDSN(os.Getenv)
	if err != nil {
		return err
	}
	runtimeConfig, err := config.LoadWithPostgresDSN(dsn)
	if err != nil {
		return fmt.Errorf("load verifier configuration: %w", err)
	}
	if !conflictVerifierConfigured(&runtimeConfig) {
		return errors.New("AI_VERIFIER_API_URL, AI_VERIFIER_API_KEY, and AI_VERIFIER_MODEL are required for overdue conflict assessment")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.timeoutSecs)*time.Second)
	defer cancel()
	pgClient, err := postgres.OpenWithClient(ctx, postgresConfig{dsn: dsn})
	if err != nil {
		return fmt.Errorf("connect postgres: %w", err)
	}
	defer pgClient.Close()
	ledger := repository.NewLedgerRepository(pgClient.GetDB(), postgres.NewRLS())
	assessmentLimits := verifier.SemanticAssessmentLimitsForConfig(&runtimeConfig)
	provider := verifier.NewOpenAIVerifierWithAssessmentLimits(&runtimeConfig, nil, assessmentLimits)
	runner, err := conflictreview.NewRunner(ledger, provider, cfg.timezone, assessmentLimits)
	if err != nil {
		return fmt.Errorf("build conflict review runner: %w", err)
	}
	out, reviewErr := reviewTeamConflicts(ctx, runner, cfg, now)
	if reviewErr != nil && out.TeamID == "" {
		return reviewErr
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	return reviewErr
}

func reviewTeamConflicts(
	ctx context.Context,
	ledger conflictReviewLedger,
	cfg cliConfig,
	now time.Time,
) (reviewOutput, error) {
	lease := time.Duration(cfg.leaseSeconds) * time.Second
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, repository.ConflictReviewRunInput{
		TeamID:       cfg.teamID,
		WorkerID:     cfg.workerID,
		LocalRunDate: now,
		Timezone:     cfg.timezone,
		Lease:        lease,
	})
	if err != nil {
		return reviewOutput{}, err
	}
	out := reviewOutput{TeamID: cfg.teamID, Claimed: claimed, Status: "skipped"}
	if run != nil {
		out.ReviewRunID = run.ReviewRunID
	}
	if run == nil || !claimed || run.Status == "completed" {
		return out, nil
	}
	counts := repository.ConflictReviewRunCompleteInput{
		TeamID:      cfg.teamID,
		ReviewRunID: run.ReviewRunID,
		WorkerID:    cfg.workerID,
		Status:      "completed",
	}
	attempted := map[string]struct{}{}
	for {
		excluded := make([]string, 0, len(attempted))
		for id := range attempted {
			excluded = append(excluded, id)
		}
		cases, err := ledger.ClaimRelationshipConflictCases(ctx, repository.ClaimRelationshipConflictCasesInput{
			TeamID:              cfg.teamID,
			WorkerID:            cfg.workerID,
			ReviewRunID:         run.ReviewRunID,
			Limit:               cfg.batchSize,
			Lease:               lease,
			MaxAttempts:         cfg.maxAttempts,
			Now:                 now,
			ExcludedConflictIDs: excluded,
		})
		if err != nil {
			counts.Status = "failed"
			counts.LastError = "conflict review claim failed"
			break
		}
		if len(cases) == 0 {
			break
		}
		for _, conflictCase := range cases {
			if _, ok := attempted[conflictCase.ConflictID]; ok {
				continue
			}
			attempted[conflictCase.ConflictID] = struct{}{}
			counts.ClaimedCases++
			result, err := ledger.ReviewRelationshipConflictCase(ctx, repository.ReviewRelationshipConflictCaseInput{
				TeamID:      cfg.teamID,
				WorkerID:    cfg.workerID,
				ReviewRunID: run.ReviewRunID,
				ConflictID:  conflictCase.ConflictID,
				Now:         now,
			})
			if err != nil {
				counts.FailedCases++
				out.Results = append(out.Results, reviewCaseResult{ConflictID: conflictCase.ConflictID, Outcome: "error"})
				continue
			}
			out.Results = append(out.Results, reviewCaseResult{
				ConflictID:           result.ConflictID,
				Outcome:              result.Outcome,
				Stage:                result.Stage,
				PreferredPositionID:  result.PreferredPositionID,
				UpdatedRelationships: append([]string(nil), result.UpdatedRelationships...),
				RetractedEvidenceIDs: append([]string(nil), result.RetractedEvidenceIDs...),
				AssessmentAttemptID:  result.AssessmentAttemptID,
				ResolutionMethod:     result.ResolutionMethod,
				ResolutionPending:    result.ResolutionPending,
			})
			switch result.Outcome {
			case repository.ConflictReviewOutcomeResolve:
				counts.ResolvedCases++
			case repository.ConflictReviewOutcomeOverdue:
				counts.OverdueCases++
			default:
				counts.NoOpCases++
			}
		}
	}
	if counts.FailedCases > 0 && counts.Status == "completed" {
		counts.Status = "failed"
		counts.LastError = "one or more conflict cases failed"
	}
	if err := ledger.CompleteRelationshipConflictReviewRun(ctx, counts); err != nil {
		return reviewOutput{}, err
	}
	out.Status = counts.Status
	out.ClaimedCases = counts.ClaimedCases
	out.ResolvedCases = counts.ResolvedCases
	out.OverdueCases = counts.OverdueCases
	out.NoOpCases = counts.NoOpCases
	out.FailedCases = counts.FailedCases
	if counts.Status == "failed" {
		return out, errReviewConflictsFailed
	}
	return out, nil
}

var errReviewConflictsFailed = errors.New("conflict review failed")

type conflictReviewLedger interface {
	ReserveRelationshipConflictReviewRun(context.Context, repository.ConflictReviewRunInput) (*repository.ConflictReviewRunRecord, bool, error)
	ClaimRelationshipConflictCases(context.Context, repository.ClaimRelationshipConflictCasesInput) ([]repository.RelationshipConflictCaseRecord, error)
	ReviewRelationshipConflictCase(context.Context, repository.ReviewRelationshipConflictCaseInput) (*repository.ReviewRelationshipConflictCaseResult, error)
	CompleteRelationshipConflictReviewRun(context.Context, repository.ConflictReviewRunCompleteInput) error
}

func conflictVerifierConfigured(cfg config.ConfigProvider) bool {
	return strings.TrimSpace(cfg.GetAIVerifierAPIURL()) != "" &&
		strings.TrimSpace(cfg.GetAIVerifierAPIKey()) != "" &&
		strings.TrimSpace(cfg.GetAIVerifierModel()) != ""
}

func parseCLI(args []string, stderr io.Writer) (cliConfig, error) {
	var cfg cliConfig
	fs := flag.NewFlagSet("review-conflicts", flag.ContinueOnError)
	fs.SetOutput(stderr)
	defaultTimezone := strings.TrimSpace(os.Getenv("APP_TIMEZONE"))
	if defaultTimezone == "" {
		defaultTimezone = "Local"
	}
	fs.StringVar(&cfg.teamID, "team-id", "", "Team UUID to review (required)")
	fs.StringVar(&cfg.workerID, "worker-id", fmt.Sprintf("operator-conflict-review-%d", os.Getpid()), "Reviewer worker ID")
	fs.StringVar(&cfg.now, "now", "", "Review timestamp override in RFC3339")
	fs.StringVar(&cfg.timezone, "timezone", defaultTimezone, "Team-local review timezone")
	fs.IntVar(&cfg.batchSize, "batch-size", 100, "Maximum cases claimed per batch")
	fs.IntVar(&cfg.leaseSeconds, "lease-seconds", 300, "Review lease seconds")
	fs.IntVar(&cfg.maxAttempts, "max-attempts", 5, "Maximum case attempts")
	fs.IntVar(&cfg.timeoutSecs, "timeout-seconds", reviewConflictDefaultTimeout, "Maximum review command duration in seconds")
	if err := fs.Parse(args); err != nil {
		return cliConfig{}, err
	}
	if cfg.teamID == "" {
		return cliConfig{}, errors.New("--team-id is required")
	}
	if cfg.workerID == "" {
		return cliConfig{}, errors.New("--worker-id is required")
	}
	if _, err := time.LoadLocation(cfg.timezone); err != nil {
		return cliConfig{}, fmt.Errorf("--timezone is invalid: %w", err)
	}
	if err := validateCLIIntRange("--batch-size", cfg.batchSize, reviewConflictMinBatchSize, reviewConflictMaxBatchSize); err != nil {
		return cliConfig{}, err
	}
	if err := validateCLIIntRange("--lease-seconds", cfg.leaseSeconds, reviewConflictMinLeaseSeconds, reviewConflictMaxLeaseSeconds); err != nil {
		return cliConfig{}, err
	}
	if err := validateCLIIntRange("--max-attempts", cfg.maxAttempts, reviewConflictMinAttempts, reviewConflictMaxAttempts); err != nil {
		return cliConfig{}, err
	}
	if err := validateCLIIntRange("--timeout-seconds", cfg.timeoutSecs, reviewConflictMinTimeoutSecs, reviewConflictMaxTimeoutSecs); err != nil {
		return cliConfig{}, err
	}
	if cfg.timeoutSecs <= cfg.leaseSeconds {
		return cliConfig{}, errors.New("--timeout-seconds must be greater than --lease-seconds")
	}
	return cfg, nil
}

func validateCLIIntRange(name string, value, min, max int) error {
	if value < min || value > max {
		return fmt.Errorf("%s must be between %d and %d, got %d", name, min, max, value)
	}
	return nil
}
