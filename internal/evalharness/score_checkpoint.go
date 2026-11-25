package evalharness

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const scoreCheckpointHeader = "verified-seed.json"
const scoreCheckpointFailure = "capture-failure.json"

type scoreCheckpoint struct {
	directory string
	hash      string
}

type checkpointScore struct {
	Seed    string    `json:"verified_seed_sha256"`
	Ordinal int       `json:"question_ordinal"`
	Score   CaseScore `json:"score"`
	Hash    string    `json:"score_sha256"`
}

type checkpointFailure struct {
	Seed    string         `json:"verified_seed_sha256"`
	Failure CaptureFailure `json:"failure"`
}

func checkpointDirectory(outputDirectory string) string {
	return filepath.Join(filepath.Dir(outputDirectory), "score-checkpoint")
}

func newScoreCheckpoint(outputDirectory string, report Report) (*scoreCheckpoint, error) {
	directory := checkpointDirectory(outputDirectory)
	if err := os.Mkdir(directory, 0o700); err != nil {
		return nil, err
	}
	header := filepath.Join(directory, scoreCheckpointHeader)
	if err := writeScoreFile(header, report); err != nil {
		return nil, err
	}
	hash, err := FileHash(header)
	if err != nil {
		return nil, err
	}
	return &scoreCheckpoint{directory: directory, hash: hash}, nil
}

func (c *scoreCheckpoint) save(ordinal int, score CaseScore) error {
	body, err := json.Marshal(score)
	if err != nil {
		return err
	}
	record := checkpointScore{Seed: c.hash, Ordinal: ordinal, Score: score, Hash: bytesHash(body)}
	return writeScoreFile(filepath.Join(c.directory, scoreFilename(ordinal, score.Policy)), record)
}

func (c *scoreCheckpoint) saveFailure(failure CaptureFailure) error {
	return writeScoreFile(filepath.Join(c.directory, scoreCheckpointFailure), checkpointFailure{Seed: c.hash, Failure: failure})
}

func scoreFilename(ordinal int, policy string) string {
	return fmt.Sprintf("%04d-%s.json", ordinal, policy)
}

// RecoverScores publishes an incomplete dataset report from committed numeric
// records when scoring was interrupted before it could write a terminal report.
func RecoverScores(manifest Manifest, corpusCount int, questions []Question) (Report, bool, error) {
	expected := NewReport(manifest, corpusCount, len(questions))
	checkIdentity := func(report Report) bool {
		identity := report.Identity
		identity.CanonicalHash, identity.SeedRelease, identity.SeedPreparationWorkers = "", "", 0
		return identity == expected.Identity && report.CorpusRows == expected.CorpusRows && report.ExpectedAnswers == expected.ExpectedAnswers
	}
	final := filepath.Join(manifest.OutputDir, "scores.json")
	var report Report
	if err := readJSON(final, &report); err == nil {
		if !checkIdentity(report) {
			return Report{}, false, errors.New("terminal evaluation report has a different identity")
		}
		return report, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return Report{}, false, err
	}
	dir := checkpointDirectory(manifest.OutputDir)
	header := filepath.Join(dir, scoreCheckpointHeader)
	if err := readJSON(header, &report); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Report{}, false, nil
		}
		return Report{}, false, err
	}
	canonicalHash, hashErr := hex.DecodeString(report.Identity.CanonicalHash)
	if !checkIdentity(report) || hashErr != nil || len(canonicalHash) != 32 || report.ImportedRows != corpusCount ||
		len(report.Scores) != 0 || report.ModelVerified || report.CaptureComplete || report.Complete {
		return Report{}, false, errors.New("evaluation checkpoint seed verification has a different identity")
	}
	hash, err := FileHash(header)
	if err != nil {
		return Report{}, false, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Report{}, false, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if name == scoreCheckpointHeader || strings.HasPrefix(name, ".scores-") {
			continue
		}
		if !entry.Type().IsRegular() {
			return Report{}, false, errors.New("evaluation checkpoint has an invalid entry")
		}
		if name == scoreCheckpointFailure {
			var receipt checkpointFailure
			if err := readJSON(filepath.Join(dir, name), &receipt); err != nil {
				return Report{}, false, err
			}
			if receipt.Seed != hash || receipt.Failure.Reason == "" || receipt.Failure.Kind == "" {
				return Report{}, false, errors.New("evaluation capture failure checkpoint has a different identity")
			}
			report.CaptureFailure = &receipt.Failure
			continue
		}
		var record checkpointScore
		if err := readJSON(filepath.Join(dir, name), &record); err != nil {
			return Report{}, false, err
		}
		score := record.Score
		body, err := json.Marshal(score)
		if err != nil || record.Hash != bytesHash(body) || record.Seed != hash ||
			record.Ordinal < 0 || record.Ordinal >= len(questions) || score.QuestionID != questions[record.Ordinal].ID ||
			name != scoreFilename(record.Ordinal, score.Policy) {
			return Report{}, false, errors.New("evaluation score checkpoint is corrupt or belongs to a different run")
		}
		report.Scores = append(report.Scores, score)
	}
	report.Failure = "scoring or preservation interrupted before the terminal report"
	report.Limitations = append(report.Limitations, "Recovered numeric checkpoints cover only committed scores. Model verification, terminal capture and server usage are unverified.")
	if err := FinalizeReport(&report); err != nil {
		return Report{}, false, err
	}
	if err := writeScoreFile(final, report); err != nil {
		return Report{}, false, err
	}
	return report, true, nil
}

func writeScoreFile(filename string, value any) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	parent := filepath.Dir(filename)
	temp, err := os.CreateTemp(parent, ".scores-*")
	if err != nil {
		return err
	}
	defer os.Remove(temp.Name())
	_, err = temp.Write(append(body, '\n'))
	if err != nil {
		return errors.Join(err, temp.Close())
	}
	if err := errors.Join(temp.Sync(), temp.Close()); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), filename); err != nil {
		return err
	}
	directory, err := os.Open(parent)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
