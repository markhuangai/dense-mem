package evalharness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/google/uuid"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
)

const IntakeProofRowsPerDataset = 20

var priorIntakeRows = map[string][]string{
	"hotpotqa":        {"1338", "3430", "9465", "9797", "9798"},
	"musique":         {"row:3913", "row:4083", "row:5488", "row:5803", "row:9656"},
	"2wikimultihopqa": {"row:941", "row:5775"},
	"nq_rear":         {"2464", "6643", "6839", "8185", "8710"},
	"popqa":           {"19940295", "10787165", "18698113", "8554867", "30826023", "6457075", "17728447", "7567292", "13343698", "row:6645", "24091860"},
}

type intakeProofInput struct {
	SourceID string `json:"source_id"`
	SHA256   string `json:"content_sha256"`
}

type intakeProofInputs struct {
	Revision string                        `json:"source_revision"`
	Registry string                        `json:"registry_sha256"`
	Datasets map[string][]intakeProofInput `json:"datasets"`
}

func selectIntakeProofCorpus(dataset Dataset, corpus []CorpusItem) ([]CorpusItem, []intakeProofInput, error) {
	prior := priorIntakeRows[dataset.ID]
	if len(prior) == 0 || len(corpus) < IntakeProofRowsPerDataset {
		return nil, nil, errors.New("intake proof requires a complete registered corpus")
	}
	missing := make(map[string]bool, len(prior))
	for _, id := range prior {
		missing[dataset.ID+":"+id] = true
	}
	selected := make([]CorpusItem, 0, IntakeProofRowsPerDataset)
	type rankedRow struct {
		item CorpusItem
		hash string
	}
	var remaining []rankedRow
	for _, item := range corpus {
		if missing[item.SourceDocID] {
			selected = append(selected, item)
			delete(missing, item.SourceDocID)
		} else {
			remaining = append(remaining, rankedRow{item, bytesHash([]byte(item.SourceDocID))})
		}
	}
	if len(selected) != len(prior) || len(missing) != 0 {
		return nil, nil, errors.New("previously rejected row is missing from the proof corpus")
	}
	sort.Slice(remaining, func(i, j int) bool {
		if remaining[i].hash == remaining[j].hash {
			return remaining[i].item.SourceDocID < remaining[j].item.SourceDocID
		}
		return remaining[i].hash < remaining[j].hash
	})
	for _, row := range remaining[:IntakeProofRowsPerDataset-len(selected)] {
		selected = append(selected, row.item)
	}
	inputs := make([]intakeProofInput, 0, len(selected))
	for _, row := range selected {
		inputs = append(inputs, intakeProofInput{row.SourceDocID, bytesHash([]byte(row.Content))})
	}
	return selected, inputs, nil
}

func FreezeIntakeProofInputs(registryPath, directory string) error {
	registry, err := ReadRegistry(registryPath)
	if err != nil {
		return err
	}
	hash, err := FileHash(registryPath)
	if err != nil {
		return err
	}
	inputs := intakeProofInputs{Revision: registry.Revision, Registry: hash, Datasets: make(map[string][]intakeProofInput)}
	if len(registry.Datasets) != len(priorIntakeRows) {
		return errors.New("intake proof requires all five registered datasets")
	}
	for _, dataset := range registry.Datasets {
		corpus, _, err := LoadDataset(dataset, directory)
		if err != nil {
			return err
		}
		_, rows, err := selectIntakeProofCorpus(dataset, corpus)
		if err != nil {
			return err
		}
		inputs.Datasets[dataset.ID] = rows
	}
	return writeJSON(filepath.Join(directory, "intake-proof-inputs.json"), inputs)
}

func ReadIntakeProofCorpus(manifest Manifest, revision string, dataset Dataset, corpus []CorpusItem) ([]CorpusItem, error) {
	var frozen intakeProofInputs
	if err := readJSON(filepath.Join(manifest.DataDir, "intake-proof-inputs.json"), &frozen); err != nil {
		return nil, err
	}
	selected, actual, err := selectIntakeProofCorpus(dataset, corpus)
	if err != nil {
		return nil, err
	}
	expected := frozen.Datasets[dataset.ID]
	if frozen.Registry != manifest.RegistryHash || frozen.Revision != revision || len(actual) != len(expected) {
		return nil, errors.New("intake proof source locks differ from frozen inputs")
	}
	for index, row := range actual {
		if row != expected[index] {
			return nil, errors.New("intake proof row differs from frozen input")
		}
	}
	return selected, nil
}

// WriteSeedAdmission derives the server allowlist from the same request builder used by import.
func WriteSeedAdmission(output, scopePath string, corpus []CorpusItem) error {
	var scope Scope
	if err := readJSON(scopePath, &scope); err != nil {
		return err
	}
	manifest := remember.PublicSeedAdmissionManifest{Generation: scope.Generation, Requests: make(map[string]string, len(corpus))}
	for _, identity := range []struct {
		value string
		id    *uuid.UUID
	}{
		{scope.TeamID, &manifest.TeamID}, {scope.CredentialID, &manifest.CredentialID},
		{scope.OwnerID, &manifest.OwnerID}, {scope.SpaceID, &manifest.SpaceID},
	} {
		id, err := uuid.Parse(identity.value)
		if err != nil || id == uuid.Nil {
			return errors.New("public seed admission requires an authenticated bootstrap scope")
		}
		*identity.id = id
	}
	for _, item := range corpus {
		wire, err := json.Marshal(corpusRememberInput(item))
		if err != nil {
			return err
		}
		var request remember.RememberRequest
		if err := json.Unmarshal(wire, &request); err != nil {
			return err
		}
		hash, err := remember.CanonicalRequestHash(request)
		if err != nil {
			return err
		}
		manifest.Requests[request.IdempotencyKey] = hash
		retryKey := request.IdempotencyKey + ":resubmit1"
		if len(retryKey) <= 128 {
			manifest.Requests[retryKey] = hash
		}
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if _, err := remember.ReadPublicSeedAdmission(bytes.NewReader(encoded)); err != nil {
		return err
	}
	if err := os.WriteFile(output, encoded, 0o400); err != nil {
		return err
	}
	// A non-root container user must be able to read the bind mount under a restrictive umask.
	return os.Chmod(output, 0o444)
}

type ordinaryIntakeOutcome struct {
	SourceID string `json:"source_id"`
	Outcome  string `json:"outcome"`
	Reason   string `json:"reason_code,omitempty"`
	Attempts int    `json:"attempts"`
}

func ObserveOrdinaryIntake(ctx context.Context, manifest Manifest, corpus []CorpusItem, client *HTTPClient, native *Native) error {
	if err := native.ResolveScope(ctx); err != nil {
		return err
	}
	outcomes := make(map[string]ordinaryIntakeOutcome, len(corpus))
	observe := func(job corpusImportJob, err error) {
		outcome := ordinaryIntakeOutcome{SourceID: job.item.SourceDocID, Outcome: "accepted", Attempts: job.attempt}
		if err != nil {
			outcome.Outcome = "unavailable"
			var tool *StructuredToolError
			var network *httpNetworkError
			var httpStatus *HTTPStatusError
			switch {
			case errors.As(err, &tool):
				items, _ := tool.Result["errors"].([]any)
				if len(items) > 0 {
					failure, _ := items[0].(map[string]any)
					outcome.Outcome = stringValue(failure["code"])
					outcome.Reason = stringValue(failure["reason_code"])
				}
			case errors.As(err, &network):
				outcome.Reason = network.kind
			case errors.As(err, &httpStatus):
				outcome.Reason = fmt.Sprintf("http_%d", httpStatus.StatusCode)
			}
		}
		outcomes[outcome.SourceID] = outcome
	}
	mapping, runErr := client.importCorpusWithOutcomes(ctx, corpus, manifest.ImportWorkers, native, observe)
	ordered := make([]ordinaryIntakeOutcome, 0, len(outcomes))
	accepted := make([]CorpusItem, 0, len(mapping.BySourceDocID))
	for _, row := range corpus {
		if outcome, ok := outcomes[row.SourceDocID]; ok {
			ordered = append(ordered, outcome)
		}
		if _, ok := mapping.BySourceDocID[row.SourceDocID]; ok {
			accepted = append(accepted, row)
		}
	}
	report := map[string]any{"dataset": manifest.Dataset, "expected": len(corpus), "accepted": len(accepted), "outcomes": ordered}
	if err := writeJSON(filepath.Join(manifest.OutputDir, "ordinary-intake.json"), report); err != nil {
		return errors.Join(runErr, err)
	}
	if runErr != nil {
		return runErr
	}
	if len(ordered) != len(corpus) {
		return errors.New("ordinary intake proof did not record all selected source rows")
	}
	_, err := native.VerifyMappings(ctx, accepted, mapping)
	return err
}
