package migrationsupervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service/migrationcontrol"
	"github.com/markhuangai/dense-mem/internal/service/migrationexecutor"
)

var (
	ErrMissingDependency = errors.New("v2 migration supervisor: missing dependency")
	ErrGateBlocked       = errors.New("v2 migration supervisor: gate blocked")
)

const (
	supervisorInternalErrorMessage = "migration supervisor encountered an internal error; inspect server logs"
	gateInternalErrorMessage       = "migration gate check failed; inspect server logs"
)

type LeaderLock interface {
	TryLock(ctx context.Context) (interface {
		Release(context.Context) error
	}, error)
}

type Restarter interface {
	RequestRestart(reason string)
}

type ProviderProbe interface {
	Probe(ctx context.Context) (*GateEvidence, error)
}

type SearchReadiness interface {
	CheckSearchReadiness(ctx context.Context) (*repository.V2SearchReadiness, error)
}

type GateEvidence struct {
	Ref     string
	Message string
	Details map[string]any
}

type Config struct {
	Control         migrationcontrol.Service
	Executor        migrationexecutor.Service
	Lock            LeaderLock
	Restarter       Restarter
	ProviderProbe   ProviderProbe
	SearchReadiness SearchReadiness
	RequiredGates   []string
	PollInterval    time.Duration
	PageRetryDelays []time.Duration
	Now             func() time.Time
	Logger          *slog.Logger
}

type Service struct {
	control         migrationcontrol.Service
	executor        migrationexecutor.Service
	lock            LeaderLock
	restarter       Restarter
	providerProbe   ProviderProbe
	searchReadiness SearchReadiness
	requiredGates   []string
	pollInterval    time.Duration
	pageRetryDelays []time.Duration
	now             func() time.Time
	logger          *slog.Logger

	wake     chan struct{}
	stopOnce sync.Once
	started  bool
	mu       sync.Mutex
	cancel   context.CancelFunc

	lastGates []domain.V2MigrationGateResult
	lastError string
}

var _ migrationcontrol.Service = (*Service)(nil)

func New(cfg Config) *Service {
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	required := cfg.RequiredGates
	if len(required) == 0 {
		required = migrationcontrol.DefaultCutoverRequiredGates()
	}
	poll := cfg.PollInterval
	if poll <= 0 {
		poll = 2 * time.Second
	}
	retry := cfg.PageRetryDelays
	if len(retry) == 0 {
		retry = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second, 60 * time.Second}
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		control:         cfg.Control,
		executor:        cfg.Executor,
		lock:            cfg.Lock,
		restarter:       cfg.Restarter,
		providerProbe:   cfg.ProviderProbe,
		searchReadiness: cfg.SearchReadiness,
		requiredGates:   append([]string(nil), required...),
		pollInterval:    poll,
		pageRetryDelays: append([]time.Duration(nil), retry...),
		now:             now,
		logger:          logger,
		wake:            make(chan struct{}, 1),
	}
}

func (s *Service) StartBackground(ctx context.Context) error {
	if s.control == nil || s.executor == nil || s.lock == nil {
		return ErrMissingDependency
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.started = true
	go s.loop(runCtx)
	return nil
}

func (s *Service) Stop() {
	s.stopOnce.Do(func() {
		s.mu.Lock()
		cancel := s.cancel
		s.mu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
}

func (s *Service) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) Status(ctx context.Context) (*domain.V2MigrationControlStatus, error) {
	if s.control == nil {
		return nil, ErrMissingDependency
	}
	status, err := s.control.Status(ctx)
	if err != nil || status == nil {
		return status, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.lastGates) > 0 {
		status.GateResults = append([]domain.V2MigrationGateResult(nil), s.lastGates...)
	}
	status.RestartPending = s.lastError == "restart_pending"
	if s.lastError != "" && s.lastError != "restart_pending" {
		status.RecentErrors = []string{s.lastError}
	}
	return status, nil
}

func (s *Service) ApprovePreflight(ctx context.Context, req migrationcontrol.OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	if s.control == nil {
		return nil, ErrMissingDependency
	}
	status, err := s.control.ApprovePreflight(ctx, req)
	if err == nil {
		s.Wake()
	}
	return status, err
}

func (s *Service) Start(ctx context.Context, req migrationcontrol.OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	if s.control == nil {
		return nil, ErrMissingDependency
	}
	status, err := s.control.Start(ctx, req)
	if err == nil {
		s.Wake()
	}
	return status, err
}

func (s *Service) Pause(ctx context.Context, req migrationcontrol.OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	if s.control == nil {
		return nil, ErrMissingDependency
	}
	return s.control.Pause(ctx, req)
}

func (s *Service) Resume(ctx context.Context, req migrationcontrol.OperatorRequest) (*domain.V2MigrationControlStatus, error) {
	if s.control == nil {
		return nil, ErrMissingDependency
	}
	status, err := s.control.Resume(ctx, req)
	if err == nil {
		s.Wake()
	}
	return status, err
}

func (s *Service) Cutover(ctx context.Context, req migrationcontrol.CutoverRequest) (*domain.V2MigrationControlStatus, error) {
	if s.control == nil {
		return nil, ErrMissingDependency
	}
	return s.control.Cutover(ctx, req)
}

func (s *Service) loop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
			resetTimer(timer, 0)
		case <-timer.C:
			if err := s.tick(ctx); err != nil {
				publicErr := publicSupervisorError(err)
				s.setLastError(publicErr)
				s.logger.Warn("v2 migration supervisor tick failed", "error", publicErr)
			}
			resetTimer(timer, s.pollInterval)
		}
	}
}

func (s *Service) tick(ctx context.Context) error {
	lease, err := s.lock.TryLock(ctx)
	if err != nil {
		return err
	}
	if lease == nil {
		return nil
	}
	defer func() {
		if err := lease.Release(context.Background()); err != nil {
			s.logger.Warn("v2 migration supervisor lock release failed", "error", err)
		}
	}()

	for {
		status, err := s.control.Status(ctx)
		if err != nil {
			return err
		}
		if status == nil {
			return nil
		}
		if status.Run != nil && status.Run.MigrationContractVersion != migrationcontrol.DefaultMigrationContractVersion {
			s.setLastError("renew preflight before resuming legacy migration contract " + status.Run.MigrationContractVersion)
			return nil
		}
		switch status.State {
		case domain.V2MigrationStateRunning:
			keepRunning, err := s.runPages(ctx)
			if err != nil {
				return err
			}
			if !keepRunning {
				return nil
			}
		case domain.V2MigrationStateReadyCutover:
			return s.evaluateAndCutover(ctx, status)
		case domain.V2MigrationStateCutOver:
			s.requestRestart()
			return nil
		default:
			return nil
		}
	}
}

func (s *Service) runPages(ctx context.Context) (bool, error) {
	var lastErr error
	for attempt, delay := range append(s.pageRetryDelays, 0) {
		result, err := s.executor.RunOnce(ctx)
		if err == nil {
			s.setLastError("")
			if result != nil && result.Done {
				return false, nil
			}
			continue
		}
		lastErr = err
		if errors.Is(err, migrationexecutor.ErrMigrationNotRunning) {
			return false, nil
		}
		if attempt >= len(s.pageRetryDelays) {
			_, pauseErr := s.control.Pause(ctx, migrationcontrol.OperatorRequest{
				Reason: "migration paused after bounded retry attempts",
			})
			if pauseErr != nil {
				return false, fmt.Errorf("%w; pause failed: %v", lastErr, pauseErr)
			}
			return false, fmt.Errorf("%w; paused_retryable after bounded retries", lastErr)
		}
		if delay > 0 {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(delay):
			}
		}
	}
	return true, lastErr
}

func (s *Service) evaluateAndCutover(ctx context.Context, status *domain.V2MigrationControlStatus) error {
	gates, err := s.evaluateGates(ctx, status)
	s.setLastGates(gates)
	if err != nil {
		return err
	}
	run := status.Run
	if run == nil {
		return migrationcontrol.ErrPreflightRequired
	}
	_, err = s.control.Cutover(ctx, migrationcontrol.CutoverRequest{
		CorpusHash:  run.CorpusHash,
		GateResults: gates,
		Metadata: map[string]any{
			"source":       "migration_supervisor",
			"completed_at": s.now().UTC().Format(time.RFC3339Nano),
		},
		Reason: "automatic cutover after migration gates passed",
	})
	if err != nil {
		return err
	}
	s.requestRestart()
	return nil
}

func (s *Service) requestRestart() {
	s.setLastError("restart_pending")
	if s.restarter != nil {
		s.restarter.RequestRestart("v2 migration cutover marker committed")
	}
}

func (s *Service) evaluateGates(ctx context.Context, status *domain.V2MigrationControlStatus) ([]domain.V2MigrationGateResult, error) {
	run := status.Run
	if run == nil {
		return nil, migrationcontrol.ErrPreflightRequired
	}
	results := make([]domain.V2MigrationGateResult, 0, len(s.requiredGates))
	for _, name := range s.requiredGates {
		gate, err := s.evaluateGate(ctx, status, name)
		if err != nil {
			return append(results, gate), err
		}
		results = append(results, gate)
	}
	return results, nil
}

func (s *Service) evaluateGate(
	ctx context.Context,
	status *domain.V2MigrationControlStatus,
	name string,
) (domain.V2MigrationGateResult, error) {
	run := status.Run
	evidence := GateEvidence{
		Ref:     "dense-mem://migration/release-evidence/" + name,
		Message: "covered by embedded v2.1.1 release evidence and current migration state",
		Details: map[string]any{
			"version": migrationGateEvidenceVersion,
			"gate":    name,
		},
	}
	var err error
	switch name {
	case "backup_snapshots_created":
		if !attestationsCreated(run.PreflightChecks) {
			err = fmt.Errorf("%w: backup and snapshot creation attestations are missing", ErrGateBlocked)
		}
		evidence.Ref = "dense-mem://migration/preflight/backup-snapshots-created"
		evidence.Message = "operator attested PostgreSQL backup and Neo4j snapshot creation; restore was not verified"
		evidence.Details["attestation_scope"] = "creation_only"
	case "terminal_official_pipeline_outcomes":
		if run.TotalItems != run.CompletedItems || run.FailedItems != 0 || run.ExcludedItems != 0 {
			err = fmt.Errorf("%w: migration outcomes are not terminal and clean", ErrGateBlocked)
		}
		evidence.Ref = "dense-mem://migration/runtime/outcomes"
		evidence.Message = "all staged items reached terminal nonfailed outcomes with no blocking exclusions"
		evidence.Details["total_items"] = run.TotalItems
		evidence.Details["completed_items"] = run.CompletedItems
		evidence.Details["failed_items"] = run.FailedItems
		evidence.Details["excluded_items"] = run.ExcludedItems
	case "provider_readiness":
		evidence, err = s.providerReadiness(ctx, run)
	case "vector_contract_current", "search_index_backlog":
		evidence, err = s.searchGate(ctx, name)
	case "worker_lease_health":
		evidence.Ref = "dense-mem://migration/runtime/supervisor-leader-lock"
		evidence.Message = "migration supervisor held the PostgreSQL leader lock while processing and verifying this run"
	case "telemetry_audit":
		if !run.PreflightApproved || !hasRecentOperatorAction(status.Actions) {
			err = fmt.Errorf("%w: migration operator audit actions are missing", ErrGateBlocked)
		}
		evidence.Ref = "dense-mem://migration/runtime/operator-actions"
		evidence.Message = "operator actions are persisted in the migration control plane"
	default:
		var ok bool
		evidence, ok = embeddedReleaseEvidence(name)
		if !ok {
			evidence = GateEvidence{
				Ref:     "dense-mem://migration/release-evidence/missing/" + name,
				Message: "no embedded release evidence is registered for this migration gate",
				Details: map[string]any{
					"version": migrationGateEvidenceVersion,
					"gate":    name,
				},
			}
			err = fmt.Errorf("%w: no embedded release evidence registered for gate %q", ErrGateBlocked, name)
		}
	}
	return gateResult(name, evidence, err), err
}

func (s *Service) providerReadiness(ctx context.Context, run *domain.V2MigrationRun) (GateEvidence, error) {
	if run.CompletedItems > 0 {
		return GateEvidence{
			Ref:     "dense-mem://migration/runtime/provider-placement",
			Message: "provider work succeeded during migrated item placement",
			Details: map[string]any{
				"version":         migrationGateEvidenceVersion,
				"completed_items": run.CompletedItems,
			},
		}, nil
	}
	if run.TotalItems > 0 {
		return GateEvidence{}, fmt.Errorf("%w: no successful provider-backed placement outcomes observed", ErrGateBlocked)
	}
	if s.providerProbe == nil {
		return GateEvidence{}, fmt.Errorf("%w: empty corpus provider probe is unavailable", ErrGateBlocked)
	}
	evidence, err := s.providerProbe.Probe(ctx)
	if err != nil {
		return GateEvidence{}, err
	}
	if evidence == nil {
		return GateEvidence{}, fmt.Errorf("%w: empty corpus provider probe returned no evidence", ErrGateBlocked)
	}
	if evidence.Details == nil {
		evidence.Details = map[string]any{}
	}
	evidence.Details["version"] = migrationGateEvidenceVersion
	return *evidence, nil
}

func (s *Service) searchGate(ctx context.Context, name string) (GateEvidence, error) {
	if s.searchReadiness == nil {
		return GateEvidence{}, fmt.Errorf("%w: search readiness check is unavailable", ErrGateBlocked)
	}
	readiness, err := s.searchReadiness.CheckSearchReadiness(ctx)
	if err != nil {
		return GateEvidence{}, err
	}
	if readiness == nil || !readiness.Ready {
		return GateEvidence{}, fmt.Errorf("%w: search readiness failed", ErrGateBlocked)
	}
	details := map[string]any{
		"version": migrationGateEvidenceVersion,
		"gate":    name,
	}
	if readiness.Contract != nil {
		details["embedding_contract_id"] = readiness.Contract.EmbeddingContractID
		details["search_index_generation_id"] = readiness.Contract.SearchIndexGenerationID
		details["index_strategy"] = readiness.Contract.IndexStrategy
	}
	return GateEvidence{
		Ref:     "dense-mem://migration/runtime/search-readiness/" + name,
		Message: "active V2 search contract and embedding backlog are ready",
		Details: details,
	}, nil
}

func gateResult(name string, evidence GateEvidence, gateErr error) domain.V2MigrationGateResult {
	outcome := domain.V2MigrationGateOutcomePass
	message := evidence.Message
	if gateErr != nil {
		outcome = domain.V2MigrationGateOutcomeFail
		message = safeGateMessage(gateErr)
	}
	metadata := evidence.Details
	if metadata == nil {
		metadata = map[string]any{}
	}
	if _, ok := metadata["version"]; !ok {
		metadata["version"] = migrationGateEvidenceVersion
	}
	return domain.V2MigrationGateResult{
		GateName:     name,
		Outcome:      outcome,
		EvidenceRef:  nonempty(evidence.Ref, "dense-mem://migration/runtime/"+name),
		EvidenceHash: evidenceHash(name, evidence, outcome),
		Message:      message,
		Metadata:     metadata,
	}
}

func evidenceHash(name string, evidence GateEvidence, outcome string) string {
	payload := map[string]any{
		"name":     name,
		"outcome":  outcome,
		"ref":      evidence.Ref,
		"message":  evidence.Message,
		"metadata": evidence.Details,
	}
	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func attestationsCreated(checks map[string]any) bool {
	return truthy(checks["backup_snapshots_created"]) &&
		truthy(checks["postgres_backup_created"]) &&
		truthy(checks["neo4j_snapshot_created"]) &&
		strings.TrimSpace(fmt.Sprint(checks["postgres_backup_reference_hash"])) != "" &&
		strings.TrimSpace(fmt.Sprint(checks["neo4j_snapshot_reference_hash"])) != ""
}

func truthy(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func hasRecentOperatorAction(actions []domain.V2MigrationOperatorAction) bool {
	for _, action := range actions {
		switch action.Action {
		case domain.V2MigrationActionPreflightApproved,
			domain.V2MigrationActionStarted,
			domain.V2MigrationActionResumed,
			domain.V2MigrationActionPaused:
			return true
		}
	}
	return false
}

func safeGateMessage(err error) string {
	if err == nil {
		return ""
	}
	if !errors.Is(err, ErrGateBlocked) {
		return gateInternalErrorMessage
	}
	msg := err.Error()
	if idx := strings.Index(msg, ": "); idx >= 0 {
		msg = msg[idx+2:]
	}
	return msg
}

func publicSupervisorError(err error) string {
	if err == nil {
		return ""
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "migration supervisor stopped"
	case errors.Is(err, context.DeadlineExceeded):
		return "migration supervisor timed out"
	case errors.Is(err, ErrMissingDependency):
		return "migration supervisor dependency is unavailable"
	case errors.Is(err, ErrGateBlocked):
		return safeGateMessage(err)
	case errors.Is(err, migrationcontrol.ErrPreflightRequired):
		return "migration preflight is required"
	default:
		return supervisorInternalErrorMessage
	}
}

func nonempty(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (s *Service) setLastGates(gates []domain.V2MigrationGateResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastGates = append([]domain.V2MigrationGateResult(nil), gates...)
}

func (s *Service) setLastError(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = strings.TrimSpace(message)
}

func resetTimer(timer *time.Timer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}
