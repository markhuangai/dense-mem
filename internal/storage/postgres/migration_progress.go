package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	gooselock "github.com/pressly/goose/v3/lock"
)

const migrationNoticePrefix = "dense_mem_migration_progress:"
const maxMigrationNoticeBytes = 4096

type migrationProgressKey struct{}

var migrationObservers sync.Map

// MigrationObservation contains counts of one work unit, never business identifiers or row contents.
type MigrationObservation struct {
	Sequence   int64  `json:"sequence"`
	Phase      string `json:"phase"`
	WorkUnit   string `json:"work_unit"`
	State      string `json:"state"`
	Metric     string `json:"metric"`
	Unit       string `json:"unit"`
	Completed  *int64 `json:"completed"`
	Total      *int64 `json:"total"`
	Discovered *int64 `json:"discovered,omitempty"`
	Iterations *int64 `json:"iterations,omitempty"`
}

type MigrationSnapshot struct {
	MigrationObservation
	MigrationVersion           int64
	ExecutionID                string
	PhaseStartedAt             time.Time
	ObservedAt                 time.Time
	LastProgressAt             time.Time
	BackendPID                 int
	BackendStart               time.Time
	TransactionStarted         bool
	TransactionID              string
	CaptureState               string
	CaptureReason              string
	WaitObservationUnavailable bool
	CollectionQueries          int64
	SQLNotices                 int64
}

// MigrationProgress owns only observations; it never decides whether database work committed.
type MigrationProgress struct {
	mu          sync.Mutex
	snapshot    MigrationSnapshot
	observe     func(MigrationSnapshot)
	closed      bool
	sqlStarted  bool
	sqlComplete bool
	sequence    int64
	lastLoop    time.Time
}

func ObserveMigration(ctx context.Context, observe func(MigrationSnapshot)) (context.Context, *MigrationProgress) {
	now := time.Now().UTC()
	p := &MigrationProgress{observe: observe, snapshot: MigrationSnapshot{ExecutionID: uuid.NewString(), PhaseStartedAt: now, ObservedAt: now, CaptureState: "pending"}}
	p.snapshot.MigrationObservation = MigrationObservation{Phase: "prepare", WorkUnit: "migration", State: "started", Metric: "work_units", Unit: "batches"}
	migrationObservers.Store(p.snapshot.ExecutionID, p)
	return context.WithValue(ctx, migrationProgressKey{}, p), p
}

func MigrationProgressFromContext(ctx context.Context) *MigrationProgress {
	p, _ := ctx.Value(migrationProgressKey{}).(*MigrationProgress)
	return p
}

func (p *MigrationProgress) Snapshot() MigrationSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.snapshot
}

func (p *MigrationProgress) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	migrationObservers.Delete(p.snapshot.ExecutionID)
}

func (p *MigrationProgress) CaptureFinished() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.snapshot.CaptureReason != "" {
		return
	}
	if !p.sqlStarted || !p.sqlComplete {
		p.snapshot.CaptureState = "incomplete"
		p.snapshot.CaptureReason = "sql_stream_interrupted_or_unavailable"
	} else {
		p.snapshot.CaptureState = "complete"
	}
}

func (p *MigrationProgress) Incomplete(reason string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.snapshot.CaptureState = "incomplete"
	p.snapshot.CaptureReason = reason
}

func (p *MigrationProgress) MarkWaitObservationUnavailable() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.snapshot.WaitObservationUnavailable {
		return false
	}
	p.snapshot.WaitObservationUnavailable = true
	p.snapshot.CaptureState = "incomplete"
	if p.snapshot.CaptureReason == "" {
		p.snapshot.CaptureReason = "wait_observation_unavailable"
	}
	return true
}

func (p *MigrationProgress) Report(phase, work, state, metric, unit string, completed, total *int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	now := time.Now().UTC()
	if state == "progress" && phase == p.snapshot.Phase && now.Sub(p.lastLoop) < time.Second {
		return
	}
	if state == "progress" {
		p.lastLoop = now
	}
	p.recordLocked(MigrationObservation{Phase: phase, WorkUnit: work, State: state, Metric: metric, Unit: unit, Completed: completed, Total: total}, now)
}

func (p *MigrationProgress) recordLocked(o MigrationObservation, now time.Time) {
	if o.State == "started" || o.Phase != p.snapshot.Phase || o.WorkUnit != p.snapshot.WorkUnit {
		p.snapshot.PhaseStartedAt = now
	}
	if o.State == "completed" || (o.Completed != nil && (p.snapshot.Completed == nil || *o.Completed > *p.snapshot.Completed)) || (o.Discovered != nil && (p.snapshot.Discovered == nil || *o.Discovered > *p.snapshot.Discovered)) {
		p.snapshot.LastProgressAt = now
	}
	o.Completed = copyProgressCount(o.Completed)
	o.Total = copyProgressCount(o.Total)
	o.Discovered = copyProgressCount(o.Discovered)
	o.Iterations = copyProgressCount(o.Iterations)
	p.snapshot.MigrationObservation = o
	p.snapshot.ObservedAt = now
	if p.observe != nil {
		p.observe(p.snapshot)
	}
}

func (p *MigrationProgress) beginTransaction(ctx context.Context, tx *sql.Tx) error {
	var pid int
	var xid string
	var started time.Time
	p.RecordCollectionQuery()
	if err := tx.QueryRowContext(ctx, `SELECT pid,backend_start,pg_current_xact_id()::text FROM pg_stat_activity WHERE pid=pg_backend_pid()`).Scan(&pid, &started, &xid); err != nil {
		return err
	}
	p.mu.Lock()
	p.snapshot.BackendPID, p.snapshot.BackendStart = pid, started
	p.snapshot.TransactionStarted = true
	p.snapshot.TransactionID = xid
	p.snapshot.MigrationVersion = evidenceOnlyMigrationVersion
	id := p.snapshot.ExecutionID
	p.mu.Unlock()
	p.RecordCollectionQuery()
	_, err := tx.ExecContext(ctx, `SELECT set_config('app.migration_execution_id',$1,true),set_config('client_min_messages','notice',true)`, id)
	return err
}

func dispatchMigrationNotice(conn *pgconn.PgConn, notice *pgconn.Notice) {
	receiveMigrationNotice(int(conn.PID()), notice.Message)
}

func receiveMigrationNotice(pid int, message string) {
	if !strings.HasPrefix(message, migrationNoticePrefix) {
		return
	}
	id, body, ok := strings.Cut(strings.TrimPrefix(message, migrationNoticePrefix), ":")
	if !ok {
		return
	}
	value, ok := migrationObservers.Load(id)
	if !ok {
		return
	}
	p := value.(*MigrationProgress)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if p.snapshot.BackendPID != pid {
		return
	}
	if len(message) > maxMigrationNoticeBytes {
		p.snapshot.CaptureState, p.snapshot.CaptureReason = "incomplete", "notice_size_limit"
		return
	}
	var o MigrationObservation
	err := jsonstrict.Decode(strings.NewReader(body), &o, maxMigrationNoticeBytes)
	if err != nil || !validMigrationObservation(o) {
		p.snapshot.CaptureState, p.snapshot.CaptureReason = "incomplete", "invalid_notice"
		return
	}
	if (p.sequence == 0 && (o.Phase != "sql_stream" || o.State != "started")) || p.sqlComplete {
		p.snapshot.CaptureState, p.snapshot.CaptureReason = "incomplete", "invalid_stream_boundary"
	}
	if o.Sequence != p.sequence+1 {
		p.snapshot.CaptureState, p.snapshot.CaptureReason = "incomplete", "notice_sequence_gap"
	}
	if o.Sequence <= p.sequence {
		return
	}
	p.sequence = o.Sequence
	p.snapshot.SQLNotices++
	if o.Phase == "sql_stream" && o.State == "started" {
		p.sqlStarted = true
		if p.snapshot.CaptureReason == "" {
			p.snapshot.CaptureState = "capturing"
		}
	}
	if o.Phase == "sql_stream" && o.State == "completed" {
		p.sqlComplete = true
	}
	p.recordLocked(o, time.Now().UTC())
}

func validMigrationObservation(o MigrationObservation) bool {
	if o.Sequence <= 0 || !migrationProgressPhases[o.Phase] || !migrationProgressWorkUnits[o.WorkUnit] {
		return false
	}
	if o.State != "started" && o.State != "progress" && o.State != "completed" {
		return false
	}
	if !strings.Contains("|rows|records|tables|fingerprints|dependencies|iterations|batches|", "|"+o.Unit+"|") {
		return false
	}
	if !strings.Contains("|counted|matched|affected|verified|rebound|discovered|completed|", "|"+o.Metric+"|") {
		return false
	}
	for _, n := range []*int64{o.Completed, o.Total, o.Discovered, o.Iterations} {
		if n != nil && *n < 0 {
			return false
		}
	}
	return o.Total == nil || o.Completed == nil || *o.Completed <= *o.Total
}

var migrationProgressPhases = progressVocabulary("sql_stream migration_context inventory_lock inventory_count fingerprint_before legal_holds dependency_check guards payload_cleanup session_cleanup ontology_seed dependency_closure ontology_cleanup table_retirement guard_restoration fingerprint_after validation receipt policy_restoration")
var migrationProgressWorkUnits = progressVocabulary("community_memberships community_records community_snapshot_runs community_sources community_summary_attempts community_topic_dependencies community_topic_discovery community_topic_versions community_topic_work dream_cycle_runs dream_diagnostic_captures dream_evidence_target_attempts dream_evidence_target_evaluations dream_path_evaluation_run_links dream_path_evaluations entity_correction_events entity_correction_plans entity_names entity_records entity_resolution_events hypotheses hypothesis_derivation_sources hypothesis_evidence_derivation_sources hypothesis_feedback_events predicate_definitions predicate_registration_events relationship_conflict_ai_assessment_attempts relationship_conflict_ai_assessment_events relationship_conflict_cases relationship_conflict_derived_evidence_tasks relationship_conflict_events relationship_conflict_evidence_derivations relationship_conflict_position_members relationship_conflict_positions relationship_conflict_resolution_plans relationship_conflict_review_runs relationship_correction_events relationship_correction_submissions relationship_cross_references relationship_evidence_supports relationship_observations relationship_records relationship_support_decision_events relationship_transition_events submission_relationship_results team_predicate_definitions value_records verification_events review_tasks placement_assessments placement_items placement_outcomes placement_runs evidence_sources evidence_source_revisions evidence_exact_aliases evidence_occurrences evidence_fragments evidence_security_events evidence_security_signals evidence_quarantines evidence_lifecycle_operations evidence_lifecycle_events evidence_conflict_cases evidence_conflict_positions evidence_conflict_events knowledge_ingests remember_attempts remember_attempt_events remember_attempt_diagnostics remember_failure_artifacts remember_invocation_diagnostics semantic_assessments session_submissions session_events session_extraction_checkpoints session_submission_receipts session_submission_diagnostics ontology_record_heads ontology_record_revisions ontology_source_dependencies ontology_revision_dependencies ontology_publications ontology_assessments ontology_draft_heads ontology_draft_revisions ontology_maintenance_sources ontology_maintenance_markers ontology_maintenance_teams ontology_maintenance_batches recall_feedback_events audit_log operation_logs search_documents search_projection_generations private_memory_migration_records private_memory_migration_constraints app_config v2_compatibility_markers migration catalog retained_evidence constraints triggers functions policies private_memory_legal_holds memory_spaces evidence_only_sessions evidence_only_retired_records evidence_only_retired_revisions evidence_only_rebind_records")

func progressVocabulary(words string) map[string]bool {
	result := map[string]bool{}
	for _, word := range strings.Fields(words) {
		result[word] = true
	}
	return result
}

// MigrationFailure keeps the original cause available to errors.Is/As without printing database payloads upstream.
type MigrationFailure struct {
	Cause  error
	Reason string
}

func (e *MigrationFailure) Error() string { return "evidence_only_migration_failed: " + e.Reason }
func (e *MigrationFailure) Unwrap() error { return e.Cause }

func MigrationFailureReason(err error) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	}
	if errors.Is(err, ontology.ErrMaintenanceUpgradeBlocked) {
		if strings.HasSuffix(err.Error(), " unreconciled attempts require terminal evidence") {
			return "ontology upgrade blocked: unreconciled attempts require terminal evidence"
		}
		return "ontology upgrade blocked"
	}
	reasons := []string{"legal hold", "unhandled retained dependency", "retained fingerprint mismatch", "retained ontology fingerprint", "missing inventory table", "graph catalog remains", "canonical metadata fingerprint phase required"}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "55P03" && pgErr.Message == "canceling statement due to lock timeout" {
			return "lock timeout"
		}
		if pgErr.Code == "P0001" {
			for _, reason := range reasons {
				if strings.HasPrefix(pgErr.Message, "evidence_only_migration_blocked: "+reason) || strings.HasPrefix(pgErr.Message, "evidence_only_migration_failed: "+reason) {
					return reason
				}
			}
			for _, reason := range []string{"membership or ownership translation mismatch", "credential translation mismatch", "membership grant translation mismatch", "SSO identity translation mismatch", "SSO sessions missing memberships", "semantic owners missing aliases", "usage history missing ownership aliases", "portal sessions missing credentials", "migration history missing ownership aliases"} {
				if strings.HasPrefix(pgErr.Message, "identity cleanup blocked: "+reason+" (") {
					return "identity cleanup blocked: " + reason
				}
			}
			if strings.HasPrefix(pgErr.Message, "identity cleanup blocked: unexpected team_profiles dependencies: ") {
				return "identity cleanup blocked: unexpected team_profiles dependencies"
			}
		}
		if validSQLState(pgErr.Code) {
			return fmt.Sprintf("database operation failed (SQLSTATE %s)", pgErr.Code)
		}
		return "database operation failed; invalid SQLSTATE omitted"
	}
	if errors.Is(err, sql.ErrConnDone) || strings.Contains(err.Error(), "sql: database is closed") {
		return "database connection closed"
	}
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		if strings.HasPrefix(cause.Error(), "evidence_only_migration_blocked: retained ontology fingerprint:") {
			return "retained ontology fingerprint"
		}
	}
	return "migration operation failed; database payload omitted"
}

func (p *MigrationProgress) SampleWait(ctx context.Context, db *sql.DB) (string, string, int, error) {
	s := p.Snapshot()
	if s.BackendPID == 0 {
		return "", "", 0, nil
	}
	p.mu.Lock()
	p.snapshot.CollectionQueries++
	p.mu.Unlock()
	var kind, event string
	var blockers int
	if s.Phase == "migration_lock" && s.State == "started" {
		err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND granted AND classid=0 AND objid=$1 AND pid<>$2`, gooselock.DefaultLockID, s.BackendPID).Scan(&blockers)
		return "Lock", "AdvisoryLockAcquisition", blockers, err
	}
	err := db.QueryRowContext(ctx, `SELECT COALESCE(wait_event_type,''),COALESCE(wait_event,''),cardinality(pg_blocking_pids(pid)) FROM pg_stat_activity WHERE pid=$1 AND backend_start=$2`, s.BackendPID, s.BackendStart).Scan(&kind, &event, &blockers)
	return kind, event, blockers, err
}

func copyProgressCount(value *int64) *int64 {
	if value == nil {
		return nil
	}
	n := *value
	return &n
}

type observedMigrationLocker struct{ gooselock.SessionLocker }

func (l observedMigrationLocker) SessionLock(ctx context.Context, conn *sql.Conn) error {
	p := MigrationProgressFromContext(ctx)
	if p == nil {
		return l.SessionLocker.SessionLock(ctx, conn)
	}
	var pid int
	var started time.Time
	p.RecordCollectionQuery()
	if err := conn.QueryRowContext(ctx, `SELECT pid,backend_start FROM pg_stat_activity WHERE pid=pg_backend_pid()`).Scan(&pid, &started); err != nil {
		return err
	}
	p.mu.Lock()
	p.snapshot.BackendPID = pid
	p.snapshot.BackendStart = started
	p.mu.Unlock()
	p.Report("migration_lock", "migration", "started", "acquired", "locks", nil, nil)
	if err := l.SessionLocker.SessionLock(ctx, conn); err != nil {
		return err
	}
	n := int64(1)
	p.Report("migration_lock", "migration", "completed", "acquired", "locks", &n, &n)
	return nil
}

func (p *MigrationProgress) SetMigrationVersion(version int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot.MigrationVersion = version
}

func (p *MigrationProgress) RecordCollectionQuery() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.snapshot.CollectionQueries++
}
