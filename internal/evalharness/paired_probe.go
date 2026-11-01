package evalharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type PairProbeRow struct {
	SourceDocID       string         `json:"source_doc_id"`
	StartedAt         time.Time      `json:"started_at"`
	FinishedAt        time.Time      `json:"finished_at"`
	ElapsedSeconds    float64        `json:"elapsed_seconds"`
	ClientSubmissions int            `json:"client_submissions"`
	Response          map[string]any `json:"response"`
	Error             string         `json:"error,omitempty"`
}

func VerifyPairProbe(c PairConfig, side, directory string, snapshot PairNativeSnapshot) (PairImportProgress, error) {
	var rows []PairProbeRow
	if err := readPairJSON(filepath.Join(directory, "responses.json"), &rows); err != nil {
		return PairImportProgress{}, err
	}
	r, err := pairRelease(c, side)
	if err != nil {
		return PairImportProgress{}, err
	}
	if r.Protocol == "adr0014" {
		var status struct {
			Enabled bool
			Paused  bool
		}
		if err := json.Unmarshal(snapshot.Ontology, &status); err != nil {
			return PairImportProgress{}, err
		}
		if !status.Enabled || !status.Paused {
			return PairImportProgress{}, fmt.Errorf("qualification requires native ontology enabled and paused before ingestion")
		}
	}
	o, err := DecodePairObservation(r, snapshot, false)
	if err != nil {
		return PairImportProgress{}, err
	}
	if len(rows) != 5 || len(o.Imports) != 5 || !o.SearchConverged {
		return PairImportProgress{}, fmt.Errorf("qualification requires five exact completions and current search")
	}
	_, corpus, _, _, _, _, hash, err := loadRunInputs(c.SeedManifest, c.Suite)
	if err != nil {
		return PairImportProgress{}, err
	}
	if hash != c.SeedHash {
		return PairImportProgress{}, fmt.Errorf("probe seed identity differs")
	}
	items := map[string]CorpusItem{}
	for _, item := range corpus {
		items[item.SourceDocID] = item
	}
	selected := []CorpusItem{}
	seen := map[string]bool{}
	for _, row := range rows {
		item, exists := items[row.SourceDocID]
		if !exists || seen[row.SourceDocID] || row.Error != "" || row.ClientSubmissions != 1 {
			return PairImportProgress{}, fmt.Errorf("qualification client ledger is incomplete or duplicated")
		}
		seen[row.SourceDocID] = true
		matched := false
		for _, attempt := range o.Imports {
			if attempt.SourceDocID == row.SourceDocID && attempt.AttemptID == stringValue(row.Response["submission_id"]) {
				matched = true
			}
		}
		if !matched {
			return PairImportProgress{}, fmt.Errorf("qualification client and durable operation identities differ")
		}
		selected = append(selected, item)
	}
	progress, err := EvaluatePairImport(r.Scope, selected, o.Imports, false)
	if err != nil {
		return progress, err
	}
	if !progress.Complete {
		return progress, fmt.Errorf("qualification imports are incomplete")
	}
	return progress, nil
}

// RunPairProbe submits one source first, then four together, with no client-level retries.
func RunPairProbe(c PairConfig, side string, ids []string, directory string, out io.Writer) error {
	r, err := pairRelease(c, side)
	if err != nil {
		return err
	}
	if len(ids) != 5 || !pairWithin(c.OutDir, directory) {
		return fmt.Errorf("probe requires five source IDs and a new directory inside paired output")
	}
	_, corpus, _, _, _, _, hash, err := loadRunInputs(c.SeedManifest, c.Suite)
	if err != nil {
		return err
	}
	if hash != c.SeedHash {
		return fmt.Errorf("probe seed identity differs from paired manifest")
	}
	items := make(map[string]CorpusItem, len(corpus))
	for _, item := range corpus {
		items[item.SourceDocID] = item
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if _, ok := items[id]; !ok || seen[id] {
			return fmt.Errorf("probe source is missing or duplicated")
		}
		seen[id] = true
	}
	key := os.Getenv(r.Runtime.APIKeyEnv)
	if key == "" {
		return fmt.Errorf("probe runtime credential is unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(directory), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return fmt.Errorf("probe output must be new: %w", err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), c.Deadline)
	defer cancel()
	client := &HTTPClient{BaseURL: r.Runtime.BaseURL, APIKey: key}
	legacy := pairLegacyRelease(r)
	call := func(id string) PairProbeRow {
		item := items[id]
		input := corpusRememberInput(item)
		if legacy {
			delete(input, "target_space")
		}
		started := time.Now()
		row := PairProbeRow{SourceDocID: id, StartedAt: started.UTC(), ClientSubmissions: 1}
		err := client.CallTool(ctx, "remember", input, &row.Response)
		row.ElapsedSeconds, row.FinishedAt = time.Since(started).Seconds(), time.Now().UTC()
		if err != nil {
			var structured *StructuredToolError
			if errors.As(err, &structured) {
				row.Response = structured.Result
			}
			row.Error = err.Error()
		} else if _, err := client.importTerminalRememberResult(item, row.Response, newKnowledgeMapping()); err != nil {
			row.Error = err.Error()
		} else if stringValue(row.Response["submission_id"]) == "" {
			row.Error = "probe response is missing submission_id"
		}
		return row
	}
	rows := []PairProbeRow{call(ids[0])}
	if err := writePairJSON(filepath.Join(directory, "responses.json"), rows); err != nil {
		return err
	}
	if rows[0].Error != "" {
		return fmt.Errorf("serial qualification failed; retained its single submission")
	}
	parallel := make([]PairProbeRow, 4)
	var group sync.WaitGroup
	for i, id := range ids[1:] {
		group.Go(func() { parallel[i] = call(id) })
	}
	group.Wait()
	rows = append(rows, parallel...)
	if err := writePairJSON(filepath.Join(directory, "responses.json"), rows); err != nil {
		return err
	}
	passed := true
	for _, row := range rows {
		passed = passed && row.Error == ""
	}
	if err := json.NewEncoder(out).Encode(map[string]any{"side": side, "client_passed": passed, "client_submissions": len(rows), "durable_verification_required": true}); err != nil {
		return err
	}
	if !passed {
		return fmt.Errorf("parallel qualification failed; retained each single submission")
	}
	return nil
}
