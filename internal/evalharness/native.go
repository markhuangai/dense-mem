package evalharness

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/modelprovider"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

//go:embed maintenance_snapshot.sql
var MaintenanceSnapshotSQL string

type Scope struct {
	TeamID       string `json:"team_id"`
	OwnerID      string `json:"owner_id"`
	SpaceID      string `json:"space_id"`
	Generation   int64  `json:"generation"`
	CredentialID string `json:"credential_id"`
}

type Native struct {
	DB                  *sql.DB
	Scope               Scope
	ControlURL          string
	ControlToken        string
	Client              *HTTPClient
	StopRuntime         func(context.Context, bool) error
	RuntimeLogs         func(context.Context) error
	ServerUsage         map[string]UsageTotals
	RequireRuntimeUsage bool
}

type NativeAttempt struct {
	TeamID         string     `json:"team_id"`
	SpaceID        string     `json:"space_id"`
	Generation     int64      `json:"generation"`
	WindowID       string     `json:"window_id"`
	OperationKey   string     `json:"operation_key"`
	AssessmentID   string     `json:"assessment_id"`
	Number         int        `json:"number"`
	AdmittedAt     time.Time  `json:"admitted_at"`
	ReconciledAt   *time.Time `json:"reconciled_at"`
	Terminal       bool       `json:"terminal"`
	UsageAvailable bool       `json:"usage_available"`
	Input          int64      `json:"input_tokens"`
	Output         int64      `json:"output_tokens"`
	Total          int64      `json:"total_tokens"`
	Outcome        string     `json:"outcome"`
}

type AttemptMeasurement struct {
	Attempts     int   `json:"attempts"`
	UnknownUsage int   `json:"unknown_usage"`
	PartialUsage int   `json:"partial_usage"`
	Unfinished   int   `json:"unfinished"`
	Input        int64 `json:"input_tokens"`
	Output       int64 `json:"output_tokens"`
	Total        int64 `json:"total_tokens"`
}

func MeasureAttempts(attempts []NativeAttempt) (AttemptMeasurement, error) {
	result := AttemptMeasurement{}
	seen := map[string]bool{}
	for _, attempt := range attempts {
		key := attempt.OperationKey + ":" + attempt.AssessmentID + ":" + strconv.Itoa(attempt.Number)
		if seen[key] || attempt.OperationKey == "" || attempt.AssessmentID == "" || attempt.Number <= 0 || attempt.Input < 0 || attempt.Output < 0 || attempt.Total < 0 {
			return result, errors.New("invalid or duplicated durable provider attempt")
		}
		seen[key] = true
		result.Attempts++
		if !attempt.Terminal || attempt.ReconciledAt == nil {
			result.Unfinished++
		}
		if !attempt.UsageAvailable {
			result.UnknownUsage++
		} else {
			if attempt.Input+attempt.Output != attempt.Total {
				result.PartialUsage++
			}
			result.Input += attempt.Input
			result.Output += attempt.Output
			result.Total += attempt.Total
		}
	}
	return result, nil
}

type NativeObservation struct {
	CanonicalHash      string                     `json:"canonical_hash"`
	EvidenceCount      int                        `json:"evidence_count"`
	ForeignSources     int                        `json:"foreign_sources"`
	ActiveBatches      int                        `json:"active_batches"`
	ActivePreparations int                        `json:"active_preparations"`
	Unreconciled       int                        `json:"unreconciled_attempts"`
	ForeignWork        int                        `json:"foreign_work"`
	ProviderAttempts   []NativeAttempt            `json:"provider_attempts"`
	Ontology           ontology.MaintenanceStatus `json:"ontology"`
	Search             struct {
		Status   string `json:"status"`
		Expected int    `json:"expected_documents"`
		Current  int    `json:"current_documents"`
		Drifted  int    `json:"drifted_documents"`
		Contract *struct {
			Model      string `json:"model"`
			Dimensions int    `json:"dimensions"`
			Strategy   string `json:"index_strategy"`
		} `json:"contract"`
	} `json:"search"`
}

func (n *Native) ResolveScope(ctx context.Context) error {
	if _, err := uuid.Parse(n.Scope.TeamID); err != nil {
		return errors.New("invalid runner team identity")
	}
	if _, err := uuid.Parse(n.Scope.CredentialID); err != nil {
		return errors.New("invalid runner credential identity")
	}
	tx, err := n.systemTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT a.legacy_owner_id::text,m.id::text,m.generation FROM ownership_aliases a JOIN memory_spaces m ON m.team_id=a.team_id AND m.kind='team_shared' AND m.lifecycle_state='active' WHERE a.team_id=$1::uuid AND a.credential_id=$2::uuid`, n.Scope.TeamID, n.Scope.CredentialID).Scan(&n.Scope.OwnerID, &n.Scope.SpaceID, &n.Scope.Generation)
	return err
}

func (n *Native) systemTx(ctx context.Context) (*sql.Tx, error) {
	tx, err := n.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `SELECT set_config('app.tx_mode','system',true),set_config('app.storage_layout','feature-storage-v1',true)`); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func (n *Native) Control(ctx context.Context, method, path string, input, out any) error {
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := n.Client.doJSON(ctx, method, n.ControlURL+"/control/api"+path, n.ControlToken, input, &envelope); err != nil {
		return err
	}
	if out != nil {
		if len(envelope.Data) == 0 {
			return errors.New("control response missing data")
		}
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

func (n *Native) rememberProviderTransportFailure(ctx context.Context, receipt map[string]any) (status int, networkFailure string, checked bool) {
	if submissionProcessingState(receipt) != "failed" {
		return 0, "", false
	}
	items, _ := receipt["errors"].([]any)
	if len(items) == 0 {
		return 0, "", false
	}
	failure, _ := items[0].(map[string]any)
	if failure["retryable"] != true || failure["next_action"] != "retry_same_request" {
		return 0, "", false
	}
	details, _ := failure["details"].(map[string]any)
	code := stringValue(failure["code"])
	var phase string
	switch {
	case (code == "provider_unavailable" || code == "request_timeout") && details["component"] == "remember.assessment":
		phase = "assessment"
	case (code == "embedding_unavailable" || code == "request_timeout") && details["component"] == "remember.embedding":
		phase = "embedding"
	default:
		return 0, "", false
	}
	id, err := uuid.Parse(stringValue(receipt["submission_id"]))
	if err != nil || id == uuid.Nil {
		return 0, "", true
	}
	var detail struct {
		TeamID        string `json:"team_id"`
		AttemptID     string `json:"attempt_id"`
		CorrelationID string `json:"correlation_id"`
		Outcome       string `json:"outcome"`
		FailedPhase   string `json:"failed_phase"`
		ErrorCode     string `json:"error_code"`
		Retryable     bool   `json:"retryable"`
		Diagnostics   struct {
			ProviderExchanges []struct {
				Component  string `json:"component"`
				StatusCode int    `json:"status_code"`
				Outcome    string `json:"outcome"`
			} `json:"provider_exchanges"`
		} `json:"diagnostics"`
	}
	lookup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err = n.Control(lookup, http.MethodGet, "/teams/"+n.Scope.TeamID+"/remember-attempts/"+id.String(), nil, &detail)
	exchanges := detail.Diagnostics.ProviderExchanges
	if err != nil || detail.TeamID != n.Scope.TeamID || detail.AttemptID != id.String() ||
		detail.CorrelationID != stringValue(receipt["correlation_id"]) || detail.Outcome != "failed" ||
		detail.FailedPhase != phase || detail.ErrorCode != code || !detail.Retryable || len(exchanges) == 0 {
		return 0, "", true
	}
	// Earlier exchanges can fail and recover within one Remember attempt.
	last := exchanges[len(exchanges)-1]
	if (phase == "assessment" && last.Component != "assessor" && last.Component != "decision") ||
		(phase == "embedding" && last.Component != "embedding") {
		return 0, "", true
	}
	if modelprovider.IsTransientNetworkFailure(last.Outcome) {
		networkFailure = last.Outcome
	}
	if last.StatusCode != 0 && (last.StatusCode < 100 || last.StatusCode > 599) {
		return 0, "", true
	}
	return last.StatusCode, networkFailure, true
}

func (n *Native) Snapshot(ctx context.Context) (NativeObservation, error) {
	var observation NativeObservation
	tx, err := n.systemTx(ctx)
	if err != nil {
		return observation, err
	}
	defer tx.Rollback()
	const fingerprint = `WITH canonical AS (
 SELECT 'evidence_fragments' kind,to_jsonb(r)::text body FROM evidence_fragments r WHERE team_id=$1::uuid
 UNION ALL SELECT 'evidence_occurrences',to_jsonb(r)::text FROM evidence_occurrences r WHERE team_id=$1::uuid
 UNION ALL SELECT 'evidence_sources',to_jsonb(r)::text FROM evidence_sources r WHERE team_id=$1::uuid
 UNION ALL SELECT 'evidence_source_revisions',to_jsonb(r)::text FROM evidence_source_revisions r WHERE team_id=$1::uuid
 UNION ALL SELECT 'evidence_lifecycle_events',to_jsonb(r)::text FROM evidence_lifecycle_events r WHERE team_id=$1::uuid
 UNION ALL SELECT 'evidence_quarantines',to_jsonb(r)::text FROM evidence_quarantines r WHERE team_id=$1::uuid
 UNION ALL SELECT 'remember_submitted_items',to_jsonb(r)::text FROM remember_submitted_items r WHERE team_id=$1::uuid
 UNION ALL SELECT 'remember_item_partitions',to_jsonb(r)::text FROM remember_item_partitions r WHERE team_id=$1::uuid
 ) SELECT encode(sha256(convert_to(COALESCE(jsonb_agg(jsonb_build_array(kind,body) ORDER BY kind,body)::text,'[]'),'UTF8')),'hex'),
 (SELECT count(*) FROM evidence_fragments WHERE team_id=$1::uuid),(SELECT count(*) FROM evidence_fragments WHERE team_id<>$1::uuid) FROM canonical`
	if err = tx.QueryRowContext(ctx, fingerprint, n.Scope.TeamID).Scan(&observation.CanonicalHash, &observation.EvidenceCount, &observation.ForeignSources); err != nil {
		return observation, err
	}
	var raw []byte
	if err = tx.QueryRowContext(ctx, MaintenanceSnapshotSQL, n.Scope.TeamID, n.Scope.OwnerID, n.Scope.SpaceID, n.Scope.Generation, n.Scope.CredentialID).Scan(&raw); err != nil {
		return observation, err
	}
	if err = json.Unmarshal(raw, &observation); err != nil {
		return observation, err
	}
	if err = n.Control(ctx, http.MethodGet, "/ontology/status", nil, &observation.Ontology); err != nil {
		return observation, err
	}
	if err = n.Control(ctx, http.MethodGet, "/search/convergence", nil, &observation.Search); err != nil {
		return observation, err
	}
	return observation, nil
}

func NativeReady(observation NativeObservation, ontologyEnabled, paused bool, manifest Manifest, teamID string) error {
	if observation.ForeignSources != 0 || observation.ForeignWork != 0 {
		return errors.New("dataset runtime contains foreign sources or work")
	}
	search := observation.Search
	if search.Contract == nil || search.Contract.Model != manifest.EmbeddingModel || search.Contract.Dimensions != manifest.EmbeddingDimensions || search.Status != "converged" || search.Drifted != 0 || search.Expected != search.Current {
		return errors.New("search or embedding contract is not converged")
	}
	status := observation.Ontology
	if status.Enabled != ontologyEnabled || status.Paused != paused {
		return errors.New("unexpected native ontology state")
	}
	if err := nativeDrained(observation); err != nil {
		return err
	}
	if !ontologyEnabled {
		return nil
	}
	if err := fullOntologyCoverage(status); err != nil {
		return err
	}
	return nativeOntologyPolicy(status, manifest, teamID)
}

func fullOntologyCoverage(status ontology.MaintenanceStatus) error {
	if status.ConfigurationError != "" || !status.DiscoveryComplete || !status.CoverageComplete || status.Counts.Pending != 0 || status.Counts.Failed != 0 || status.Counts.Ambiguous != 0 || status.Counts.Eligible <= 0 || status.Counts.Eligible != status.Counts.Organized {
		return errors.New("ontology coverage is incomplete")
	}
	return nil
}

func nativeOntologyPolicy(status ontology.MaintenanceStatus, manifest Manifest, teamID string) error {
	if status.Window == nil || status.Window.Policy.Model != manifest.Model || status.Window.Policy.ContextWindowTokens != int64(manifest.ContextWindowTokens) || status.Window.Policy.MaxConcurrency != 4 || status.Window.Policy.TeamConcurrency(teamID) != 4 {
		return errors.New("effective ontology capacity differs from campaign settings")
	}
	return nil
}

func nativeDrained(observation NativeObservation) error {
	if observation.ActiveBatches != 0 || observation.ActivePreparations != 0 || observation.Unreconciled != 0 || observation.Ontology.ActiveWork != 0 {
		return errors.New("native model work has not drained")
	}
	measurement, err := MeasureAttempts(observation.ProviderAttempts)
	if err != nil {
		return err
	}
	if measurement.Unfinished != 0 {
		return errors.New("durable attempts have not reconciled")
	}
	return nil
}

func (n *Native) Pause(ctx context.Context) error {
	return n.Control(ctx, http.MethodPost, "/ontology/pause", map[string]string{"operation_key": uuid.NewString()}, nil)
}

func (n *Native) VerifyMappings(ctx context.Context, corpus []CorpusItem, mapping KnowledgeMapping) (map[string]CanonicalEvidence, error) {
	if len(mapping.BySourceDocID) != len(corpus) || len(mapping.Partitions) != len(corpus) {
		return nil, errors.New("import mapping count is incomplete")
	}
	stored, canonical, err := n.readSeedMappings(ctx, corpus, false)
	if err != nil {
		return nil, err
	}
	if len(stored.Partitions) != len(corpus) {
		return nil, errors.New("native completed import denominator is incomplete")
	}
	if !reflect.DeepEqual(stored.Partitions, mapping.Partitions) {
		return nil, errors.New("native partition receipt mismatch")
	}
	return canonical, nil
}

func (n *Native) readSeedMappings(ctx context.Context, corpus []CorpusItem, verifyRequest bool) (KnowledgeMapping, map[string]CanonicalEvidence, error) {
	mapping := newKnowledgeMapping()
	wanted := map[string]CorpusItem{}
	for _, item := range corpus {
		if _, duplicate := wanted[item.SourceDocID]; duplicate || item.SourceDocID == "" {
			return mapping, nil, errors.New("source identity is empty or duplicated")
		}
		wanted[item.SourceDocID] = item
	}
	tx, err := n.systemTx(ctx)
	if err != nil {
		return mapping, nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT a.idempotency_key,a.request_hash,s.content,a.public_result
FROM remember_attempts a LEFT JOIN remember_submitted_items s ON s.team_id=a.team_id AND s.ingest_id=a.attempt_id
WHERE a.team_id=$1::uuid AND a.owner_profile_id=$2::uuid AND a.space_id=$3::uuid AND a.space_generation=$4
AND a.outcome='completed' ORDER BY a.idempotency_key`, n.Scope.TeamID, n.Scope.OwnerID, n.Scope.SpaceID, n.Scope.Generation)
	if err != nil {
		return mapping, nil, err
	}
	defer rows.Close()
	completed := make([]CorpusItem, 0, len(corpus))
	for rows.Next() {
		var key, hash string
		var content sql.NullString
		var raw []byte
		if err = rows.Scan(&key, &hash, &content, &raw); err != nil {
			return mapping, nil, err
		}
		source, prefix := strings.CutPrefix(key, "eval:")
		item, exists := wanted[source]
		if !exists {
			source = strings.TrimSuffix(source, ":resubmit1")
			item, exists = wanted[source]
		}
		if _, duplicate := mapping.BySourceDocID[source]; !prefix || !exists || duplicate || !content.Valid || item.Content != content.String {
			return mapping, nil, errors.New("native import source identity mismatch")
		}
		if verifyRequest {
			baseKey, expectedHash, err := seedRememberIdentity(item)
			if err != nil {
				return mapping, nil, err
			}
			if hash != expectedHash || (key != baseKey && key != baseKey+":resubmit1") {
				return mapping, nil, errors.New("checkpoint receipt differs from the original Remember request")
			}
		}
		var result map[string]any
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		if err = decoder.Decode(&result); err != nil {
			return mapping, nil, err
		}
		mapping, err = n.Client.importTerminalRememberResult(item, result, mapping)
		if err != nil {
			return mapping, nil, err
		}
		completed = append(completed, item)
	}
	if err = rows.Err(); err != nil {
		return mapping, nil, err
	}
	canonical := map[string]CanonicalEvidence{}
	rows, err = tx.QueryContext(ctx, `SELECT fragment_id::text,content,metadata->>'source_doc_id' FROM evidence_fragments WHERE team_id=$1::uuid AND space_id=$2::uuid AND space_generation=$3`, n.Scope.TeamID, n.Scope.SpaceID, n.Scope.Generation)
	if err != nil {
		return mapping, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, content, source string
		if err = rows.Scan(&id, &content, &source); err != nil {
			return mapping, nil, err
		}
		item, ok := wanted[source]
		if !ok || !strings.Contains(item.Content, content) {
			return mapping, nil, errors.New("canonical Evidence has no exact published source")
		}
		canonical[id] = CanonicalEvidence{SourceID: source, Content: content}
	}
	if err = rows.Err(); err != nil {
		return mapping, nil, err
	}
	return mapping, canonical, ValidateMappings(completed, mapping, canonical)
}
