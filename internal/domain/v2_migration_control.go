package domain

import "time"

const (
	V2MigrationStateNotRequired  = "not_required"
	V2MigrationStateRequired     = "required"
	V2MigrationStatePreflight    = "preflight"
	V2MigrationStateReady        = "ready"
	V2MigrationStateRunning      = "running"
	V2MigrationStatePaused       = "paused_retryable"
	V2MigrationStateFailed       = "failed"
	V2MigrationStateVerifying    = "verifying"
	V2MigrationStateReadyCutover = "ready_to_cutover"
	V2MigrationStateCutOver      = "cut_over"
	V2MigrationStateIncompatible = "incompatible"

	V2MigrationActionPreflightApproved = "preflight_approved"
	V2MigrationActionStarted           = "started"
	V2MigrationActionPaused            = "paused"
	V2MigrationActionResumed           = "resumed"
	V2MigrationActionFailed            = "failed"

	V2MigrationMarkerKindCutover  = "v2_cutover"
	V2MigrationMarkerCompatible   = "compatible"
	V2MigrationMarkerIncompatible = "incompatible"
	V2MigrationMarkerCorrupt      = "corrupt"
)

type V2MigrationRun struct {
	RunID                    string         `json:"run_id"`
	MigrationContractVersion string         `json:"migration_contract_version"`
	CorpusVersion            string         `json:"corpus_version"`
	SourceKind               string         `json:"source_kind"`
	State                    string         `json:"state"`
	Phase                    string         `json:"phase,omitempty"`
	Required                 bool           `json:"required"`
	PreflightApproved        bool           `json:"preflight_approved"`
	BackupReference          string         `json:"backup_reference,omitempty"`
	PreflightChecks          map[string]any `json:"preflight_checks,omitempty"`
	CorpusWatermark          string         `json:"corpus_watermark,omitempty"`
	CorpusHash               string         `json:"corpus_hash,omitempty"`
	TotalItems               int            `json:"total_items"`
	CompletedItems           int            `json:"completed_items"`
	FailedItems              int            `json:"failed_items"`
	ExcludedItems            int            `json:"excluded_items"`
	LastError                string         `json:"last_error,omitempty"`
	Retryable                bool           `json:"retryable"`
	LeaseOwner               string         `json:"lease_owner,omitempty"`
	CheckpointKey            string         `json:"checkpoint_key,omitempty"`
	CheckpointValue          map[string]any `json:"checkpoint_value,omitempty"`
	StartedAt                *time.Time     `json:"started_at,omitempty"`
	CompletedAt              *time.Time     `json:"completed_at,omitempty"`
	CutoverAt                *time.Time     `json:"cutover_at,omitempty"`
	CreatedAt                time.Time      `json:"created_at"`
	UpdatedAt                time.Time      `json:"updated_at"`
}

type V2MigrationOperatorAction struct {
	ActionID  string         `json:"action_id"`
	RunID     string         `json:"run_id,omitempty"`
	Action    string         `json:"action"`
	Actor     string         `json:"actor"`
	RemoteIP  string         `json:"remote_ip,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type V2CompatibilityMarker struct {
	MarkerID       string         `json:"marker_id"`
	MarkerKind     string         `json:"marker_kind"`
	Version        string         `json:"version"`
	Status         string         `json:"status"`
	RunID          string         `json:"run_id,omitempty"`
	CorpusHash     string         `json:"corpus_hash,omitempty"`
	GateReportHash string         `json:"gate_report_hash,omitempty"`
	Metadata       map[string]any `json:"metadata,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
}

type V2MigrationControlStatus struct {
	State            string                      `json:"state"`
	Required         bool                        `json:"required"`
	DataPlaneAllowed bool                        `json:"data_plane_allowed"`
	ReadinessMessage string                      `json:"readiness_message"`
	Run              *V2MigrationRun             `json:"run,omitempty"`
	Marker           *V2CompatibilityMarker      `json:"marker,omitempty"`
	Actions          []V2MigrationOperatorAction `json:"actions,omitempty"`
}
