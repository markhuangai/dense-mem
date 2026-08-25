package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/markhuangai/dense-mem/internal/config"
	"github.com/markhuangai/dense-mem/internal/conflictassessment"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/conflictreview"
	postgresstorage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/verifier"
)

const (
	reviewLease = 2 * time.Minute
	claimWait   = 15 * time.Second
)

type output struct {
	TeamID               string   `json:"team_id"`
	ConflictID           string   `json:"conflict_id"`
	ReviewRunID          string   `json:"review_run_id"`
	Outcome              string   `json:"outcome"`
	Stage                string   `json:"stage"`
	PreferredPositionID  string   `json:"preferred_position_id"`
	ResolutionMethod     string   `json:"resolution_method"`
	AssessmentAttemptID  string   `json:"assessment_attempt_id"`
	UpdatedRelationships []string `json:"updated_relationship_ids"`
	RetractedEvidenceIDs []string `json:"retracted_evidence_ids"`
}

type legacyConflictProvider struct {
	provider *verifier.OpenAIVerifier
}

func (p legacyConflictProvider) ModelName() string {
	if p.provider == nil {
		return ""
	}
	return p.provider.ModelName()
}

func (p legacyConflictProvider) AssessRelationshipConflict(
	ctx context.Context,
	request conflictassessment.ConflictAssessmentRequest,
) (conflictassessment.ConflictAssessmentResponse, error) {
	legacy := verifier.ConflictAssessmentRequest(request)
	response, err := p.provider.AssessRelationshipConflict(ctx, legacy)
	if err != nil {
		return conflictassessment.ConflictAssessmentResponse{}, err
	}
	return conflictassessment.ConflictAssessmentResponse(response), nil
}

func main() {
	teamID := flag.String("team-id", "", "team containing the conflict")
	conflictID := flag.String("conflict-id", "", "conflict to review")
	nowRaw := flag.String("now", "", "explicit RFC3339 review time")
	flag.Parse()

	if strings.TrimSpace(*teamID) == "" || strings.TrimSpace(*conflictID) == "" {
		fatal("--team-id and --conflict-id are required")
	}
	now, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(*nowRaw))
	if err != nil {
		fatal("parse --now: %v", err)
	}

	cfg := driverConfig()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := postgresstorage.Open(ctx, &cfg)
	if err != nil {
		fatal("open PostgreSQL connection: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		fatal("resolve PostgreSQL connection: %v", err)
	}
	defer sqlDB.Close()

	rls := postgresstorage.NewRLS()
	ledger := repository.NewLedgerRepository(db, rls)
	limits := conflictassessment.DefaultSemanticAssessmentLimits()
	provider := verifier.NewOpenAIVerifierWithAssessmentLimits(&cfg, nil, verifier.SemanticAssessmentLimits(limits))
	reviewer, err := conflictreview.New(conflictreview.Dependencies{
		Repository: ledger,
		Provider:   legacyConflictProvider{provider: provider},
		Timezone:   "UTC",
		Limits:     limits,
	})
	if err != nil {
		fatal("build conflict reviewer: %v", err)
	}

	workerID := fmt.Sprintf("conflict-e2e-%s-%d", compactID(*conflictID), now.Unix())
	run, claimed, err := ledger.ReserveRelationshipConflictReviewRun(ctx, repository.ConflictReviewRunInput{
		TeamID:       *teamID,
		WorkerID:     workerID,
		LocalRunDate: now,
		Timezone:     "UTC",
		Lease:        reviewLease,
	})
	if err != nil {
		fatal("reserve conflict review run: %v", err)
	}
	if run == nil || !claimed {
		fatal("conflict review run was not claimable for %s", now.Format("2006-01-02"))
	}

	record, err := claimTarget(ctx, ledger, *teamID, *conflictID, run.ReviewRunID, workerID, now)
	if err != nil {
		completeFailedRun(ctx, ledger, *teamID, run.ReviewRunID, workerID)
		fatal("claim conflict: %v", err)
	}
	result, err := reviewer.ReviewRelationshipConflictCase(ctx, repository.ReviewRelationshipConflictCaseInput{
		TeamID:      *teamID,
		WorkerID:    workerID,
		ReviewRunID: run.ReviewRunID,
		ConflictID:  record.ConflictID,
		Now:         now,
	})
	if err != nil {
		completeFailedRun(ctx, ledger, *teamID, run.ReviewRunID, workerID)
		fatal("review conflict: %v", err)
	}

	completion := repository.ConflictReviewRunCompleteInput{
		TeamID:       *teamID,
		ReviewRunID:  run.ReviewRunID,
		WorkerID:     workerID,
		Status:       "completed",
		ClaimedCases: 1,
	}
	switch result.Outcome {
	case repository.ConflictReviewOutcomeResolve:
		completion.ResolvedCases = 1
	case repository.ConflictReviewOutcomeOverdue:
		completion.OverdueCases = 1
	default:
		completion.NoOpCases = 1
	}
	if err := ledger.CompleteRelationshipConflictReviewRun(ctx, completion); err != nil {
		fatal("complete conflict review run: %v", err)
	}

	encoded, err := json.Marshal(output{
		TeamID:               *teamID,
		ConflictID:           result.ConflictID,
		ReviewRunID:          run.ReviewRunID,
		Outcome:              result.Outcome,
		Stage:                result.Stage,
		PreferredPositionID:  result.PreferredPositionID,
		ResolutionMethod:     result.ResolutionMethod,
		AssessmentAttemptID:  result.AssessmentAttemptID,
		UpdatedRelationships: append([]string(nil), result.UpdatedRelationships...),
		RetractedEvidenceIDs: append([]string(nil), result.RetractedEvidenceIDs...),
	})
	if err != nil {
		fatal("encode conflict review result: %v", err)
	}
	fmt.Println(string(encoded))
}

func claimTarget(
	ctx context.Context,
	ledger *repository.LedgerRepositoryImpl,
	teamID string,
	conflictID string,
	reviewRunID string,
	workerID string,
	now time.Time,
) (*repository.RelationshipConflictCaseRecord, error) {
	deadline := time.Now().Add(claimWait)
	for {
		records, err := ledger.ClaimRelationshipConflictCases(ctx, repository.ClaimRelationshipConflictCasesInput{
			TeamID:      teamID,
			WorkerID:    workerID,
			ReviewRunID: reviewRunID,
			Limit:       10,
			Lease:       reviewLease,
			MaxAttempts: 20,
			Now:         now,
		})
		if err != nil {
			return nil, err
		}
		for i := range records {
			if records[i].ConflictID == conflictID {
				return &records[i], nil
			}
		}
		if len(records) > 0 {
			return nil, fmt.Errorf("claimed %d unexpected conflict cases", len(records))
		}
		if time.Now().After(deadline) {
			return nil, errors.New("target conflict was not claimable before the deadline")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func completeFailedRun(
	ctx context.Context,
	ledger *repository.LedgerRepositoryImpl,
	teamID string,
	reviewRunID string,
	workerID string,
) {
	_ = ledger.CompleteRelationshipConflictReviewRun(ctx, repository.ConflictReviewRunCompleteInput{
		TeamID:      teamID,
		ReviewRunID: reviewRunID,
		WorkerID:    workerID,
		Status:      "failed",
		FailedCases: 1,
		LastError:   "conflict e2e driver failed",
	})
}

func driverConfig() config.Config {
	if strings.TrimSpace(os.Getenv("DENSE_MEM_E2E_CONFLICT_REVIEW_LIVE")) == "1" {
		limits := conflictassessment.DefaultSemanticAssessmentLimits()
		return config.Config{
			PostgresDSN:                         postgresDSN(),
			AIVerifierAPIURL:                    requiredEnv("AI_VERIFIER_API_URL"),
			AIVerifierAPIKey:                    requiredEnv("AI_VERIFIER_API_KEY"),
			AIVerifierModel:                     requiredEnv("AI_VERIFIER_MODEL"),
			AIVerifierDisableTemperature:        true,
			AIVerifierTimeoutSeconds:            10,
			AIVerifierMaxConcurrency:            1,
			AIVerifierMaxInputTokens:            limits.MaxInputTokens,
			AIVerifierMaxOutputTokens:           limits.MaxOutputTokens,
			AIVerifierMaxCandidateContextTokens: limits.MaxCandidateContextTokens,
			AIVerifierMaxPredicateOptions:       limits.MaxPredicateOptions,
			AIVerifierTokenizer:                 limits.Tokenizer,
		}
	}
	limits := conflictassessment.DefaultSemanticAssessmentLimits()
	return config.Config{
		PostgresDSN:                         postgresDSN(),
		AIVerifierAPIURL:                    requiredEnv("DENSE_MEM_E2E_CONFLICT_PROVIDER_URL"),
		AIVerifierAPIKey:                    "dense-mem-conflict-e2e-key",
		AIVerifierModel:                     "dense-mem-conflict-e2e-verifier",
		AIVerifierDisableTemperature:        true,
		AIVerifierTimeoutSeconds:            10,
		AIVerifierMaxConcurrency:            1,
		AIVerifierMaxInputTokens:            limits.MaxInputTokens,
		AIVerifierMaxOutputTokens:           limits.MaxOutputTokens,
		AIVerifierMaxCandidateContextTokens: limits.MaxCandidateContextTokens,
		AIVerifierMaxPredicateOptions:       limits.MaxPredicateOptions,
		AIVerifierTokenizer:                 limits.Tokenizer,
	}
}

func postgresDSN() string {
	host := requiredEnv("DENSE_MEM_E2E_POSTGRES_HOST")
	port := requiredEnv("DENSE_MEM_E2E_POSTGRES_PORT")
	user := requiredEnv("DENSE_MEM_E2E_POSTGRES_USER")
	password := requiredEnv("DENSE_MEM_E2E_POSTGRES_PASSWORD")
	database := requiredEnv("DENSE_MEM_E2E_POSTGRES_DB")
	value := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(user, password),
		Host:     net.JoinHostPort(host, port),
		Path:     "/" + database,
		RawQuery: "sslmode=disable",
	}
	return value.String()
}

func compactID(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "-", "")
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func requiredEnv(name string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		fatal("%s is required", name)
	}
	return value
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
