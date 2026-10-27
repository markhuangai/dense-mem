package main

import (
	"flag"

	"github.com/markhuangai/dense-mem/internal/evalharness"
)

func registerPairFlags() *evalharness.PairCommand {
	cmd := &evalharness.PairCommand{}
	flag.StringVar(&cmd.ConfigPath, "pair-config", "", "paired evaluation manifest")
	flag.StringVar(&cmd.Action, "pair-action", "validate", "paired monitor action: validate, containers, session, tools, sql-import, sql-ontology, observe, begin, finish, verify, latest, capture-proof, report, fail")
	flag.StringVar(&cmd.Side, "pair-side", "candidate", "baseline or candidate")
	flag.StringVar(&cmd.Stage, "pair-stage", "", "paired stage name")
	flag.StringVar(&cmd.Input, "pair-input", "", "snapshot file, or - for stdin")
	flag.StringVar(&cmd.Directory, "pair-directory", "", "stage or capture directory")
	flag.IntVar(&cmd.PID, "pair-pid", 0, "owning monitor process ID")
	flag.BoolVar(&cmd.Fresh, "pair-fresh", false, "require an empty prepared runtime")
	flag.BoolVar(&cmd.Frozen, "pair-frozen", false, "require ontology enabled and paused")
	return cmd
}
