package main

import (
	"flag"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvalRuntimeFlags(t *testing.T) {
	previous := flag.CommandLine
	flag.CommandLine = flag.NewFlagSet("runtime", flag.ContinueOnError)
	t.Cleanup(func() { flag.CommandLine = previous })
	cmd := registerRuntimeFlags()
	require.NoError(t, flag.CommandLine.Parse([]string{"--runtime-action", "retire", "--runtime-dir", "/run/old", "--retention-plan", "/run/retain.json", "--stop-timeout", "2s", "--fixture-container", "first", "--fixture-container", "second"}))
	require.Equal(t, "retire", cmd.Action)
	require.Equal(t, "/run/old", cmd.Directory)
	require.Equal(t, "/run/retain.json", cmd.RetentionPlan)
	require.Equal(t, 2*time.Second, cmd.StopTimeout)
	require.Equal(t, []string{"first", "second"}, cmd.Fixtures)
}
