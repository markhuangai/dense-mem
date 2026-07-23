package domain

import "time"

const (
	V2MigrationMarkerKindCutover  = "v2_cutover"
	V2MigrationMarkerCompatible   = "compatible"
	V2MigrationMarkerIncompatible = "incompatible"
	V2MigrationMarkerCorrupt      = "corrupt"
)

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
