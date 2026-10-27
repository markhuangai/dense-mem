package evalharness

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type PairCaptureIdentity struct {
	Release        string     `json:"release"`
	SourceRevision string     `json:"source_revision"`
	ImageID        string     `json:"image_id"`
	RunnerSHA256   string     `json:"runner_sha256"`
	SeedHash       string     `json:"seed_hash"`
	SuiteSHA256    string     `json:"suite_sha256"`
	Models         PairModels `json:"models"`
	Scope          PairScope  `json:"scope"`
	Protocol       string     `json:"protocol"`
}

type PairCaptureProof struct {
	SchemaVersion string              `json:"schema_version"`
	Identity      PairCaptureIdentity `json:"identity"`
	Before        PairObservation     `json:"before"`
	After         PairObservation     `json:"after"`
	Files         map[string]string   `json:"files"`
}

func capturePairIdentity(c PairConfig, r PairRelease) PairCaptureIdentity {
	return PairCaptureIdentity{Release: r.Release, SourceRevision: r.SourceRevision, ImageID: r.ImageID, RunnerSHA256: r.Runner.SHA256, SeedHash: c.SeedHash, SuiteSHA256: c.SuiteSHA256, Models: r.Models, Scope: r.Scope, Protocol: r.Protocol}
}

func pairCaptureHashes(dir string) (map[string]string, error) {
	files, err := pairArtifactHashes(dir)
	delete(files, "pair_capture.json")
	return files, err
}

func sealPairCapture(c PairConfig, side, dir string) error {
	r, err := pairRelease(c, side)
	if err != nil {
		return err
	}
	proof := PairCaptureProof{SchemaVersion: PairSchema, Identity: capturePairIdentity(c, r)}
	if err := readPairJSON(filepath.Join(dir, "before.json"), &proof.Before); err != nil {
		return err
	}
	if err := readPairJSON(filepath.Join(dir, "observation.json"), &proof.After); err != nil {
		return err
	}
	proof.Files, err = pairCaptureHashes(filepath.Join(dir, "native"))
	if err != nil {
		return err
	}
	return writePairJSON(filepath.Join(dir, "native", "pair_capture.json"), proof)
}

func VerifyPairCaptureProof(c PairConfig, release PairRelease, dir string) (PairCaptureProof, error) {
	var proof PairCaptureProof
	path := filepath.Join(dir, "pair_capture.json")
	if release.CaptureSHA256 != "" {
		hash, err := PairFileHash(path)
		if err != nil {
			return proof, err
		}
		if hash != release.CaptureSHA256 {
			return proof, fmt.Errorf("baseline capture proof hash mismatch")
		}
	}
	if err := readPairJSON(path, &proof); err != nil {
		return proof, err
	}
	if proof.SchemaVersion != PairSchema || proof.Identity != capturePairIdentity(c, release) {
		return proof, fmt.Errorf("baseline capture provenance is incompatible")
	}
	actual, err := pairCaptureHashes(dir)
	if err != nil {
		return proof, err
	}
	a, _ := canonicalJSONHash(actual)
	b, _ := canonicalJSONHash(proof.Files)
	if a != b {
		return proof, fmt.Errorf("capture artifacts differ from their immutable receipt")
	}
	_, corpus, _, _, _, _, _, err := loadRunInputs(c.SeedManifest, c.Suite)
	if err != nil {
		return proof, err
	}
	progress, err := EvaluatePairImport(release.Scope, corpus, proof.After.Imports, false)
	if err != nil {
		return proof, err
	}
	if !progress.Complete {
		return proof, fmt.Errorf("captured import is incomplete")
	}
	for _, o := range []PairObservation{proof.Before, proof.After} {
		if o.Scope != release.Scope {
			return proof, fmt.Errorf("capture observation scope mismatch")
		}
		ready, err := EvaluatePairReadiness(release.Protocol, o, true)
		if err != nil {
			return proof, err
		}
		if !ready.Ready {
			return proof, fmt.Errorf("captured runtime did not satisfy its recorded readiness protocol")
		}
	}
	if proof.Before.SourceHash != proof.After.SourceHash || proof.Before.CanonicalHash != proof.After.CanonicalHash || proof.Before.WindowID != proof.After.WindowID {
		return proof, fmt.Errorf("capture changed source, canonical state or window")
	}
	if err := validatePairLiveMapping(c, dir, proof.After.Imports); err != nil {
		return proof, err
	}
	if err := ValidatePairCapture(c, dir); err != nil {
		return proof, err
	}
	return proof, nil
}

type PairStageTiming struct {
	Side           string     `json:"side"`
	Stage          string     `json:"stage"`
	Attempt        int        `json:"attempt"`
	Status         string     `json:"status"`
	StartedAt      time.Time  `json:"started_at"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	ElapsedSeconds *float64   `json:"elapsed_seconds,omitempty"`
}

type PairSideReport struct {
	Identity        PairCaptureIdentity               `json:"identity"`
	ReusedCapture   bool                              `json:"reused_capture"`
	Progress        PairObservation                   `json:"progress"`
	DistinctSources int                               `json:"distinct_sources"`
	Readiness       PairReadiness                     `json:"readiness"`
	Usage           PairUsage                         `json:"usage"`
	HistoricalUsage PairUsage                         `json:"historical_usage"`
	Attempts        PairAttemptMeasurement            `json:"provider_attempts"`
	Windows         map[string]PairAttemptMeasurement `json:"window_measurements"`
	Scores          Summary                           `json:"scores"`
}

type PairReport struct {
	SchemaVersion  string                    `json:"schema_version"`
	RunID          string                    `json:"run_id"`
	Status         string                    `json:"status"`
	StartedAt      time.Time                 `json:"started_at"`
	FinishedAt     time.Time                 `json:"finished_at"`
	ElapsedSeconds float64                   `json:"elapsed_seconds"`
	Scorer         PairBinary                `json:"scorer"`
	Sides          map[string]PairSideReport `json:"sides"`
	Stages         []PairStageTiming         `json:"stages"`
	Comparison     Comparison                `json:"comparison"`
	Limitations    []string                  `json:"limitations"`
}

func latestPairStage(c PairConfig, side, stage string) (string, error) {
	root := filepath.Join(c.OutDir, side, stage)
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", fmt.Errorf("missing paired %s stage", stage)
	}
	dir := filepath.Join(root, entries[len(entries)-1].Name())
	if err := VerifyPairStage(c, dir); err != nil {
		return "", err
	}
	return dir, nil
}

func WritePairReport(c PairConfig) (PairReport, error) {
	report := PairReport{SchemaVersion: PairSchema, RunID: c.RunID, Status: "complete", FinishedAt: time.Now().UTC(), Scorer: c.Scorer, Sides: map[string]PairSideReport{}, Stages: []PairStageTiming{}, Limitations: []string{
		"Attempt overlap measures admission through reconciliation, not network-only concurrency or model-generation time.",
		"Unknown usage is not zero consumption. Configured capacity does not establish a speedup.",
	}}
	scoreDirs := map[string]string{}
	for _, side := range []string{"baseline", "candidate"} {
		r, _ := pairRelease(c, side)
		capture := r.CaptureDir
		if capture == "" {
			dir, err := latestPairStage(c, side, "capture")
			if err != nil {
				return report, err
			}
			capture = filepath.Join(dir, "native")
		}
		proof, err := VerifyPairCaptureProof(c, r, capture)
		if err != nil {
			return report, err
		}
		score, err := latestPairStage(c, side, "score")
		if err != nil {
			return report, err
		}
		scoreDirs[side] = filepath.Join(score, "native")
		ready, err := EvaluatePairReadiness(r.Protocol, proof.After, true)
		if err != nil {
			return report, err
		}
		if !ready.Complete {
			report.Status = "incomplete"
		}
		row := PairSideReport{Identity: proof.Identity, ReusedCapture: r.CaptureDir != "", DistinctSources: len(proof.After.Imports), Progress: proof.After, Readiness: ready, Usage: proof.After.Usage, HistoricalUsage: proof.After.HistoricalUsage, Windows: map[string]PairAttemptMeasurement{}}
		row.Attempts, err = MeasurePairAttempts(proof.After.ProviderAttempts)
		if err != nil {
			return report, err
		}
		windows := map[string][]PairProviderAttempt{}
		for _, attempt := range proof.After.ProviderAttempts {
			windows[attempt.WindowID] = append(windows[attempt.WindowID], attempt)
		}
		for window, attempts := range windows {
			measurement, err := MeasurePairAttempts(attempts)
			if err != nil {
				return report, err
			}
			row.Windows[window] = measurement
		}
		if err := readJSONFile(filepath.Join(score, "native", "summary.json"), &row.Scores); err != nil {
			return report, err
		}
		report.Sides[side] = row
		if r.Models.BackingRevision == "unknown" {
			report.Limitations = append(report.Limitations, side+": backing model revision is unknown; provider/model-name equality cannot prove identical weights.")
		}
		if r.CaptureDir != "" {
			report.Limitations = append(report.Limitations, side+": original capture reused; its import/build time is not part of this run's elapsed time.")
		}
	}
	var err error
	report.Comparison, err = CompareRunDirs(scoreDirs["baseline"], scoreDirs["candidate"], filepath.Join(c.OutDir, "comparison"))
	if err != nil {
		return report, err
	}
	for _, side := range []string{"baseline", "candidate"} {
		err := filepath.WalkDir(filepath.Join(c.OutDir, side), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || entry.Name() != "intent.json" {
				return nil
			}
			var intent PairStageIntent
			if err := readPairJSON(path, &intent); err != nil {
				return err
			}
			timing := PairStageTiming{Side: side, Stage: intent.Stage, Attempt: intent.Attempt, Status: "interrupted", StartedAt: intent.StartedAt}
			if report.StartedAt.IsZero() || intent.StartedAt.Before(report.StartedAt) {
				report.StartedAt = intent.StartedAt
			}
			var receipt PairStageReceipt
			if err := readPairJSON(filepath.Join(filepath.Dir(path), "receipt.json"), &receipt); err == nil {
				if err := VerifyPairStage(c, filepath.Dir(path)); err != nil {
					return err
				}
				seconds := receipt.FinishedAt.Sub(intent.StartedAt).Seconds()
				timing.Status = "completed"
				timing.FinishedAt = &receipt.FinishedAt
				timing.ElapsedSeconds = &seconds
			} else if !os.IsNotExist(err) {
				return err
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "failure.json")); err == nil {
				timing.Status = "failed"
				report.Status = "incomplete"
			}
			report.Stages = append(report.Stages, timing)
			return nil
		})
		if err != nil {
			return report, err
		}
	}
	sort.Slice(report.Stages, func(i, j int) bool { return report.Stages[i].StartedAt.Before(report.Stages[j].StartedAt) })
	report.ElapsedSeconds = report.FinishedAt.Sub(report.StartedAt).Seconds()
	if err := writePairJSON(filepath.Join(c.OutDir, "paired_report.json"), report); err != nil {
		return report, err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# Paired evaluation %s\n\nStatus: **%s**. Elapsed: %.1f seconds.\n\n| Release | Sources | Cases | Recall@K | Unknown usage attempts | Maximum admitted-attempt overlap |\n|---|---:|---:|---:|---:|---:|\n", c.RunID, report.Status, report.ElapsedSeconds)
	for _, side := range []string{"baseline", "candidate"} {
		r := report.Sides[side]
		fmt.Fprintf(&md, "| %s | %d | %d | %.4f | %d | %d (%s) |\n", r.Identity.Release, r.DistinctSources, r.Scores.ScoredCaseCount, r.Scores.AverageRecallAtK, r.Attempts.UnknownUsage, r.Attempts.MaximumOverlap, r.Attempts.IntervalStatus)
	}
	md.WriteString("\nThe JSON report retains per-axis scores, every stage attempt, terminal outcomes, historical usage and per-window interval validity.\n\n")
	for _, limitation := range report.Limitations {
		fmt.Fprintf(&md, "- %s\n", limitation)
	}
	if err := os.WriteFile(filepath.Join(c.OutDir, "paired_report.md"), []byte(md.String()), 0o600); err != nil {
		return report, err
	}
	return report, nil
}

func PairFailure(dir, reason string) error {
	var intent PairStageIntent
	if err := readPairJSON(filepath.Join(dir, "intent.json"), &intent); err != nil {
		return err
	}
	return writePairJSON(filepath.Join(dir, "failure.json"), map[string]any{"at": time.Now().UTC(), "reason": reason, "intent": intent})
}
