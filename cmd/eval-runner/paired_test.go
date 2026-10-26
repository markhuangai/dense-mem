package main

import (
	"flag"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPairedFlagsKeepRuntimeCredentialsExternal(t *testing.T) {
	previous := flag.CommandLine
	flag.CommandLine = flag.NewFlagSet("paired", flag.ContinueOnError)
	t.Cleanup(func() { flag.CommandLine = previous })
	cmd := registerPairFlags()
	require.NoError(t, flag.CommandLine.Parse([]string{"--pair-config", "/run/pair.json", "--pair-action", "observe", "--pair-side", "candidate", "--pair-stage", "capture", "--pair-frozen", "--pair-input", "snapshot.json"}))
	require.True(t, cmd.Frozen)
	require.Equal(t, "capture", cmd.Stage)
	require.Equal(t, "observe", cmd.Action)
	require.Equal(t, "/run/pair.json", cmd.ConfigPath)
	require.Nil(t, flag.CommandLine.Lookup("pair-api-key"))
}
