package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/observability"
)

const MeasurementSchema = "dense-mem.eval.measurements.v1"
const measurementJournal = "execution_journal.jsonl"
const measurementManifest = "execution_measurements.json"

type ExecutionMeasurements struct {
	SchemaVersion string            `json:"schema_version"`
	CaptureID     string            `json:"capture_id"`
	RunID         string            `json:"run_id"`
	SeedHash      string            `json:"seed_hash"`
	SuiteHash     string            `json:"suite_sha256"`
	Imports       []string          `json:"scheduled_imports"`
	Cases         []string          `json:"scheduled_cases"`
	Status        string            `json:"status"`
	Artifacts     map[string]string `json:"artifact_sha256"`
	Outcomes      map[string]int    `json:"outcomes"`
}

type executionEvent struct {
	Counts            map[string]int `json:"counts,omitempty"`
	LastCompleted     string         `json:"last_completed_work,omitempty"`
	Sequence          int            `json:"sequence"`
	CaptureID         string         `json:"capture_id"`
	RunID             string         `json:"run_id"`
	Event             string         `json:"event"`
	At                time.Time      `json:"at"`
	OperationID       string         `json:"operation_id,omitempty"`
	Kind              string         `json:"kind,omitempty"`
	ItemID            string         `json:"item_id,omitempty"`
	Phase             string         `json:"phase,omitempty"`
	AttemptID         string         `json:"attempt_id,omitempty"`
	Method            string         `json:"method,omitempty"`
	Outcome           string         `json:"outcome,omitempty"`
	ElapsedMS         *float64       `json:"elapsed_ms,omitempty"`
	Dispatched        bool           `json:"dispatched,omitempty"`
	StatusCode        int            `json:"status_code,omitempty"`
	CorrelationID     string         `json:"correlation_id,omitempty"`
	ErrorCode         string         `json:"error_code,omitempty"`
	Cause             string         `json:"cause,omitempty"`
	RetryDelayMS      int64          `json:"retry_delay_ms,omitempty"`
	IdempotencyKey    string         `json:"idempotency_key,omitempty"`
	OriginalAttemptID string         `json:"original_attempt_id,omitempty"`
	OriginalRetryable *bool          `json:"original_retryable,omitempty"`
	OriginalOutcome   string         `json:"original_outcome,omitempty"`
	TraceHash         string         `json:"trace_sha256,omitempty"`
}

type executionJournal struct {
	mu         sync.Mutex
	file       *os.File
	traces     *os.File
	directory  string
	manifest   ExecutionMeasurements
	sequence   int
	nextID     int
	queryCount int
	failure    error
	logger     *observability.Logger
	protector  *observability.CredentialProtector
	secrets    []string
}

type measuredOperation struct {
	journal *executionJournal
	event   executionEvent
	started time.Time
}

type operationContextKey struct{}
type attemptContextKey struct{}
type measuredAttempt struct {
	operation *measuredOperation
	event     executionEvent
	started   time.Time
}

type protectedExecutionError struct {
	cause error
	text  string
}

func (e *protectedExecutionError) Error() string { return e.text }
func (e *protectedExecutionError) Unwrap() error { return e.cause }

func protectExecutionError(ctx context.Context, opts RunOptions, cause error) error {
	if cause == nil {
		return nil
	}
	_, secrets, _ := measurementEndpoint(opts.BaseURL)
	secrets = append(secrets, observability.AuthenticationSecretsFromContext(ctx)...)
	secrets = append(secrets, opts.APIKey)
	protector := opts.Protector
	if protector == nil {
		protector = observability.NewCredentialProtector()
	}
	body, reason := observability.ProtectDiagnosticText(observability.WithAuthenticationSecrets(ctx, secrets...), protector, []byte(cause.Error()), modelprovider.MaxProviderDiagnosticBodyBytes)
	if reason != observability.CredentialProtectionAvailable {
		return &protectedExecutionError{cause: cause, text: fmt.Sprintf("evaluation failure details unavailable: credential protection reason %d", reason)}
	}
	if string(body) == cause.Error() {
		return cause
	}
	return &protectedExecutionError{cause: cause, text: string(body)}
}

func measurementEndpoint(raw string) (string, []string, error) {
	endpoint, err := url.Parse(raw)
	if err != nil {
		return "", nil, errors.New("invalid evaluation base URL")
	}
	if endpoint.User == nil {
		return raw, nil, nil
	}
	var secrets []string
	if userinfo := endpoint.User.String(); userinfo != "" {
		secrets = append(secrets, userinfo+"@")
	}
	if password, present := endpoint.User.Password(); present {
		if password != "" {
			secrets = append(secrets, password)
		}
		// net/http uses a different password marker from net/url in transport errors.
		secrets = append(secrets, endpoint.User.Username()+":***@")
	}
	endpoint.User = nil
	return endpoint.String(), secrets, nil
}

func newExecutionJournal(opts RunOptions, config RunConfig, corpus []CorpusItem, suite []SuiteCase) (*executionJournal, error) {
	if opts.OutDir == "" {
		return nil, nil
	}
	_, secrets, err := measurementEndpoint(opts.BaseURL)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(opts.OutDir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(opts.OutDir, measurementJournal), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create execution journal (use a new output directory): %w", err)
	}
	j := &executionJournal{file: file, directory: opts.OutDir, logger: opts.Logger, protector: opts.Protector, secrets: append(secrets, opts.APIKey)}
	if j.protector == nil {
		j.protector = observability.NewCredentialProtector(opts.APIKey)
	}
	if j.logger == nil {
		j.logger = observability.NewWithProtector(slog.LevelInfo, j.protector)
	}
	j.manifest = ExecutionMeasurements{SchemaVersion: MeasurementSchema, CaptureID: uuid.NewString(), RunID: config.RunID, SeedHash: config.SeedHash, Status: "in_progress", Imports: []string{}, Cases: []string{}, Outcomes: map[string]int{}}
	suiteBody, err := os.ReadFile(opts.SuitePath)
	j.manifest.SuiteHash = pairBytesHash(suiteBody)
	if err == nil && opts.ImportSeed {
		for _, item := range corpus {
			j.manifest.Imports = append(j.manifest.Imports, item.SourceDocID)
		}
	}
	if config.Mode != "import" {
		for _, row := range suite {
			j.manifest.Cases = append(j.manifest.Cases, row.CaseID)
		}
	}
	if err == nil {
		err = writeJSONFile(filepath.Join(opts.OutDir, measurementManifest), j.manifest)
	}
	if err == nil {
		err = writeJSONFile(filepath.Join(opts.OutDir, "run_config.json"), config)
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(opts.OutDir, "suite.jsonl"), suiteBody, 0o600)
	}
	if err == nil {
		j.traces, err = os.OpenFile(filepath.Join(opts.OutDir, "recall_traces.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL|os.O_APPEND, 0o600)
	}
	if err == nil {
		err = j.append(executionEvent{Event: "run_started"})
	}
	if err != nil {
		_ = file.Close()
		if j.traces != nil {
			_ = j.traces.Close()
		}
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"plan": j.manifest, "settings": config, "max_client_attempts": maxToolCallAttempts, "default_http_timeout_ms": (5 * time.Minute).Milliseconds()})
	observability.LogDiagnosticBody(observability.WithAuthenticationSecrets(context.Background(), j.secrets...), j.logger.ContextSlog(), j.protector, "evaluation_plan", body, modelprovider.MaxProviderDiagnosticBodyBytes, slog.String("run_id", config.RunID), slog.String("capture_id", j.manifest.CaptureID))
	return j, nil
}

func (j *executionJournal) append(event executionEvent) error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failure != nil {
		return j.failure
	}
	j.sequence++
	event.Sequence, event.CaptureID, event.RunID, event.At = j.sequence, j.manifest.CaptureID, j.manifest.RunID, time.Now().UTC()
	body, err := json.Marshal(event)
	if err == nil {
		var reason observability.CredentialProtectionUnavailableReason
		body, reason = j.protector.ProtectDiagnosticBytes(body, modelprovider.MaxProviderDiagnosticBodyBytes, j.secrets...)
		if reason != observability.CredentialProtectionAvailable {
			err = fmt.Errorf("execution journal credential protection unavailable: %d", reason)
		} else {
			var protected executionEvent
			if err = json.Unmarshal(body, &protected); err == nil {
				expected := event
				expected.Cause = protected.Cause
				if len(expected.Counts) == 0 {
					expected.Counts = nil
				}
				if event.Cause != "" && protected.Cause == "" || !reflect.DeepEqual(expected, protected) {
					err = errors.New("credential protection altered execution record identity")
				}
			}
			if err == nil {
				_, err = j.file.Write(append(body, '\n'))
			}
		}
	}
	if err == nil {
		err = j.file.Sync()
	}
	if err != nil {
		j.failure = fmt.Errorf("persist execution journal: %w", err)
		return j.failure
	}
	level := slog.LevelDebug
	if event.Event == "operation_started" || event.Event == "operation_finished" || event.Event == "run_started" || event.Event == "run_finished" {
		level = slog.LevelInfo
	}
	if event.Event == "retry_scheduled" {
		level = slog.LevelWarn
	}
	if (event.Event == "operation_finished" || event.Event == "run_finished") && event.Outcome != "completed" && event.Outcome != "reused" {
		level = slog.LevelError
	}
	j.logger.LogContext(observability.WithAuthenticationSecrets(context.Background(), j.secrets...), level, "evaluation_"+event.Event, observability.String("run_id", event.RunID), observability.String("capture_id", event.CaptureID), observability.LogAttr{Key: "execution", Value: json.RawMessage(body)})
	return nil
}

func (j *executionJournal) start(ctx context.Context, kind, itemID string) (context.Context, *measuredOperation, error) {
	if j == nil {
		return ctx, nil, nil
	}
	j.mu.Lock()
	j.nextID++
	id := fmt.Sprintf("%s:%d", j.manifest.CaptureID, j.nextID)
	phase := "setup"
	if kind == "recall" {
		phase = "subsequent_query"
		if j.queryCount == 0 {
			phase = "first_query"
		}
		j.queryCount++
	}
	j.mu.Unlock()
	op := &measuredOperation{journal: j, event: executionEvent{Event: "operation_started", OperationID: id, Kind: kind, ItemID: itemID, Phase: phase}, started: time.Now()}
	if err := j.append(op.event); err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, operationContextKey{}, op), op, nil
}

func (op *measuredOperation) finish(cause error, outcome string) error {
	if op == nil {
		return cause
	}
	event := op.event
	event.Event = "operation_finished"
	event.Outcome = outcome
	if event.ElapsedMS == nil {
		event.ElapsedMS = elapsedMilliseconds(op.started)
	}
	if cause != nil {
		event.Outcome, event.ErrorCode = executionError(cause)
		event.Cause = cause.Error()
	}
	return errors.Join(cause, op.journal.append(event))
}

func elapsedMilliseconds(start time.Time) *float64 {
	value := float64(time.Since(start)) / float64(time.Millisecond)
	return &value
}

func executionError(err error) (string, string) {
	if errors.Is(err, context.Canceled) {
		return "cancelled", "request_cancelled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "cancelled", "request_timeout"
	}
	var status *HTTPStatusError
	if errors.As(err, &status) {
		return "failed", fmt.Sprintf("http_%d", status.StatusCode)
	}
	var tool *StructuredToolError
	if errors.As(err, &tool) {
		if values, ok := tool.Result["errors"].([]any); ok {
			for _, value := range values {
				if item, ok := value.(map[string]any); ok && stringValue(item["code"]) != "" {
					return "failed", stringValue(item["code"])
				}
			}
		}
		return "failed", firstNonEmpty(stringValue(tool.Result["code"]), "structured_tool_error")
	}
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "failed", "response_decode_error"
	}
	return "failed", "client_error"
}

func (c *HTTPClient) startAttempt(ctx context.Context, method string) (context.Context, *measuredAttempt, error) {
	op, _ := ctx.Value(operationContextKey{}).(*measuredOperation)
	if op == nil {
		return ctx, nil, nil
	}
	j := op.journal
	j.mu.Lock()
	j.nextID++
	id := fmt.Sprintf("%s:attempt:%d", j.manifest.CaptureID, j.nextID)
	j.mu.Unlock()
	attempt := &measuredAttempt{operation: op, started: time.Now(), event: op.event}
	attempt.event.Event, attempt.event.AttemptID, attempt.event.Method = "attempt_started", id, method
	attempt.event.CorrelationID = uuid.NewString()
	if err := j.append(attempt.event); err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, attemptContextKey{}, attempt), attempt, nil
}

func (a *measuredAttempt) finish(cause error) error {
	if a == nil {
		return cause
	}
	event := a.event
	event.Event = "attempt_finished"
	event.ElapsedMS = elapsedMilliseconds(a.started)
	event.Outcome = "completed"
	if cause != nil {
		event.Outcome, event.ErrorCode = executionError(cause)
		event.Cause = cause.Error()
	}
	return errors.Join(cause, a.operation.journal.append(event))
}

func (j *executionJournal) trace(trace RecallTrace) error {
	if j == nil {
		return nil
	}
	body, err := json.Marshal(trace)
	if err != nil {
		return err
	}
	protected, reason := observability.ProtectDiagnosticText(observability.WithAuthenticationSecrets(context.Background(), j.secrets...), j.protector, body, modelprovider.MaxProviderDiagnosticBodyBytes)
	if reason != observability.CredentialProtectionAvailable {
		return fmt.Errorf("trace credential protection unavailable: %d", reason)
	}
	if string(protected) != string(body) {
		return fmt.Errorf("trace contains operational authentication material")
	}
	j.mu.Lock()
	if j.failure != nil {
		err = j.failure
	} else {
		_, err = j.traces.Write(append(body, '\n'))
		if err == nil {
			err = j.traces.Sync()
		}
		if err != nil {
			j.failure = err
		}
	}
	j.mu.Unlock()
	if err != nil {
		return err
	}
	hash, err := canonicalJSONHash(trace)
	if err != nil {
		return err
	}
	return j.append(executionEvent{Event: "trace_saved", OperationID: trace.ClientOperationID, Kind: "recall", ItemID: trace.CaseID, TraceHash: hash})
}

func (j *executionJournal) finish(cause error) error {
	if j == nil {
		return cause
	}
	events, readErr := loadExecutionEvents(j.directory)
	if readErr == nil {
		j.manifest.Outcomes, readErr = executionOutcomes(j.manifest, events, false)
	}
	cause = errors.Join(cause, readErr)
	lastCompleted := "none"
	for _, event := range events {
		if event.Event == "operation_finished" && event.Kind != "setup" && (event.Outcome == "completed" || event.Outcome == "reused") {
			lastCompleted = event.Kind + ":" + event.ItemID
		}
	}
	if cause == nil {
		terminal := executionEvent{Sequence: len(events) + 1, CaptureID: j.manifest.CaptureID, RunID: j.manifest.RunID, Event: "run_finished", Outcome: "completed", At: time.Now().UTC()}
		_, cause = executionOutcomes(j.manifest, append(events, terminal), true)
	}
	outcome := "completed"
	errorCode := ""
	errorCause := ""
	if cause != nil {
		outcome, errorCode = executionError(cause)
		errorCause = cause.Error()
	}
	eventErr := j.append(executionEvent{Event: "run_finished", Outcome: outcome, ErrorCode: errorCode, Cause: errorCause, Counts: j.manifest.Outcomes, LastCompleted: lastCompleted})
	closeErr := errors.Join(j.file.Close(), j.traces.Close())
	j.manifest.Status = "incomplete"
	if cause == nil && eventErr == nil && closeErr == nil {
		j.manifest.Status = "completed"
	}
	sealErr := sealMeasurementArtifacts(j.directory, &j.manifest, j.manifest.Status == "completed")
	if sealErr != nil {
		j.manifest.Status = "incomplete"
		cause = errors.Join(cause, sealErr)
		logCaptureRejection(j.logger, j.directory, j.manifest.SeedHash, len(j.manifest.Cases), sealErr)
	}

	manifestErr := writeJSONFile(filepath.Join(j.directory, measurementManifest), j.manifest)
	return errors.Join(cause, eventErr, closeErr, manifestErr)
}
