package evalharness

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
)

// PreparedOntology seals the derived state of an immutable bootable child.
type PreparedOntology struct {
	Version        int                          `json:"version"`
	ParentImage    string                       `json:"parent_seed_image"`
	SourceRevision string                       `json:"source_revision"`
	Image          string                       `json:"image"`
	Fingerprint    string                       `json:"ontology_sha256"`
	AttemptHash    string                       `json:"attempts_sha256"`
	PolicyVersion  string                       `json:"policy_version"`
	Model          string                       `json:"model"`
	Context        int                          `json:"context_window_tokens"`
	Workers        int                          `json:"workers"`
	Counts         ontology.MaintenanceCounts   `json:"counts"`
	Windows        []ontology.MaintenanceWindow `json:"windows"`
	Usage          AttemptMeasurement           `json:"usage"`
	ElapsedMS      float64                      `json:"elapsed_ms"`
}

func verifyPreparedOntology(ctx context.Context, n *Native, manifest Manifest, sealed *PreparedOntology, snapshot NativeObservation) error {
	if sealed.Version != 1 || !revisionPattern.MatchString(sealed.SourceRevision) || !imagePattern.MatchString(sealed.Image) ||
		!validParentSeed(manifest, sealed.ParentImage) || sealed.Model != manifest.Model || sealed.Context != manifest.ContextWindowTokens ||
		sealed.Workers != ontologyWorkers(Manifest{Operation: "bake-ontology"}) || sealed.PolicyVersion != ontology.OrganizationPolicyVersion {
		return errors.New("ontology preparation identity or policy differs")
	}
	// Validate paused state and search without consulting the ephemeral active window.
	frozen := snapshot
	frozen.Ontology.Enabled = false
	if !snapshot.Ontology.Enabled || NativeReady(frozen, false, true, manifest, n.Scope.TeamID) != nil || fullOntologyCoverage(snapshot.Ontology) != nil {
		return errors.New("restored ontology is not complete and paused")
	}
	windows, err := n.preparationWindows(ctx, snapshot.ProviderAttempts)
	if err != nil {
		return err
	}
	if err := verifyPreparationWindows(windows, sealed.Model, sealed.Context, sealed.Workers, n.Scope.TeamID); err != nil {
		return err
	}
	fingerprint, err := n.ontologyFingerprint(ctx)
	if err != nil {
		return err
	}
	attemptHash, usage, err := preparedAttempts(snapshot.ProviderAttempts)
	if err != nil {
		return err
	}
	if fingerprint != sealed.Fingerprint || attemptHash != sealed.AttemptHash || usage != sealed.Usage ||
		snapshot.Ontology.Counts != sealed.Counts || !reflect.DeepEqual(windows, sealed.Windows) {
		return errors.New("restored ontology, terminal attempts or coverage differ from the preparation receipt")
	}
	return nil
}

func preparedAttempts(attempts []NativeAttempt) (string, AttemptMeasurement, error) {
	usage, err := MeasureAttempts(attempts)
	if err != nil || usage.Unfinished != 0 {
		return "", usage, errors.New("ontology preparation has unverifiable provider attempts")
	}
	data, err := json.Marshal(attempts)
	if err != nil {
		return "", usage, err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), usage, nil
}

func (n *Native) preparationWindows(ctx context.Context, attempts []NativeAttempt) ([]ontology.MaintenanceWindow, error) {
	tx, err := n.systemTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	used := map[string]bool{}
	for _, attempt := range attempts {
		used[attempt.WindowID] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT window_id::text, starts_at, ends_at, policy FROM ontology_maintenance_windows ORDER BY starts_at,window_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	windows := []ontology.MaintenanceWindow{}
	for rows.Next() {
		var window ontology.MaintenanceWindow
		var raw []byte
		if err := rows.Scan(&window.ID, &window.StartsAt, &window.EndsAt, &raw); err != nil {
			return nil, err
		}
		if !used[window.ID] {
			continue
		}
		if err := json.Unmarshal(raw, &window.Policy); err != nil {
			return nil, err
		}
		window.StartsAt, window.EndsAt = window.StartsAt.UTC(), window.EndsAt.UTC()
		windows = append(windows, window)
		delete(used, window.ID)
	}
	if len(used) != 0 {
		return nil, errors.New("ontology preparation attempt has no durable window")
	}
	return windows, rows.Err()
}

func verifyPreparationWindows(windows []ontology.MaintenanceWindow, model string, context, workers int, team string) error {
	if len(windows) == 0 {
		return errors.New("ontology preparation has no durable provider window")
	}
	for _, window := range windows {
		if !window.Policy.Enabled || window.Policy.Model != model || window.Policy.ContextWindowTokens != int64(context) ||
			window.Policy.MaxConcurrency != workers || window.Policy.TeamMaxConcurrency != workers || window.Policy.TeamConcurrency(team) != workers {
			return errors.New("ontology preparation window policy differs")
		}
	}
	return nil
}

func (n *Native) ontologyFingerprint(ctx context.Context) (string, error) {
	tx, err := n.systemTx(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	const fingerprint = `WITH heads AS (
 SELECT * FROM ontology_record_heads WHERE team_id=$1::uuid AND shared_space_id=$2::uuid AND space_generation=$3
 ), input AS (
 SELECT 'catalog' kind,to_jsonb(c) body FROM ontology_catalog_heads c WHERE team_id=$1::uuid AND shared_space_id=$2::uuid AND space_generation=$3
 UNION ALL SELECT 'head',to_jsonb(h) FROM heads h
 UNION ALL SELECT 'revision',to_jsonb(r) FROM heads h JOIN ontology_revision_records r USING(team_id,shared_space_id,space_generation,record_id,version)
 UNION ALL SELECT 'source_dependency',to_jsonb(d) FROM heads h JOIN ontology_source_dependencies d USING(team_id,shared_space_id,space_generation,record_id) WHERE d.record_version=h.version
 UNION ALL SELECT 'revision_dependency',to_jsonb(d) FROM heads h JOIN ontology_revision_dependencies d USING(team_id,shared_space_id,space_generation,record_id) WHERE d.record_version=h.version
 UNION ALL SELECT 'source',to_jsonb(s) FROM ontology_maintenance_sources s WHERE team_id=$1::uuid AND shared_space_id=$2::uuid AND space_generation=$3
 ) SELECT encode(sha256(convert_to(COALESCE(jsonb_agg(jsonb_build_array(kind,body) ORDER BY kind,body)::text,'[]'),'UTF8')),'hex') FROM input`
	var hash string
	err = tx.QueryRowContext(ctx, fingerprint, n.Scope.TeamID, n.Scope.SpaceID, n.Scope.Generation).Scan(&hash)
	return hash, err
}

func BakeOntology(ctx context.Context, manifest Manifest, corpus []CorpusItem, client *HTTPClient, native *Native) (runErr error) {
	started := time.Now()
	work, cancel := context.WithDeadline(ctx, manifest.ModelDeadline)
	defer cancel()
	seed, _, _, initial, err := verifySeedContent(work, manifest, corpus, client, native)
	if err != nil {
		return err
	}
	if seed.Ontology != nil {
		return errors.New("ontology preparation needs an ontology-off parent")
	}
	if manifest.ResumeScope == nil {
		if err := verifySeedCondition(work, native, manifest, seed, initial); err != nil {
			return err
		}
	} else if *manifest.ResumeScope != native.Scope || !initial.Ontology.Paused || nativeDrained(initial) != nil {
		return errors.New("ontology preparation checkpoint is not drained in its original scope")
	}
	defer func() {
		if runErr == nil {
			return
		}
		drain, stop := context.WithTimeout(context.WithoutCancel(ctx), 16*time.Minute)
		defer stop()
		runErr = errors.Join(runErr, pausePreparation(drain, native))
	}()
	if err := native.EnableOntology(work, manifest); err != nil {
		return err
	}
	path := filepath.Join(manifest.BakeStateDir, "recovery.json")
	var retries []preparationRetry
	if err := readJSON(path, &retries); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for {
		var status ontology.MaintenanceStatus
		if err := native.Control(work, http.MethodGet, "/ontology/status", nil, &status); err != nil {
			return err
		}
		if !status.Enabled || status.Paused || status.ConfigurationError != "" {
			return errors.New("local ontology preparation policy is unavailable")
		}
		if status.Window != nil {
			if err := nativeOntologyPolicy(status, manifest, native.Scope.TeamID); err != nil {
				return err
			}
		}
		if client.Capture != nil && client.Capture.Logger != nil {
			client.Capture.Logger.Info("ontology preparation progress", "run_id", manifest.RunID, "dataset", manifest.Dataset,
				"workers", ontologyWorkers(manifest), "counts", status.Counts, "discovery_complete", status.DiscoveryComplete,
				"active", status.ActiveWork, "elapsed_ms", elapsedMS(started),
				"organized_per_minute", float64(status.Counts.Organized-initial.Ontology.Counts.Organized)/max(time.Since(started).Minutes(), 1))
		}
		if status.DiscoveryComplete && status.Counts.Pending == 0 && status.ActiveWork == 0 && status.Window != nil {
			snapshot, err := native.Snapshot(work)
			if err != nil {
				return err
			}
			if snapshot.CanonicalHash != seed.CanonicalHash || snapshot.ForeignSources != 0 || snapshot.ForeignWork != 0 {
				return errors.New("ontology preparation changed canonical data or scope")
			}
			if nativeDrained(snapshot) != nil || snapshot.Ontology.Counts.Pending != 0 || !snapshot.Ontology.DiscoveryComplete {
				if snapshot.Unreconciled != 0 && snapshot.Ontology.ActiveWork == 0 {
					return errors.New("ontology preparation has stranded native attempts")
				}
			} else {
				waiting, err := retryPreparation(work, native, manifest, snapshot, path, &retries)
				if err != nil {
					return err
				}
				if !waiting {
					if snapshot.Ontology.Counts.Ambiguous != 0 {
						return native.recordPreparationAmbiguity(work)
					}
					if err := fullOntologyCoverage(snapshot.Ontology); err != nil {
						return err
					}
					break
				}
			}
		}
		select {
		case <-work.Done():
			return work.Err()
		case <-time.After(10 * time.Second):
		}
	}
	if err := pausePreparation(work, native); err != nil {
		return err
	}
	seed, _, _, ready, err := verifySeedContent(work, manifest, corpus, client, native)
	if err != nil {
		return err
	}
	sealed := &PreparedOntology{
		Version: 1, ParentImage: manifest.SeedImage, SourceRevision: manifest.SourceRevision, Image: manifest.Image,
		Model: manifest.Model, Context: manifest.ContextWindowTokens, Workers: ontologyWorkers(manifest),
		PolicyVersion: ontology.OrganizationPolicyVersion, Counts: ready.Ontology.Counts, ElapsedMS: elapsedMS(started),
	}
	sealed.Fingerprint, err = native.ontologyFingerprint(work)
	if err != nil {
		return err
	}
	sealed.Windows, err = native.preparationWindows(work, ready.ProviderAttempts)
	if err != nil {
		return err
	}
	sealed.AttemptHash, sealed.Usage, err = preparedAttempts(ready.ProviderAttempts)
	if err != nil {
		return err
	}
	if err := verifyPreparedOntology(work, native, manifest, sealed, ready); err != nil {
		return err
	}
	seed.Version, seed.Ontology = 1, sealed
	if err = native.DB.QueryRowContext(work, `SELECT COALESCE(max(version_id),0) FROM (SELECT DISTINCT ON (version_id) version_id,is_applied FROM goose_db_version ORDER BY version_id,id DESC) latest WHERE is_applied`).Scan(&seed.SchemaVersion); err != nil {
		return err
	}
	if err := work.Err(); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(manifest.BakeStateDir, "ontology-prepared.json"), seed); err != nil {
		return err
	}
	return client.Capture.Emit("ontology_prepared", sealed)
}

func pausePreparation(ctx context.Context, n *Native) error {
	if err := n.Pause(ctx); err != nil {
		return err
	}
	for {
		var status ontology.MaintenanceStatus
		if err := n.Control(ctx, http.MethodGet, "/ontology/status", nil, &status); err != nil {
			return err
		}
		if status.Paused && status.ActiveWork == 0 {
			snapshot, err := n.Snapshot(ctx)
			if err != nil {
				return err
			}
			if !snapshot.Ontology.Paused {
				return errors.New("ontology preparation pause was lost")
			}
			return nativeDrained(snapshot)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

type preparationRetry struct {
	Origin     string   `json:"origin"`
	Key        string   `json:"key"`
	MaxBatches int      `json:"max_batches"`
	Sources    []string `json:"stale_sources"`
	Run        string   `json:"run,omitempty"`
	terminal   bool
}

func retryPreparation(ctx context.Context, n *Native, manifest Manifest, snapshot NativeObservation, path string, retries *[]preparationRetry) (bool, error) {
	if len(*retries) > 0 && !(*retries)[len(*retries)-1].terminal {
		last := &(*retries)[len(*retries)-1]
		if last.Run == "" {
			// A process may have stopped between dispatch and checkpointing its receipt.
			return true, dispatchPreparationRetry(ctx, n, path, *retries)
		}
		state, err := n.retryState(ctx, ontology.MaintenanceRun{ID: last.Run, OperationKey: last.Key})
		if err != nil {
			return false, err
		}
		switch state.Status {
		case "pending", "running":
			return true, nil
		case "completed", "incomplete":
			if state.Status == "incomplete" && state.FailureCode != "provider_unavailable" &&
				state.FailureCode != "stale_input" && state.FailureCode != "maintenance_paused" && state.FailureCode != "window_closed" {
				return false, errors.New("ontology preparation retry has an unsupported outcome")
			}
		default:
			return false, errors.New("ontology preparation retry has no terminal receipt")
		}
		last.terminal = true
		// The retry may have finalized after the snapshot was read.
		return true, n.Client.Capture.Emit("ontology_recovery_finished", state)
	}
	failures, err := n.failedOntologySources(ctx)
	if err != nil {
		return false, err
	}
	if int64(len(failures)) != snapshot.Ontology.Counts.Failed {
		return false, errors.New("native failure count differs from the authenticated scope")
	}
	if len(failures) == 0 {
		return false, nil
	}
	if err := n.Client.Capture.Emit("ontology_failed_sources", map[string]any{"scope": n.Scope, "failures": failures}); err != nil {
		return false, err
	}
	next := preparationRetry{Origin: failures[0].RunID}
	count := 0
	for _, failure := range failures {
		if !failure.recoverable() {
			return false, errors.New("local ontology failure has no supported recovery")
		}
		if failure.Reason == "stale_input" {
			source := failure.SourceKind + ":" + failure.SourceID
			for _, previous := range *retries {
				if previous.Run != "" && previous.Origin != failure.RunID {
					for _, retried := range previous.Sources {
						if source == retried {
							return false, errors.New("ontology source remained stale after recovery")
						}
					}
				}
			}
			if failure.RunID == next.Origin {
				next.Sources = append(next.Sources, source)
			}
		}
		if failure.RunID == next.Origin {
			count++
		}
	}
	next.MaxBatches = min(count, ontology.MaxManualBatches)
	next.Key = uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s:%s:%d", manifest.RunID, next.Origin, len(*retries)))).String()
	*retries = append(*retries, next)
	if err := savePreparationRetries(path, *retries); err != nil {
		return false, err
	}
	return true, dispatchPreparationRetry(ctx, n, path, *retries)
}

func dispatchPreparationRetry(ctx context.Context, n *Native, path string, retries []preparationRetry) error {
	last := &retries[len(retries)-1]
	var receipt ontology.MaintenanceRun
	n.logRecovery("evaluation ontology preparation retry", last.Origin, "operation_key", last.Key, "max_batches", last.MaxBatches)
	if err := n.Control(ctx, http.MethodPost, "/ontology/runs/"+last.Origin+"/retry", map[string]any{"operation_key": last.Key, "max_batches": last.MaxBatches}, &receipt); err != nil {
		return err
	}
	if _, err := uuid.Parse(receipt.ID); err != nil || receipt.Kind != "retry" || receipt.OperationKey != last.Key {
		return errors.New("ontology preparation retry receipt differs")
	}
	last.Run = receipt.ID
	if err := savePreparationRetries(path, retries); err != nil {
		return err
	}
	return n.Client.Capture.Emit("ontology_recovery_started", receipt)
}

func savePreparationRetries(path string, retries []preparationRetry) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".retry-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	writeErr := json.NewEncoder(file).Encode(retries)
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func (n *Native) recordPreparationAmbiguity(ctx context.Context) error {
	tx, err := n.systemTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT source_kind,source_id::text,reason,COALESCE(last_run_id::text,''),COALESCE(assessment_id::text,'') FROM ontology_maintenance_sources
 WHERE team_id=$1::uuid AND shared_space_id=$2::uuid AND space_generation=$3 AND eligible AND status='ambiguous' ORDER BY source_kind,source_id`, n.Scope.TeamID, n.Scope.SpaceID, n.Scope.Generation)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var failure nativeFailure
		if err := rows.Scan(&failure.SourceKind, &failure.SourceID, &failure.Reason, &failure.RunID, &failure.AssessmentID); err != nil {
			return err
		}
		if err := n.Client.Capture.Emit("ontology_preparation_ambiguous", map[string]any{"scope": n.Scope, "source": failure}); err != nil {
			return err
		}
	}
	return errors.Join(errors.New("ontology preparation has unresolved ambiguous sources"), rows.Err())
}
