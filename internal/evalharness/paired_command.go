package evalharness

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type PairCommand struct {
	ConfigPath string
	Action     string
	Side       string
	Stage      string
	Input      string
	Directory  string
	PID        int
	Fresh      bool
	Frozen     bool
}

func RunPairCommand(cmd PairCommand, out io.Writer) error {
	c, err := LoadPairConfig(cmd.ConfigPath)
	if err != nil {
		return err
	}
	if cmd.Action == "sql-import" {
		_, err := fmt.Fprintln(out, PairImportSnapshotSQL)
		return err
	}
	if cmd.Action == "sql-ontology" {
		_, err := fmt.Fprintln(out, PairOntologySnapshotSQL)
		return err
	}
	var result any = map[string]any{"verified": true}
	switch cmd.Action {
	case "runtime-lifecycle":
		if err := CheckPairRuntimeLifecycle(c, cmd.Side); err != nil {
			return err
		}
	case "protocol":
		r, err := pairRelease(c, cmd.Side)
		if err != nil {
			return err
		}
		var response struct {
			Status int    `json:"status"`
			Body   string `json:"body"`
		}
		if err := readPairJSON(cmd.Input, &response); err != nil {
			return err
		}
		if err := VerifyPairProtocol(r, response.Status, []byte(response.Body)); err != nil {
			return err
		}
	case "check-config":
		result = c
	case "complete-import-mapping":
		if err := CompletePairImportMapping(c, cmd.Side, cmd.Directory); err != nil {
			return err
		}
	case "claim-runtime":
		if err := CheckPairRuntimeLifecycle(c, cmd.Side); err != nil {
			return err
		}
		r, err := pairRelease(c, cmd.Side)
		if err != nil {
			return err
		}
		var snapshot PairNativeSnapshot
		if err := readPairJSON(cmd.Input, &snapshot); err != nil {
			return err
		}
		path := filepath.Join(r.Runtime.StateDir, "paired_identity.json")
		hash, err := pairConfigHash(c)
		if err != nil {
			return err
		}
		var saved struct {
			Hash  string `json:"config_hash"`
			RunID string `json:"run_id"`
		}
		fresh := false
		if err := readPairJSON(path, &saved); os.IsNotExist(err) {
			fresh = true
		} else if err != nil {
			return err
		} else if saved.Hash != hash || saved.RunID != c.RunID {
			return fmt.Errorf("prepared runtime belongs to another paired run")
		}
		if _, err := DecodePairObservation(r, snapshot, fresh); err != nil {
			return err
		}
		if fresh {
			saved.Hash, saved.RunID = hash, c.RunID
			if err := writePairJSON(path, saved); err != nil {
				return err
			}
		}
		result = map[string]any{"fresh": fresh}
	case "process":
		result, err = PairProcessIdentity(cmd.PID)
		if err != nil {
			return err
		}
	case "validate":
		for _, binary := range []PairBinary{c.Baseline.Runner, c.Candidate.Runner, c.Scorer} {
			if err := VerifyPairBinary(binary); err != nil {
				return err
			}
		}
		if c.Baseline.CaptureDir != "" {
			if _, err := VerifyPairCaptureProof(c, c.Baseline, c.Baseline.CaptureDir); err != nil {
				return err
			}
		}
		hash, err := pairConfigHash(c)
		if err != nil {
			return err
		}
		identityPath := filepath.Join(c.OutDir, "identity.json")
		var saved struct {
			SchemaVersion string    `json:"schema_version"`
			Hash          string    `json:"config_hash"`
			StartedAt     time.Time `json:"started_at"`
		}
		if err := readPairJSON(identityPath, &saved); err == nil {
			if saved.SchemaVersion != PairSchema || saved.Hash != hash {
				return fmt.Errorf("paired run identity changed")
			}
		} else if os.IsNotExist(err) {
			saved.SchemaVersion, saved.Hash, saved.StartedAt = PairSchema, hash, time.Now().UTC()
			if err := writePairJSON(identityPath, saved); err != nil {
				return err
			}
		} else {
			return err
		}
		result = c
	case "containers", "session", "tools":
		r, err := pairRelease(c, cmd.Side)
		if err != nil {
			return err
		}
		var raw []byte
		if cmd.Input == "-" {
			raw, err = io.ReadAll(io.LimitReader(os.Stdin, 16<<20))
		} else {
			raw, err = os.ReadFile(cmd.Input)
		}
		if err != nil {
			return err
		}
		switch cmd.Action {
		case "containers":
			err = VerifyPairContainers(r, raw)
		case "session":
			err = VerifyPairSession(r, raw)
		case "tools":
			err = verifyPairTools(raw)
		}
		if err != nil {
			return err
		}
	case "observe":
		r, err := pairRelease(c, cmd.Side)
		if err != nil {
			return err
		}
		var native PairNativeSnapshot
		if err := readPairJSON(cmd.Input, &native); err != nil {
			return err
		}
		observation, err := DecodePairObservation(r, native, cmd.Fresh)
		if err != nil {
			return err
		}
		if r.Protocol == "adr0014" && cmd.Stage != "preflight" && cmd.Stage != "capacity" && (observation.WindowID == "" || observation.Policy != r.Capacity || observation.Model != r.Models.Ontology) {
			return fmt.Errorf("required maintenance capacity is not active")
		}
		_, corpus, _, _, _, _, _, err := loadRunInputs(c.SeedManifest, c.Suite)
		if err != nil {
			return err
		}
		progress, err := EvaluatePairImport(r.Scope, corpus, observation.Imports, cmd.Fresh)
		if err != nil {
			return err
		}
		ready, err := EvaluatePairReadiness(r.Protocol, observation, cmd.Frozen)
		if err != nil && !observation.Enabled && cmd.Stage == "preflight" {
			ready = PairReadiness{WaitingFor: []string{"capacity_configuration"}}
			err = nil
		}
		if err != nil {
			return err
		}
		if cmd.Frozen && !ready.Ready {
			return fmt.Errorf("runtime is not frozen: %v", ready.WaitingFor)
		}
		result = struct {
			Observation PairObservation    `json:"observation"`
			Import      PairImportProgress `json:"import"`
			Readiness   PairReadiness      `json:"readiness"`
		}{observation, progress, ready}
	case "begin":
		process, err := PairProcessIdentity(cmd.PID)
		if err != nil {
			return err
		}
		result, err = BeginPairStage(c, cmd.Side, cmd.Stage, process)
		if err != nil {
			return err
		}
	case "finish", "verify", "fail":
		if !pairWithin(c.OutDir, cmd.Directory) {
			return fmt.Errorf("stage must belong to this paired output directory")
		}
		switch cmd.Action {
		case "finish":
			err = FinishPairStage(c, cmd.Directory)
		case "verify":
			err = VerifyPairStage(c, cmd.Directory)
		case "fail":
			err = PairFailure(cmd.Directory, "required_stage_command_failed")
		}
		if err != nil {
			return err
		}
	case "latest":
		dir, err := latestPairStage(c, cmd.Side, cmd.Stage)
		if err != nil {
			return err
		}
		result = PairStageStart{Directory: dir, Reused: true}
	case "capture-proof":
		r, err := pairRelease(c, cmd.Side)
		if err != nil {
			return err
		}
		proof, err := VerifyPairCaptureProof(c, r, cmd.Directory)
		if err != nil {
			return err
		}
		if cmd.Input != "" {
			var now PairObservation
			if err := readPairJSON(cmd.Input, &now); err != nil {
				return err
			}
			ready, err := EvaluatePairReadiness(r.Protocol, now, true)
			if err != nil {
				return err
			}
			if !ready.Ready || now.Scope != proof.After.Scope || now.CanonicalHash != proof.After.CanonicalHash || now.SourceHash != proof.After.SourceHash {
				return fmt.Errorf("captured runtime changed before reuse")
			}
		}
		result = proof
	case "report":
		report, err := WritePairReport(c)
		if err != nil {
			return err
		}
		if err := json.NewEncoder(out).Encode(report); err != nil {
			return err
		}
		if report.Status != "complete" {
			return fmt.Errorf("paired comparison is incomplete; native terminal outcomes are retained in paired_report.json")
		}
		return nil
	default:
		return fmt.Errorf("unknown paired action %q", cmd.Action)
	}
	return json.NewEncoder(out).Encode(result)
}

func pairWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func verifyPairTools(raw []byte) error {
	var response struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("invalid native tools/list response")
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		return fmt.Errorf("native tools/list failed")
	}
	seen := map[string]bool{}
	for _, tool := range response.Result.Tools {
		if seen[tool.Name] {
			return fmt.Errorf("duplicate native tool")
		}
		seen[tool.Name] = true
	}
	for _, name := range []string{"remember", "eval_list_knowledge_refs", "eval_run_recall_case"} {
		if !seen[name] {
			return fmt.Errorf("prepared evaluation runtime is missing %s", name)
		}
	}
	return nil
}
