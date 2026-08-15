package postgres

import (
	"bytes"
	"log"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/stretchr/testify/assert"
)

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
