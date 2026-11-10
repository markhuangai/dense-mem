package postgres

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEvidenceOnlyUnitMigrationSourceSelection(t *testing.T) {
	directory := t.TempDir()
	release := filepath.Join(directory, "v3_0")
	require.NoError(t, os.MkdirAll(release, 0o700))
	body := []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 1;\n")
	require.NoError(t, os.WriteFile(filepath.Join(release, "20261108010000_baseline.sql"), body, 0o600))
	migrator := &Migrator{dir: directory}
	included, err := migrator.IncludesEvidenceOnlyMigration()
	require.NoError(t, err)
	require.False(t, included)
	require.NoError(t, os.WriteFile(filepath.Join(release, evidenceOnlyMigrationSQLName), body, 0o600))
	included, err = migrator.IncludesEvidenceOnlyMigration()
	require.NoError(t, err)
	require.True(t, included)
	_, err = (&Migrator{dir: filepath.Join(directory, "unavailable")}).IncludesEvidenceOnlyMigration()
	require.Error(t, err)
}

func TestLogMigrationStatuses(t *testing.T) {
	var output bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	previousPrefix := log.Prefix()
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
		log.SetPrefix(previousPrefix)
	})

	logMigrationStatuses([]*goose.MigrationStatus{
		{
			Source: &goose.Source{Path: "v2_5/2026010101_first.sql"},
			State:  goose.StatePending,
		},
		{
			Source:    &goose.Source{Path: "v2_4/2026010102_second.sql"},
			State:     goose.StateApplied,
			AppliedAt: time.Date(2026, time.August, 15, 1, 2, 3, 0, time.UTC),
		},
	})

	assert.Contains(t, output.String(), "Pending                  -- 2026010101_first.sql")
	assert.Contains(t, output.String(), "Sat Aug 15 01:02:03 2026 -- 2026010102_second.sql")
}
